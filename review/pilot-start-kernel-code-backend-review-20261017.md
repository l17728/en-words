# Pilot Start kernel：实现级后端复审

评审对象：`agent/pilot_start.go`、`agent/pilot_start_test.go`、`scripts/pilot_exact.go`、`store/pilot_start_sidecar.go`、`store/pilot_start_sidecar_test.go`，对照 docs/41 §24、44–46、94。当前实现仍未接入 Coordinator/HTTP harness；因此本报告把依赖未实现 supervisor 的要求标为集成门，不将其误报为这些文件已经端到端支持。只读；未修改代码，未运行测试。

## 结论

**BLOCK：2 项 P1、2 项 P2；另有 1 项 Coordinator/harness 集成门。** T-01 固定模板/缺失时 fallback 的核心路径清楚，sidecar 有 HMAC 链、canonical replay 和严格相位校验。但 sidecar reopen 会把缺失文件或空文件当作新链，可能丢失历史失败尝试并允许重放；状态机也拒绝合法的 attempt 2 `terminal_failed`，并未严格把所有终态绑定到当前 commit attempt。错误路径、I/O 故障、进程并发和完整生命周期 HMAC 测试还不足以覆盖设计要求。

## 发现

| ID / 严重度 | 文件行号 | 发现与建议 |
|---|---|---|
| BE-CODE-1 / **P1** | `store/pilot_start_sidecar.go:66-98, 143-221` | `openPilotStartSidecar` 无论是否为首次 case 初始化都会 `O_CREATE`；文件缺失时创建空 sidecar，空文件 replay 也成功返回 `phase=0, seq=0`。若 sidecar 在已有 attempt 后丢失/被截为空，重启会静默把历史当新 case，可能重新执行原 command/Registry。应分离 `CreateNewSidecar` 与 `OpenExistingSidecar`；只有新 case 可创建，restart/recovery 缺失或零长度必须 fail closed。首次创建还需确保父目录项耐久（POSIX parent dir Sync），并以监督器传入的已验证句柄/身份打开。测试增加“已写 attempt 后删除/清零 sidecar，重启拒绝”以及“全新 case 允许空文件”。 |
| BE-CODE-2 / **P1** | `store/pilot_start_sidecar.go:260-292, 304-336`; tests `pilot_start_sidecar_test.go:150-173` | 状态机 attempt 号约束不完整：phase 11（attempt 2 已启动）不接受合法 `terminal_failed`，与 §46“每次 commit 可 terminal_failed”冲突；phase 8/11 的 `terminal_failed` 又不校验 AttemptNo 与当前 attempt，可写出错误编号终态。增加当前 commit attempt 状态并要求 result 的 attempt_no 精确匹配；phase 11 允许合法 attempt 2 terminal failure。补有效/错号负例及 replay 重启断言。 |
| BE-CODE-3 / **P2** | `store/pilot_start_sidecar.go:224-245`; `pilot_start_sidecar_test.go` 全文 | 已知 plan HMAC 的 continuity 对 `terminal_failed` 特判允许缺失；已知 bundle HMAC 只强制 commit-start/committed/unknown/rollback 事件，允许 bundle_ready 后的 terminal_failed 不带 bundle HMAC。此时失败记录与已形成的计划/bundle 脱链。应在已有值时要求所有后继事件（含 terminal_failed）携带相同 HMAC；只有该值尚未产生时才允许 null。增加 tool failure 与 writer rejection 两阶段 terminal HMAC 测试。 |
| BE-CODE-4 / **P2** | `store/pilot_start_sidecar.go:21, 108-145`; tests `pilot_start_sidecar_test.go:51-101` | replay 限制总文件 1 MiB，但 Append 只限单行 8 KiB，不检查写入后总长。达到上限的 append 会返回成功，之后重启却无法 replay 该文件。应在写入前检查当前文件长度+line≤上限，并按协议拒绝/封存，而不是产出无法恢复的成功写。现有测试只做截断/篡改/wrong key，没有大小边界和 I/O 注入。 |
| BE-CODE-5 / 集成门 | `store/pilot_start_sidecar.go:50-64, 66-98, 108-145` | 只有对象内 `sync.Mutex`，无 OS/process-wide append lock、无 OpenVerified handle/ACL 注入。多个进程/对象可各自 replay 到同一 `seq` 并 append 分叉链。文档依赖 supervisor 持 case OS lifecycle lock、一个 active child；当前没有 Coordinator/harness 来建立该前提。集成前必须证明每 case 单 writer 与生命周期锁覆盖 child，或在 sidecar 层增加跨进程锁；并把受保护的文件句柄/身份传入而不是让 Store 仅凭 path reopen。Windows helper 的 ACL 检查目前只由注释委托，`pilotSidecarModePrivate` 对 Windows 恒返回 true（101-105）。 |

## 已通过项

- `agent/pilot_start.go:23-43` 只对 `lesson_started`、正词数、规范昵称渲染 OP-1；失败时用固定 fallback，并验证类别、来源、模板 ID、老师自称、昵称、60 rune 上限及无残留花括号。`scripts/pilot_exact.go:7-21` 按给定类别/ID 顺序查模板，不走随机口语化；正向 golden 和缺模板 fallback 测试位于 `agent/pilot_start_test.go:11-31`。
- `store/pilot_start_sidecar.go:143-220` replay 对大小/行长、换行完整性、UTF-8/JSON、重复键/未知字段、canonical 编码、字段 schema、序号、迁移、前序 HMAC 及 HMAC-SHA256 做校验；坏链拒绝，不截断修复。
- `validatePilotStartEvent` 和 `nextPilotStartPhase` 覆盖固定 say→show_word 次序、最多两次 commit 与一次 rollback retry；已有 golden/replay、篡改/截断/wrong key、乱序/跨 attempt、commit retry 边界测试。

## 测试缺口与接入验收门

1. 补 sidecar 全生命周期有效 golden：plan_ready、say/show_word started/result、bundle_ready、commit 1 rollback、commit 2 outcome；核对每类事件 canonical bytes/HMAC golden 与 `event_hmac` 必填/null 语义。当前 only attempt_started golden（test:13-49），文档 §45 要求 plan/bundle/result_class golden。
2. 用注入式 writer/file abstraction 覆盖 partial Write、0-byte write、Sync 错误、父目录 Sync 错误；每种失败后同对象不得再 Append，重启应完整链恢复或 fail closed，不得产生可重放的空新链。
3. 覆盖 bundle_ready 后终态、attempt 2 terminal_failed、commit result 错 attempt_no、终态后 append、plan/bundle HMAC 缺失/替换；测试应对真实 Append + reopen/replay 而不只直接调用相位函数。
4. 覆盖 Unicode/控制/格式字符、昵称边界（空值已由 Store 映射“同学”、12 rune 截断、超长、括号清理）、OP-1 错 category/残留占位符/超长输出及准确 fallback。现有 agent tests 只覆盖固定正常值、缺 Bank 和 4 个无效输入（`pilot_start_test.go:11-45`）。
5. 接入后验证 single-writer/OS lifecycle lock、OpenVerified sidecar ACL/identity、key 生命周期、receipt-first、COMMIT unknown 和真实多进程 restart；当前五个文件没有 Coordinator/API/E2E 证明这些 contracts。

当前 JSONL 1 MiB cap 是实现常量，docs/41 未规定该上限或超过后的生命周期策略；需在规格中补充容量/封存策略，或移除无法自洽的固定上限。

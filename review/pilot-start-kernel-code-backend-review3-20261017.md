# Pilot Start kernel：实现级后端复审 3

评审对象：`server/internal/agent/pilot_start.go`、`pilot_start_test.go`、`server/internal/scripts/pilot_exact.go`、`server/internal/store/pilot_start_sidecar.go`、`pilot_start_sidecar_test.go`。对照 docs/41 §24、44–46、94、108。只读评审；本轮未运行测试或修改产品代码。记录的最终定向验证日志显示 pilot-tag agent/scripts/store 测试均通过。Coordinator/HTTP harness 尚未接入；跨进程锁、OpenVerified、ACL 和 Windows 目录耐久仍是集成门。

## 结论

**BLOCK（P2：耐久 I/O 故障路径缺少锁定测试）；BE-CODE-1..4 当前报告中的实现/测试问题已关闭。** create/reopen 分离、missing/empty fail-closed、attempt 1 rollback 后 attempt 2 terminal failure、已知 plan/bundle HMAC 绑定及追加前容量拒绝均与 §108 一致。新 full-chain 用例覆盖 attempt1 rollback → attempt2 terminal_failed 并在 reopen 后校验恢复状态；容量用例改为合法 `attempt_started`，可到达容量保护分支。

## 复核结果

| 项目 | 状态 | 依据与建议 |
|---|---|---|
| BE-CODE-1：create/reopen 与 missing/empty | PASS（平台集成门） | `pilot_start_sidecar.go:67-119` 仅 create 使用 `O_CREATE|O_EXCL`；reopen 不创建，缺失及零字节文件均拒绝。POSIX create 后同步父目录；Windows ACL/目录耐久明确委托监督器。`pilot_start_sidecar_test.go:176-193` 覆盖 missing/empty。 |
| BE-CODE-2：commit attempt 状态迁移 | PASS | `pilot_start_sidecar.go:346-377` 限制 attempt 编号并允许 attempt 2 `terminal_failed`。`pilot_start_sidecar_test.go:216-265` 用合法完整事件链写入、关闭、重开，并核对 terminal phase、sequence 与 plan/bundle HMAC；另有迁移单测。 |
| BE-CODE-3：plan/bundle HMAC 连续性 | PASS | `pilot_start_sidecar.go:271-285` 在值已知后要求后续事件提供相同 HMAC。`pilot_start_sidecar_test.go:195-214` 覆盖终态缺失及一致值。完整链也覆盖写入和 replay。 |
| BE-CODE-4：总容量保护 | PASS（容量测试使用注入限值） | `pilot_start_sidecar.go:171-183` 在写入前核对现有长度与新行长度，超限或 stat 失败即 degrade，未写文件。更新后的 `pilot_start_sidecar_test.go:267-294` 使用字段合法的 `attempt_started`，把测试限值设为 0，断言错误、文件长度不变、degraded。该用例现能命中保护分支；未实际填充 1 MiB 边界。 |
| Agent 固定开场与模板渲染 | PASS | `agent/pilot_start.go:23-55` 校验事件、词数和已规范化昵称，固定 OP-1 纯渲染并提供受约束 fallback；`scripts/pilot_exact.go:7-22` 不调用随机变体/口语化。相应用例覆盖模板、fallback 和不可信输入拒绝。 |
| 同步/写入失败恢复 | **BLOCK / P2** | Append 对短写、Write 错误和 `Sync` 错误会标记 degraded（`sidecar.go:184-193`），创建时父目录 Sync 失败会关闭并报错（`sidecar.go:102-109`）；但当前测试没有故障注入覆盖这些分支，也没有验证失败后旧链/重开状态以及监督器对不确定持久化的处置。按 §94 的 `sidecar Sync failure` 验收要求，补可注入文件/同步接口，覆盖写零字节、部分写、文件 Sync、父目录 Sync 失败；证明不得继续 append 或静默重建链。 |
| OpenVerified、OS 生命周期锁、ACL | 未接入门，不作为本 slice 通过 | helper 仍通过 path `Lstat`/`OpenFile` 打开；Windows 私有权限检测由监督器负责；对象内 mutex 不能替代文档要求的跨进程 case 锁。harness 接入前必须统一生命周期锁、二次 catalog 授权、已验证路径/ACL 和句柄关闭协议。 |

## 范围说明

本轮没有发现 BE-CODE-1..4 的新矛盾。§108 关于 1 MiB 上限、写前拒绝、sidecar degrade、不得截断/轮转及 recovery-only-existing 的合同与代码一致；Windows durability/ACL 仍明确是监督器集成责任。此结论不代表整个试点内核或 §94 已验收：Coordinator/HTTP harness、跨进程锁、OpenVerified、Windows ACL/目录耐久与 cleanup 尚未在当前五文件内实现或验证。

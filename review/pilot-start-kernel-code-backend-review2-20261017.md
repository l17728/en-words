# Pilot Start kernel：实现级后端复审 2

评审对象：指定 5 个 pilot_e2e 文件，对照 docs/41 §24、44–46、94 及新 §114。只读；未修改代码，未运行测试。Coordinator/HTTP harness 仍未接入，OpenVerified、OS lifecycle lock、Windows ACL/目录耐久仍是集成门。

## 结论

**BLOCK：1 项 P2 测试缺陷；BE-CODE-1..3 的实现缺陷已关闭，BE-CODE-4 的实现已关闭但对应测试无效。** create/reopen 已分离，恢复时拒绝 missing/empty；状态机已检查 commit attempt 编号并允许 attempt 2 terminal failure；plan/bundle HMAC 已在已知后要求后继事件沿用；Append 已在写入前检查 1 MiB 上限并隔离 case。新增容量测试使用空的无效事件，未覆盖尺寸分支，而且断言 `degraded` 会失败。

## 复核结果

| 项目 | 状态 | 依据与建议 |
|---|---|---|
| BE-CODE-1：create/reopen 和父目录耐久 | PASS（平台集成门） | `pilot_start_sidecar.go:66-119` 分为 `createPilotStartSidecar` 与 `openPilotStartSidecar`；recovery 不带 CREATE，缺文件时报错，existing zero-length 明确拒绝。新文件 `Sync` 后 POSIX 同步父目录（129-142）。Windows 等价目录持久化与 ACL 由 supervisor 集成提供，§114 已标为门。现有测试验证 missing/empty（sidecar_test.go:176-193）；建议再覆盖“曾有效写入后被清零/删除”，但逻辑现已拒绝。 |
| BE-CODE-2：commit attempt 迁移 | PASS | `nextPilotStartPhase` 对 attempt 1/2 的 started、committed/unknown、rollback 与 terminal failure 分别匹配 exact attempt number（sidecar.go:340-373）；attempt 2 terminal failure 已允许。对应转移表包含该路径（test.go:150-174）。仍建议将 attempt 号错配放进表格及真实 Append/replay 用例。 |
| BE-CODE-3：已知 HMAC continuity | PASS | `validateContinuity` 在 plan/bundle HMAC 已确定后拒绝省略或替换，覆盖终态（sidecar.go:265-281）。测试直接断言 plan/bundle terminal HMAC 缺失拒绝、相等接受（test.go:195-214）。 |
| BE-CODE-4：1 MiB replay cap | 实现 PASS；测试 **BLOCK / P2** | Append 在写前 stat 并检查现有长度+新行不会超过 cap；失败置 degraded、不写入（sidecar.go:171-189）。但新增测试 `TestPilotStartSidecarRejectsAppendBeyondReplayLimit` 把 `pilotStartEvent{}` 传给 Append（test.go:216-227）；该事件先在 schema/phase 校验处返回，根本不会到达长度检查或设置 `degraded`。随后 `if !s.degraded` 必然失败。测试应先构造合法、且按当前 phase 可接受的事件，再达到 cap；若补齐后剩余空间不足一条，则测试“不写、degraded、长度不变”。 |
| 单写者、OpenVerified 与 ACL | 未接入门，不作当前 PASS | `pilotStartSidecar` 仍只有对象内 `sync.Mutex`（sidecar.go:50-64）；多个进程/多个对象可能各自从旧 seq/prev append。`openPilotStartSidecar` 仍按 path `Lstat` 后 `OpenFile`，Windows ACL helper 也明确恒放行、委托 supervisor（84-126）。在 coordinator/harness 尚未接入时不能声称满足 §44 的 case-level OS lifecycle lock/verified handle。接入前必须由 supervisor 保证单 case 单活动 writer 并在受保护 handle/ACL 检查后调用。 |

## 仍需补的验收

1. 修正 1 MiB 测试，使合法事件确实进入 cap 检查；不运行测试的本轮只能静态确认当前断言走不到目标分支。
2. 增加 event type 与 result_class 的组合约束，以及 terminal event 与 attempt_no 的 Append→reopen/replay 全链路负例；当前 retry 测试大多直接调用 `nextPilotStartPhase`，未覆盖完整持久链。
3. 增加 write partial、0-byte write、file Sync 错误、创建时父目录 Sync 错误注入；确认 degraded 与重启恢复状态不允许重放。
4. 加入 plan/bundle 和各 result_class canonical HMAC golden；目前有 attempt_started golden，但没有覆盖 §45 声明的全生命周期 golden。
5. Coordinator/harness 接入时验证 cross-process 单写者锁、OpenVerified、Windows ACL、父目录 durability 和 cleanup 状态门。当前报告不把这些未实现组件当成已满足。

# Pilot Start kernel：实现级后端复审 4

评审对象：指定的五个 `pilot_e2e` 文件；只读，未运行测试或修改代码。参考 docs/41 §§24、44–46、94、108。现有最终验证摘要记录 production tests/vet 与 pilot-tag agent/scripts/store tests 通过；这不替代本轮状态机静态复核。Coordinator/HTTP harness、跨进程锁、OpenVerified、Windows ACL/目录耐久仍是集成门。

## 结论

**BLOCK：1 项 P1 状态机/结果枚举缺陷。** review3 的 I/O 故障测试门已经补上：零字节写、部分写、file Sync 与 parent-dir Sync 均有注入用例；非 `ok` 工具结果也不再允许继续执行后续工具。但 `terminal_failed` 仍可被写在尚无 `tool_result` 的 `tool_started` 阶段，也能携带 `commit_unknown` 结果码。该组合会把协议明确要求保持 `pending_unknown` 的状态持久化为 terminal failure，恢复方可能误判为 `not_committed`。

## 复核结果

| 项目 | 状态 | 依据 |
|---|---|---|
| review3 I/O 故障门 | PASS（当前文件层） | `pilot_start_sidecar_test.go:541-580` 覆盖 zero/partial write 与 file Sync 失败，断言 degraded、序号不前进且同 writer 重试拒绝；`:582-598` 覆盖 parent Sync 失败、构造失败和空文件恢复拒绝。实现将不确定写入 fail closed。监督器对跨重启 pending/degraded 的处置仍需集成验证。 |
| 非 ok tool_result 禁止下一工具 | PASS（迁移函数层） | `pilot_start_sidecar.go:367-376` 将 say/show_word 的非 ok 结果分别转到 phase 13/14；`:391` 允许其写 terminal failure。测试 `pilot_start_sidecar_test.go:201-228` 锁定 say 失败后不能启动 show_word。建议再加 Append→close→reopen 的失败链用例，但不是本轮阻断。 |
| terminal failure 的合法来源与结果码 | **BLOCK / P1** | `pilot_start_sidecar.go:340-341` 对 `terminal_failed` 只要求 result_class 在总枚举中且不为 `ok`，因此 `commit_unknown`、`rollback_proven` 等互斥/阶段专用码也被接受。`nextPilotStartPhase` 的 `:391` 允许 phase 3/5（耐久 `tool_started`、无 `tool_result`）直接转 terminal；这些阶段的副作用结果在协议中属未知，必须恢复为 `pending_unknown`，不能转 `not_committed`。 |
| BE-CODE-1..4 旧闭环 | PASS | create/reopen、missing/empty 拒绝、完整 rollback retry replay、HMAC 连续性及写前容量检查仍保持 review3 的结论。容量测试用合法事件触发注入限值，未实际填满 1 MiB。 |

## 必须修订

1. 把终态迁移收紧为显式阶段集合：`terminal_failed` 不得从 phase 3/5（tool_started 但无 tool_result）落盘；此时只能保持未决/隔离。工具结果已耐久失败后（phase 13/14）才可转终态。
2. 对 `terminal_failed.result_class` 使用专用枚举/阶段映射，至少拒绝 `commit_unknown`、`rollback_proven` 和 `ok`；unknown 只能写 `commit_unknown` 事件并保持 pending，rollback 只能写 `rollback_proven` 事件。
3. 补负例测试：从 phase 3/5 直接 Append terminal_failed 必须拒绝；在 commit attempt 阶段用 `terminal_failed + commit_unknown` 必须拒绝；合法失败链需 Append、关闭并 reopen，验证恢复 phase 与 outcome 不会把 unknown 误认成 not-committed。

OpenVerified、OS lifecycle lock、ACL、cleanup、DB receipt 优先恢复及 Coordinator 行为不在这五个文件内实现，仍属未关闭集成门。

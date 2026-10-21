# Pilot Start kernel：实现级后端复审 6

评审对象仍为五个 `pilot_e2e` 实现/测试文件，重点复核 replay 末尾 Seek 错误归一和故障分支测试；对照 docs/41 §§24、44–46、94、108。只读，未运行测试或修改代码。按任务说明，最新 r12 定向三包测试全绿；报告的 replay/continuity/schema/phase 覆盖率分别为 98.3%/100%/94.4%/100%。Coordinator/HTTP harness、跨进程锁、OpenVerified、Windows ACL/目录耐久仍是集成门。

## 结论

**PASS：未发现新增代码阻断。** replay 开头/末尾 seek、读、stat、记录校验、phase/continuity 错误均归一为 sidecar integrity error。新增故障用例覆盖这些 replay 失败分支；r5 状态机修复及 I/O 故障用例保持有效。

## 复核结果

| 项目 | 状态 | 依据 |
|---|---|---|
| replay 末尾 Seek 失败 | PASS | `pilot_start_sidecar.go:280-283` 检查 `Seek(0, io.SeekEnd)` 并返回统一的 `errPilotStartSidecar`，不把底层平台错误泄漏为不同恢复类别。 |
| replay 故障分支测试 | PASS | `pilot_start_sidecar_test.go:68-164` 注入 seek-start、read、stat、seek-end、event-validation、phase-transition、continuity 故障，并逐项要求 `errPilotStartSidecar`。其中 seek-end 用第二次 Seek 调用触发，正好覆盖本次改动。 |
| R4 状态机修复 | PASS | `pilot_start_sidecar.go:340-341,391-395` 限制 terminal_failed 结果码及合法来源；`pilot_start_sidecar_test.go:345-366,384-392,569-602` 覆盖拒绝未决工具结果、合法失败结果的 close/reopen 恢复和非法结果码。 |
| 文件/目录耐久故障 | PASS（文件层） | `pilot_start_sidecar_test.go:541-598` 仍覆盖 zero/partial write、file Sync 与 parent Sync 故障。 |
| replay/FSM 覆盖情况 | 记录通过 | 按任务提供的 r12 报告：三包定向测试全绿；replay 98.3%、continuity 100%、schema 94.4%、phase 100%。未在本轮独立重跑。 |

## 集成门

此 PASS 限于五个隔离实现文件。Coordinator/HTTP harness、跨进程 case 生命周期锁、catalog 双阶段授权、OpenVerified、Windows ACL/目录 Sync、SQLite receipt-first 恢复与 cleanup 仍须集成实现和双平台验收。

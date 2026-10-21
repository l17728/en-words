# Pilot Start kernel：实现级后端复审 5

评审对象：`server/internal/agent/pilot_start.go`、`pilot_start_test.go`、`server/internal/scripts/pilot_exact.go`、`server/internal/store/pilot_start_sidecar.go`、`pilot_start_sidecar_test.go`。对照 docs/41 §§24、44–46、94、108。只读，未运行测试或修改代码。现有最终验证摘要记录 production 与 pilot-tag 测试通过；此报告仅给当前五文件的静态复核结论。Coordinator/HTTP harness、跨进程 OS 锁、OpenVerified、Windows ACL/目录耐久仍是集成门。

## 结论

**PASS（本轮先前 P1 已关闭；无新增阻断）。** phase 3/5 的 `tool_started` 未收到结果时不能直接写 terminal failure；已知非 ok `tool_result` 则转到独立失败相位，后续工具和 bundle 路径被禁止。`terminal_failed` 也拒绝 `ok`、`commit_unknown`、`rollback_proven`，未知提交不能被伪装成明确失败。新回归覆盖未决负例及已知工具失败的 append/close/reopen。

## 定向复核

| 项目 | 状态 | 依据 |
|---|---|---|
| 未决工具调用不得终结 | PASS | `pilot_start_sidecar.go:391-392` 仅允许 phase 1、2、4、6、7、13、14 进入 attempt 0 的 terminal failure，排除 phase 3/5。测试 `pilot_start_sidecar_test.go:384-392` 对这两个拒绝转移作负向锁定；`:414-439` 验证 say 已 started 时 terminal_failed 被拒绝，收到耐久 handler_error 结果后才能终结，并在 reopen 后恢复 terminal phase。 |
| terminal_failed 结果码与 commit outcome | PASS | `pilot_start_sidecar.go:340-341` 要求 terminal failure 的结果码既合法又不是 `ok`、`commit_unknown` 或 `rollback_proven`。`pilot_start_sidecar_test.go:345-366` 对三种冲突码作拒绝断言；commit_unknown 和 rollback_proven 各自保留专用事件类型。 |
| 非 ok 工具结果关闭执行路径 | PASS | `pilot_start_sidecar.go:367-376` 把工具失败转为 phase 13/14；之后只能终结，不能启动第二工具或生成 bundle。`pilot_start_sidecar_test.go:201-228` 和 phase 表覆盖主要转移。 |
| 文件与目录耐久故障 | PASS（文件层） | `pilot_start_sidecar_test.go:541-580` 注入零写、部分写和文件 Sync 失败，并断言 degraded、seq 不变及同 writer 重试拒绝；`:582-598` 注入 parent Sync 失败并验证创建拒绝、空文件不能作为恢复链。Windows ACL/目录耐久由监督器集成实现，尚未在这五个文件验证。 |
| BE-CODE-1..4 | PASS | 创建/reopen 分离与 missing/empty 拒绝、attempt1 rollback 后 attempt2 terminal failure 完整 replay、plan/bundle HMAC 连续性及写前容量保护仍与前轮一致。容量测试通过注入较小限值触发分支，不是实际填充到 1 MiB 的边界测试。 |

## 未覆盖范围

当前 PASS 不代表整个试点可投用。Coordinator/HTTP harness、跨进程 lifecycle 锁、catalog 双阶段授权、OpenVerified、Windows ACL/父目录 Sync、receipt-first SQLite 恢复与 cleanup 尚未在这些文件内实现或验证。正式接入前仍需按 docs/41 对这些集成门进行双平台和多进程验收。

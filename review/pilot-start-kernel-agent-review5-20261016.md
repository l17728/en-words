# Agent 域复审：Start Kernel SDD（第 5 轮）

- 评审文档：`docs/41-试点课堂Start持久迁移内核SDD.md`
- 范围：T-01 精确话术与 fallback、Planner Prepare / Registry Execute、sidecar 同步的前后顺序、Agent 输出与 fingerprint 绑定时点、稳定输出与禁止重规划测试。
- 类型：只读设计复审；未修改代码或文档，未运行测试。
- 结论：**BLOCK**。T-01 Agent API 与两段 Planner 接口已变得可实现，预算/重试也有较完整边界；但 Prepare 输出和已执行工具事件的绑定晚于执行，sidecar 对失败调用的身份/作用域审计也不够，崩溃窗口的测试没有覆盖。

## 已通过

1. **T-01话术由Agent API负责。** §3 规定 `TeacherAgent.RenderLessonStartT01` 固定调用 `Bank.RenderExactID("OP-1", vars)`，adapter 不得直接访问 Bank 或自行拼文案；方法内负责 fallback，类别、模板 ID、source 和长度均有验收值。准确开场黄金文本与昵称/词数输入明确，设计符合 docs/04 T-01。
2. **Prepare 与 Execute 分工正确。** Prepare 只做本地确定性决策/渲染；Execute 才调用两个 Registry 工具。`attempt_started` Sync 失败会阻止后续 Agent/工具调用；正常工具结果写 sidecar 后才形成 bundle；sidecar 和工具不持有 SQLite writer。
3. **成功后输出有绑定。** 最终 `bundle_hmac` 将 `command_hmac`、最终命令 fingerprint、bundle digest 绑定；`bundle_ready` 持久化后才进入数据库 writer。相同 command/task 回滚复用 bundle，未知提交后禁止重新规划/执行工具。
4. **重启/稳定输出有正向合同。** OP-1/fallback 用例锁定 template/source/text；same command 失败或未决在 sidecar tombstone/事件链存在时不重建 task，重启复用同 DB/sidecar/key。

## 阻断项

### AGENT-START-12 — Agent 输出只在工具已执行后才进入 sidecar fingerprint（P1）

位置：§3 规划分段与 fingerprint、§3 attempt 事件迁移、§6 sidecar 矩阵。

`planning_fingerprint` 有意不含 Agent 输出；Prepare 生成文案后，Execute 随即调用 Registry；最终 Agent say 和 show_word 才通过 `bundle_hmac` 在 `bundle_ready` 事件绑定。但 `bundle_ready` 晚于 say/show_word 的 `tool_result`。若 `classroom.say` 成功、`classroom.show_word` 失败，或在工具结果 Sync 后进程崩溃，sidecar 没有 `bundle_ready`，现存工具事件只绑定原 planning inputs，不能证明实际执行的 `classroom.say` 是哪条 Agent 输出。若 Registry 调用返回后、对应 `tool_result` 完成 Flush+Sync 前崩溃，则连该次调用结果也没有耐久记录。恢复虽 fail closed、不重规划，但这只能防重复，不能形成 D-15 所需的可审计 Agent→工具决策链。

**关闭条件：**Prepare 完成后、任何 Registry 调用前，先生成最终 canonical command fingerprint（含精确 T-01 文案及冻结 show_word），追加并 Sync 一个 `plan_ready`/等价事件，含最终 fingerprint 的 HMAC、模板 ID/source、两项 effect 绑定；之后每个工具事件都必须引用该绑定。每次工具执行还应先持久记录具体 `tool_started`，再调用 Registry，返回后持久化 `tool_result`；如果 handler 已返回但结果 Sync 未成功，恢复标记为 unknown 并禁止重执行。sidecar 只保存 HMAC/固定类别、不保存原始文案/词面/args。对 Prepare 完成、say 前、say 返回至结果 Sync、show_word 返回至结果 Sync、bundle_ready Sync 等边界逐一做故障注入，断言无未绑定的 Registry 调用、恢复不再规划，生命周期表没有部分写入。

### AGENT-START-13 — 失败 sidecar 记录不足以证明受信 task/scope（P1）

位置：§3 sidecar schema、§3 “失败工具尝试审计与成功生命周期审计分账”。

失败 sidecar 明确“不存 Actor、TraceID、risk、原始Args或 task ID”，事件字段只有 HMAC、attempt 序号、工具名、result_class 和时间。成功路径的 DB `audit_logs` 能验证 actor/risk/role；失败路径没有成功 receipt，也没有包含这些固定身份/授权语义的可查询记录。仅用 command HMAC 不能让运维/审计者确认这是受信 `teacher_agent` / `classroom_event` task 在 `teacher_agent` scope、低风险工具上的调用，还是某个不同授权上下文；这使 D-15 的 scope + audit 在失败路径不完整。

**关闭条件：**sidecar 增加不含用户标识的固定/受保护审计维度，例如 `actor_role=teacher_agent`、`trigger=classroom_event`、`scope=teacher_agent`、`risk=low`，以及由受信 capability 派生的 `task_hmac/trace_hmac`；记录 schema/budget/handler 失败类别和工具调用序号。明确这些字段由 Store 已验证的 task capability 与 Registry AuditEntry 生成，Planner/HTTP 不得自报。用字段篡改、错 role/scope、非允许工具和跨 command HMAC 关联测试验证 sidecar fail closed。保持不记录原始参数、昵称、文本、panic/stack。

## 测试矩阵补充要求

现有 §6 覆盖精确 OP-1/fallback、一般失败、sidecar Sync 失败、重启不重调工具；但尚未锁定 Agent 输出先于 Execute 被 fingerprint 绑定，也未在“工具返回但 result Sync 未完成”的崩溃点证明事件链和 no-replan。AGENT-START-12/13 的用例关闭后，Agent 域才能签字。

本审查只对 Start-only isolated slice 有效；不批准产品入口、outbox dispatch、模型/语音接入或真人课堂试点。

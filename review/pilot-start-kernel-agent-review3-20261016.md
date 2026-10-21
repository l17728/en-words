# Agent / 教学策略域复审：Start Kernel SDD（第 3 轮）

- 文档：`docs/41-试点课堂Start持久迁移内核SDD.md`，对照 `docs/04-Agent与话术库设计.md` 及当前 `TeacherAgent`、`scripts.Bank`、`tools.Registry` 结构。
- 评审类型：只读设计复审；未修改产品代码，未运行测试。
- 结论：**BLOCK（一个 P1 审计缺口，另有 P2 实现合同建议）**。

## 第二轮问题复核

| 问题 | 状态 | 判断 |
|---|---|---|
| `RenderLessonStartT01` 可实现性与话术边界 | 已关闭（设计合同） | SDD 现在明确新增 Agent 层方法、固定 OP-1、禁用口语化/称呼变体、固定 fallback、输出 source/templateID、黄金文本和长度/事实断言。adapter 不直接访问 Bank 或拼话术。虽然当前代码尚无该新方法，但 SDD 已把它作为实现接口变更，符合设计先行要求。 |
| rollback 同 ID 重试的 Registry 次数与预算 | 已关闭（同一 task 生命周期内） | bundle 在 Registry 两次有序调用后一次生成；rollback 后复用同一 bundle 与审计，不重建 task/context、不重调 Registry、不刷新 budget。预算=2 对这个路径闭合：两次工具调用仍只发生一次，重试只重做存储，不重复教学动作/Agent tool。若 commit 结果未知，既有规则仍保持 `pending_unknown`，不会生成新 bundle。 |

## 阻断项

### AGENT-START-08 — Registry 失败/panic 时 task-local 审计被丢弃（P1）

位置：docs/41 §3 中 `Registry失败或panic且未形成完整bundle时，丢弃审计buffer并终态失败`；质量/审计合同在 §4、§6、日志合同处。

`tools.Registry.Call` 对 schema、scope、budget、handler 等普通返回都会调用 auditor（当前包见 `server/internal/tools/registry.go`）；SDD 也规定 auditor 是本片唯一 Registry 审计来源。若 say 成功、show_word 失败，或任一调用被拒绝/handler panic，当前要求丢弃整个 task-local buffer，数据库生命周期事务也不提交；结果是已经发生的 tool 调用尝试没有持久审计/可检索安全事件。后续以同 command 终态失败，故没有成功 receipt 可供 lifecycle verifier 关联。将它称为“终态失败”并不能满足 D-15 对 tool scope/schema/audit 的要求，也使无效 task/工具滥用无法复盘。

**关闭条件：**为未形成 bundle 的失败尝试规定无原文、无凭据的持久审计路径，并说明它如何避免成为 `lesson.start` 成功 receipt 的孤儿 lifecycle audit。可选择独立的 harness/security attempt log，或为失败调用单独定义受限审计事件；必须保留 Registry 给出的 tool、风险、actor/task、trace、stable result code、schema/budget/handler failure class，不保存 raw args、panic 内容或 stack。区分“工具调用已审计但 start 未提交”与“生命周期 receipt 成功”。补充测试：say 成功后 show_word schema/handler/panic 失败、budget denial、say handler 失败；每例断言无 checkpoint/receipt/outbox，失败审计恰当存在且不泄露唯一哨兵。若产品设计选择不持久这些事件，需明确说明 D-15 的等价安全审计载体并验证可检索，不能只丢弃 buffer。

## 实现前需写清的 P2 合同

### AGENT-START-09 — bundle 的重试生存期和不可变性缺少可验证定义

位置：docs/41 §3 的 `PilotStartToolBundle`、rollback 重试约束及 §6 回滚用例。

“不可变”目前是概念描述。Go struct/slice/`json.RawMessage` 可被调用方继续修改；也没有规定 bundle 仅存于一次 coordinator 调用内、同一进程同一 task，还是跨 HTTP 重试。当前方案可以安全实现为同一次已认证 coordinator 操作中保存在私有内存、深拷贝/封闭字段，rollback 已明确证明时只重跑存储事务；COMMIT 结果未知仍 `pending_unknown`，不能重规划。这一生存期需明确，避免进程/HTTP 重试重新创建 task 和 budget。

**建议关闭条件：**定义 bundle 只在当前受信 task 的 coordinator lifetime 内有效，私有字段且构造后不可变；Store 仅接收该 opaque bundle capability，或在进入事务时深拷贝并校验确定性 bundle digest；同 ID 不同 bundle 一律冲突。Go 用例明确：首次完整 Registry 调用两次后注入可证明 rollback，再同 task/ID 重入只发生第二次存储事务，Registry 调用计数仍是2、budget 不变、最终只持久一组审计/outbox；COMMIT unknown 不重建 bundle、不再调工具。

## 通过项与边界

- `RenderLessonStartT01` 将模板选择与话术留在 Agent 层，输出限定首课 OP-1/fallback；符合 T-01，自我介绍/昵称/≤60 rune/不声称历史事实都有可验收断言。
- ordinal=1 来自新建隔离 DB 的唯一 reservation，非首课拒绝；没有把这个 fixture 事实扩大成真实学生历史或产品授权。
- Say→`Machine.Start` 产生 action→`PlanEffects`/`classroom.show_word` 严格保持 T-01 次序；`Machine.Start` 是领域转换而非伪造 tool；冻结 meanings 原序进入 command/fingerprint/typed outbox，不查 live 词库。
- `tools.WithEventBudget(ctx,2)` 与固定两次有序 Registry 调用相符；rollback 重用 bundle 的路径不再消耗/刷新预算。task 由 Store 同包凭已核 owner/active/expiry/reservation 事实构造，HTTP 不可自造。
- 隔离 harness 不接 Room、产品 route、网络/模型/音频/consumer；本评审不批准外部模型、生产路由、真实课堂或真人录音。

AGENT-START-08 关闭前，Agent 域不签字进入实现；09 建议一并写入，以锁住跨层审计与测试含义。

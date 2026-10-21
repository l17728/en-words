# Agent 域复审：Start Kernel SDD（第 6 轮）

- 文档：`docs/41-试点课堂Start持久迁移内核SDD.md`
- 范围：第 5 轮 AGENT-START-12/13、T-01 话术/fallback、Prepare/Execute 与 sidecar 状态机。
- 类型：只读复审；未修改代码/文档，未运行测试。
- 结论：**BLOCK**。AGENT-START-12/13 的机制问题已在设计层关闭；但 T-01 品牌文字有一处自相矛盾，导致按文面实施时固定黄金话术不满足 Start 验收。

## 上轮问题

| 问题 | 状态 | 判断 |
|---|---|---|
| AGENT-START-12：工具前绑定 Agent 结果 | 已关闭（设计层） | §3 明确 `Prepare → plan_ready Sync → 每个 tool_started Sync → Registry → tool_result Sync`。`plan_hmac` 在 Registry 前绑定最终 command fingerprint、template/source 与固定 effect 序列；tool_started/result 均引用它。崩溃点测试覆盖 Prepare、plan_ready、工具前、handler 返回到结果 Sync、bundle_ready，未决时 pending_unknown 且不重放。 |
| AGENT-START-13：失败路径 provenance | 已关闭（设计层） | §3 sidecar 现在记录固定 `actor_role=teacher_agent`、`trigger=classroom_event`、`scope=teacher_agent`、`risk=low`，以及从受信 capability/AuditEntry 派生的 task/trace HMAC；禁止 Planner/HTTP 自报，并测试篡改/跨 command 关联拒绝。失败尝试与 lifecycle audit 分账，没有成功 receipt 孤儿。 |
| T-01 精确 Agent API / fallback | 通过（除下述文字矛盾） | `RenderLessonStartT01` 与 `Bank.RenderExactID` 固定 OP-1、不随机改写，方法内负责 fallback；adapter 不直接拼文案；Golden 精确锁定。 |
| Prepare / Execute 与 budget | 通过 | Prepare 只做本地 Teacher 决策/渲染，Execute 才经 Registry 做两次有序本地调用；budget=2；rollback 只复用 bundle，未知/重启不重放。 |

## 阻断项

### AGENT-START-14 — 自我介绍名称拼写与黄金/fallback 冲突（P1）

位置：docs/41 §3 规划器合同（约第 24 行）。该段写 `OP-1/fallback均须含叮叮老师自我介绍及昵称`，但 docs/04 的 OP-1、SDD 同段 Golden、固定 fallback 均写的是“叮当老师”。若按规格原文执行包含检查，规定的 Golden 和 fallback 会被拒绝；若实现按黄金文本执行，则未满足文字断言。T-01 产品命名也已定为“叮当老师”。

**关闭条件：**将“叮叮老师”统一更正为“叮当老师”，并在 Agent 规范、fallback、字符断言和 golden test 中使用完全一致的品牌字串；做一次 docs/04 ↔ docs/41 ↔ Agent seed/template 的终扫。无需改动教学逻辑。

## 其余核对

- `attempt_started` Sync 成功前不调用 Prepare；Prepare 后必须 `plan_ready` Sync 才执行任何 Registry tool；工具各自有先行 `tool_started`，缺 `tool_result` 的重启状态为 pending_unknown 且禁止重执行。
- plan HMAC 在 Registry 前绑定最终文案/frozen show_word，工具结果后续各自绑定该 plan；完整 bundle digest 只在两次调用均完成后写入。部分失败不写生命周期 checkpoint/receipt/audit/outbox，sidecar保留attempt链。
- task/trace 在 sidecar 中只保留 HMAC，scope/role/trigger/risk 是受信固定值；失败审计与成功 receipt 分开，因此不会制造孤儿成功 audit。
- 测试矩阵覆盖稳定 OP-1 和缺模板 fallback、无重复规划/调用、预算和各 Sync 崩溃窗口。它仍是隔离 Start-only 设计，不授权生产入口、声音派发、模型接入或真人课堂试点。

AGENT-START-14 更正后，Agent 域可对本轮关注范围给出 PASS；更正前不能签字。

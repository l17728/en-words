# lesson.start 持久迁移内核 SDD：后端复审 10

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新稿）。只读核查新 Planner Prepare/ExecuteTool 与一次性 PilotToolPermit、sidecar attempt 事件、SQLite writer/receipt recovery、v2 schema 和 build tag 隔离，并扫描状态恢复与 ID/audit 关联矛盾。对照现有 Go auth/store/api、tools.Registry 与 atomicConnection；未修改代码/文档，未运行测试。

## 结论

**BLOCK / P1：一次性 permit 的消费语义尚未被实现合同保证。** 其余新边界整体可实现，未发现新的 P2 数据库或 sidecar 状态冲突。BE6/BE7/BE8 既有闭环保持；Pinned SQLite proof、真实 build-tag 和 sidecar golden 仍是实现验收事项。

## 重点复核

| 范围 | 结论 | 依据 |
|---|---|---|
| Planner Prepare / ExecuteTool 分段 | PASS | Prepare 只做确定性计划并生成 plan_ready；每个固定 invocation 由 Coordinator 单独发起 ExecuteTool，不再允许整 bundle 执行。Store 与 API adapter 分包，harness 组装无 import cycle。 |
| PilotToolPermit 绑定 | PARTIAL / BLOCK | Permit 字段绑定 command_hmac、plan_hmac、attempt_id、effect_index、tool_name，Coordinator 只在对应 tool_started Flush+Sync 后创建；但文档未冻结 Registry 调用边界怎样消费 permit。Go 的 opaque struct 即使字段私有仍可被调用方复制；API adapter 收到 permit 后可在一次 ExecuteTool 内再次调用 Registry，或忽略 permit。事后发现多条 AuditEntry 只能拒绝 bundle，不能撤销已执行调用。需增加可执行的一次性门，例如 Store 生成带原子消费状态的 PermitExecutor/闭包，由它验证 invocation 并只暴露一次 Registry.Call；或者明确 Registry wrapper 在执行 handler 前原子 Consume permit，第二次必拒绝。Adapter不得直接持有可绕过 wrapper 的原始 Registry.Call。 |
| Sidecar attempt 事件 | PASS WITH IMPLEMENTATION GATE | attempt_started → plan_ready → 每个 tool_started/tool_result → bundle_ready → 最多两次 commit attempt 的次序明确；permit 在 durable tool_started 之后发放，Sync 失败不会调用工具，缺 tool_result 重启为 pending_unknown 且不重放。HMAC 字段和 task/trace 脱敏关联与 Registry audit 比对一致。 |
| SQLite writer 与 COMMIT recovery | PASS WITH IMPLEMENTATION GATE | 工具及 sidecar I/O 都在 writer 外，writer 二次授权后原子提交审计/checkpoint/receipt/outbox；unknown 只读查一次 receipt；仅原连接 typed rollback_proven + 原 bundle 可重试一次。当前 atomicConnection 只有 error 返回、无专用 typed proof，须按该合同实现；未知必须维持 pending_unknown。 |
| schema / 混合库兼容 | PASS | 固定 v2 DDL，不扩列；active verifier 继承 legacy bridge/runtime、schema 与逐表 FK 审计，然后按 reservation 做 created/active lifecycle 分派。audit marker、receipt TraceID、effect index、outbox scope及冻结 payload 的关联链完整。 |
| build-tag / package 隔离 | PASS | store pilot contracts 与 api pilot_e2e adapter 各在自己的 package/tag 文件，cmd harness 组合；store 不 import api。默认 server 依赖图不包含 adapter/route。实际仍需构建门验证有/无 tag 两种依赖图。 |
| ID 与审计关联 | PASS | command_key_hmac 用 lesson+command 定位，command_hmac 绑定 planning fingerprint，plan_hmac 绑定最终计划，bundle_hmac 绑定最终 command 与 bundle digest；task/trace HMAC 与 Registry AuditEntry 比对；DB TraceID 仅经 receipt.decision_id 关联，不混用 sidecar ID。 |

## BE8 与旧项状态

BE8-1 的确定性拒绝类别已在 result_class 中列出；BE8-2 的 pilot_marker 同时存在于 pilot audit 映射和持久 Args allowlist；BE8-3 改为 say payload.text 与 room_facts.teacher_text 比较、receipt 元数据独立校验，均 **CLOSED**。

BE6-1..11 和 BE7-1..5 维持 **CLOSED**（BE6-5 仍需实现 pinned connection rollback proof）。本轮未发现 schema 版本、mixed lifecycle、重启恢复或 task/trace/decision 关联的新矛盾。

## 实现前需要补清

1. 冻结 PermitExecutor/one-shot consume API 与 Registry wrapper 边界，使一次 permit 在调用 handler 前原子消费，且不能旁路。
2. 增加断言：一个 tool_started 对应至多一次 Registry handler 进入；复制/复用 permit、错 effect/tool、重复消费均在 handler 前拒绝。
3. 实现并验证 pinned SQLite connection 专用 rollback_proven typed outcome；区分 callback rollback、commit unknown 与确定性拒绝。
4. 用实际 Go 构建图验证 store/api/harness 的 pilot_e2e 文件在默认生产构建中完全排除，并验证带 tag 的 adapter 可实现接口。

本轮仅为后端设计评审；不代表代码实现、测试验收或真人课堂试点已经完成。

# Start 内核 SDD 第三轮局部复核

2026-10-16。仅复读当前 docs41 的 D15 修订及 outbox 必填字段，不重做全审、不运行测试。

**D15 结构性阻断已关闭。** §3 明确 Machine.Start → Teacher.PlanEffects → Registry classroom.show_word，和 classroom.say 同走受信 scope/schema/budget 与纯 handler；§4 恰两条真实工具审计、工具各一、两条 outbox。未新增直接教学效果旁路，也不调用旧 callAgentTool 的独立事务。

outbox 的 next_attempt_at、created_at 均为 start_time_ms；pending/attempt_count=0，claim_token/claim_until、output_auth_generation、lease_id、segment_id 均为空，与现有 pilot_schema.go 的非空时间字段和 claimed/lease CHECK 相容。owner/lesson/decision/schema/version 仍须由受信创建事实与 receipt 填入，不从客户端扩展字段获取；这是已有合同，不需改 DDL。

**剩余一处验收文本必改：** §6.1（本次读取第67行）仍写“1匹配audit”，应为“恰2条 audit，classroom.say/classroom.show_word 各一”。否则测试会对正确实现给出矛盾预期。这属于计数同步，不是新架构缺陷。

修正该计数后，本局部项可通过；第二轮其余 approve 保持。未审批 Room 接线、媒体执行或真实模型能力，未产生运行证据。

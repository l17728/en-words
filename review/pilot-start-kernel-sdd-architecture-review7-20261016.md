# Start 内核 SDD 第七轮架构复核

2026-10-16；只读版本 SHA256 `79b025ccc3b4195282f8be2875078213214bb4386dc1b39f3feabe36953f7ee4`。未执行测试、构建或服务，未修改规格/产品。

结论：**BLOCK，2项P2具体协议冲突。** 上轮三项主要设计修订已读到，不再重复要求更换整体方案。

## 上轮闭合项

- START6-01：planning_fingerprint 不含 Agent 输出；intent 先持久化，再 Prepare/Execute，bundle_hmac 后绑定最终指纹及 bundle digest，时间依赖环已解除。
- START6-02：single-flight 改为 case 内 lesson_id；不同 lesson 可并行纯规划，最终 SQLite writer 串行化。case append mutex 串行分配序号/HMAC/Sync，OS 独占子进程锁防双进程同时修改同 case。
- START6-03 的字段冲突：sidecar 不再同时要求 Registry 原始 Actor/TraceID/risk 字段，成功 DB audit 与失败尝试事件分账。损坏 sidecar 即使 DB 有 receipt 也令 harness 失败关闭，正常 receipt 优先规则有明确前置条件。
- 当前身份授权、学生各自 ordinal、两工具两审计、active/created 混合审计、writer 外执行工具/Sync、未知禁止新写、至多一次已证明 rollback 的同 bundle 重试，方向一致。

## START7-01：终态事件仍缺严格可编码的拒绝映射（P2）

§3 sidecar `result_class` 枚举有 `authorization_rejected/schema_rejected`，没有 `unauthorized/not_supported/schema_invalid`；紧接的终态合同却要求按后三种原因写 terminal_failed，未定义转换。严格 encoder/decoder 无法同时接受这些行为。请给固定映射（例如业务 unauthorized→authorization_rejected，schema_invalid→schema_rejected，not_supported明确选定一个专门枚举），并锁每种结果的 null/attempt_no/tool_name 规则；不能直接序列化HTTP reason。

恢复表还应明确：合法 durable `committed` 事件但数据库没有对应receipt必须为完整性错误、停止case，而不是新命令/普通pending/not_committed。当前无receipt列表只覆盖未决与失败状态，没有此终态分支。此要求不增加功能，只封闭已定义状态空间。

验收：各确定性writer拒绝可编码且重启稳定；手工构造合法签名committed事件但无receipt时明确失败关闭；未知结果绝不被映射成rollback。

## START7-02：say 与 receipt 的逐字节校验对象不存在（P2）

§4 两处要求“say payload逐字节等于对应receipt结果”或“对比receipt与room_facts.teacher_text”；但同节 receipt.result_json 精确限定为 `result/audit_trace_id/outbox_count`，既没有say文本也不是say DTO。正确记录也不可能满足该比较。

最小修订：say DTO 的 type/category固定，text只与 active room_facts.teacher_text 逐字节比较；receipt校验kind/版本/owner/decision/outbox_count及request_fingerprint。若需验证完整最终指纹，应从既有冻结事实、Start时间与say/show_word规范DTO重建并比对，不新增receipt正文。明确“关联校验”与“内容相等”不是同一断言。show_word与frozen_words[0]逐字段比较保持。

## 非结构性同步和实现约束

- §5仍写harness每次创建目录且失败清理，与监督器跨子进程保留case矛盾；应明确只有监督器在全部子进程/DB关闭后清理，子进程失败不删恢复证据。
- 进程生命周期OS锁必须持续覆盖该case使用期（自然包含SQL事务）；只有append mutex不应跨SQL。现“mutex/文件锁不跨SQLite事务”需区分这两种锁，否则独占保护会失效。
- 同Coordinator的双请求会等待lesson锁，不能同时等待SQLite writer；§6.1应把lesson队列测试和存储内核独立pool竞争分开，避免barrier永远等不到第二writer。无需改变原超时门。
- Registry已移至SQL事务外，handler panic应在Planner/协调器边界捕获并固定分类；§6.2“只允许事务边界recover”应限定真正SQL事务panic，不能要求工具panic必须发生在SQL事务内。
- cache容量需在intent/Planner前预留，不能并发生成第33份bundle后才拒绝。有效HMAC前缀不证明没有整行尾部删除；不要把链验证声称为任意回滚检测。

这些是当前文档内部的编码/验收可执行性问题，不是发现产品运行失败。Room接线、outbox派发、真实模型及声学仍不在本片范围。

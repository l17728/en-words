# lesson.start 持久迁移内核 SDD：后端复审 5

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新稿）。本轮按要求只读复核 BE4-1..4 的闭环及跨包 coordinator、opaque actor、bundle 的事务/COMMIT-unknown/rollback 生命周期。对照当前 auth、store、api、Agent/Registry 代码；未修改代码、未运行测试。

## 结论

**BLOCK / 暂不能进入编码。** 文档已改善 BE4-2（不再引用不存在的 TeacherInput.LessonOrdinal）、BE4-3（区分 tool schema 与 audit-map 版本），并把昵称字段/规范化步骤写具体；BE4-1 只部分闭环。当前的 Store/API 分层方向合理，但具体输入类型、身份与规划器的跨包传递合同仍不完整，且 bundle 的重试只写了行为要求，没有给出能保证“完全相同”的生命周期持有机制。

## 上轮问题复核

| 原项 | 结论 | 依据 |
|---|---|---|
| BE4-1 跨包可见性/opaque actor | PARTIAL / BLOCK | 新稿把入口改成导出的 PilotStartCoordinator，并要求 Store 内部构造私有 VerifiedPilotStartActor，方向解决了 unexported method 不能跨包调用的问题。但签名用的 auth.Principal 当前不存在（代码有 auth.Claims，Verify 返回 *Claims）；Coordinator/Planner 的可编译 Go 方法签名、opaque 计划输入和已验证上下文给 Planner 的传递方式仍未冻结。 |
| BE4-2 TeacherInput.LessonOrdinal | CLOSED | 新稿明确不扩展公共 TeacherInput，先验 server ordinal 再调用现有字段；新增 RenderLessonStartT01 单独把固定模板策略封装在 Agent。 |
| BE4-3 schema_version | CLOSED | 新稿定义 Args 中 schema_version=工具参数 JSON Schema 版本，映射协议版本由 pilot-audit-map/v1 表示。 |
| BE4-4 昵称来源/规范化 | PARTIAL | 明确了 User.Nickname、规范化规则及 VerifiedActor 快照，但仓库同时存在 auth.User 与 store.User，且身份服务是内存 auth.Service。未明确 HTTP middleware 的权威账号来源如何与 Store DB 的 User.Nickname 保持一致。 |

## 新发现

| ID | 结论 | 问题与修订建议 |
|---|---|---|
| BE5-1 | BLOCK / P1 | auth.Principal 不是当前代码类型；现有 token 验证返回可普通构造的 Claims，而不是不可伪造 principal。若需要 opaque principal，应在 SDD 声明新增认证边界类型，由 Verify(token) 产生、字段不可外部构造，并写明 UID/role/expiry 的语义；或显式使用已验证 Claims，并把“不可伪造”改成“仅可信 middleware 可传入”。Store 还须在 DB 复核 UID 对应 user 的 role=student、status=active，不能把 Claims 本身当作当前账号状态。 |
| BE5-2 | BLOCK / P1 | Store 在内部建 VerifiedPilotStartActor，但 API 中的 Planner/隔离 executor 需要创建 agentTask（该类型及私有 context key 属于 api 包），并需拿到已验证 lesson/student/book/nickname/taskID。文档只说 Store 注入 test-only Planner、adapter 不能构造 VerifiedActor，却没有规定 Store 如何向跨包 Planner提供最小可信 DTO/opaque handle，或由谁生成并验证 teacher task capability。需要冻结接口方向与字段：例如 Store 在 writer 内先验证 principal 与 reservation，再调用窄 Planner interface，传入带私有字段、只读 getter 的 PilotStartPlanningContext；api adapter据此建一次 task/context，返回不可变 typed bundle；Store 再做独立校验及落库。不可让 API 传回任意 actor/task 字段绕开 Store 检查，也不能让 Store import api。 |
| BE5-3 | BLOCK / P1 | Bundle 的“同 ID、同 task 仅一次生成；rollback 重试完全复用；unknown 不重调 Registry”目前是约束，没有持有者或状态转换。若 Registry 在 Store writer callback 内执行，callback rollback 后 bundle 是否仍被 coordinator 保留、再次进入 writer如何取回，未定义；若工具在事务外执行，则需定义授权预查、并发 replay race 和如何确保命中 receipt时零工具调用。COMMIT unknown 后独立查回执仍不可达时，bundle需要跨 HTTP 请求保存；进程重启会丢失 task-local bundle/audit且其 TS/DurationMs 无法精确重建。请选定生成/持有位置、command+task key、并发 single-flight、保留期、取消/崩溃后的终态。重启后只有查到 receipt才可收敛；无回执且 bundle 丢失时应保持 pending_unknown/人工或协议化恢复，不能假装可用同一 bundle 重试。 |
| BE5-4 | BLOCK / P1 | COMMIT unknown 的文案允许“同 ID 重入 writer”，但若前一次 Registry 执行产生的 bundle保存在请求栈而调用已返回 pending_unknown，后续请求如何引用同一 opaque bundle未定义；同一 ID 的新请求又不能从客户端提供 bundle。建议明确 coordinator 对 command 的内部状态机：planned → bundle_ready → committing → pending_unknown / committed / rollback_proven；只有 bundle_ready 且原 bundle仍可用时才可在rollback_proven后重提。pending_unknown期间禁止新bundle生成。 |
| BE5-5 | APPROVE WITH CLARIFICATION / P2 | 昵称来源写为 identity store 的 User.Nickname，但当前存在 auth.User.Nickname 与 SQLite store.User.Nickname 两份类型。测试 harness 的认证 token通常由 auth.Service生成，而 Store独立DB检查用户；必须指定冻结和重核均读 SQLite store.User.Nickname，或规定 auth/Store 数据一致性及冲突时失败关闭。另应规定已提交命令回放的次序：如果用户昵称在成功提交后被修改，owner仍应能依 command ID 取回原 receipt；新候选才需要校验昵称快照一致，避免内容资料更新让 COMMIT-unknown 的已提交结果永久无法恢复。 |
| BE5-6 | APPROVE WITH CLARIFICATION / P2 | Writer/Planner 的先后需要用一条调用序列明确表达。§3称“事务外规划”，§3事务顺序又在已取 writer 后调用 tx-bound executor；新版 §32同时称 bundle先生成后由 Store 在 writer内校验/落库。请明确 Registry/tool handler执行是在 writer前还是 writer内。若在 writer内，必须解释有限执行时间如何计入 writer等待与事务延迟并保证 callback rollback后缓存bundle；若 writer外，说明已存在 receipt/并发胜出时，预生成但未产生持久副作用的 bundle如何废弃，且符合“重放不调用工具”。 |
| BE5-7 | APPROVE WITH CLARIFICATION / P2 | tool schema 版本语义已明确为 JSON Schema v1，但还需冻结实际 schema bytes/hash 与版本边界。Registry当前 say schema 是 text/category；show_word 是 index integer（现有子集 validator 不一定实施 additionalProperties:false）。后续 handler 另行做严格解码可成立，但 SDD“额外payload拒绝”应声明由 Registry schema以外的严格 typed decoder完成，并在 schema升级时拒绝或升版本。 |

## 可以保留的设计判断

- BE4-2、BE4-3 已闭环；昵称长度/控制字符处理的目标明确。
- 事务内先验 owner、再查 receipt、未命中再 CAS 的顺序和 conservative pending_unknown 规则正确。
- v2 DDL 使用 active lifecycle、无 active_command、两条 ordered outbox 的落库方向与当前 schema 相容。
- harness build-tag 隔离及纯 handler、task-local auditor 与 bundle 原子持久审计的方向可以继续；本轮没有发现需要改变该特性范围的后端原因。

## 编码门之前必须补充

1. 定义真实存在或明确新增的不可伪造 auth principal 类型、middleware 到 Store 的可信路径。
2. 定义跨包 Coordinator 与 Planner 的可编译接口及 verified planning context；说明 API 如何获得必要事实而不能伪造 actor/task。
3. 给 PilotStartToolBundle 一个具体的单次生成、内存持有、并发复用和 unknown/rollback/restart 生命周期合同。
4. 固定 Registry 执行相对 writer transaction 的准确位置。
5. 消除 User.Nickname 双来源歧义，并保证已提交 receipt 在昵称变化后仍可恢复。
6. 明确工具 schema 字节/版本和严格额外字段拒绝位置。

本次为 SDD 只读复审，不批准实现，未运行任何测试。结论也不代表完整课堂、音频/模型接入或真实课堂试点已开放。

# lesson.start 持久迁移内核 SDD：后端复审 4

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 修订稿）。只读核对 BE3-1..10 闭环与新冲突；对照现有 tools.Registry、教师工具、TeacherInput、Machine/State 及 v2 schema。未修改产品代码，未运行测试。

## 结论

**BLOCK / 尚不批准编码。** 上轮 BE3-1..10 的主要缺口已在文档层逐项闭环：审计映射、生命周期分派、隔离 Registry 组装、outbox 类型和 harness build tag 均有具体合同，且无须改 v2 DDL。当前新阻断集中在跨 Go package 的 Store/API capability 调用不可执行，以及 SDD 引用了不存在的 TeacherInput.LessonOrdinal 字段。另有审计元数据版本命名需澄清。

## BE3-1..10 复核

| 原项 | 本轮结论 | 复核依据 |
|---|---|---|
| BE3-1 Registry Result | CLOSED | SDD 固定 Registry 原始成功 audit 为 ok，receipt queued、outbox pending 各自表达提交/派发状态，三者未混用。 |
| BE3-2 created/active 生命周期分派 | CLOSED（文档合同） | auditPilotLifecycleTx 统一 dispatch 已覆盖 schema inspection、creation nonce replay、Start/receipt 和 creation audit；created-only verifier 保持严格，active 使用新 verifier。实施时必须按此接通每个列举入口。 |
| BE3-3 AuditEntry 与持久 Args | CLOSED（文档合同） | 原始 Registry Args={"bytes":N} 保留来源语义，再显式映射到 pilot-audit-map/v1 元数据对象；已不声称 Registry 原生携带 scope/effect index。 |
| BE3-4 隔离教师工具上下文 | CLOSED（文档合同） | 描述了隔离 App/task/private context、task-local Registry auditor、withTeacherEffects、固定两次调用及不触碰 Room/global auditor 的组装。该流程可复用当前纯 handler。 |
| BE3-5 v2 schema/verifier | CLOSED（文档合同） | 明确 v2 DDL 不改；active 用独立严格 codec/verifier；如需新增字段则升 schema 版本。 |
| BE3-6 回执、unknown commit | CLOSED | 授权后先查 (lesson_id,command_id) receipt 再做 CAS；未知结果保守返回 pending_unknown，不以普通 not-found 证明 rollback。 |
| BE3-7 outbox kind/payload | CLOSED | 固定 kind=say/show_word，并区分 Registry 输入与 action 的规范化输出 payload。 |
| BE3-8 harness build 边界 | CLOSED（文档合同） | 每个 Go 文件的 build tag、默认包图/正式 server 排除断言已冻结；实施仍需靠构建检查验证。 |
| BE3-9 并发与故障证明 | CLOSED | 后提交响应丢失钩子先独立读取 DB 验证；提交前断言无记录以 rollback 已证实为前提。 |
| BE3-10 State/codec/DDL 字段 | CLOSED | State 字段、初始化值及 active DDL 映射已有显式合同，保持旧 Room/默认产品入口排除。 |

## 新发现

| ID | 结论 | 问题与修订建议 |
|---|---|---|
| BE4-1 | BLOCK / P1 | §3 给出的 commitPilotStart、lookupPilotStartReceipt 是小写 unexported Store 方法，但 executor 明确实现在 api package 的 test-only build-tag 文件中。Go package 无法调用另一个 package 的 unexported 方法；API 也不能访问 Store 私有类型/字段来构造 VerifiedPilotStartActor。请冻结可编译的边界：例如由 store 暴露仅接受不可伪造 opaque capability 的窄 exported method/interface，再由 store 内的验证入口构造 capability；或者把 orchestration/executor 移到同一 package。同步说明默认产品二进制虽然可链接 Store API，但没有构造 capability 的入口、路由或 harness 依赖。禁止用 any、SQL callback 或公开可任意构造字段解决。 |
| BE4-2 | BLOCK / P1 | §3 明写 TeacherInput{Event:lesson_started,LessonOrdinal:1}，但当前 agent.TeacherInput 没有 LessonOrdinal 字段。必须在 SDD 决定是为该功能给 TeacherInput 增加字段并约定所有调用点默认值/回归行为，还是不扩展公共类型、只由已验证服务端 ordinal 控制是否调用 Decide。建议后者更小；若保留字段写法，则需明确类型变更、行为定义与测试覆盖。 |
| BE4-3 | APPROVE WITH CLARIFICATION / P2 | pilot-audit-map/v1 将 Args 扩展为含 schema_version:1，但没有说明该键表示 tool 参数 schema 版本，还是 audit-map 协议版本（后者已有 pilot-audit-map/v1）。请把字段语义明确为固定 tool_schema_version 或删除冗余键；否则之后工具 schema 升级时无法判断旧记录应按哪版解释。若为了固定现有 schema 而保留，需写清 schema 内容及升版策略。 |
| BE4-4 | APPROVE WITH CLARIFICATION / P2 | VerifiedPilotStartActor 被描述为绑定昵称快照，Store 每次重核昵称未变；核对点应落到当前用户资料的可信昵称列及明确规范化结果。请明确 nickname 是 identity/auth Store 中哪个字段，以及规范化是在授权前冻结一次还是每次 replay 重算；否则 API middleware 与 Store 可能对相同账号得到不同 greeting/fingerprint。 |

## 进入编码前的最小修订

1. 解决 BE4-1 的 Go package 可见性和 opaque capability 创建/验证路径。
2. 解决 BE4-2 的 TeacherInput 类型错位，避免把不存在的字段当作当前代码能力。
3. 固定 schema_version 与昵称快照的来源/版本语义（BE4-3、BE4-4）。
4. 再请架构、安全、测试和 Agent 域复核上述修订；本报告没有运行测试，不能替代实现验收。

此结论只针对 start-only 隔离持久化内核。真实产品入口、完整课堂、旧 Room 并发兼容、outbox 消费、模型/音频和真人课堂试点仍未验收或开放。

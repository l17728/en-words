# lesson.start 持久迁移内核 SDD：后端复审 7

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新稿）。只读核查 BE6-1..11 闭环、新增 sidecar HMAC 双键/状态迁移/崩溃表、Coordinator 生命周期、unknown 重试、mixed lifecycle legacy/FK 保留、audit/outbox 跨课关联及新歧义；对照现有 Go package 边界和 SQLite Store。未修改代码或设计文档，未运行测试。

## 结论

**BLOCK。** BE6-1..3、BE6-5..9、BE6-11 已在设计层面闭环；BE6-10 的关联规则有实质进展，但还缺少如何可靠识别孤儿 pilot audit 的保留标记。BE6-4 仍未闭环：设计要求 API 包的私有 task/context 能力，却规定相关实现全部在 store 包。另有 sidecar 状态迁移与数据库写阶段拒绝结果不完整、后续完整性校验未明确验证 outbox payload 来源等新问题。

## BE6-1..11 逐项复核

| 项目 | 状态 | 依据 |
|---|---|---|
| BE6-1 Planner 时序 | PASS | §1/§3/§3执行时序勘误及恢复补充均规定 Planner、Registry、sidecar Sync 在 writer 外，并由勘误明确优先级。 |
| BE6-2 昵称与 replay | PASS | 当前登录/owner/active 授权后先查 receipt；命中不核 task expiry/昵称；新 command 和未提交 bundle 才比较规范昵称。 |
| BE6-3 cache key | PASS | cache key 为 lesson_id+command_id，fingerprint/digest 为必比对值；同键异 fingerprint 在 Registry 前拒绝。 |
| BE6-4 Store/API 包边界 | **BLOCK** | §3要求 Coordinator/Planner/context/bundle 全放 store 的 pilot_e2e 文件，§3又要求 harness 构造 API 私有 agentTask、context key 与 withTeacherEffects。现有这些符号只在 api 包可见；cmd harness/store 都无法直接构造。若把 adapter 放 api 包，则违反“所有…代码均放…store文件”约束。必须明确 API 下的 test-only adapter 文件及职责，再由 harness 组装；Store 仍不 import api。 |
| BE6-5 COMMIT unknown | PASS WITH IMPLEMENTATION CONDITION | §3恢复补充和崩溃表已禁止新 writer 的 not_found/超时/锁释放作为 rollback 证明；只有原子连接层 typed rollback_proven 且同进程持原 bundle 才可重试。现有 atomicConnection 尚无该 outcome，实施时必须新增并区分 callback rollback 与 commit unknown。 |
| BE6-6 Coordinator 生命周期/cache | PASS | case 构造器一次注入长生命周期 Planner/store/sidecar/cache，HTTP 不能替换；32项满时 fail closed，不驱逐 unresolved bundle。 |
| BE6-7 sidecar/key 恢复 | PASS | 监督器持久化 case dir、SQLite、key；双域分离 HMAC、全文件链、截断/损坏/冲突 fail closed 与崩溃恢复策略都有描述。 |
| BE6-8 rollback终态 | PASS | 活进程持有 bundle 才能按 rollback_proven 同ID重试；重启后无 bundle 的 command 永久 not_committed，要求新 ID。 |
| BE6-9 legacy/FK | PASS | `auditPilotDatabaseIntegrityTx` 明确保留既有 legacy bridge/runtime、未迁移拒绝、schema 和逐表 FK 检查，仅替换全库 created-only 空表假设。 |
| BE6-10 audit/receipt/outbox关联 | PARTIAL / BLOCK | 已规定 TraceID=decision_id、两条唯一工具 audit、effect_index 与 outbox decision/effect 对应及跨课拒绝；但没有持久的显式 pilot marker。当前映射字段 `scope=teacher_agent,schema_version=1,effect_index` 可能与普通 audit 参数重合，如何识别“无 receipt 的孤儿 pilot audit”及避免误分类普通日志仍未冻结。应规定保留的 pilot 专属 marker/精确识别谓词，并写入 schema 映射版本。 |
| BE6-11 ordinal | PASS | 新增混合用例明确 A active 不阻塞 B 的首个 reservation 仍 ordinal=1；合同应按 student 维度解释。 |

## 新发现

| ID | 级别 | 问题与修订建议 |
|---|---|---|
| BE7-1 | P1 | Sidecar 状态迁移没有覆盖 Planner 成功后、bundle_ready/commit_attempt_started 阶段发生的确定性 writer 拒绝。§43仅允许 attempt_started/tool_result → terminal_failed；但§3最终 writer 可因 owner/task expiry、schema、reservation、CAS/frozen content 校验失败而无 COMMIT unknown，并返回 stale/unauthorized/already_started。若记 rollback_proven，合同又允许同进程重提同一 bundle；若不记终态，重启恢复为 pending_unknown。需定义 commit_attempt_started → terminal_failed 的允许失败类别（不包含未知 COMMIT），并明确这些失败后同 ID 永不再规划/写入。 |
| BE7-2 | P1 | `rollback_proven`同时被描述为“终态”又允许其后追加 commit_attempt_started；状态表的“每命令仅一个终态”因此语义不稳。建议将事件日志状态机拆成 attempt terminal 与 command terminal：一次 commit attempt 的 rollback_proven可后续被新 commit attempt取代，但只有 committed/terminal_failed/not_committed 是 command终态；或明确 rollover规则、attempt_no递增与有限重试上限，防止无限同 bundle writer 重试。 |
| BE7-3 | P2 | audit孤儿识别依赖 Args 映射字段，但当前工具审计表没有 pilot 专属关联列；若普通 audit恰好具备同样的参数键/值，将被混合审计误判。需冻结保留的 Args marker，例如独立 pilot_map/version 名称，并禁止普通调用使用，或在协议中声明该精确 Args 形状全局保留。 |
| BE7-4 | P2 | active verifier 需显式校验 outbox payload 与权威来源一致。现文档定义了 typed say/show_word payload、receipt及 outbox 关联，但跨课/篡改恢复时还应逐字段比较 say 与 active room_facts.teacher_text、show_word 与 checkpoint frozen_words[0]，并检查 effect kind/index/decision 与相同 receipt。否则合法 JSON 但内容被改写的 payload 仍可能通过链接审计。 |
| BE7-5 | P2 | §36的一般句“相同 ID 重入 writer”与 §38后明确的 unknown期间“只读查 receipt、未证 rollback 不再写”优先级仍可被误读。建议把 §36 改为只允许 receipt 查询/原子层专用 rollback-proven 后重试，避免调用方把 pending_unknown 当作可直接重发写请求。 |

## 进入编码前需补齐

1. 将 api 包内 pilot_e2e adapter 纳入源码/构建合同，解决 BE6-4 的私有类型不可见性。
2. 补全 writer 阶段确定性拒绝的 sidecar 迁移，并规范 attempt 与 command 两层终态/重试次数。
3. 为 pilot audit 设置不会与普通日志重合的保留 marker，并固定 orphan 检测规则。
4. active audit verifier 比较 outbox payload 与 room_facts、frozen_words 权威事实。
5. 修正文案，令 unknown 只读及 rollback-proven 写重试规则在所有段落一致。

本报告只覆盖后端与 SQLite 设计可实施性，不代表整体评审通过、实现完成或课堂试点已开放。

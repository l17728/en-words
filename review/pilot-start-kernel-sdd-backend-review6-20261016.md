# lesson.start 持久迁移内核 SDD：后端复审 6

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 当前稿）。本轮只读核查第3轮 BE3-1..10 的闭环及新加入的 Planner/PlanningContext、bundle 恢复、昵称重放、混合 lifecycle 审计合同，并对照 auth、Store、API、Registry 与 SQLite 审计代码。未修改产品代码，未运行测试。

## 结论

**BLOCK：设计稿还不能作为无歧义的实现合同。** 第3轮 BE3-1、BE3-3 的 Registry audit 映射，BE3-2 的统一 lifecycle audit 入口，BE3-4 的隔离 Registry/task-local auditor，BE3-5 的 v2 DDL 边界现已明确到可实现方向；BE3-6、BE3-7、BE3-8、BE3-9、BE3-10 的关键约束也保留或细化。本稿仍有互相覆盖的时序/恢复条款，且 build-tag 边界把 API 私有 task 创建职责放错包。需先清理以下 P1 项。

## 原 BE3 项目复核

| 原项 | 状态 | 复核 |
|---|---|---|
| BE3-1 / BE3-3 Registry audit | CLOSED | §4 固定成功 Result=ok、Args 原始 bytes 计数及持久化 pilot-audit-map/v1 映射；和 Registry 成功路径相容，且区分失败 sidecar 与成功 lifecycle audit。 |
| BE3-2 lifecycle 分派 | PARTIAL / BLOCK | 明确全库完整性加逐课 created/active 分派，但“每个 facade”范围需要列出具体入口并确保不能有旧入口继续只调 auditPilotCreatedDataTx。当前 auditPilotDataTx 尾部仍调用全库 created-only auditor，后者会因任一 receipt/outbox 拒绝数据；改造必须完整替换其调用路径，同时保留 legacy bridge/runtime 与 FK 全库检查。 |
| BE3-4 task/auditor | PARTIAL / BLOCK | App 私有 agentTask/context key 只能由 api 包构造；§3 同时要求 Planner/Coordinator/context/bundle 都放在 store 的 pilot_e2e 文件，无法在不违反包边界的情况下实现隔离 API adapter 的 task 构造。应拆分 store coordinator 与 api harness adapter 的源码位置。 |
| BE3-5 active codec/v2 DDL | CLOSED WITH IMPLEMENTATION GATE | active 行与 v2 列约束兼容，仍需按明确 codec/verifier 实现；不需改 DDL。 |
| BE3-6..10 receipt、outbox、隔离 harness、unknown E2E、machine codec | CLOSED IN DESIGN | 规格保留了先授权后 receipt、CAS、明确 kind/payload、test-only 构建、提交后丢响应验证和显式 codec 字段。实现时仍须按后述 unknown 语义修正文案。 |

## 新发现

| ID | 级别 | 发现与修订建议 |
|---|---|---|
| BE6-1 | P1 | Planner 执行边界有双重相反合同。§3 首段写“事务外形成不可变候选”；后文称“事务外一次性执行 Registry”；时序勘误也要求事务外且任何 IO 不持 writer。这些明确彼此一致，但较早 §32 仍出现“协调器在 SQLite 事务外调用 Planner”尚可，§3 中的“在完成授权预检并持久记录attempt_started后”依赖 sidecar；更大的矛盾在“COMMIT unknown后…后续同进程请求可复用原bundle继续查询/存储”与只允许证明 rollback 后重提。请以时序勘误为唯一正文合同，将其他段落改成同一状态机，不留下持 writer规划的旧句。 |
| BE6-2 | P1 | 昵称检查与 receipt replay 冲突。§3 第一段称“每次commit/replay重新读取…昵称变更则要求重新开始受信规划”；稍后又规定已提交 receipt replay 不受昵称变化影响。先查 receipt 的幂等合同要求 replay 不调用 Planner，也应不被昵称快照阻断。明确顺序为当前认证、DB owner/status 与 reservation 授权 → receipt 查询并原样返回；只有 receipt miss 的新 command/未提交 bundle 才比较昵称快照。 |
| BE6-3 | P1 | cache 键为 lesson+command+fingerprint，与“同 lesson+command 不同 digest 冲突”及数据库 receipt 主键语义不一致。若 fingerprint 属于 key，不同 fingerprint 会变成第二个 cache entry，无法保证检测冲突。键应为 lesson+command，fingerprint 作为被比较的值；命中但不同 fingerprint 必须冲突且不得调用 Planner。 |
| BE6-4 | P1 | 跨包职责与 build tag 不可同时满足。文档把 coordinator、Planner、PlanningContext、bundle 全放 store 包的 pilot_e2e 文件，但隔离 App 的 agentTask、私有 key、withTeacherEffects 及工具注册均在 api 包内。store 不应导入 api（会循环），api 也不能构造 store 内的私有 context。建议 store 定义公开但只读/不可 JSON 构造的 DTO 与 Planner interface、coordinator；api 包下单独的 pilot_e2e adapter 实现 Planner 并构造 task/context，adapter只从 context getter 读取必要绑定事实。Harness 外层组装两者。明确生产默认构建两侧文件均被排除。 |
| BE6-5 | P1 | COMMIT unknown 状态仍允许写入的措辞超出证据。§3/补充段说 unknown 后同进程可“继续查询/存储”或“同ID重入 writer”；只有后文说明原连接明确证明 rollback 才可 storage retry。无 receipt 的普通查询、取得新 writer 或锁等待结束都不能证明原 COMMIT 未生效（atomicConnection 目前也未提供 rollback-proven 类型结果）。唯一写重试前置条件应是持有原 bundle 且原连接层给出专门 rollback-proven 结果；否则只读查询/返回 pending_unknown，不能再走新事务写入。增加从 atomicConnection 到 Coordinator 的显式 outcome 类型及测试矩阵映射。 |
| BE6-6 | P1 | Coordinator 接收每次调用传入的 planner，和“跨 HTTP 请求 single-flight/cache、进程级恢复”没有所有权约束。若每个 request 构造新的 coordinator/cache，重试会漏掉 bundle；若并存不同 Planner 实例，同一命令的 task/audit 不稳定。冻结 harness 生命周期：每个 case 一个长寿命 Coordinator/Planner/cache，HTTP handlers 共享；cache 满32时 fail closed，不能淘汰 pending_unknown 或 rollback-proven bundle。更清楚的签名是构造时注入 Planner，Start 每请求不再接收 Planner。 |
| BE6-7 | P2 | 失败 sidecar 无数据库唯一性，且“进程启动时先恢复 sidecar状态”要求启动恢复能把 sidecar command HMAC 与新请求中的 command_id 建立确定对应。当前说 key 同目录持久化，但没有定义目录/数据库的生命周期、损坏或截断处理、重复/部分 JSONL 行策略。固定 case-dir 由监督器跨子进程重启保留；启动逐行严格验证，尾部不完整/重复矛盾事件 fail closed；sidecar/HMAC key 丢失时不能误报 not_committed 或允许同 ID 新执行。 |
| BE6-8 | P2 | 进程重启后把 sidecar 中 rollback_proven + bundle 丢失判为 not_committed，这对新 command 可行，但同一个 command_id 必须永久 terminal，拒绝重建 task。当前条款同时写“not_committed并要求新command”与“只有仍持原bundle才允许同ID存储重试”；请分清 rollback_proven 可给调用方的终态与仍持原 bundle 的进程内可重试状态，不能将落在 sidecar 的 rollback_proven 当成无 bundle 的同 ID 可重试授权。 |
| BE6-9 | P2 | 混合 lifecycle 全库完整性合同需将既有 legacy 审计纳入明确流程。当前 auditPilotDataTx 先 loadPilotLegacyBridgesTx、auditPilotLegacyRuntimeTx、FK checks、bridge scope 校验，末尾再全库 auditPilotCreatedDataTx。新 auditPilotDatabaseIntegrityTx 应复用这段 legacy/FK/未知事实检查，逐课分派仅替换 created-only 全库假设；不能只遍历 pilot reservation 后遗漏 legacy bridge/unsupported legacy 状态。 |
| BE6-10 | P2 | “无孤儿 audit”在表结构中无法按 lesson_id 直接连接：audit_logs 只有 TraceID，Args 映射现在带 schema_version/scope/effect_index，但没有 receipt/lesson 的持久映射字段。verifier 可从 receipt 的 decision/trace ID及工具名/两条数量反向绑定；请冻结精确 JOIN/匹配算法以及多条 receipt/重复 trace 的全库拒绝规则，保证混合 A active+B created 时既不会串课，也不会把普通审计行误判为 pilot audit。 |
| BE6-11 | P2 | ordinal=1 的定义只在单用户fixture限制下可证明。若支持同一scope第二 reservation 就拒绝；跨学生各有首个reservation时均为ordinal=1，则需说明 ordinal 是按 student 计算，而不是全DB reservation 顺序。全库计数不得把学生B的首课误判为非首课。 |

## 编码前的修订门

1. 消除时序正文残留冲突，并冻结 unknown → read-only pending_unknown → rollback_proven 的唯一转换与写重试授权。
2. 将 cache key 改为 lesson+command，fingerprint 用于冲突比较；Coordinator/Planner 生命周期固定为 harness case 级共享对象。
3. 把 store 数据/事务协调与 api task/tool adapter 分到各自 package，定义可编译的接口方向和可读 PlanningContext getter。
4. 将昵称快照比较限定为新 command；授权后的已提交 receipt replay不检查昵称。
5. 定义 sidecar case 目录/key 持久性、启动恢复与损坏策略；明确终态失败禁止同 ID 重新规划。
6. 明确混合审计的 facade 清单、legacy/FK 完整性复用及 audit_logs 到 receipt 的逐项关联算法。

本报告是后端/SQLite设计复审，不等于整体设计门通过；未修改代码、未执行测试。

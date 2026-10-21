# lesson.start 持久迁移内核 SDD：后端复审 9

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新稿）。只读复核 BE8-1..3 与此前 BE6/BE7 项，并检查 Coordinator、sidecar 状态、unknown/rollback、legacy/FK 与 audit/outbox 关联的新矛盾。对照 Go auth/store/api 和 SQLite schema 审计边界；未修改文件或代码，未运行测试。

## 结论

**PASS（设计复审通过；实现仍有明确验证门）。** BE8-1..3 已全部闭环，未发现新的后端/SQLite 设计矛盾。BE6-1..11 与 BE7-1..5 在本稿保持闭环。这里的 PASS 只代表本轮 SDD 后端设计合同清楚到可开始实现，不表示已实现、已测试或课堂试点可用。

## 定点结果

| 项目 | 状态 | 依据 |
|---|---|---|
| BE8-1 result_class 枚举 | CLOSED | §42 result_class 已包含 unauthorized、not_supported、schema_invalid；§43 deterministic writer rejection 使用同一类别，sidecar 可记录后再返回。 |
| BE8-2 pilot_marker allowlist | CLOSED | §53/§63 映射要求 pilot_marker，§95 持久 Args allowlist 也含 pilot_marker 且固定值为 pilot-audit-map/v1；全库用此保留键识别 orphan audit。 |
| BE8-3 say payload 权威比对 | CLOSED | §53/§63 明确 say payload.text 与 active room_facts.teacher_text 逐字节比较；receipt 单独验证 lesson/command/decision/kind/version/outbox_count。show_word 逐字段比较同课 frozen_words[0]。 |
| BE6-1..3 | CLOSED | Planner/Registry 在 SQLite writer 外；receipt replay 不依赖昵称；cache key 是 lesson+command，fingerprint/digest 做值比较。 |
| BE6-4 | CLOSED | Store contracts 与 API pilot_e2e adapter 分包；API 私有 task/context 在 API 包构造，cmd harness 组装，Store 不 import API。 |
| BE6-5..8 | CLOSED WITH IMPLEMENTATION GATE | unknown 只做一次回执查询；同进程原 bundle 加 pinned connection typed rollback_proven 可重试一次；case级协调器/cache、sidecar双HMAC与恢复、重启后同ID封口均已定义。实现需提供并验证当前 atomicConnection 尚不具备的专用 typed proof。 |
| BE6-9..11 | CLOSED | mixed audit 保留 legacy bridge/runtime、schema 和 FK 检查；pilot_marker 可识别 orphan audit，逐 receipt/audit/outbox 关联；ordinal fixture 明确 A active 不阻塞 B 的首课 ordinal=1。 |
| BE7-1..5 | CLOSED | writer deterministic rejection 有 terminal_failed；最多两次 commit attempt 且第二次 rollback 封口；保留 marker、payload 权威核对与严格 unknown 重试文案均一致。 |

## 整体一致性核查

Planner Prepare/Execute 的两阶段顺序与 attempt_started durable、Registry事务外执行、bundle_ready缓存、writer内复核提交的时序一致。Sidecar 事件有固定 canonical bytes、HMAC 链、单 case append mutex 和进程独占目录锁；状态转换限定为工具失败终态、最多两次 commit attempt、unknown只读收敛或专用 rollback proof 重试。创建/active lifecycle 分层校验保留旧 global legacy/FK 审计，再逐 reservation 分派；pilot audit 通过保留 marker、receipt TraceID 和 effect index 匹配，outbox 行再与同 receipt 的 lesson/student/version 及冻结事实比对。未发现会让合法 A active+B created 混合库互相误拒的新增合同。

实现和验收仍需验证：

1. pinned SQLite connection 能可靠区分 commit 未知与明确 rollback_proven，并在任何不确定情况下不产生第二次写。
2. Store、API adapter、harness 三组 pilot_e2e 文件在有/无 build tag 下依赖图符合规格，生产 server 不含 route。
3. sidecar golden HMAC、所有 result_class 状态迁移及最多两次写尝试边界按规格逐字节锁定。
4. mixed lifecycle 全库审计识别所有 marker orphan，并按冻结事实拒绝篡改后的 say/show_word outbox payload。

本报告为只读设计评审；没有修改文档/产品代码，没有运行测试，也不替代架构、安全、测试及 Agent 域总评审。

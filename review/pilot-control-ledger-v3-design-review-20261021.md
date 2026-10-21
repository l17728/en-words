# Pilot Control Ledger v3 物理设计评审记录

## 评审范围

本记录冻结 docs/51–53 的物理设计复核结果，以及与之绑定的两份场景清单和授权签名向量。评审只覆盖设计、约束与测试设计；不代表 literal SQLite DDL 已完成，也不代表 v3 账本代码、Linux 原生运行或真实课堂试点已验收。

## 精确快照

| 文件 | SHA-256 |
|---|---|
| `docs/51-pilot-control-ledger-v3-sdd.md` | `5f7a9c8f6da961fd42b4006c5db63b38947bbd592b6e4c76fc990e63b580a394` |
| `docs/52-pilot-control-ledger-v3-physical-model-decisions.md` | `43a8482e2d893a2373c883acf9d1b58d17a98555a7b0ac67fddae1d2bfd69075` |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `47fe7a81970f1031dae37df55e0a4dcaa9480c0e359d4e1b41e03ec30d07086d` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` | `4d9c0729ce74b0eeadd2f296f93771a6407dfdc6d78fd960d0371e144ffbe648` |
| `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `d91f300e1b2dee509c644d99d33d9c1d62f2a56f45aa912c9a5b63aeceabd78d` |
| `review/pilot-control-ledger-v3-authz-vectors-20261021.json` | `1ac497a2fa25db41bb1cfdc8b5fe568e1338df1bf7376ef7544e287ea5786968` |

两份 manifest 内 docs/51–53 源文件摘要与上表一致。schema/fault 清单包含 31 个唯一场景，产品编排清单包含 93 个唯一场景；授权向量包含 11 项。向量使用固定测试种子，产物只含公钥，不含私钥；请求 ID 篡改向量复用原签名并应验签失败，其余有效签名向量及摘要域分隔按固定规则验证。

## 领域结论

- 架构：PASS。分离了永久 epoch tombstone、运行 lease、生命周期、业务结果和清理状态；写事务先建外键父行，再 CAS lease，失败整笔回滚。
- 后端：PASS。精确 schema/fault manifest 与物理设计源哈希匹配。表、索引、触发器、显式列定义和命名不变量 SQL 仍须在 literal DDL 阶段逐项冻结。
- 安全：PASS。签名授权更新先验签、再校验实时授权 fence；精确重放不绕过授权，容量限制只阻止新业务键；计数与阈值事件由唯一跨表 allocation trigger 原子维护。无敏感身份或凭证进入事件/日志设计。
- 测试设计：PASS。最终只读复核再次计算上述精确摘要；确认 31 个 schema/fault case ID 与 93 个产品场景 ID 唯一，source/artifact hashes 匹配；11 个授权向量的有效/篡改签名行为正确。容量并发与漂移、百万行性能、日志 canary、迟到 ACK 和 prepared cancellation 均有具名屏障及观测断言。场景仍是设计蓝图，尚非已执行测试。

## 下一阶段与门禁

下一产物是 literal SQLite DDL 与绑定测试清单：列顺序、类型、NULL/default、CHECK、FK、partial index、唯一允许的跨表触发器、命名不变量 SQL、允许/拒绝的 schema inventory，以及每条规范化语句的摘要。该产物须由架构、后端、安全、测试领域复核并在同一摘要快照上通过，之后才进入账本实现。当前 31 项 schema/fault 测试、93 项产品编排场景及 Linux amd64/arm64 原生文件系统验收均未因本次设计评审而视为已执行。

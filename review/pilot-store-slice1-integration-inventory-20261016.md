# 首片可实施清单：隔离 store 组件，不接生产课堂

状态：最小组件边界已经主审与架构评审批准；`.test-tools/pilot-component-work/server` 隔离源码副本 r2 定向测试已通过 31 顶层/216 含子项，正式 `server/` 尚未集成。精确 16 文件哈希、整合器 r1 失败与修复、选择范围和下一片顺序见 [隔离验收记录](pilot-component-isolated-targeted-20261016.md)。下文“尚未整合测试”及草稿未执行说明保留为初始方案历史，不代表当前 r2 状态；正式源码仍等待当前浏览器冻结验收结束。

## 结论与硬边界

可以先将 schema、严格 codec、opaque plan、created 创建事务作为独立不可由 HTTP 到达的 store 组件落地并测试。这一片只证明隔离数据库上的组件契约，**不授权正常应用启动使用 facade、执行 v2 迁移或创建 pilot 课程**。尚未接齐 legacy 写入口时，在生产数据库上执行迁移/创建是不安全的。

这一片不接 App、main、Router、Room、Timer、Agent 工具注册，不把 v2 表加进 `store.OpenContext` 的普通 AutoMigrate。没有新增 flag 或生产维护命令。首片新增 migration/facade 构造入口建议保持包私有，package store 测试直接调用；以后经整链门批准再暴露受信适配器。是否保留草稿当前导出名称可由主审裁定，但不得存在生产调用点。

## 最小文件与依赖

| 正式文件候选 | 对应草稿 | 首片边界 |
|---|---|---|
| `store/pilot_schema.go` | schema module + 固定 DDL inventory | 私有完整结构检查、显式迁移、审计；正常 Open 不调用 |
| `store/pilot_schema_verifier.go` | verifier | pool 绑定、已提交 DDL 证明、当前数据核验；无成功 scope bool 缓存 |
| `store/pilot_legacy_audit.go` | legacy runtime audit + bridge codec | 显式迁移的完整归属审计；未迁移正常结构检查不额外审旧 runtime |
| `store/pilot_scope.go` | scope row + bridge 私有写 helper | 创建事务依赖的表模型、固定错误与严格桥接解码；不替换现仓储写方法 |
| `store/pilot_creation_plan.go` | creation plan | 固定 config snapshot/revision vector、一致读、opaque 输入与 hash |
| `store/pilot_created_codec.go` | created baseline | 仅 created，禁止 active 状态冒通过 |
| `store/pilot_identity.go` | identity | writer 授权→nonce→首次 scope/事实检查→同事务创建、严格重放与固定 observer |
| `store/pilot_store.go` | facade 的最小子集 | 仅 db/verifier/observer、私有构造及创建能力，不含仓储绑定工厂 |

`atomicConnection`、实际 User/WordBook/Lesson/RuntimeRecord 模型、ConfigPersister、config.NewStore/Snapshot/Revisions 为已有真实依赖。保持 helper 的错误 COMMIT 清理和每连接 PRAGMA 合同，不复制一个不可靠的简化事务实现。

当前 facade 草稿的 `SchedulerRepo(tx)`、`ConfigPersister(tx)`、`ScopeRepo()` 工厂依赖尚未存在的 `pilotVerifier` 字段及 legacy writer 接线，**首片不照搬这些工厂、不为编译而偷偷扩生产仓储行为**。将它们留在下一片草稿。统一 `errPilotSchemaInvalid`/`errPilotV2SchemaInvalid` 的归属或显式关系，避免独立草稿重定义、遗漏导入与重复方法。

若保留精确 DDL SQL 单独文件，使用固定嵌入资源与来源 hash；不得运行时读取 `.test-tools`。草稿 generator、DDL inventory 与正式代码应有单一事实源并锁一致性，避免两份 SQL 漂移。

## 已有测试草稿如何接入

- `pilot-created-baseline_test.go.draft`：仅依赖 created codec 与共同 sentinel，可最先独立运行。
- `pilot-legacy-bridge_test.go.draft`：依赖真实 Open/atomicConnection/RuntimeRecord 与 strict bridge；不需要 HTTP。
- `pilot-v2-schema-verifier_test.go.draft`：依赖真实 schema/bridge/verifier；锁 SAVEPOINT、TxDone、rollback cookie ABA、跨 pool。迁移后分支须补真实迁移 fixture。
- `pilot-identity_test.go.draft`：使用真实用户/词库/词条及显式测试迁移、私有 facade、plan、创建事务；覆盖 nonce、owner、config ABA、COMMIT、双 pool、冻结字段损坏与 observer。改名仅对齐最终私有接口，不伪造成功迁移或 mock scope。
- `pilot-lesson-write_test.go.draft` 与 `pilot-legacy-card-write_test.go.draft`：**留到下一片**，它们依赖现有仓储方法替换与 service/API 门，不得让旧方法运行后声称 scope 测试已完成。

首片迁移测试只作用于 `t.TempDir` 的隔离数据库。应有单独静态调用清单证据证明新增迁移/创建组件没有非测试生产调用点；该检查不能替代真实数据库行为测试。生产 Open 的 schema 等价回归确认它没有新增 v2 表。

## 下一片必须完成后才能启用

1. 所有 legacy 副作用前 scope/bridge 门，含 service 缓存、AlreadyPlanned/Settle/Placement/全范围配置等绕过路径；仅仓储防御不够。
2. legacy 新课 DB 分配 ID、非零 Save owner/mode 门及 failed COMMIT 不发布；批量与 tx-bound 工厂保留 verifier 来源且不每卡全库审计。
3. 启动兼容三门实际实现，显式迁移失败不改变旧库；没有支持版门的旧实例不能同时继续写迁移后的库。
4. 取消选库的生命周期与旧孤儿策略独立通过。当前不可将删除草稿当作已安全的完整功能。
5. 可信 Agent/task 适配器提供 principal；构造 opaque principal 不是鉴权证据。加入精确 scope/schema/审计后才谈工具接线，仍不等于 Room/Teacher/output 整链完成。

只有这些条件及后续整链设计门通过，才允许新增生产迁移入口或启用 pilot 模式。本片组件测试通过时，交付文案必须保留“未集成、不可在生产启动使用”的限定。

## 主审批准的组件边界（2026-10-16，尚未整合测试）

主审与架构批准上述最小子集；构造、schema 检查和迁移保持包私有，使用单独的 `pilot-component-store`，不复制带旧仓储工厂的完整 facade。正式集成脚本只新增 store 组件与测试文件，不改旧方法、默认 22 表、App、路由或工具。当前浏览器冻结验收结束前不执行集成。

新增 created 数据审计包括：持久计划最多 64 KiB，严格字段、重复键、类型及 canonical 字节校验；nonce 为 16 字节小写十六进制，词 ID 唯一且为正，配置 revision 向量完整有序。配置边界在无持久化、无应用回调的独立 config.Store 上复用现有校验，不另写一份范围。冻结配置、词条、创建计划指纹与 scope/lesson/reservation/state 必须自洽；历史审计不以当前账号启停、词库发布状态或当前配置推翻已提交冻结事实，创建重放则仍须当前账号授权。

本片只支持 created：每个 pilot scope 恰有一个创建课次，各课次恰有一个完整 created state；不允许孤立 pilot scope 或挂在 legacy reservation 下的 state。未来 transition receipts、answer claims、active commands、effect outbox、completion jobs 必须为空；任何 active 或未来事实都要求后续专用 codec，不因外键合法就放行。此限制是尚未启用组件的版本准入合同，不能直接用于未来完整课堂而未升级相应校验。

测试先验证真实数据库插入和外键检查成功，再断言不支持状态被拒，禁止将夹具插入失败计作审计通过。当前这些用例仍为草稿，未报告编译或测试成功。

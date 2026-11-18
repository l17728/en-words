# docs36 §15 后端与安全交叉评审

结论：2026-10-16 实际阅读 §15.1–15.3 后，设计通过，无剩余设计阻断。主审已准入为下一片基线；docs37 完整验收后才能开始该片产品实现。本次仅文档复审，没有修改 G1、课堂或启动产品源码，也未执行新模型请求。

原两项实施阻断已收敛：

1. 不再尝试为已有课授予“新课资格”。唯一受信入口在同一个 writer 事务中新建 Lesson、pilot reservation 与 v2 checkpoint，不接受已有 lesson_id；普通排课、重排及旧课均不自动获得 pilot。nonce 在调用前冻结，返回既有课之前仍先验证 owner/权限；同 nonce 不同 fingerprint 拒绝。
2. 明确 `atomicConnection` 的 deferred Begin 不等于取得 SQLite writer。已迁移分支先更新 metadata 单例取得 writer 后读授权、模式和 nonce；未迁移分支先在 runtime_records 执行零行 UPDATE 取得 writer，再复查 marker/schema，防止跨 App 迁移后仍使用启动缓存走旧课堂。

追加 schema 边界也已闭合：legacy 三个创建字段必须 SQL NULL，pilot 以 CHECK 约束保证非空有效；普通 UNIQUE nonce 因 SQLite 允许多个 NULL，可让多个 legacy 课程正常并存。不得实现为全局 UNIQUE 空串。

显式迁移同事务写 marker、metadata、完整 v2 schema；NewApp 一致性读取区分完整未迁移、完整已迁移与任意损坏/读取错误。无 marker 不能靠空测试表推断可教学，有 marker 缺表不能静默自动补空表。G1 已提交事实和 v2 不能混用。该方案不向 docs37 的正常 startupModels 添加 pilot 表。

后续实现必须用真实双独立池屏障锁定：半建课不可见、提交后 Enter 仅走 pilot 门；失败提交无三项残留；App A 缓存 legacy 后 App B 迁移并建 pilot，A 首次 Enter 也不得旧路启动；两 legacy NULL nonce 并存；同 nonce 幂等和跨 owner 拒绝。只检查 SQL 字符串或进程内 mutex 不足以验证这些合同。

本结论是可实施设计通过，不代表统一 Room/Agent/结算链已实现或全双工已可用。

## §16–17 与完整 DDL 追加交叉审

已只读核对现有 SchedulerRepo、App、工具事务和非生产 `.test-tools/pilot-v2-schema-draft.sql`，未执行该 SQL，未修改冻结的 docs37 Go 源码。最终结论：新增设计阻断已收敛为 0，可作为后续隔离 mock 首片实施基线。

此次关闭的具体问题：

- SQLite MAX+1 的两个调用改为数据库分配；零 ID Save 用 Create，非零缺失 Save 明确 not_found，不凭未发现的“历史重放依赖”保留任意 upsert。已有课不能被旧 Save 改 owner/book 或覆盖 pilot。
- `AlreadyPlanned→ReserveCards` 没有 Save，摸底先写草稿后重排，旧结算还可能先改完成缓存。因此只在 Save 拒 pilot 不足。永久 student/book scope 在第一副作用前预留 legacy，未迁移库使用 runtime_records 桥接墓碑；迁移同 writer 导入并保留，pilot 只允许全新 scope。删除业务数据不清墓碑，避免多阶段旧操作中途被 pilot 抢占。
- tx-bound 工具复用现有 writer，不能回原 pool 开嵌套事务。跨范围反馈和 `TruncateLadderSteps` 必须在配置/卡/缓存/通知首写前核全部范围，不能先部分成功后遇 pilot。
- scope 的 UPDATE/DELETE trigger 之外补同键 BEFORE INSERT 拒绝，关闭 recursive_triggers=OFF 时 OR REPLACE 隐式删除绕过永久墓碑的边界；三 trigger 都进入严格 schema inventory。正常服务先同 writer 查询，只有缺失才 INSERT，不依赖 IGNORE/REPLACE 幂等。
- 初始 state 是 created/pending、machine:null、rev0、checkpoint1；授权接管可以改变 generation/epoch 而不开始教学。typed room facts 完整记录租约终态和一次恢复余量；配置冻结使用现有按键 Global/Student revision 向量及内容 hash，防 ABA，nonce 重放先授权并返回历史冻结值。

静态检查未发现 FK 提交循环死结：创建时 active 指针为 NULL，后续 state↔active 回指及 receipt/claim 引用使用声明的 deferred 边界；completion 内部 receipt、summary outbox 和 job done 可同事务发布。该判断仍须真实 SQLite 的失败 COMMIT、双池竞争、schema 损坏、REPLACE/UPSERT 负例证明，不能以本次只读 review 代替运行验收。

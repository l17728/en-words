# Pilot 首片：启动兼容门与取消选库的窄修订提案

状态：供主审/架构评审，未实施、未运行测试；冻结生产与现有验收不变。启动兼容修订可以独立落地，取消选库仍需本文的生命周期屏障，不以数据库删除几行冒称已完成。

## 1. 明确区分三种审计入口

| 调用目的 | 未迁移 legacy | 完整 v2 |
|---|---|---|
| 正常启动/构造 facade/建立已提交 schema 证明 | 仅完整 schema 形态、marker/metadata 缺失组合、G1 冲突检查；不新增 legacy runtime 全量严格归属扫描 | schema 与数据全量严格审计 |
| 显式 `MigratePilotSchema` | 在取得公共 writer 后执行完整 legacy bridge/runtime 归属及相关完整性审计；失败则 migration blocked，零 schema/marker/数据变更 | 完整审计后幂等返回，不降低检查 |
| 普通 legacy 写入口 | writer 内复核当前 schema/marker/当前 scope 或 bridge，不做每卡全库扫描 | writer 内当前数据授权检查，DDL 证明仅按已审核 verifier 复用 |

普通启动不自动调用迁移，也不因 legacy 历史孤儿而关闭原业务。此兼容只针对新引入的严格 runtime 审计；旧产品原有启动检查仍保持。partial v2、marker 与表不一致、G1 冲突等结构问题仍拒绝，不能把所有错误都回落 legacy。

具体草稿接点：`InspectPilotSchema` 和 `pilotSchemaVerifier.AuditCommitted` 不再无条件调用针对未迁移数据的 `auditPilotDataTx`；在得到 `PilotLegacyUnmigrated` 后止于已定义的启动结构检查。在 `PilotSchemaReady` 继续完整数据审计。`MigratePilotSchema` 保留未迁移状态的完整审计，且发生在任何新表、metadata、marker 发布之前。不要用一个被调用方含义不清的“宽松=true”开关分流。

日志分别为启动结构检查、显式迁移审计，固定 reason 可定位 `legacy_runtime_orphan`/`legacy_runtime_invalid` 等类别，不输出 runtime JSON 或原始 SQL。严格 JSON/归属检查的规则没有删除，只是移到正确的准入边界。阻断迁移后用户仍可使用旧模式；不自动修复、删除、迁移未知 key 或孤儿记录。

最低回归：合法历史取消选库留下 canonical 孤儿时，未迁移正常启动仍成功；显式迁移明确失败且原库所有记录/表/marker 不变；v2 已迁移后同类损坏启动必须失败；partial schema/G1 冲突三入口都不被宽免；同库两个实例启动与迁移争用的 writer/缓存证明边界保持。

## 2. 新取消选库必须先使旧执行失效

现接口 `DELETE /student/books/:bookId → Sched.RemoveBook` 不足以承担该操作，需 App 协调入口。前置仍为当前学生/书归属授权和 legacy scope 门。

仅 `Rooms.Drop` 不够：另一实例、已捕获 Room 指针、慢判分或延迟任务可在删除前后继续提交；删除失败后重连也可能重新恢复同课。建议最小持久删除屏障作为独立待批补充：

1. 在同一 writer 下建立/重读该学生-词库的删除操作身份、单调 generation 与 `pending` 状态，取得当时 owned lesson ID 集。重试复用该操作；不因请求重试递增预算或创建另一清理任务。永久 legacy scope/bridge 始终保留。
2. 屏障提交后，拒绝该 scope 新 Enter/Join/课次创建/选库或摸底写，退休本实例所有相关 Room、取消 pending work、关闭旧连接。所有支持版本的异步任务和 Room 持久写在其最终 writer 中复核 generation/状态；单凭入口内存检查不够。不得持 DB writer 等待 Room 锁或等待外部模型取消。
3. 取得 writer 执行清理，重新核对 owner/scope/删除操作身份与当前 lesson ID 集，再按下节顺序删除。旧任务若先取得 writer，先提交的效果会进入清理快照；若后取得 writer，则由持久屏障拒绝，不能重建孤儿。
4. 清理失败时屏障保留 pending，所有旧 Room/回调仍失效；用户可显式重试同操作。不能把退休标记清掉或自动恢复原 Room 来假装回滚内存。清理成功写 done receipt 并保留失效 generation，随后按既有允许重新选库的政策建立新一代操作；旧任务不得因新一代启用而恢复许可。

这一屏障的持久表示、支持版本的每个最终写入口和任务 token 传播，需要单独准确接口评审后实施。不能只加 `deleting bool` 缓存、等待当前实例 goroutine 结束或持锁跨网络；它们无法覆盖跨实例和崩溃。若不接受本轮增加该边界，应将删除完整修复明确延期，而不是宣称仓储清理已经安全。

## 3. 清理事务只处理可证明归属的数据

事务中以 `lessons WHERE student_id=? AND book_id=?` 取得准确 ID，规范十进制生成四种 key：`snapshot/<id>`、`report/<id>`、`settled/<id>`、`agent_result/<id>`。仅使用参数化精确 key 集合，分块删除；不使用通配前缀，不把 `snapshot/01` 等未知/非规范 key 猜成目标记录，也不追删找不到 owner 的历史孤儿。

删除当前目标的 placement 草稿、允许删除的 legacy lesson mode reservations、卡、课程，以及上述已知 runtime records；全部与 done receipt 同一清理事务。完成的课报/结算标记原本会因课程删除失去归属，本提案明确将该次取消选库对应的四类记录一同删除，需主审确认数据保留政策。scope 墓碑和未迁移 bridge 永不删除。其他学生、其他词库、未知 runtime key 保持原样。

任务历史、审计与通用通知不按猜测清理。涉及该课程的活跃结算/逾期任务必须由第 2 节的失效协议阻止后续写；完成历史保留或归档另按现有合同。若任务表要求保留课 ID，则在删除前明确关联策略，不能靠关闭外键掩盖。

内存缓存仅在清理 COMMIT 后按已删除 ID 失效；失败不发布成功、不删读缓存冒充成功。退休旧 Room 是屏障行为，即使清理失败也不撤销，和数据库事务回滚分账。返回结果区分 `deletion_pending`、`deleted` 与授权拒绝，不回显底层错误。

## 4. 必要故障与用户旅程

- 取消正在慢判分的课堂，旧模型完成不能再判分、保存 snapshot 或执行工具；另一学生课堂继续。
- 两独立实例：A 已拿旧 Room/任务，B 建立删除屏障并清理，A 后写被拒；重新选库后旧 generation 仍被拒。
- 四类精确 runtime+草稿+卡+课+legacy reservation 同事务删除，其他 scope、非规范/未知 key 及永久墓碑保留。
- 先删 runtime/卡后故障、真实 COMMIT 故障、取消：清理数据全回滚，屏障 pending 保持，重试完成且旧回调始终失效。
- 屏障后进程崩溃并重启：不能自动恢复旧课；有界恢复/用户重试继续同删除操作。
- UI 真实多用户取消、失败提示、重试、重新选库与重启验证；后台数据库断言只补充用户路径，不替代它。

以上不要求为当前冻结候选再次执行测试。下一阶段先批准具体屏障和数据保留合同，再写真实红绿与 UI 旅程；启动兼容门可先作为独立且更小的草稿修订审查。

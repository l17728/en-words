# 取消选库与 pilot 启动审计的兼容缺口

只读发现；当前冻结生产未改，草稿未编译或运行。不能将本文件当作已执行红测。

实际路径是 `api/router.go` 的 `DELETE /student/books/:bookId` 直接调用 `Sched.RemoveBook`，后者直接调用仓储 `DeleteStudentBook`。现 SQLite 方法仅在事务中删除该学生/词库的 `memory_cards` 与 `lessons`。此路径没有 App 层 Room 退休、运行时缓存清理或 `runtime_records` 清理。

新 `.test-tools/pilot-legacy-card-write.go.draft` 增加同 writer scope 门及 legacy lesson reservation 删除，但仍保留上述清理缺口，并未删除 `placement_drafts`。七个写入口统一 `legacyWrite` 能拒绝 pilot scope，却不能单独解决旧取消选库的生命周期一致性。

因此合法历史操作可能产生已不存在 Lesson 的 `snapshot/<id>`、`report/<id>`、`settled/<id>`、`agent_result/<id>`。拟议启动/迁移审计按 canonical lesson key 找归属时会拒绝这些孤儿，不能将其全部归因为非法外部篡改。活跃 Room 仍可能在删除后重新保存旧快照；仅在删除事务中清一次 runtime 不足以完成修复。

实施前需评审的最小合同：

- 明确取消选库是否允许活跃课堂，以及如何先建立持久失效/退休屏障，阻止已捕获 Room、定时器、异步任务和旧连接在删除后重新写入。
- 只对事务内确认属于目标学生/词库的 lesson IDs 处理已知 canonical runtime keys。不得用宽前缀删除未知 key，也不能靠无 owner payload 猜归属。
- 明确 placement 草稿、已完成课报的保留/删除策略、结算与逾期任务/receipt 的历史合同，不能仅为启动 audit 通过而静默删除历史数据。
- 配置/卡/课程/已知运行时清理及 scope 墓碑的事务边界须具体化。永久 scope 与未迁移 bridge 不得删掉，否则重启后可重新变成 pilot 空 scope。
- 默认旧应用启动是否立即启用严格全量审计，与显式 pilot 迁移是不同准入门。已有合法历史孤儿必须有明确兼容或修复流程，不能不经批准导致旧用户无法启动。

最低新增回归：真实 REST 取消已入课的库后，旧 WS/旧 ticker/慢任务无法再写；重启与显式迁移行为符合已批准合同；另一学生同库不受影响；删除中途及 COMMIT 失败全回滚；原 scope/bridge 墓碑跨重启保留；未知 key 不误删。需同时有真实多用户 UI 旅程，不能用仓储单测替代 Room 生命周期验证。

现已准备的 `.test-tools/pilot-legacy-card-write_test.go.draft` 仅覆盖七写路径 pilot 拒绝、legacy/未迁移正常写、零值字段更新、scope 墓碑及卡先删后课程失败回滚，明确不宣称解决本缺口。

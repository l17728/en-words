# docs36 下一接线片：后端一致性与安全独审

结论：私有 store 组件可作为下一片基础，但当前不能开放迁移、pilot 创建或把 pilot 课交给旧课堂。以下为启用阻断，不表示当前关闭试点的产品已暴露可调用攻击入口。只读审查，未修改产品、未运行测试。

## 当前证据边界

正式 `pilot_store.go:69` 的构造器和 `pilot_schema.go:289` 的迁移函数仍私有；文件末尾明确没有 SchedulerRepo/ConfigPersister 工厂、HTTP/工具适配或自动迁移。默认 Open 的模型表单没有 v2。已有 r5 Go 全量、race 和全部覆盖门通过；浏览器跨批次 12 套/131 套内执行通过，不代表尚未接线的 mode/scope/删除或课堂提交协议验收完成。

文档 §15–18 的分层方向正确。§18 中“草稿/尚未编译”描述应在后续同步时区分：16 文件私有组件已正式落地测试，旧入口接线及其草稿仍未落地，不能整章一概写已实现。

## 启用前 blocker

1. **旧 writer 仍可绕过 scope。** `scheduler_repo.go:120` SaveLesson 仍直接 GORM Save，零 ID 先 MAX+1 并提前回填，非零可改变 owner/book 或隐式插入。卡、草稿、排课/摸底/结算等旧多阶段入口尚无统一永久预约。仅保护 pilot Create 或仅过滤 ListLessons 都会 fail open。新梯度 config scope 检查是保守写保护，不等价于所有 legacy writer 的授权/预约门。
2. **旧课入口无持久 mode 检查。** `hub.go:86` EnterContext 在归属检查后会 NewMachine/Restore/RecoverJudgment/Start，再执行 teacherLifecycle；没有 CheckLegacyLesson/ReserveLegacyLessonExecution。若现在开放 pilot 创建，既有 REST/WS/直接 Go 入口可能将该课当旧课堂处理。必须在可执行 Room 发布前拒 pilot，并在写入前同 writer 复核当前 schema/mode，不能只信 App 启动缓存。
3. **取消选库生命周期和孤儿数据未闭合。** `router.go:207` DELETE 直接调用 Sched.RemoveBook；`scheduler.go:312` 直接委托 repo；`scheduler_repo.go:154` 事务只删 MemoryCard 和 Lesson。此链没有 Room 退休、pending task 隔离或 runtime 清理，也没有删除 PlacementDraft。`lessonexecution.go:111–165` 持久化 report/snapshot/agent_result/settled 四类键，因此删课会留下归属依赖 Lesson 的孤儿。不能用严格审计直接堵住原来合法旧库启动，也不能猜 key 或忽略孤儿。
4. **终态只读豁免不能按状态名推断。** 当前 Enter 的 restored 路仍 RecoverJudgment/teacherLifecycle；Join（`hub.go:230`）仍 cancelJudgment、RecoverDelivery、broadcastActionsOnly，后者在 `hub.go:516` 进入 teacherTask/effects。它们不是已证零 DML 的只读链。SessionTimeout 的 finished/retired no-op 与完成课 Enter/Join 零写是不同合同。若所有 Enter 都预约，会给真实已完成课新增持久写；若只按 phase=lesson_end 豁免，又可能让待结算恢复绕过写门。
5. **在线迁移与未迁移桥接未接入口。** 组件已有迁移 writer 与严格审计，但正式旧入口没有在任何副作用前写入永久 `pilot/legacy_scope/sid/bid`。因此迁移扫描时没有事实的、已经开始的旧操作不能只靠扫描识别。只有所有支持版本入口先桥接并拒绝错误降级，或明确排空旧版本进程且有可验证维护边界，才可开放显式迁移；不能拿“建议停进程”代替现有在线设计的安全条件。

## 迁移兼容合同

正式 `pilot_schema_verifier.go:55–65` 和 `pilot_schema.go:261` 保持三门：未迁移结构检查不新增旧 runtime 全量拒绝；显式 migrate 在 writer 下全量归属审计；ready-v2 严格审计。这个窄兼容修订正确，但目前不是 App 默认启动已经调用的证明。

历史孤儿库应允许原 legacy 运行，显式迁移给出固定 blocked 结果且不改库；不得静默删除未知 runtime。新取消选库需先退休所属 Room、阻断/等待已在执行的任务，再在同 writer 事务核 legacy scope，按事务内取得的 owned LessonID 精确清理四类 canonical runtime、草稿、legacy reservation、卡和课，scope/bridge 永留。失败保留退休状态、可明确重试，旧回调不得重新发布或重建数据。settled 等去重事实删除后的重放屏障/持久墓碑仍需具体接口与故障合同，当前不能声称闭合。

## 最小安全下一片

建议先交付**关闭 pilot 的 legacy 保护接线片**，而非开放创建再补防线：

1. 将受信 pool 绑定 verifier/tx guard 注入仓储；支持已有外层事务，不嵌套取池。实现未迁移 bridge 和迁移后永久 legacy scope 写门，查询失败/取消/BUSY/坏 schema 一律拒绝，不缓存 scope 成功布尔许可。
2. 接卡/草稿/课写入及 service 多阶段副作用前门；修 DB 分配新 Lesson ID、已有课 owner/book 不变及事务失败不发布新 ID。pilot 范围拒绝覆盖直接 Go、工具和 HTTP 共用入口，不能只保护路由。
3. 接课次只读检查和可变执行预约；完成课只读路径先用真实 SQLite 全 DML 计数证明，待结算恢复保持写门。只读结果不是未来可写的凭证。
4. 删除协调器单独完成退休/任务 fence/精确清理/失败重试与墓碑合同；在未完成前迁移和 pilot 工厂仍不可达。
5. 全部旧入口门闭合、跨池迁移与删除回归通过后，才开放受信维护迁移及仅新 scope 的 created-only 创建。仍不启动 Teacher/Room，不把 active checkpoint、receipt/outbox 接线冒充已完成。

最低补验：跨池旧操作预约与迁移竞争；坏 marker 不降级；所有旧 writer pilot 拒绝零部分写；新建 ID 竞争及 COMMIT 失败；历史孤儿库旧启动/迁移拒绝；完成课 Enter/Join/ticket/report 零 DML；删除失败回滚与迟到回调不能复活；两学生真实 UI 隔离。测试应在该片实际落地后单独执行，当前审查未产生新的通过证据。

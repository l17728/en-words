# 新库启动迁移批事务候选：只读评估

状态：方案候选，未改冻结产品、未执行 A/B、未提高启动门。当前独立 D 盘启动样本20.175秒及部分建表失败产物证明需要定位，不证明全部延迟来自迁移提交次数；同时存在 D 盘 I/O 排队。

## 现有源码依据

`store.Open`（store.go:191）先打开连接、设置 WAL/PRAGMA、限制单连接，再对22个模型 AutoMigrate、创建6个配置 revision trigger、DROP/CREATE 词库索引。该段没有外层事务，业务 logger 在 Open 返回后才创建，故迁移中的空日志不能证明进程没有运行。

固定依赖 GORM1.31.2 `migrator/migrator.go:120` 按模型调用 HasTable、CreateTable；CreateTable 为各索引单独执行 CreateIndex。glebarez/sqlite1.11.0 的 Migrator 设置 CreateIndexAfterCreateTable=true。在当前非事务连接上，这些 DDL 是多次独立自动提交，具备同步写放大机制；实际每阶段耗时及提交数仍须测量，不把一次 Exec 回调数冒充事务数。

SQLite迁移器 AlterColumn/DropTable 会走 RunWithoutForeignKey；事务内切换 foreign_keys 不是有效替代方案。已有库不得直接套入新外层事务。

## 建议最小边界

1. WAL/连接 PRAGMA 保持事务外且不降低同步策略。严格查询 sqlite_master，检查查询错误；只有没有非 sqlite_% 用户 schema 对象的新库才进入候选路径。不能仅依赖文件不存在、文件大小或某个业务表不存在。含旧表、视图、部分失败迁移表的库均按已有库路径处理。
2. 新库在既有 atomicConnection 的固定物理连接中再次核实为空，再创建同一模型列表、同一索引和6个 trigger。建议使用显式 Migrator.CreateTable(models...)，避免 AutoMigrate 的 HasTable 布尔接口吞查询错误，以及意外走已有表重建分支；必须用新旧 schema 对照证明结果等价，不能假定等价。
3. 新路径所有 SQL 都用 tx，不能 helper 回到原 db 再取唯一连接。批事务返回前不得发布 ready；失败/取消/COMMIT故障必须整批回滚并沿已有连接清理，关闭失败 Open 的连接池。
4. 并发首启需显式合同：事务内重新判空；遇另进程已创建或 SQLite busy/snapshot 冲突时整次退出事务，返回明确初始化冲突/错误，或在另一次有界操作重新进入已有库路径。不能在持有旧读快照的失败事务中继续 AutoMigrate，也不能无限重试。首版可以保守 fail-fast，并测试下一次显式启动成功。
5. 已有库原迁移行为保留，包括历史单列词库索引修复；新库是否省略刚建索引的 DROP/CREATE，应单独按完整 schema 等价审查，不能顺手改变原约束。

## 可观测与故障门

logger 在打开库前建立或提供不含路径/SQL/数据的启动阶段回调。固定事件 start/completed/failed，固定 stage=open/pragmas/schema_probe/fresh_schema/legacy_schema/triggers/index/finalize/app_restore/listen，含单调 duration_ms、fresh布尔、schema对象计数和固定 reason。最终 total 不能声称是物理 fsync 耗时；finalize 包含 commit/rollback。原始数据库错误不写凭据/用户字段。

回归：空库成功 schema 全量等价（表/列/索引/trigger/FK）；第二阶段 DDL/trigger/index 错误全部回滚；取消与 COMMIT失败后同池/新连接可继续且无半库；foreign_keys=1/busy_timeout=5000/WAL不回退；已有旧表/旧索引/部分迁移表走原路径且保留数据；两个独立池并发新库创建无部分成功；重复启动幂等。前端保持真实冷启动/登录/首启旅程，不换 C 盘或放宽30秒 readiness。

## 轻量同条件 A/B 计划（尚未执行）

获设计批准后用旧冻结二进制与候选二进制，固定同 D 盘父目录、独立新 DB、同 testmode/mock 参数、同 owned PID 生命周期及30秒 readiness/15秒停止门。至少三对交错 A/B，保留每一轮结果与超时右删失，不挑最快值；不加载模型、不运行浏览器/Go全量，不停止外部共享程序。

记录 binary/source SHA、DB路径、启动命令、每阶段时长、ready/退出时刻、exitCode/signalCode、schema清单，以及同期磁盘队列背景。提交次数使用可信事务边界观测或由已固定调用结构说明，DDL次数另列。比较中位数/最大值，三对样本不报告有意义的p95。不设置未经基线批准的“必须提速N倍”；目标是减少新库DDL独立提交，并在原门内可靠完成且数据/故障语义不退化。已有库另做小规模等价回归，不用新库改善冒充所有启动性能已修复。

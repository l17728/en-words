# 正式树 r3：startup 定向 race

结论：失败即停，未启动后续 store race、全量 coverage、vet 或全量 race。没有重试或源码修改，r1/r2 原件保留。

## 命令与结果

- 工作目录：`D:/en-words/server`。
- 本机 Go 命令：`go test -p=1 -count=1 -json -race -run '^TestStartup(SQLiteBusyPrimaryCodeClassification|SQLiteBusyErrorRecognizesWrappedDriverError|TwoIndependentPoolsNeverPublishPartialSchema)$' ./internal/store`。
- 全新构建缓存：`.test-tools/ladder-integrated-go-r3-cache-20261016`。使用已有离线模块缓存、`GOTOOLCHAIN=local`、`GOPROXY=off` 和既有 clang；没有下载依赖。
- 命令 exit 1，总耗时 323.84 秒，主要包含新缓存冷编译。三测试实际运行：2 pass、1 fail、0 skip。失败测试体 0.02 秒；没有 DATA RACE 报告。
- 原始 native JSON 行日志：`ladder-integrated-go-acceptance-r3-20261016.logs/targeted-startup.log`。
- 命令、计数与封口结果：`ladder-integrated-go-acceptance-r3-20261016.json`。其 `candidate_missing_passed` 是通用统计器列出的未执行梯度用例，不代表额外测试失败。

## 可证阶段定位

`TestStartupSQLiteBusyPrimaryCodeClassification` 与 `TestStartupSQLiteBusyErrorRecognizesWrappedDriverError` 均通过。

`TestStartupTwoIndependentPoolsNeverPublishPartialSchema` 在第 648 行失败，固定摘要为 `ready=worker_returned_before_ready`：

| Worker | 实际记录 |
|---|---|
| 1 | open 成功 4ms；pragmas 失败 2ms，StageEvent reason=pragma_failed，最终错误分类 sqlite_busy |
| 0 | open 成功 4ms；随后因测试取消而 pragmas 失败 13ms，reason=canceled，最终分类 canceled |

两个 worker 都未记录 schema_probe。此次屏障前置没有建立，不能报告为两方成功到达 schema probe 后的 DDL/COMMIT 竞争失败，也不是先前的 20 秒无诊断等待。worker 0 的 canceled 是测试收到提前返回后主动取消的结果，不能当成独立产品故障。

pragmas 阶段包含 journal_mode=WAL、busy_timeout 和 foreign_keys 三条语句；当前摘要没有逐条 SQL 标识。因此只可定位该阶段返回 SQLite BUSY 家族错误，不能仅凭本日志断定具体哪条 PRAGMA、锁持有者、文件系统或硬盘原因。没有对 pragma 失败做自动重试。

## 清理与哈希

本次已回收两 worker、检查 pool 关闭后才进入前置失败报告；没有 cleanup 失败输出。专属 `enwords-startup-two-pools-*` 目录运行前后均为空，实际清理完成。

Go 源树 SHA256：`6b6a78ba3f79107085bb1b0974b8e819eec08836b35f05fcc565d47b5afe45c1`。前后源码相同，批准的 34 文件 manifest 仍匹配；完整源清单在日志目录。当前没有可宣称通过的最终全量验收，历史课堂长尾也未被关闭。

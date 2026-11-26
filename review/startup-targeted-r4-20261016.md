# Startup 独立缓存定向 race：r4

结论：唯一一次指定命令退出 0，定向通过。按本轮范围停止，没有运行 store 全包或全量回归，没有重试。此前 r1/r2/r3 失败证据不变，本结果不替代最终全量验收。

## 执行

- 工作目录：`D:/en-words/server`。
- 命令：`D:/en-words/.test-tools/go/bin/go.exe test -race ./internal/store -run ^TestStartupTwoIndependentPoolsNeverPublishPartialSchema$ -count=1 -timeout 60s`。
- GOCACHE、GOMODCACHE、GOTMPDIR、TEMP/TMP 均位于新建的 `.test-tools/startup-targeted-r4-20261016/`，未复用旧构建缓存。
- GOMODCACHE 从已存在本地模块资产复制，耗时 62.36 秒；`GOPROXY=off`、`GOTOOLCHAIN=local`，未下载或安装依赖。
- 指定命令总耗时 184.08 秒，含冷编译。Go 输出包耗时 5.452 秒，不能把冷编译时间当测试或产品请求时间。
- 使用既有本机 clang/cgo 执行 race。没有其他测试、服务或浏览器负载由本验证器启动。

## 原始结果

stdout 原文：`ok  enwords/internal/store 5.452s`（原文件含 Go 默认制表符）。stderr 为空，没有失败或 DATA RACE 输出。命令仅选择一个命名测试、count=1；没有逐测试 JSON/verbose 输出，因此不编造 worker 逐阶段耗时或实际 loser 错误类别。

此通过结果证明本次所选测试的屏障、允许失败类别、资源回收、schema 等断言完成；具体 worker 阶段只在失败时由测试输出，成功日志没有这些细节。

## 封口与清理

- 测试源码 SHA256：`c272437e6c3c85f14b63897eed584fa9c1be16694f5076f3809d74efa1441465`，运行前后相同。
- 完整正式 Go 源码 inventory 前后相同，验证器未修改源码。
- 专属 TEMP 递归条目为空；GOTMPDIR 条目为空。构建缓存及日志作为证据保留，未删除其他路径。
- Go 命令自然退出，未强杀、未重新执行。

机器记录为 `startup-targeted-r4-20261016.json`；完整源码 inventory 与独立 stdout/stderr 原件在 `startup-targeted-r4-20261016.logs/`。本轮仅通过这一指定定向门；原始课堂延迟问题、完整 Go 验收和浏览器验收不由此结果关闭。

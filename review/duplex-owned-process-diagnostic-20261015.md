# G1 浏览器后端生命周期诊断

只读代码/失败产物检查，加一次隔离 owned 进程探针；未改产品、时间门或共享服务，未重跑 Go 全量。

失败产物由浏览器代理定位：`closure-1792051874406-5840` 的录音失效用例 teardown 报 15 秒未退出；`closure-1792052404952-17528` 的 QTY 正向及 `closure-1792052610520-11152` 的 JDG 用例启动 30 秒仍 502。三个 server.log 均空，SQLite 分别只有 3/11/15 个表。此证据与启动尚处于 logger 之前的迁移阶段一致，但不能独自确定磁盘、调度或锁的根因。

## 独立探针

原始报告 `.test-artifacts/owned-start-stop-1792053491806/report.json`，同目录保存 server.log。脚本 `.test-tools/probe-owned-start-stop.cjs`，只对自己创建的 ChildProcess 调用 kill；新 D 盘 DB、端口18189、mock/testmode，没有模型调用。使用冻结 server SHA256 `b73c9a80719f6a7cec401d39d93e7a2c9f44ea0f552114c0f81d76195c50465c`。

- PID7972，readiness 20174.75ms，原30秒门内。
- kill 到 exit 15.55ms；事件 code=null、signal=SIGTERM。
- 退出后的 ChildProcess.exitCode=null、signalCode=SIGTERM，确认只检查 exitCode 会把已信号退出进程当活进程。
- owned 进程已结束，不存在为了通过测试而提高期限或重复试到绿。

## 代码结论与限制

`web/e2e/recovery-server.ts` 原 stop 只检查 exitCode，若先前 exit 已发出而再次进入 stop，就会注册一个永远等不到的 exit 监听并等15秒。原 finally 先 await stop；stop 抛错会跳过日志关闭、mock响应销毁、平台关闭等后续清理。此为确定的夹具可靠性缺口；原失败是否恰好走到该竞态，缺少当时 exit/signal 记录，不能冒称已证明。

`server/cmd/server/main.go` 在 store.Open/NewApp 后才设置业务日志；testmode 跳过 AutoSeed，mock 不发现远程模型。收到信号后 cancelRuntime、srv.Close 并返回，没有等待数据库 drain 的15秒路径。本次隔离退出很快，未见 panic 证据。`store.Open` 执行22表 AutoMigrate、触发器安装和索引重建；没有阶段日志，不能把空日志认作未启动。

主审同阶段 WMI 快照另观察 D 盘队列8、读230次/秒，PercentDiskTime807为并发队列口径而非单设备百分比；C盘无队列。此为环境 I/O 排队证据，与代码/夹具缺口分账，不能由单快照归因某个进程或证明全部启动延迟来自磁盘。不得停止共享程序来取绿，也不未经设计将所有迁移套外层事务（SQLite迁移外键规则须另审）。

后续最小夹具修正由浏览器代理实施：同时判断 exitCode/signalCode、重复 stop 幂等、无论 stop 成败都执行其余 cleanup，并补真实已 signal 退出子进程回归。启动30秒与停止15秒门保持不变。

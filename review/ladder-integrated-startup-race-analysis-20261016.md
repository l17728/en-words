# 合入后 startup 竞争测试失败：只读取证

本附件只读取正式代码和本轮原始日志，未修改代码、未执行新测试。正式验收仍为未通过；隔离 r2 的通过结果不替代本轮失败。

## 可证事实

- 原件：`ladder-integrated-go-acceptance-20261016.logs/race.log`。race 命令退出 1；唯一失败测试为 `TestStartupTwoIndependentPoolsNeverPublishPartialSchema`，耗时 21.63 秒。没有 `DATA RACE` 报告。
- 第一条失败为 `startup_matrix_test.go:355: independent pools did not reach outer probe`；随后 TempDir 清理报告 `compete.db` 被占用。因此未到后续恰好一个成功、schema 完整性和重新打开的断言，不能把它报告为这些产品断言失败。
- `server/internal/store/startup_matrix_test.go:339–348` 启动两个 goroutine，以 `context.Background()` 调用 OpenContext。observer 只在外层 schema_probe 成功时发送 ready，再阻塞于 release。完成结果写入容量为 2 的 results 通道。
- 第 350–356 行等待两个 ready，但不读取 results，也不保存其他阶段事件。因此某一 OpenContext 提前返回错误与某一 OpenContext 尚未完成前置步骤，两者都会表现为 ready 不足。20 秒定时器在每次循环中新建；日志没有记录实际收到的 ready 数量。
- timeout 分支只关闭 release 后调用 t.Fatal，没有取消 context、等待 goroutine、排空 results 或关闭成功返回的 pool。第 360–370 行的正常结果回收和 Close 不会执行。即使两者都 ready，后续 require 失败也可能跳过尚未关闭的资源。
- `server/internal/store/startup.go:194–198` 会关闭未成功发布的 pool；成功返回后 pool 由调用方负责。OpenContext 在探测之前还执行 open/Ping 和 PRAGMA 阶段（第 199–239 行）。所以“未到 outer probe”并不能定位到 schema 创建或 COMMIT。

## 清理竞态结论与局限

测试存在确定的所有权缺口：t.Fatal 只终止测试 goroutine；它不等待被启动的两个 goroutine。关闭 release 还会允许已经到达屏障的一方继续初始化，与 TempDir 自动删除发生并发。成功返回的 pool 被保留在未读取的 results 中，也不会自动 Close。这个代码路径与本轮文件占用错误相符。

但原始日志没有 goroutine 状态、两个结果或失败阶段，故不能断言本轮具体是哪一 pool 占用文件、是否已经成功返回，也不能将这个清理缺口当成最初 ready 超时的根因。最终测试进程已经自然退出；没有通过强杀或重跑获得通过结果。

## 尚未证实的解释

前置 open/Ping/PRAGMA 错误、SQLite 锁等待、较慢初始化、线程调度等均需进一步证据。现有输出不支持归因硬盘、杀毒软件、梯度实现或数据库部分 schema 发布。busy_timeout=5000 的配置值也不是本轮具体锁等待的实测时长。

## 建议的下一步（未实施）

在保持原有双 pool、20 秒等待和 schema/唯一成功断言的前提下，让屏障等待同时观察结果，记录固定阶段/结果分类；将 release、取消、goroutine 结束与所有已取得 pool 的关闭纳入统一且有界的清理。错误不能仅因提前返回就当作允许的竞争失败：没有两方均到达外层探测时，应明确判为竞争前置未建立并保存原因。测试修订须先经主审，不以放宽超时或重跑代替定位。

# 双 Pool 启动竞争夹具调整规格

## 证据

`review/ladder-integrated-go-acceptance-r3-20261016.md` 的原始 race 日志显示：worker 1 在 `open` 后的 `pragmas` 阶段得到真实 SQLite BUSY，尚未到 `schema_probe`；worker 0 是测试取消后的退出。双方 schema barrier 没有建立。因此 r3 不证明产品 schema 竞争失败，也不能将屏障前 BUSY 当作测试通过。

## 目标与边界

只修改 `TestStartupTwoIndependentPoolsNeverPublishPartialSchema` 的数据库夹具，不改生产启动逻辑、SQLite 参数、超时或 BUSY 处理。测试开始前由单个临时连接把空数据库文件置于 WAL 模式，关闭此连接，并断言 journal mode 为 WAL 且用户 schema 对象仍为零。随后启动两个真正独立的 `OpenContext` pool；两者各自完成打开、PRAGMA 和空 schema probe 后才进入屏障。只在双 ready 后释放竞争。若任一 worker 在 barrier 前失败，按测试失败处理并输出该 worker 固定错误类别、有限阶段摘要和清理结果；不得因 BUSY 放行。

## 业务断言

双方都确实观察到空 schema 后，最终仅允许一个 pool 发布成功。另一方可因内层非空 probe 返回初始化冲突，或在写升级时返回已批准的 SQLite BUSY family；取消、PRAGMA 阶段失败、未知存储错误均失败。最终赢家 schema 与独立完整 schema 一致，关闭 pool 后重开数据库仍得到相同完整 schema。该比较验证最终状态无部分/额外对象，不声称隔离验证每个失败事务的独立回滚。

## 验收

先由架构/测试专家评审本规格，再实现夹具。运行新增 WAL 预置断言及定向 `-race`；首个失败即停，保留原始 JSON、worker stages 和目录清理证据。通过后再运行 store race、全量 coverage、vet 和全量 race。现有 ST06 浏览器竞争启动 E2E 已覆盖独立进程端到端行为，不改变其输入或断言。

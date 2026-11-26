# Startup 双连接池 race 失败诊断与夹具修复设计

状态：失败已封存；本设计只针对 `TestStartupTwoIndependentPoolsNeverPublishPartialSchema` 的取证与测试资源回收。未改代码、未重跑。独立评审后再实施。

## 证据与未知项

- 正式树普通全量 coverage 中，同名用例在 0.08 秒通过；正式树 `-race` 中于 20 秒未等到两次 `schema_probe/startup_stage_completed`，单测耗时 21.63 秒后失败。
- race 原始日志没有 DATA RACE 报告。失败行是 `startup_matrix_test.go:355`。
- 超时路径只 `close(release)` 后调用 `t.Fatal`，没有等待两个 `OpenContext` worker 返回，也没有关闭其可能返回的 DB pool；TempDir 随后报告 `compete.db` 仍被占用。这解释清理失败，但不能解释缺少第二个通知。
- 当前等待循环只读 `ready`，不读 `results`；任何 worker 早退都会被错误归类为等待 20 秒超时。原测试没有按 worker 记录 startup stage，因此无法从失败报告识别它停在 open、PRAGMA、初始 probe 还是 outer probe。
- 目前没有证据断定是产品初始化竞态、SQLite 锁、race 放大、机器负载或测试夹具调度造成首次失败。

## 实施合同

只修改该测试及必要的局部 helper，不改 production startup/SQLite 参数，不提高或移除 20 秒门槛，不减少双独立 pool、唯一胜者、完整 schema 和重开恢复断言。

1. 两个 worker 以独立索引和 context 启动；每阶段事件按 worker 记录有界摘要（stage/event/duration/固定 reason），不包含路径或 SQL。worker 运行期间由 mutex 保护摘要。
2. 两 worker 共用一个 20 秒 ready 截止。等待 outer-probe 时同时 select `ready` 和 `results`；worker 在 ready 前返回即报告 worker ID、固定错误分类及最后阶段，不把早退伪装为 20 秒超时。
3. 两 worker 都 ready 后只关闭 release，不取消正常竞争的 context；另设 20 秒结果看门狗（独立资源上限，不延长 ready 门）等待结果。提前退出、ready 超时或结果等待到限时，均先关闭 release 再取消两个 context。
4. worker 始终拥有自己的 DB pool：成功打开后自行校验完整 schema、关闭 pool，再发送只含结果/固定错误分类/schema 摘要的消息。主线程不接管裸 DB，避免 timeout 与迟到结果竞争 Close 所有权。所有断言、`Fatal` 和 `t.Log` 只由主线程在 join 后执行。
5. 所有失败路径再用固定 10 秒 cleanup join 等待两个 worker 终态。测试目录由 `os.MkdirTemp` 创建，不交给 `t.TempDir` 自动删除：两个 worker 均 join 且池均关闭才删；join 未完成则报告 `cleanup_incomplete` 并保留目录，不声称全回收或与仍运行的 SQLite 竞争删除。迟到 worker 退出后不得调用 testing API。
6. 成功路径继续断言恰有一个 opener 成功、另一个收到 initialization conflict、获胜 schema 完整，且主线程恢复打开成功；恢复池须先登记 Close 再作可能失败的断言。原始失败与补强后的结果分开记录；评审批准前不运行新测试，不以重试抹去原始失败。

## E2E 边界

本问题目前是 Go 测试夹具的失败可见性和 goroutine/DB 回收，不是已证实的产品行为缺陷。若补强后发现正式启动行为确实出错，再按证据设计真实用户可观察的启动恢复 E2E；不得为当前未知根因先写会重复同一内部断言的浏览器测试。

## 独立 review 要求

评审确认等待逻辑不会丢失早到结果、所有路径都 join/close/cancel、20 秒产品验收门不变，并明确诊断信息足以区分 worker 的退出错误与阶段停滞。通过后方可修改该测试。

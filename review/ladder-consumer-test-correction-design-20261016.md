# 梯度配置消费用例修订设计

状态：针对隔离候选 Go 首轮结果的测试设计增补；生产代码与调度合同不变。复审 r1 保留：25 个候选测试 23 通过、2 个消费断言失败；两失败由 fixture 无剩余可学内容触发，不是配置事务失败。完整证据见 `.test-tools/ladder-implementation-20261016/_baseline/go-validation-r1/RESULT.md`。

## 问题与目标

`PlanNext` 按设计只把 24 小时内到期的学习卡、boost 卡及 `new` 卡放入课批。新梯度首项为 3 日时，本课失败卡的到期时间为 72 小时；若词库已全部在本课，`ErrNoWords` 是合法结果。因此，声称验证“下一课消费新梯度”的用例必须具有真实、未分配的同库新词。并发测试不可无条件假设两个操作的先后顺序。

## Go 用例修订

- Reload/新 Room 用例：固定 `TestMode=false` 与时钟；在同一真实词库添加有效词条，为其创建 `StatusNew` 卡；显式断言该词不在当前 `BatchWordIDs` 且 `BoostFlag=false`。保留旧缓存、刷新失败、下一 Room 使用新配置的既有断言。结算后断言原课程卡 `NextDueAt-LastReviewAt=72h`，且唯一下一课包含该新词、`OpenAt-FinishedAt=72h`。
- Room/完成竞争用例：同样使用真实剩余 `new` 词；先创建旧配置 Room，再竞争全局配置改为 `[3,4,5]` 与结课。结果必须匹配完整串行顺序：旧配置先结课时，学习卡 due 与下一课 `OpenAt` 均为 24h；新配置先结课时，两者均为 72h。拒绝混合值；断言课报通知恰一份、旧 Room 梯度不变。
- 为避免单次竞争碰巧只覆盖一个顺序，再以 barrier 明确执行“配置先、结课后”及“结课先、配置后”两种顺序；保持当前自由竞争用例作为并发互斥/原子性检查。

## 浏览器用例修订

现 production-schedule A 词库仅含 `cat`；结算后其唯一学习卡 5 日才到期，因而下一课可能合法地不存在。用例顺序固定为：先仅以 `cat` 建库、选库并排当前课；再通过真实管理员导入端点向已发布词库添加第二个词，再经真实学生 enable 路径创建对应 new 卡。前置断言确认当前 `BatchWordIDs` 仍只有 cat，剩余卡状态为 new、未分配且非 boost；不得 SQL 造卡。保持 A 只完成 `cat`；结课后断言新课唯一、包含剩余词、`OpenAt-FinishedAt=5d`。B 的真实 WS/TTS 跟读和隔离断言保持不变。

## 验收与边界

上述只修改测试前置/断言，不调整 `PlanNext` 24 小时 due 窗、梯度策略、持久模型或 API。先复审修订测试设计，再改隔离候选；更新逐项 acceptance mapping/hash；后台执行 targeted 红绿、全量 Go coverage/vet/race及专用 Playwright。保留原 r1 结果，不覆盖失败证据。所有运行资产只读复制须逐文件记源/副本 SHA；不带密钥，不访问共享服务。

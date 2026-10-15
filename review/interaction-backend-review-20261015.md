# 交互性能与可靠性：后端评审与修复状态

日期：2026-10-15。范围为API、课堂WS、SQLite、模型/媒体边界及启动；没有加载模型、修改共享服务。按最终状态整理，初始静态假设不与已测缺陷混列。本轮完整 Go/coverage/vet/race 已通过；浏览器总族由主代理统一收口。

## 本轮冻结树完整 Go 验收

`server/.test-artifacts/final-runtime-coverage-r11-summary.json`：489 个顶层测试通过、1009 个含子用例通过，7 跳过，20 个有测试包通过、1 个无测试包；119.64 秒，vet=0。六个跳过是明确 opt-in 的真实模型/媒体调用，一个是由 Playwright 独立运行的 harness，并非失败豁免。源码前后 SHA256 `d8dffa31bccdc49265483a0e585b768d233be7fd778bfb37c47593d2d0fb6eb4` 一致。

覆盖率沿跨包 `-coverpkg=./internal/...` 同源码块合并计数：API 80.88%、store 85.94%、wordbook 91.50%、scheduler 90.47%、classroom 91.30%、judge 98.36%、feedback 94.77%，其余核心均 ≥80%。race r11 已通过（217.58 秒，同样 489 顶层/1009 含子通过、7 跳过，无竞态，源码指纹前后一致），最终浏览器总族已交接资源，不以这些 Go 结果替代浏览器或真实模型质量门。

本轮交互项的实现收敛与产品全部验收是不同结论。IX16 只增加课堂工具事务阶段定位，历史 15.5 秒延迟没有被宣称已修根因；既有真实模型质量未达标也不被确定性回归绿覆盖。

## 已修实现与定向证据（完整 Go 结果见上）

### IX07 同一WS慢判分阻塞心跳

原ws.go在唯一ReadMessage循环同步调用课堂判分和SubmitFeedback；释放Room锁不等于释放socket读泵。真实旧WS心跳500ms超时见 `.test-artifacts/ws-responsiveness-red.txt`。旧close子例本次通过，不伪称红。

现每连接16容量输入队列、唯一串行业务worker；reader直回heartbeat并检测close，worker执行前重核账号/client/context，旧resume仍FIFO。满队列关闭取消并固定reason日志；退出先cancel/Close，reader最多等10秒，最终Leave和断开日志由worker保证。无新增WS pause协议，初始“pause帧到达”假设撤销；后台SessionTimeout及REST继续通路保留。

7子例覆盖心跳、关闭、队满、重复仅一次判分、换连接、SessionTimeout及忽略cancel超过10秒后晚返回不判分且最终移除client；连同原Runtime/WS族14.472秒通过（`ws-responsiveness-final-targeted.txt`）。500ms仅受控慢模型范围，不承诺数据库锁阻塞下同门。

### IX08 课堂小批次读取整本词库

原wordbook.go的EntriesByIDs全库ListEntries后建map，调用处Hub.Enter还持全局锁；不是逐词N+1。真实红：1000词库请求2个本库词却读1000行（`classroom-batch-red.txt`）。现SQLite book+ID IN、500ID分块，仅取2行；保持顺序/重复/缺失/跨库隔离，空请求不查询词条，后块错误无partial返回。MemRepo直接查map并clone；Hub锁结构未重构。store全包9.661秒、wordbook全包3.568秒通过，架构实际独审通过。

### IX10 快照context与已完成房间无效写

原persistence.go的saveRuntimeContextLocked仅记录ctx却未传SQL，连接池占用时200ms取消仍等约1秒释放。现DB.WithContext(ctx)，失败不更新mem并记录snapshot_failed；只兑现传入context，Background课堂调用不因此自动获得HTTP取消。

原hub.go的SessionTimeout对已完成/退休房间仍写快照，1000完成房间扫描1002次upsert。现锁内finished/retired直接返回，不按LessonEnd相位误跳未结算重试；扫描1002→2次。架构红绿及reviewed基线见 `review/interaction-baseline-20261015.json`，后端实际代码与测试独审通过。SQLite独立写锁busy等待仍约1秒，未声称完全消除存储竞争。

### IX13 登录与初始化小请求保护

身份两入口新增完整正文16KiB/10秒接收期限，超限413、超时408、畸形/null/尾随第二对象400，不再以空凭据认证；固定trace日志不含凭据。保留合法Unicode与keepalive，初始化后HTTP200/code403优先且关闭连接避免net/http drain未完正文；实际server设置/恢复deadline失败503。仅login/bootstrap，不波及导入/媒体。

原四个Content-Length/chunked慢TCP请求12秒客户端超时，四个超量请求旧200，见 `identity-body-red.txt`。补充红锁初始化封禁未及时flush和reset失败误吞，见 `identity-body-boundary-red.txt`。最终Identity与Bootstrap定向14.109秒通过（`identity-body-final-targeted.txt`）；架构实际复核通过；本段为定向时点，随后 r11 全量后端门已通过，浏览器总族待最终回收。

### IX14 大词库导入/启用事务放大与失败原子性

原浏览器 trace 实际为导入完成 99.484 秒、随后大库 enable 等待 74.916 秒被测试总预算截断；“导入尾部死锁”初始推断撤回。独占 C 盘临时 SQLite 直调 Service 的 1000 条导入 4.049 秒、首次启用 2.163 秒，各实际 1000 个独立 `*sql.Tx`，不能与 D 盘 HTTP 当同条件比。原始及经审指标见 `review/import-enable-baseline-20261015.json`，GET 校验 code=0/空数组，p95 使用 nearest-rank。

同旧二进制 `44dc262e…` 的独占 D 盘真实 HTTP 基线复现：100 条导入 8.312 秒、首次启用 9.125 秒；1000 条导入完成 109.906 秒，enable 到整轮 240 秒预算时仍未完成，右删失下界 >110.515 秒，不是产品返回 timeout。停止后保留 753 张部分卡；另一学生 GET 业务正确。见 `review/import-enable-http-old-20261015.json`。没有改 SQLite 同步策略，独立只读连接观测 WAL/synchronous=2 不冒充服务连接内部所有 PRAGMA。

新增事务批量导入/建卡按 100 条分块；非法及重复项仍跳过、输入顺序不变、跨库隔离、既有进度 DoNothing；存储/取消/提交失败整体回滚。3 条旧树红锁存储错误假成功、逐项事务和残留半初始化。进一步真实 deferred FK 在 COMMIT 阶段触发错误，发现 database/sql 标 TxDone 后 SQLite 原连接残留事务：独立连接 count=0、原池 count=3；保留两份红证据。现固定物理连接，GORM 事务失败后同连接独立 2 秒清理，必要时弃连接；driver DSN 为每个替代连接维持 foreign_keys=1/busy_timeout=5000。

最终定向 14 顶层/23 含子用例全绿（api 6.113 秒、store 2.090 秒、scheduler 0.877 秒、wordbook 0.855 秒），包括第二批故障、取消、两独立 SQLite 池并发、mastered/boost/review 不被覆盖、Mem nil 无 partial、无 atomic 接口 failclosed、COMMIT 错误后同池/外部零行及下次成功、error/panic/nested 清理与 replacement PRAGMA。证据 `.test-artifacts/import-enable-atomic-green-r3.txt`。

同 D 盘/真实 HTTP/相同 payload/独占资源，新二进制 `a7924bdbf163266df444f84d2ee459f62c28f77e42700e65210d958a7add25dd`：1000 条导入 422 毫秒、首次启用 563 毫秒、重复启用 296 毫秒；100 条分别 125/156/140 毫秒。另一学生 GET 全部业务正确，1000 条导入/首次启用时最大等待 422/453 毫秒：单连接原子事务仍会短暂阻塞读取，不宣称零影响。旧新各一次测量，不伪称统计性能分位或普遍吞吐保证；旧 1000 条重复 enable 未执行，无该同条件对照。完整旧新报告、二进制/脚本 SHA、清理与独立连接观测见 `review/import-enable-http-comparison-20261015.json`。本节新增生产修复尚需新树完整覆盖/race及浏览器，不能套用下面 IX14 之前 r10 结果。

## IX14 之前的完整回归记录

最终回归过程保留记录：r9 的 coverage/vet 中，vet 为 0、源码前后相同且所有覆盖门达标，但旧 `TestCov_HelpersAndDrop` 仍要求非法 JSON 经空凭据认证返回 401，造成唯一失败。依据 IX13 已审合同，仅将该断言同步为 400（坏 token 的 401 断言保留）；定向通过 3.849 秒。原 `server/.test-artifacts/final-runtime-coverage-r9-summary.json` 和失败明细保留，最终 r10 结果另行记录，不以定向绿色掩盖原全量失败。

r10 普通全量与 vet 已通过，485.94 秒：466 个顶层通过、7 个跳过；含子用例 964 通过、7 跳过；20 个测试包通过，另 1 个无测试包。6 个真实服务/诊断用例仅显式 opt-in 执行，浏览器 harness 另由 Playwright 启动，均未冒充普通测试通过。前后 Go 树 SHA 均为 `33114ad1a4ab5908842a26a6d093b75a0d98f43c4f35ec124f31a9eb6a3699cb`。证据：`server/.test-artifacts/final-runtime-coverage-r10-summary.json`。

跨包 `-coverpkg=./internal/...` 按源码块合并命中：api 80.62%、store 84.21%、classroom 91.11%、scheduler 90.21%、judge 98.36%、feedback 94.77%；其余包均过门，完整分布见同名前缀 `-packages.txt`。r9→r10 API 净少 1 语句：只读对比确认 overdue.go 125/193 的已处理出口与 237/297 的 CAS 竞争出口互换命中，ws.go 77 的关闭 select 出口本次未命中；覆盖口径未变，未据此宣称业务随机错误，也未为追相同百分比重复测试。

同树 race r10 通过（906.09 秒，GOMAXPROCS=2、-p=1）：466 顶层/964 含子用例通过，7 项同理由跳过，20 测试包通过，0 失败/竞争报告，源码前后相同。原始结果为 `server/.test-artifacts/final-runtime-race-r10-summary.json`。运行前段与单 worker mock 浏览器并行出现明显机器资源竞争，后段已停浏览器串行完成；该总时长不是独占性能基准。

## 保留为待量化候选，不是已证明故障

- Hub.Enter全局锁仍覆盖DB读取；agenttransaction.go串行审计事务及配置加载。需大库/多学生并发耗时后决定结构改动，不能删审计提速。
- RunTimersContext仍顺序扫描且无自动淘汰。完成房间无效写已消除；内存长期增长与活跃课timer lateness需另测。
- main.go直接srv.Close而非等待在途请求；尚无本轮数据丢失实证。可另设计事务中SIGTERM和重启恢复测试，当前不贸然更改结算关闭协议。
- StudentStatistics全量cards/lessons后排序、读取最近20报告；需一年/三年数据量与提交答案并发基线，当前不改统计语义或索引。
- 身份以外JSON限额须按导入等业务合同逐一盘点，不能把16KiB推广全局。

这些候选不阻塞已准入修复的完整核心回归；后续有实证再形成独立规格。文本8秒、ASR/TTS20秒、媒体大小/PCM校验、旧turn拒绝和输出背压保护保持。记录区分冷/热、负载、SQL行数/次数与连接池等待；正确性全绿不替代性能或真实模型质量验收。
# IX15 / IX16 定向验收补记（2026-10-15）

摸底旧实现的两个失败已记录于 `.test-artifacts/placement-red-tool-timing-green.txt`：100 张状态未变化卡仍发生 100 次更新；草稿写入失败后卡片状态未回滚。修复后 `.test-artifacts/placement-atomic-green-r6.txt` 中 API/调度共 13 个顶层、26 个含子用例通过，无跳过。覆盖 105 卡跨块、学习/毕业进度字段保护、草稿顺序与副本、取消、延迟外键导致真实 COMMIT 失败、双独立连接池一致性、内存事务回滚，以及 HTTP 成功/保存失败/已保存但排课失败的分类日志。中间 r2/r3/r5 的失败分别来自测试未持久化账号、请求字段拼写、日志扁平字段误按嵌套读取，原报告保留；未据此放宽产品断言。

课堂工具事务新增七个非负阶段耗时字段，内存/成功提交/失败回滚/进入前取消/锁等待五分支通过，独立架构实际审查通过。只增加定位能力，不改变锁、同步审计或 ACK；字段不包含提交后配置 Reload，不代表整个调用总时长，更不证明已消除 D 盘延迟。

供新浏览器专项使用的产物：`interaction-placement-server-20261015.exe` SHA256 `5686ce420d5c2c10a6bcbc8e93d687104e5a956547092b9b2a63ee7638a10bfe`；`interaction-placement-harness-20261015.exe` SHA256 `0b47afff2d672fd7a86ad1d1ff8b87243cca5ae4fe126a0cd9f53b62ac43fc07`。以上为定向阶段产物记录。其后同树 r11 完整 Go 覆盖率/vet/race 已通过（详顶部及末尾），浏览器总族尚待回收；未沿用 IX14 前 r10 结果冒充。


### 2026-10-15 后端最终验收更新：r11普通与race同树通过

已核原始 `server/.test-artifacts/final-runtime-coverage-r11-summary.json` 和 `server/.test-artifacts/final-runtime-race-r11-summary.json`：普通/覆盖率/vet运行119.64秒，489顶层、1009含子用例通过，7项显式opt-in或测试harness跳过，20个测试包通过（另1包无测试），vet退出0，覆盖率全部达门。race运行217.58秒，同样489顶层/1009含子通过及7跳过，退出0，无失败/竞争报告。

两轮源码前后均为 `d8dffa31bccdc49265483a0e585b768d233be7fd778bfb37c47593d2d0fb6eb4`，source_unchanged=true。此前“race待回收”为历史时点，此处为最新状态。116浏览器总族仍执行中，恢复组27条已过但不能替代总族；本阶段尚未封板。真实模型质量缺口、SQLitebusy边界及IX16只观测不等于15.545秒停顿消除的结论保持不变。

# 新库原子启动：后端实施与验收记录

对应 docs37；2026-10-16 主审准入。范围仅真正空 schema 的初始化与启动日志，已有库仍走原 AutoMigrate。没有降低 WAL/synchronous/FK/busy_timeout，没有改变浏览器 30 秒 readiness 或 15 秒停止门，没有修改共享服务。

## 基线与真实红

旧服务保留在 `.test-artifacts/simple-confirmation-server-20261015.exe`，SHA256 `282bd570550cf3e29d3b6c68f9987f4a89e02dd3dd75a6ce3cb81d122c27472f`。浏览器另外冻结副本与同 D 盘输入；原 Open 实现另存 `.test-artifacts/startup-open-before.go.txt`。

先做行为保持的 OpenContext/私有 connector 拆分，仍调用旧 AutoMigrate，未增加事务或取消行为。`.test-artifacts/startup-atomic-red-r2-20261016.txt` 是实际旧行为红：中途表/trigger 故障遗留部分对象，idx DROP 错误被忽略，已取消的上下文仍可成功初始化；失败池未关闭还导致 Windows TempDir 删除锁定。首次 `startup-atomic-red-20261016.txt` 只是拆分后多余 import 的编译错误，不当作产品红。测试已补 cleanup，防止故障断言失败时测试自身遗留句柄，但关闭池的产品断言仍在 cleanup 前执行。

## 实现合同

- `OpenContext(ctx, path, observer func(StageEvent))`，旧 `Open` 使用 Background 兼容；初始化成功返回的 DB 不携带启动上下文，后续业务请求不受启动 ctx 取消影响。
- sqlite_master 严格统计 table/index/trigger/view，精确排除 `sqlite_` 前缀。任意用户对象走 legacy，probe 错误不能当空库。
- fresh 使用已有 atomicConnection 固定物理连接；事务内再 probe，显式 CreateTable 相同 22 模型，全部索引与 6 配置 revision trigger 同事务。内层变非空则 conflict，不落入 legacy，也不自动重试。
- legacy 保留原 AutoMigrate、配置 trigger 与历史 idx_book_norm 复合索引修复；不承诺旧库迁移全回滚。
- 所有 PRAGMA、索引 DROP、pool 获取及连接错误检查；失败/panic 关闭 owned pool，包括 connector 返回部分 DB 与 error 的边界。没有增加外部故障配置。
- 固定 StageEvent 不携带路径、SQL、原始错误。finalize 从迁移 callback 退出计到 atomicConnection 完成，包含 COMMIT 或 rollback/清理；outcome 为 committed/failed/panicked，不把失败泛称物理已回滚，也不把该耗时解释为 fsync。父子阶段重叠不可相加。
- main 由主代理接线：logger 提前，同 startup trace；真实 net.Listen 成功后才 server_started。后台 loop 取消并有界等待后才关闭 pool，超时记录不完整关闭并退出；没有并发关闭仍在使用的池。

## 专项证据

`startup-atomic-green-r1-20261016.txt`：原红转绿，store 5.225 秒 / cmd/server 3.794 秒。

`startup-atomic-green-r2-20261016.txt`：store 27.003 秒 / cmd/server 1.133 秒，exit 0。覆盖：

- 旧 AutoMigrate 空库与新路径完整 sqlite_master SQL 清单一致，22 表、6 trigger，列约束、索引顺序与定义保留；FK=1、busy_timeout=5000、WAL 与 synchronous 策略不退化。
- schema/trigger/index、outer/inner probe、pragma、取消、panic 故障；真实 DEFERRABLE FK 在 COMMIT 才失败，初始化对象全退，失败池不可用，下一次显式 Open 可恢复。
- 既有数据、自建视图、部分旧表、旧单列索引保留/修复；`sqliteX_` 对象不能被 LIKE 下划线误忽略。
- 外 probe 为空后另一连接建表，内 probe 拒绝；两独立池受控竞争只发布一个完整 schema，失败者不返回 DB，显式重启成功。
- main 真实端口占用失败不报完成；实际 bind 后可连接；error/panic 固定隐私日志，同 trace；后台停止已退出、响应取消和超时三个分支。

r3 在上述基础上增加实际 `*sql.Tx` 唯一身份断言，确保所有 fresh DDL 处于同一事务；不拿 Exec 次数冒充独立事务数。另加启动 ctx 不泄漏到返回 DB、部分 open 失败池关闭。`startup-atomic-green-r3-20261016.txt` 已完成：15 顶层 / 34 含子用例 PASS、1 opt-in harness SKIP、零失败；store 19.758 秒 / cmd/server 4.195 秒。架构最后增量实际复核无新增阻断。

三项构建均退出 0，保留旧二进制，已交浏览器独占窗口：

构建后候选基线 `.test-artifacts/startup-candidate-source-20261016.json`：全部 Go（含测试、go.mod/go.sum）树 SHA256 `2387a5c459585f264dba9a04b2455609c9fa66c6c90f000133174b67df21a727`；排除 `_test.go` 的生产源码树 SHA256 `e3af2b203194858c2d0faa71612e8f79cc4cefc7096dae566d1fde0589cb0c34`。完整回归仍需以同树前后封口验证，不将专项结果冒充全量。

| 新产物（仓库根 .test-artifacts） | SHA256 |
|---|---|
| startup-atomic-server-20261016.exe | e06b8c0883cf41a569350a00642c4645e5132cdd091aa22d225e4b8b95552785 |
| startup-atomic-process-harness-20261016.exe | ea5c3401d7d4578d5de55c2dd0ab3ee54ed8f837deb8777150155a12ff4740ff |
| startup-atomic-classroom-harness-20261016.exe | 64788d2f58ccb529173e2d307802b2bfc97a6c1d9fe781960140e2196d0c3d41 |

## 进程验收边界

`TestStartupFailureHarness` 位于 external store_test，通过仅 `_test.go` 的窄桥接调用真实初始化。显式 opt-in、artifact 路径与真实路径限制、loopback 18000–18999、固定八故障；none 模式仅在初始化成功后创建真实 App/Router。两个进程的外 probe 使用 nonce 屏障，不以串行启动冒充竞争。

harness 的 panic 捕获转固定失败用于进程日志脱敏；生产原 panic 传播由单元测试独立锁定。普通 Go 回归增加该项显式 SKIP（由浏览器另行启用），不能记为已执行成功。没有生产 HTTP 故障后门。

以上为 r3 保存点的历史状态；后续验收见下。新库优化不能视为课堂热提交长尾的修复。

## r14 全量覆盖与 vet（清理组合补强前的历史候选）

`server/.test-artifacts/final-runtime-coverage-r14-summary.json`：测试及 vet 均退出 0，359.06 秒，23 包通过，529 顶层 / 1213 含子用例 PASS、8 显式 SKIP、零失败。全部 Go 树 SHA256 `2387a5c459585f264dba9a04b2455609c9fa66c6c90f000133174b67df21a727` 前后不变。

跨包源码块合并覆盖：agent 97.42、api 80.93、auth 89.94、classroom 91.30、config 87.59、duplex 97.60、feedback 94.77、judge 98.76、llmzhipu 100、localmodel 90.48、memory 93.10、obs 92.59、remoteprovider 96.67、scheduler 90.47、scripts 93.91、store 89.86、tools 93.75、voiceclone 94.64、voicecmd 96.15、wordbook 91.50，全部既定覆盖门通过。

八项跳过为显式 opt-in：ProductionFB001Diagnostic、BrowserOverdueHarness、IndependentASRThroughOwnedProductRoute、ChatReal、ChatJSONReal、ChatRefuseLongInput、LocalSpeechQuality、StartupFailureHarness（报告中保留完整 Test 名）。真实服务探针不能由 Mock 回归代替；两个进程 harness 由浏览器显式执行。

主审在 r14 后确认：原 stopRuntime 单测不足以证明 run 清理组合。因此 r14 未启动 race，保留为旧候选证据；后续窄 ownedRuntimeCleanup 与真实资源组合测试必须独立验收，不能引用本节冒称最终同树全量通过。

## 生命周期组合验收补强

`ownedRuntimeCleanup` 是 main 私有资源所有者，run 成功取得资源即登记，仅一个 defer 执行：关闭 HTTP/listener，取消并等待后台，再关闭池。5 秒后台停止失败时固定记录 `shutdown_incomplete/runtime_stop_timeout`，保留仍被后台持有的池；池关闭本身失败记录 `pool_close_failed` 并返回固定错误，不输出驱动底层消息。原始 run error 不被 cleanup error 覆盖，panic 不被吞掉。

这次是已批准生命周期合同的可测性补强，未伪造旧树编译失败为产品红例。`startup_cleanup_test.go` 使用真实 SQLite、HTTP listener 和可控 worker 验证：取消后的最后一次 SQL 可在池关闭前完成；timeout 已关 listener 但池仍可 Ping；pre-runtime/active-runtime 的 error、panic 保持且资源回收；实际 run 的 invalid-provider 与占用端口错误返回后，Windows 可删除 DB 文件。真实 `sql.DB` 测试驱动 Close 错误验证固定失败日志和隐私，不是直接模拟 owned.Close 的结果。

专项 `.test-artifacts/startup-cleanup-targeted-r3-20261016.txt`：cmd/server 4.948 秒、10 顶层 / 21 含子用例 PASS、1 opt-in harness SKIP、零失败。此前 r1/r2 结果完整保留。架构实际复核生产、测试与 harness 为零新增阻断。

`TestStartupCleanupHarness` 仅测试构建启用，真实 App.Router/SQLite 使用同一 production Close；normal/timeout 模式和受控 worker 明确是资源组合验收，不冒称完整 run 故障注入。唯一 stdin `stop`，5 秒 timeout 后先记录池未关闭，再由 harness 释放其自有 worker、关闭池并非零退出；无生产故障路由。普通全量新增此项为第九个显式 SKIP，由浏览器另启用。

新冻结清单 `.test-artifacts/startup-cleanup-source-20261016.json`：全 Go 含测试 SHA256 `b837a6b530fbb0f26bffa970db302d3c3703e3458a00d10f65713cf04c8fdcf4`；生产源码 SHA256 `ed28d45a65ef661ac7ce9b802643d729c87c40b33da342834a1de4335755193e`。该树最终浏览器 14 项及全 Go/coverage/vet/race 仍待后续记录。

## 最终 r15 同树后端验收

上述待验状态已由本节收口。完整 `go test ./...` 跨包覆盖及 vet 均退出 0，330.08 秒；同树完整 race 退出 0，966.12 秒，未报告数据竞争。两次均为 23 包、535 顶层 / 1225 含子用例 PASS、9 显式 SKIP、零失败。真实原始摘要与逐测试 JSONL 位于：

- `server/.test-artifacts/final-runtime-coverage-r15-summary.json`、同前缀 `.jsonl` / `.out` / `-vet.txt`。
- `server/.test-artifacts/final-runtime-race-r15-summary.json`、同前缀 `.jsonl`。

两轮全部 Go 源码（含测试及 go.mod/go.sum）SHA256 均为 `b837a6b530fbb0f26bffa970db302d3c3703e3458a00d10f65713cf04c8fdcf4`，前后封口均 `source_unchanged=true`。浏览器使用的前后端生产组合 SHA 是另一统计口径，不能与此 Go 全树 SHA 直接比较。测试使用本机既定 Go 工具链、隔离缓存、GOTOOLCHAIN=local、GOPROXY/GOSUMDB=off、GOMAXPROCS=2、`-p=1 -count=1`；race 使用既定便携 clang/CGO，未切模型或共享服务。

覆盖采用历史一致口径：`-coverpkg=./internal/...`，按同一源码块合并各测试二进制命中数，非单包局部统计。

| 模块 | 语句覆盖率 |
|---|---:|
| agent | 97.42% |
| api | 80.95% |
| auth | 89.94% |
| classroom | 91.30% |
| config | 87.59% |
| duplex | 97.60% |
| feedback | 94.77% |
| judge | 98.76% |
| llmzhipu | 100.00% |
| localmodel | 90.48% |
| memory | 93.10% |
| obs | 92.59% |
| remoteprovider | 96.67% |
| scheduler | 90.47% |
| scripts | 93.91% |
| store | 89.86% |
| tools | 93.75% |
| voiceclone | 94.64% |
| voicecmd | 96.15% |
| wordbook | 91.50% |

全部既定门通过。9 项跳过为：`TestOptInProductionFB001Diagnostic`、`TestStartupCleanupHarness`、`TestBrowserOverdueHarness`、`TestOptInIndependentASRThroughOwnedProductRoute`、`TestChatReal`、`TestChatJSONReal`、`TestChatRefuseLongInput`、`TestOptInLocalSpeechQuality`、`TestStartupFailureHarness`。它们是显式真实模型或进程 harness，不按未运行测试计成功；startup 两个 harness 已由独立浏览器专项启用验证，旧 classroom harness 仍由完整用户旅程调用。

r14→r15 的 api 净增 1 个命中语句已追溯：新增实际 run 测试命中 SetTestMode；WS done-select return 的关闭交错命中变化；feedbackplan.go 首次冻结 `invalid_date` 少 1 次命中与固定 fakeClock 的并发额度测试相关。两 App 同目标日期，输家 prepare 是否在赢家改 OpenAt 后执行决定进入首次非前向日期拒绝还是后续额度拒绝；用例固定锁定仅一次 applied/总额度 3，允许两种合法交错。不是随机 map 或真实日期引起的产品错误，不为追覆盖值重跑或串行化正确并发测试；后续独立确定性首次规划非前向日期例可补强该分支。

## 浏览器与性能证据的精确边界

最终 cleanup 候选完整启动 14/14 通过、219.006 秒、零 skip/flaky/retry、测试输入及产物前后不变，见 `review/startup-cleanup-browser14-20261016.json`。它包含真实 UI 正常关闭和 5 秒后台超时后保留池的两条组合旅程，但不冒称整个 run 的故障注入。

历史 fresh 候选的 12/12 与固定交错 A/B 见 `review/startup-browser-and-ab-20261016.json`：同 D 盘三对旧 ready 3656/2781/4390 ms，新 ready 1516/1313/1188 ms，三对 schema 完全相等。A/B 使用旧 e06b8c… fresh 候选，不将数据重标成新 cleanup 候选；新清理提取不重复性能实验。未修改 durability、30 秒 readiness 或 15 秒 owned-stop 门；未知 CIM 样本不能用于认定磁盘根因，冷建库改善不证明课堂热 COMMIT 长尾已消除。

最终四产物 SHA 清单 `.test-artifacts/startup-cleanup-binaries-20261016.json`：server `9496f4e18cf648541896f187ba43ab057e5691adb5f7eb10d27a021069c28063`；startup-process `ea5c3401d7d4578d5de55c2dd0ab3ee54ed8f837deb8777150155a12ff4740ff`；cleanup-owned `fbab607792146a3e87a1b06e823c1f118983bf0e28df4d9f465e9b29968e38c8`；classroom `64788d2f58ccb529173e2d307802b2bfc97a6c1d9fe781960140e2196d0c3d41`。Go 重任务已结束并交浏览器独占窗口，原 127 项完整回归尚待其独立报告，不在此提前记通过。

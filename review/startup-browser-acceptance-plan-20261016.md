# docs37 新库启动：浏览器与 A/B 验收实施计划

状态：2026-10-16 主审批准以下 12 例与三对交错 A/B；架构已完成设计及测试工具实际复核。e06b8c候选完整12例、固定6轮A/B已执行通过，详见 [实测报告](startup-browser-and-ab-20261016.json)。后续收尾接线改变生产候选，新增ST09两例；现最终目标为新候选完整14+原127=141，尚待冻结及执行。旧12不复用作新版本通过，旧A/B保持原hash和性能范围。只改测试与诊断工具，不改生产后端或共享服务。

## 冻结与隔离

旧产品 binary 为 `simple-confirmation-server-20261015.exe`，SHA256 `282bd570550cf3e29d3b6c68f9987f4a89e02dd3dd75a6ce3cb81d122c27472f`。专属冻结副本和既有 127 验收 manifest 存 `.test-artifacts/startup-baseline-20261016/`；源码来源是旧验收 manifest，不能用正在修改的工作树冒充旧 binary 构建来源。候选 binary/source/script SHA 在后端冻结后登记。

新独立 Playwright 配置，headless Chromium、单 worker、0 retry；仅本仓库 D 盘新目录，固定隔离端口 18083/18084 与前端 15175，绝不接 8080/5173 或共享 8090。每例沿用 180 秒总预算、启动 30 秒、owned 停止 15 秒、普通 expect 15 秒。启动故障预期退出即记录，不用等满 readiness 才知道失败。不扩大超时、不更换 C 盘、不降低 WAL/synchronous/FK。

成功路径使用真实生产 binary；失败注入只用后端 `_test.go` 专属进程 harness。两者证据明确分开。复用 owned-process 的 exitCode/signalCode、有限 stop、finally 全项清理及 unpipe/drain；日志文件从 spawn 前已打开且持续消费，避免 stdout 管道积压。只停止本轮保存的 PID/子树。

## 12 个独立浏览器用例

| 归属 | 个数 | 用户路径及不可弱化断言 |
|---|---:|---|
| ST01 + ST07 | 1 | 全新 schema 真实首启，页面创建管理员；管理员用页面建立两学生及词库，两个独立 context 登录、选择词库、摸底并入课，真实文字/语音转写课堂请求与另一用户隔离。不得以 Go/SQL 写入代替这些用户操作。只读 schema/PRAGMA 补充证据，不能当浏览器行为本身。 |
| ST02 | 3 | schema / trigger / index 故障分别独立用例：专属 harness 在该阶段失败，固定 stage/reason、无 listen success、端口未 ready；只读数据库用户对象为 0。登录页可见错误且提交禁用。移除故障后同一数据库用正常候选 binary 启动，用户点重试完成 bootstrap/登录。 |
| ST03 | 3 | commit failure / context cancellation / panic，各自真实 test-only 故障；有界进程退出和失败日志，零半初始化对象；同 DB 正常重启后页面可恢复。COMMIT 故障必须真 SQLite/driver 失败，不能 observer 伪造日志；panic 保留原退出语义，不能报告成功。 |
| ST04 | 1 | 旧 binary 用真实 UI 建账号/词库/摸底/课堂记录，停止后以候选同 DB 重启，真实登录及课堂恢复，旧数据/归属保留，日志走 legacy。旧索引/自建视图/部分迁移表的更细 schema 语义由 Go 独立覆盖；本例不冒称覆盖全部 legacy 变体。 |
| ST05 | 2 | 外层 probe / 事务内 probe 故障分别独立：固定 probe_failed，不能 fresh 成功或 ready；移除故障后页面手动重试恢复。与 Go 查询语义测试分账。 |
| ST06 | 1 | 两个 owned harness、两个不同端口、同一个 fresh DB；测试专属有限跨进程屏障让两进程都观察外层空 schema 后同时继续，确有竞争。允许一方冲突退出，不要求两方都 ready；任意成功者只在完整 schema 后可响应。失败方明确退出、不留下额外对象；停止后再次显式正常启动，再真实 bootstrap/登录。屏障自身超时算夹具失败，不改产品门。 |
| ST08 | 1 | 测试自己持有 18083 端口，响应固定 503；先旧 binary 红锁定绑定失败前假 server_started，再候选固定 listen_failed、无 listen completed、非零退出。浏览器只看到故障，不假进入成功界面；释放端口后用户手动重试并成功首启。占位进程是测试资产，不占已有用户服务。 |

ST08 日志隐私作为所有 12 例共同断言：非空同次 startup trace、实际 stage started/completed/failed、有限非负整数耗时；失败后不记该阶段成功或 ready。不含原 SQL、DB 绝对路径、凭据、用户输入。字段白名单/无关阶段缺失由 Go 细粒度测试与浏览器实际进程日志分别锁定。

## 与后端 harness 的最小合同（具体符号由后端选定）

仅测试二进制，显式 opt-in，DB 绝对路径必须在本轮 `.test-artifacts` 下，loopback 端口；不提供生产 HTTP 故障路由。建议环境参数 `ENWORDS_STARTUP_HARNESS=1`、DB/PORT、FAULT 枚举 `schema|trigger|index|commit|cancel|panic|probe_outer|probe_inner|none`。参数非法立即非零退出。fault=none 仅竞争用，不用于生产 A/B。

竞争屏障需测试专属 barrier 目录/nonce，写各自“外层 probe 已判空”的 ready 文件；两个 ready 都到齐后协调器释放一次，等待上限固定且退出时清理本轮资源。observer 只收固定事件，不能借生产 observer 改迁移行为；注入应放 `_test.go` 钩子/driver。store 外部 test package 可组装 API，但仅故障/竞争进程使用；正常恢复一律生产 main，避免以 harness 代替 main 验收。

异常启动时前端已有 bootstrap 查询错误 Alert/重试及禁用提交；验证真实网络拒绝/503与后端退出，不 fulfill 伪造应用成功。预计不需要产品前端变更；若发现真实 UX BUG，先报告设计、补逻辑测试，再单独批准修复。

## 同条件三对 A/B 工具

候选功能专项通过后才运行；固定序列 **A1 B1 / B2 A2 / A3 B3**，六次独立 fresh DB，均在 `.test-artifacts/startup-ab-20261016/` 同 D 盘父目录。相同 `-testmode -providers mock -port <owned> -db <unique>`，同端口串行复用。testmode 不播种七库，故只评价此固定启动场景，不能外推生产 AutoSeed 耗时。

计时从 Popen 前单调时钟至真实 `/bootstrap/status` HTTP200 且 JSON code=0、needed=true；绑定日志仅辅助，不代替 HTTP ready。每次 readiness 30 秒，超时记右删失及实际 cleanup 时长，不将样本删除/补跑；退出门 15 秒，保存 PID、exitCode/signalCode、启动/ready/退出单调时间。普通进程成功退出与 Windows signal 强制停止分开，不伪称优雅关机。

每次保存 binary/source/script SHA、固定参数、完整只读 schema 清单（table_info、foreign_key_list、index_list/index_xinfo、sqlite_master SQL），以及阶段日志。等价比较保留列/索引顺序、唯一性、FK与 trigger 语义；不只数表。新旧阶段观测不对称：旧 binary 没有新 stage 日志时标 unavailable，不补造。DDL 执行次数、事务次数分别注明来源；没有可信边界观测时只引用固定实现结构/后端 Go 证据，不从 Exec 次数推提交数。

磁盘观测在进程启动前准备完成并绑定该轮 owned PID，最多 30 秒有限只读采样；只读 D 盘队列/raw 时间戳和该 PID CPU/IO，不扫描其它服务。采样器缺字段/权限失败/错过 PID均标 unknown，不拖长 readiness；不把格式化整数0当零延迟。采样自身有开销，六次保持同一采样合同，原始时刻可关联但不直接断言 fsync/杀毒根因。

统计保留六个原样本、每对差值、A/B 各中位/最大及超时数；三对不报告可靠 p95，也不承诺速度倍数。候选 fresh 原子性/schema 等价、30 秒门全部满足才过该场景；任何失败按事实报告。历史课堂 COMMIT 长尾与真实声学门不由新库结果自动关闭。

## 执行顺序与质量门

当前只设计/冻结基线；后端短 Go 红绿 → 后端交付冻结候选及 test harness → ST08 旧红、12 例专项新绿 → 独占三对 A/B → 全 Go/vet/race → 完整既有127+新增12（预计139，按发现清单核精确数）。所有重任务串行交接，失败保留，不赌重跑，不提交或推送。

主审补充：完整12例专项若与后续127例在同一生产/二进制/相关测试输入冻结树上，可组成139条唯一用例的分批总验收；不机械重复已通过完整族，不拼零散 case。新12导致生产修订时，失效证据按新树重新验收。

工具开发验证：`test_owned_io_observer.py` 最新6条通过；`test_startup_ab.py` 4条通过。A/B元数据单测首次2条在Windows临时目录清理时因 SQLite connection context manager 不关闭连接报 WinError32，随后 runner 与测试统一 `contextlib.closing`，完整4条通过。该项是测试工具开发缺陷，不是产品启动测试结论。实际A/B以及新旧端口占用红绿仍待执行。

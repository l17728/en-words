# 新库启动性能规格与SDD

状态：2026-10-16主审批准实施，开发及验收进行中，尚未执行候选A/B。上一阶段docs38已完成独立验收并冻结证据，本阶段解除产品代码冻结。依据`review/fresh-database-startup-proposal-20261015.md`及store/main实际代码，严格保持本文的fresh/legacy边界。

交叉评审记录：2026-10-15后端/安全/测试实际复审通过，确认fresh严格判空、同事务、legacy保护、schema等价、COMMIT清理/owned pool关闭、并发fail-fast与原时间门，无新增设计阻断。2026-10-16主审确认实施准入；设计通过不代替开发或性能验收。

## 1. 问题事实与目标

独占同D盘新库启动样本为20.175秒；其它失败运行出现部分建表产物，环境曾有磁盘I/O排队。这些证据支持继续定位，不能证明全部延迟来自DDL提交，更不能把夹具退出码误判与迁移性能混为同一BUG。

目标是减少**真正空schema新库**的独立DDL自动提交，并提高初始化原子性。保留原30秒readiness与15秒owned停止门，不换C盘、不降低synchronous/WAL持久性、不删除历史数据、不以新库改善宣称所有启动场景已解决。产品热路径和全双工教学不在此范围。

## 2. 改前实现依据与明确非目标

改前保存点的`store.Open`（当时store.go:191）打开SQLite，设置WAL/PRAGMA及单连接池，然后AutoMigrate 22个模型、安装6个配置revision trigger、DROP/CREATE复合词库索引；当时没有外层迁移事务，部分Exec/DB错误未检查。旧`main.go:42`调用Open，业务logger在51行才创建。上述行号与缺陷为历史依据，不是本轮候选现态；迁移期缺日志不能解释为进程闲置。

2026-10-16候选已落`store/startup.go`与`cmd/server/startup.go`：fresh单固定连接事务、legacy原迁移路径、固定阶段日志、真实绑定后ready和有界runtime收尾。架构已实际审阅，当前无新增代码阻断；后端r3专项15顶层/34含子PASS、1 opt-in harness SKIP，store19.758秒/cmd-server4.195秒，包含部分打开pool关闭、真实单事务与返回DB不继承startup context。详见review/startup-backend-review-20261016.md。浏览器12例与三对A/B结果分别回收，全量仍待；不以设计或专项绿替代阶段验收。

固定GORM1.31.2 migrator逐模型HasTable/CreateTable，索引另执行；glebarez/sqlite1.11.0配置CreateIndexAfterCreateTable=true。该调用结构有多次DDL自动提交可能；Exec计数不是实际事务次数，必须分别报告。

已有库的SQLite AlterColumn/DropTable可涉及RunWithoutForeignKey。事务内PRAGMA foreign_keys无效，因此**禁止直接给所有AutoMigrate套外层事务**。本方案不更换driver/GORM、不重做已有库迁移、不减少FK/唯一性检查，也不增加全局重试。

## 3. 精确新库识别与路由

新增内部严格schema探测，直接查询sqlite_master并传播错误：统计table/index/trigger/view，排除名称真实前缀`sqlite_`的SQLite内部对象。使用`substr(name,1,7) <> 'sqlite_'`等精确前缀条件，不能用未转义LIKE下划线误排除用户对象。

只有用户schema对象数=0才判fresh候选。文件不存在、文件长度=0、某业务表不存在、HasTable返回false均不能代替严格查询。含任意用户对象、旧表/视图/历史索引、部分失败迁移产物的DB均属legacy。

1. 连接池建立、WAL与每连接busy_timeout(5000)/foreign_keys(1)设置在事务外；逐个检查错误。synchronous保持当前策略，不为A/B改变。
2. 事务外严格probe确定路由。fresh进入固定物理连接atomicConnection；事务内再次probe确保仍空。
3. fresh事务使用显式Migrator.CreateTable，按与原Open相同的22模型、索引和trigger定义创建。不调用AutoMigrate的HasTable分支，也不访问全局db另取唯一连接。
4. 若内层probe已非空或写升级BUSY/SQLITE_BUSY_SNAPSHOT，整事务退出并清理，返回固定初始化冲突/存储错误；首版fail-fast，不在旧读快照里继续legacy迁移。下一次用户显式启动重新probe，可以走legacy。
5. legacy仍走原AutoMigrate、trigger安装及历史idx_book_norm修复，不额外包事务。错误检查和启动日志可共用，但不得声称legacy失败全回滚。

新路径完整schema必须与原路径在独立空DB输出等价：表/列类型、NULL/default、PK、FK、唯一/普通索引、trigger SQL。可规范化SQL空白比较，但不能丢弃索引字段顺序、唯一性或trigger语义。新库是否免掉刚创建索引的DROP/CREATE需基于该等价证明；首版可保留同定义以缩小变更。

## 4. 接口、事务与失败合同

保留`Open(path)`调用兼容，新增内部或导出窄接口`OpenContext(ctx, path, observer)`；Open委托Background及无操作observer。observer仅接收固定StageEvent，不拿DB/原SQL/用户路径；main在Open前创建logger并建立一次startup trace，传入回调。禁止以observer改迁移行为。

fresh整个schema、索引及trigger都使用同tx；atomicConnection确保COMMIT错误同连接清理，清理失败丢弃连接。遇普通错误、ctx取消、panic时整批回滚，不返回可用DB。失败Open关闭自己创建的连接池，不关闭外部共享服务；不能把失败半初始化对象发布给App。

COMMIT成功才返回DB，随后App构造/恢复完成才允许HTTP监听。启动失败固定stage/reason可定位；不输出原SQL、连接串、数据、密钥或用户目录。底层error可向调用者包装，但正常产品日志必须固定分类，不直接打印可能含路径/SQL的原文。

现有atomicConnection使用BeginTx，fresh内层先读后写仍可能遇跨进程升级冲突；该结果按上述fail-fast处理，不隐含BEGIN IMMEDIATE保证。并发启动成功者必须完整schema，失败者不得留下额外部分对象。第二次显式启动应可恢复完成，不宣称两进程都会成功。

## 5. 启动阶段日志

每次启动生成非空startup trace；固定module=startup、event=`startup_stage_started`/`startup_stage_completed`/`startup_stage_failed`。stage枚举：open、pragmas、schema_probe、fresh_schema、legacy_schema、triggers、index、finalize、app_restore、listen。阶段只出现实际执行分支；fresh字段在probe后才可知，不提前伪填false。

payload允许stage、duration_ms（单调计时非负）、storage_mode、fresh（已知时）、schema_object_count（已成功探测时）、reason固定枚举、transaction_outcome。reason枚举open_failed/pragma_failed/probe_failed/initialization_conflict/schema_failed/trigger_failed/index_failed/commit_failed/canceled/restore_failed/listen_failed。

finalize独立测量commit/rollback及清理，不能把整个迁移耗时都叫commit_ms；fresh_schema与其子阶段重叠时报告层次，禁止直接相加。listen成功应以真实绑定成功为准，不在ListenAndServe之前宣称ready；从首stage到ready总耗时另列。不能把total或finalize解释为物理fsync耗时。失败阶段至少一条terminal事件；panic路径也须有有限清理与失败观测，之后保持原panic/退出语义。

schema探测/DDL计数仅测试观测，不输出完整schema数据到产品日志。性能工具可在隔离报告保存schema清单和数据库位置，但不混入产品日志。

## 6. 单测、集成和E2E矩阵

| ID | Go/SQLite合同 | 浏览器/进程验收 |
|---|---|---|
| ST01 fresh等价 | 老/新独立空库schema完整对照；22模型和6trigger实际验证，不硬计自动索引名数量 | fresh启动后真实bootstrap、登录、词库/课堂基础旅程 |
| ST02 fresh故障原子 | 中间表、trigger、索引逐阶段故障：所有用户对象回滚；同连接与新连接可查；关闭失败pool | 测试专属故障启动不ready、固定日志；移除故障后真实首启成功 |
| ST03 COMMIT/取消 | 真COMMIT失败或受控driver提交失败、ctx前/中取消、panic；固定连接清理与discard，不遗留脏pool | 进程失败/恢复路径不永久加载；不通过延长readiness掩盖失败 |
| ST04 legacy保护 | 旧数据/旧单列索引/部分迁移表/自建视图不误判fresh；数据和最终schema不退化 | 既有账号/词库/课堂重启恢复正常 |
| ST05 probe失败 | sqlite_master查询失败不能被当空库；事务内重探测失败全退 | 日志probe_failed而不是schema成功 |
| ST06 双进程首启 | 两独立池屏障竞争，最多完整成功；冲突整退；显式重启成功 | owned两个隔离进程/真实请求验证无半ready，不操作共享服务 |
| ST07 连接参数 | 新连接/失败后replacement的FK1、busy5000；WAL及同步策略不回退 | 现有首启和多学生真实写入回归保持 |
| ST08 日志隐私 | 非空trace、正确stage、非负耗时、失败不发成功、无原SQL/路径/输入 | 真实冷启动日志与ready时刻可关联；UI不得显示错误成功 |
| ST09 自有资源收尾 | 真实pool/listener/受控worker调用同ownedRuntimeCleanup.Close：正常加入后关池、5秒超时不提前关池；run唯一defer装配独立review | normal/timeout两例：真实Router建号，stdin stop后错误可见，正常main同DB重新登录；test harness不冒称完整run注入 |

故障注入仅_test.go/harness或独立进程工具，不新增生产HTTP后门。单测/集成与E2E不能互替。最终按03门槛及全Go/vet/race、全部浏览器回归封板；历史旧树通过不计候选通过。

## 7. 同条件性能A/B（已执行，结果见§9）

设计批准且候选功能测试通过后，使用旧冻结binary与候选binary，固定同D盘父目录、每次唯一新DB、同mock/testmode参数、owned PID生命周期。至少三对交错A/B，序列预先记录；不并跑浏览器/Go全量/模型，不停止系统外部服务。

每轮记录source/binary/script SHA、schema清单、启动总时长/分阶段时长、ready结果、退出时刻、exitCode/signalCode及磁盘队列背景。保留全部失败与30秒超时右删失，不挑最快结果。DDL次数与独立事务次数分开：后者用可信事务边界观测或严格说明固定实现结构，不用Exec回调冒充。

报告中位数/最大值及原始每轮，三对样本不宣称可靠p95或普遍加速倍数。验收门：fresh原子性和schema等价全部通过；候选在原30秒门内可靠ready，已有库语义不退化。若环境使双方超时，报告未满足门并定位，不放宽门。无证据的启动全部瓶颈、App恢复耗时、磁盘后台排队仍分别保留，不宣称本改动一并修复。

## 8. 实施前批准点

根/后端/安全/测试已确认实施约束：fresh精确判空、同tx无外部DB调用、legacy FK行为不变、COMMIT清理/失败pool关闭、startup logger时序与固定隐私字段、并发fail-fast和A/B边界。开发先保存旧行为失败证据，候选通过后再执行冻结序列A/B。

### 2026-10-16 启动入口补充评审

- `main`只负责最终退出码；实际装配在返回error的`run`中，使已成功打开的自有pool在后续失败或panic时关闭，避免`os.Exit`绕过defer。
- store观察事件与app_restore/listen使用同一startup trace；app_restore只测NewApp恢复，不冒充整个启动。首启播种仍保持既有非致命行为，真实监听必须在播种结束后；`startup_total_ms`覆盖本轮完整装配到绑定。
- `net.Listen`成功后才记录listen完成及server_started。后续`Serve`失败单记固定`serve_failed`，不能抹掉已成功绑定事实，也不输出网络地址错误正文。
- 保留30秒readiness、15秒owned停止门。端口被占旧版假ready需以真实进程锁定红例；新版验证失败无server_started。新增日志与生命周期分别由Go测试和浏览器/进程验收覆盖。

### 2026-10-16 后台循环与pool收尾补充

主审、架构与后端针对组合行为测试缺口批准窄收尾对象 `ownedRuntimeCleanup`：仅持本次run成功取得的pool/server/listener及runtime取消和done；run最早注册唯一defer，资源取得后登记。Close先关HTTP监听，再取消并有界等待两条后台循环；超时返回固定错误且不关pool，正常退出才关pool。runtime未启动时只释放已取得资源。run仅在原error为空时采用清理error，不recover，原panic保持。无需App/模型通用注入框架或生产故障接口。真实SQLite池、监听器和阻塞worker屏障的组合测试锁定正常/错误/panic/超时；既有helper单测与源码审查不能替代此证据。原12及A/B保留原hash，修订后的候选另做验收。

`run`的新pool关闭不能与仍在运行的Tick/SessionTimeout或逾期扫描竞跑：两条自有runtime loop必须有done/WaitGroup。退出先取消runtime，再以固定5秒等待这两条循环终态；成功才允许后续pool.Close。此门低于owned停止15秒，不改变启动或业务请求时限。绑定之前的错误仍直接关闭已打开pool。

等待超时记录固定`shutdown_incomplete`、`reason=runtime_stop_timeout`，跳过pool.Close以免继续用池与无界Close，正常返回路径以error退出进程；已有panic保持原样传播。`shutting_down`只表示意图，`runtime_stopped`只证明这两条后台循环退出，不声称HTTP/被劫持WS及所有请求已优雅清空。最终实现以单一ownedRuntimeCleanup收尾替代先前两个defer及允许关闭标志，在同一函数内保证join成功先于pool.Close。

设计要求用短注入deadline锁定正常done、超时不关pool、后置错误与panic仍清理/原panic传播，不得靠真实等待5秒替代同步屏障。原保存点仅有stopRuntime/helper测试及main静态审，缺少组合资源实证；现已提取窄ownedRuntimeCleanup并补真实SQL池、监听器与worker的组合用例，以及真实run后置失败释放测试。最终代码哈希、完整测试及浏览器结果以新增验收报告为准；不将调用共享Close的测试harness称为完整run故障注入。

pool.Close自身失败另记`shutdown_incomplete`及固定`reason=pool_close_failed`，返回固定清理错误，不能将底层driver错误正文写日志。此时已完成的runtime_stopped事实仍成立，但不表示数据库关闭成功；测试driver注入关闭错误验证定位与隐私。

## 9. 2026-10-16 浏览器专项与固定 A/B 实测

历史候选阶段：新启动族完整 **12/12**，0失败、0跳过、0 flaky、每例只通过一次，189.375秒；原127族仍待候选全Go结束后回归，故尚不宣称139条总验收完成。测试设计与实际工具经主审、架构复核；完整证据和12个唯一test ID见 [启动浏览器与A/B报告](../review/startup-browser-and-ab-20261016.json)。

生产组合SHA256 `5ad902ba2e06917cb9d3003c42e3cfb12bfbafbf1b97090f349309d99dbe458f`，候选server `e06b8c0883cf41a569350a00642c4645e5132cdd091aa22d225e4b8b95552785`，启动test harness `ea5c3401d7d4578d5de55c2dd0ab3ee54ed8f837deb8777150155a12ff4740ff`。专项前后生产及相关测试/工具hash一致。12条真实UI/进程覆盖ST01–08；正常恢复走生产main，八类故障和竞争屏障只在测试harness。

ST08有效旧红使用同一双栈占位：`::`双栈exclusive监听，并先验证IPv4、IPv6都返回固定503。旧版真实日志先记`server_started`，随后`listen_failed`，准确失败于“绑定失败不得宣告启动成功”断言；新完整12中的同例通过。更早IPv4单栈占位未排除IPv6并存，明确撤销其产品红归因。首轮候选诊断因AntD“重 试”accessible name和reload后需重选词库的夹具错误被主动中止；该轮不复用任何case为最终成功。

固定次序与原始HTTP ready耗时（毫秒）：

| 次序 | A1旧 | B1新 | B2新 | A2旧 | A3旧 | B3新 |
|---|---:|---:|---:|---:|---:|---:|
| Popen→HTTP200、code0、needed=true | 3656 | 1516 | 1313 | 2781 | 4390 | 1188 |

每轮都是同D盘父目录下的新DB、mock/testmode、无生产AutoSeed，30秒启动门与15秒owned退出门保持。全部六轮ready，无超时、无删除或补跑样本；三对完整schema（SQL、列、外键、有序索引与trigger）严格相等，binary前后hash不变。旧组n=3，中位3656/最大4390；新组n=3，中位1313/最大1516。该小样本不报告可靠p95，不外推生产播种全首启或所有机器；日志嵌套stage不相加，也不从DDL次数冒充事务数。

IO观察器六轮均有界退出，但共35个CIM样本全部`sample_timeout`、process为`missing_or_unknown`，有效disk样本为0。只能确认采样未知，不能凭本轮归因fsync、OS调度、文件锁或杀毒。原始jsonl和各stage日志保留于`.test-artifacts/startup-ab-20261016/`。历史课堂22.6秒COMMIT长尾以及真人声学/模型能力缺口不因本次新库启动结果关闭。

离线工具测试：observer 6条、A/B元数据4条通过。A/B工具开发初测因Python SQLite连接context manager不自动close而触发Windows临时目录句柄清理失败，统一显式close后完整4条通过；该失败与产品结果分账。完整139最终可由同冻结候选下的新12与后续原127完整族组成，不重复无变化的已通过专项，也不拼零散case。

### §9后继版本：ST09收尾接线补充（完整14已通过，原127待回归）

上节12/A-B属于明确记录的e06b8c候选。后继生产加入窄`ownedRuntimeCleanup.Close`并由`run`唯一defer调用，补正常/超时两条ST09，要求新候选**完整14+原127=141**。两条浏览器用测试专属main harness组装真实Router/SQLite及production cleanup，stdin只允许stop；观察正常pool关闭、超时pool未提前关闭及固定日志，再验证正常产品main同DB恢复已有账号登录。它们不声称调用完整`run`；完整装配顺序由主审/架构review及Go合同检查分账。生产5秒runtime等待、15秒owned退出与30秒启动门不变。该后继版本不沿用旧12当通过，也不为收尾小改重跑旧性能A/B；旧A/B结果只支持上节记录的旧候选。

当前后继候选完整启动族 **14/14** 已通过，219.006秒，0失败/跳过/flaky，每个唯一test ID单次通过。证据见 [cleanup浏览器14报告](../review/startup-cleanup-browser14-20261016.json)。生产组合SHA为 `00fbaba5ce4a2daa39f200021c853727b24a177c4657b6724391db7a96daf63e`，server为 `9496f4e18cf648541896f187ba43ab057e5691adb5f7eb10d27a021069c28063`，源码与三个测试输入前后不变。一次错误工作目录导致CLI配置文件未找到，零测试/零后端，原日志保留；修正调用位置后执行完整14。ST09正常和超时均经同DB正式main恢复已有账号登录。当前已交回后台全Go窗口，原127未在本候选执行，故尚不声明141总验收完成。

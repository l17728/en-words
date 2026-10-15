# 交互架构、安全与 Agent 独立评审

2026-10-15；评审基线为本轮 IX 修改前的当前代码。只读检查真实调用链，不运行模型或更改平台。行号指评审时文件；新实现须另行实际复核。历史验收通过不等于交互性能或模型质量通过。

## 具体发现与验证门

| 优先级 | 定位与事实 | 复现、门槛及修复边界 |
| --- | --- | --- |
| P1 / IX07 | `server/internal/api/ws.go:203` 同一 ReadMessage 循环同步执行答题、语音反馈和 SubmitFeedback；`roomcontext.go:42` 仅释放 Room 锁，不释放读泵。 | 阻塞可取消 L2，使用同一真实 socket 发心跳、关闭连接：模型未释放时心跳须在 500ms 内返回，断连取消在途请求，旧结果不得结算。暂停控制若为新增协议须明确记录，不能冒充既有接口的红例。采用一个读泵和一个有界串行业务 worker，禁止逐帧无限 goroutine。 |
| P1 / IX05（与前端 IX-B02 合并） | `web/src/api/client.ts:60–70` fetch 和响应体无总期限；`web/src/ws/client.ts:48` authorize 永久 pending 时也无法进入重连 catch。 | 分别挂住响应头、响应体，普通请求 30 秒总期限退出；调用方取消与超时分型。保存、登录、课程表必须出现明确恢复状态。写操作可能已提交，不自动重放；反馈保持原 ID 查询。旧 token 的迟到 401 不清新会话。 |
| P2 / 待测 | `web/src/ws/client.ts:55–56` 定时发 heartbeat，没有对应 ACK 期限。 | 下行黑洞但浏览器 socket 仍 OPEN 时，发送成功不等于服务器可达。用代理只丢服务端帧验证假在线时长；需先冻结 liveness 门，再设计恢复，不能把正常 onclose 测试当此故障覆盖。 |
| P2 / IX08 | `server/internal/wordbook/wordbook.go:375` EntriesByIDs 整库读后筛选；`api/hub.go:89` Enter 持 Hub 锁包含该读取。 | 固定 50 个 ID，对 1千/1万/10万词库记录返回行数、SQL、分配及另一学生进课延迟。按 book+IDs 批量查询，保持输入顺序、重复和缺失语义；不是 N+1 问题，不借优化改变题序。 |
| P2 / 待测 | `server/internal/api/agenttransaction.go:41–45` 全局写锁及逐工具事务；`hub.go:333–356` Room 锁内逐效果调用；工具日志有结果但缺锁等待/事务耗时。 | 10 个独立课堂加反馈，隔离 SQLite 占连接/写锁，记录 p50/p95/max、锁等待与事务耗时。上下文能取消 SQL 不等于能取消 sync.Mutex 等待。不得移除 Agent 工具 scope、审计或原子事务来提速；证据不足时不盲改架构。 |
| P2 / 待测 | `server/internal/api/router.go:32` 等普通 JSON 入口无 body 大小限制；`server/cmd/server/main.go` HTTP Server 仅配置 ReadHeaderTimeout。 | 受控超大登录/慢 body 验证资源与无关用户请求；分别设计路由大小和接收期限，不用通用 WriteTimeout 误伤 WS/媒体。反馈已有 16KiB、媒体已有大小校验，不将其误报为全站缺失。 |
| P1 / 已有质量证据 | `review/real-media-b-final78-20261014.json` 中 ASR/TTS 成功，但中文回答两次明确 `l2/model_verdict/wrong`；后续提示对照安全误接收阻止采纳。 | 保留冻结标签和真实质量门。HTTP 成功、流程完成、降级有日志都不能替代语义正确性。缓存及队列修复不关闭此项，也不授权更换共享模型。 |

上述等待/队列缺口有直接代码路径依据，性能数值尚待受控测试，不能报告为已实测延迟。Timer 历史房间扫描、统计全量聚合与快照锁传播采用后端报告的独立基线方案，不以“本轮未修”推断不存在。

## docs31 设计交叉评审

IX01–05 通过：摸底保存采用同步 ref 单飞，冻结同一 payload，成功后导航；课程表成功空、加载中、失败严格分离，并用取消加 generation 双保险；反馈忙/未知送达保留原稿和 ID，明确应用拒绝后才解除锁定。总期限覆盖 body，finally 释放监听器/定时器，错误不回显上游 HTML。点击忙态目标 250ms，E2E 在受控响应返回前以 1 秒调度上限观察。

IX06 通过：内存 scope 为当前 token+课堂，清理失效旧 lease；8MiB/16项/5分钟 TTL，命中不延长 TTL，成功播放后才缓存合法完整 WAV。只复用 Blob，每段仍独立播放、字幕和 ACK；失败/取消/身份变化不回填，坏缓存淘汰。不得把缓存命中当课程授权或从 snapshot 启动朗读。已实际复核 `speech-cache.ts` 及七条单测源码：异步代次、字节计数、并发淘汰、RIFF 块界限符合合同；尚未接入 local.ts，不能宣称产品请求数已下降。

IX07 有条件通过：读泵中控制按收包顺序串行，暂停先核 active client/envelope 再取消在途判分并恢复可回答状态，继续不复活旧任务。现有 WS 仅裸 `resume`，显式暂停/继续合同需补文档及来源校验。普通队列执行前重新核账号、client、turn，旧排队事件明确拒绝；满队列关闭并记录固定原因，断连取消 worker。500ms 控制门针对慢模型夹具，不虚称可以绕过任意 SQLite 阻塞。具体控制屏障由后端与根代理冻结后实施。

IX08 通过按需查询设计；测量必须保留数据规模、并发、冷/热条件与样本数，未测不填写推断的提升比例。

### IX07 最终收敛决定

主审与后端随后冻结为**不新增 WS pause**：500ms 门只覆盖同 socket heartbeat 与 close 取消；已有 REST resume/SessionTimeout 合同保持，旧 WS resume 与普通业务全部进入有界 FIFO。读泵逐帧检查连接有效性，worker 执行前再核账号、client、ctx 以及适用的 turn；队满明确关闭和固定原因日志。此方案独审通过并已通知后端实施。清理顺序必须先 cancel/Close 再等待 worker，避免读写泵互等。上面的暂停设计是早期备选，不是本轮实现要求。

## 保持的正确性与收尾要求

现有 Room 锁外判分、turn/epoch 过期拒绝、身份隔离、反馈任务持久化幂等及事务审计应继续保留。日志增加固定原因和耗时摘要，不记录 token、音频或反馈正文。控制帧响应、业务副作用和模型质量分别验收；Go 并发/持久化测试与多用户真实浏览器用户路径不能互相替代。

本次为设计与静态评审结论，不是实施完成或总验收通过声明。每个确认的产品 BUG 必须有红→绿逻辑测试和 E2E；最终由后台代理跑全量并回收证据，再复核实际实现与本表。

## IX09 活性检测设计审查

连接后立即心跳；当前 ACK 到达后 10 秒发下一次，只有一个在途探测；10 秒无 ACK 失效当前连接，立即通知离线并重新取票，不能等待黑洞连接的 close 回调。所有 socket 回调及定时器核当前实例和会话，旧 ACK/非在途重复 ACK 不得推迟当前期限；关闭和登出清全部 timer。不重放业务事件。设计通过；前台 20 秒门指发现静默断链，不承诺授权请求或网络在 20 秒内恢复。后台标签调度延迟可安全重连，不放大业务副作用。

## 隔离压力基线执行设计（尚未测量）

仅使用临时 SQLite、假模型、独立测试服务，不访问 8090 或真实用户库。等待重构建资源释放后由后台测试执行；测量不预置“必须有缺陷”的断言。

1. **锁传播**：基线无占用与受控占用各 20 次。持有数据库唯一连接的事务，由 channel 确认持锁后启动 A 的保存快照、B 的通知/课程 GET；200ms 时取消 A 请求，1000ms 明确释放事务。记录开始/取消/退出/释放时刻及 DB WaitCount/WaitDuration，分别判断无关 B 是否被传播阻塞、取消后是否仍等释放。另以第二 SQLite 连接持写事务区分连接池等待和 SQLite busy 等待。全部占用由 defer rollback，所有 goroutine 有 10 秒清理截止。不得在同一数字中混算两种锁。
2. **Timer 规模**：真实 Room/隔离 SQLite，完成房间数 0/100/1000，另有两活跃房间；固定数据与时钟，直接执行与生产一致的 Tick/SessionTimeout 扫描顺序，使用只计数不打印 SQL 的 GORM 回调记录写次数及每房间耗时。每规模预热一次、测 10 次；记录整轮 p50/p95/max、活跃房间首次处理等待、完成房间是否仍产生写。并用一个受控慢房间测最坏顺序，明确 map 顺序不可当确定性因果。生产 1 秒 Tick 周期作为 lateness 对照，不把测试进程时间自动等同部署 SLA。
3. **JSON body**：独立 httptest TCP 服务，合法格式登录正文按 1KiB/64KiB/1MiB/8MiB 阶梯；记录响应码、读字节和延迟，最多 8MiB。另发送完整 headers 后每 100ms 一小块、总计最多 2 秒的未完成 body，在停止前验证 B 健康请求仍可结束；主动关闭测试客户端清理。该 2 秒探针只能证明窗口内是否有拒绝/隔离，不能据未超时证明“永不超时”。若需修复，先冻结各路由限额和接收期限，再用越界/慢体红绿与浏览器合法保存兼容回归。

三个报告均须保留机器、并发、样本数、原始时刻/计数及源码 SHA；最终按实测影响决定后续实现，不能把设计表当基线结果。

## 已执行的隔离基线与 IX10 修复

原始行与 nearest-rank 汇总保存在 `review/interaction-baseline-20261015.json`，重算入口 `review/summarize_interaction_baseline.py`。机器为 i3-4130（2核4线程），临时 SQLite，未调用模型。与轻量 Chromium 测试/短构建存在并行，因此耗时不是独占机器 SLA；SQL 调用计数为主要结构证据。

| 场景 | 样本 | 修前 | 最小修复后 |
| --- | --- | --- | --- |
| 连接池被另一事务占用，200ms取消保存，1000ms释放池 | 各20 | 保存p50/p95/max 1003/1021/1028ms | 200/204/205ms |
| 独立连接持 SQLite 写锁，同样取消/释放 | 各20 | 保存1051/1112/1117ms | 1051/1141/1165ms，未解决busy等待 |
| 1000完成房间+2已Start、未交付scheduled房间，SessionTimeout扫描 | 各10，另预热1次 | 每次1002快照upsert；扫描p50/p95 219/263ms | 每次2upsert；扫描0/1ms |
| 登录正文1KiB/64KiB/1MiB/8MiB | 各1 | 都返回普通认证失败200；8MiB130ms | 未修改此路径；8MiB72ms不能称性能优化 |
| 慢login body持续2秒 | 各1 | 无响应，socket read明确timeout；另一小login请求200/0ms | 同样结果，未改路由限制 |

锁基线第二稿误将屏障置于隐式事务之后，持池场景未到屏障导致测试失败；该失败保留在 `interaction-baseline-reviewed-20261015.txt`。修为 `Before(gorm:begin_transaction)` 后锁族独立通过，汇总只采用第三稿锁族和第二稿通过的Timer/body行。所有临时事务增加defer回滚。初版通知同时启动与未Start机器的数据仅保留历史，不混入本表。

生产仅两处：持久化保存 `DB.WithContext(ctx)`；Room SessionTimeout 对 `retired || finished` 返回。未移除锁、未扩大DB池、未更改未结算LessonEnd的Tick重试。预取消仍落库/占池取消不退出/退休及完成仍写库均有红证据 `interaction-runtime-red-20261015.txt`，修后与既有Room超时正例通过 `interaction-runtime-green-20261015.txt`（4.544s）。同reviewed基线修后全部通过（48.523s）。传Background的既有调用与SQLite busy handler仍可能等待，不声称所有用户断连均解除SQL。

已向前端提出独立故障夹具E2E，最终全量和用户路径尚未结束。IX09 `ws/client.ts`、媒体 `media-request.ts`、cache→local.ts 接线、后端最终有界worker/清理及EntriesByIDs实际复核均通过；Entry查询保持分块失败无部分结果、排序与重复由Service恢复。WS清理此前10秒超时会漏Leave，已转worker最终清理并由后端补回归。

## 增量设计与代码复核

- IX11 页面状态设计通过：文字与自动识别共享同步答题门，同turn业务重放不解锁，新turn/断线等权威变化失效旧门；录音阶段必须依据真实onstart，转WAV/上传归识别。课报按所选ID及读取代次隔离，通知单飞，键盘仅Enter/Space激活。实际代码仍待前端完整保存点与E2E。
- IX12 已实际核词库 enable/delete 共用 ExclusiveAction、确认弹窗整个生命周期锁、卸载销毁本页弹窗、token和generation检查、未知写先查询不自动重发；Admin动作key区分spinner和统一冻结表单通过。发现Admin初始词库GET与动作后refresh反序覆盖，已交根代理加最新请求代次，未以token相同误判安全。
- IX13 `identitybody.go` 与两路由实际复核通过：完整正文16KiB含尾随字节，10秒接收deadline，固定413/408/400且关闭拒绝连接；合法请求清读期限支持keepalive；Gin Unwrap已从本机依赖核实，真实server不支持deadline时failclosed。已初始化bootstrap先403，同时关闭连接避免net/http自动drain慢body。未修改导入/音频或教学策略；测试transport例外与真实TCP验收分开。
- 媒体期限日志仅固定 `speech/media_request_timeout`、数字lesson或preview trace、操作和耗时，不回显token、文本或底层异常。真实录音onstart与原capture回调守卫已替换“调用start即显示录音”的中间实现；取消和失败不发成功ACK。

这些是实际设计/源码复核结果。交付状态仍以最终后台Go、浏览器、覆盖率、lint和证据一致性为准，保留真实模型质量与SQLite busy等待边界。

### 六域收敛与范围说明

本轮由架构代理承担架构/安全/AI-Agent边界，后端代理承担后端/测试，浏览器代理承担前端/可用性，根代理交叉评审并整合设计。实际评审覆盖源码、规格、红绿证据及隔离基线；没有运行默认连接历史外部qwen配置的 `run_round4_review.py`，不声称该脚本通过。

目前实现复核通过：IX05网络有界等待、IX06媒体缓存、IX07有界WS输入与最终清理、IX08按需词条、IX09心跳活性、IX10取消池等待和完成房间零重写、IX12弹窗/管理串行及迟到读取、IX13身份小报文保护。Admin最终使用LatestRead已封闭同页反序覆盖。IX01–04及IX11由浏览器代理验收完整用户路径，前述实际读过的辅助合同通过，最终用例结果未回收前不提前写全通过。

保留分类：模型语义质量是已测未达标；SQLite busy-handler取消是已测未改善；其余全局Hub锁拆分、统计聚合、优雅关闭、房间长期内存清理为尚无本轮独立影响量化的候选，不虚报已修，也不在本次没有证据时重构。没有更换模型、调用云或修改教学策略。

可复用流程：先固定受控故障和性能尺度，再分解网络/连接池/SQLite锁/Room排队的等待来源；响应可见性、取消退出、业务幂等和模型正确性分别立门。报表保存原始失败与修订夹具来源，统计只合并明确通过的同语义样本；SQL工作量优先于共享机器上的单次耗时。后台测试结束再核最终源码与证据，不用中间保存点结论替代总验收。

### IX11 最终保存点复核

已实际复核完整 Classroom/Dashboard/Placement 保存点：AnswerFlight只锁wait_answer的文字和语音，未误锁跟读；同turn重放不释放，匹配事件的拒绝/错误/反馈命令可释放，新context/phase及断线统一revoke。课报LatestRead关闭后dispose，通知同步单飞，移除词库成功回调二次代次检查已补。Placement每book建立独立保存gate并重置saving，旧请求finally不影响新book，完成弹窗onOk核lifetime+token。录音实际onstart、transcribing以及旧capture过滤均符合规格。当前源码复核无新增阻断，最终E2E/全量尚待回收，前文“待完整保存点”属于早期状态。

### 安全随机标识和客户端定位补充复核

实际复核`client-id.ts`及`api/client.ts`：原生randomUUID缺失或抛错转安全随机字节，UUIDv4位正确；无安全能力时业务ID失败关闭，无时间/弱随机后门。仅诊断ID错误被捕获，REST读取仍可执行。响应X-Trace-ID采用固定白名单进入server_trace_id，和本地request_id分开，日志只含去查询参数路径、固定原因及耗时。已向前端代理确认实现无新增阻断，并要求最终短Node覆盖抛错回退/能力全缺失及浏览器普通HTTP兼容用户路径。总验收仍待最终回归，不据静态审查提前宣称全绿。

### 交互E2E断言强度复核（最终运行前）

已通读两个interaction spec：媒体body超时以英文已完成的1个ACK为基线，中文失败后仍1、Audio创建0、字幕空，不能把降级当成功；Books在请求及确认弹窗两个阶段均锁另一按钮并检查写次数；键盘摸底通过离开后重新读取验证持久化。已要求加强通知恢复为真实成功响应及未读状态，而非仅按钮解除busy；Admin迟到完成和关闭课报必须在旧响应真正完成后断言，不以250ms等待或释放函数调用冒充完成。

r2修订的文案/选择器错误（中文朗读Alert、loading图标改变可访问名前缀、通知实际按钮名）属于夹具错误，保留原失败记录且不减少业务断言。r3暴露测试helper自身randomUUID、中文关闭名、Select重复文本及fake clock安装晚于socket timer，已交前端统一修正：fixture ID在Node生成，选择器限定明确语义/可见选项，假钟在被测连接创建前安装。真实字幕专项实际使用NativeAudio，3英3中、唯一合成HTTP200、6段字幕绑定实际start/end且零ASR；该专项不宣称模型判分质量通过。

### 新发现的大库写入候选（等待独立量化，不作为已修项）

浏览器1000词夹具在并行race期间观察导入成功约99.484秒，随后选库约74.916秒尚未完成；两项必须分账，不能据此称死锁。实际源码`wordbook.ImportBatch`每词查询key、MAX(id)再独立Create；`App.EnableBook`每词GetCard后独立SeedCard。后端负责空闲窗口100/1000词导入、首次选库及重复选库基线，排除并行负载后再准入IX14。

候选设计边界已交根/后端：导入保留非法输入和同库重复跳过、同批first-wins、跨库同词允许及输入顺序；当前存储错误也静默记Skipped不是安全合同，若修应明确存储/取消/提交错误整体回滚有效写并返回错误，不报成功数量。事务内查询和写入必须绑定同tx，不能回调原单连接repo造成自阻塞。选库可事务内分块INSERT，冲突仅三元组(student,book,word) DO NOTHING；严禁覆盖既有mastered、boost及复习状态。后块故障新增全部回滚，既有卡不变，双App并发及MemRepo同合同须测试。当前仅只读设计意见，未修改产品。

阶段执行更正：interaction r3因Go race与Chromium竞争期间owned server未在30秒就绪而中止，不能计为完整失败族或全通过族；原中止日志保留。后续按race→独立写入基线→最终Go→浏览器串行，不通过增加启动或产品超时掩盖资源竞争。新增课报body释放、Admin POST.finished+渲染帧完成屏障已实际复核；通知恢复改真实POST成功和GET状态，另要求非空未读前置，避免空集断言假绿。

## 当前实现与实际跨领域复核矩阵（IX14定向后，总验收尚待）

| 项目 | 当前实现及复核结论 | 尚需最终证据 |
| --- | --- | --- |
| IX01 | 摸底保存同步单飞，成功才导航，失败留稿；换book隔离旧保存 | 新树完整浏览器 |
| IX02 | 摸底加载/失败/编辑门及键盘可访问操作明确 | 新树完整浏览器 |
| IX03 | 课程/词库/通知加载、错误、真空态分离，代次拒迟到 | 新树完整浏览器 |
| IX04 | 反馈正文与submission ID绑定，busy冻结，未知送达沿原ID恢复 | 新树完整浏览器 |
| IX05 | REST及媒体头+body总30秒，取消/超时分型，安全UUID兼容，固定脱敏日志 | 最终Node/浏览器 |
| IX06 | scope/lease隔离的成功WAV缓存，16项/8MiB/5分钟，不复用Audio或ACK | 真实3次中文播放缓存轨及全量 |
| IX07 | 单读泵+16项FIFO业务worker，heartbeat直回、close取消及最终Leave | 新树Go/race与浏览器 |
| IX08 | book+IDs分块查询保序/重复/缺失与跨库过滤 | 新树全量及大库真实入课 |
| IX09 | 单在途心跳与ACK期限、旧socket隔离，检测与恢复期限分开 | 假钟Node和真实WS黑洞全量 |
| IX10 | DB context与finished/retired超时no-op；池等待和SQL数量实测改善 | 新树总回归；SQLite busy限制保留 |
| IX11 | 答题pending、课报迟到隔离、通知恢复、实际录音/识别状态 | 新树完整浏览器 |
| IX12 | Books选择/删除与确认弹窗全生命周期锁，Admin动作及读取代次守卫 | 新树完整浏览器 |
| IX13 | login/bootstrap 16KiB/10秒，拒绝关闭连接，合法keepalive及封禁优先 | 新树Go/浏览器 |
| IX14 | 原子导入/建卡、进度保护、固定连接失败COMMIT清理与替换连接配置 | 同D盘HTTP比较和最终全量 |

架构/安全/AI-Agent代理已实际检查上述设计、调用链与权限边界；后端/测试代理独立审Go及SQL事务并执行红绿；前端/可用性代理实际审页面、编写多用户E2E；根代理对实现和合同再次交叉复核。IX14由本代理实现，最终独立代码批准来自根与后端，不能把作者自审冒充独立评审。旧外部qwen评审脚本未运行，AGENTS保持不变。

IX14新增真实缺陷已闭合于定向：失败COMMIT原先令原池看到3条未提交行，独立连接为0；本地驱动Commit只执行SQL，database/sql先标TxDone使GORM再Rollback无效。修复固定sql.Conn、显式GORM Begin/Commit并在归还前同连接清理，不改变全部仓储事务。后端r3为14顶层/23含子通过、0失败：api6.113s/store2.090s/scheduler0.877s/wordbook0.855s，证据`.test-artifacts/import-enable-atomic-green-r3.txt`。包含真实deferred FK提交错误、原池/外部零行、下一事务成功、panic/取消/nested拒绝、nil输入零残留及替换连接PRAGMA；主审与后端实际代码复核通过。r10的964含子/7skip发生在IX14前，严禁拿它证明新事务helper全量通过。

API/日志一致性核对：导入与enable路由传Request.Context；旧内部入口用Background，Enable前置读取仍不承诺立即取消。无新增产品端点/WS事件/教学配置，非原子自定义Repo明确拒绝。导入失败不再伪装Skipped；四个wordbook完成/失败事件使用请求trace与固定摘要，完成只在commit后。事务失败中文文案不回显SQL，原子失败E2E第501词是在100行分块的第6块，检查之前所有块回滚而不是描述为第2块。

方法沉淀：事务回滚验收须区分INSERT失败和COMMIT失败，并同时核原池、独立连接及后续事务；一个连接中的未提交可见行也属于可靠性BUG。性能计数分开Create调用和唯一sql.Tx，样本p95使用明示分位法，存储/HTTP/浏览器并发条件不可混算。测试屏障绑定真实响应/body交付，不以固定sleep或按钮变回enabled猜成功。当前已实现项无未处理代码评审阻断，但新树最终验收仍未完成；模型质量、SQLite busy取消和未量化的全局锁/统计/优雅关闭候选保持各自证据边界，不宣称全问题归零。

### IX14 D盘真实HTTP对照证据复核

已读`review/import-enable-http-comparison-20261015.json`（SHA256 `15ed5bc0ade3f4c15dadd58d44bebb6124b8c5b03d820a352c04433101fdc5ca`）：同D卷新SQLite、同测量脚本、mock provider/GOMAXPROCS2、无并行Go/浏览器/模型，每二进制每规模操作仅一次。1000词导入109906→422ms；旧首次enable在110515ms预算处右删失，不能当完成耗时，新563ms；新重复enable296ms，旧同规模D盘未执行，不能引用C盘103ms计算改善倍数。新并行学生GET全部正确，导入max422ms（n1）、enable max453ms（n2），说明单事务仍会占唯一连接，不宣称零阻塞。

新二进制SHA `a7924bdbf163266df444f84d2ee459f62c28f77e42700e65210d958a7add25dd`；只读外部连接观察WAL和synchronous=2，但该连接的设置不代表活跃Go连接的全部设置。源码未降低WAL/synchronous，DSN改动仅确保每个替换连接的外键与busy期限，非放松持久性来换速度。表中单次结果不是p95或普遍性能保证；前述C盘Service事务计数基线及旧浏览器竞争场景仍独立保留。最终用户路径与总验收尚待串行执行。
<!-- IX15/IX16 independent checkpoint -->

IX15准入证据为新树真实HTTP大库摸底116682ms、小库1962ms；请求可能含finish重排，不能全归MarkPlacement。原子差异保存待旧写次数/草稿失败红证据后实施，不复用IX14验收结论。

IX16设计及实际agenttransaction.go独审通过：callback退出defer覆盖错误，未进入回调的begin等待等于transaction，失败备用审计单独计时，内存路径各项为零。日志无正文/SQL/凭据，不改变锁、审计提交和ACK顺序。finalize含提交或回滚，不解释为纯磁盘耗时；字段舍入后不保证严格求和。config.set_student提交后Reload不包含在此课堂事务阶段计时。测试及新D盘分段实证待回收，新增观测不代表15.545秒停顿已消除。
IX15后续保存点：旧红已实测无变化100卡仍100次Update、草稿失败已改变两卡；实现转为固定连接事务内writer→读→status-only差异分块→草稿upsert，Mem同锁副本发布并将草稿纳入内存事务。主审及后端实际初审无阻断，Context入口保持旧Background包装，提交后hook只记录输入数量和变化数量；finish排课失败另包装并明确草稿已保存。双独立池、取消、提交失败与最终全量尚待验收，不把IX16五分支绿或旧r10全量混入IX15结论。
### IX15/IX16 定向及E2E实际代码复核

后端最终定向证据 `.test-artifacts/placement-atomic-green-r6.txt`：13个顶层、26个含子用例全通过，无跳过；API 5.613s、scheduler 0.674s。覆盖105卡其他字段保护、取消、deferred COMMIT故障、独立双池草稿/卡一致、Mem草稿事务与HTTP trace/计数/finish部分成功。此为定向结果，不替代新树全量。

已实际审阅 `interaction-import.spec.ts`：两词真实选库并建立空草稿；限定student/book的草稿UPDATE触发器导致实际存储失败，等待HTTP响应后比较全部卡/草稿行仍为旧值，同时页面保留选中和中文错误；删除唯一故障触发器后手动提交并重新打开验证持久。1000词十屏同学生/词库的placement_saved恰10条、input_count均1、changed_count首条1其余0，未缩短词库或跳过屏幕。

已审阅 `interaction-boundaries.spec.ts` 真实课堂同lesson trace的committed工具日志非空与七个固定耗时字段检查。建议日志读取使用有界poll等待管道落盘后精确断言，并补整数检查，避免输出落盘时序制造假红；不改业务等待预算。该E2E验证观测合同，不能单独证明15秒课堂停顿已消失。最终浏览器与全量Go仍待回收。
## 最终冻结增量六域复核（r11普通全量之后）

当前新增代码review阻断：**0**。这是代码/设计审查结论，不等于race、116浏览器和整体模型质量已通过。

| 领域 | 实际收口与未夸大的边界 |
| --- | --- |
| 架构 | bounded WS reader/worker、锁外L2上下文、原子仓储固定连接清理与生命周期一致；IX16仍同步审计，不借性能删除Agent工具边界 |
| 后端 | IX10只跳finished/retired，IX13仅身份入口16KiB/10秒，IX14/15单事务与status-only写；finish排课、旧前置读取的取消边界保留 |
| 前端/易用性 | 单飞/未知写查询恢复、旧会话及turn拒绝、真实播放字幕与成功后缓存、30秒头+body期限、心跳ACK恢复无业务重放；后台标签计时不作前台SLO承诺 |
| 测试 | 普通Go r11已实读原始汇总：源d8dffa31bccdc49265483a0e585b768d233be7fd778bfb37c47593d2d0fb6eb4前后不变，489顶层/1009含子通过、7跳过、vet0及覆盖过门；race和116总族仍待 |
| 安全 | 请求/媒体/输入超限有边界；token隔离及安全随机ID无弱随机降级；日志固定摘要、错误不回显SQL；源码与AGENTS差异检查确认AGENTS未改 |
| AI-Agent | 改善运输、存储与日志，不改变课次/机会/排课决策归属；候选提示未过质量门不采纳；工具audit必须成功提交再发动作 |

IX16最新E2E原先误纳另一执行器memory.append的日志失败保留，已按实际callAgentTool合同限定committed classroom.*且要求play_tts，未弱化为任意日志存在。13事件全部七字段整数非负，transaction max79ms/finalize max78ms，只证明本次正常观察。历史15.545秒停顿没有本轮分段原始证据，不能假称已定位某个fsync或消除。SQLitebusy、单连接阻塞、真实模型质量缺口明确保留，其他未经量化候选不冒充已修。

### 2026-10-15 后端最终验收更新：r11普通与race同树通过

已核原始 `server/.test-artifacts/final-runtime-coverage-r11-summary.json` 和 `server/.test-artifacts/final-runtime-race-r11-summary.json`：普通/覆盖率/vet运行119.64秒，489顶层、1009含子用例通过，7项显式opt-in或测试harness跳过，20个测试包通过（另1包无测试），vet退出0，覆盖率全部达门。race运行217.58秒，同样489顶层/1009含子通过及7跳过，退出0，无失败/竞争报告。

两轮源码前后均为 `d8dffa31bccdc49265483a0e585b768d233be7fd778bfb37c47593d2d0fb6eb4`，source_unchanged=true。此前“race待回收”为历史时点，此处为最新状态。116浏览器总族仍执行中，恢复组27条已过但不能替代总族；本阶段尚未封板。真实模型质量缺口、SQLitebusy边界及IX16只观测不等于15.545秒停顿消除的结论保持不变。

## 2026-10-15 交互阶段最终验收封板

本轮已准入IX01–16阶段验收完成，新增代码评审阻断0。后端r11普通/覆盖率/vet与race同树全部通过（489顶层、1009含子通过、7显式跳过；普通119.64秒/race217.58秒），覆盖率全部过门；前述“待验收”保留为历史时点，由本节更新现态。

浏览器按三段**完整用例族**接受，总计116：首段recovery41+overdue1；第二段feedback26+legacy9+journey1+closure6+firstuse1+playback1；第三段interaction30。接受族全部0 skipped/unexpected/flaky，Node60、lint0、九族构建通过。不是一次运行全绿：首段feedback有19通过/1旧英文错误文案断言失败/6未跑；续段interaction有29通过/1 AntD离场图标影响精确名称的夹具失败。两次原报告保留，修正仅测试后分别整族重跑，不合并失败族的零散通过项、不修改生产或放宽产品超时。

三段原始manifest已独立核验生产SHA `ea3283a7f342a97310d19ff29981c9110bd4d4db05799e2091a8e127ef11a7c3`、server `5686ce420d5c2c10a6bcbc8e93d687104e5a956547092b9b2a63ee7638a10bfe`、harness `0b47afff2d672fd7a86ad1d1ff8b87243cca5ae4fe126a0cd9f53b62ac43fc07` 一致且unchanged=true。证据汇总为 `review/interaction-browser-final116-20261015.json`，完整阶段说明为 `review/interaction-final-acceptance-20261015.md`；Go源SHA与上述前后端生产指纹范围不同，不混为同一hash。

阶段完成不等于所有历史任务完成：IX16仅分阶段观测，历史15.545秒长尾未宣称消除；SQLitebusy和单连接等待、摸底草稿与排课部分成功边界保留；真实文本/ASR质量未过不被mock回归替代。共享模型未在本轮切换，AGENTS未修改。

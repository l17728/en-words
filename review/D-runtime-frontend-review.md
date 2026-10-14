# 课堂恢复、媒体边界与历史E2E迁移评审

状态：实施/阶段验证中，最终质量门尚未通过。设计以docs17/18/19及03台账为准；AGENTS未修改。

## 发现与修复

| 发现 | 修复与锁定证据 |
|---|---|
| WS重连复用内存短期ticket，后端重启后无法恢复 | 每次连接前认证enter刷新；捕获token、closed守卫、认证终止错误可返回；真PID重启用例 |
| 断线旧音频/录音继续产生迟到输入 | onClose取消TTS/本机录音/浏览器识别；onresult/onend都检查实例和token；真实重启后旧识别器onend不得重启 |
| 无voice、error、watchdog、catch假报tts_finished | 只有真实结束成功ACK；失败显示文字提示、结构摘要探针，服务端tts_skipped推进；4类故障各有E2E |
| Teacher工具失败前端无恢复入口 | classroom_error显示明确重进操作；协议故障夹具只证明UI，真实事务/工具拒绝由Go覆盖 |
| 日志包含朗读正文 | 改为语言/段号/长度/课程/上下文摘要；探针不再储存正文；真实发声与mock证据分开 |
| 历史固定开发端口、共享账号、skip和弱冒烟 | 独立进程/SQLite/端口、headless；每条BUG新学生/库；七条BUG和两条smoke逐条映射，fulljourney复用UI首启强化旅程 |
| 澄清子反馈迟到后可能查询旧父 | hook query入口/返回、所有parent refresh前后冻结session/generation，跨标签换号拒旧请求；UI草稿稳定子ID与父标记 |
| 澄清发送中仍可改正文，不确定送达可能换ID | busy或uncertain期间锁正文，保留原ID查询恢复；真实POST已提交但响应/查询断网的浏览器用例已通过 |

## 独立复核

架构代理已复核WS和TTS成功/失败边界；其发现旧识别器onend身份检查缺失已补。主代理复核补send关闭/换号守卫、403/404错误UI、Teacher错误恢复UI及父反馈查询会话守卫。仍须复核最终恢复/澄清补丁与全部实际运行结果，不能把阶段通过当最终通过。

## 阶段证据与失败分类

- `web/.test-artifacts/closure-1791947058134-1136/results.json`：恢复组12通过、1失败、0跳过；失败为测试精确“退出”定位不匹配AntD“退 出”，已全同类改正则。
- `web/.test-artifacts/closure-1791951763381-10724/results.json`：取消迟到合成body用例通过；旧recognizer用例未进入业务阶段，自有进程30秒readiness超时，server.log为空且无SQLite产物。保留启动证据，未调大产品时限；待环境正常后实跑，不计通过。
- 当前编译器仅完成官方包SHA256和clang版本验证；race未跑，不宣称通过。
- 历史隔离迁移9/9阶段通过（`.test-artifacts/2026-10-14-legacy-stage/browser-legacy.log`）。恢复扩展阶段18/19通过（`closure-1791952923832-10552/results.json`）；T3夹具期望误写overdue，实际协议为expired，已修，不是产品缺陷。
- 反馈扩展第一轮13通过、FIFO用例1失败：21次用户反馈、界面淘汰词断言均通过，附加SQL核验误用words表，已改实际word_entries。第二轮16通过，FIFO完整核验通过；月额度前置ISO日期暴露规则不支持ISO却默认明天的真实产品缺陷，交主代理修复并补Go红绿。所有未运行项不计通过。
- 原识别器迟到onend在恢复扩展轮已实际通过；此前进程未启动失败保留记录，不再作为未验证代码结论。

真实8090媒体验收独立配置，输入为独立WAV经真实MediaStream/MediaRecorder编码，实际HTTP、Audio playing/ended和ASR响应共同断言。其结果与确定性mock组分列；人工试听仍需独立结论。

## 真实媒体失败与定位

首轮`2026-10-14-real-media/summary.json`与补日志诊断轮`2026-10-14-real-media-diagnostic/summary.json`均0/1，完整保留，不作为成功证据。原生英文浏览器朗读有真实完成回调；中文TTS上游500（约11秒）导致产品503，两个真实MediaRecorder WebM录音上传均上游400。平台目录发现成功不代表推理或编解码成功。

从trace仅提取无凭据音频，EBML头`1a45dfa3`；同录音通过8090复现`Failed to load image or audio file`，平台ASR日志确认解码buffer失败。产品新增MediaRecorder压缩结果经OfflineAudioContext16000Hz解码、混音并编码PCM16WAV，完整保留采集链路；纯函数5条和坏解码/取消/换号3条浏览器边界分别验证。真实旅程记录压缩输入魔数、实际上传WAV头、实际HTTP与播放事件，不能用注入WAV代替产品转换。

TTS独立诊断见`review/tts-cat-valid-diagnostic-20261014.json`：有效文本“猫”在平台CLI链路成为OOV“è”，词典本身存在正确猫词条。平台编码链路由主代理独立处理；没有通过更换音频标签或跳过TTS断言掩盖失败。

## 最终轮中间证据（不得当作完整绿验收）

r4 活课堂退休 1/1 通过：`web/.test-artifacts/closure-1791956387215-4016/results.json`，旧 A 连接关闭、B 继续、进程未重启，未来新课遵守时间门。反馈 18 通过、1 旧 UX 断言失败、7 未运行：跨标签换号实际已清旧单词/气泡/草稿，但用例仍要求自动导航。主代理审 App 会话重挂载与 Classroom 清态后批准显式终止→返回合同；测试补旧单词和老师气泡消失，录音换号同类同步。失败保留于 `closure-1791956462282-5236/results.json`。

真实媒体音源启动时机经独立评审修正：以 MediaStream WeakMap 关联音源，在真实 MediaRecorder start 事件后才播放独立样本，断言启动先后与 ≤4 秒；未替换原生压缩结果或产品转换。架构代理已复核通过，最终真实实跑仍待平台与 race 时段协调。

r5 反馈 24 通过、1 夹具等待竞态、1 未运行。依赖重试按钮在发送中已隐藏，测试过早 GET 读到第一项已提交/第二项未提交的中间态。最终 SQLite 两项实际为 stale_target/dependency_failed、attempt_count=2 且不可重试；补等待真实 retry POST 完成再核验，不改产品。保留 `closure-1791956970878-6988/results.json` 与同目录 DB 证据。完整反馈将重新运行。

r6 的恢复26、历史9、完整journey1全部通过。closure旧外学生导航断言仍要求自动dashboard，实际403终止且无课程内容，与已批准终止视图合同相同；改为先锁终止与无cat/dog，再用户点击返回，owner仍继续课堂。保留 `closure-1791958287828-18152/results.json`（0通过/1失败/5未运行）；r7 closure6/6全绿，没有调整业务拒绝预期。

旧冻结版本71已完整全绿，索引 `.test-artifacts/phase71-manifest.json`。其后总审发现快照恢复日志缺少稳定trace和非认证enter错误无界面提示，新补REC09与WS Node2，旧71不作为新生产树最终证据。Node临时/终止错误两条先红后绿；评审发现classroom_error状态类型迁移遗漏已补且tsc通过。REC09定向前两轮仅夹具失败：第一次误捕重启期间Vite空500而非后端JSON，第二次旧测试读取payload.reason而实际日志字段平铺为reason；已修响应过滤和断言位置，未改变产品日志格式，失败报告保留。

r8恢复27/活退休1/反馈26/历史9/journey1/closure6通过后，默认首启在创建词库精确accessible name等待失败。trace证明创建学生POST 200/40ms，无pageerror/未完成HTTP；目标button无ant-btn-loading、无disabled，残留图标motion-leave-active且width0/opacity0/scale0但aria-label=loading仍参与名称。属于测试名称假设，未改Admin；共享firstuse helper改为允许图标前缀，必须enabled且无loading类后普通click（不force）。按主审重跑journey/firstuse/playback三个消费者；旧失败保留 `closure-1791961079985-13420/results.json`，其余同生产SHA独立组复用。

## 最终确定性验收与真实媒体结论（2026-10-14）

可提交机读汇总：`review/runtime-browser-acceptance-20261014.json`。同生产树确定性72/72通过、0失败/0跳过/0flaky；Node WAV5+WS2=7/7。各正式配置均实际执行TypeScript/Vite build。lint退出0、0错误、17警告，未声称零警告；Playwright fixture `use`误认React Hook已仅重命名测试参数。

| 分组 | 通过数 | 唯一JSON报告索引 |
|---|---:|---|
| recovery | 27 | `web/.test-artifacts/closure-1791960145895-10288/results.json` |
| overdue | 1 | `web/.test-artifacts/closure-1791960612299-8176/results.json` |
| feedback | 26 | `web/.test-artifacts/closure-1791960645934-6360/results.json` |
| legacy | 9 | `web/.test-artifacts/closure-1791960860409-16188/results.json` |
| closure | 6 | `web/.test-artifacts/closure-1791961021188-11404/results.json` |
| firstuse | 1 | `web/.test-artifacts/closure-1791961788198-7636/results.json` |
| playback | 1 | `web/.test-artifacts/closure-1791961865264-18000/results.json` |
| journey | 1 | `web/.test-artifacts/closure-1791961937643-16464/results.json` |

页面/路由8/8与FR A–I（包含H配置）的旅程映射见docs17，按真实页面断言复核，不声称未实现CDP覆盖采集。生产Go/TS共99文件逐文件SHA附机读报告，合并SHA `dbdcfaf4d4b11d01bb612238783ad5aa5c9b3b83b8bd89bf23057cd0be111f3f`；server SHA `6003511c174d86bc36c71539f5b011dcef78d0c8dd6eb318ccca8c24bc01074b`，harness SHA `80093a8cde34fae5cd68e143bebbe641402059d1b1269d951cea74214b56794f5`。额外记录go.mod/go.sum/package锁与Playwright配置hash，但生产源合并hash本身不代表全部构建输入，也不单独证明二进制编译来源；构建由后端代理在同冻结树串行完成。

17条lint警告保留于`.test-artifacts/final-browser-lint.txt`：会话/异步初始化effect、清理时读取当前ref、静态按钮actions缺key、渲染时间gate及speak直接依赖提示。speak通过pumpTts间接依赖lessonId，后者已显式包含lessonId，不存在已证明的旧课堂ID捕获；本轮未为消除静态提示而改变已验收生产行为。独立架构复核结论另见运行时架构评审记录。

正式复现工具`review/run_browser_acceptance.py`已保存8套配置+精确预期数+Node7+lint，真实媒体仅--media。本机Playwright源码确认PLAYWRIGHT_JSON_OUTPUT_FILE优先于配置，工具每套绑定独立JSON路径；--help与静态评审已通过。当前实际证据来自等价r8/r9后台编排，不虚称新保存脚本已再次整套运行。

真实模型轨最终0/1：唯一报告`web/.test-artifacts/closure-1791962314611-10116/results.json`，summary位于`.test-artifacts/2026-10-14-real-media-wav-final/summary.json`。worker目录与顶层报告目录不同，不能替代此唯一报告索引。164秒一次旅程内4次实际原生WebM压缩录音→产品解码→PCM16单声道16kHz WAV上传，全部ASR HTTP200；英文两次Cat.，独立Piper预期“猫”两次识别“咯。”，均触发业务补学。中文TTS同一旅程初教/补学共3次503，本机中文voice亦不可用并明确失败，无假成功ACK/Audio成功。没有为取绿替换标签、篡改模型返回或反复重跑；严格100%课报/中文发声断言未通过。

该结果证明录音格式兼容修复已真实走通，但不能证明中文识别质量或中文朗读可用。独立音源内容可懂度与设备播放仍待人工试听；共享平台候选修复未部署、平台未重启。所有前轮失败分类与证据保留，确定性全绿不抵消真实媒体未过。

## 后续人工证据修正与警告清零阶段

用户人工反馈独立Piper journey-cat-zh音源“不清楚或不是猫”。因此旧真实媒体中预期猫→咯只能证明该音源与标注不一致的端到端结果，**不能可靠归因于ASR模型识别能力**；原HTTP/音频/返回文本证据保留，不改旧样本标签取绿。主代理安排新的独立且人工确认音源后再验收。TTS编码修复已获用户授权并由架构代理应用，共享平台12条TTS复测均成功，详情由后端/平台报告承载，旧媒体0/1仍是历史失败而非当前已复测结果。

17警告按docs23实际逐项处理，发现并修复Dashboard静置跨开课时间未刷新的真实展示BUG；REC10红→绿、Node新4/4，所有局部豁免有逐行语义原因而非全局关规则。正式runner Node11和lint退出0且无warning输出，全73进行中。旧72+17warnings报告保留历史身份，待新全量完成写新汇总。


## REC05后续真实缺陷与未完成验收（2026-10-14）

清17条lint后正式73轮Node11/lint0通过，recovery27通过/1失败：真实重启快照提前开放答题，业务帧清草稿，answer_length=0消耗机会。该轮`.test-artifacts/2026-10-14-final73`保留，不称完整绿。docs23§6已经主审/架构批准，新增ready(epoch,turn,phase)同步门覆盖手动/native/local采集；同turn重放不重录、不清稿。实际REC05定向已绿。

REC11先红锁定未ready输入仍enabled，`.test-artifacts/readiness-rec11-red.json`。新夹具两轮顺序错误分别留在`readiness-targeted-green.json`、`readiness-targeted-r2.json`：第一轮扣单帧越序、第二轮扣整段还未显示phase；均修夹具保持协议序，不豁免产品期望。最新Node就绪/路由6通过，总Node17；完整75及真实平台新B句子旅程仍待运行，不以前阶段72/73替代当前树验收。


75阶段基线已完成：`.test-artifacts/2026-10-14-final75-r2`八套75/75，0失败/跳过/flaky，Node17/17，lint0错误0警告，所有配置TS/Vite构建通过。机器报告`review/runtime-browser-acceptance-20261014-final75.json`，生产SHA `4a88132a3e9675bed69df543216ee4c748c07da51243d7f588af47f9c059a537`前后不变。随后docs26发现独立的模型数量授权缺陷，故此为该修复之前的完整基线，不冒充下一冻结树最终验收。B句子真实媒体另轨。


## docs26/27红证据与真实媒体诊断

B真实音源人工签收后旧75树媒体0/1，95秒；review/real-media-b-diagnostic-20261014.json记录3次TTS HTTP200且实际Audio playing/end，4次真实WebM录制→PCM16 WAV上传200，Cat./这是一只猫。准确识别。错误在两次判分wrong；同trace text请求成功，旧适配器缺失/非法judgment静默wrong故不能断言纯模型语义错。保留100%期望与原音源，未重复抽样挑绿。

CH030 UI旧binary真实红为need_confirm预期却applied；正向用例原末尾文案错误“已生效”导致夹具失败，修为现有“已应用”后旧树1/1绿，记录quantity-ui-positive-baseline.json。JDG UI红：真实两轮作答/重教/0%结束均通过，只缺预期两条judge_decision来源。新增3例仅测试独立本机adapter，不改共享模型，不拦最终业务结果。正式runner现完整78/Node17，等新Go冻结后全量。


26+27冻结产物定向验收已完成：`quantity-judge-ui-green.json` 3/3通过，CH030真实红转绿、相对减量/合法双维配置正向保持、合法wrong与非法枚举按同lesson trace区分。新server SHA `079462faaad05a61b92ac2d466a3b138039a7ff88cd2f1c584fb513e25e289d1`，新harness SHA `4a2c81da9791efccfa18fa5285f8ad6fc91cba66f42b9cec2730e2454d496857`，旧产物保留。正式78轮已启动于`.test-artifacts/2026-10-14-final78`，Node17/lint0先通过；全量结果以最终summary/manifest为准，不能以定向3条替代。


## 2026-10-14 最终78冻结树验收

26数量授权与27判分来源改动合并后，正式入口 `review/run_browser_acceptance.py` 已实际完整运行：recovery33、overdue1、feedback26、legacy9、journey1、closure6、firstuse1、playback1，共 **78/78通过，0失败/跳过/flaky**；Node17/17、lint0错误0警告，八套独立TS/Vite构建通过。定向新增QTY/JDG三例亦3/3通过，红证据原样保留。

可提交机器汇总：`review/runtime-browser-acceptance-20261014-final78.json`；原始完整日志/每套JSON与manifest：`.test-artifacts/2026-10-14-final78/`。生产Go/TS合并SHA `2eb237cf4ec8037bb6ff394caeb05c151d59c446479e9d490ce922f0f1b321cd` 前后一致；server SHA `079462faaad05a61b92ac2d466a3b138039a7ff88cd2f1c584fb513e25e289d1`，harness SHA `4a2c81da9791efccfa18fa5285f8ad6fc91cba66f42b9cec2730e2454d496857`。该源hash不等价于全部依赖/构建输入，二进制由后端代理在冻结树串行构建；旧75产物不覆盖。

真实B句子音源媒体单列执行，不能以78个确定性用例证明真实模型质量。8/8页面路由与FR A–I映射保持docs17既有台账（含H设置）；未声称通过浏览器代码覆盖率采样。就绪门、时钟门、旧响应隔离、故障恢复均有上述实际回归证据。


同冻结树真实媒体唯一诊断已完成，报告 `review/real-media-b-final78-20261014.json`（原始 `.test-artifacts/2026-10-14-real-media-b-final78/`）：0/1，96秒，首答率100%断言保持。3次中文TTS HTTP200且实际Audio播放开始/结束；4次真实WebM→PCM16 WAV上传及ASR均200，Cat./这是一只猫。准确。两次中文语义判分同 `lesson-1` 明确记录 `source=l2, reason=model_verdict, result=wrong`，文本模型请求314/213ms成功，排除本次协议无效/超时/回退。该证据锁定现行模型对这条已确认句子释义的判错能力缺口，不通过子串特例、换音源或反复调用消除。英文跟读两次为l1/match/correct。源码前后一致；平台未改动，推理窗口已释放。确定性78全绿与真实媒体0/1严格分轨，仍不能宣布全产品质量门全部通过。


## docs28 判分语义路由新增用例（待新冻结树验收）

JD28-01/02在真实课堂分别输入`not dolphin`与包含dolphin的改分指令，测试专属本机adapter按完整文本固定wrong；经历真实重教后再输入完整中文释义句并返回correct。每条同时验证短答dolphin跟读仍走L1、两次L2判分日志同lesson trace、另一学生课表及学习资源未变化。fixture不使用新40留出集的词或句子，不能代表真实模型质量。

旧binary079462fa的真实红报告`.test-artifacts/judge-semantic-routing-red-r2.json`：2/2失败，首轮应调用L2但实际judgeCalls=0，证明旧英文token命中绕过了语义检查。最初`judge-semantic-routing-red.json`因编辑脚本工作目录错误No tests found，只是执行错误，不计行为红证据。新增两条后正式runner严格期望80=35+1+26+9+1+6+1+1；尚未跑新树，原78作为前阶段完整基线保留。真实B音源与100%期望不变。

## 当前字幕与语义路由冻结树验收（2026-10-14）

确定性 **86/86通过、0失败/跳过/flaky**，Node **27/27**，lint **0错误0警告**，八套TS/Vite构建通过。此为同生产SHA的分批合成证据：`.test-artifacts/2026-10-14-final86` 已通过 recovery41/overdue1/feedback26/legacy9=77；共享旅程夹具经主审和架构独审修正阶段同步后，`final86-r2` 的journey1/firstuse1/playback1/closure6=9通过。首轮journey计时失败保留，未声称一次全86绿；产品未为该夹具改动。

唯一机器汇总 `review/runtime-browser-acceptance-20261014-final86.json`；生产非测试Go+web/src SHA `54c462c90dd7581826fdf7f0163d561902e8cbe893fa274bda7cbcd9678984c9` 各批前后一致。server SHA `13ebca11a31692efad58b32fa0f8d330758a816fc397119674deccda19ee35a5`；harness SHA `c18bd11587d02834eb91ae6de6e5e7b5f2ea8b94da81c4bc6a6894ced37e4959`。二进制由后端代理冻结构建，该源hash不声称涵盖所有依赖/构建输入。8页面路由与FR A–I（含H配置）沿既有映射全覆盖，不冒称浏览器语句覆盖采样。

真实字幕独立 **1/1通过**：3段英文cat与1段完整中文“猫是一种常见的家养动物。”，实际8090合成200、NativeAudio playing/end与字幕时序一致。报告 `review/real-subtitle-acceptance-20261014.json`；发声中截图 `review/actual-chinese-subtitle-20261014.png` 已人工查看位置。范围仅TTS字幕，零ASR/学生答案/判分；不是孤立猫音质证明，也不抵消原 `review/real-media-b-final78-20261014.json` 的完整B课堂0/1或模型安全门失败。原失败均保留，没有重跑B挑绿。

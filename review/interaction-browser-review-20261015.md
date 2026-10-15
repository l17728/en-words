# 浏览器交互速度、易用性、完整性与可靠性评审

历史静态基线（2026-10-15，实施前）。下表位置对应评审时源树，不是最终代码行号或已关闭台账。

日期：2026-10-15。范围：web页面、共享REST客户端、WS、音频及字幕；只读静态评审，未运行新浏览器基线、未调用模型、未改产品或共享服务。以下为可从代码推出的路径，运行时性能数字和红例均待独立验收，不能冒称已实测。

## 优先修复发现

| ID/优先级 | 文件定位 | 用户路径与影响 | 最小设计方向与验收 |
|---|---|---|---|
| IX-B01/P1 | `web/src/pages/Placement.tsx:33`、`:59`、`:118` | 返回按钮调用submit(false)后立即导航，文案先声称“进度已保存”；请求失败后用户已离开。已有保存未完时submit直接return，期间词条仍可编辑，新选择可能未保存就离开。 | 保存成功才导航；保存中冻结相关选择/翻页/完成，或显式排队最新版本但不得跳过。失败保留页面与选择并可重试。真实后端延迟POST，点击翻页再编辑/返回，核DB最终草稿；500不导航、不假成功。纯异步控制单测锁成功/失败/重入。 |
| IX-B02/P1 | `web/src/api/client.ts:60–90`；`web/src/ws/client.ts:47` | fetch和res.json无截止时间、无调用方signal；悬挂bootstrap使登录永久禁用，settings/stats永久Spin，enter悬挂导致WS尚未建立且无法开始退避。非JSON网关错误直接抛JSON解析错误给用户。 | 可选signal+有界整体请求（含body读取），稳定timeout/network/invalid-response错误；鉴权守卫仍在异步读取之后。GET取消可停止等待；mutation超时仅“结果待确认”，不得自动重发或宣称服务未写。Node覆盖fetch/body分别悬挂、abort、旧401；E2E真实页面挂请求→明确错误/重试恢复，不改服务业务时限。 |
| IX-B03/P1 | `web/src/pages/Dashboard.tsx:35–38`、`:43–58`、`:128`、`:175` | 首屏books/notifications错误被吞；books初始[]直接显示“还没有词库”。点击词库时清lessons=[]，加载期间及失败后显示“还没有课程/去摸底”，会诱导已有进度用户重新摸底；picking期间换库被静默忽略。 | books、notifications、lessons各自loading/error/empty状态，只有成功空响应才引导摸底；选库可见忙状态与重试。E2E延迟与500断言不出现假空态，再释放真实响应恢复，B账号隔离。 |
| IX-B04/P1 | `web/src/pages/Dashboard.tsx:92–103`、`:248–267` | 普通反馈发送时只有确认按钮loading，TextArea仍可修改；用户提交A后输入B，A成功回调清空并关闭B。澄清组件已禁busy/uncertain，普通反馈遗漏同类保护。 | 最小冻结提交中的正文与取消，未知送达保持原ID/原文及恢复入口；不能给mutation abort伪撤销语义。E2E真实POST延迟、输入/取消、丢响应恢复同submission；Node冻结草稿策略。 |
| IX-B05/P1 | `web/src/pages/Classroom.tsx:576–587`、`:844–850` | 输入门只约束业务就绪；点击提交成功send后无本地pending，phase/judge帧回来前仍可连续点击，每次生成新event_id。用户感受无即时反馈，网络慢时可能多发相同答案；是否服务重复扣机会须后端独审，不在此断言。 | 以当前epoch/turn同步登记发送中，立即可见“正在判分”并禁重复按钮/Enter/同turn自动采集；收到明确结果/新turn或断线解除，发送false不锁死、不REST绕门。E2E延迟真实返回帧，不篡改结果，连续点击/Enter只发一次，恢复后可继续；Node同步发送门。 |
| IX-B06/P2 | `web/src/pages/Books.tsx:15–16`、`:28–42`、`:69` | 首次读词库无loading/持久错误/重试；多个词库可并发enable却共用单个enabling ID，A完成finally可清B的忙状态，重复确认弹窗与导航选择混乱。 | 选择动作全局互斥或每ID独立状态，保持错误恢复；获取列表区分加载/失败/空。E2E A/B延迟乱序，按钮状态与modal最多一个，失败保留选择机会。 |
| IX-B07/P2 | `web/src/pages/Dashboard.tsx:128–129`、`:222–225` | 通知标已读没有catch/loading；失败成为未处理Promise且按钮无反馈。课报点多次可并发，较早课程迟到覆盖较新课程，弹窗不带课号。 | 通知独立busy/error、失败不假已读；课报最新选择generation+加载标题/课号，关闭使旧请求失效。E2E真实A/B课报响应反序、关闭后迟到不得弹回；通知500后重试。 |
| IX-B08/P2 | `web/src/pages/Placement.tsx:85–110`；`web/src/pages/Dashboard.tsx:160–167` | 摸底词条div role=button无tabIndex/onKeyDown，键盘无法完成核心流程；移除词库图标按钮无可访问名称。 | 使用原生button或完整键盘语义，Enter/Space切换aria-pressed，按钮命名包含词库。E2E仅键盘完成摸底/翻页/保存；无障碍名称断言。 |

## 性能与可靠性候选（需测量，不能当既成性能回归）

- **IX-P01** `web/src/audio/local.ts:53–64`、`Classroom.tsx:262`：每次中文重复均重新HTTP合成，同文多遍会重复付出模型延迟。建议主代理拥有的session/lesson有界成功WAV缓存；只缓存成功合法音频，token/lesson隔离，失败不缓存，不缓存Audio实例/URL，取消后不公布迟到结果。每次实际playing/end、字幕lease、ACK及设置间隔不变。E2E声明2英2中，同一段合成次数1而真实播放2；换课/换号隔离、失败重试和取消红绿；真实TTS另轨不代替mock。
- **IX-P02** `web/src/App.tsx:5–12` 静态引入8页，登录首屏需加载课堂与管理依赖。可用route lazy+明确Suspense/错误恢复，保留token keyed重挂载与Modal清理。先记录生产构建chunk字节和冷缓存登录请求/可交互时间，再评估收益；不预设未经测量的“快X%”。E2E每路由正常导航及chunk失败可恢复，不能白屏。
- **IX-P03** `web/src/audio/local.ts:26–43`、`Classroom.tsx:656–668,836`：固定4秒录音后上传/识别，listening一直true直到整次HTTP完成，UI在转写期间仍说“正在听你说”。改用采集/识别/判分明确阶段可改善反馈；缩短固定窗口或端点检测须ASR质量对照，不作为本次随意减时优化。媒体回调必须同步就绪门并禁止旧turn更新。
- **IX-P04** `web/src/pages/Admin.tsx:30–33,57–92` 全页busy让四个不同动作同时转圈，输入仍可改；提交对象是点击当时快照，后来的输入与成功提示可能不对应。建议动作级反馈/禁编辑正在提交表单、保留其他独立操作边界；不要仅解除全局互斥而引入同词库导入/发布竞态。

## 已确认应保留的保障

1. REST token实时读取、迟到401拒旧会话；App按userId+token重建页面并销毁静态弹窗。
2. WS每次重连先新ticket，401/403/404终止，其余退避；课堂snapshot不授予输入，epoch/turn控制手动和语音；typed/captured send失败不REST绕门。
3. 字幕实际onstart/playing才出现，lease与取消先失效，音频成功结束才ACK；恢复snapshot离播放相位立即cancel；不能为表面低延迟提前ACK或显示假字幕。
4. Settings/Stats已有独立loading/error/retry和过期响应拒绝；可复用其交互模式，但共享client超时仍需补。
5. feedback稳定submissionID与查询恢复已存在；优化必须保持未知送达状态，不自动创建新mutation。

## 验收设计与测量口径

先设计review→Node异步合同红→真实UI故障注入红→最小修复→定向绿→完整回归。网络夹具仅延迟/失败真实请求或缓冲真实帧，不伪造业务成功。A/B学生+管理员独立上下文，UI状态不交叉；DB前后证明进度/配置/成绩不被“提速”改变。

交互反馈预算拟定：本地点击后下一次React提交即可出现busy/状态，自动验收用保守≤500ms观察窗口；网络总时长与本地反馈分别测量，不用延长原业务超时掩盖。记录点击时间、loading出现、请求开始/结束、结果可操作时间，失败保留日志/trace。弱机器耗时必须标注条件，避免将测试主机资源争用等同产品性能。

新增最少覆盖：保存成功/失败/未完成再编辑、列表三态、反馈草稿、答题去重、课报乱序、通知失败、键盘摸底、全局timeout/body取消、中文缓存隔离/字幕次数、懒加载chunk恢复。每个实际修复BUG有Node/Go语义用例与Playwright体验用例，不能以已有86绿替代新树验收。正文/密码/token不进定位日志，仅操作种类、耗时、取消/超时原因、lesson trace与匿名资源ID。此文件仅评审建议，最终规格、优先级及实现边界待主审/架构会审。


## 实施后口径（最终应用回归已通过，不覆盖初始发现）

最终采用 docs31 的操作反馈目标 250ms、浏览器受控慢请求可见反馈观察门 1s；本报告初始建议的其他数值不再作为实施门槛。B01–B08 已按批准设计补保存单飞、请求截止、三态读取、稳定反馈草稿、答题pending、词库互斥、迟到课报拒绝和键盘支持，最终应用回归结果见 `interaction-browser-final116-20261015.json`。

P02 懒加载仍是未准入候选，没有修改为路由懒加载，也没有宣称拆包优化。新增五个隔离浏览器context的登录测量记录当前生产bundle交付和表单可用时间；既有服务与OS缓存可暖，五样本p95实际为样本最大值，不能冒充严格冷启SLA。实测见 `interaction-cold-login-20261015.json`：五个新 context 可交互 766.3–879.9ms，中位840.7ms；JS传输367284B、解码1152728B，加载78.5–93.9ms。此本地小样本未显示该chunk是本轮主要瓶颈，故不扩大为拆包重构；保留后续慢网/设备测量候选。

中间 red/green、选择器和时钟夹具修正、r3资源中止、1000词真实HTTP步骤耗时及误推断修正见 `review/interaction-browser-progress-20261015.json`。所有中间失败保留；未完成批次不计作整套通过。当前新增30条（含IX14真实UI及两种SQLite回滚、IX15草稿原子回滚）与既有86条合计116条已在相同生产源码与二进制上整族通过，真实TTS复播专项单列，不包括已知未通过的模型语义质量。


### 原始发现逐项实施与有限验收映射

下表保留实施与定向证据来历；最终116族已在三段执行中整族通过，不把历史r5或任何失败attempt冒充全绿。

| 初始发现 | 实际处理 | 已有用户路径证据/剩余门 |
|---|---|---|
| B01 保存/编辑竞态 | PlacementSave同步单飞、冻结payload、成功才导航、按book生命周期换gate | r5成功/失败/切库均绿；IX15真实draft失败全回滚定向绿，最终全族通过 |
| B02 无限请求/错误泄漏 | REST与媒体fetch/body共30s、abort分型、安全错误及响应trace；安全UUID兼容HTTP | Node60含请求/UUID；r5 stats悬挂恢复、媒体超时无假ACK绿；最终全族通过 |
| B03 假空态 | books/lessons/notifications显式加载失败空态，LatestRead代次+取消 | r5 pending/500不引导摸底、真实重试绿 |
| B04 新草稿被清 | FeedbackDraft冻结原正文/ID、未知送达保留恢复 | r5持有提交期间不能覆盖草稿绿；旧反馈族26条完整通过 |
| B05 无答题pending | 同epoch/turn同步AnswerFlight，文字/自动采集共门，明确判分中 | r5真实帧延迟只发一次、sendfalse不走REST合同单测绿 |
| B06 词库加载/互斥 | 列表三态、ExclusiveAction覆盖选择/删除及Modal生命周期；未知结果查询 | r5选择/删除/导航失效/丢响应不重写绿 |
| B07 通知/课报 | 通知单飞错误重试；课报LatestRead、关闭失效、课号标题 | 课报定向绿；通知真实反馈产生未读后失败/真重试定向绿；旧空通知前置失败保留 |
| B08 键盘摸底 | tabIndex、Enter/Space与aria-pressed，移除控件可访问名称 | r5及修正通知定向持久化真实选择绿 |
| P01 重复中文合成 | session/lesson作用域16项/8MiB/5分钟成功WAV缓存，每次仍独立播放/字幕/ACK | mock失败不缓存/换号隔离绿；真实TTS专项1/1，HTTP1次、3中文实际起止、miss1 hit2 |
| P02 登录大chunk | 未准入lazy重构，保留当前路由/会话清理；只测当前交付 | 5context约0.77–0.88s，样本/本地缓存边界见上，不称严格冷启SLA |
| P03 阶段不明 | 实际MediaRecorder onstart才录音，停轨后转写准备，答题发送后判分中 | Node实际stage2例+r5晚回调/退出无上传绿；录音时长/模型时限未缩短 |
| P04 管理端假多busy | 单动作loading、表单冻结、同步互斥与刷新代次/卸载守卫 | r5输入冻结/同页反序/离页迟到无刷新绿 |

真实中文缓存详细证据：`interaction-real-tts-cache-20261015.json`。中文为“猫是一种常见的家养动物。”，一次冷调度到playing8363.2ms，两次热373.2/377.2ms，均包含配置300ms间隔；n=1冷/2热，不是统计p95或纯网络时间。该用例不调用ASR/判分，不消除既有模型质量未通过项。


### 最终验收结论

`interaction-browser-final116-20261015.json` 为本轮唯一聚合索引：116个浏览器用例、60个Node用例通过，lint 0错误/0警告。九个正式接受族均完成TypeScript/Vite构建。前三段生产源码前后SHA均为 `ea3283a7f342a97310d19ff29981c9110bd4d4db05799e2091a8e127ef11a7c3`，server/harness亦完全一致。

执行明确分三段：恢复41+逾期1；反馈26+历史9+journey1+closure6+firstuse1+playback1；交互30。两次失败attempt分别为旧英文错误文案合同、AntD零宽透明退出动画污染按钮精确名称，修正测试后完整重跑对应族，失败报告保留；没有靠Playwright重试取绿、跳过或放宽业务时限。9次接受族build之外有2次被后续整族替代的attempt build，均留记录。

真实TTS缓存另1/1已通过；原模型语义质量未通过项与旧15.545s I/O延迟观测边界仍单独保留，不以本轮确定性应用回归宣称所有外部服务质量或所有宿主机I/O尾延迟已解决。

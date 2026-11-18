# 全双工课堂 SDD

状态：2026-10-15 后端/安全/协议修订稿，已交叉评审32/34，待主审条件性设计冻结；未实施、未执行推理或切换共享服务。本文“新增/应”是设计合同，不是当前能力声明。

## 1. 现状与边界

已读 `server/internal/api/{ws,ingress,roomcontext,agenttransaction,providers}.go`、`server/internal/localmodel/client.go`。现有课堂使用单 WS 读泵、有界 FIFO 业务 worker、epoch/turn/event_id 校验；判分释放 Room 锁并用 8 秒 context，返回后重新校验 turn/连接状态。语音为完整录音上传后转写，既有端侧 4 秒 PCM WAV 路径保留为 fallback。模型/媒体调用不得占数据库事务或课堂锁。

现有 2048 条内存 event_id 去重不是持久幂等；`callAgentTool` 的动作与审计事务不是课堂状态、结算与传输回执的整轮原子事务。IX16 阶段日志也不意味着慢磁盘已消失。新协议必须补持久提交协调，不能直接宣称已有 exactly-once。

目标：老师输出与学生输入可以重叠，允许打断和增量预览，同时任何教学状态、成绩、记忆、排课只由确认结果经 Agent 工具提交一次。端侧 AEC/NS/AGC 为采集约束，不能仅凭开启参数宣称回声已消除。完整双工声学效果另验。

## 2. 能力协商与远端边界

远端为用户指定、通过 SSH 跳转访问的 OpenAI 兼容服务，不默认连接 OpenAI 云。只读调查已确认代理链：nginx 124.156.193.122:8001 → http://1.92.241.129:8001；models返回401、health返回200、openapi返回404。模型key未提供，不能据此验证模型列表或模态。用户正在确认key安全配置与上游HTTPS/SSH；禁止绕过鉴权。单段SSH不覆盖nginx至上游的HTTP段，未补全链路保护前禁止真实课堂音频/文本走该明文段。本文不含凭据。

适配层分别声明并实测以下能力，未知值为 false/unknown：文本完整响应、Chat SSE、输入音频格式/时长、增量 ASR 与 final 标记、双向音频会话、流式 TTS、response.cancel、播放截断/历史校准、服务端 VAD、每会话并发上限。`/models` 列表及 HTTP 200 都不替代协议探针。

| 协商模式 | 启用条件 | 对外表达 |
|---|---|---|
| legacy | 原有本地完整录音与播放链可用 | 原课堂录音模式 |
| streaming_pipeline | 增量 ASR/可取消 TTS 等具体能力逐项通过 | 分阶段流式课堂，不称原生双向音频 |
| native_duplex | 双向音频、取消与截断语义及边界实测通过 | 全双工实验课堂 |

Chat SSE 仅证明文本增量输出；不能当作音频上行、声学轮次、可打断合成或全双工协议。若原生接口无法证实，`native_duplex` 为 `blocked_capability`，仍可评审其他模式但不得悄然降格后宣称原生验收成功。能力报告绑定 provider/model/version 与探针时间；运行时能力丢失要显式中断当前候选并恢复安全模式，不自动换云模型。

## 3. 分层与职责

1. 浏览器 MediaSession：权限、采集、AEC/NS/AGC、VAD提示、真实播放队列与已播放水位；不判分、不写业务状态。
2. ClassroomTransport：连接鉴权、会话 epoch、音频序号、流控、取消与状态同步；网络控制通路不得等待推理或 SQLite。
3. UtteranceCoordinator：收集片段、维护 transcript revision、候选快慢轨与确认屏障；每课只有一个可提交决定。
4. ProviderAdapter：远端协议转换、受限请求、取消与能力错误分类；不能持有平台工具权限。
5. Teacher/System Agent：只对确认输入决定教学/教务行动；所有动作走注册表 scope/schema/审计。声学 VAD、拥塞拒绝不是教学策略；何时提示、接受/拒绝答案、重教/跳过属于 docs04 决策表扩展。
6. DecisionCommitter：持久收据、状态前置条件、工具动作与审计、待发送事件统一协调。禁止模型直接操纵 Room、数据库或 WebSocket。

## 4. 标识与消息合同

新通道拟 `/ws/classroom-duplex`，独立版本 `duplex.v1`，不扩旧 WS 16KiB JSON入口承载无限音频。短时一次性 ticket 绑定用户、lesson、protocol；新增通道严格校验 Origin 白名单，不能沿用现有 `CheckOrigin=true`。ticket 不写访问日志。

| 标识 | 生命周期和用途 |
|---|---|
| session_id | 服务端创建，绑定学生/课次，稳定逻辑会话；不能由客户端指定另一课 |
| epoch | 每次连接接管/恢复变化；旧 epoch 输入与输出不可生效 |
| turn | 既有传输快照版本，状态改变即更新；不单独作为最终答案身份 |
| input_revision | 服务端逻辑回答版本，仅题目/机会/显式撤答/授权改变时更新，preview及judging展示不递增 |
| output_lease | 一个response内一个可听segment的播放授权，撤销后迟到回调不可ACK |
| utterance_id | 本 epoch 内单次学生发声，绑定采集起止与顺序 |
| revision | 同 utterance 转写修订单调递增；旧版本不可覆盖新版本 |
| response_id | 单个老师响应候选，绑定输入 revision、逻辑input_revision与题目事实 |
| segment_id / seq | 响应音频分段唯一标识/顺序；不复用旧响应段 |
| decision_id | 服务端产生的持久提交幂等键；不是客户端 event_id 的别名 |
| event_id | 单帧请求/回执关联，重发允许取同一结果，不执行第二次 |

控制 JSON 含 protocol、session_id、epoch、turn、event_id、type；具体帧带 utterance/response/revision。身份由 ticket 得出，不信任消息中的 student_id。二进制音频使用明确版本头与 utterance_id/seq/采样数，不能从无身份裸字节推断归属。协商固定 PCM16LE/mono/16k 为首版格式；压缩格式若以后加入需独立格式验证。

拟输入帧：`session.open`、`audio.start/chunk/end`、`response.interrupt`、`playback.progress/ended`、`state.sync`、`heartbeat`。拟输出：`session.ready`、`transcript.preview/final`、`response.preview/confirmed/canceled`、`audio.segment`、`decision.committed/rejected`、`flow.pause`、`state.snapshot`。类型与字段须形成 schema 白名单，拒绝未知字段/超长ID/非有限数值/非法状态；未来兼容通过协议版本实现，不宽松猜测。

## 5. 快轨预览与确认轨

快轨只允许可撤销字幕与中性提示，不宣布“答对/答错”、不推进题目、不启动有副作用工具。禁止把未确认答案先播成正确反馈，再靠最终纠正补救。预览可以被 revision 替代；UI 必须标暂定。

确认轨条件：同一 utterance 已结束；服务端取得明确 final 转写或明确用户确认文本；输入音频序列完整；epoch及逻辑input_revision/题目机会仍有效；无取消；模型协议与安全策略通过。静音、乱序缺块、无 final、互相冲突或超时均不默认猜答案。首版快慢转写只在保守normalized exact match时视为一致：依次NFC、Unicode lowercase、trim/空白折叠、仅移除单个终末中英文句号（. 或 。）；连续多个句号、问号、感叹号、省略号和逗号保持原样；禁止NFKC、改内部标点、数字、否定、词序或同音同义/模糊/编辑距离猜测。任何不一致进入澄清，不按慢轨覆盖或两票猜答案。只有单轨时必须明确模式并独立过质量门，不能伪造一致证据。用户确认只确认所说文字，不能确认自己答对；高风险教务意图继续既有澄清/确认策略。

同一 revision 的多个模型结果（如经批准并发）仅一个候选能进入提交屏障；较新 revision 到达则取消旧候选。`utterance.end` 不等于模型确认，也不等于成绩已保存。所有提交依赖服务端 input fingerprint（会话/epoch/input_revision/题目机会/utterance/revision/规范输入；起始turn仅冻结证据，不用当前judging turn重算），相同 key 不同指纹必须冲突拒绝。

## 6. 单提交、审计与恢复

新增持久 decision receipt（唯一 lesson_id+decision_id，及同lesson+epoch+input_revision的终态唯一约束（不含utterance））、checkpoint 与 outbox。提案状态：collecting→finalized→deciding→committed；旁路 canceled/rejected。临时 preview 不持久化为业务事实。

先在隔离状态副本计算候选checkpoint与Agent actions，不直接修改活动Room。提交事务内重新读课堂版本/课次归属和状态；CAS 限定冻结的 input_revision、QuizItemID/机会、session/epoch；preview、awaiting_verification与judging显示迁移不使合法答案失效，手动提交则必须改变逻辑回答版本；执行同事务绑定的 Agent 工具与审计；保存课堂 checkpoint、decision receipt、outbox。一处失败全回滚且丢弃候选内存状态，成功后才发布对应Room版本；发布前并发版本已变则仅对仍获授权的活动Room从已提交checkpoint重建，不再次执行教学动作；已退休/换账号/换epoch不得恢复或发声，仅保receipt供后续授权查询。不能在既有 `callAgentTool` 外层再调用独立事务导致嵌套/单连接死锁；需抽出显式 tx-bound executor，经独立红绿再准入。SQLite COMMIT 失败后的同连接清理必须沿既有原子连接合同，不吞错误。

提交前取消：不产生业务写入；提交竞态以持久事务线性化点为准。已提交后取消只停止媒体输出，不能反向撤销成绩或重新判一次；回传既有 receipt，标注 `committed=true`。审计失败不能报告成功。广播在事务后由 outbox 驱动；崩溃在 commit 与发送之间，恢复重发同 decision_id 而不重复教学动作。此单次保证是持久教学效果，不是物理声音 exactly-once；播放ACK丢失时按lease及段义务明确恢复，可能重播中断段但绝不再判分。

G1状态组件新增明确屏障：`Confirm` 的一致性检查不替代Judge；coordinator须在共享8秒内完成所需判分，再调用 `BeginCommit(identity, now)` 检查剩余截止并从verified进入committing。`MarkCommitted`只允许committing/已committed，反映已知DB提交，不在慢DB返回后拿8秒超时伪称未写。committing期间取消仅撤输出并请求事务context取消；结果未知保持committing并查询receipt，不准重新输入/创建新decision。只有获得事务确定回滚证据，`CommitFailed`才将其置为rejected（取消者为canceled），不原地自动再提交；恢复由coordinator新授权或同decision持久恢复协议决定。该组件尚未接真实Room/Judge/SQL协调。

重连创建新 epoch，先加载 checkpoint/receipt；未提交输入不自动重放评分。客户端同步最后已播放 segment/样本水位，只能校准播放历史，不能证明答案或工具提交。旧音频排队全部丢弃；明确返回“已提交待呈现”或“未提交需重说”。课次过期/账号禁用/另一连接接管终止会话，沿现有禁止重激活守卫。

### 6.1 可实施的事务边界与接口（拟新增，非现有API）

首版实验不直接扩展原全局Room handler。新增协调层在课次互斥提交门下捕获深复制State/题目/机会与checkpoint版本；锁外计算最终Judge结果及Teacher计划；短事务时重新CAS。媒体取消先撤销内存lease，不等待这个提交门。相同课次的键盘、legacy语音与duplex确认必须共用逻辑提交门，不能只对新协议去重。

| 持久对象 | 键与事务职责 |
|---|---|
| `duplex_checkpoints`（新增） | lesson_id唯一，owner、schema_version、input_revision、checkpoint_version、state JSON；CAS版本递增与事务一致 |
| `duplex_receipts`（新增） | decision_id主键；lesson_id+epoch+input_revision唯一（utterance_id仅作冻结证据，不能换句规避唯一约束）；fingerprint、最终状态/结果摘要、checkpoint_version。相同键不同内容固定conflict |
| `duplex_outbox`（新增） | decision_id+effect_index唯一；schema版本、response/segment/lease、已确认效果payload、投递状态。delivery不等于播放/教学提交 |
| `audit_logs`（既有） | 同事务写Teacher工具scope/schema/结果审计，额外关联decision_id；不保存原音频/预览 |
| `runtime_records`（既有） | 同事务写兼容snapshot/lesson-ID；防旧恢复代码读到新checkpoint之前的状态。只读fallback不能让两种状态源分别演进 |
| `duplex_completion_jobs`（新增） | lesson_id唯一，冻结result/state指纹、pending/done；最后一题后持久请求现有结算，不在判分事务内调用自建事务结算器 |

拟函数边界：

```go
// 类型名称为设计接口，实施时保持依赖方向，禁止对HTTP暴露*gorm.DB。
PrepareConfirmedInput(ctx context.Context, frozen FrozenInput) (VerifiedInput, error)
PlanTeacherDecision(state DetachedState, in VerifiedInput) (DecisionPlan, error)
CommitDecision(ctx context.Context, expected Version, plan DecisionPlan) (Receipt, error)
ExecuteTeacherPlanTx(ctx context.Context, tx *gorm.DB, task TrustedTask,
    plan FrozenTeacherPlan) (effects []Effect, audits []Audit, err error)
LookupReceipt(ctx context.Context, owner Owner, key DecisionKey) (Receipt, error)
PublishCommitted(room RoomRef, receipt Receipt) error
DispatchOutbox(ctx context.Context, owner Owner, epoch string) error
```

`PrepareConfirmedInput`只做受限远端调用/严格schema及保守冲突核验；模型输出字段白名单为转写/判分建议，不允许工具名或任意actions成为执行指令。`PlanTeacherDecision`由现有Teacher决策表和状态机的隔离副本生成可信计划，沿现D-20/D-21，不让模型suggestions直接入工具。

`CommitDecision`取得单一 pinned SQLite connection，用既有原子连接清理合同启动唯一事务；写锁→归属/课次/逻辑版本检查→查询已有receipt→执行可信计划→审计→checkpoint/runtime snapshot→receipt/outbox/必要completion job→commit。返回前不调用WebSocket/模型/TTS，不更新活动Room。`ExecuteTeacherPlanTx`仅注册本次Teacher允许的冻结效果工具（classroom.*），复用scope/schema及withTeacherEffects验证，不调用`callAgentTool`，不再获取agentWriteMu、不创建内部Transaction、不使用a.DB或全局Repo。registry传入同一tx-bound服务，工具输出只是待发布效果。按API包依赖边界包装store的原子连接入口，不能从store反向调用api。

提交门内不嵌套既有`completionMu → agentWriteMu`锁序。最后一题的receipt先表示“答案已保存，结算待完成”；completion job在判分事务外调用现有幂等`commitLessonWithAgents`（既有settled/lesson-ID指纹回放），完成后独立标done。崩溃在结算commit与标done之间，重试读取同settled结果，不重复改memory/cards/config。未完成结算不能发送“成绩报告完成”效果；失败保持job pending并可重试。该两步明确不是判分+整课结算同一事务，需故障测试证明最终一致，不将它包装成一次大事务。

非课堂配置/改期/投诉反馈继续走现有持久反馈任务，确认输入只产生带幂等关联的反馈任务请求；不借Teacher事务越权写System域。outbox中的反馈请求采用唯一decision_id派生submission_id，重放不能重复调整相对配置。首版如未完成该适配则禁用duplex教务执行并显式引导既有反馈入口，不能静默直写。

事务外发布时校验owner/epoch/Room未退休；若不可发布保留receipt，不能重激活旧房间。数据库已提交但内存发布失败返回“已保存待同步”，禁止以新ID重判。物理播放水位只服务输出恢复，不用于确认成绩。独立两进程写同DB时以唯一约束/CAS为最终仲裁，不仅依赖进程mutex。

新增表只在独立实验DB迁移；所有duplex入口关闭时原路径行为不变。启用同一课实验后，legacy文字回退必须通过统一CommitDecision，完成模式排空才允许返回旧handler；若不能做到该路由互斥，则该课不得启用实验。

## 7. 打断、取消与上下文

播放期间检测到学生发声，端侧先停当前输出并报告打断（语音活动不等于有效答案）；服务端立即取消该 response 输出与上游生成，标记已播水位。已有工具提交不撤销。未确认草稿不进入下一轮模型历史；确认老师内容仅在有可靠文本/音频对齐时把实际播放部分加入可听对话历史；首版无字级对齐则记录已完成段及中断状态，不凭样本水位截字，避免模型以为学生听完未播内容。

上游不支持 truncate 时，不能伪发不存在指令：清本地待播队列，按确认 checkpoint 构造新的受限对话上下文，并在能力状态报告该限制。上游取消不可靠时占用槽保留到真正结束/超时，不能假释放后无限并发。旧 response 的迟到 chunk、final、工具请求均按 epoch/response版本拒绝。

会话退出、切账号、课次切换、断线与服务关闭均 cancel 树；模型/媒体请求携派生 context，不得遗留后台请求。不能让读泵等待业务worker退出才发cancel。正常停止不标模型质量失败；超时/协议错/容量拒绝独立计账。

## 8. 队列、预算与资源隔离（拟首版上限，待测试冻结）

首版参数提案统一为20ms帧、80ms发送批次、300ms前滚、200ms尾留、600ms端点静音、15s单句上限，100ms起音候选但不得以最短100ms硬过滤短词；确认候选本地立即停声，不只duck（仍待主审发布冻结版本）。同input_revision语音活动租约只允许一次15s输入+8s计算，噪声或重复帧不得刷新超时；具体教学恢复沿32/04决策表。

每连接一个读泵、一个有界控制处理器与一个串行业务提交worker；严禁每音频帧无限goroutine。控制队列16、单用户活动utterance1、待复核utterance最多1；PCM输入缓冲最多2秒/64000 bytes，单utterance最多15秒/480000 bytes（468.75KiB）；老师输出未播放缓冲最多2秒。超过限制明确流控或拒绝本 utterance，不静默丢中间音频后继续判分。心跳、取消、关闭有独立保留容量；其验证仅做有界内存检查，持久账号状态更新以吊销信号和提交前再检双保险实现。

模型容量为显式全局信号量与按用户公平队列，初始远端推理槽1，仅串行调度；快慢两模型同时运行需另有独立槽与实测资源证据，当前不承诺并发。确认轨优先取消/结束快轨再获取槽，计入同一确认期限；不得擅自扩大远端GPU并发。排队最多2秒、endpoint到最终确认计算总计8秒（包含排队、声学复核及文本Judge；分两调用共享剩余deadline，不各自重置8秒）；数据库提交与真实播放分别计时，提交途中取消须查询receipt而非假称未写、媒体请求20秒沿现门；新流首次结果/空闲/整utterance上限由能力探针和34测试冻结，不能把每chunk刷新deadline变成无限请求。每阶段耗时/队深分别记录，吞吐不足是准入失败而非加长产品门取绿。

实验使用独立 app端口/SQLite/工作目录与 feature flag，默认关闭；单独显式配置远端隧道endpoint与model，不复用8090配置作隐式覆盖。SSH仅本地loopback绑定、固定host key验证，权限最小化；隧道进程仅由实验拥有者启停。不改现有共享8090/8000/8001/8002服务、模型或默认账户。GPU/CPU/内存预算由主审运行计划限定，任何OOM/平台故障停试并保留证据。

## 9. 安全与数据生命周期

学生语音、转写、模型输出都是不可信数据；模型提出工具调用仍需角色/scope/schema/业务前置条件，不能凭语音改变角色、提示或跨用户课次。远端base URL固定配置白名单，不接受用户提供URL，拒绝HTTP重定向；本机隧道不等于可跳转任意内网。API key只在服务端内存/授权配置，不进浏览器、WS帧、命令行、数据库、报告或日志。

音频默认仅内存短环形缓冲，结束/取消立即释放；不落原始音频、完整转写或模型原文。若显式授权诊断采样，独立受控目录、明确保留截止与删除清单，并不得进入Git。程序无法保证运行时内存物理擦除，应明确仅释放引用，不宣称密码学擦除。持久receipt只保存必要的幂等指纹、版本、结果摘要与工具审计；若工具必需内容沿既有数据访问边界，不复制到普通日志。错误输出固定枚举，无上游body/SSH命令/凭据回显。

## 10. 可观测与验收交接

稳定 trace=`lesson-ID`，关联 request_trace/session/epoch/utterance/response/decision/segment/revision；用户标识仅必要ID。事件覆盖 capability_selected、audio_started/ended/rejected、preview_revised、confirmation_started/completed、decision_committed/conflict、response_interrupted/canceled、stale_output_dropped、stream_backpressure、provider_timeout、session_recovered、fallback_activated。记录字节数、样本数、时间/队深和固定reason，不记录正文。

性能分开量测采集→预览、end→确认、确认→首次真实播放、软件VAD→lease撤销/本地cancel，以及真人近端起声→物理扬声器静音（独立参考声道实测）、取消→上游槽释放、事务锁等待/写/最终提交、重连→可继续。软件取消目标p95≤120ms；物理静音目标p95≤250ms/p99≤500ms，按34每设备至少100次、参考声道校准，unknown不能判通过。N=2/4先证明槽1过载有界/公平性，不预称4并发性能SLO。端侧声学效果、真实播放与数据提交各有证据；服务器发chunk不等于用户听到。

34测试应包含真实WS/真实浏览器多学生隔离、乱序/重复/缺块、快慢冲突、提交前后取消、COMMIT故障、commit后崩溃重放、重连/旧epoch、迟到媒体、队满、恶意超长/跨用户、回声导致误打断、远端能力不足及legacy恢复。Mock确定性门与远端冻结语料质量门分账；不能以模拟stream通过声称原生全双工可用。

## 11. 开发前阻断清单

- 远端实际能力及模型/接口/隧道端点由主代理提供探针证据；未证实 native duplex 时该模式阻断。
- 32确认“快轨可说什么”、打断恢复和澄清 UX；34确认15秒新utterance及各队列/时限提案，不能与既有4秒fallback混为一次合同变更。
- 明确持久checkpoint/工具/outbox原子边界及tx-bound重构范围，先故障用例与专家共识，不直接拼接现有非原子调用。
- 明确远端资源配额、数据传输范围及实验清理所有权；默认共享服务完全不动。

### 条件性设计共识范围

G1最小实现可先落 `internal/duplex` 纯协议/规范化/候选版本/取消/单确认/预算组件，以及 `store/duplex_*` 的 `CommitDuplex(ctx, expected, candidate, trustedTxCallback)`、`LoadDuplexReceipt(ctx, owner, key)`、`LoadDuplexCheckpoint(ctx, owner)`、`ListDuplexOutbox(ctx, owner, afterSeq)`。callback只获当前事务句柄、不得开新事务/联网/发布Room；故障集成测试可验证同事务写入与回滚。组件通过不代表该callback已接现有Agent注册表，也不代表Room、结算job、反馈任务或浏览器已完成贯通；35台账须逐项标未集成。真实业务启用前必须完成§6.1全链边界测试，不能用component通过替代。

建议主审分别批准：A）可独立mock验证的provider抽象、strict OpenAI兼容文本/音频请求适配（未知能力关闭）、媒体会话协议、持久receipt/checkpoint/outbox及故障测试开发；B）真实远端调用与native_duplex/自动教学启用。A无需等待外部原生能力证据才能开发，但仍须主审明确批准；B必须同时具备鉴权、全链路传输保护、能力探针、资源、真实质量和声学门。基础协议测试通过不得宣传全试点可用。

32/34交叉结论：共同确认deadline含排队≤2s、总计算8s；物理静音与软件取消分指标；unknown不能通过；首版冲突保守exact match，否则确认。远端401是鉴权阻断，不得绕过；nginx后HTTP链需单独保护，不能以单段SSH代替。剩余外部能力门不阻塞A的可独立设计，但阻塞B启用。当前仅文档，未经主审明确批准不实施。
## G1批准实施接口边界（2026-10-15）

主审已批准G0及条件性G1。以下为本轮实际可实施边界；前文完整Room提交/结算/outbox设计仍须后续接线评审与端到端验收，不因组件存在冒称完整全双工可用。

### remoteprovider严格兼容客户端

新增独立后端remoteprovider只提供`Models`、`Chat`、`ChatJSON`的OpenAI兼容文本合同。模型ID和endpoint来自受控服务端配置，未知流式/原生音频能力保持unknown，不能从Models列表或普通Chat成功推导支持音频。真实接入资料由根代理只读核实；本轮接口不能猜路径、凭据、模型名或自动回退云服务。

仅允许HTTPS，或显式声明受保护通路的loopback端点；拒绝公网明文HTTP、URL userinfo及凭据query，不接受页面传入base URL。受保护loopback仅描述客户端这一跳，配置者仍须确认隧道后的链路受保护，不把SSH到跳板等同端到端TLS。保留证书/host key校验，禁止跟随重定向。

每次调用一个派生context总deadline覆盖dial、响应头和完整body，Models/Chat/ChatJSON均有有限read cap，读取上限+1以识别截断/超限，不解析被截断正文为成功。取消/超时与HTTP状态/协议错误使用固定分类；错误不得包含上游原body、URL查询、认证头、请求原文或SSH命令。ChatJSON需严格解析单一合法结构，不能从解释文本中猜提取JSON或把空/错误类型零值当有效输出。默认不自动重试，避免同输入未知完成导致重复请求。具体时限/上限在实现配置与单测中冻结，不能为各阶段重新计时。

### 只读课堂预检能力端点

`GET /api/v1/classroom/lessons/:id/duplex-capabilities`：学生认证与现有ownLesson资源归属校验；跨用户、非法课ID或不可用身份沿现有拒绝合同。不得只凭客户端lesson ID或查询开关返回授权。

成功data固定形状：`{pilot_enabled, mode:"preflight", native_duplex:false, automatic_teaching:false, reasons, limits}`。`reasons`仅固定安全枚举，不泄露密钥、远端URL、原始错误或机器配置。`limits`返回33冻结的可观察端侧限制（单位明确），不表示远端吞吐或声学能力通过。端点只读，不创建Room、迁移课堂、发模型请求、写receipt或预加载模型。

`ENWORDS_DUPLEX_PILOT=1`仅开放预检入口与UI；未配置默认关闭。任何其他未知值按未启用处理。无论该值如何，当前响应native_duplex与automatic_teaching都固定false；不因配置开关绕过G2/G3。

### VoicePreflight路由

新增VoicePreflight只在用户动作后读取设备requested/effective/unknown、运行本地PCM/VAD预检及显示观察状态；不上传音频/草稿、不调用模型、不启用原生双向通道、不接管现有Room或教学工具。离页/换号/关闭麦克风必须释放track、AudioContext/worklet及timer；失败仍允许返回原课堂/文字。远端连接文档新增内容由主代理脱敏归档后再补能力证据，不能仅凭文件存在解除真实接入阻断。

remoteprovider.ChatJSON能力边界：仅检查JSON无歧义、类型、unknown字段、null和深度，不从任意Go struct推断required/枚举/范围。空对象或缺字段可以成功解码为零值；必须由显式业务adapter校验required/枚举/范围后才可用于决策。目前未接教学判分或工具，不能宣称完整JSON Schema或可信教学输出。

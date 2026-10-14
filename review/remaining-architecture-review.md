# 剩余闭环架构与安全设计复核

日期：2026-10-14。评审角色：独立架构/安全；本记录不代替实现后的全量验收。

## 课堂并发和媒体设计（docs/18）

设计通过。同步 REST 判分应在 Room 锁外调用模型，返回后依据问题代次、相位和暂停状态提交；取消、退出和暂停使旧代次失效。持久化 judging 缺有效在途任务时恢复同题等待，不消耗答题机会。WS 背压关闭连接，通过状态快照和独立恢复动作重放未确认段；不能将队列丢帧当成功。Close 不回调持锁 Room，Tick 不发起模型网络调用。

媒体必须捕获原会话 token，在上传、响应体读取后和播放成功结束时复核身份/取消状态。迟到麦克风授权、录音启动异常、播放异常均释放资源；旧媒体不能跨账号提交。TTS onerror/watchdog 不发成功 ACK，服务端超时降级与真实播放成功分别留痕。WAV 校验容器和格式，不代表音质通过；允许未知块与奇数字节填充，拒绝越界、空数据和不完整帧。已向实现方指出播放结束身份检查与 Audio 构造异常 URL 清理，待最终代码复核。

## 工具和版本（docs/16）

历史实施前盘点：13 个当时注册工具，对照 SDD 29 个去重能力条目。能力、注册、受信执行入口与真实旅程调用必须分别验收；管理能力不自动授权 Agent。课堂效果工具不重入 Room；逾期教学策略由 Agent 决策，持久任务保证重放幂等。实施前技术栈 React19/AntD6 与原锁定版本不同；用户已于2026-10-14明确批准保留当前版本，docs01 ADR及docs16已更新，AGENTS保持原文。当前实际Registry为31项，最终验证仍以测试报告为准。

## 本机评测样本和执行器（docs/15）

英文独立音源16条、中文 TTS12条标签无内容阻断，人工试听仍为 pending。反馈64条中的四处改期文本已要求消除“目标日请假却改到该日”的歧义；非法日期/备选日期必须要求 needs_confirm，不能把生产 guard 纠正计为模型理解正确。标签修改在真实推理前完成，冻结 SHA 后再采集。

评测器使用真实生产子请求 body 指纹记录同一次模型响应；纯投诉旁路不进入模型质量分母，混合投诉只评实际非投诉子请求。原始 JSON 先校验类型/必需字段，模型语义重复与产品去重分开判定。计划禁止动作指标不得声称执行副作用已验收。已要求报告写入失败向调用方报错，以及产品唯一意图显式断言；待执行器单测和最终复核后运行。
# 2026-10-14 实施期交叉复核补充

本轮T1/T2/T3与docs19由本架构代理实施，主代理承担独立产品审查，后端/测试代理独立回归；本节不将作者自查冒称独立通过。主代理已认可受信工具范围、Room→agentWriteMu锁序、逾期版本凭证及冷恢复、父claim与计划同事务、月度receipt额度和FIFO不刷新年龄。其反馈的额度魔数已统一为Agent访问函数。生产保存点已通知测试代理，最终全量尚未收齐。

对其他代理代码的独立复核：WS每次重连刷新认证ticket且拒绝旧会话发送；Classroom仅成功onend ACK，失败/看门狗不伪造播放完成。曾指出旧SpeechRecognition.onend可重启旧麦克风，前端已加实例+token守卫并新增迟到回调E2E。反馈父回复组件应禁用发送中编辑、不确定送达保留同一子ID查询，已交前端处理。FIFO用户摘要必须显示已提交事实，读取词条错误回滚，日志仍仅资源ID。

模型通用schema/prompt设计通过：原始七字段先强类型校验，非法日期/有限置信度/允许kind边界明确，产品规则纠正不计模型语义成功。新增留出标签在推理前改正负数槽位及日期措辞歧义；28条独立留出已由测试代理冻结。中文独立Piper12条ASR标签通过内容复核，音源与平台不同、人工试听pending、权重不随仓库分发。媒体质量评分应验证manifest SHA和唯一case集合、中文以CER为主，失败仍入分母；这些是评测可靠性条件，不代表真实质量已通过。
## 最终冻结点安全与AI-Agent只读复核

2026-10-14再次阅读 feedback.go/zhipu.go、agenttools/agenttransaction、teachereffects/roomcontext/hub、feedbackstorage/execution/clarification/limits/tools、boost_policy 与 overdue 执行事务。结论：下列已声明边界未见新的未关闭实质缺陷；这是代码复核，不替代后台最终Go/E2E结果，也不证明任意自然语言模型正确。

- 原文频率读取完整数量，负数/小数/超限/多个备选不得默认1或仅取尾数；错误kind不能绕过原文无效频率约束。日期中文数字只改校验副本，Raw保留；模型明确目标与提交锚计算日期不一致需确认。收尾确认“五月一号，或者二号”曾因逗号分句绕过省略月份备选检查，已反馈主代理；现代码允许首日期到备选连接词间的逗号/分号/空白，静态复核该阻断关闭，红绿证据由测试代理留存。
- Zhipu七字段逐项存在且非null，再Go强类型解析；未知kind、非法日期、非有限/越界confidence均拒绝。非法模型响应进入显式provider_fallback日志，不能将降级结果计入原模型成功。规则仍不是所有自然语言的完备授权证明，真实留出质量单列15/20。
- 31项是实际Registry能力名，管理REST不冒称Agent自主权限。客户端JSON不能构造agentTask；role、app及受信触发校验有效。Teacher index只选冻结动作且工具名必须匹配，不能伪造相位或判分结果；同事件预算不会因封装重置。记忆修订/删除同时核对owner、student、book。
- 反馈每事务第一句UPDATE获取SQLite写锁，随后读取月度成功receipt、父claim与FIFO状态；任务/意图/业务写/审计一起提交。父须同学生实际completed clarify，认领和子计划同事务；失配拒绝且清理无计划子任务。已完成意图跳过，重复加强不刷新年龄；淘汰与新标记、词面摘要读取一起回滚。成功升级日志仅在提交后发出。
- Room网络判分解锁后返回核对pending对象、turn、paused、retired及ctx；Join取消旧请求并提升turn，退休取消判分，旧结果不能消耗新答题。Teacher后续效果复用当前调用局部ctx；WS换连接与浏览器媒体会话守卫共同拒绝迟到旧输入。
- 逾期生产入口RunOverdue经版本claim、冻结计划和事务凭证运行；冷恢复保留历史再规划，新旧课/通知/成功审计原子。内存模式不承诺进程崩溃恢复；多实例验收必须SQLite。范围外的外部Agent凭证、观察员与语法插件不在本轮通过声明中。

交叉证据：后端代理已报告docs19首轮定向API44.339秒/scheduler4.728秒通过，新增额度双实例争最后额度与跨学生父澄清API8.977秒通过；额度回滚、真实消费/FIFO重激活与持久序号检查、最终全量和覆盖率仍以其最新最终报告为准。前端busy/uncertain草稿锁及旧recognizer实例/token guard已实际复核；自动音频容器检查不等于人工可懂度通过。
### 冻结后全量复核纠正

主代理追加复核发现：overdue退休分支使用`interface{ Close() }`，而真实`wsClient.Close() error`，类型断言永不匹配，导致旧连接未真正关闭。前轮只检查退休/取消/调用存在，没有核对可选接口方法集；已有测试只检查retired与旧课Enter拒绝，不能证明socket已关闭。`TestOverdueRetirementClosesClientWithRealCloseSignature` 已先红（`.test-artifacts/overdue-close-red.txt`，retired=true而closed=false），随后最小修改为实际`Close() error`接口并调用关闭；其他Hub.Join/Drop方法签名经全库扫描正确。后端代理已确认真实Close签名回归及Windows WS reset接票恢复定向通过；浏览器活Room过期关闭验证与全量证据仍待最终记录，不能只以生产修改保存声明通过。

旧事务用例失败的另一原因是fixture正文“明天”而fake模型返回原课（已在明天）再加一天，新错日guard正确拒绝；应把正文改为明确目标月日，保留原有失败回滚、资源重检和重试断言，不改产品安全检查。IT09旧期望原课+24小时在检测时已过去，按已批准新策略改为检测日次日原时分与当前通知文案，未逾期检查统一用生产CronOverdue适配入口。

### ISO明确日期补充独立复核

已读docs20与feedback.go实际代码：yearDateCueRe捕获异常年份分隔串，严格Parse/格式往返/正年份校验；显式ISO优先于相对日期，不滚年、不因非法日期退回明天。完整目标参与全文备选与模型错日校验，Raw保留。此新增合同限YYYY-MM-DD及年份分隔异常cue，不声称完整支持任意中文日期。backend已报告失败复现iso-date-red.txt、修复后全feedback 2.389秒与扩展定向1.506秒绿；原ISO浏览器旅程继续执行，最终全量仍待收齐。

### REC08退休重连与真实录音协议复核

REC08暴露expired课堂enter返回包装400导致无限重连。最小修改独立复核通过：Hub明确资源不可用sentinel，router仅errors.Is该类映射403；存储/快照错误不误分类。Enter拿Room锁后复查retired，Join在激活前拒绝退休并关闭新连接，覆盖取ticket到Join间竞态。定向已由后端报告绿，真实浏览器最终证据待收集。

真实MediaRecorder的WebM不能直接作为平台WAV上传。docs21及wav.ts/local.ts实际复核通过：OfflineAudioContext解码到16kHz，明确多通道均值而非假定构造参数保证mono，有限采样校验、PCM16LE/RIFF一致、audio/wav与recording.wav；转换前停麦克风，每await后校验会话/取消，失败不上传。此代码正确性结论不替代真实ASR语义或人工试听验收。

真实媒体夹具追加交叉review：原先独立音源在MediaRecorder开始前播放，可能截词头，已要求改为同stream WeakMap绑定并在NativeRecorder真实start事件启动源，当前实现及顺序/时长断言复核通过。WAV纯函数5例覆盖头/采样边界/立体声均值/异常几何/取消身份；3条浏览器解码失败、退出、换号断言停tracks和零上传。REC08真实A连接关闭/终止、B继续、未来新课disabled旅程符合已批准调度语义。

### 最终回归收尾证据

后端已完成最终Go覆盖率门及race全绿；REC08浏览器真实退休旅程已通过。依赖反馈E2E首轮曾读到stale_target/storage_unavailable，但独立只读实际SQLite确认最终两项为stale_target/dependency_failed、均attempt2/retryablefalse且完成时间相差约262ms。根因是测试把请求中busy导致按钮隐藏当作任务完成，在逐项事务间发GET；应等待对应retry POST响应后断言，不改生产、不弱化结果码。浏览器已接修并重跑。

TTS隔离修复证据与未应用状态见platform-tts-utf8-application.md：同模型UTF8输入关闭中文窄argv编码缺陷，3中文样本合法非静音，混合英文OOV为明确内容失败；实际候选HTTP猫200。共享平台尚待用户批准仅8002重启，不能将隔离成功计作产品真实课堂或人工听懂验收通过。

### Runtime快照日志补全独立复核

实际代码复核persistence.go与Hub恢复路径通过：runtimeTraceContext保留已有trace、nil安全，snapshot/report/settled/agent_result四类canonical正int64键归lesson，其他生成非空trace；保存成功/失败补关联。readRuntimeError集中记录非NotFound读取/解码失败reason=read_failed，覆盖启动/逾期/结课直调，旧布尔包装不重复日志；Hub仅状态Restore语义失败invalid_snapshot与恢复成功补Room trace。无原文/error详情泄漏，原返回与状态语义不变。测试与坏快照重启E2E以最终后台记录为准。

### 临时课堂连接错误可见性实际复核

已复核ws/client.ts与Classroom.tsx最终小改动：onError明确retryable，临时enter失败显示提示并保持单timer退避；401/403/404先close不重试。onOpen清旧错误，Tag仅终止错误显示连接已停止，临时仍重连中，返回/手动reload入口保留。发现classroom_error分支字符串state迁移遗漏并交前端修为对象，实际复核关闭；REC09日志断言误用payload.reason也已指出，应与obs顶级reason一致，不改产品日志格式。Node2例锁临时恢复与终止，破损快照真实重启E2E最终结果待测试代理。

### 当前确定性验收与提交安全复核（2026-10-14）

第四轮Go为441顶层用例（434通过、7显式真实服务/harness入口跳过），coverage/vet/race通过。72条确定性浏览器用例按同生产树r8/r9分组通过，完整证据见16§7及 `final72-browser-manifest.json`；不把r8夹具失败抹成一次全绿。TTS共享8002随后获用户授权应用，真实8090“猫”烟测HTTP200；部署、备份与其他服务PID不变证据见22，完整媒体/人工试听仍另行验收。

对293个modified/staged/untracked且非ignored候选文件进行路径与文本静态检查，未读取secrets目录。未发现secrets、运行DB、工具二进制候选；未发现真实key前缀、私钥或JWT匹配。宽泛凭据赋值规则4处命中：`review/apply-platform-tts-utf8.ps1:38`为授权说明，`web/e2e/closure-regression.spec.js:6`、`feedback-regression.spec.js:5`、`isolated-fixtures.ts:3`为新临时SQLite和隔离端口使用的固定测试密码，前端验收代理确认不用于真实账户或共享服务。仅记录路径/行号，不输出值。此检查是当前候选树快照，后续新增文件应在提交前重扫。

docs23设计复核通过：每秒/focus/visibility只刷新展示时间门，服务端开课授权不变；取消计数cleanup保留最新代数增量，Map仅在实例不替换前提捕获。局部规则豁免与逐项解释可接受，不可全局disable。实际实现及新增REC10回归待后续保存点复核，不能以设计批准代替代码验收。

### docs23实际实现与候选模型留出复核

随后已读取clock.ts、Dashboard、Classroom及局部警告注释的实际保存代码：每个时钟订阅独立timer和监听，cleanup先标记disposed、移除监听并拒绝已排队回调；Dashboard lazy时间初始化只决定显示按钮，不改server时钟或授权。Classroom固定Map实例捕获、请求代数cleanup递增、lessonId显式依赖符合设计，没有用捕获旧标量削弱跨会话拒绝。Node4合同覆盖时间边界、focus/可见性及卸载隔离；代码复核无新增阻断，REC10与全量结果仍以测试代理为准。

候选模型新32条留出在任何候选推理前独立核验：跨年后天、2028闰日明天、ISO明确目标与锚一致；绝对数量、未指定数量、多意图、同义去重、频率越界、非法/备选日期与未决定数量的needs_confirm标签一致。安全禁止动作针对Agent写动作，不以澄清/投诉回复充当业务写成功。未修改标签内容，已冻结 `feedback-candidate-holdout.json` 为frozen_reviewed及SHA256 `480f46a27696b5f331d27888dc075b7e86ca9a7c6d5f3f3ff488adc8aea98b2e`；未运行模型。另发现评测CLI案例数校验仅允许64/28，已通知主代理支持32并更新错误提示，不能把机械入口拒绝当模型能力失败。原64/28仅作已见回归。

### 恢复就绪门实际代码复核

REC05真实恢复暴露enter快照已wait_answer、WS已open但show_quiz_item稍后清稿。docs23§6与实际InputReadiness/Classroom补丁独审通过：只有业务帧grant，重复同epoch/turn/phase复用对象、同题保稿；新题清稿，撤销后即使同context也因对象代次不同拒绝旧采集。native识别固定capture对象/token/实例，本地采集开始与await返回均复验；effect依赖完整inputTurn，覆盖同词不同turn。typed/captured发送在socket实际已关闭但onclose未触发时不走REST，dispatchClassroom微窗口保护已核实。普通控制保留既有降级，服务端授权未改变。Node路由与REC11主要用户路径分工明确；实际全量由浏览器代理回报，不以代码复核替代测试。

候选代理另发现阻断：产品8秒超时后代理仍可阻塞上游20秒，旧请求占候选单槽影响下一案例公平性；必须下游断开取消上游，并用慢响应离线服务验证。生命周期finally若terminate/wait失败不能带活候选拉起原模型，应kill/wait确认退出。已交后端代理修订，未执行候选停服实验。

上述候选阻断随后实际代码复核关闭：代理硬8秒计时器持有上游socket并shutdown/close，覆盖等待header和body；慢上游测试验证关闭及下一请求恢复。它是固定8秒截止，并非任意更早客户端取消的即时传播，不夸大保证。生命周期正式调用manager停止接口清PID文件以避15秒自动重启；try/finally先于停止，terminate失败kill/wait，仍活则禁止原模型并存启动并明确恢复失败。参数/脚本SHA与其他服务身份门保留。已告知后端在完整模型size/SHA及独占窗口满足后按授权方案执行，下载未完成前不得停服务。

### 最终增量一致性与安全复扫

本次待提交候选增至418个，新增只读资源报告后419个。secrets、运行DB、工具二进制路径候选仍为0，真实key前缀/私钥/JWT匹配为0；宽泛凭据规则仍只有此前已解释的4处授权说明/隔离测试密码。扫描仅输出路径行号，不输出值；AGENTS未修改。新候选脚本只把原命令行经api-key脱敏后留证，原始密钥不跨进程输出。音频/模型报告是合成标签与试验结果，不是用户录音或模型权重；仍按当前文件清单在提交前复核，不将静态扫描称为绝对无秘密保证。

配置/WS增量核对：就绪门没有新增服务器配置键或协议字段，使用已有context.epoch/context.turn与业务action；state_snapshot/enter/WS open不授作答权限，typed/captured不经REST绕过，控制/独立反馈继续旧合同。现有课堂console诊断只见lesson/phase、voice名称/语言/速率及队列长度，不含答案/token；模型代理只保存已声明试验原始响应并禁HTTP头打印。75条最终回归正在执行，72旧树绿与73中途失败均不能冒充新冻结树全绿，结果由主代理最终台账更新。

Fun-ASR-Nano只读资源与依赖计划已落 `fun-asr-nano-resource-plan-20261014.md`：完整仓库1,989,178,168字节，模型+主要CUDA轮子已知下限4,849,409,992字节；完整传递依赖总量未解析，不虚报。系统Python3.12.10/Torch2.6.0+cu124不满足当前官方2.9最低要求，Piper环境不改。未下载、安装或切ASR，GPU/CPU20秒能力均需后续隔离实测。
# 2026-10-14 新候选后的增量独立审查

## 最终本地提交就绪复核

已直接核对r6原始coverage/vet/race摘要：445顶层通过+7显式跳过，896含子通过+7跳过，19包通过且源码前后不变；全部覆盖门通过。final78八组原始报告合计78通过、失败/跳过/flaky均0；生产SHA `2eb237cf4ec8037bb6ff394caeb05c151d59c446479e9d490ce922f0f1b321cd` 前后相同，逐文件重算无变更。可提交简版报告与原始分组、Node17、8次构建和lint零警告口径一致。

唯一新树B真实媒体0/1保留：3段TTS与4次ASR成功，中文准确转写；两次`l2/model_verdict/wrong`与314/213ms文本成功请求同lesson-1，排除本次协议错误/超时归因。docs10/25/27与可提交媒体报告均明确模型质量未达门，未用确定性78通过掩盖失败。候选9B质量不足、原模型已恢复、生命周期pending与独立恢复分账保持。

最终新增报告落盘后再次扫描622个Git跟踪/非忽略候选：禁止路径0、常见密钥/私钥/JWT模式0，AGENTS未改，证据`.test-artifacts/final-commit-security-scan.json`。本地提交已具备证据与安全检查条件，但这不是“项目总验收/模型质量通过”的结论；后续模型质量和资源计划仍按文档明确保留。未执行提交或推送。

最终数量保存点已复核收敛：或/或者及跨分句共享单位、legacy个作用域与提取一致、G_other拒绝日志均修正；定向feedback3.456s/API5.328s已由后端回收，允许统一产品冻结。历史初审发现保留，不代表仍未修复。裸逗号无明示连接词不扩为本轮通用语言承诺。

最终增量安全扫描覆盖620个Git已跟踪及非忽略候选路径：禁止目录/运行DB/工具二进制路径0，常见密钥前缀/私钥/JWT模式0，文本解码跳过0，AGENTS未改。输出仅路径/行号与计数，证据`.test-artifacts/final-incremental-security-scan.json`；模式扫描不能替代任意未知格式凭据的人工审查。当前新增模型脚本/报告沿用端点摘要，不读secrets或输出原进程密钥。

docs27实际实现复核与定向结果：Judge每条返回路径只调用一次只读Decision，hook仅有固定source/reason/result，不传原文/词面/底层错误；App闭包读取当前Log，原lesson trace透传，L2调用后取消仍由Room判stale，不改变返回评分。缺失/null/非法枚举返回固定ErrInvalidJudgment，合法wrong保留model_verdict。judge全包1.326s通过、语句覆盖93.3%；新API日志测试3.585s通过，换logger后旧sink不再增长且隐私断言成立。证据为`.test-artifacts/judge-observability-green.txt`和`judge-trace-green.txt`；完整跨包覆盖/race/浏览器仍待统一回归。

- docs26主审通过，03/04/SDD已标设计待验。实际初版数量守卫发现“每课50或60个词/50至60个词/50、60个词”共享尾单位可能只捕获60，以及规则小数/半次数澄清缺固定拒绝日志；已交后端补红和修复，不能把当前初审写成零问题。
- docs27最小判分来源设计独审通过，不改评分/提示/课堂机会数。已后台实跑适配器红例：`TestLLMAdapterSeparatesInvalidJudgmentFromModelWrong`失败于缺失/非法枚举返回nil，证据`.test-artifacts/judge-observability-red.txt`。真实句子B准确ASR+HTTP成功仍无法凭旧日志区分合法wrong与协议失败，需同次来源证据，不能新增猫子串规则迎合。
- 实读recovery-regression新增两条数量/一条判分测试与专属本地adapter：真实POST重放、同ID幂等、两学生四资源表不变、合法双维配置、两次真实作答后重教/0%结果及不同判分来源日志，未发现新阻断。它们是协议夹具E2E，不是模型质量证明；新增和全量通过数待实跑。
- 候选生命周期恢复启动只做DEVNULL句柄加固，snapshot仍捕获结构化输出。同型Windows临时Start-Process在旧/新行为均通过，因此保留真实pending和独立恢复证据，不冒充已证明挂起根因。完整观察见`candidate-launcher-observation-20261014.md`。

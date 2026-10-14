# 本机模型、运行恢复与开放能力后端验收

日期：2026-10-14。本记录是 C 基线之后的增量验收，不覆盖 `C-backend-test-review.md` 历史证据。全量覆盖率、vet、race 结论待下方最终证据更新；真实质量未通过不能用确定性测试绿代替。

## 已闭环的后端问题

- 阻塞模型判分原来持有课堂锁：Enter/SessionTimeout 红例已复现，现外部调用解锁并结合回合、连接、取消、退休状态拒绝旧结果。重复事件仅一次L2；暂停/Drop/HTTP取消不消耗答题次数。
- Join重发未确认片段且旧turn失效；判分中恢复不罚学生。测试只认 `ui_action.action.type=play_tts`，早期误认顶层类型已纠正，未当产品缺陷。
- Restore未知phase、负/越界QuizIdx、缺当前题四红→绿；拒绝不覆盖原状态。旧验收只测空phase，确有覆盖缺口。退休且待结算房间Tick不得写报告、通知或结算。
- 坏WAV七类红→绿：仅RIFF头、坏块长度、截断PCM、错byteRate/codec、缺fmt/data。失败日志区分取消与非法媒体，不能因defer cancel顺序把格式错记成取消。
- `/speech/synthesize`课堂归属跨用户拒绝且上游0调用；合法调用传递lesson trace、不记录原文。反馈真实降级才记provider_fallback，成功/取消均不误记降级。
- 真实模型暴露非法频率错分A/B导致写，扩至A/B/C/D/E与正向合法混合；E难词也尊重NeedsConfirm。跨分句备选日期、中文月日省略、逗号/分号备选、明确日期被模型换成近错日期均有回归。合法同日不同小时不误挡；下个自然周算法及超7天确认区分。
- 结构适配器缺字段/null/unknown kind/confidence越界原先接受，五类红→绿；生产协议校验与通用提示同步docs20，保留原始质量分数。

## 开放能力与任务事务审查

T1/T2工具注册、scope/主体绑定、只读查询、冻结老师效果经工具交付定向通过。T3遵守Hub→Room→agentWrite锁序，SQLite任务版本认领、过期计划重生成、新旧课次+通知+成功审计同事务；失败保留失败审计。冷恢复/双实例仅一次重排定向绿。

docs19额度由已提交回执计算：双实例争最后第3次只一项应用；故障回滚不耗额度、恢复retry完成；重复submission不再耗额。父澄清跨学生拒绝、不认领对方父项、不留ghost子项。FIFO覆盖实际Settle消费后保留序号、重新激活新序号、淘汰后事务rollback恢复、SQLite重启字段一致。

最后定向证据：`server`相关包运行日志在`.test-artifacts/`：`runtime-overdue-third.txt`、`docs19-directed.txt`、`final-boundaries-directed.txt`、`quota-rollback-green.txt`、`calendar-final-green.txt`、`model-safety-log-directed.txt`。所有后端生产修改归主代理/架构代理实现，本验收代理独立补测试与审查。

## 真实质量与边界

完整采集轨迹与命令见docs15及review JSON，不混合不同次推理。原64恢复后模型93/116；通用提示改善后98/116；独立28留出46/56，均低于90%。原响应与产品规划成对，无真实业务写。HO027禁止规划经原始响应回放锁定守卫，不能把回放当新模型准确率。

英文独立Zira16请求全成功，短词11/12、关键3/3；中文独立Piper12请求全成功但精确9/12，CER10.87%，关键案例保守整句3/4。两者只代表异源合成语料，不代表真人口音。中文TTS12中10合法非静音，1超时、1上游429；human_listening始终pending。离线评分器6回归涵盖绑定SHA、case唯一性、错误分母、归一边界、静音、截断。

## 最终全量证据

首轮完整执行证据保留于 `server/.test-artifacts/final-runtime-coverage-*`：429个顶层测试（416通过、7失败、6 opt-in跳过），含子用例748条（725通过、17失败、6跳过），vet通过。19个有测试包中17通过、2失败；覆盖率全达门槛但测试失败，不能宣称验收通过。源码前后SHA一致 `d46787601fd72eec7d96f9c60da5bb640653816fff981b37bc21155de3cb261c`。

失败逐项复核：Join恢复多业务帧使旧心跳“下一帧必须ACK”假设失效；月日原文与fake模型明天目标不一致；逾期重排从检测日算次日的新合同；课堂trace改为lesson-ID；Restore严格校验要求reteach/review fixture含实际题目。这些只修fixture并保留原业务断言。WS洪峰Windows实际收到10054远端reset，测试仅接受明确reset/CloseError或未饱和时ACK，超时仍失败，随后必须新ticket重连同课继续；容量1单测独立确定性证明饱和关闭。

另发现真实产品问题：逾期退休的Close接口签名与真实wsClient不匹配，新增红例后架构修复 `Close() error`；定向证据 `.test-artifacts/ws-overdue-green.txt` 通过。专用opt-in浏览器harness仅测试二进制内暴露stdin控制，真实Router/SQLite/WS，无产品HTTP后门，默认明确skip，由Playwright单列执行。

浏览器发现ISO日期被规则默认明天：新增合法次/后日、跨年份精确日期、非法日历、残缺/斜杠、备选、模型错日红例后，生产支持严格ISO并约束所有写类；补原文保留、明确过去不滚年、同日不同小时允许的正反回归。反馈全族通过（2.389s），最终ISO定向通过（1.506s），证据 `.test-artifacts/iso-date-red.txt`、`iso-feedback-green.txt`、`iso-date-final-green.txt`。

第二轮 `final-runtime-coverage-r2-summary.json`：19个测试包全绿、vet通过，434顶层测试=427通过+7 opt-in跳过，含子用例765=758通过+7跳过；全部覆盖率达门槛。因运行中浏览器又发现过期课enter包装400引起重连不终止，补红测试导致源码变更，`source_unchanged=false`，此轮保留但不能当最终冻结树证据。

过期enter现返回专用terminal sentinel映射403；Join退休房间关闭新client，不重启交付、不发快照、不增turn；Enter捕获退休room后二次锁检查拒绝，存储异常保留原错误不伪装永久403。红→绿证据 `expired-enter-red.txt`、`retired-join-red.txt`、`expired-enter-guards-green.txt`（完整Overdue族22.739s）。等待浏览器实际媒体协议问题闭环后再跑最终Go/race；不得把此前覆盖率冒充当前树通过。

## 冻结树 Go 验收（r3）

2026-10-14，`final-runtime-coverage-r3-summary.json`：19个有测试包全通过，cmd/server无测试单列；438顶层用例=431通过+7跳过，含子用例771条=764通过+7跳过，0失败；vet通过，110.86秒。源码SHA `e2d74dd388ccfe9d64bfab81a31b8ca00e4e68e32d43e291e3705a22d2941b01`，执行前后完全一致。

覆盖率使用显式 `-coverpkg=./internal/...`，相同源代码块跨测试二进制取命中并集，不将每个测试包显示的跨包百分比误当自身覆盖率。

| 模块 | 命中/语句 | 覆盖率 |
|---|---:|---:|
| agent | 151/155 | 97.42% |
| api | 3152/3916 | 80.49% |
| auth | 143/159 | 89.94% |
| classroom | 462/506 | 91.30% |
| config | 233/266 | 87.59% |
| feedback | 341/361 | 94.46% |
| judge | 127/140 | 90.71% |
| llmzhipu | 41/41 | 100.00% |
| localmodel | 152/168 | 90.48% |
| memory | 135/145 | 93.10% |
| obs | 50/54 | 92.59% |
| scheduler | 571/633 | 90.21% |
| scripts | 108/115 | 93.91% |
| store | 207/249 | 83.13% |
| tools | 150/160 | 93.75% |
| voiceclone | 53/56 | 94.64% |
| voicecmd | 85/93 | 91.40% |
| wordbook | 244/270 | 90.37% |

全部核心≥80%，课堂/调度/判分/反馈≥90%。7跳过均有显式opt-in原因：生产FB001诊断、浏览器逾期harness、独立ASR产品轨、三项历史智谱真实网络测试、独立语音评测。历史智谱云测试本轮未获云调用授权；其余真实本地采集及浏览器harness分别在专门报告验收，不能算此轮普通Go运行通过。

同一冻结树完整 `-race -p=1` 已通过：`final-runtime-race-r3-summary.json`，19包、438顶层（431通过/7跳过）、含子用例771（764通过/7跳过），无数据竞态或失败，源码SHA与coverage完全相同且执行前后不变。便携官方llvm-mingw clang23.1.3、CGO_ENABLED=1、GOMAXPROCS=2，仅进程级环境，无全局安装；耗时1841.97秒，包含首次全依赖race编译。测试期间暂停真实模型推理，与单worker确定性浏览器套件并行。

## 文档与异常日志复核增量

反馈提交/查询/重试/通知API、朗读五字段原子设置、媒体lesson_id权限与trace对应源代码；SDD配置表旧间隔边界及缺少中文次数、工具盘点“当前13”误作最终分母均已反馈主代理更新。React18/AntD5历史ADR保留，当前用户批准React19/AntD6已明确覆盖。

复核发现异常恢复日志仍有无lesson trace路径，不能因正常链路日志通过而豁免：Hub非法快照/成功恢复使用随机trace，DB读取异常漏事件，legacy运行时key保存也不能关联课堂。新增红例后补canonical snapshot/report/settled/agent_result正整数key推导；原显式trace保留，非规范key不冒充lesson。中央readRuntimeError只记录一次有界read_failed，Hub非法状态只记invalid_snapshot，不输出损坏快照正文。

`runtime-trace-red.txt`保留失败；`runtime-trace-green.txt`3顶层加3恢复子用例通过（10.542s），覆盖nil上下文、4类key、最大int64、非法后缀、背景保存、显式trace及原文脱敏。因此r3仅代表该增量前冻结树；随后重新执行的r4全量及race结果见下。

## 当前冻结树 Go 验收（r4）

`final-runtime-coverage-r4-summary.json`：19包全通过，441顶层=434通过+7跳过，含子用例777=770通过+7跳过，0失败；vet通过，145.47秒。源码 `d191ddecfa105cc98757bec523f5de1c4e49a22c422b73a2e60343e4a2a3adaf` 前后不变。相较r3，仅API覆盖变为3159/3924=80.50%；其余各模块覆盖率与上述表相同，全部门槛通过。跳过7项及理由同r3，不计作通过。

r4完整race已通过：`final-runtime-race-r4-summary.json`，19包全通过，441顶层=434通过+7跳过，777含子=770通过+7跳过，0失败且无数据竞态，493.27秒。源码SHA与r4 coverage完全相同，执行前后不变。此r4为当前Go最终验收证据；本地真实模型准确率、语音可懂度和共享平台部署仍按各自报告判断，不由Go绿替代。

## 新32条候选留出入口后的 Go 验收（r5）

local-eval仅增加32条冻结数量白名单和离线验证回归，生产服务逻辑未变化。新增验证锁定真实32正例、篡改SHA、31条、单轮、草稿、重复ID及非法时间锚。r5覆盖/vet与race分别460.44秒、667.16秒通过：19包，442顶层=435通过+7显式跳过；783含子=776通过+7跳过，0失败。两轮源码SHA `b1499202544e705b9a771434596a284cb5c3f9bde1a87681361b64f7fa2149e9` 完全相同且执行前后未变；证据 `server/.test-artifacts/final-runtime-{coverage,race}-r5-summary.json`。

跨测试包语句合并后的API为3163/3924=80.61%，其余17项模块指标与r4一致，核心80%和课堂/调度/判分/反馈90%门全部通过。对比r4/r5 profile后，变化位于并发逾期去重/状态重查、取消的roomcontext、WS握手/关闭分支；没有生产源码变化或分母变化。应解释为并发/断连测试调度覆盖的分支差异，不能凭百分比误称模型随机错误，也未为追求相同数字重复测试。七个opt-in跳过仍分账，真实模型报告另行判断。r5为此入口变化后的当前Go验收，不覆盖随后Python候选生命周期工具的独立测试。

## 数量授权与判分观测修复后的最终 Go 验收（r6）

docs/26 的数量源约束和 docs/27 的判分来源日志经架构独立实际代码复核通过后合并冻结。新Go源码SHA（含测试）`6acf6807eba1723edf2ade2530d3870e3c29152c9c2be9468efe2e3b43a53fb2`；完整覆盖率/vet与race前后均不变。证据为 `server/.test-artifacts/final-runtime-{coverage,race}-r6-summary.json` 及对应jsonl/profile/vet文件。

覆盖率与vet共123.44秒，完整race245.75秒，均退出0。19个测试包全部通过，cmd/server没有测试文件；两轮均452个顶层测试=445通过+7显式opt-in跳过，含子用例903=896通过+7跳过，0失败、无数据竞态。7项跳过原因与r5相同，未计为通过。仅使用进程级便携clang/CGO配置、`-p=1`、`GOMAXPROCS=2`，与单worker确定性浏览器并行，未并行真实模型推理。

跨包profile按相同源码块合并命中后的覆盖率：agent97.42%、api80.67%（3167/3926）、auth89.94%、classroom91.30%、config87.59%、feedback94.77%（417/440）、judge93.33%（140/150）、llmzhipu100%、localmodel90.48%、memory93.10%、obs92.59%、scheduler90.21%、scripts93.91%、store83.13%、tools93.75%、voiceclone94.64%、voicecmd91.40%、wordbook90.37%。核心80%和课堂/调度/判分/反馈90%门全部达到；API/反馈/判分新增源码改变分母，旧结果不替代当前树。

浏览器使用独立新产物，旧红例二进制保留：`feedback-acceptance-quantity-server.exe` SHA256 `079462FAAAD05A61B92AC2D466A3B138039A7FF88CD2F1C584FB513E25E289D1`，`browser-overdue-quantity-harness.exe` SHA256 `4A2C81DA9791EFCCFA18FA5285F8AD6FC91CBA66F42B9CEC2730E2454D496857`（均在`.test-artifacts/`）。浏览器、真实模型质量和人工音频结论仍分别验收，Go通过不代表模型语义门通过。

候选工具生命周期最后加固后，统一执行`python -m unittest discover -s review -p test_*.py -v`，19/19通过（11.366秒）：候选代理5、生命周期4、ASR对照4、语音评分6。日志`.test-artifacts/review-python-final-r6.txt`。仅隔离loopback假上游与短寿命启动器，不调用共享模型、不下载资源。生命周期加固测试是当前行为验证；同型旧capture也能通过，因此不能声称已证实真实pending的唯一根因，真实恢复仍以独立后验身份/健康证据判断。

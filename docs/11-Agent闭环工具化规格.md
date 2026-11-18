# 11 Agent 闭环工具化规格与 SDD

日期：2026-10-12，更新：2026-10-13。状态：**A 阶段经独立架构评审批准，代码与回归验收中；B/C 尚未接通，不是整体完成声明**。上游：02 §3–4、04 决策表、09 G05/G06。本增量不改现有 AGENTS.md，不宣称 22 个工具全部可用。

## 1. 现状与本轮边界

`api/app.go` 注册 `memory.append/config.set`，`agenttools.go` 增加 `config.set_student`。但 memory 参数可自行携带学生 ID，反馈改期/难词/记忆直接调用服务；SQLite 结课直接修改配置并手写成功审计，绕过 Registry。`commitLesson` 只消费 `adjust_words_down`，丢弃 `plan_next/memory_append/adjust_new_ratio/reply_comfort/report_push` 的动作语义。Hub 自选话术槽，未使用 Teacher.Decide，后者还硬编码鼓励阈值 8。

本轮目标：已有反馈、结课和教师气泡形成受信任务→Agent 决策→工具执行→结果/审计的可测闭环。暂不增加外部自主 Agent 写入入口，不承诺观察员、语法、声音克隆、全量 memory CRUD、全部课堂指令工具、cron 决策与跨课分析已完成。G05/G06 只能按子项关闭。

## 2. 受信任务与工具注册

由应用层创建不可由 JSON 构造的 `AgentTask`，包含 studentID、bookID、lessonID、触发类型、taskID、traceID、预算和绑定服务。外部 REST/WS 只提供业务输入；身份来自认证及课程归属验证。反馈任务允许尚无 lessonID，但学生必须可信；结课/话术必须校验 lesson→student/book。scope 校验之后，handler 再检查任务、资源归属、enum、范围及未知字段。

每个任务共用最多 8 次调用预算，不能每个工具重新初始化预算。预检按动作展开后的实际工具调用数计费（包括 get/recall/reply），不是只数动作；预算数据不得被并发调用产生竞态。取消信号向下传递。一次反馈/结课重试使用稳定 taskID，trace 关联本次请求及任务；不能只用 `student-N` 混合所有请求。

任务还绑定允许的 caller 角色和触发类型；拿到学生任务不能改用 system/admin 身份，也不能把教师话术任务用于改期或调参。创建器及上下文 key 不导出，任务由当前 handler/调度入口建立；有 lessonID 时从持久化课程核对 student/book，有 bookID 时核对选库关系。原 `memory.append` 等旧名字也必须进入此约束，不能只保护新增工具而保留同功能旁路。

同一注册函数接受 `AgentServices`，构造配置、调度、记忆、结果收集等 handler。默认调用绑定应用服务；事务调用绑定同一 `tx` 的服务，**注册定义、权限、校验和审计管线完全相同**。禁止 handler 隐式退回全局 App 服务，禁止全局 registry 临时换 handler。

| 工具 | 角色/风险 | 输入和结果约束 |
|---|---|---|
| config.set_student | system/high | key/value/reason；白名单词量、频率；返回实际生效值 |
| config.get | system/low | 当前任务学生的指定配置；不接受任意学生 |
| memory.append/recall | teacher/system，mid/low | owner 派生自角色；student/book/source_lesson 派生自任务，兼容 ID 输入须相等；返回真实记录/检索结果 |
| scheduler.plan_next_lesson | system/mid | 当前任务学生/词库；返回 planned/already_planned/no_words，不能把所有错误当 no_words |
| scheduler.postpone_lesson | system/mid | 当前学生课程、toDate、非空 reason；遵守未来七日及既有冲突规则，返回实际课程与日期 |
| scheduler.boost_word | system/mid | 当前学生已选词库内 word、reason；找不到/保存失败显式返回错误 |
| classroom.say | teacher/low | 受信课堂、已渲染模板文本/类别；最长 60 字，拒绝任意学生/课堂；返回待发送帧，不推进状态机 |
| feedback.reply / report.publish | system/mid | 消费经过验证的执行结果，生成回复/报告/通知；不得接受模型编造的成功状态 |

`config.set` 的全局管理通道保留 admin；system 若继续允许全局配置，必须另有可信全局维护任务，不能使用学生任务扩大权限。外部工具端点缺任务凭证时，新工具应明确拒绝。

## 3. 结课事务与动作执行器

决策在事务外生成，任何模型网络请求不进入 SQLite 事务。先检查所有 action kind/参数/预算，未知动作返回 `unsupported_action`，记录索引与类型，不允许无 default 的 switch 静默跳过。

事务内顺序为：读取 `settled/{lesson}` 幂等记录→结算本课事实→绑定 tx 服务与 Registry→配置/记忆等调整→排下一课→报告/通知/快照→工具审计→settled 标记→commit。Agent 输出的 `plan_next` 表示排课意图，执行器按依赖顺序延后执行，保证下课先调参再排课。重放已完成任务不重复调用工具、不增加记忆/通知。

`SettleLesson` 写入实际答题事实是平台事务入口；Agent 改变配置、排课、记忆、发送总结须经工具。工具不得另开数据库事务，不得调用会重取 App.mu/Room.mu 的公开入口。`classroom.say` 只收集帧，调用方在持有现有 Room 锁的安全位置发出，不能从 handler 再锁 Room。

现 `Auditor.ToolCalled` 无错误返回，不能保证审计落库失败时回滚。建议事务 registry 使用任务私有缓冲审计器（含拒绝、失败、预算耗尽），事务结束前统一 `tx.Create`；写审计失败使整批回滚。成功调用审计只在 commit 后对外标记 committed；回滚过程的尝试日志标记 rolled_back，并在事务外记录一次失败摘要，禁止伪装成已提交成功。非事务写工具也须具备审计失败错误传播或使用同等工作单元，不能无声丢失审计。

内存模式不具备 SQLite 回滚能力；保留冻结绝对目标和动作完成凭证避免重试重复累加，失败必须暴露，不宣称重启幂等。报告及“下课成功”只在结算提交后发布；配置内存刷新失败可重试刷新，不重复业务副作用。

### 3.1 动作完备性

`adjust_words[_down]/adjust_frequency`→配置工具；`memory_append`→记忆工具；`plan_next`→排课工具；`postpone/boost`→调度工具；`reply_*`→反馈结果工具；`report_push`→报告工具。`escalate` 必须有真实管理员通知/待办记录，只有状态文字不算升级完成。

`adjust_new_ratio` 当前没有配置/调度支持：本轮若实现，新增 `PlanNextWithPolicy` 的一次性 `MaxNewRatio=0.4`（默认无覆盖保持原行为），由执行器传入排课工具并保存到任务结果。复习优先、新词计数包含 boosted 新词，按 `floor(W×ratio)` 限制；不足时允许小批次，不得补新词破限。没有复习词时保持首课全新词例外并记录原因。不要将一次课后策略悄悄变成永久学生配置。若未实现此项，预检显式返回失败并保持 G06 未关闭；不得删除 Agent 动作来制造完成假象。

反馈多意图按 04 S-12 独立提交，结果逐项保存：已应用项显示实际值，失败项标失败，依赖该项的记忆/成功回复不执行；其他独立意图可以继续。统一 `failed` 文案不能抹掉已生效事实；不得全局 `failed` 阻止后续无关记忆。需要意图/动作分组与依赖结果，重试不能重复相对减量。

反馈幂等契约须在 C 阶段实施前同步到 REST/WS 客户端：同一提交使用客户端生成的 `submission_id`（1–128 字节）；服务端以 `(认证学生, submission_id)` 唯一绑定冻结解析结果、输入摘要及各意图结果，重复相同请求返回已有结果，不重新相对调参；同 ID 不同文本返回冲突。没有 ID 的旧请求可生成新任务，但不能承诺客户端重试幂等。模型解析在写事务前完成，竞争提交须在事务内重查唯一凭证；先创建且冻结任务，再逐意图提交副作用与完成凭证。不得持有数据库事务等待模型。结课 taskID 使用 lessonID 和任务版本，凭证仍以已结算课程防重复；取消或重启后复用已保存的任务计划。

## 4. Teacher 与快路径

状态机负责 D-21 相位/合法迁移和 D-20 两次机会，不调用 LLM。Hub 把动作转为 TeacherInput；Teacher.Decide 负责选择话术事件/槽，Render 使用现有模板，输出经 classroom.say。鼓励阈值从课堂冻结配置传入 TeacherInput，删除 Teacher 的常量 8；Hub 只维护事实计数，不再选择教学话术。

开始/恢复/终测/判对/判错/超时/结束分别有明确事件映射；同一事件至多发一次，重放不重复说话。结课 closing 在事务成功之后。无话术是合法决策结果；渲染失败有固定模板降级与日志。普通气泡不额外触发 TTS，朗读仍只有 `play_tts` 驱动，避免双读。全部课堂 show_word/play_tts/advance_phase 工具化及 T-01..20 的细项覆盖另列待办，本轮接入气泡不代表 D-15 全部达成。

## 5. 测试与日志验收清单

每项修复同时具备 Go 回归和 Playwright 用户旅程断言；仅调用接口不替代浏览器交互。测试由子 agent 执行。

| 编号 | Go 单元/集成断言 | Playwright 无头旅程 |
|---|---|---|
| AT01 | 无任务、伪造学生/课程、错误角色、越界参数、空 reason 均拒绝且审计 | 两学生独立 context 登录，A 的反馈/课堂不改变 B 的设置/计划 |
| AT02 | SQLite 调参/记忆/排课/审计任一写故障整批回滚；重启重试各副作用仅一次 | 完成低正确率课堂→课报/下课→下一课；故障后不得假成功，恢复后重试 |
| AT03 | 每种 SystemAction 有执行器映射；未知 kind 拒绝，依赖次序先调整后排课 | 难词、改期、词量、频率、投诉及多意图结果实际显示并能重新登录读取 |
| AT04 | 慢答 0.4 策略含 boosted 新词、不足批次、无复习例外；不得永久污染下课 | 慢答课程完成后下一课批次与报告建议一致 |
| AT05 | Teacher 冻结鼓励频率、开始/恢复/判错/结束事件，重复不双发；say 不改相位 | 真实页面完整教学→跟读→错答重教→终测→报告，检查老师气泡和 mock 播放次数 |
| AT06 | 预算跨工具共用、审计失败传播、取消、失败重试无重复记忆；无原始敏感文本 | 反馈失败显示真实失败，不显示“已应用”；多意图展示部分生效事实 |

关键日志：`agent_decision`（trigger、action kinds/count），`agent_action_completed/failed`（task、index、kind、tool、result_code），`tool_called`（role、scope、duration、transaction_outcome），`teacher_say`（category、text_length），`lesson_settlement_failed/lesson_settled`。共用 trace_id/task_id/student_id/lesson_id；仅记录参数摘要，不记学生原文、完整提示词、密钥。审批拒绝与预算耗尽同样可定位。E2E 保存 trace/console/network failure 并关联后端 trace。

## 6. 评审与完成条件

先审查受信身份、事务绑定、审计失败及动作完备性，再实施。通过不等于原 22 工具全部完成；最终台账逐项列出本轮工具、仍未注册工具、未接决策表条目。新增实现仍须满足核心 ≥80%、状态机/调度/判分/反馈 ≥90%，全量 Go/E2E 通过、文档 API/配置/协议一致及专家复审无未解决问题；缺证据保持未验收。

待同步的旧文档冲突：04 S-03 的“不低于40”已被 D-21/09 的 50–100 覆盖；S-03“10%”与当前代码“-10词”不一致，沿用本轮 09/既有代码的减10词并在实施时同步04，不混称比例调整。02 的工具表有重复 classroom.say，工具总数必须按唯一工具名核算。

## 7. 2026-10-13 设计复核记录与实施分段

复核者：文档起草代理 `agent_gap_design`。本次是结合当前实现的设计复核，**不冒称独立作者外审通过**；主代理或另一领域评审者仍须确认实施范围。未运行代码测试，不以本节替代验收证据。

| 发现 | 级别 | 设计处理 / 实施门 |
|---|---|---|
| 受信 student 不等于受信角色/触发能力，旧 memory 工具有旁路 | 阻断 | §2 增加角色、触发、选库与课程校验；新旧工具同一管线，测试旧名称不能跨学生 |
| 动作数与实际工具调用数不同，八次预算可能执行一半才超限 | 阻断 | §2 改为编译计划计费；预检超预算不写入，运行时 guard 保留；不能通过拆任务绕预算 |
| 反馈 taskID 缺重试协议和持久化身份来源 | 阻断 | §3.1 新增 submission_id 契约及每意图事务凭证；C 阶段必须连客户端一起实施 |
| 不支持 slow-answer 动作时 fail-closed 会使正常课堂无法结算 | 阻断 | B 阶段上线前一次性排课策略及所有当前结课动作必须完整支持；不可先启用部分执行器再让用户课堂永久失败 |
| tx 缓冲审计在提前 return 时不会 flush | 阻断 | 事务失败后在事务外追加尝试失败摘要（task/index/tool/error类）；成功副作用审计随事务回滚；若故障仍阻止持久化，结构化错误日志必须显示 audit_persist_failed，不能声称审计已落库 |
| 全局 App 配置缓存可能被 tx 临时值污染 | 阻断 | tx 内新建服务实例，只持有 tx 状态；commit 后刷新应用缓存，失败可重复刷新；回滚不得更新全局缓存 |
| 新词策略“无复习词例外”过宽 | 需澄清 | 仅真正首课允许全新词例外；已有学习记录但当前无到期复习词不得据此绕过 0.4 上限；复习候选/数量不足允许较小批次，并记录原因 |
| 新工具模板文本来自模型还是受信渲染不明 | 需澄清 | classroom.say 只消费 Teacher 渲染产物；若未来对外接模型，应接模板ID+变量白名单并由服务端渲染，不能只用长度检查当模板合规 |

建议按以下可审查范围实施，每段仍须 Go+E2E 和代码复审，不等于 G05/G06 全关闭：

**A：工具基础与教师气泡。** 可先批准任务身份、线程安全预算、绑定服务的注册工厂、审计缓冲/错误传播、原入口权限收紧；TeacherInput 增加冻结鼓励配置，Hub 使用 Decide/Render→say 收集帧。A 不更改结课动作执行方式，不宣称解决当前漏执行动作。需要现有 admin 工具与 internal Agent 入口兼容回归、跨学生负向测试、教师气泡完整课堂 E2E。

**B：结课完整执行。** 在 A 基础上，一次性支持当前 `lesson_finished` 产生的全部动作，包括记忆、降量、慢答策略、排课、安慰与报告发布；已落库/无词等业务结果都有明确返回。先编译依赖和预算，随后同一 SQLite 工作单元执行。任一工具/审计写故障全量回滚；只有提交后发 closing/summary。要求事务注入失败、进程恢复、重复结课、实际慢答下一课组成用例。缺其中一项不可开启替代路径。

**C：反馈逐意图闭环。** 先完成 submission_id、冻结计划、每意图的依赖组和完成凭证，再替换直接 postpone/boost/memory 调用。每意图工具与审计同事务；前端展示完整逐项结果，投诉升级必须生成实际管理员记录。旧接口兼容期必须明确旧请求的幂等限制。上线前必须覆盖同时提交、重试、同ID异文、部分失败、重新登录后历史一致。

剩余全课堂能力工具化、全 22 工具目录、cron 和跨课建议继续留在台账，不以 A/B/C 完成替代 D-15 整体验收。

## 8. B 前置：一次性排课策略接口（主代理已批准设计，代码待复审）

新增 `PlanPolicy{MaxNewRatio float64}` 与 `PlanNextWithPolicy(studentID, bookID, policy) (*Lesson, error)`。`PlanNext` 等价于传零值 policy：零代表未设置，保留已有批次行为；显式比例必须为有限数 `(0,1]`，NaN/Inf/负数/大于1返回 `ErrInvalidPlanPolicy`，在任何写入前失败。此接口不提供“禁止全部新词”的 0 比例语义，未来需要时另增显式字段，不复用零值。

非零策略使用确定性选择：先选 due（learning 且 NextDueAt≤now+24h，到期时间升序、同时间 WordID 升序），至多 W；再选不重复的 boosted（WordID 升序）；最后普通 fresh（WordID 升序）。新词总数（包括 boosted new）不得超过 `floor(W×MaxNewRatio)`，learning 的 boost 不计新词。到期词多于 W 时不会被 boosted new 挤掉。没有到期复习而已有学习历史时仍保留新词上限；不足 W 允许小批次，不把未来非 boosted 复习强行提前，也不补新词破限。选中0词返回 `ErrNoWords`。零值路径不改既有 boost 优先顺序。

**真正首课**定义为该学生该词库没有任何课程记录，且卡片没有 learning/graduated、LastReviewAt 或正 LadderStep 的学习证据。仅摸底产生的 mastered 不算教学历史。只有真正首课可放宽至 W 个新词；有 expired/voided 课程或导入学习记录均不能复用首课豁免。ListLessons 读取失败必须返回错误，不能当空历史放宽策略。

策略仅为本次调用参数，不改配置中心、不写 Card 额外状态。Lesson 仍保存实际 BatchWordIDs，重新打开持久化课程得到同一批次；策略/例外原因的任务审计由 B 执行器保存，此前置接口不伪造日志已接线。新的同学生调用不带 policy 时恢复原默认行为。外部 SQLite 事务仍由调用方负责，接口本身不另开事务；已有 CreateLesson→ReserveCards 的非事务失败语义在此不扩大保证。

独立 Go 测试：零值与旧 PlanNext 等价；无效比例无写入；真正首课/摸底 mastered 豁免；有历史但无到期复习不豁免；boosted 新词计入上限；due 优先与同到期稳定排序；小批次与极小比例0容量；下一次零值无策略残留；跨学生/词库隔离；历史读取失败显式失败。B 接线后补浏览器慢答→结课→下一课旅程，当前仅提供可测试 API，不宣称 E2E 或 B 已完成。

2026-10-13 实施记录：新增 `scheduler/plan_policy.go` 与 `plan_policy_test.go`，`PlanNext` 转调零值策略；ListLessons 错误传播至两条路径，非法 W（配置契约 50–100）提前返回错误防止切片容量 panic。10 个新增顶层回归测试覆盖上述策略边界。子代理执行 `go test ./internal/scheduler -count=1 -coverprofile=...` 通过（0.723s）；策略 validate/firstTeachingBatch/batch 和新公开入口语句覆盖率均 100%。scheduler 包整体仅 **81.1%**，仍未达到 90% 门槛，不能宣称调度模块整体验收通过。证据文件 `.test-tools/scheduler-policy-coverage.out`。未修改 api/settlement，未执行此接口的浏览器 E2E。

## 9. A 阶段代码独立复审（2026-10-13，处理中）

评审者 `agent_gap_design` 未参与 A 业务实现；审查 `agenttools.go/agenttransaction.go/feedbackflow.go/app.go/teachertools.go` 与工具预算实现。本节只记录静态证据，不替代测试结果。

| 编号 | 结论 | 证据 / 要求 |
|---|---|---|
| A-R01 | 修复静态复核通过，测试待验 | newAgentTaskContext 已对所有 bid>0 校验 StudentBooks，并传播错误；lesson 任务同时校验课程归属 |
| A-R02 | 修复静态复核通过，测试待验 | 已返回 result + committedToolError；反馈识别已提交状态并提示保存成功/刷新待恢复，不再报告未保存；故障注入测试仍须通过 |
| A-R03 | 验收缺口，待处理 | classroom.say 走全局 MemAuditor，纯工具审计未持久化；成功日志缺统一 task_id/tool_called；若 A 要求完整审计，应提供纯工具审计出口后再发布帧，否则维持未验收 |
| A-R04 | 静态通过 | 预算用 mutex 计数，嵌套 WithEventBudget 不刷新预算；context cancellation 在 registry 检查 |
| A-R05 | 静态通过 | 注册工厂绑定 tx 的 config/memory，持久写工具拒绝非事务服务；成功审计同 tx，失败摘要在回滚后尝试落库，持续故障记录 audit_persist_failed |
| A-R06 | 静态通过 | 旧 memory.append 入口移至受信任务约束；config.set 收紧为 admin；caller 角色与 task 角色及 App 实例相等校验 |

不把 C 的已知缺口误报为 A 已解决：反馈稳定任务凭证、逐意图依赖/预检、直接改期/boost、未知动作处理仍未落地；也不将仅仅共用预算当作多意图事务已经完整。Teacher 的完整事件预算范围与纯工具审计须在实际课堂回归中核对。

## 10. B 完整结课执行器实施规格（设计已批准，实现验证中）

### 10.1 编译完整计划

新增 `compileLessonPlan(actions)` 返回强类型 `lessonAgentPlan`：配置变化、记忆数组、一次性 PlanPolicy、回复类别、发布要求及实际工具调用预算。只接受当前结课产生的 `plan_next/adjust_words_down/memory_append/adjust_new_ratio/reply_comfort/report_push` 六类；逐项校验参数类型与有限值，恰好一个 plan_next 和 report_push，重复配置/ratio/report 操作拒绝。未知 action 或坏参数在首次副作用前返回明确错误。只排序执行，不删动作：每个原 action 保留 index 与处理结果（纯策略动作标记 compiled_into_plan，不能伪报已调用工具）。最多六次实际工具调用，新增决策导致超八次时预检失败。

输入事实来自 `res/st` 的冻结副本及已核对的 lesson，禁止后续修改调用者的 map/切片；判题集合必须属于该课批次，sid/bid 与 lesson 完全相符。System.Decide 在数据库事务外执行，无网络进入事务。受信任务 trigger=lesson_finished，taskID 稳定为 `lesson/{id}/settlement-v1`，请求 trace 可变，日志同时包含稳定任务ID。事务重试重用相同事实和语义计划；SQLite 回滚没有已提交副作用，绝对配置目标在事务内基于当时配置计算；内存部分成功模式则首次计算后冻结绝对目标。

### 10.2 同一工厂绑定工具与工作单元

扩充 `agentServices` 注入 scheduler、结课专用结果收集器/发布器、Clock/TestMode；缺服务报错，不隐式回落到 App。注册以下 B 工具，均要求 system + lesson_finished 任务且服务绑定同一资源：

| 工具 | 行为 | 结果 |
|---|---|---|
| config.get / config.set_student | 读取当前词量；低正确率按决策减10并夹至50–100 | 生效绝对值，配置目标冻结时不重复减 |
| memory.append | task_memory、任务学生/词库/source lesson；执行既有performance记忆 | 记忆ID；每个原记忆 action 有完成凭证 |
| scheduler.plan_next_lesson | 从已编译计划得到 policy，调用 PlanNextWithPolicy；校验任务资源 | planned/already_planned/no_words，真实lessonID、batch和policy结果 |
| report.compose_reply | 只接受 reply_comfort 类别，按冻结报告事实及真实配置结果渲染模板并收集 | 文本与原action索引；不得直接发网络帧 |
| report.publish | 使用受信 Result/State 和前述实际结果，写报告/快照与一条通知 | 报告ID/通知ID；重复发布无新增 |

`adjust_new_ratio` 是编译排课参数，不新增虚构的永久配置写工具。计划结果记录 requested_ratio、selected_new_count、batch_size、first_lesson_exemption、no_words/already_planned。遇已有 scheduled 课程，返回该实际课程，明确已有批次未按新策略重排；不得说新配置已改变既有课。真正新课的 testmode OpenAt/ReserveCards 修正在该工具所绑定 scheduler 上进行，不调用全局测试辅助方法。

发布器由平台创建，接收的是受信事实和先前工具结果，不接受模型自由传入的 report/通知收件人。低正确率安慰文本与report_push共同组成同一通知（避免两条“课报”通知），保存补充 `agent_result/{lesson}` 执行明细；现有课报端点结构保持兼容。通知 UI 已能显示 Body，E2E 必须验证安慰/真实调整说明出现，不能只检数据库记录。完整历史课报建议展示可随补充字段一起实现，但不可让该动作被保存后永远不可见。

### 10.3 SQLite 提交顺序与错误语义

锁顺序统一为 completionMu → agentWriteMu → App.mu（不倒置，也不在handler重取）。创建/核验受信任务及生成决策在拿 DB 事务前；事务中再次核对 lesson 归属及 `settled/{id}`。已提交者直接返回已保存结果/刷新缓存，不重新调用工具。

事务内新建 cfg、sched、memory(tx)、缓冲auditor和同定义Registry。先 SettleLesson 写学习事实，再通过 Registry 顺序调用 config.get/set → memory.append → scheduler.plan_next_lesson → report.compose_reply → report.publish；最后审计flush和settled标记一起提交。不要调用 `callAgentTool`：它会取得 agentWriteMu 并打开新的事务。共享同一ctx预算，最多八次；所有日志直到commit后才能标 committed，失败尝试标 rolled_back。

任何数据/审计写错回滚学习事实、记忆、配置、下一课、报告/通知/标记全部；回滚后发布失败审计摘要，DB不可写则输出audit_persist_failed。commit后才同步 App.notifications/计数、配置缓存和房间完成帧。Cfg.Reload错误采用A的已提交语义：不得把已完成课再次作为未提交重跑，可返回保存结果并附refresh_pending日志；下次刷新不能增加副作用。

当前 report/snapshot/notifications 使用 RuntimeRecord，B沿用结构而不悄悄另建第二事实源；事务发布器直接tx写入并返回待同步内存结果，不调用会获取 App.mu 的 Notify/saveReport。任务执行明细必须包含已处理动作清单，便于完整性断言。

### 10.4 内存重试凭证

用 `completionTasks[lessonID]` 保存冻结 Result/State、编译计划、目标词量、settled标识及按原action索引保存的结果。仍由completionMu串行执行；同lesson传入不同事实拒绝而不是覆盖旧任务。每一工具成功立即记完成结果后才继续；失败保留之前结果，重试只执行未完成项。memory.append成功后不能因后面的排课失败再次追加；配置重复写冻结绝对值，不能重复做减10。

主代理设计复审补充：每次内存重试重新分配最多8次调用预算，但只执行未完成工具，稳定taskID与冻结计划不变。SQLite已提交回放先读取完成标记并校验保存的student/book归属，不再要求当前仍选中词库；新标记有fingerprint时拒绝不同事实，旧版标记没有fingerprint则兼容原所有者事实校验。

学习事实复用 scheduler.SettleLesson 的绝对卡片目标及lesson.finished守卫。PlanNext可能在CreateLesson后ReserveCards失败；重试遇already_planned须读取实际scheduled课程并重做该批次 `ReserveCards(..., OpenAt)` 后才记排课动作完成，不能直接忽略错误。修复仅同步该课已有时间与批次，不能重排；若存在多节scheduled则显式冲突，避免挑错课。只承诺单进程重试，不承诺内存模式重启恢复或任意仓储“提交成功但返回错误”的exactly-once语义。

report.publish放最后，通知/报告/快照在App锁内写入并一次性标记发布凭证，再标任务完成；避免报告已存在就提前跳过此前未完成动作。内存任务完成后可以压缩为事实摘要与完成结果；与现有课程/报告同生命周期，不创建无界额外日志队列。

### 10.5 B 必须新增的验证

Go：六类action全部覆盖与未知/坏参数/重复/超预算写前失败；高/中/低正确率×快/慢答矩阵；50边界；事务每一写点及审计故障回滚；memory成功后plan失败的内存重试不重记忆/减量；创建课成功但预约失败后修复；新App重放不重复课报/通知；任务归属与已有课/no_words真实返回；提交后缓存刷新失败不重跑。

E2E：低正确率课→安慰和真实配置调整→下一课；慢答课→下一批新词上限；模拟提交/审计故障时无假成功，恢复后单次课报；两个学生完整课堂各自产生一次报告与独立下一课。模型纯文本/语音质量验收仍是独立轨，不以mock课堂替代真实能力。

所有六类动作、SQLite与内存重试、工具审计和上述回归同时就绪才替换旧commitLesson路径。不得只接config/memory就开启执行器，更不得用跳过未知动作换取绿测。

### 10.6 实施及代码复审记录

已新增 `api/lessonplan.go`（强类型计划/冻结事实）、`lessonagenttools.go`（同定义工具/一次性排课/报告回复）、`lessonexecution.go`（SQLite原子工作单元与内存逐动作凭证）。`settlement.go` 转入完整执行器，六种现有action全部覆盖。默认工具目录也注册同定义；没有受信绑定publisher的调用明确拒绝，事务内不走开启新事务的callAgentTool。

主代理首轮独立代码复审四项已处理：内存报告/快照map懒初始化；默认工具目录补全；动作日志补task/student及稳定错误代码；新完成标记回放检查fingerprint。低正确率词量已在50下限时，持久记忆描述改为实际词量，不再虚称继续降量。安慰使用已有RB-7模板，避免随机选择依赖未提供跨课字段的模板产生空数据；与课报合并为同一通知。

新增Go回归覆盖：六决策分支全执行；未知/重复/坏参数/预算预检；default工具有定义但拒绝无任务；SQLite删除选库后重启回放与异事实拒绝；配置、记忆、下一课、快照、通知、执行明细、完成标记及审计逐写点故障回滚；连续内存失败重试不重记忆/减量；已创建下一课但预约失败后修复；no_words、错误归属/词条、NaN；提交后配置缓存刷新失败不重跑。前轮api/scheduler全包测试通过（7.208s/0.428s），包覆盖率79.2%/83.2%，**仍未达80%/90%门槛**。追加测试后的证据写 `.test-tools/agent-b-coverage.out`，由最终台账记录最近结果。浏览器E2E与新代码复审仍在进行，不标记B已验收。

最新追加刷新故障/工具资源测试后，api全包发现 `TestCov_RoomTimeouts`（coverage2_test.go:50）期待tts_seg实际show_word。测试代理已定位为并行新增的deliveryReady契约要求Join才启动计时，旧fixture缺Join；不是放宽时间阈值，已补显式Join，等待全量回归。当前api覆盖率79.4%、scheduler83.2%。B的13个新增顶层回归与旧durable结算回归定向运行通过（6.195s），全量验收仍以主台账最新结果为准。

### 7.1 A 阶段 Registry 基础契约（2026-10-13 复核批准）

预算对象由同一任务上下文共享并发锁保护，派生上下文及不同 Registry 共用额度；再次调用 `WithEventBudget` 不重置额度。未取消的调用尝试消耗一次，包括未知工具、权限和参数拒绝；非正额度拒绝全部调用。进入时已取消的请求不消耗预算。

Registry 在入口及 handler 前检查取消，原上下文传入 handler。取消及超时错误保留 `errors.Is` 语义；不强制中止忽略上下文的 handler，不把已经成功的副作用因随后取消改报失败。正常返回路径每次恰一条审计，本轮不承诺 panic 恢复。

审计参数仅记录字节数；结果使用 `ok/budget_denied/unknown_tool/denied/invalid_args/reason_required/canceled/deadline_exceeded/handler_failed`，禁止写入原始参数或错误字符串。返回调用者的错误保持原语义，应用边界另行处理公开错误内容。每条审计含 trace、调用身份及耗时。`MemAuditor.Snapshot` 提供加锁副本，可作为任务私有事务审计缓冲；是否提交由事务层负责，Registry 成功不等同数据库已提交。

`ToolsFor` 按工具名稳定排序并返回独立 Schema 副本；注册时复制 scope/Schema，防止外部切片修改已注册权限和定义。验证包括跨 Registry 并发预算、嵌套额度不可恢复、取消与 trace 传播、审计敏感内容隔离及定义副本不影响权限。

### 7.2 首次媒体交付竞态修正（2026-10-13）

浏览器进入课堂先 REST enter 后 WebSocket Join；两者之间服务端若自动开始朗读，首段业务帧没有接收者，随后超时跳到第二段，导致实际仅两遍英文。首启 E2E 的真实播放探针已重现此缺陷，禁止以快照触发补播或降低三遍英文断言掩盖。

Room 初始 `deliveryReady=false`。首次 Join 或通过校验的显式 REST/受信事件、语音输入激活交付，并重新计算当前相位起始时间。激活前 Tick 不推进教学超时，但已到结课相位的事务重试照常执行。激活后保持原有超时和 REST-only 语义，不将断线扩展为全课堂暂停。快照只恢复展示，音频仍仅由业务帧驱动。

回归：Go 覆盖 enter 后推进 30 秒仍停留 show_word、Join 后首段必须为 seg1，以及 REST-only 事件激活后的正常超时；Playwright 保留每词三遍英文加一遍中文的实际播放与完成计数，并校验老师开场/纠错/结课气泡。

## 11. C阶段当前实施状态（2026-10-13）

本地基线已提交`fcf5cc8`，后续反馈改动尚未提交。docs12 §8完成实施前架构/协议/测试复核后启动实现：稳定提交ID、冻结IntentIndex计划、单意图事务、独立待办/通知、页面及语音恢复。教学策略与回复仍需遵守04决策表，工具只能使用受信任务绑定资源。

代码复审和回归已发现并锁定：语音正文200字节截断/共享队列串单、模型路径漏等价去重、旧token未检查当前停用状态、任务版本与结果非一致读取、GORM复合主键index=0误插入、缓存刷新失败不得假报成功、处理查询网络失败后轮询停止。2026-10-14最终专项验收通过：Go382个顶层用例通过、18个核心包覆盖率达门，四套专项Playwright共26/26通过；独立评审与详细证据见10当前进度、12 §9。本结论限C阶段，不关闭全量工具调度、真实语音及其他待办。

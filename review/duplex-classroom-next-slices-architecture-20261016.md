# 统一提交下一实施片：架构与安全独立定位

2026-10-16，只读代码/设计复核，不含新产品实现或测试执行。依据docs36、37和`duplex-classroom-slice1-implementation-plan-20261015.md`；上一批G1/简单跟读验收不等于此接线已完成。37已获主审实施授权，与36分开验收。

## 1. 首片最小交付

先做**持久模式/授权身份与旧写入口封口**，不做半套教学：隔离SQLite显式创建v2表及一条受控pilot基线；可鉴权查询、连接接管/退休；pilot旧输入明确拒绝，legacy所有旅程不退化。没有新coordinator时不显示可答题/可播放的pilot课堂，不把ENWORDS_DUPLEX_PILOT预检开关当迁移授权。无需远端模型/音频即可验证。

| 建议文件 | 首片职责 | 依赖/禁止 |
|---|---|---|
| store/classroom_pilot_schema.go | 显式版本化v2七类表：states/transition receipts/answer claims/active commands/effect outbox/completion jobs及必要schema版本记录 | 不修改G1 duplex_receipts唯一键冒充升级；不把原3表当完整实现；与37固定22表默认迁移边界协调，首片pilot显式迁移独立 |
| store/classroom_pilot_identity.go | LoadMode、CreatePilotBaseline、ClaimConnection、Retire；固定连接writer先于读，owner/账号/课次状态再次检查；uint溢出拒绝 | 不用进程mutex证明跨App唯一；DB/schema错误不能默认为legacy；只“无pilot行”在已知正确schema下可证明legacy |
| api/pilot_mode.go | 单一分流/固定安全错误/授权查询，持久generation与epoch接管 | 新增学生写API和内部kind身份不能靠JSON role；未接实现的Transition明确unsupported |
| api/router.go、ws.go、ingress.go | Enter/Join前分流；events/voice/resume与WS FIFO执行时再次门；state查询ownLesson | 不能先Hub.Enter启动旧Machine再判mode；排队期间模式/代次变化不得旧路推进 |
| api/hub.go、roomcontext.go、overdue.go | 内部Handle*、Tick/FireTimeout/SessionTimeout、Join恢复/Drop退休封口；本地撤音可立即，持久退休需明确成功/未知 | 不持Room锁等待网络；旧便捷接口无身份不能获得pilot写权限 |
| api/persistence.go及app.go保存入口、lessonexecution.go | pilot兼容snapshot禁止旧独立覆盖；旧结算入口拒绝pilot直写 | 后续coordinator同tx镜像有受信接口，不能靠布尔绕过供外部调用 |
| web独立pilot占位/state页（后续owner确认） | 显示未接线/状态读取、明确安全错误，旧课堂正常 | 首片不能复用旧AnswerFlight冒称未知提交恢复；不接真实自动教学 |

首片验收是模式/身份/封口，不是“完整课堂exactly-once”。需真实REST/WS双学生旧/试点并行、两独立SQLite池接管、老App携新checkpoint旧generation仍拒绝、读取失败不落legacy、旧timer/snapshot/结算零副作用；组件故障测试与用户E2E分开。

## 2. 持久约束不能缩减

- 命令receipt唯一(lesson,command_id)，payload指纹冲突拒绝；终态answer_claim唯一(lesson,logical_input_revision)，不含epoch/utterance。多次纯控制不消费机会。
- state持久owner/mode/schema/checkpoint_version/logicalrev/question/auth_generation/epoch/retired/active指针；接管只增授权代次，不重置答题机会。
- active command同writer占位；稳定command ID不重复Judge，token/generation/deadline在最终写门重新验证。回收必须取得writer再查receipt/claim，不能凭lease过期或not_found推定rollback。
- outbox按decision+effect_index唯一；ACK引用当前segment/lease/auth。播放收到/开始/结束不同，cancel不是ended。
- completion job冻结state/result/System plan版本/指纹；claim token/lease，done+summary outbox原子唯一，旧worker不得覆盖。已有settled再查保留。

现`store/duplex_repository.go`只有基础3表，receipt按lesson+epoch+input_revision且callback可执行受信写；不能直接接所有Transition。`agenttransaction.go`的callAgentTool自行取agentWriteMu和事务；不得在新的提交事务内嵌套调用。

## 3. 后续文件级切片

1. **Detached候选**：新增api/pilot_candidate.go，fresh Machine+Restore已提交State，独立复制词条/config与Room教学事实；不要调用Join/RecoverDelivery初始化普通候选，不复制mutex/client/context。当前跟读已是EvaluateRepeat，不再调用意义Judge；quiz保现规则。
2. **冻结计划与tx执行**：新增api/pilot_teacher_plan.go、pilot_teacher_tx.go，复用teachereffects的索引/角色/schema/预算；绑定tx的repo和缓冲audit，模型/随机话术在锁和tx外冻结。现broadcastEffects/emitTeacher/commitLesson不能直接作为回调。
3. **CommitDecision**：store/pilot_commit.go + api/pilot_coordinator.go，writer重验身份/机会/token→必要claim→工具/审计→checkpoint+runtime镜像+receipt+outbox/job一事务；成功后短Room锁发布，未知结果查原receipt。只有此片完成才接typed/repeat Transition；所有旧入口一并保持封口。
4. **输出与结算**：api/pilot_outbox.go、pilot_completion.go；接管后新lease重投未完成段，不重复教学工具。现lessonexecution.go仍普通GORM.Transaction，纳job前必须提交失败/连接污染实证与安全原子路径，不能认为外层job自动修复。worker在Room/提交锁外运行。
5. **独立PilotClassroom**：typed/state.sync/receipt→媒体义务→模拟voice与timeout竞争；LogicalSubmission和MediaReadiness分离。仅全链故障/用户旅程通过才开放隔离mock试点；真实音频另门。

## 4. 需在开发前收紧的实际缺口

1. **已有活动legacy课迁移的竞态**：36“无活动请求”不是跨App可执行证明。旧Room若先查legacy、另一App迁移后再写snapshot，普通查询门会TOCTOU。最小首片建议仅允许显式新建且从未Enter的隔离课成为pilot，并由所有Enter/迁移共用持久writer模式预留；活动legacy课→pilot迁移暂不开放。若要求迁移既有活动课，必须新增共享写租约/排空协议覆盖所有旧writer，不能只遍历本App Rooms。
2. **模式表缺失与未知错误**：未启试点的DB可以保持默认模式，但不能每次“no such table”都自动legacy；需启动时明确schema能力/模式标记。开过pilot的DB遇表损坏必须fail closed，配置关掉也不能绕pilot课写门。
3. **38后继合同同步**：36入口表仍描述跟读Judge、§11.1称两种confirm均经Judge。实施应repeat.confirm_text使用EvaluateRepeat、quiz用意义Judge；这不改变确认不是正确的语义。此为旧设计描述落后，不能按旧文字重新引入跟读LLM。
4. **模式排空不是环境变量**：draining必须拒新计算并解析committing/outbox/job，兼容snapshot与mode同事务；首片没有这些worker前不能承诺可在线回退。

以上不阻塞37独立新库优化；36首片具体开关/隔离课创建及模式预留接口需主审选择后再开发。无需等待远端stream/cancel音频契约即可完成所有mock持久恢复；真实声学、模型非思考模式、原生双工能力不可由这些组件替代。

## 5. 37异步复核重点

backend保存后检查：严格空schema两次探测、同tx固定22模型/索引/6trigger等价、legacy不套外层事务、PRAGMA错误检查、COMMIT失败同连接清理、失败owned pool关闭、并发fail-fast。main logger先于Open，真实bind才ready，NewApp/播种/恢复阶段不漏计；os.Exit绕defer须显式关闭或run返回错误，不能宣称defer必执行。基线/A-B与最终全量另回收，本报告不提供性能通过结论。

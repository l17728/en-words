# Agent / 教学策略域评审：Start Kernel SDD

- 文档：`docs/41-试点课堂Start持久迁移内核SDD.md`
- 评审范围：docs/04 的 T-01、D-15、TeacherAgent.Decide/Render、PlanEffects、工具顺序/预算/task 权限、首课话术与防越界。
- 评审类型：只读设计评审；未修改产品代码/设计，未运行测试。
- 结论：**BLOCK（当前草案不能进入实现）**。隔离、只写 start、通过工具执行及不触碰旧课堂的边界总体正确；但 T-01 语义与冻结内容尚未完整落实，且当前 Agent 实现不能保证要求的话术。

## 已通过的设计点

1. 将该切片限定为测试专用、无默认产品入口；不调用模型、网络、Room、媒体或 outbox consumer；没有把它描述成可用课堂。这符合 D-15 的工具边界与试点隔离要求。
2. 文档要求教师话术先经 `TeacherAgent.Decide/Render`，效果由 `Machine.Start` 产出的 action 交 `TeacherAgent.PlanEffects` 映射，再经 Registry scope/schema/budget 管线；store 只允许固定工具、顺序和冻结数据，不能接受模型给出的任意工具、SQL 或 machine JSON。该方向符合“Agent 作教学决策，平台做授权/验证/持久化”。
3. 将 Say 放在 show_word 之前，符合 docs/04 §2 T-01 的动作顺序；不同 lesson 的回执重放要求先做当前 owner 授权，也避免以回执读取绕过 task/owner 权限。
4. “开始”本身不宣称已播放或完成教学，保留 show_word 的 queued 语义，没有隐式授予答题机会或继续执行教学流程。

## 必须关闭

### AGENT-START-01 — T-01 首课话术不满足“自我介绍”保证（P1）

SDD 只约束 `category=open`、不超过 60 rune 及合成昵称，没有冻结可验收的 T-01 语义断言。当前 `TeacherAgent.Decide(lesson_started)` 只选 `open`；`Render` 可选用 OP-1 或 OP-3，而 OP-1（“{name}，上课啦！今天我们搞定…”）及 OP-3（“Hi {name}！先深呼吸…”）都没有“我是叮当老师”的自我介绍。fallback 虽有自我介绍，但正常渲染不保证走 fallback。因此 SDD 所说“复用 Decide/Render”不能证明 docs/04 T-01 的“自我介绍+称呼昵称”已经达成。

**关闭条件：**设计明确首课采用哪个允许模板/模板集合及其确定性选取规则，所有可选正常渲染和 fallback 均包含老师自我介绍与合成昵称称呼，且保留 `≤60 rune` 与事实边界。给出固定输入/输出黄金用例，并负向证明不会读取、暗示不存在的历史成绩；不要只断言 category=open。

### AGENT-START-02 — T-01 中英对照没有进入冻结命令和持久效果（P1）

docs/04 T-01 的第二步是 `show_word(词1, 中英对照)`。SDD 的 `show_word` 命令只含 `word_id, word`，frozen-word 校验也只核对 ID 与词面；既未冻结释义字段，也没有证明 `classroom.show_word` 的纯工具结果/outbox 含该词创建时冻结的中文释义。按当前合同，词面正确仍可能产生无法满足 T-01 的效果，也可能在实现时从 live 词库补释义，违反“冻结事实权威”。

**关闭条件：**将需要展示的创建时释义（含多个释义的选取/顺序/空释义拒绝规则）加入 typed plan、fingerprint、工具参数、校验与 outbox payload；或明确产品定义证明 T-01 的“中英对照”只要求词面且中文事实由别处持久提供。任何解释都需有可审查 schema 和正反向 fixture，禁止 live lookup。

### AGENT-START-03 — T-01“首课”事实来源没有绑定到决策（P1）

SDD 说固定测试样例为首课 T-01，但没有定义由哪个不可变创建事实判定“首课”，也未要求 `TeacherInput`/决策计划绑定此事实。当前 `TeacherAgent.Decide` 对所有 `lesson_started` 都返回 `open`，并不自行区分 T-01/T-02。仅在测试里口头标记“首课”会使平台可在缺少证据时选择 T-01，或意外把 T-02 的输入套入本片。

**关闭条件：**定义首课判定字段及可信来源（例如创建事实中的 ordinal/previous-success fact），把它纳入不可变计划/fingerprint；本切片对非首课必须 fail closed 或显式排除并验证。Teacher 只收到经核验的首课上下文，不能由 HTTP 参数或模板代码自报。

## 实现约束需更精确（P2）

### AGENT-START-04 — 工具预算与任务 capability 的可执行上限

SDD 要求使用受信 `classroom_event` task、`teacher_agent` scope、Registry 和共享预算，但没有冻结本事件具体预算及消耗规则。现行 API `newAgentTaskContext` 使用 8 次共享预算，嵌套 context 不可重置；本切片正常路径需要且只需要两个工具调用（`classroom.say`、`classroom.show_word`），其他尝试/拒绝是否消耗预算沿 Registry 规则审计。需明确新 test-only capability 只能由 harness 内部为已授权 owner/lesson 生成、仅绑定此 lesson/event、过期即失效；客户端不能构造 task，也不能通过重试重置预算。验收需检查正好两次有序受限调用、错误/重复/第三次调用被预算或 allowlist 阻止，且无绕开 Registry 的 direct handler。

### AGENT-START-05 — D-15 审计调用与状态 action 关系需避免伪造 Agent tool

SDD 把 `Machine.Start` action 经 `PlanEffects` 映射为工具，方向符合 D-15；同时需在执行合同中固定 `Machine.Start` 是确定性领域状态转换，不是 Agent 决策/工具调用，`classroom.show_word` 是对 Teacher 已批准的 action 事实的受限执行入口。只有 Registry 生成审计；action index 与 PlanEffects 输出必须逐项一一对应且不可改写目标词/相位。不能再额外记录一条不存在的“Machine.Start tool”或让 store 伪造工具审计结果。

## 复审清单

当前意见为 **BLOCK**，不能仅以“隔离 harness / 不发布媒体 / 两条工具审计”作为 Agent 域批准。关闭 AGENT-START-01..03 后，需提交更新版给 Agent/教学策略域复审；AGENT-START-04..05 应在实现前写入 SDD 并在后续实现验收中锁定。

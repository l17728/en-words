# Agent / 教学策略域复审：Start Kernel SDD（第 2 轮）

- 文档：`docs/41-试点课堂Start持久迁移内核SDD.md`，并对照 `docs/04-Agent与话术库设计.md` 及现有 Agent/Registry 接口。
- 评审类型：只读设计复审；未修改产品代码/设计，未运行测试。
- 结论：**BLOCK（尚有两个实现前合同需闭合）**。第一轮三项 P1 的语义缺口均已在 SDD 层实质补齐，但新合同在 `TeacherAgent` 精确渲染接口和同一 task 的失败重试预算上仍有歧义。

## 第一轮问题关闭情况

| 原问题 | 状态 | 证据 / 判断 |
|---|---|---|
| AGENT-START-01 T-01 自我介绍和昵称 | 已关闭（设计层） | docs/04 OP-1 已改为“我是叮当老师！{name}…”。docs/41 锁定 OP-1、合成昵称归一化、固定 fallback、≤60 rune、黄金文本及不声称历史成绩；验收覆盖空/超长/控制字符。 |
| AGENT-START-02 冻结中文释义 | 已关闭（设计层） | docs/41 将冻结有序 meanings 纳入命令 fingerprint、受限计划、验证和 show_word outbox；空释义拒绝，不做 live lookup。执行器用冻结释义补足 `Action.Meanings` 后还须逐项比对创建事实。 |
| AGENT-START-03 首课可信来源 | 已关闭（本隔离 fixture） | docs/41 明确仅全新临时 DB、单一 reservation 的服务端 ordinal=1，事务复核唯一性；非首课拒绝，不信任客户端字段，也不声称隔离 DB 外历史。 |
| AGENT-START-04 预算/task | 部分关闭 | 单次执行已定为 task-local budget=2、固定两次有序调用、客户端不能构造/重置；但前次 rollback 后同一 command/task 的重执行是否重置预算未定，见新发现。 |
| AGENT-START-05 effect/tool 审计 | 已关闭（设计层） | SDD 明确 `Machine.Start` 是领域状态变更、不是 tool；`ActShowWord → PlanEffects → classroom.show_word`，Registry 产生唯一原始 audit，store 只核验/映射已有 audit 和 typed result，不伪造工具记录。 |

## 剩余阻断项

### AGENT-START-06 — 精确渲染接口未落到可实现的 TeacherAgent 合同（P1）

SDD 写“`Teacher.Decide`…由 `RenderID(OP-1)` 生成 say”，但 `RenderID` 目前是 `scripts.Bank` 方法，`TeacherAgent` 仅暴露 `Decide/Render`；其 bank 字段未导出，test-only adapter 位于 `api` 包，不能直接调用。`TeacherAgent.Render` 当前会按 category 抽取且排除了 OP-2/4/5/6，并不保证 OP-1。若让 `TeacherAgent.Render` 包装 `Bank.RenderID`，该方法仍执行随机口语化和昵称变体，SDD 的精确 golden output 不能直接作为所有运行输入的精确输出合同；fallback 也需要确保走同一 Agent 边界并带归一后的昵称。

**关闭条件：**在 SDD 明确一个可编译的 Agent 层 API（例如 `TeacherAgent.RenderLessonStartT01`，输入只含已验证的 ordinal、规范昵称和冻结词数，返回 category/text/templateID/fallback 来源）；规定 OP-1 是否允许口语化、黄金向量是精确文本还是仅语义 fixture；并冻结 fallback 路径。API 必须在 Agent 层内部访问 Bank，禁止 adapter 越过 D-15 直接读取脚本库或自行拼教学话术。验收断言每个允许结果均保留“叮当老师”自我介绍和昵称，长度≤60 rune，且不含上次成绩/能力断言。

### AGENT-START-07 — rollback 后重试可能重置同一事件预算（P1）

SDD 固定每个 task context 预算为 2，并允许在存储层证明 rollback 后，以相同 command ID 重执行同一纯工具计划。但现有 `tools.WithEventBudget` 的预算绑定 context；重建 task/context 就会重新给 2 次调用。若同一 lesson.start task/command 经第一次 say+show_word 后 rollback，再创建新 context 重跑，累计实际 Registry 调用会超过 T-01 单事件预算，违反 docs/04 §16 “每事件共享工具预算”和 §16 的 HTTP caller 不可自造 task 原则。若保留原 context，则预算已耗尽，无法按当前描述重跑。

**关闭条件：**明确该两次调用结果如何跨事务重试：要么 task capability 持有不可重置的跨重试预算，且给出超额后的终态/恢复规则；要么只在事务外计算一次经 Registry 审计的纯候选并将不可变 typed 结果交事务（需重新审查审计与 tx-bound 原子性）；或规定工具调用失败/rollback 后同一 command 只能终态失败，不自动重放。不得以生成新 task ID/context 隐式刷新预算。补充“首次失败 + rollback + 同 ID 重试”的预算与审计用例，且不得改变相同命令幂等性合同。

## 仍需实现时锁定的约束（非新 BLOCK）

- ordinal=1 仅代表隔离 fixture 的首课，不代表实际学生历史首课或产品授权；报告/界面不能外推。
- T-01 次序严格为 Registry `classroom.say` 后 Registry `classroom.show_word`；`Machine.Start` action 不能直接显示/发声。show_word meanings 必须来自冻结创建记录，保持原序。
- task 只能由 adapter 在完成 owner/active/lesson/expiry 复核后创建；共享 budget 不得由 HTTP 或重试路径重建。
- 复审范围不批准模型、语音、outbox 派发、旧 Room 集成或真人课堂试点；这些仍留在 SDD 明确的后续门。

关闭 AGENT-START-06/07 后可再次做 Agent 域复审；在此之前 Agent 域不签字进入实现。

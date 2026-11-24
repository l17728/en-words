# 有效复习梯度：原子协调器实施设计

状态：设计评审已通过并完成实施。隔离候选已通过 Go 全量/race/vet/覆盖率门及 3 条梯度 Playwright E2E；正式工作树已按 manifest 合入，合入后完整回归仍在执行。实现合同与当前状态见 [docs/40](../docs/40-有效复习梯度原子协调器.md)，候选验收报告见 `.test-tools/ladder-implementation-20261016/_baseline/`。

## 1. 真实调用链与本片边界

`config.Store.set` 持 config.mu，在配置持久保存前调用无 scope 的 OnLadderChanged；`App.NewApp` 将其绑定到只处理 MemRepo 的 TruncateLadderSteps。DeleteOverride 没有副作用协调。持久临时配置由 agenttransaction、feedbackstorage、lessonexecution 等工厂独立 NewStore，根本没有该回调。故不能只改 App 回调或给 SQLite 增加全库 UPDATE。

当前实际敏感写入口是 router 的配置 set、extra 的 override delete、App.registerTools 中管理员 config.set，以及已配对的 live Cfg 直接调用。System Agent 的 config.set_student 目前只允许 wordsPerLesson/lessonsPerDay，本片不扩大权限。未来把梯度开放给 Agent 时仍须使用受信任务、schema、scope 和同事务审计。

只改 ladder/goalMode 的有效配置协调；不重排现有 NextDueAt，不改间隔策略、不提升已截断步骤，不接入未开放的 Pilot 产品入口。复用已有每键 Global/Student revision 与 SQLite 触发器，禁止新增 revision 表、全局序号或替代 ABA 合同。

## 2. 具体接口与职责

建议新增 `config/mutation.go`：

```go
type Mutation struct {
    Operation string // set | delete_override
    Actor string
    Scope Scope
    StudentID int64
    Key string
    Value any
    Reason string
}
type MutationResult struct {
    ChangedCards int64
    AffectedStudents int64
    Noop bool
    Committed bool
    RefreshPending bool
}
// 概念签名；名称可在实施审查中精简，但三种实例状态不得混淆。
func (s *Store) ApplyContext(context.Context, Mutation) (MutationResult, error)
func (s *Store) BindAtomicMutationHandler(MutationHandler) error
```

保留 SetGlobal/SetStudent/DeleteOverride 兼容包装；产品 HTTP/tool 改用带实际 ctx 的入口。Store 明确区分：①独立 config-only（配置单测、codec 验证保持可用）；②已配对 live（敏感键必须转协调器）；③协调器私有 working copy（只在所属 work 内应用原有配置校验与保存）。已标记 paired 却未绑定 handler 的敏感写必须拒绝，不能退回旧回调。handler 在获取 config.mu **之前**分派，装配仅在发布 App 前完成，运行期不能换绑。

新增 `api/configmutation.go`：

```go
func (a *App) ChangeConfig(ctx context.Context, m config.Mutation) (config.MutationResult, error)
func applyConfigMutationInWork(ctx context.Context, w *configMutationWork, m config.Mutation) (config.MutationResult, error)
```

work 含同一事务/克隆中的 cfg、scheduler、范围检查器、待提交审计与摘要；不含可回调全局 Store 的闭包。配置包提供私有工作实例的 prepared/local apply 路径，避免 ApplyContext 再次分派形成递归。先 canonicalize 并验证 reason，再进入 work；实际存在性、旧有效配置、删除回落必须在 work 中重读，不能依据 live 缓存决定幂等返回。

具体装配用 `agentServices.configMutation` 保存受信 work 的协调能力：持久工具工厂绑定当前 tx cfg+sched；反馈内存三层 clone 在最内层绑定这三份私有对象。绝不复制 OnLadderChanged 或把 live handler 复制到 WithMemoryTransaction 克隆。callAgentTool 当前内存支路直接 a.Tools.Call；若敏感工具未来开放，默认 registry 的 handler 必须调用外层 ChangeConfig；已在 work 的 registry 只能调用其 InWork 能力，两者不可混用。现有不允许敏感键的工具保持原白名单。

新增 scheduler 能力：`CardStudentIDs(ctx)` 返回有卡学生的有序去重 ID；`ClampLadderSteps(ctx, studentID, maxStep) (changed int64, err error)` 必须显式 studentID。MemRepo 与 SchedulerRepo 均实现，后者执行 `UPDATE memory_cards SET ladder_step=? WHERE student_id=? AND ladder_step>?`，只改 ladder_step。旧无 scope TruncateLadderSteps 移除生产调用，不再对不支持仓储返回假成功。

config 提供只读有效状态快照/候选覆盖操作，复用现有 effective 优先级，不在 API、SQL 各写一套默认梯度规则。不能用 StudentID=0 伪装所有学生的有效值。

## 3. 范围计算与预检

| 操作 | 梯度可能受影响的学生 |
|---|---|
| global ladder set | 无学生 ladder 覆盖者，不论其 goalMode |
| student ladder set/delete | 该学生；delete 按事务中实际 global ladder 或有效 goalMode 回落 |
| global goalMode set | 无 student ladder、无 global ladder、无 student goalMode 覆盖者 |
| student goalMode set/delete | 该学生，且无 student/global 显式 ladder |

分别计算候选前后有效 ladder；间隔改变但长度不变也属于有效配置变更。DefaultLadder 与 SprintLadder 当前均长 8，只换默认模式不导致进度升降，但配置/revision 仍按现合同更新。

卡裁剪的候选 ID 来源为事务内所有卡所属学生（包含停用/未选库的历史卡，不以当前选库过滤）；目标学生即使无卡也须做目标授权。范围保护不能仅扫描卡：全局变更需先检查全局声明及所有会继承该变化的受保护学生/现有 Pilot scope 墓碑，包括无卡的受保护范围。任何禁止范围都在首次配置/卡写前整次拒绝；学生有显式覆盖且有效梯度未变不应被全局裁剪误伤。

仅对有效梯度变化的学生裁剪超界卡；扩长不提升，小于等于上界不写。相同值 set 保留既有 Save/upsert 与 revision/审计行为，不擅自改成无写；不存在 override delete 保留无 revision、无审计、无卡写的幂等行为。合法同值 set 不承担历史坏卡修复职责，历史数据修复须另立任务。

## 4. 持久事务及缓存

新增窄公开 store 外层包装 `WithAtomicWrite(ctx, db, fn)`，复用现有 atomicConnection 的连接固定、COMMIT 失败原连接 ROLLBACK/污染连接丢弃机制；不复制一套事务实现。配置 work 的第一条 SQL 必须取得 SQLite writer 顺序，例如 `UPDATE config_globals SET key=key WHERE 1=0`，不会改变行或触发每行 revision；再加载配置、覆盖和卡。必须以两连接回归证明 writer barrier；不能先 NewStore 读快照，再争取写锁。

外层包装显式拒绝嵌套事务，只有受信 outer owner 拥有 COMMIT 和失败连接清理权；范围检查来自装配能力，不能暴露让调用者任意传 `allowPilot=true` 的绕过参数。当前尚未接入口的 Pilot reservation 保护仍属组合器后续必接依赖，不宣称本片已启用 Pilot 产品隔离。

外层执行顺序：agentWriteMu → 安全数据库事务/writer → 读取事务 cfg/sched → 计算候选和全部范围 → 预检 → 配置及 revision 写入、范围卡更新、事务审计 → ctx 检查 → COMMIT → 缓存刷新/成功日志。配置先写或卡先写均只能发生在同一 work 内，失败由外层整体回滚。

若已经位于反馈/结课/工具事务，调用 InWork，不再获取 agentWriteMu、不再开启事务或回调 live Cfg。启用敏感键的外层 owner 必须保证 writer-first 和安全 COMMIT 清理；不能内层套 atomicConnection。既有其他 GORM Transaction 不因本片设计自动获得该保证：工厂清单逐一核对，未升级的 work 不得获敏感写 capability。

COMMIT 失败：不刷新 live 缓存、不宣称已提交，新连接和原连接均不能看到部分结果。成功后使用现有 Reload 思路，禁止把提交前的整个私有 cfg 盲覆盖 live 缓存。刷新需一致快照加载 globals/overrides，且同一 App 的配置写入口统一经串行写门，避免较旧刷新覆盖较新配置。

Reload 自身仍在 agentWriteMu 范围内执行；锁不得在 COMMIT 后、Reload 前提前释放。

提交成功但 Reload 失败：返回 committed=true、refresh_pending=true（兼容现 committedToolError），不得作为“保存失败”自动重试。标记本 App 配置快照需刷新，随后配置读取/调度入口必须先刷新成功或显式失败，不能继续拿旧 ladder 运行。只重试读取，不重新写配置或裁剪卡。该标记是缓存状态，不是新 revision 机制。

多 App 没有进程间缓存推送：变更范围判断和调度持久写必须使用事务新鲜配置；配置 API 读取可依据现有 ladder/goalMode 的 Global/Student revisions 校验并刷新。不能承诺另一个 App 的既有内存快照瞬间同步。实施验收须核对每个消费有效 ladder 的边界，不能以数据库正确掩盖旧缓存继续调度。

## 5. 内存事务与锁顺序

复用现有固定顺序：`agentWriteMu → App.mu → Cfg.WithMemoryTransaction(config.mu) → Sched.WithMemoryTransaction(MemRepo.mu)`；如嵌入反馈/结课原 work，继续保持之后的 memory repo 锁顺序，绝不逆向。最内层只用私有 cfg/sched 计算、写入、校验和收集审计，所有可失败操作必须在外层发布前完成。成功后各层仅交换克隆，不再执行可能失败的副作用。任何错误取消均丢弃两份克隆。

禁止持 MemRepo 锁反调 live Cfg、持 config.mu 反入协调器，禁止回调中重新拿 App.mu。普通卡写通过同一 MemRepo 锁受阻，不会被 clone/replace 覆盖；普通配置写通过 config.mu 受阻。现有 clone API 不是跨两个仓储的无锁读取快照承诺，跨域强一致读取仍应走协调 work。

仅支持纯内存成对后端或同一 SQLite work。`MemRepo + 外部配置 persister`、未知自定义 repo 在任何写入前返回 `atomic_configuration_unavailable`；不能用只克隆内存卡冒充外部保存可回滚。失败适配器用例锁定拒绝且零副作用，不伪称支持混合事务。

## 6. 接线和日志清单

- App 初始化移除 OnLadderChanged；配置 set/delete HTTP 与管理员 config.set 进入 ChangeConfig，保留原权限和工具审计。不能新增 HTTP-only 旁路。
- 检查 agenttransaction、feedbackstorage、lessonexecution 的私有 cfg/sched 配对；playbacksettings、overdue 仅使用非敏感键时保持其边界，不扩大权限。未来 Pilot 组合器只接受带范围预检的同 work 能力，不现在接产品入口。
- config-only 单测/codec 不被强迫依赖 scheduler；生产 paired 实例漏装配有负向测试。
- 日志固定事件 config_mutation_started / committed / rolled_back / refresh_pending；携带 trace_id、module、operation、key、scope、适用 student_id、受影响学生数、改变卡数、transaction_outcome、稳定 error_code。提交前计数只能记 proposed，成功事件只在 COMMIT 后输出；不记 SQL、配置原文或用户正文。工具调用审计与变更处于同一 work；配置 audit 回调若当前仅日志性质，应转缓冲提交后发出，不能记录已回滚成功。

## 7. 红绿矩阵（全部待执行）

| 层级 | 必须先红/锁定的场景 | 修复后断言 |
|---|---|---|
| SQLite + Mem | global 缩短，A继承/B覆盖；student B set | 仅实际受影响学生卡超界下降；隔离其他学生和所有非 step 字段 |
| SQLite + Mem | delete ladder：显式global/默认goal两种回落；不存在覆盖 | 回落取事务真值；无覆盖删除零写、revision不增 |
| config + API | global/student goalMode set/delete及ladder优先级全矩阵 | 长度8模式切换无伪裁剪，实际间隔可见；due时间不变 |
| SQLite | 配置写失败、第二学生卡更新失败、审计失败、真实延迟约束COMMIT失败、取消 | 配置/卡/revision均回滚；原/新连接一致；缓存不发布；解除故障重试成功 |
| Mem | 私有cfg/card改后注入失败、并发普通设置/卡写 | 两克隆均不发布，无丢更新；混合后端先拒绝 |
| 两独立App/连接池 | global/student/delete交错；缩短→扩大 | 可成立的串行截断历史；不要求扩长恢复步骤；无静默成功 |
| API/工具 | 管理员REST+config.set等价；角色越权；paired漏绑定；inner嵌套事务 | 无绕过，无副作用；System Agent权限不扩大 |
| scope | global影响继承Pilot/legacy混合；无卡保护对象；有独立覆盖对象 | 禁止范围整次拒绝，零配置/卡/revision变化；未受影响对象不误伤 |
| cache | COMMIT成功但Reload失败，再查询恢复 | 明确已提交待刷新；不二次写、不重复截断、不用旧缓存调度 |

真实 Playwright 旅程：管理员UI缩短global → A/B各刷新与重登录核对有效梯度/卡；管理员设置再删除B覆盖 → 实际回落；重启验证持久化；切换global/student goalMode并核有效间隔；SQLite触发器故障使真实UI提交失败、重新读取无变化，解除后主动重试成功。用数据库补充断言卡字段，不以直接DB写替代用户配置操作。

实读确认：当前 web/src 没有 ladder、goalMode、/config/set、/config/override 的产品调用；Admin 页面只有跳往学习设置的导航，不能当作已有梯度编辑界面。现 GET /config 只读 studentID=0 的全局视图，也不能展示学生覆盖来源。因此真实UI旅程依赖主审已批准、尚待实现的最小子片：管理员“复习梯度”面板，明确全局/指定学生，展示有效值与来源，支持梯度/目标模式编辑以及学生“恢复全局”操作，保存必须有 reason；提交失败保留编辑内容，已提交待刷新只提供刷新，不再次保存。

该子片新增管理员只读 `GET /config/effective?studentId=<id>`，返回 ladder/goalMode 各自 effective、source(global/student/default)、has_override 及现有 revisions；不暴露新revision格式。学生目标复用现有用户查询而非猜测ID。写复用协调后的 set/delete API；删除确认文案显示删除后回落值。读接口同样执行缓存/事务一致性约束。UI子片及真实浏览器验收未完成前，不能宣称本矩阵的用户真实旅程通过。所有新场景分别计入逻辑回归与E2E，不复用 docs39 冻结树结果。

## 8. 评审结论与批准条件

architecture_review 已认同 API/work coordinator 优于把卡侧逻辑塞入 ConfigPersister；双方一致要求 writer-first、外层安全COMMIT、私有实例不递归、真实删除存在性、显式scope及混合后端拒绝。设计不变更16个尚未接入口的私有Pilot组件。

2026-10-16 architecture_review 实读本文件后独立结论：架构/安全/Agent 方向 **0 项新增设计阻断**。确认接口、writer-first、安全COMMIT、内存锁序、同长间隔scope、删除及缓存刷新合同；仍须主审确定UI子片范围，并在编码时逐点锁定有效ladder消费边界。未接Pilot保护不得用来延后当前legacy配置缺口。

主审后续明确批准本片包含缺失的真实UI删除/学生覆盖入口，无需再向用户请求该范围授权；消费边界与DTO以下述补充为准。实施完成后必须独立review、先红后绿证据、全量覆盖率/vet/race与完整浏览器回归；本文件不声明已完成任何新功能或测试。

## 9. 已批准面板与精确读取 DTO

管理页增加“复习梯度”区，范围选“全局 / 指定学生”，学生从管理员用户查询中选，禁止自由拼接用户名当ID。编辑字段为目标模式、梯度数组；分别保存，带 reason，不以一个表单保存偷偷重写另一个键。学生两个字段各自显示“恢复全局”按钮；删除 ladder 后可能按学生自己的 goalMode 导出，应显示真实预览，不承诺总是全局目标模式。全局不新增删除入口。每次范围切换清旧表单并取消旧请求，迟到响应不得覆盖新目标；提交中禁双击、保留失败内容。目标模式不影响显式梯度时明确显示“当前使用显式梯度”。

交互沿用 Admin 已有同步 ExclusiveAction single-flight 与 LatestRead 生命周期模式，不用仅React state抵挡同帧双击；身份/token变化清除旧目标及表单，旧请求不再提交或展示结果。超时/连接中断属送达未知，先重读有效值和revision提示用户核对，不自动重发set/delete；这类普通配置写不冒用反馈提交ID幂等保证。

`GET /api/v1/config/effective` 仅管理员，省略 studentId 或明确 0 表示全局；指定学生必须是合法正整数且存在学生身份，禁止负数、空字符串、重复参数或其他角色ID静默变全局。停用学生允许管理员查看与修正历史配置，但不扩大其登录/学习权限。结果示例（学生未设 ladder、学生 goalMode 覆盖）：

```json
{
  "student_id": 7,
  "goalMode": {
    "effective": "exam_sprint", "source": "student", "has_override": true,
    "override": "exam_sprint", "fallback": "longterm", "fallback_source": "default",
    "revision": {"global": 0, "student": 2}
  },
  "ladder": {
    "effective": [1,1,1,1,2,2,3,3], "source": "default", "has_override": false,
    "derived_from": "goalMode", "goal_mode": "exam_sprint", "goal_mode_source": "student",
    "revision": {"global": 0, "student": 0}
  }
}
```

source 严格只有 student/global/default，表示该键的值来源，不表示调用者选的面板范围。ladder 为 default 时必须附 derived_from、goal_mode、goal_mode_source；显式 global/student ladder 时不附这些推导字段，即使数组恰与默认相同也仍为显式来源。has_override 表示该指定学生实际存在该键的覆盖，global视图永远false。override 只在覆盖存在时返回；fallback/fallback_source 只为可删除的学生覆盖返回，ladder fallback 为默认推导时另附 fallback_goal_mode/source。fallback 是仅删除当前键后的候选：删除 ladder 不删除 goalMode，删除 goalMode 不删除 ladder。

两键 revision 均沿用现有 `{global,student}`，读到删除墓碑也保留数值；ladder 默认推导的完整依赖必须同时携带 goalMode.revision，不能只比较 ladder.revision。DTO所有字段、候选回落及revision从同一只读事务/内存锁快照计算，不拼接多个时点。该DTO不引入写入CAS合同；当前面板采用后提交覆盖语义，保存后重读，不拿过期fallback替服务器决定删除结果。

写路径保留 /config/set 和 /config/override 的权限，补严格 scope/目标/正文验证，未知scope不得默认global。仅在返回已提交结果后展示“保存成功”；refresh_pending 显示“已保存，读取待恢复”并提供刷新，不能把此状态当可重试写失败。HTTP外包装延续项目约定，业务data增加 committed/refresh_pending/changed_cards，旧调用者不依赖原字符串结果时同步适配。

## 10. 全部配置入口核对表

以下仅列生产源码；纯配置单测和 codec 明确另列，不用“统一缓存门”一句话代替接线。所有现有无ctx兼容方法仍存在，但真实请求把ctx传到 ChangeConfig。

| 文件 / 函数 | 当前实例与错误出口 | 实施时明确接线 |
|---|---|---|
| api/router.go 配置 POST /set | live a.Cfg，返回业务失败 | strict decode→ChangeConfig；global/student明确校验 |
| api/extra.go registerConfigRoutes DELETE /override | live a.Cfg，返回业务失败 | 同协调器delete；存在性/回落在work取真值 |
| api/app.go registerTools config.set | live a.Cfg，handler有error | 管理员授权/schema之后ChangeConfig，缓冲审计与提交结果对应 |
| api/agenttools.go registerAgentTools config.set_student | svc.config 可live/tx，handler有error | 现仅两个计数键白名单不扩大；svc附work协调能力，为敏感键拒旁路负例 |
| config/config.go SetGlobal/SetStudent/DeleteOverride | 无ctx兼容方法、有error | paired实例敏感键获取mu前分派；working local apply不递归；未装配paired拒绝 |
| api/playbacksettings.go 写五项播放参数 | tx私有cfg或内存原子发布、有error | 不涉及ladder/goalMode；仍与本App写门同序列，不能被完整缓存替换覆盖 |
| store/runtime.go ConfigPersister.SaveGlobal/SaveOverride/DeleteOverride | 存储原语、有error | 不把scheduler逻辑放persister；敏感产品写只由受信working cfg调用，无新HTTP出口 |
| store/pilot_created_audit.go validatePilotFrozenConfig | standalone无persist cfg，仅校验冻结数据、有error | 保持config-only，不截断生产卡、不装live hook |
| config.NewStore/Reload、config.WithMemoryTransaction | 启动/刷新/私有克隆，有error | 加载不等于配置变更，不裁卡；clone不复制外部handler |

私有 work 工厂逐点装配：agenttransaction.callAgentTool、feedbackstorage.withFeedbackWork（SQLite和三层内存clone）、lessonexecution.commitLessonWithAgents。overdue.refresh/execute 工厂和 playbacksettings 工厂保持各自非敏感职责；未来需要敏感写时只能显式携带协调capability。不得因一个cfg由NewStore创建就默认它已经获得跨仓储原子能力。

## 11. 全部梯度消费边界与刷新前门

选择明确门而非让无error的 `Cfg.Ladder` 暗中失败：生产 paired scheduler 的梯度写方法必须运行在配置 work；纯standalone scheduler 保持现有测试/库兼容。paired live持久service误调返回 `configuration_work_required`，不得继续用a.Cfg缓存。新增 `App.WithCurrentConfigWork(ctx, fn)` 外层与 `...InWork` 内层变体，复用安全writer-first和私有cfg/sched；已有outer持锁/事务只用InWork，不能重入agentWriteMu。

内存入口与ChangeConfig共享agentWriteMu序列；原已持该锁的B结课不重复获取。paired纯Mem的当前配置读没有外部持久刷新失败，仍在同work中冻结本次值；不为此把原有结课完成任务重新计算或恢复已裁剪进度。外部自定义仓储的 pendingSettlements 部分失败重放不是本片支持的成对内存事务，不得拿它冒充原子内存后端。

B内存分支须显式获取受信 InWork scheduler view/capability，不能原样把已标paired-live的a.Sched传入后又绕过拒绝门；该view必须保持原仓储及结算/完成元数据的所有权，不用重新NewService悄悄丢失重试状态，不暂时修改live service的共享cfg指针。所有work capability仅限回调生命周期使用。

| 实际消费者 | live/tx/冻结及现返回值 | 刷新前门 / 失败时行为 |
|---|---|---|
| scheduler/scheduler.go planNext（PlanNext、PlanNextWithPolicy共用，读Ladder约442） | service.cfg；返回(*Lesson,error) | paired生产仅在current config work；读取失败在CreateLesson/Reserve之前返回，不使用默认值兜底 |
| api/app.go PlacementSubmitContext finish调用PlanNext约365 | 当前直接live a.Sched；有error | draft现有先提交语义保留；后续ResetForReplacement+PlanNext改同一current config work，刷新/tx失败返回placementPlanError且不做半个reset |
| api/lessonagenttools.go planLessonFromAgent / 下一课工具约103 | B work scheduler，返回error | 已有work读同一cfg，不能重入live；pending/已完成下一课凭证保持原幂等语义 |
| scheduler/settlement.go SettleLesson约28 | B使用tx cfg；内存B当前live且持agentWriteMu；有error | 持久outer先writer再NewStore，内存沿同写门序列；在冻结结算卡目标之前取得本次有效梯度 |
| api/lessonexecution.go commitLessonWithAgents | completionMu→agentWriteMu；有error | 持久结算+下一课共用一个fresh work；不拿已保存课堂梯度覆盖当前调度配置；旧完成凭证重放不重新结算 |
| scheduler/scheduler.go SettleLessonFinished约626 | legacy公开方法；无生产调用点；有error | paired实例非work调用拒绝，standalone保留；新增负例防未来调用绕门 |
| scheduler/scheduler.go MaintenanceCheck约750 | 公开方法；无生产调用点；有error | 同上，失败不得SaveCard；未来入口必须current config work |
| api/router.go GET /config（约404）和新 GET /effective | 当前live无error getter组合 | 改一致只读work加载；失败响应storage/config unavailable，不输出旧ladder假成功 |
| api/hub.go Hub.Enter约124→Cfg.Snapshot | live快照，Enter有error | 新建课堂先取得带error的FreshConfigSnapshot(ctx,sid)，在Hub.mu之前完成，避免Hub锁→agentWriteMu倒序；失败不创建/启动新machine |
| config/config.go Snapshot→LadderLocked / Ladder / String(goalMode) | 无error叶子getter | 只读已验证工作快照或纯Mem状态；不能把网络/DB读取塞进无error getter；产品调用点由本表前门保护 |
| classroom.NewMachine / Restore / State.Config | 已冻结EffectiveSnapshot含ladder/goalMode，当前教学不直接用Ladder调度 | 旧课恢复保留保存快照；已存在房间重入不因当前配置刷新失败重置旧课。新课取fresh快照；不能把“刷新”变成课中改节奏 |
| store/pilot_creation_plan.go readPilotConfigSnapshotTx / verifyPilotPlanConfigTx | 已有tx直接NewStore，返回error；尚无产品入口 | 保持同tx快照与既有revision指纹，未来outer负责writer；不能接live缓存，不改nonce重放冻结合同 |
| store/pilot_created_audit.go validatePilotFrozenConfig | 独立验证冻结值，有error | 不读当前live配置，不触发裁卡 |

补查非消费者：scheduler.HandleOverdue/PostponeLesson/BoostWord不读ladder；api.statistics新建scheduler虽传a.Cfg但当前只读统计，不触发上述调度方法，不能据此伪报已需刷新。classroom.playback里的snapshot复制/旧版本恢复不重新选择默认梯度；Cfg.Int其他设置不因此整片改为可失败接口。

FreshConfigSnapshot/只读配置work持久分支始终从一致DB快照读取，因此即使另一个App修改也不依赖本App标脏才发现；同App提交后刷新失败不能阻止“新鲜DB工作快照可读”的请求，但必须禁止回退旧live值。若DB读取失败则该操作失败。配置缓存标脏只用于兼容缓存修复，不是绕过fresh work的通行证。已保存课堂冻结快照、已完成事务凭证是明确例外，不要求当前配置可读才能继续重放历史。

Hub.Enter具体顺序：先持Hub.mu查已有房间，有则直接沿原房间；无则释放锁读取已保存课堂状态，有保存状态只恢复原冻结配置；确为新课才取得fresh snapshot。随后重新持Hub.mu二次确认，其他请求已创建则使用其房间，不覆盖；仍无才创建。只读DB快照取得时刻是新课配置冻结点，不需锁住之后所有配置修改；已有/已完成房间重进不被无关当前配置刷新失败阻塞。

新增回归必含：配置提交成功+Reload失败后新课进入/摸底完成/结课下一课均读取新DB值或明确失败；两App一方改梯度另一方规划读取新值；内存并发ChangeConfig与B结课不死锁且串行合法；恢复旧课保持冻结配置；paired service直接调用四种写方法拒绝；新增Hub前门不在Hub.mu下取agentWriteMu。每条业务失败均验证没有额外卡/课程写，补对应UI可观察失败/恢复旅程。

2026-10-16 第二次独立复核：architecture_review 实读§9–11后确认 **0 项新增设计阻断**。核对通过DTO来源及双键revision、四类梯度写消费、Placement live入口、新课堂快照门与旧课冻结例外、paired work拒旁路、inner不重入写门和三层clone绑定。主审已批准UI范围；当时等待候选验收的状态为历史记录，当前实施与验收状态见文档顶部及 `docs/40-有效复习梯度原子协调器.md`。

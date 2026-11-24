# 冻结 startup / repeat / G1 最终架构、安全、Agent 复核

状态：2026-10-16只读实际复核；未运行新测试、模型请求，未改生产或AGENTS。批准范围代码新增阻断 **0**；最终浏览器回收仍是独立验收门，不能用本记录提前封板。

## 当前可提交内容与证据

- 新库startup：`store/startup.go`只在严格判空后走原子新建schema，writer内二次检查，已有库保原AutoMigrate；失败关闭本次取得的池。main提前固定摘要日志，真实Listen成功后才server_started。`ownedRuntimeCleanup`先停止服务/后台，5秒停止失败保池并固定失败事件；正常才关池，原error/panic保持。没有放宽SQLite同步、启动30秒或owned-stop15秒门。
- 跟读：`judge/repeat.go`独立Latin/NFC与词内撇号词法，只目标侧模板骨架；无中文释义/LLM判对。原始输入控制/跟读与quiz既有sanitize分离；控制整句匹配，否定/疑问不猜命令。quiz评分及Agent机会策略未改。unsupported_target不是读错或成功，复杂spoken_target与声学来源仍未覆盖。
- G1：preflight endpoint保学生ownLesson鉴权、环境开关仅显示本地设备预检，始终native_duplex=false、automatic_teaching=false。端侧不上传、不转写判分；能量门不等于声学AEC/VAD已验收。独立Session/store原子组件尚未接真实Room，不冒称统一课堂exactly-once。remoteprovider严格格式/取消/限制不等于业务required或模型质量验证。
- 固定日志保trace及摘要，启动/预检/跟读不记录密钥、SQL、学生正文。各项详细发现及闭合保留在`startup-backend-review-20261016.md`、`repeat-contract-architecture-20261015.md`、`duplex-foundation-architecture-review-20261015.md`；后者旧124待验段为历史，不当作本次最终计数。

已核原始r15 coverage/race summary：23包、535顶层/1225含子PASS、9显式opt-in SKIP；coverage/vet退出0，330.08秒；race退出0，966.12秒；Go全树SHA `b837a6b530fbb0f26bffa970db302d3c3703e3458a00d10f65713cf04c8fdcf4`。核心覆盖全部达门（含api80.95%、classroom91.30%、scheduler90.47%、judge98.76%、duplex97.60%、remoteprovider96.67%、store89.86%）。原始路径为`server/.test-artifacts/final-runtime-{coverage,race}-r15-summary.json`。

当前cleanup候选启动14/14、219.006秒、0skip/flaky/retry，见`startup-cleanup-browser14-20261016.json`。历史fresh候选固定三对A/B见`startup-browser-and-ab-20261016.json`：旧3656/2781/4390ms、新1516/1313/1188ms，schema三对等价；不同候选hash分账，n=3不是p95，CIM35次unknown不能归因IO。旧repeat片r13/最终127已验收，不自动转算为当前cleanup候选全量127通过。

当前完整浏览器恢复族出现missing-schema模型调用计数为0；实际规则fallback成功不能代替schema用例。仅missing/null夹具Connection:close与无正文socket观测已实际审通过，原次数/结果/时限不变。Node真实socket终态需等待socket close而非仅server close，修正另留失败记录。旧23ms运输错误的keepalive原因未被证实。等待专项与完整42回收，其他族复用须由runner同生产/binary/hash核对。

## 明确保留的未完成边界

docs36统一提交整链和全部legacy写门未实施；`.test-tools`是未编译、未运行SQL的草稿，不是可提交生产实现。真实远端转写role12请求已完成，格式/静音/预算未达门，不继续提示试探或切模型；普通input_audio WAV可达不等于原生流式/取消或非思考模式。课堂热COMMIT长尾、SQLite busy及复杂词条spoken_target未由新库性能优化关闭。ladder有效配置/卡步骤一致性与取消选库遗留runtime缺口是已单列后续设计，不能写成不存在，也不混入本次startup修复已完成范围。

可按上述范围提交冻结代码、测试、设计和证据；最终阶段完成须等待当前浏览器清单回收。secrets、DB、音频临时产物、工具二进制及`.test-tools`草稿不得随普通代码提交。

## Pilot启动兼容窄修订复审

`pilot-startup-compatibility-and-delete-design-20261016.md`的三门区分可接受：未迁移正常启动只核结构/marker/G1冲突，不引入全runtime归属扫描；显式迁移取得writer后严格审孤儿并原子拒绝；v2始终严格。不得以宽松bool绕过partial schema或把孤儿静默删掉。现仅设计，尚未修改草稿调用行为或测试。其删除generation屏障是另一未批准扩展，不是三门修订的前置实施内容；可以避免把范围继续扩大到所有旧课堂生命周期。


## 2026-10-16: historical intermediate review checkpoints

This section repairs an encoding-damaged status appendix; it does not replace original test artifacts.

At the first checkpoint, Pilot code existed only in .test-tools. Ready-schema inspection included the supported created payload audit and ASCII-case namespace handling. Isolated r1 failed compilation (package declaration matched inside a comment): zero tests and no SQL-execution evidence. Only the integrator's three Python tests passed at that point.

Isolated r2 subsequently passed: 31 top-level / 216 including subtests, zero failures or skips, 322.906 seconds including compilation; package test body 46.176 seconds. Raw summary: .test-artifacts/pilot-component-targeted-20261016/r2-summary.json. Source mappings and results: pilot-component-isolated-targeted-20261016.md/json. Production bytes were unchanged. This demonstrated isolated SQLite component tests, not full store/race/coverage or Room/HTTP/Agent integration.

Recovery fixture review separately approved explicit enter/completed/start_repeat prerequisites, atomic recognition-object capture, and the bounded B-side protocol barrier for client isolation. The barrier does not prove unblocked concurrent server progress. Fixed 7+7+7 partitioning required exact test-ID unions and unchanged production/binary inputs, retaining prior failures and the 900-second runner timeout. Node 76 and partition Python 6 were separate reported evidence, not a complete browser acceptance claim.

The independently checked input baseline was .test-artifacts/schema-transport-20261016/inputs-before-full42.json (43 files). Only the reviewed recovery spec bodies changed; the prior spec SHA was 425a0df452902fa0d7eff71d0baf68c934272f49a83c7dd35adec6677b11d315. Later SUB02/SUB03 outcomes and successor candidates must be assessed from their own reports, not inferred from this intermediate fixture approval.

## 2026-10-16: formal Pilot component integration (current status)

Read-only review verified all 16 current SHA256 values against `pilot-component-integration-20261016.json`. All pre-gofmt normalized texts match the isolated tested copy. The constructors/verifier/migration are package-private. No App/main/HTTP/Agent caller or default Open/22-model migration registration was added; no old SchedulerRepo replacement was integrated. Static integration scope: 0 new blockers. This is persistence-component integration, not a usable pilot classroom. Isolated r2 (31 top-level / 216 including subtests) does not replace formal-tree full regression, coverage, race or browser acceptance.

The unique 50-round diagnostic report was independently reviewed: finalize median 24ms, nearest-rank p95 124ms, maximum 155ms, no slow stacks. Historical 21.678/22.594-second tails remain unexplained and open. Repeated identical snapshots do not represent full classroom persistence cost.

Docs39 introduces a new frontend candidate. Old browser evidence does not automatically cover it. Initial review requested generation-scoped REST-error clearing plus stronger bounded-frame and second-student assertions. Actual revised code and acceptance are tracked separately; this UX change is not a commit-latency fix.

### Docs39 observer implementation review

The revised helper and Classroom integration were read directly after the logging increment. Generation-scoped REST-error cleanup is implemented; obsolete tickets cannot clear a later wait. Observation emits only fixed summary fields, catches observer exceptions, logs no empty/stale clear, and orders replacement as old cleared/replaced before new started. It adds no request, ACK, receipt or teaching action. Static product blockers: 0. Five helper tests include transition/privacy and throwing-observer cases; their execution is owned by the browser agent, not this review.

The four browser cases retain bounded ordered downstream frames, original ACK counts, REST failure without a close event, and a separate student's completed-class result. This is result isolation, not simultaneous speech isolation. The console collector must retain arrival order when resolving asynchronous argument values; the reviewer requested this fixture correction without relaxing exact event-order assertions. Runner registration adds four continuation cases: 131 ordinary cases plus 14 startup cases, 145 planned in total. New-source acceptance remains pending; original long-tail failures remain open.

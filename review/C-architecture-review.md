# C 阶段架构、安全与 Agent 独立复核

日期：2026-10-14。评审者：architecture_review。范围：当前工作区 `feedbackexecution/plan/storage/tools/query/routes`、工具绑定、`store.GetEntry`、Agent 反馈策略及 docs/02、04、12。既有安全交叉评审见 `C-security-agent-review.md`。未改 AGENTS.md，未读取密钥，未提交或推送。

## 已核实的设计边界

- **身份与隔离**：学生身份来自认证声明，并在反馈服务及每个写事务重新检查有效账户；客户端不能指定 task/student/owner。管理员待办入口使用管理员路由权限，学生查询由 `(student_id, submission_id)` 限定。配置、课程和难词资源由受信任务及冻结计划派生，工具校验 App/角色/触发器/资源归属。
- **跨实例幂等**：数据库唯一键建立提交身份；重复 POST 不重跑。每个意图事务先更新 task version 取得 SQLite 写锁，再检查完成凭证、版本及资源。业务写入、意图结果、通知、待办和工具审计处于同一事务。失败尝试另事务写摘要，并再次检查完成凭证，避免覆盖竞争者成功结果。
- **冻结与恢复**：解析在业务事务外，日期锚为首次提交时间；计划安装后冻结绝对配置目标、revision、课程和词条集合。配置 revision 保留删除墓碑，防 ABA。成功项仅查询；缓存刷新失败不撤销已提交事实或再次执行工具。内存事务使用私有副本及固定锁顺序，不宣称重启持久性。
- **损坏计划**：`decodeFrozen` 拒绝负索引、与行索引不符、非法负依赖、自依赖及前向依赖；整条运行先校验 schema=1 和连续索引。因此依赖访问只能落到此前有效项，损坏后项不会让前项先执行。已有 `TestFeedbackCorruptFrozenPlanFailsWithoutBusinessWrites` 覆盖五类损坏。
- **错误分类**：`WordbookRepo.GetEntry` 仅将 `gorm.ErrRecordNotFound` 转为领域未找到；其它数据库错误原样传播。缺失资源终止失败与暂时存储故障可恢复得以区分，不能将数据库故障假报“未选词库”。
- **Agent 归属**：相对词量 ±10 及 50–100 边界由 `agent.ResolveFeedbackWords` 决定，按原意图索引分组；平台冻结、授权并提交。回复通过 `agent.RenderFeedbackFacts` 依据实际工具事实生成，回滚同时丢弃成功回复，不接受模型自报成功。此结论只覆盖 C，不代表全部 22 工具或 cron 已实现。
- **文档对应**：docs/12 §8 与 docs/04 §8 为本轮冻结合同，docs/02 §17 覆盖旧反馈单状态/共享队列描述；查询、恢复、待办和通知端点与增量清单对应。纠正 docs/12 无 ID 生成“UUID”的描述为实际 `tr-*` ID；现代客户端仍使用 UUID。

## 本轮发现与修复

| 问题 | 处理 | 回归证据 |
|---|---|---|
| 终止失败的前置项令后项永久 `retryable=true` | 先补 §8.2 合同；前置终止则后项保存终止凭证，前置 pending/可重试失败才允许恢复 | `TestFeedbackDependencyRetryabilityFollowsPredecessor`；独立红跑见 `.test-artifacts/c-dependency-red.txt` |
| 规格要求的 `feedback_admin_escalated` 未发出 | 只在事务成功提交且实际结果为 escalated 后记录，跳过已完成项不重复发出 | `TestFeedbackEscalationLogOnlyAfterCommit`；独立红跑见 `.test-artifacts/c-escalation-red.txt`；覆盖回滚不发、恢复只发一次及 trace/脱敏 |

以上两项代码和测试由本评审者补充，**其验收交给 backend_acceptance 独立复核与执行，不计作作者自审通过**。浏览器代理收到依赖状态用例要求，使用隔离 SQLite 的显式持久计划故障夹具；正常语义等价意图已去重，不能把该夹具描述成自然语言正常生成的计划。

主代理已交叉复核新增实现：终止与临时前置判断正确，提交错误分支不发成功日志，已完成项跳过不重复发日志；静态复核无新增阻断。最终运行仍以测试代理证据为准。

backend_acceptance 已独立确认新增缺陷红→绿；定向证据 `.test-artifacts/c-final-targeted.txt`，覆盖两项新测试及 HTTP token 隔离。后端全量及覆盖率证据由 `review/C-backend-test-review.md`、`server/.test-artifacts/c-current-summary.json` 统一记录，不以单项通过替代总验收。

追加独立复核 `memory/transaction_regression_test.go` 与 `scheduler/transaction_regression_test.go`：用例预置已有记录、时间指针和词 ID 集合，在工作副本修改期间即断言原仓库不变，再分别验证 commit/rollback 后的内容、集合与序列号。该方法使原先依赖并发调度先后才能进入的深复制分支稳定可测，且锁定了隔离语义，不是仅为命中语句；静态复核无阻断，未涉及产品实现变更。

## 前端会话方案复核

接受 Routes 按 userId+token 重建页面局部状态、API 返回前比较请求时 token 和实时 token、旧 401 不清除新登录的方案。token 比较必须保留在异步 `res.json()` 之后、401 处理之前。静态 Modal 不属于路由生命周期，须会话变更时销毁，且确认回调检查创建时身份，防止旧正文在新身份下提交。浏览器应验证旧管理员详情、学生草稿清理及迟到 401；最终代码与报告由浏览器代理提供。

### 最终代码复核（2026-10-14）

已在浏览器代理恢复红跑临时旧行为后，重新读取实际 `App.tsx`、`api/client.ts`、`Dashboard.tsx` 及新增 E2E。结论：**本次会话补丁未发现阻断问题**。

- `Routes` 的 key 实际包含 userId 和 token；同角色换账号、同账号续 token 都重建页面局部状态。key 仅用于 React 协调，没有写入 DOM 属性或日志。
- `App` 以 `[userId, token]` effect cleanup 调用 `Modal.destroyAll()`，覆盖静态弹窗不随路由组件卸载的问题。
- client 在 `await res.json()` 后检查捕获 token 与当前存储，再进入 401 分支。迟到旧成功数据不会返回给新页面，旧 401 不清新 token；当前 token 的 401 仍清态并跳登录。正常登录响应先返回、随后由调用方更新 token，不被误判成旧会话。
- Dashboard 移除词库静态确认捕获 generation 与 token，`onOk` 发送 DELETE 前双重校验；旧组件卸载推进 generation。未发现该补丁输出 token 的日志调用。
- E2E 含同角色学生草稿/选中项/静态确认清理、旧通知迟到不显示、设置未保存值清理、同账号续 token 后旧 401 与当前 401 分流、管理员详情关闭。依赖用例在隔离 DB 同时更新任务与意图冻结字段，随后真实管理员变更 revision、真实 UI 恢复，断言终止依赖无按钮且资源不变；明确属于持久计划故障夹具。

浏览器代理已提供旧行为红证据 `web/.test-artifacts/closure-1791942868502-3116/results.json`；正式四组最终绿色报告由统一验收记录登记。本次复核未改动正在验收的前端文件。

## 频率规则追加复核

浏览器依赖用例发现“每天一次”未解析，测试代理进一步锁定超界次数被默认或局部匹配为 1/2 的缺陷。已复核主代理的最终 `feedback.go` 修订及 docs/12、04 合同，限定于声明的明确频率数量与安全边界，**所报阻断已关闭，静态复核无剩余阻断**：

- 完整捕获阿拉伯/中文数量与次、遍、节单位，再判断是否允许 1/2；缺量澄清，超界/无法可靠转换/溢出确认，分钟不当课程次数。12 不再后缀命中 2，单独负数或小数不能局部变成合法整数。
- 原文保护逐个每天/一天/一日前缀检查，合法分句不能掩盖另一无法匹配的频率分句；模型将“每天4次”误归一为 1 时也须确认。规则聚合后再次检查完整原文，避免逗号拆句丢掉“或两次”。
- “一次半”“一次或两次”及范围需要确认；续接数字必须带频率单位。因此独立“每天一次，50个词”“每天一次，半小时”“每天一次，10分钟”不再误触发频率确认。
- `frequency_regression_test.go` 同时覆盖规则、fakeLLM 原文保护、合法独立词量/时长及相对减量组合。独立扩展红证据为 `.test-artifacts/c-frequency-expanded.txt`；最终绿跑、全量和对应浏览器结果由统一验收登记。本评审未改产品或测试文件，未自行宣布浏览器冻结。

## 验收限制

本记录为静态复核及缺陷闭环记录，不代替最终全量 Go、覆盖率、Playwright 和六域总验收。新增用例绿跑及最终报告由统一验收台账登记。无 gcc 的本机未运行 `-race`；语音 mock 浏览器测试不证明真实 ASR/TTS 可用；本机精选判分 100/100 不证明反馈解析或所有模型能力达标。其余路线图和项目 GAP 不因本次 C 复核被宣称完成。

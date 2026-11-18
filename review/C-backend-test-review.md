# C 阶段后端与测试验收复核

日期：2026-10-14。复核者：backend_acceptance 子代理；未参与本轮生产实现，补充会话隔离测试并执行回归。范围为 docs/12 反馈任务闭环，不代表项目所有 GAP 验收。

最终状态：包括频率规则补修及确定性事务测试在内的当前树，全量与覆盖率门通过。完成时间：2026-10-14 10:12（本地时间）。

## 复核方法

对照 docs/12 输入、冻结计划、逐项事务、恢复、权限和日志合同阅读 feedbackexecution/storage/plan/routes、对应真实 SQLite/HTTP/WS 回归；检查既有最终 profile，再对当前树运行 Go 全量与 vet。未读取密钥或调用真实云 API。

## 发现与闭环

- 架构交叉复核发现前置项终止失败时，依赖项仍可恢复。独立红跑 `TestFeedbackDependencyRetryabilityFollowsPredecessor/terminal_stale_target`，观察期望 false、实际 true；临时存储故障分支通过。红证据 `.test-artifacts/c-dependency-red.txt`。执行器已按前项持久状态派生可恢复性；终止失败及临时故障恢复两分支绿跑。
- 文档要求投诉提交后专属事件，但代码未发出 `feedback_admin_escalated`。独立红跑 `TestFeedbackEscalationLogOnlyAfterCommit`，提交后期望 1 条、实际 0 条。红证据 `.test-artifacts/c-escalation-red.txt`。修复后仅在意图事务成功返回且结果为 escalated 时发出；用例验证回滚不发、重复恢复不重发、父 trace 和原文脱敏，现已通过。
- 新增 `TestFeedbackHTTPTokenSwitchKeepsHistoryNotificationsAndWritesOwned`，使用实际认证 HTTP 请求验证 A→B→A 身份切换、B 无权查询/重试 A 任务、历史与通知隔离，以及相同 submission ID 只改变各自配置。前端生命周期修复另由浏览器代理验收。
- UUID 文案与兼容服务端生成 ID 的实现不一致已反馈主代理；`tr-...` 唯一 ID 符合既定允许字符，不要求为文案替换实现。
- 覆盖波动逐块比对：memory/transaction.go已有记录克隆2语句，以及scheduler/transaction.go的LastReviewAt/FinishedAt深复制4语句，原仅由反馈与结课并发用例间接触达，取决于获得锁的顺序。属于测试覆盖依赖时序，未观察到业务结果错误。新增两包 `transaction_regression_test.go` 预置实际记录、指针和词集合，分别验证commit/rollback，回调内更改副本不泄漏原对象、回滚保留内容/集合/ID序列。架构代理独立静态复核通过，定向及全量绿；memory稳定覆盖93.10%、scheduler92.28%。原并发用例保留。
- API剩余微小覆盖波动定位为ws.go写泵写失败/关闭分支：真实socket关闭与写入先后影响能否进入失败分支。已向主代理说明，不能把一次通过profile当作所有异步分支确定性证明；当前所有运行仍超过API门槛。
- 频率规则修复按完整数量和明确单位解析，不再默认为1或把12截成2；模型候选还要遵守原文明确数量。扩展红→绿同时锁定中文一/二/两/三至十、零/〇、数字0及3–12、复合未知数、空数量、负数/小数、半次和备选次数、分句后的冲突，以及合法的“每天一次，50个词/半小时/10分钟”不被过度拦截。自然语言无明确次数的模型合法1/2候选仍可用。红证据为 `c-frequency-red.txt`、`c-frequency-expanded.txt`、`c-frequency-independent-red.txt`，最终定向绿为 `c-frequency-green.txt`（均在根`.test-artifacts/`）。测试通过不等于真实本机模型已理解这些输入；模型质量另属docs15。

## 可观测性与异常路径

请求拒绝日志有 HTTP trace、身份、路由、稳定错误码；任务事件有 task/attempt/intent、事务结果及耗时，工具审计另带实际工具与资源。原文、token、底层 SQL 错误不得进入日志，已有真实故障回归检查。持久审计失败与业务成功分离处理；投诉专属事件缺口按上列回归收敛。

检查确认：复合键含零索引显式 UPSERT；任务与结果同事务读取；SQLite 第一条写语句锁定任务；副作用与凭证/通知同意图事务；失效资源与数据库故障分类不同；配置版本防 ABA；损坏索引/依赖及未知 schema 在业务执行前拒绝；解析取消不触发规则降级写入；完成项恢复不重做。

## 验收边界

上述修复与身份隔离共3个顶层定向测试通过，证据 `.test-artifacts/c-final-targeted.txt`。随后新增确定性事务及频率用例纳入全量，当前树最终382个顶层测试通过（含子用例623条pass记录），0失败，3个真实云测试跳过：TestChatReal、TestChatJSONReal、TestChatRefuseLongInput。18个internal测试包通过，cmd/server无测试；vet退出0且无输出。57个本轮新增/修改Go文件gofmt检查净。独立复核范围内发现已闭环，无未解决阻断项。

## 最终覆盖率与证据

执行 `go test -count=1 -json -coverpkg=./internal/... -coverprofile=<绝对路径> ./...` 及 `go vet ./...`。工具为本地Go 1.25.14，以Python subprocess参数数组启动，避免PowerShell拆分coverprofile参数。统计按源码块位置去重，任一测试二进制命中即计覆盖；不把每包控制台显示的全instrumented包百分比当成该包覆盖率。普通包内profile另存对照。

| 包 | 覆盖率 | 门槛 |
|---|---:|---:|
| agent | 98.28% | 80% |
| api | 81.22% | 80% |
| auth | 89.94% | 80% |
| classroom | 92.29% | 90% |
| config | 87.59% | 80% |
| feedback | 97.08% | 90% |
| judge | 90.91% | 90% |
| llmzhipu | 100.00% | 80% |
| localmodel | 90.00% | 80% |
| memory | 93.10% | 80% |
| obs | 92.59% | 80% |
| scheduler | 92.28% | 90% |
| scripts | 93.91% | 80% |
| store | 83.13% | 80% |
| tools | 90.62% | 80% |
| voiceclone | 94.64% | 80% |
| voicecmd | 91.40% | 80% |
| wordbook | 90.37% | 80% |

原始证据：`server/.test-artifacts/c-current-go.jsonl`、`coverage-c-current.out`、`coverage-c-current-packages.txt`、`c-current-summary.json`、`c-current-vet.txt`（后四项同目录）。本地测试产物被gitignore排除，本评审保留结果与测量口径。

`-race` 本机无 gcc，未执行；外网模型集成测试需显式 opt-in，默认跳过。Go 不能替代 Playwright 交互、真实语音或模型质量验收。历史 profile 和定向绿跑不作为当前树全量通过证明。

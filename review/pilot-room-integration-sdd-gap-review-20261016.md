# G1 到 Room 接线的独立差距审查

2026-10-16，只读代码/文档审查；未编译、运行测试或修改生产。结论：**正式组件可作为下一阶段基础，但共享数据库的真实 Room 接线尚不具备准入条件。不能只保护 Enter 就开放试点。** 本报告中的阻断针对下一阶段，不否定已通过的组件、梯度或启动验收。

## 1. 当前可证边界

- `store/pilot_store.go:69` 的构造及 `pilot_schema.go:289` 的迁移仍包私有；正式生产调用搜索未见 App/Router/Agent 接线。默认 22 表与现有课堂路径未改为 pilot。
- `pilot_identity.go:31,64,105`：principal 的私有字段阻止普通 JSON 构造；绑定函数本身只验证 ID/task 格式，不是授权。未来 adapter 必须先通过真实受信 task scope 检查。创建事务核 active student、nonce owner、计划指纹及新 scope，返回后才允许发布；不得把任意 task 字符串当 SystemAgent 身份证明。
- `pilot_created_audit.go:109` 明确拒绝非空 transition receipt/answer claim/active command/outbox/completion job；创建重放还只支持 created codec。**已有表不等于已有运行时协议**，写入首个 active 状态前必须同时升级合法状态审计、恢复及创建 nonce 重放路径。
- Go r5 全量 coverage/race 各 1508 pass、9 skip，20 包门通过；browser 聚合为四批次 12 套/131 次 suite-case 执行、130 raw IDs。它们不覆盖尚不存在的 pilot Room 接线。旧 145/127 是历史保存点，不是本阶段新门。
- docs35/36 保留若干“草稿、未编译、梯度未合入”历史段，现状应链接正式组件和 r5/跨批次索引。不能按这些旧时态断言组件没有实现，也不能把后继组件通过推成 §18 已实施。

## 2. 真实接线前的阻断

| 项 | 已有合同与实际缺口 | 开放前要求 |
|---|---|---|
| B1 删除与重放生命周期 | §18.3 与独立 deletion 设计承认缺口。`router.go:209 → Sched.RemoveBook → scheduler_repo.go:154` 只删卡/课；snapshot/report/settled/agent_result、placement 与活动 Room 仍可能留存/回写 | 主审批准数据保留政策、持久删除 operation/generation、pending/done 与重试语义；所有最终写门核旧 token。不能只 Drop 当前进程 Room 或清四个 key 就称跨进程安全 |
| B2 持久 scope/课次模式门未接 | §17/18 清单已存在，但正式 legacy Repo/Room 不使用它。`ErrAlreadyPlanned` 可直接 ReserveCards，未必经过 SaveLesson | 服务首副作用前永久预留 legacy scope，仓储 tx-bound 门纵深；跨范围先全预检。桥接保留并与迁移竞争同 writer；不得每卡全库扫描或缓存 scope 成功许可 |
| B3 finished 只读重进判据未实证 | `hub.go:86–220` 会恢复、RecoverJudgment、teacherLifecycle；`Join:230` 会 activateDelivery、RecoverDelivery、广播动作。仅 phase=lesson_end 或 lesson.status 不能保证 settled 已完成 | 给出 terminal view 与可变恢复的明确分支，核持久完成事实。真实 SQLite 对完成课 Enter/Join/ticket/report 全 DML=0；待结算则仍走受保护可变恢复，不吞重试 |
| B4 active 状态/授权/原子教学尚未实现 | docs33 §6 与 docs36 §§9–13 已设计 command receipt、跨 epoch answer claim、active fencing、outbox/job；正式组件只 created | 严格 active codec、版本上界、auth generation、稳定 logical revision、可信 transition kind、同 tx 工具/audit/checkpoint/receipt/outbox、commit 后发布与未知结果恢复一起实现；禁用 G1 checkpoint 与 v2 双写 |
| B5 ID 与旧仓储语义 | `scheduler_repo.go:100–127,195` 仍 MAX+1、Save upsert；`LoadPlacementDraft:184` 吞 DB/JSON 错误，Service.PlacementDraft 还吞 error | 新旧创建共用 DB 分配 ID；非零 Save 不创建不存在课、不覆盖 owner/book/pilot。草稿 error 签名贯穿 App/路由，不能依据伪空态判定新 scope |
| B6 结算与外部副作用边界 | docs33/36 有冻结 completion job、claim token、done+summary outbox 设计，当前创建组件不支持这些事实 | 最后一题先持久答案及 job，独立幂等结算；重启用冻结输入，不从当前 Room 重造。expired worker 不发布。发送/模型调用均不在事务内 |

删除还需要准确区分两种 tombstone：**scope/bridge 墓碑**保证永不转换；**删除 generation/owner/settlement 重放事实**防旧任务复活。前者不能代替后者。历史无 owner 的 runtime 孤儿不得猜归属或自动删；未迁移启动只做已批准结构门，显式迁移全审失败保持原库，ready-v2 严格审计。

## 3. 必须覆盖的入口，不以 HTTP 检查代替

1. `Hub.EnterContext`、直接 `IssueTicket/CheckTicket`、WS upgrade/Join：先 owner+mode，只读检查不返回未来写许可；活动 Room 构造/恢复前 writer 重核。不得持 Hub.mu 等 DB；复锁后检查已有房间。
2. REST event/voice/resume、WS worker、`ingress.HandleClient*` 及直接 `Room.HandleEvent/HandleVoiceText/Resume/Tick/SessionTimeout/resumeClient`：在 machine/Judge/Feedback/snapshot 副作用前同一保护合同。heartbeat/close/finished-retired no-op 不新增 DB 写。
3. EnableBook、PlacementSubmit、SeedCard(s)、Reset/Plan/ReserveCards、Boost/Postpone、RemoveBook，含直接 Go 服务/仓储调用；API route 不可作为唯一门。
4. `agenttransaction`、`feedbackstorage`、`lessonexecution`、`overdue` 的 tx-bound 服务工厂；复用原 writer/事务，不嵌套 checkout。结算在 rememberFinished/通知/卡写之前保护，过期扫描不得先 retire 再拒绝。
5. 配置梯度协调已按有效范围原子保护卡并拒 pilot scope，但**不等于其它旧写入口已保护**。继续沿本轮 config fresh-work/旧 Room frozen 配置合同，不能恢复过时全局 Truncate 方案。
6. `persistence.saveRuntimeContextLocked` 等最终写点不能仅信早期内存 Room：若支持删除/跨实例，提交时必须携 scope generation 与授权事实；否则旧模型回调能在删除后重建 snapshot。

## 4. 推荐的最小下一交付片：受信 lesson.start 持久迁移内核

推荐先推进 **created → lesson.start 的单一可信迁移**，而不是开放混合 DB 试点或把旧 Room 接一半。这是能独立验收的新实现片，不是再交付一份方案；但它仍不是可用真实课堂。

范围：在独立 SQLite 和 opt-in test harness 中，从已验证 creation receipt 出发，受信 Teacher 决策表规划首批课堂效果；同固定连接 writer 内验证 owner/auth generation/checkpoint version、command fingerprint，写 start receipt、严格 started checkpoint、工具 audit 和有序 outbox；成功后返回已提交事实。重复 command 返回同 receipt、不同 fingerprint 拒绝；旧 generation 拒绝。**不发声音、不调用网络、不生成答题 claim、不结算、不暴露普通创建/旧写接口。**

接口/文件建议（需新片设计批准后实施）：

- `store/pilot_transition.go`：包内 `commitPilotStart(ctx, principal, expected, plan)` 与只读 receipt lookup；外层不得传任意 SQL callback/任意 machine state。成功日志仅 commit 后发布。
- `store/pilot_active_codec.go`：只增加明确 started 状态及相应 room facts/outbox codec；未知 future kind 继续拒绝。`pilot_created_audit.go` 改按生命周期分派，不能删掉空表拒绝后接受任意 JSON。
- `api/pilot_start_plan.go`：无 App 注册的受信 adapter；复用 Teacher 决策与工具 scope/schema，构造 detached candidate；不得经现有 `callAgentTool` 再嵌事务或提前写全局服务。
- `pilot_identity.go`：创建 nonce 重放核原冻结 creation facts，并允许已合法 started 的现有课返回原创建 receipt；不能重新 Start，也不能用当前词/config 改写旧指纹。
- test-only harness：提供受控登录、创建、启动、receipt 查询及故障装配；普通 Router/WS/Timer/工具注册表完全不安装此入口。浏览器只证明“开始已保存/重连查询同结果”，不宣称真实课堂教学或物理播放完成。

这一片不提供任何生产可调用的 pilot 写入口，因而不会在尚未保护的 legacy Router 上暴露 pilot 数据。显式不支持：旧 DB 在线迁移、旧 Lesson 转换、用户可用 pilot UI、答案/voice/跟读/控制/Timer、ACK 投递、全课结算、删除/重新选库、多阶段教务反馈、真实模型/真人音频。不得将 harness 受信创建当作产品授权完成。

如下一交付目标必须是“可用真实试点课堂”，则这个片不能冒名替代：需主审另行选择**完整共享 DB 入口闭环**，或批准 **独立 pilot-only App/账号/SQLite/端口与显式 Router allowlist** 替代路线。独立目录本身不是安全边界；不得挂默认 legacy Router、旧 Timer、旧调度/反馈工具，错误 DB/模式必须拒启，且直接 Go adapter 的能力边界仍需测试。这一替代尚未获准，不能默改 docs36。

## 5. 下一片必要用例与日志

- Go 双池：同 command 重放、不同 fingerprint、旧 auth generation、并发 Start 只有一个 checkpoint/outbox/audit、context cancel、真实 deferred COMMIT 失败原/外连接零部分记录、失败后可用、保存后发布前崩溃恢复。
- codec：created/started 混淆、篡改配置/词/计划、非规范 JSON/重复/null/数字溢出、未知 outbox kind、creation nonce 在 started 后重放不产生新效果。
- browser：学生 A 开始响应丢失后只查原 receipt、不自动重发；B 的创建/状态保持不变；跨账号 receipt 拒绝；重启显示原已保存事实；普通 legacy 深链/REST/WS 不能访问 harness pilot DB。测试与生产路由隔离单独断言。
- 固定事件：`pilot_transition_started/committed/rejected`、`pilot_receipt_replayed`、`pilot_state_restored`；同 trace、lesson/student、command 摘要身份、checkpoint 前后版本、固定 reason、writer/transaction 时长、replayed。prepared 不报 committed，未知提交不报 rollback；不输出文本、token、epoch、SQL、路径或模型 body。

真实 Room 开放另需 §18 全入口负例、finished 全 DML=0、多实例删除 fencing、旧 writer 与迁移/创建竞争、outbox delivery 与 effect.ended 去重、completion claim 过期和结算重启 E2E。不能拿上面的 start 内核用例替代。

## 6. 文档补齐项

已有：docs33 原子工具/发布/结算边界；docs36 §§15–17 创建、scope、桥接和 schema 缓存合同；§18 的大部分入口及负例。

待补：§18 删除 operation/generation/owner tombstone 的精确 schema、清理与保留政策；finished terminal-view 判据与 error 接口；active codec/creation-replay 生命周期兼容；所有实际工厂与直接 Go 调用点签名；下一片 start-only 的批准范围与 test harness 授权。docs35/36 应追加正式组件/r5/131 聚合现态，保留历史失败但消除当前状态混读。

架构结论：完整 Room 接线仍 blocked；以上 start-only 内核可提交新片设计评审。安全结论：不以 opaque type 自证授权、不以独立测试 DB 自证路由隔离。AI-Agent 结论：沿 Teacher 表生成效果；跟读沿 docs38 RepeatMatch、quiz 评分不改；短转写探索失败不能靠该持久层工作宣称真实模型准入。

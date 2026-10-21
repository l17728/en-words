# lesson.start 持久迁移内核 SDD：后端复审 3

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md`（2026-10-16 当前稿）。本轮仅只读核对 SDD 与 `pilot_schema.go`、`pilot_created_codec.go`、`pilot_created_audit.go`、`classroom.State/Machine`、`tools.Registry`、教师工具 handler 及既有审核记录；未修改实现、未运行测试。

## 结论

**BLOCK：不建议进入编码。** 本版已解决首轮提出的大部分事务顺序、回执幂等、`active` 映射、冻结创建事实与 unknown commit 问题，且 start-only 隔离边界可实现。但审计字段仍与现有 Registry 的实际行为矛盾；在现有 v2 schema 下，`active` checkpoint 还要求明确 lifecycle verifier 的受限分派，否则任何现有 `auditPilotCreatedDataTx` 入口仍会拒绝它。还有少量可执行性合同需补齐。

## 逐项结论

| ID | 结论 | 发现与修订建议 |
|---|---|---|
| BE3-1 | **BLOCK / P1** | §4 要求两条工具审计 `Result="queued"`，但 `tools.Registry.Call` 的成功路径固定产生 `AuditEntry.Result="ok"`，`classroom.say` 与 `classroom.show_word` 都走该路径。`queued` 是 outbox 持久状态，不是 Registry 工具调用结果。若在落库时改写为 `queued`，就不再是原始 Registry audit。建议把 `audit_logs.Result` 固定为 `ok`，由 outbox `delivery_status=pending` 与 receipt `result=queued`表达排队；或明确一个有版本的 pilot audit 映射规则，并保留原始 Registry 结果另作验证，不能笼统声称“审计来自 Registry”。 |
| BE3-2 | **BLOCK / P1** | SDD 说“生命周期审计/创建事实分开校验”，但没有冻结所有数据库读写入口如何调用分派。当前 `auditPilotCreatedDataTx` 明确要求所有后继表为空，且 `verifyPilotCreatedReplayTx` 假设 `lifecycle=created`；只新增 start writer/verifier 会使 `inspectPilotSchema`、创建 nonce 重放或普通审计仍拒绝合法 active 行，或者产生多个互不一致的验证路径。需列出受支持的 pilot facade 入口及统一事务内 `created`/`active` 分派规则；created 检查继续严格，active 只由独立 codec/verifier 校验。不要改变旧 Room/默认 server 路由。 |
| BE3-3 | **BLOCK / P1** | §3 的“开始计划按序调用 `classroom.say`，再由 `Machine.Start`/`PlanEffects` 调用 `classroom.show_word`”，§4 又说两行审计 `Result=queued`，但现有 `tools.Registry.Call` 成功 audit 为 `ok`，且 Registry 会调用其 auditor。`PilotStartExecutor` 可用 task-local `MemAuditor` 缓冲后同事务写库，但 SDD 必须明确 auditor 捕获两条原始 `AuditEntry` 后如何核验和映射；不能既说复用生产 Registry，又把其记录字段改造成它不会发出的 scope/effect_index/schema 元数据。建议 audit `Args` 保持生产管线的 `{"bytes":N}`；scope、effect index、参数 schema 由 verifier 依据固定工具定义、顺序及 outbox 单独验证。 |
| BE3-4 | **BLOCK / P1** | 现有 `classroom.show_word` handler 位于 `teachereffects.go`，要求 `teacherEffects` context，验证 task pointer、Room/App 及 `Teacher.PlanEffects`，并返回 `classroom.Action`；它不直接做 I/O，因而可在隔离 App/task context 中复用。但 SDD 的 executor 构造/调用流程没有明确该 frozen-effects context 的产生者，也未说调用 `Registry.Call` 时 auditor 绑定在 Registry 实例（Registry auditor 是构造时固定字段），而非普通 context value。补一条无 Room 的 typed adapter 流程：由隔离 adapter 持有固定 App、task 与 actions/state，注册同源 handler，Registry 构造时绑定 task-local buffer；调用只准 say 与索引0的 show_word，不能共用产品 App 的全局 Registry/DB auditor。 |
| BE3-5 | **BLOCK / P1** | 已有 v2 DDL 的 lifecycle 允许 `active`；checkpoint FK 允许 `active_command_id=NULL`，且 auth_generation=0/epoch NULL 可通过 CHECK。receipt/outbox 字段容量足以装本片所需数据，不需要新 DDL。但 active machine JSON 与 created `room_facts_json` 的变更必须以固定字段分派进入 schema verifier；created verifier 当前拒绝任何 receipt/outbox。SDD 已指出需分离审计，但 §7 又说还要核定 codec/audit/outbox“能否由 v2 DDL完整表达”，与正文已冻结字段仍显含糊。请明确：DDL 继续严格用 v2；只变 active 行数据、由 codec-verifier 做一致性审计；若确需新列则另起 schema 版本。 |
| BE3-6 | **APPROVE** | 幂等顺序与现有 receipt 键 `(lesson_id,command_id)` 对齐；授权后先 receipt、再 created CAS 可支持响应丢失恢复。FK 允许先 checkpoint 再 receipt、outbox 通过 receipt FK 同事务插入；结果未知时不将 `not_found` 当回滚的规则正确。`auth_generation=0` 及 NULL epoch 也与现有 v2 CHECK 相容。 |
| BE3-7 | **APPROVE WITH CLARIFICATION** | v2 outbox 主键 `(decision_id,effect_index)`、receipt 唯一 decision ID 与本片两条有序 effect 相容；`active_commands` 没有强制必须存在记录的 DDL 约束，所以空表可合法。明确 `kind` 具体为 `classroom.say`/`classroom.show_word` 还是 UI action kind，并冻结精确 payload schema；现有工具 `show_word` 输入为 `{"index":0}`，handler 返回 Action，不能把输入参数误当作 outbox payload。 |
| BE3-8 | **APPROVE WITH CLARIFICATION** | 受限 harness、独立 SQLite、loopback ephemeral listener、默认 server 不依赖 test package 的方向可实施；需让 build 边界可由源码/构建图验证。Go build tag 是文件级编译选择，不是运行时安全边界；确保 harness 目录内所有源码均带 `//go:build pilot_e2e`，默认 `go list ./...`/正式 binary route 检查看不到 package/route，并在文档中说明 test harness 仅用于验证，不能开放真实课堂。 |
| BE3-9 | **APPROVE WITH CLARIFICATION** | 双连接 writer barrier、held-writer unknown、COMMIT 后丢响应钩子都比一般 mock 更有证明力。E2E 中的钩子须由 harness 服务端在 COMMIT 后、响应写出前触发，并用独立 DB 连接断言 receipt/checkpoint/audit/outbox；浏览器只通过同一 HTTP 服务恢复。另应明确“COMMIT 前中断”是已证明 rollback 后才可断言无记录，超时/连接断开单独仍是 pending_unknown。 |
| BE3-10 | **APPROVE** | `classroom.State` 字段集与代码当前定义一致；`NewMachine` 初始化五个 map、Machine.Start 只附加 show-word action，codec 把非空数组/map 显式编码且不直接 marshal State，可复现。显式 EffectiveSnapshot golden JSON 的大写字段对应 Go 当前无 JSON tag 的字段命名。 |

## 需在编码门前完成

1. 修正/冻结 Registry audit `Result` 与 `Args` 的映射，不能与 `tools.Registry.Call` 成功路径冲突。
2. 写明所有当前 pilot facade 的统一 created/active 生命周期审计分派；保留旧 created-only 失败关闭行为。
3. 补充无 Room 的 isolated `App + task + teacherEffects context + Registry(task-local auditor)` 组装与纯调用序列，明确不触及全局 auditor/Room/外部副作用。
4. 明确 outbox 的 `kind`、payload 精确字节/字段 schema，并区分 tool input 与 tool result。
5. 做一次小型可行性原型前还须先由架构/安全/测试域复审本版修订。此 SDD 的设计门仍未通过，当前不得据此编码或宣称可以启动课堂。

评审范围未覆盖生产路由接入、真实模型/音频、旧 Room 兼容或课堂试点；这些在当前 SDD 中明确排除，继续作为开放事项。

# Start 内核 SDD 第四轮架构评审

2026-10-16，只读复核。读取版本 SHA256：`0a26c33db1ee14a72b486cd2cda8aa53073d6f32e5240fee1bd7e9a768022b12`。未运行测试、构建或服务，未改产品及规格。

结论：**2 项 P2 设计阻断，需要明确后再实施。** 第三轮审计计数 P3 已清零：§6.1 现在要求两条审计、工具各一次。

## 通过项

- D15：say 与 Machine.Start → PlanEffects → show_word 均经 Registry，两个真实成功 AuditEntry 对应两条 queued outbox，不把工具执行成功混成已播放。
- Machine 自定义初始 codec 字段与当前 State 对齐。active room_facts 已明确 teacher_text、phase_since，其余未启用字段保留 null/空容器；outbox 为唯一待发送事实，不复制到 pending_output_effects。
- 现有 DDL 可表达 active/version2/auth0/revision0、receipt 1→2、pending outbox。next_attempt_at/created_at 明确为 start_time_ms；claim/lease 为空符合 CHECK。
- Agent 新增固定 T-01 渲染方法是明确计划新增 API，不冒充当前已有方法；不随机口语化、不使用历史成绩。冻结释义补入 show_word 的 typed action 后再交 capability handler，与现有 index=0 handler 可以衔接。
- 当前 owner/账号校验先于 receipt replay，冻结创建配置不被 live 配置覆盖；默认产品未接线、隔离 harness 不代表真实课堂交付。

## 阻断

### START4-01：按课分派仍调用全库 created-only 审计

docs41 §4 要求 `auditPilotLifecycleTx(tx, lessonID)` 在 created 分支调用现有 `auditPilotCreatedDataTx`。实际 `server/internal/store/pilot_created_audit.go:110–160` 对五张后继表做全库 EXISTS，并遍历所有 pilot reservation。A 已 active、B 仍 created 时，B 分支会因 A 的 receipt/outbox 拒绝；即使只恢复 B，也不能通过 facade 的启动全库 audit。

应明确拆为：数据库级结构/FK/孤儿/未支持事实审计 + 每课不可变创建事实审计 + 每课 created/active 生命周期审计。created 的“后继事实为空”必须限定该 lesson，不能保留全库断言；active 不得绕过创建冻结事实校验。全库 active_commands/answer_claims/jobs 仍应为空。审计入口统一遍历后再逐课分派，不能逐课重复运行旧全库函数。

回归必含同一 DB 的 A active+B created，冷启动 facade、两方 nonce 重放、B Start，以及跨 owner 孤儿/错挂 receipt/outbox 拒绝。现有 created-only 测试应保留语义，但不能保留与混合合法生命周期冲突的全库假设。

### START4-02：bundle 生成时点、所有权与丢失后的恢复未定型

§1/事务步骤⑤仍称 writer 内执行工具；随后又规定每 command/task 只生成一次不可变 bundle，rollback 重试复用、不可重建 task/context。需明确是首次 writer 内生成，还是预先生成后 writer 只验证。当前 `taskFromContext` 会拒绝取消的 context（api/agenttools.go:81），不能把旧请求 context 直接当重试上下文。bundle 只在内存时，COMMIT 未知后进程重启、查不到 receipt 又禁止重新 Registry 调用，未定义合法终态；双 pool 同 command 也需要明确共享同一个受信 bundle，不能各生成不同 TS/Duration 的 audit 再声称完全相同 bundle。

最小方案无需新增通用日志表：固定首次执行时点；由可信协调器持有私有字段、深复制的 bundle，绑定 command/decision、fingerprint、task、owner/lesson，区分原始执行身份与每次新请求的当前授权/取消。同 command 并发只允许一次生成；重试仅持有原 bundle 才允许进入 writer，receipt 命中不依赖重新生成 bundle。若原 bundle 在进程退出前未持久提交且已丢失，明确拒绝该命令继续执行并保持查询/未知边界，不自动重新生成；如希望支持这种恢复，应另定义持久方案，而不是无意扩本片。补取消后同 bundle 重试、双池共享命令、提交后重启查询、未提交 bundle 丢失四类用例。

## 实施备注（不单列阻断）

- §3 写“命令、指纹、工具参数、审计/outbox都携带释义”，但实际 show_word 参数只有 index，audit 仅元数据。应表述为冻结 effects context 和 typed outbox 携带释义，参数/audit 不复制正文。
- `pilot-audit-map/v1` 目前是协议约定，Args 的 schema_version=1 指工具 schema；不要声称已经持久化独立 mapping-version 字段。可由 receipt kind/协议限定唯一映射，未知映射拒绝。
- 每课检查不得将其他协议既有 audit 判作孤儿。新 active codec 与创建 nonce replay 分派需要同时实现，不得单独放宽 audit。

此报告只审批设计，不证明 SQL、跨进程恢复、E2E 或真实模型/音频已通过。

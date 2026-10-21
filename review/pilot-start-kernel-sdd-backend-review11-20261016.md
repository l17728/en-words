# lesson.start 持久迁移内核 SDD：后端复审 11

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新稿）。只读复核上轮 BE10-1 permit 一次性消费问题，并扫描 ExecuteTool/Coordinator/API package 边界、SQLite 事务/receipt 恢复及 schema/build-tag 合同。对照现有 Go store/api/Registry/atomicConnection；未修改代码或文档，未运行测试。

## 结论

**PASS（BE10-1 已关闭）。** 新稿规定 PilotToolPermit 通过共享指针承载 atomic CAS 状态；API 的 PilotRegistryOnce 独占裸 Registry，先逐项校验 permit 绑定、再 CAS 消费，最后恰好调用底层 Registry 一次。Coordinator 只在 tool_started 事件 Flush+Sync 后签发 permit；HTTP、Planner/adapter 外层及 API 其他路径不得持有裸 Registry 或绕过 handler 引用。重复/复制/错绑/错误 tool 的负例要求在 handler 前拒绝，permit 消耗后的 panic 也不可重试。这个合同已经解决上轮 P1，包结构没有 Store→API 依赖或新增 import cycle。

未发现新的 P1/P2 设计阻断。一个低风险接口细节建议在实现中显式落为 Store 的窄 verify-and-consume 方法，供 API wrapper 使用，避免 API 包需要读取 Store 私有字段。

## 重点核查

| 范围 | 结论 | 依据 |
|---|---|---|
| Permit防伪/复制/重复调用 | CLOSED | 共享指针让复制 Go 值共享 consumed 状态；wrapper 校验 command/plan/attempt/effect/tool 绑定后先 CAS，CAS 成功才调用裸 Registry；handler panic 时 permit 仍已消耗。 |
| Registry唯一入口 | PASS | PilotRegistryOnce 是 API pilot_e2e 唯一裸 Registry 所有者；adapter 只经 ExecuteOnce 提交单项 invocation；其他组件无裸实例/绕过 handler 引用。计划事件先 Sync、工具启动事件先 Sync，handler 进入前有 sidecar barrier。 |
| Planner/Coordinator/包边界 | PASS | Store 定义 opaque token、invocation、Planner 接口及 Coordinator；API pilot_e2e adapter 实现 Planner 并构造 API 私有 task/context；cmd harness 组装，Store 不 import API。构建标签统一排除生产二进制。 |
| Sidecar attempt / ID 审计 | PASS | attempt_started→plan_ready→每工具 tool_started→tool_result→bundle_ready→commit attempt；HMAC 分别绑定 command、plan、bundle，task/trace 仅以 HMAC 落 sidecar；DB trace 通过 receipt.decision_id 关联。permit attempt_id/effect/tool 与调用顺序一致。 |
| SQLite / COMMIT unknown | PASS WITH IMPLEMENTATION GATE | Planner/Registry/sidecar 全在 writer 外；writer 内二次授权与冻结数据检查后原子写入。COMMIT unknown 只查 receipt；同进程、同 bundle 且 pinned connection typed rollback_proven 才最多重试一次。当前 atomicConnection 尚无该 typed outcome，必须在实现中明确区分。 |
| schema / lifecycle / build tags | PASS | v2 DDL不扩列；全局 legacy/FK 审计后逐课 created/active 分派；pilot_marker、audit、receipt、outbox 和冻结 payload 的匹配合同延续。默认构建排除 store/api/harness pilot 文件。 |

## 低风险接口建议

Permit 字段对 API 包私有，因此 PilotRegistryOnce 不应尝试读取 permit 字段。建议在 store 的 permit 类型提供仅供验证/消费的一次性窄方法，例如以完整 Store invocation 为参数执行 validate+CAS，失败原因固定且不暴露 permit 内部标识；方法返回后 API wrapper 才能调用底层 Registry。该建议不会改变现有设计边界，只把跨包可实现的调用点写成显式签名。

实现验收仍须验证：复制和复用 permit 在实际 handler 前拒绝；错绑命令/计划/attempt/index/tool 同样拒绝；合法调用恰好一次；handler panic 后不可重试；无 pilot_e2e tag 的正式 server 构建图无 adapter/route；有 tag 的 Store/API/harness 可共同编译；SQLite typed rollback proof 不会把 unknown 误报成 rollback。

本轮是后端设计复审，不代表代码已实现或测试已通过。

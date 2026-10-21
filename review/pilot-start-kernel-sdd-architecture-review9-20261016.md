# Start 内核 SDD 第九轮架构复核

2026-10-16，只读版本 SHA256 `4e3f7f70dabafeb722030862df6973d85f976353892f39d3bbb2f8469a5da6c3`。未运行测试、构建或服务，未改规格/代码。

结论：**BLOCK，1项P2接口闭合问题；另3项P3文字/验收同步。** 本轮针对新增逐工具事件设计，不撤销review8对当时修订的通过记录。

## START9-01：逐工具耐久屏障尚无可实施接口（P2）

§3公开接口仍为 Prepare 返回计划、Execute(ctx, PlanningContext, PilotStartPlan) 一次返回完整bundle；case级Coordinator持有sidecar，API adapter负责两个Registry调用。新事件合同要求每个调用之前Sync tool_started，之后Sync tool_result，任一失败立即停止且不能执行下一工具。但接口未说明Coordinator如何在Execute内部插入这些有错误返回的屏障。

现有Registry auditor的 `ToolCalled(AuditEntry)` 是返回后通知、无error返回，不能单靠它阻止当前handler先于tool_started落盘，也不能以吞掉Sync错误的observer保证下一工具不执行。不能直接让Planner获得任意sidecar写权限以绕开固定schema/序号/plan绑定。

最小闭合：增加只允许固定plan与固定工具顺序的受限typed attempt recorder/permit，明确 BeforeTool 必须完成Sync才返回permit，AfterTool校验真实AuditEntry并Sync；任何错误使Execute终止。或者Coordinator按序调用受限 ExecuteTool，由Coordinator包围每次Registry调用。接口必须写清所属包、输入字段/私有绑定、error传播、panic后的记录归属；不得暴露SQL或任意事件JSON callback。

回归：say的before-Sync失败→两个handler均0次；say返回后result-Sync失败→say1次、show_word0次；show_word之前断点恢复unknown；同一permit不能复用/跨command/错工具，所有失败无生命周期行。

## 新增设计通过项

- attempt_started → Prepare → plan_ready → 两组 tool_started/tool_result → bundle_ready 的逻辑顺序合理；计划HMAC先于调用，工具开始先于handler，弥补“已调用但结果未记录”的观测窗口。
- plan_hmac绑定最终命令、模板来源及固定工具序列；bundle_hmac进一步绑定plan与bundle。新增事件字段在allowlist和新attempt golden中都出现，旧无plan字段的golden已替换。此次只核文档字段/顺序，不声称运行过HMAC golden测试。
- task/trace用HMAC关联，role/scope/risk来自受信能力与真实审计，拒绝Planner自报；成功DB audit与sidecar分账。
- receipt-first只在当前授权及完整DB事实校验后适用；坏sidecar令case禁止新命令，未决不重跑工具，原有恢复边界保留。
- ACL与保留期明确限于可信监督器OS账号威胁模型；不声称抵抗管理员或同账号入侵。监督器拥有case、成组清理DB及sidecar/key、失败隔离不复用，政策方向一致。Windows目录句柄/SQLite实际打开及ACL继承需实现时用真实平台测试证明，文档承诺不是已有能力证据。

## 旧段落同步（P3）

1. 第47行无receipt未决状态枚举漏plan_ready/tool_started；虽然第46行与新增崩溃用例已说明tool_started恢复unknown，仍应把优先恢复表列全，避免严格switch遗漏。
2. 第24行要求“叮叮老师”与产品定名/Golden“叮当老师”冲突；改回叮当。同段“工具参数/审计都携带词面释义”仍与index-only参数/元数据audit不符，限定为冻结context与typed outbox。
3. §6旧“每次尝试恰一条脱敏记录”与现在一attempt多事件链矛盾；应按event_type、工具名及合法前缀计数，例如完整成功有两组tool_started/tool_result，不能仅断总记录数1。

这些修订不要求增加模型/网络或旧Room功能。本报告不代替代码审查、覆盖率和真实崩溃/E2E验收。

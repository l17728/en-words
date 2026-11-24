# Start 内核 SDD 第二轮架构评审

2026-10-16；只读对照 docs41、docs33 §6、现有 State/Teacher 工具及 pilot DDL。未运行测试、构建或服务，未改规格或产品。

结论：**1 项 P2 阻断，暂不批准实施；范围与其余合同通过。**

| 项目 | 结论 | 依据 |
|---|---|---|
| START-SDD-01 生命周期/DDL | approve | active/version2/auth0/epoch NULL/revision0 可以由现有 `pilot_schema.go` 表达；两条 pending outbox 无 lease 不授播放权。必须扩生命周期 verifier，不能放宽旧 created 检查。 |
| START-SDD-02 重放 | approve | 当前授权在先，receipt 在 created/CAS 之前；旧回执不产生执行权限；unknown/not_found 不冒充 rollback。 |
| START-SDD-03 冻结配置 | approve | 使用创建冻结词条/配置；live 配置改变不使 Start 失效；账号/归属仍实时复核。 |
| State codec | approve | `classroom.go:169` 的 State 字段均有对应；QuizItemID/判断枚举与 `config.go:48` 的 EffectiveSnapshot 字段一致。自定义毫秒时间和空容器是显式协议，不冒充直接 marshal State。仅允许初始空 quiz/maps，不能借此支持活动课堂。 |
| START-SDD-04 Agent 工具 | **block** | 受限执行器方向正确，但把 show_word 排除于工具链，仍违反现有 Teacher 能力映射与 docs33。 |
| 隔离/授权/恢复 | approve | 专用带 tag harness、真实数据库与 A/B 授权测试、默认入口不接线及 COMMIT 后丢响应边界清楚；没有真实课堂/声学验收承诺。 |

## START-SDD-04 尚未闭合

位置：docs41 §3（约第37行）声明 `Machine.Start` 不是工具调用；§4（约第53行）限定仅一条 `classroom.say` audit。

Machine 计算动作本身可以是纯状态机操作，但 **将 ActShowWord 转为待发布效果的能力调用不能因此省略**。当前 `agent/teacher_effects.go:14–21` 的 `Teacher.PlanEffects` 明确映射 `ActShowWord → classroom.show_word`；`api/teachereffects.go:36–89` 已有受信 task、冻结 facts、index/schema/scope 及 capability 匹配的纯 handler。docs33 §6 要求提交可信计划时经同事务绑定工具和审计。此版直接把第二效果写 outbox、只审计 say，会形成首片的工具旁路。

最小闭合：保留 Machine.Start 的纯候选计算，再经 Teacher.PlanEffects 取得 `classroom.show_word`，与 say 一起走受限 Registry/冻结 facts 验证。仍只有两条 outbox、不发送媒体；明确两条真实工具审计及各自受限 Args，按 Tool/decision 关联核唯一和完整性。不能继续使用“TraceID 恰一行”断言；改为恰有两种指定工具各一行，拒绝额外/缺失/重复。E2E/故障矩阵的 audit 数量同步；任何一项工具失败必须全事务回滚。无需扩展业务功能或修改 DDL。

实现前细节：receipt 的实际列名是 `logical_input_revision`，outbox 是 `delivery_status`；文档短写不能变成新列。`started_at` 必须按 UTC 毫秒恢复且 config 使用冻结原值。审计 orphan 检查要限定本协议事实，不能将既有其他工具 audit 判作孤儿。

本结论只审批隔离 Start 存储设计。旧入口保护、删除屏障、完整 Room、outbox consumer、真人与模型能力仍未完成；本轮不提供实现/SQL运行证据。

# docs41 Start 内核架构/安全/Agent 评审

结论：**范围方向通过，实施准入暂缓，4 项 P2 设计阻断需闭合。** 仅只读检查 docs41、docs33/36 和正式 store/Teacher 相关代码；未测试或改生产。

## 已一致的边界

独立临时 SQLite、包私有提交内核、无普通 App/Router/Timer 接线、无真实媒体/模型、无答题/结算、不开放旧写入口，符合此前最小片建议。opaque principal 不等于授权、固定 writer、commit 后发布、未知结果只查原 receipt、创建事实不可被当前配置覆盖，均保留正确方向。本片不是 Room 接入或可用课堂。

## 编码前阻断

### START-SDD-01：started 的精确持久表示未冻结

docs41 §§1/3/4 以 started 作为生命周期，但正式 `store/pilot_schema.go:87` 的 CHECK 仅允许 created/active/completed/retired。不得在实施时静默加枚举或放宽 schema verifier。建议“started”仅作本片语义名称，持久 `lifecycle=active`，并冻结 phase、logical revision、question key、checkpoint version、active_command、auth generation/epoch 和完整 room facts 的具体值。`Machine.Start` 只追加首个 showWord action；不能把它等同已播放、已创建输入机会或整个 teacherLifecycle 已执行。

创建基线 auth_generation=0、epoch=NULL；本片未包含 connection takeover。必须选定 Start 仅接受内部 generation=0（outbox 不授播放 lease），还是纳入受信接管并给确定接口。还需给 outbox/audit/receipt 的唯一性与外键关系表、工具数量/顺序及字段上界，不能把 §7 问题留给编码者临时决定。

### START-SDD-02：重放与 CAS 的顺序存在歧义

§3 同时要求 created-only/expected version 匹配和同 command 原 receipt 重放。成功后 checkpoint 已不是 created、version 已推进，若先 CAS 就会把合法重放拒绝。沿 docs36 规定：当前 owner/账号及读权限核验 → 同 command receipt/fingerprint 查询 → 命中返回持久事实（不授新媒体权）→ 未命中才检查 created、generation、expected version 并提交。不同 owner 不得先获知 receipt 存在；同 ID 不同 fingerprint 冲突。原 nonce 在 active 后只重放创建事实，不能要求当前状态仍 created。

补双池两种顺序：第二请求到达时第一已提交；第二请求与第一同时进入 writer。两者均只产生一份 receipt/effects。新 command 请求已 active 课固定拒绝。

### START-SDD-03：并发 live config 变化的测试合同与冻结事实冲突

§6.5 写“并发配置/owner/schema变化严格 stale/拒绝”，会把合法创建后管理员改配置变成永远无法 Start。docs36 与现 `pilot_identity.go` 明确 creation plan/config/words 为冻结事实，同 nonce 重放不比较 live 配置。

应拆分：当前 owner 被停用/授权失效、模式/schema变坏、checkpoint/version/generation变化须拒绝；live config/词条随后改变不重造候选，Start 仍使用 creation 时保存的冻结内容。候选指纹绑定原 creation/content fingerprint、计划版本、稳定 Start 时间/输入与 expected checkpoint；若需要不同政策，须明确为新设计变更，不能借测试隐式修改。

### START-SDD-04：可信工具执行与审计来源未对齐

§3 禁止 writer 内“Teacher、工具”全部调用，而 docs33 §6 要求受信 tx-bound 工具执行/审计与 receipt/checkpoint/outbox 同事务。两者需区分：Teacher 决策/模型/候选形成在事务外；事务内允许受限、无外部副作用的工具 scope/schema/事实匹配验证及审计，禁止既有 `callAgentTool`、二次事务、全局 a.DB 和网络。

若本片只持久化规划结果而不执行注册表工具，必须把记录命名为“plan validation”，不能伪造 tool committed 审计或声称实现 docs33 执行器。本项目 Agent 动作经工具的合同要求明确选定一个方案。现 `registerTeacherEffects` 依赖受信 task/App，且 handler 内调用 PlanEffects；需给窄 adapter 拆分点，不能直接套现 Room teacherLifecycle。另应明确首片是否包含 classroom.say；不包含须说明它留待后续 Start consumer，不能称完整课堂开场已执行。

## 非阻断但需随稿锁定

- §7 的 harness 方案应定案：真实 test-only HTTP →真实 store transaction 的双用户 UI/协议 E2E；route mock 只能注入丢响应，不可 fulfill 虚构成功替代数据链。默认 production routes 不存在检查与 harness 行为分账。
- COMMIT error 结果未知与确定 rollback 保留区分；not_found 单独不等于 rollback。复用已批准固定连接清理与 writer 后查事实，禁止自动换 ID。
- §6“任何 skip 都保留在失败分母”建议明确 planned/attempted/pass/skip 分账；预声明 opt-in real-provider harness skip 不冒充失败，也不得算执行通过。
- 新 SDD 应继续独立，docs36 §18 只加依赖/范围索引。旧删除/混合 DB/finished 零写阻断不在本片解决，也不允许通过本片验收清除。

修订以上具体合同后可快速交叉复审；无需扩大到回答、媒体派发或完整 Room。

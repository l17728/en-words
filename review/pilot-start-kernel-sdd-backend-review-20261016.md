# lesson.start 持久迁移内核：后端独审

评审对象为新版 `docs/41-试点课堂Start持久迁移内核SDD.md`，不沿用旧 legacy-fence 草案的阻断。结论：隔离范围和事务方向正确；以下具体兼容合同未冻结前，暂不批准编码。未修改代码或运行测试。

## SK-B1：started 与现有 schema/codec 的字段映射缺失

正式 `pilot_schema.go:87` lifecycle CHECK 仅允许 created/active/completed/retired，直接写 started 必失败。需明确 started 是领域名称映射 lifecycle=active 加严格子版本/phase，还是新增 schema 版本与显式迁移；不应默默修改现有 v2 CHECK 然后称旧 v2 等价。首选无需 DDL 变化的窄映射，但必须逐字段冻结。

还需列 checkpoint 1→2、logical_input_revision=0、question_key=null、active_command_id=null、auth_generation/connection_epoch 的明确值；本片不创建答题机会，不能为了满足 active_commands 的 revision>0/question_key/generation>0 条件造假输入。created 初值 auth=0/epoch=null；若 Start 必须 auth>0，需新增受信接管步骤及其版本合同，不能由 fixture 直接伪造声明。

## SK-B2：幂等次序与查询键必须贴合既有 DDL

transition_receipts 的主键为 (lesson_id,command_id)，不是全局 commandID。建议 lookup 明确 lessonID+opaque principal+commandID；只用 owner/commandID 可能跨该 owner 的课产生歧义。lookup 必须重新核当前授权，不接受调用方传 owner 即视为凭证。

writer内先核身份/scope授权，再查询同课同command receipt并比fingerprint，合法重放返回原值；**只有新提交**才检查 created、expectedVersion 和 CAS。否则第一次提交递增版本后，同ID重放会被旧expectedVersion拒绝，违丢响应恢复。另需固定同一课不同commandID的第二次Start返回already_started/conflict，不能新建第二receipt。并发验收须含这两种情况。

## SK-B3：detached plan 的可生成内容及冻结来源尚不具体

已存 frozen_words/config/revisions 是本课事实。§6“配置变化得到 stale”与“created/started nonce不按当前配置重建”需区分：Start应使用已存冻结课配置，不因当前全局配置正常变化令旧课永久无法开始；若计划依赖额外可变策略，应单列其版本、检查与拒绝理由。不得无意把 live 配置重新引入冻结课堂。

需指定纯 Teacher planner/工具schema校验接口及有限effect清单、固定排序、数量/字节上限、时间/segment/decision ID来源。不能调用现有会改Room或同步审计的 teacherEffects 后再称 detached。候选须私有构造、深拷贝，提交前重算canonical fingerprint；不能让调用方直接填写机器JSON或工具审计“已执行成功”。outbox未派发时审计应表述计划接受/持久化，而非工具外部效果已完成。

## SK-B4：audit/receipt/outbox 双向一致性仍缺可执行定义

目前 created audit要求5张未来表为空，verifyPilotCreatedReplayTx也硬性 lifecycle=created。需拆不可变创建事实验证与生命周期验证，所有 audit/创建nonce重放/新facade恢复路径均接同一分派；不能仅改一个入口。合法started后创建nonce仍返回原creation receipt，不能错误返回当前checkpoint为原始创建版本。

应明确：本片每课恰一start receipt，kind白名单、from/to版本、decisionID、result_json严格schema；audit使用哪张表、如何唯一关联该decision/command；outbox为连续0..N-1且精确匹配规范计划、checkpoint版本和归属，不仅验证FK；pending/attempt0、claim/lease字段空，无已执行状态。未使用的answer_claims/active_commands/completion_jobs仍必须空。额外receipt、孤儿audit、缺effect、重复/乱序/改payload均拒绝。

## SK-B5：COMMIT未知与确定回滚恢复需有有限接口

未知后只查询原receipt方向正确；但receipt暂时不存在不能证明旧提交已回滚。查询需在可判定旧writer结束的事务边界读取或维持unknown，不得直接换ID或执行效果。当前atomicConnection已做同连接回滚/清理；明确如何将“确定未提交”和“提交未知”分开返回、取消后仍如何安全查询，避免取消ctx令查询永远失败。

真实deferred COMMIT失败测试可以证明已知回滚；另需模拟响应丢失后的已提交查询，不能把两者混称未知commit故障。仍不引入新后台任务或无限重试。

## 可接受边界与建议切片

先冻结上述字段/接口表，再实现纯plan+started codec及生命周期分派，再实现同连接writer/CAS/audit/receipt/outbox提交与只读恢复。只在临时数据库的显式test harness可达，默认Open不迁移、App/Router/Room无注册。真实Playwright隔离harness可验证授权、丢响应与查询；route mock只能验证UI，不能替代数据库提交E2E。

本片无需先实现旧scope全部接线或删除协议，因为不与旧App共享运行库；这些仍是后续开放pilot的阻断。原legacy写门、删除代际、默认启动兼容、Teacher执行/媒体派发均不得在此片报告为已完成。

# Agent / 教学策略域复审：Start Kernel SDD（第 4 轮）

- 文档：`docs/41-试点课堂Start持久迁移内核SDD.md`；重点检查失败尝试 sidecar、成功 lifecycle receipt 隔离、工具 bundle/rollback、D-15 预算与 T-01。
- 评审类型：只读设计复审；未修改产品代码，未运行测试。
- 结论：**BLOCK（sidecar 尚未覆盖一个已实际发生的 Registry 工具调用）**。

## 第三轮意见复核

| 问题 | 状态 | 判断 |
|---|---|---|
| `RenderLessonStartT01` | 已关闭（设计层） | 新增 Agent 公共方法及 `Bank.RenderExactID`，OP-1 精确渲染、不随机口语化；固定 fallback、受信昵称/词数输入、黄金文本边界明确。 |
| 同 task rollback 的 budget 重置 | 已关闭（同进程） | bundle 在两次 Registry 调用后生成；同 ID、同 task、明确 rollback 后只重跑事务，复用相同 bundle/audit，不重调工具、不刷新两次预算；unknown 不产生新 bundle。 |
| AGENT-START-08 失败审计缺口 | **未关闭** | sidecar 覆盖失败调用，但当 say 成功、随后 show_word 失败时，成功 say 的 Registry audit buffer 也会被丢弃；没有持久记录这次成功调用。 |
| AGENT-START-09 bundle 可变性 | 基本关闭 | store 深拷贝并计算 digest、同进程 bounded cache 和 single-flight，重启/unknown 规则已明确；sidecar/DB 重启目录生命周期还需实现合同锁定。 |

## 阻断项

### AGENT-START-10 — 不完整 bundle 丢失此前成功的工具审计（P1）

位置：docs/41 §3（Registry 失败时“丢弃生命周期审计buffer并终态失败；失败尝试进入sidecar”）、§4 成功 lifecycle audit 合同、§6 sidecar 用例。

新 sidecar 明确记录 Registry 的普通失败和 panic，并与成功 lifecycle audit 分离；因此失败调用不会被误当成成功 receipt 的审计，也不会形成“成功 receipt 孤儿”。但是它仍未满足 D-15 的完整工具审计：典型的 `classroom.say` 成功后 `classroom.show_word` schema/handler/panic 失败，会先在 task-local auditor 产生一条成功 say AuditEntry，然后丢弃整个 lifecycle buffer；sidecar 只记失败的 show_word。最终 say 已执行过一次 Registry 工具但无任何持久/可检索审计证据。反过来，若把成功 say 写进 `audit_logs`，又违反本课无成功 receipt 时禁止孤儿 lifecycle audit 的合同。

**闭环条件：**sidecar 不应只存“失败工具”事件，而应在 Planner 失败时保存该 attempt 所有已发生的 Registry 调用结果（有序、无原始参数）：工具名、risk、scope/actor-task关联、schema/budget/handler 的稳定 outcome、trace/attempt/decision 关联及时间；先前成功 say 与最终失败 show_word 都要出现。调用 bundle 不完整时仍不写 lifecycle `audit_logs`/receipt/checkpoint/outbox。sidecar 校验器需要确认每个 terminal_failed attempt 的调用序列与 T-01 前缀一致（0、1 或 2 次调用），最多两次，且最后失败记录匹配 terminal reason；重复写入、缺行、非法行、相同 attempt_id 多行须 fail closed。增加 say 成功后 show_word schema/handler/panic、say 自身失败、budget denial 的审计断言，并扫 sidecar 不含 args/name/词面/panic/stack。这样失败审计与 lifecycle 成功审计仍为两个关联域，不会出现成功 receipt 孤儿。

## 建议的实现前合同（P2）

### AGENT-START-11 — sidecar 重启位置与 terminal_failed 去重需统一

位置：docs/41 §3 sidecar 段与 §5 临时目录/每次启动规则、§6 restart 用例。

sidecar 和 HMAC key 存在“私有临时目录”，但 harness 同时描述每次创建临时目录；重启后识别已存在的 terminal_failed 记录及稳定 command HMAC，要求 restart 继续使用原 SQLite、原 sidecar 文件和同一 HMAC key。若“harness 重启”创建新目录，失败墓碑消失，无法阻止同 lesson/command 重建 task，也无法查询先前失败审计。

**建议关闭条件：**区分“同一个测试 harness 内重启 server 子进程”和“创建全新隔离 harness”。前者需显式复用原私有目录/DB/key 且不能接受外部路径覆盖；后者是新的测试隔离空间，不能声称恢复旧 command。重启测试应验证 sidecar 非法尾行/截断、key 缺失/替换、重复 terminal_failed 记录均 fail closed；sidecar 的 append+sync 等待也应有明确超时/失败处理，避免无限持有 SQLite writer。

## 通过项与边界

- 新增 sidecar 与 lifecycle `audit_logs`/成功 receipt 分离，失败调用不会成为成功 receipt 的孤儿；sidecar 写不成就 fail closed。
- 成功 bundle 仍只持久化两条按 T-01 排序的 tool audit + 两条 queued outbox；成功 audit 与 receipt 一一验证。
- rollback 已证明时复用同一 bundle，不重新花费 D-15 event budget；COMMIT unknown 保持 pending_unknown，进程重启后无 receipt/bundle 不允许再调用 Registry。
- T-01 的 OP-1 精确渲染、昵称、首课 ordinal、冻结中文释义和 say→show_word 顺序均有明确合同；无产品入口、无媒体 dispatch，不构成真实课堂准入。

AGENT-START-10 关闭前，Agent 域仍不签字进入实现。11 建议随同修订，以免隔离 harness 的重启承诺无法落实。

# Agent 域复审：Start Kernel SDD（第 7 轮）

- 文档：`docs/41-试点课堂Start持久迁移内核SDD.md`；对照：`docs/04-Agent与话术库设计.md`
- 范围：上轮 AGENT-START-14 品牌拼写修正；T-01 精确话术与 fallback；plan/tool sidecar 事件顺序、失败审计与恢复链。
- 类型：只读复审；未修改代码/设计文档，未运行测试。
- 结论：**PASS（本轮指定范围）**。拼写问题已关闭；未发现新的 P1/P2 矛盾。

## 上轮问题状态

**AGENT-START-14（P1，叮叮/叮当名称冲突）：已关闭。** docs/41 §3 现已将固定 fallback 写为“我是叮当老师，{name}，我们开始学单词。”，强制断言要求 OP-1/fallback 含“叮当老师”及昵称；其 Golden 为“我是叮当老师！小明，今天我们搞定 50 个新单词，准备好了吗？”。该 Golden 与 docs/04 §话术库中的 OP-1 在 `{name}=小明`、`{n}=50` 替换后逐字一致。只读搜索未发现“叮叮老师”残留。

fallback 是模板缺失/渲染失败时的独立固定短句，不等同于 OP-1 的正常模板文本；SDD 对两种来源分别规定 `template_id/source`，E2E 也分别锁定正常 OP-1 与 fallback，因此这里不存在需要两段文本彼此相同的矛盾。

## 关键链路复核

- **Agent 结果先持久绑定：** attempt intent Sync 后才 Prepare；Agent 精确 say 与冻结 show_word 组成最终 command fingerprint；`plan_ready` 的 HMAC 在任何 Registry 调用前绑定最终计划、模板来源和固定工具序列。
- **工具不重放：** 每次 Registry 前先 Sync 对应 `tool_started`，返回后校验 typed result/Registry AuditEntry 并 Sync `tool_result`。缺 result 的 started 事件恢复为 `pending_unknown`，不得重规划或重执行；两个工具各自有独立的顺序事件。
- **失败链与成功账分开：** sidecar 以 HMAC 链记录固定 role/trigger/scope/risk、工具结果分类及 task/trace 的 HMAC 关联；身份和权限来源限定为 Store 验证的 capability 与 Registry AuditEntry，Planner/HTTP 不能自报。中途失败不写 DB lifecycle audit/receipt/outbox；完整 bundle 只有在 DB 原子提交成功时才产生成功审计。receipt 存在但终态 marker 写坏时 receipt 优先恢复，同时隔离为 `sidecar_degraded`，不允许新命令。
- **预算/重试边界一致：** 共享 event budget=2 对应 say、show_word 两个有序工具；同进程且持相同 bundle、明确 `rollback_proven` 才允许一次同 ID 存储重试，不重建 Agent task、不再调 Registry。未知 COMMIT 或进程重启后的无回执均不得重放。
- **验收覆盖：** 设计矩阵包括 plan/tool 同步前后崩溃、handler 返回到 result Sync 窗口、缺 result 的恢复、provenance 篡改/跨 command 事件、工具失败与 panic、fallback/正常模板、rollback 与 COMMIT unknown；断言包含稳定输出、零重规划/Registry 重放和零生命周期部分写。

## 遗留与边界

本轮没有发现指定 Agent 域范围内的 P1/P2 阻断。PASS 仅代表设计复审通过，不代表代码已实现或测试已执行，也不批准生产路由、媒体派发、模型接入或真实课堂试点。

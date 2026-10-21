# lesson.start 持久迁移内核 SDD：后端复审 8

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新保存稿）。只读复核 BE7-1..5、BE6-1..11 与新增 package adapter、HMAC sidecar、重试状态、混合审计合同，并对照当前 Go package/API/SQLite 边界。未修改文件或代码，未运行测试。

## 结论

**BLOCK。** BE6-4 与 BE6-10 已通过修订闭环；BE6-1..3、5..9、11 继续成立。BE7-1..5 的核心意图已补上，但新稿仍有三处实现冲突：sidecar result_class 不覆盖确定性 writer 拒绝中列出的值；audit Args 的持久化 allowlist 漏掉新保留的 pilot_marker；say outbox 与 receipt 结果的逐字节比对没有可比字段，因为 receipt result_json 是固定 queued 摘要，不含 say 文本。

## BE7 与 BE6 定点复核

| 项目 | 状态 | 依据 |
|---|---|---|
| BE7-1 确定性 writer 拒绝 | PARTIAL / BLOCK | §43 已要求确定性拒绝写 terminal_failed 并在响应前 Sync；但 sidecar result_class allowlist没有 `not_supported`、`schema_invalid`、`unauthorized`，而该段用这些作为拒绝类别。需要冻结到 allowlist中的精确映射，不应把 schema/权限拒绝含混映射为其他类别。 |
| BE7-2 rollback attempt上限 | PASS WITH CLARIFICATION | 明确最多两次 commit_attempt、仅第一次 rollback_proven 可进入 attempt 2、第二次 rollback后封口 terminal_failed，不可第三次。建议明确 rollback_proven 是 attempt result、不是 command terminal；当前文字总体如此。 |
| BE7-3 pilot audit marker | PASS | pilot_marker 被明确定义为普通 Registry 永不生成的保留键，全库所有含 marker 的行必须且只能关联 receipt；可以检测孤儿 pilot audit。 |
| BE7-4 outbox权威事实 | PARTIAL / BLOCK | show_word 逐字段绑定 frozen_words[0] 已清楚；say 要求“逐字节等于对应receipt结果/active room_facts.teacher_text”，但 receipt.result_json 固定仅含 queued、decision trace、outbox count，不包含文本，无法与 payload 逐字节比较。应改为 say.text 与 room_facts.teacher_text 精确相等，并独立验证 receipt 的 decision_id/outbox_count。 |
| BE7-5 unknown重试措辞 | PASS | §36/§46 将 unknown 期间重试限定为只读 receipt 查询；仅原连接 typed rollback_proven 且持有同一原 bundle 时最多一次存储重试。 |
| BE6-1..3 | PASS | 事务外两段 Planner/Registry 时序、昵称 replay 例外、cache 主键与 fingerprint 冲突比较均明确。 |
| BE6-4 | PASS | 已新增 api package 下 pilot_e2e Planner adapter 实现；API私有 agentTask/context 在 API 包内构造，Store 不 import API，harness 负责组装。 |
| BE6-5..9 | PASS WITH IMPLEMENTATION CONDITION | unknown专用 typed proof、case级 coordinator/cache、双HMAC与sidecar重启验证、rollback后重启封口及legacy/FK审计保留均已具体化；atomicConnection需实现新的 proof outcome，当前实现没有该 typed result。 |
| BE6-10 | PASS WITH ALLOWLIST FIX | pilot_marker 让 orphan audit 可识别，TraceID、decision/effect索引与每课 receipt/outbox链接也已写明；但 §7日志/持久 allowlist遗漏 marker（见 BE8-2）。 |
| BE6-11 | PASS | 混合库用例明确 A active 不影响 B 的首个 reservation ordinal=1。 |

## 新发现

| ID | 级别 | 问题与修订建议 |
|---|---|---|
| BE8-1 | P1 | sidecar result_class allowlist与 terminal_failed categories 不一致。§42仅允许 `ok|schema_rejected|budget_denied|handler_error|handler_panic|planner_error|output_invalid|authorization_rejected|stale_checkpoint|already_started|command_conflict|write_error|rollback_proven|commit_unknown`；§43还要求记录 `not_supported|schema_invalid|unauthorized`。这些类别不能都无损映射到 authorization_rejected/schema_rejected。将三者加入 allowlist并给 golden/status tests，或冻结无损且一对一的映射。 |
| BE8-2 | P1 | audit 持久化 Args allowlist缺少 pilot_marker。§53/§63要求 pilot audit Args 包含 `pilot_marker="pilot-audit-map/v1"`，但§95将持久 Args 白名单冻结为仅 `bytes,schema_version,scope,effect_index`。实现按后者检查会拒绝所有合法 start audit，按前者写又会违反日志/持久 allowlist。同步两处，并确认数据库 Args 2 KiB限制包括 marker。 |
| BE8-3 | P1 | say outbox payload 与 receipt.result_json无可比文本。§63的 receipt JSON固定为 queued摘要，而 say payload另存文本。不能要求 payload“逐字节等于receipt结果”。将权威校验明确为 payload.say.text == active room_facts.teacher_text；receipt仅校验同一 decision_id、kind、版本、outbox_count，并校验 teacher_text 为该 command/bundle 的可信 rendered say。 |

## 其余新稿观察

Planner Prepare/Execute 两阶段与 durable intent 顺序、case级 coordinator、全局 sidecar append 锁及单活动 harness 进程锁彼此可组合。双HMAC域分离、canonical event bytes/HMAC chain、receipt优先恢复、损坏 fail closed、同 lesson single-flight 与不同 lesson 并发也未发现新的结构性冲突。混合生命周期方案明确保留旧 auditPilotDataTx 的 legacy bridge/runtime、未迁移态与逐表 FK 检查，仅把全库 created-only 空表约束改为全库结构校验后逐课分派；audit/outbox 已按 receipt TraceID、decision/effect_index、lesson/student/version 关联，并有跨课篡改用例。

达到设计可实施门之前，修复 BE8-1..3，并由实现/测试评审确认 typed rollback_proven 能由 pinned SQLite connection 准确产生，且普通 audit 永不创建 pilot_marker。

本轮是后端设计复核，不代表整体专家门通过或功能已经实现。

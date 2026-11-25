# 双工课堂持久效果 Outbox 规格与 SDD

日期：2026-11-24。状态：实现设计；实现完成及验收结论以本文件后续增补为准。

## 1. 目标与范围

把已确认的 ASR 文本形成的课堂状态、确认收据和待执行课堂效果写入同一个 SQLite 事务。进程在事务提交后、效果发送前退出时，重连应按原决策 ID 重放待执行效果；同一效果重放不得使课程推进或浏览器重复播放已确认完成的音频。

本范围只接管 `voice_confirm` 的确认路径，不改变未确认的 ASR 快路径、现有文字输入合同、教学判分规则或默认关闭的 Pilot/Shadow 产品开关。普通课堂迁移、默认生产启用和真人声学试点不由本设计自动批准。

## 2. 持久化合同

`DuplexCheckpoint` 是每课唯一 CAS 行，保存 owner、单调版本和课堂 `classroom.State`。首次接入时从已校验的 `snapshot/<lesson>` 建立版本 0；owner 不一致、非法快照或版本溢出均 fail closed。恢复时读取 checkpoint 作为双工路径的持久事实源，并校验其 owner 与课程一致。

`DuplexReceipt` 绑定 `decision_id + lesson_id + student_id + epoch + input_revision + utterance_id + text_hash`。同 ID、同 fingerprint 且 `committed` 的重放返回原收据；`committing`/`rejected` 不可伪装成已提交。新原子路径只读查询现有 revision 冲突，**不先写 `PrepareReceipt` reservation**；缺失时由 `Commit` 在状态转换、snapshot/outbox 同一事务内创建 committed receipt。旧 shadow 路径可暂用 reservation，但必须以 `CommitSnapshot` 的 expected snapshot CAS 终结，且不得与已初始化 checkpoint 并存。

每个确认事务必须在同一 SQLite writer transaction 中：

1. 对 checkpoint `version == expected_version` 做 CAS，保存下一版本和新状态。
2. 幂等检查并创建已提交 receipt。
3. 同事务检查本课是否存在未 ACK 的 batch；存在则返回 `effects_pending` 且零写入。插入有序 Outbox batch。Payload 必须是版本化、限长、JSON 有效的**冻结最终效果帧/播放参数**，不能只存 action 意图再于恢复时重新生成话术；不包含凭据、音频、任意工具名或客户端身份。
4. 同事务更新兼容读取的 `runtime_records(snapshot/<lesson>)`，使现有 REST/legacy 恢复读取保持一致。

版本冲突、receipt 冲突、任一 outbox 插入失败、snapshot 保存失败或提交结果不确定时，不发送任何成功确认或课堂效果。事务回滚后 Room 恢复提交前状态。

## 3. Outbox 交付与重放

Outbox 每项使用不可变、无拼接歧义的 `effect_id = "ddw-effect-v1." + base64url(decision_id) + "." + effect_index`，唯一键防止重复插入。状态为 `pending → claimed → sent → acked`；claim 包含 token、有限租约和尝试计数。租约使用 UTC 时间并由数据库条件更新仲裁；过期可重领，payload 和 effect ID 不变。达到重试阈值后保留未 ACK 项、阻断本课后续写入并报管理员可见故障，不把效果静默丢入死信。ACK 项按保留策略清理，receipt 和决策审计仍保留。存储层需用条件更新完成 claim/ACK，不能依赖单进程内存锁保证多进程互斥。

最终帧在确认事务中冻结：事务内通过受信 Teacher 工具定义生成结果，工具审计和帧 payload 与 checkpoint/receipt/outbox 同事务提交；处理过程不得调用 Room、网络或模型。需要外部模型的判分不伪装成可重放帧，而由持久状态中的 `judging` 相位独立恢复，并以新版本提交其结果。重放不重新调用教师工具或状态机。效果帧带稳定 effect ID。浏览器按 effect ID 幂等处理并持久记录 batch 内容 hash 和每个 TTS effect 状态。

协议按 effect 逐项确认，batch 只是一次 decision 下按 `effect_index` 有序的多项集合，不单独 ACK；同一时刻只投递最小未 ACK effect，ACK 后才允许投递下一项，整个 batch 全部 ACK 后才解除课堂写入屏障。`payload_hash` 是 SHA-256，对 Outbox 中冻结并实际发送的 UTF-8 JSON payload 原始字节计算；重放发送相同字节，禁止重新 marshal/规范化。每个 wire effect 必须含 `protocol_version`、`effect_id`、`decision_id`、`effect_index`、`payload_hash`、不可变帧 payload 和当前 replay context。普通 UI 帧用 `effect_ack`；TTS 音频仅在 `tts_finished` 到达后 ACK。客户端回传 effect ID/hash、decision/index 和 origin context，**不接触内部 lease token**；服务端从已认证 WS connection lease 和当前已 claim Outbox 行取 token，在条件更新中校验 owner、lesson、未过期 token、原始 effect identity 与 payload hash，任何字段不符拒绝。数据库在 Claim 与 ACK 两层都拒绝越序；ACK 同一 effect 同 token 幂等，旧/错误 token 即使 effect 已 ACK 也拒绝。

发送采用至少一次语义：服务端在 ACK 前可重发相同 effect ID；浏览器去重后仍回 ACK。ACK 顺序按持久 `effect_index` 检查；迟到 ACK 只能确认原 effect，不能越过或确认较新 batch。恢复后的帧使用新 Room epoch/context，但保留原 effect ID 和 payload hash；状态快照先发，Outbox 按 ID 顺序重放。任何课堂写输入在待处理 batch 清空前都返回 `effects_pending`，避免快照与旧动作互相覆盖。旧输入和旧连接 ACK 均按当前 epoch/lease 校验拒绝。

TTS 回执的 `origin_context` 绑定音频开始播放时的课堂 epoch/turn；新 replay context 只用于当前连接授权，不能将旧完成事件解释成新课堂输入。浏览器先持久化 `(effect_id, payload_hash, completed, origin_context)` 再发 `tts_finished`；若音频已经播放但本地完成记录未持久化，恢复时按至少一次语义允许重播，不承诺 exactly-once。客户端不持有服务端 lease token；服务端根据当前连接与 claim 行完成 fencing。

一个 lesson 若有未 ACK 的 batch，不接受新的双工确认决策，返回明确可恢复的 `effects_pending`，防止较新状态越过待发送的教学效果。重连先发最新 `state_snapshot`，再按 ID 顺序交付 pending/过期租约效果。确认收据已提交而效果未 ACK 时，`voice_confirmed.effects_status=pending`，不得显示为已完成播放。

## 4. 失败与恢复规则

| 失败点 | 持久状态 | 恢复行为 |
|---|---|---|
| SQLite 事务开始/写入/提交前失败 | checkpoint、receipt、outbox 均不变 | 恢复 Room 快照；允许用户显式重试 |
| 提交成功、执行效果前退出 | 新 checkpoint、committed receipt、pending outbox | 重启从 checkpoint 恢复，重连领取同一效果 ID |
| 最终帧和工具审计落库前退出 | 确认事务整体回滚 | 不发送成功帧；Room 回滚，可显式重试 |
| 效果发送成功但 `MarkOutboxSent` 失败 | outbox 保持 claimed，租约最终过期 | 用相同 effect ID 重发；浏览器幂等处理 |
| 效果执行中退出，尚未 ACK | outbox 未 ACK，租约最终过期 | 重放冻结 payload；浏览器幂等处理 |
| ACK 已提交后进程退出 | outbox acked | 不重放该效果 |
| ACK 已提交但响应丢失 | outbox acked | 重复 ACK 返回成功；不重放 |
| 新 worker claim 与 ACK 并发 | 条件更新/事务决定唯一终态 | ACK 一旦提交为 acked，迟到 send-complete 不得降级状态 |
| Room 校验拒绝已预留 receipt | receipt=`rejected`，无 checkpoint/outbox 写入 | 同一 revision 重试返回明确拒绝，不复用超时 committing receipt |
| owner、版本、payload 或 DB integrity 不一致 | 不发布 Pilot/Shadow readiness | 记录脱敏的 trace/lesson/reason 并拒绝 |

如果工具效果不能安全重放，必须将其结果先在事务内固定到 outbox，或明确标为不可重放并阻断，而不能在失败后生成另一个教学决策。跨进程重试不允许再次调用教学状态机。

## 5. 观测与隐私

每步日志包含 `trace_id`、lesson、decision、effect index、checkpoint version、状态迁移、attempt、duration 和 outcome；不记录回答正文、text hash 原值、音频、prompt、授权信息或密钥。必记 `duplex_checkpoint_cas_conflict`、`duplex_outbox_enqueued`、`duplex_effect_claimed/sent/acked/replayed/failed` 和 `duplex_recovery_blocked`。

## 6. 验收矩阵

### Go/SQLite

- 首次 checkpoint 初始化、同 owner 恢复、异 owner 拒绝、CAS 冲突、版本边界。
- 新事务的状态/receipt/outbox/runtime snapshot 原子回滚；每个写入边界故障注入。
- 双进程同版本竞争只有一个提交；相同 decision 重放不重复状态推进或 outbox 插入。
- pending claim、活租约排斥、过期 claim 重领、错误 owner/token 拒绝、ACK 幂等、ACK 与 mark-sent 竞态、终态不得回退、重试阈值阻断不丢项、保留清理边界。
- 杀进程后新 App 恢复 checkpoint 和 pending batch；ACK 后重启不重放；flush 中途失败只重放未确认项。

### Playwright

- 同一 turn 首次错误/未匹配输入不推进；正确确认后事务提交一次、outbox 效果出现一次。
- 在“提交后、发送前”杀服务，重启后状态快照正确且同 effect ID 被重放；不重复判分/结算。
- 在多帧 batch 中途断线，只重放未确认 batch；刷新页面后 browser local receipt 防止重复播放已完成音频；并校验重放 payload hash 完全一致。
- 普通 UI effect ACK 与 TTS `tts_finished` 分别测试；ACK 丢失、迟到、乱序及重复；旧 connection lease、伪造 effect ID、错误 lesson/student 均拒绝。
- 独立进程 A/B 共用隔离 SQLite：A 提交后被杀，B 重连恢复；另测双方同版本并发 CAS；提交结果不确定、MarkSent 失败、ACK commit 后断连和租约边界精确故障注入。
- 双进程同时确认同 checkpoint，CAS 只允许一个成功；另一个保持可恢复错误。

只有 Go 故障测试、真实 SQLite 双进程 Playwright、全量回归和同源评审全部通过后，才能将 outbox 标记完成。FakeStore 或仅检查 schema 不构成验收。

## 7. 实施状态、评审决议与发布门

### 2026-11-24 实施状态

- 本轮增强了 `DuplexRepo.Commit`：checkpoint CAS、receipt、outbox 和兼容 `snapshot/<lesson>` 写入同一事务；每课有未 ACK 效果时拒绝下一次提交；拒绝超过 SQLite 有符号整数范围的 checkpoint 版本。
- Outbox 存储层有 pending/claimed/sent/acked、有限租约、尝试次数、过期重领和带 lease token 的 ACK/MarkSent fencing。新增回归覆盖事务回滚、兼容快照原子性、未 ACK 阻断、过期 worker 拒绝及最大版本边界。
- 存储层按 id 顺序拒绝领取后续 effect，并在 ACK CAS 中拒绝同一 decision 的乱序确认；ACK 后只允许同 lease token 的幂等重试。客户端协议处理器仍未实现，因此这只是仓储不变量，不代表 WS ACK 已可用。
- **业务接线仍未完成**：`server/internal/api/ws.go` 的 `voice_confirm` 仍调用 `PrepareReceipt → Room mutation → CommitSnapshot → flushDeferredEffects`。`CommitSnapshot` 现带 legacy snapshot expected-value CAS，并在 checkpoint 存在时拒绝写入，但仍不创建 checkpoint/outbox，也无法解决提交后崩溃丢动作。生产 API 没有调用 `DuplexRepo.Commit`、没有 `effect_ack` 协议 handler、没有从 checkpoint 恢复 Room，也没有 outbox 发送/恢复消费者。
- 目前 deferred actions 是 `classroom.Action`，Teacher Agent 的解析结果及审计由 `callAgentTool` 各自事务生成。将 action 意图直接放入 outbox 再于恢复时重跑工具不满足冻结最终 payload/同事务审计；此边界须先重构出可在确认事务内执行的受信事务工具 executor，再接入 Room。不得通过将 Agent 工具调用改成无审计快捷路径规避该约束。
- Room rejection 现在通过 `RejectReceipt` 将预留收据终结为 `rejected`，并有存储回归；端到端竞态和跨进程 receipt 旅程仍未验收。旧 DuplexOutbox 表结构迁移、双连接/双进程真实 SQLite Playwright 崩溃旅程尚未全部验收。
- 只有状态为 `committed` 且 fingerprint 相同的 receipt 可被 `Commit` 视为幂等成功；`committing/rejected` 冲突。删除了可脱离状态事务单独把 receipt 标为 committed 的 `FinalizeReceipt`；已有 checkpoint 读取时也验证 JSON 与 SQLite 版本边界。
- 当前提交复验：`go test ./... -count=1`、`go vet ./...`、`go test -race ./internal/store -run TestDuplex -count=1` 通过；`web/playwright.duplex.config.ts` 使用本次构建的 server binary 通过 **9/9**。这些回归验证已接线的 Shadow/预检路径和存储原语，不覆盖尚未实现的 Outbox ACK/跨进程恢复旅程。

### 评审决议

架构、后端和 E2E 三域评审均认为设计目标合理，但拒绝将当前实现标记完成。收敛出的实施顺序是：

1. 定义安全重放的最终帧合同和 ACK/TTS 完成语义；为 Teacher 工具提供受信、可审计、可绑定外层事务的执行器。
2. 修复 receipt rejected/committing 生命周期；从可信 snapshot 初始化 checkpoint，并将 Room 状态、receipt、冻结帧、工具审计及 outbox 原子提交。
3. 接通 checkpoint 恢复、连接/epoch lease 校验、effect ACK、TTS 完成回执和有序恢复消费者；重放不再次运行判分或状态机。
4. 补旧 schema 升级、多连接 CAS/ACK 竞态和 SQLite 故障注入；再做双进程真实 `api.App` Playwright 杀进程恢复旅程。

以上第 1、2 项是当前阻塞。Pilot/Shadow 继续 opt-in；仓储单测通过不代表产品闭环，也不解除生产 Pilot、VFS、TLS、真人课堂或听感验收门。


### 2026-11-25 Transaction executor verification update
- Added `trustedToolExecutor`, which binds teacher classroom tools to the caller's GORM transaction. It accepts only a trusted teacher task for this App and derives audit identity from that task. It does not begin or commit the transaction.
- Each tool call persists its new audit rows inside the same outer transaction. Tests cover missing transaction rejection, non-teacher task rejection, audit rollback/commit, and atomic audit persistence with `DuplexRepo.Commit`. Replaying an already committed decision does not rerun the tool callback or duplicate audit/outbox rows.
- Verified with `go test ./internal/api -run 'TestTrustedToolExecutor|TestDuplexCommitCanAtomicallyPersistTrustedToolAudit' -count=1`, `go test -race ./internal/api -run 'TestTrustedToolExecutor|TestDuplexCommitCanAtomicallyPersistTrustedToolAudit' -count=1`, full `go test ./... -count=1`, `go vet ./...`, and the duplex Playwright config (9/9).
- This is transaction infrastructure only. Production `voice_confirm` still uses `PrepareReceipt -> CommitSnapshot -> flushDeferredEffects`; Room checkpoint restore, WS effect ACK/TTS completion, and outbox recovery delivery remain unimplemented. Do not treat these checks as end-to-end outbox acceptance.

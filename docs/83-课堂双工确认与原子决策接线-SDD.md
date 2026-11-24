# 课堂双工确认与原子决策接线 SDD

## 目标
将课堂语音输入接入同一条可取消、可确认的 WebSocket 旅程，并为后续 SQLite 原子提交保留稳定边界。

## 快路径与确认路径
- 采集器按 16kHz 单声道 PCM16 分片发送 `voice_stream_start/chunk/end`。
- 服务端只返回 `voice_final` 和 `voice_confirmation_required`，不在转写完成时判分。
- 用户确认后发送 `voice_confirm`，服务端校验 session、epoch、revision 和文本 hash。
- 当前 REST `/voice` 和文字输入继续作为降级路径。

## 原子决策边界
课堂决策必须拆成纯状态转换和持久化提交：输入快照 + 文本得到下一状态、教学效果和 outbox；SQLite 事务内完成 CAS、receipt、outbox；提交后才广播 WS 和播放音频。Room 不得进入 SQLite 回调。

### 当前确认路径实现状态（2026-11-24）
- WebSocket 的每次录音使用同一课堂 epoch 下单调递增的 `input_revision`；它独立于课堂 `turnVersion`，因此同一相位重试不会复用收据唯一键。课堂事件仍用 `TurnContext` 做 stale 检查。
- `input_revision` 计数器保存在 Room（不是单 WebSocket 连接），在 Room 锁内递增；同进程重连/连接替换不会重用版本。进程重启会新建 Room epoch。
- 确认文字后持 Room 级 `mutationMu` 写锁覆盖快照→receipt 事务→动作刷新；普通 ingress、重连和计时器使用共享读锁，状态转换仍由 Room mutex 保护。这样并发命令不能进入错误的确认快照或被失败回滚覆盖，普通读锁也不会把慢模型调用导致的控制/超时路径全部串行阻塞。SQLite 在一个事务中写入课堂快照并将匹配 receipt 标记为 committed。只有事务成功后才向客户端发 `event_processed`、课堂动作及 `voice_confirmed`。事务失败会丢弃待发动作并恢复内存快照。
- 当前 Playwright 目标用例先提交一次未匹配转写（课堂不推进），再在同一课堂 turn 提交正确转写；断言确认前无效果，成功确认后只推进一次并恰好显示一个 quiz item。
- **尚未达到本节完整目标**：legacy `CommitSnapshot` 现在使用 expected snapshot 值做乐观 CAS，并拒绝与已初始化 checkpoint 并存；它仍未执行 checkpoint version CAS，也没有把待执行动作写入持久 outbox。提交成功后进程若在动作执行前崩溃，恢复依赖课堂状态的 `RecoverDelivery` 重建当前相位动作；无法保证重放所有原始动作和教师话术。Pilot/Shadow 不得据此宣称具备完整 outbox 的崩溃一致性。
- 2026-11-24 已补强通用 `DuplexRepo.Commit` 存储原语：CAS、receipt、outbox 与兼容课堂 snapshot 同事务提交；pending batch 阻止后续提交；Outbox lease 具 token fencing。**该原语尚未接入本节的 Room/`voice_confirm` 路径**，不改变上一条未闭环结论。具体阻塞和实施顺序见 `docs/95-双工课堂持久效果Outbox规格与SDD.md`。
- 同日补充：Room 校验拒绝时将预留 receipt 终结为 `rejected`，避免旧 `committing` 记录在等待窗口后被再次执行；这只闭合拒绝状态存储，不代表进程崩溃恢复已完成。
- 动作执行中途失败时，当前客户端会收到可重试的课堂错误；动作尾部持久化重试及逐动作确认仍需实现。生产默认启用前必须补齐版本 CAS、持久 outbox、重连恢复/确认协议，并通过跨进程故障注入验收。

- `voice_confirmed.effects_status` 区分 `completed` 与 `pending`；后者表示课堂决策已持久提交，但动作工具未能完成，客户端同时收到 `classroom_error` 并应走文字/重试降级。当前 pending 尚无持久重试机制，不能仅凭收据已提交判断课堂播放完成。

## 故障规则
- 取消、断开、旧相位不得产生课堂推进。
- `committing` 超过 30 秒只返回 `commit_pending_recovery`。
- ASR、模型或网络失败自动降级文字输入。

## 验收
Playwright 覆盖真实浏览器 WS 旅程；Go 覆盖 hash、乱序、重复确认、跨进程恢复和事务冲突。

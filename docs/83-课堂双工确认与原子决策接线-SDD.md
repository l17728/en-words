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

## 故障规则
- 取消、断开、旧相位不得产生课堂推进。
- `committing` 超过 30 秒只返回 `commit_pending_recovery`。
- ASR、模型或网络失败自动降级文字输入。

## 验收
Playwright 覆盖真实浏览器 WS 旅程；Go 覆盖 hash、乱序、重复确认、跨进程恢复和事务冲突。

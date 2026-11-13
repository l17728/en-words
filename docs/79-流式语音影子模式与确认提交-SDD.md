# 流式语音影子模式与确认提交 SDD

状态：设计阶段；默认关闭，不改变现有课堂行为。

## 1. 目标与范围

第一阶段接入浏览器连续采集、短帧聚合、本机 ASR 草稿/定稿和用户确认提交。影子模式只负责媒体采集、转写展示和取消；不得直接判分、消耗机会、推进词序、写入成绩或长期记忆。

保留现有 4 秒 MediaRecorder、文字输入和 `/transcribe` 降级路径。`native_duplex` 与 `automatic_teaching` 继续为 false，必须由服务端特性开关授权，查询参数不能开启。

## 2. 状态机

`idle → capturing → draft → final_pending_confirmation → submitted | canceled | degraded_text`。

服务端为每个课堂维护权威 `pendingUtterance`：`session_generation`、lesson、epoch、turn、input_revision、stream_id、utterance_id、status、final_text、expires_at、commit_receipt。客户端字段不能自行提升权限。状态转换使用互斥/CAS：`start` 只能从 idle，`chunk` 只能从 capturing，`end` 只能一次，`confirm` 只能从 final_pending_confirmation，重复 confirm 返回原 receipt，不能重新判分。

优先级固定为：退出/换号/换课/新题/机会变化 > 手打提交或显式 cancel > confirm > final > draft。较低优先级到达时只返回 `stale_stream`、`stale_input_revision` 或 `already_committed`，不得修改课堂状态。

每次 utterance 固定：`session_generation`、`lesson_id`、`epoch`、`input_revision`、`stream_id`、`utterance_id`、`chunk_seq`。新题、机会变化、暂停、退出、换号、换课会递增 generation 并撤销旧 lease。旧 draft/final/callback 只能被丢弃，不能恢复麦克风、字幕、播放或课堂状态。

## 3. 协议（v1，冻结）

第一版只使用 UTF-8 JSON，不使用 WebSocket binary opcode，避免现有 WS 读泵把二进制帧误判为非法 UTF-8。音频块使用 base64 PCM16；解码后单块最多 2560 字节（80ms@16kHz/mono），单流最多 15 秒、188 块、480000 个解码字节，base64 wire 字段另限 6400 字节，单学生最多 1 个活动流。超过限制立即结束并降级文字。后续二进制协议必须另行版本化，不能在 v1 中隐式切换。

start 请求包含 `protocol_version:1`、客户端 `event_id`、`context{epoch,turn}`、`stream_id`、`utterance_id`；服务端在 `start_ack` 首次生成并返回 `session_generation`、`stream_generation` 和 `input_revision`，客户端不得自造可信 generation。后续 chunk/end/cancel/confirm 必须回传这些服务端字段；缺失或不匹配返回 `stale_stream`。`input_revision` 表示当前题目/机会/账号授权版本。

浏览器发送：

- `voice_stream_start`：语言、采样率（16000）、声道（1）、流模式（shadow）；
- `voice_stream_chunk`：base64 PCM16，单调 `chunk_seq`，服务端只接受下一个序号；
- `voice_stream_end`：原因、最后序号、utterance_id；
- `voice_stream_cancel`：幂等 `cancel_id` 和目标 `stream_generation`；
- `voice_confirm`：`utterance_id`、`final_revision`、`text_hash`、用户确认文本和新的 `event_id`。

服务端返回：

- `voice_draft`：第一版只表示本地 VAD/采集状态，不伪造 ASR 文本；
- `voice_final`：定稿文本，仍不属于教学提交；
- `voice_confirmation_required`：要求用户确认或改写；
- `voice_stream_ack/error`：缺序号、超长、背压、权限、模型或网络错误。

第一版 provider adapter 在 `voice_stream_end` 后把已收集 PCM 封装为 WAV，调用现有本机 `/transcribe`，因此只承诺流式采集和有界传输，不宣称实时 interim ASR。没有本机 ASR provider 时，start 被服务端拒绝并回到文字。`voice_final` 只在 provider 返回后生成。

错误码冻结为：`feature_disabled`、`provider_unavailable`、`invalid_schema`、`invalid_audio_format`、`chunk_out_of_order`、`stream_overflow`、`stream_expired`、`stale_stream`、`stale_input_revision`、`manual_input_won`、`already_committed`、`permission_denied`、`asr_failed`、`network_lost`。其中 `already_committed` 必须携带原 `commit_receipt`，其余错误不得携带可提交正文。

确认回执以 `(student_id, lesson_id, utterance_id)` 为唯一键，保存 `input_revision`、`final_revision`、`text_sha256`、`status`、`commit_receipt` 和 `created_at`。先持久化 `committing`，再调用既有教学管线；重连或进程恢复先查询该键：`committed` 返回原 receipt，`committing` 进入有界恢复任务，未知结果不得新建提交。确认请求携带 `text_sha256`，哈希不一致返回 `text_hash_mismatch`。内存状态机只是协议单测，生产接入必须使用 SQLite 持久化。

控制帧必须独立于慢模型队列；队列有界，溢出显式降级文字。所有帧带 trace_id、lesson_id、generation、stream_id、utterance_id 和 seq，日志只记录文本长度与摘要，不记录原始音频或完整转写正文。

## 4. 确认提交

用户确认使用 `voice_confirm`，服务端把确认文本转换为现有 `voice_text` 处理，但保留 `input_revision` 和 `utterance_id`。确认只允许服务端仍处于相同题目/机会版本；若 `turn` 已变化，返回 `stale_input_revision` 并降级文字，不放宽现有严格相位校验。确认前任何 ASR 结果都不进入 `judge`、`repeat_evaluated`、`quiz_judged` 或课堂推进。

同一 `utterance_id` 只能产生一个业务提交；超时按原 ID 查询，不创建新提交。手打文本先提交后，迟到语音 final 必须被拒绝；反向亦然。

## 5. 降级与安全

麦克风权限拒绝、设备断开、AudioWorklet 错误、序号缺失、网络断开、模型超时、队列满和超过 15 秒上限，都停止当前 utterance，保留手打草稿并显示“使用文字继续”。不得自动启用云服务，不上传到未知端点，不跨账号或课堂复用缓存。

## 6. 验收用例

### Go

- 帧序列、重复帧、缺帧、超长和背压；
- generation/epoch/turn 变化时旧 final 不提交；
- 同 utterance 幂等提交与超时恢复；
- 确认前不调用 judge、不推进、不写成绩；
- 权限、模型、网络失败均降级文字并写可定位日志；
- 多学生 stream 隔离。

### Playwright

- 影子模式显示 draft/final，确认前课堂不动；
- 取消/换题/刷新后旧音频、字幕、转写均不复活；
- 迟到乱序 final 不覆盖手打草稿；
- 标签、否定、问句和内部标点保持现有拒绝规则；
- 两学生并发、断网恢复、模型 503、文字降级；
- 特性关闭时页面不可启动流式上传。

## 7. 发布门

先完成文档 review，再实现协议和影子模式；影子模式 Go/Playwright 全绿后才接确认提交。自动教学、真实 VAD 声学门、回声/噪声和真人课堂试点另行验收，不与本阶段合并放行。

## 8. 当前实现增量（2026-11-13）

已实现第一阶段的安全子集：服务端通过 `ENWORDS_DUPLEX_PILOT=1` 开启设备预检，再通过独立的 `ENWORDS_DUPLEX_SHADOW=1` 开启影子转写；两者均未开启时不会请求麦克风。浏览器端以 20ms PCM16 帧聚合为有界 WAV，调用既有 `/transcribe`，只展示结果，不进入判分、机会消耗、课堂推进或持久化成绩。采集超限、停止、权限变化和转写失败均回到文字路径。

本增量不实现 WebSocket 流式协议、`voice_confirm`、SQLite 提交收据或正式课堂接入；这些仍是下一阶段的独立开发门，必须在 Go 与 Playwright 全量验证后才能启用。

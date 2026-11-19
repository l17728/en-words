# TTS 素材预生成规格

## 目标与范围

在进入课程前，按课程词条、朗读规则和当前 Profile 计算所需语音素材清单；已存在且可读的素材直接复用，缺失素材进入可恢复的预生成任务。任务完成后才允许语音课堂开始，并在页面显示扫描、生成、完成或失败进度。ASR 是实时输入，不缓存识别结果，也不纳入本规格。

本规格建立在现有 `SpeechAsset`、`synthesizeCached`、`ModelProfile` 和 `/api/v1/speech/synthesize` 之上。第一阶段使用服务端本地模型；后续可由驻留程序执行同一任务协议。8 人真实容量验收保持暂停，不作为本特性的通过条件。

## 缓存身份与复用

缓存键必须由规范化后的语言、规范化朗读文本、TTS 服务 ID、模型、声线、朗读规则版本和词条内容版本组成，并使用 SHA-256。首版声线固定为服务声明的 `default`，Profile 暂不暴露 voice；后续增加声线时必须进入键。不得包含用户 ID、课程 ID 或 Profile ID；不同 Profile 只要上述有效服务参数相同即可复用。模型、声线、规则或内容变更自然产生新键；旧键只读兼容，不得被新 revision 复用。文件写入临时文件后原子改名，数据库记录仅在文件完整校验后标记 `ready`。

## 任务状态

每个预生成请求以 `(cache_key, manifest_revision)` 幂等合并：`pending → running → ready|failed|canceled`。SQLite 持久化任务表使用唯一索引、attempt token 和 lease expiry；worker 通过条件更新领取任务，跨进程只允许一个有效租约。重复提交返回同一任务；并发请求不得重复调用上游。任务保存尝试次数、错误摘要、创建/更新时间和 trace_id。失败允许重试，已完成项不重复生成；管理员可按词条或整课强制刷新。进程启动时将过期 `running` 任务恢复为 `pending`。

## 课堂门禁

`scan` 返回总数、ready、missing、failed 和 manifest revision。课程快照取自服务端 lesson/book 当前已发布版本；按词条 ID、语言、段序稳定排序，使用 canonical JSON 计算 revision。`ensure` 创建或复用任务；客户端轮询进度（首版 REST，间隔退避）并展示固定高度进度区。门禁接在普通 classroom `enter` 状态迁移之前；未 ready 返回 `speech_materials_pending` 且不写入课堂进度。只有 `ready == total` 才进入语音课堂；失败时明确展示原因并提供“重试语音”或经用户明确点击的“使用文字继续”，文字降级写入审计并不伪装为语音就绪。

## 可观测性与安全

记录 `speech_material_scan_started`、`speech_material_cache_hit`、`speech_material_job_started`、`speech_material_ready`、`speech_material_failed`、`speech_material_gate_blocked`、`speech_material_degraded_text`，均带 trace_id、课程/词库摘要、cache_key 前缀、耗时和错误类别；不得记录音频原文或密钥。管理员强制刷新必须审计操作者。


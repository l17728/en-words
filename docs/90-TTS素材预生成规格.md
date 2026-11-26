# TTS 素材预生成规格

## 目标与范围

在进入课程前，按课程词条、朗读规则和当前 Profile 计算所需语音素材清单；已存在且可读的素材直接复用，缺失素材进入可恢复的预生成任务。任务完成后才允许语音课堂开始，并在页面显示扫描、生成、完成或失败进度。ASR 是实时输入，不缓存识别结果，也不纳入本规格。

本规格建立在现有 `SpeechAsset`、`synthesizeCached`、`ModelProfile` 和 `/api/v1/speech/synthesize` 之上。任务按固定 service/model 能力由服务端本地 provider 或已登记的驻留执行器完成，并汇入同一缓存与课堂门禁。ASR 是实时输入，不缓存识别结果。8 人真实容量验收保持暂停，不作为本特性的通过条件。

## 缓存身份与复用

缓存键由规范化语言、实际送入 TTS 的朗读文本、TTS 服务 ID、模型、声线和朗读规则版本组成，并使用 SHA-256。当前首版声线为 `default`；新增声线时必须进入键。不得包含用户 ID、课程 ID 或 Profile ID；不同 Profile 的有效服务参数相同即可复用。英文文本先应用 `NormalizeForSpeech` 规则；中文使用课堂相同的首条释义并移除词性前缀。朗读文本、模型、声线或规则变更会产生新键。文件必须是完整 WAV，且大小与 SHA-256 校验通过后才视为 `ready`；文件采用临时文件写入后原子改名。

## 任务状态

每个预生成请求以 `(cache_key, manifest_revision)` 幂等合并：`pending → running → ready|failed|canceled`。SQLite 任务表使用唯一索引、attempt token 和 lease expiry；worker 以条件更新领取，过期租约由后续进度读取触发恢复。进程内素材 worker 默认最多并发 2 个；模型客户端另有 TTS 并发闸门。重复提交合并任务，完成项不重复生成。失败或取消项可显式重试；记录尝试次数、错误类别、时间和 trace_id。管理员按词条强制刷新只使当前有效服务/模型键失效。

## 课堂门禁

`scan` 返回总数、ready、missing、failed 和 manifest revision。课程快照来自服务器端课程批次和当前有效 Profile；revision 对去重后的朗读文本、语言及 cache key 排序后计算。`ensure` 创建或复用任务；客户端顺序轮询并检查 manifest revision，不接受旧清单响应，显示固定高度进度区和失败类别。进度 ETag 表示完整响应状态（包括计数、尝试次数和错误类别），不是仅代表 manifest。服务端本机 TTS 可用时，门禁在普通 classroom `enter` 状态迁移前检查当前 Profile 缓存；未 ready 返回 `speech_materials_pending` 且不推进课堂。明确选择文字模式会写入 `speech_material_degraded_text` 审计事件。

## 可观测性与安全

记录 `speech_material_scan_started`、`speech_material_cache_hit`、`speech_material_job_started`、`speech_material_ready`、`speech_material_failed`、`speech_material_gate_blocked`、`speech_material_cancelled`、`speech_material_degraded_text`。请求事件通过 trace_id 关联任务；失败日志只记录错误类别，不记录完整合成文本、音频、密钥或绝对文件路径。管理员强制刷新审计操作者。


## 并发与生命周期补充

相同 cache key 在共享缓存目录中通过内核文件锁串行化，锁等待响应请求取消；合成成功后再原子发布 WAV 与 SQLite 元数据。管理员强制刷新与普通预热使用相同锁。服务器关停取消并排空素材 worker 后关闭数据库。损坏素材和 `failed` 资产必须允许重建并更新原有资产行。

缓存访问还会校验资产记录中的文件路径必须精确落在配置的 cache root/key 路径下；取得 per-key lock 后清理同 key 崩溃遗留的临时文件，避免误读外部路径及缓存目录逐步堆积。

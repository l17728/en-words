# TTS 素材预生成 SDD

## 现状接线

`server/internal/api/speechcache.go` 已提供按键缓存、进程内合并锁和原子文件写入；`store.SpeechAsset` 保存文件元数据。`store.ModelProfile` 将文本、ASR、中文 TTS、英文 TTS 绑定为可持久化 Profile。新功能复用这些模型，不改变现有同步合成接口。

## 组件与数据流

1. **Manifest Builder**：读取课程快照中的词条和朗读变体，按当前 Profile 解析中英文朗读文本，去重后生成有序 manifest。
2. **Material Coordinator**：对每个 `(cache_key, manifest_revision)` 执行数据库幂等 upsert；已有 `ready` 且文件校验通过则跳过，否则创建任务。新增 `speech_material_jobs`（唯一键、状态、attempt、lease、错误摘要、时间戳）和必要索引。worker 以事务条件更新领取，租约过期可恢复；进程内 key 锁只是减少争用，不能替代数据库 claim。上游调用必须受 TTS 并发闸门保护。
3. **Progress API**：提供 `POST /classroom/lessons/:id/speech-materials/scan`、`POST .../ensure`、`GET .../progress` 和 `POST .../cancel`。响应包含 `manifest_revision`、计数、状态、可开始标志和降级原因；支持 `If-None-Match`，revision 不匹配返回冲突。权限沿用课堂所有者/管理员校验，cancel 只取消当前 revision 的未完成任务。
4. **Classroom Gate**：普通 classroom `enter` 的事务状态迁移前调用 coordinator；未 ready 时返回可重试的 `speech_materials_pending`，不得推进教学状态。文字模式显式携带 `mode=text`，要求用户确认并记录降级事件。迟到的旧 revision 结果在写入时被拒绝。
5. **Resident Adapter（后续）**：服务端只发出带 profile/service 能力的出站任务；驻留程序回传状态和音频摘要。不得开放未认证的入站本地端口。第一阶段保留接口抽象，不阻塞本地闭环。

## 一致性与恢复

任务领取使用短租约和 attempt token；服务启动 worker 扫描并恢复过期任务，SQLite 使用事务和条件 UPDATE 保证跨进程一致。进程退出后超时任务可重新领取。状态转移必须校验旧状态，迟到结果不能覆盖新 revision。音频 hash、RIFF 完整性和文件存在性是 `ready` 的必要条件。删除或强制刷新先标记旧 revision，再生成新 revision，旧音频仍可被正在播放的课堂使用。

## API 约定

错误码至少包括：`speech_materials_pending`、`speech_materials_failed`、`speech_materials_revision_conflict`、`speech_materials_text_fallback`。轮询响应禁止返回密钥、绝对服务器路径和完整音频内容；只返回状态、百分比、错误类别和重试建议。

## 验收门

Go 单测覆盖键稳定性、跨 Profile 复用、并发合并、失败重试、租约恢复和 revision 冲突；核心 coordinator 覆盖率 ≥90%。Playwright 覆盖真实登录、进入课程、进度显示、门禁、重试、文字降级、Profile 切换和两名学生并发。日志事件可由 trace_id 串起扫描到课堂开始的完整链路。


## 驻留程序安全边界（后续）

首版不实现驻留程序。后续 adapter 必须先注册设备能力，使用短期 token 或 mTLS；服务端仅通过出站长轮询/WS 下发任务。每个任务绑定 nonce、attempt token、profile/service 能力和最大音频大小，回传必须校验 hash、revision 和租约，拒绝重放。禁止开放未认证入站端口，密钥不写日志。

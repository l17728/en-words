# TTS 素材预生成 SDD

## 现状接线

`server/internal/api/speechcache.go` 提供按键缓存、进程内合并锁、文件校验和原子写入；`store.SpeechAsset` 记录素材元数据；`store.ModelProfile` 提供当前有效的中英文 TTS 服务/模型。预生成复用该缓存，不替换同步合成 API。ASR 是实时输入，不缓存。

## 组件与数据流

1. **Manifest Builder**：读取课程批次词条。英文使用 `NormalizeForSpeech`；中文使用课堂相同的首条释义并清除词性前缀。缓存键包含实际送入 TTS 的文本、语言、服务 ID、模型、声线和规则版本；不含用户、课程或 Profile ID。有效参数相同的 Profile 可共享。
2. **Material Coordinator**：以 `(cache_key, manifest_revision)` 幂等创建持久 job。ready 必须同时有可读文件、WAV 头、正确大小和 SHA-256；校验失败的记录标为 failed，并允许重新生成。worker 使用唯一索引、attempt token 和条件 UPDATE 领取；App 内最多并发 2 个素材任务，上游 TTS client 仍有独立并发闸门。
3. **Progress API**：学生可调用 scan、ensure、progress、cancel。响应包含 manifest、计数、错误类别、尝试数、取消数和可开始标志。ETag 对完整进度状态计算；状态变化时返回 200，内容未变时才返回 304。越权学生只能访问自己的课程。
4. **Classroom Gate**：持久化与本机 TTS Provider 可用时，普通 classroom `enter` 在 Room 状态迁移前校验当前 Profile 的素材；未 ready 返回 `speech_materials_pending`，不推进课堂。没有本机 TTS Provider 时保留浏览器本地语音通路。文字降级需显式携带 `mode=text`，并记 `speech_material_degraded_text`。
5. **Resident Adapter（已实现）**：服务端通过受限 HTTPS API 签发一次性 enrollment，驻留程序主动出站领取固定 service/model/voice/language 任务，在本机 loopback Model Console 合成 WAV，并通过限额 reservation 上传；服务器复用既有 cache key、manifest 和 ready 门禁。无入站 listener。管理员可登记 capability、轮换或撤销设备令牌。

## 一致性与恢复

job 的 claim 和完成都检查状态及 attempt token；取消后迟到 worker 不能覆盖 canceled 状态。进度请求发现过期 running lease 时重新排队，因此进程重启后用户再次打开课程即可恢复。已缓存文件的 SHA-256、大小、WAV 头和可读性都须通过。强制刷新只失效当前有效 cache key，其他 Profile 的缓存保留。进度轮询顺序检查 manifest revision，拒绝旧 revision 的响应。

## 可观测性与安全

scan、cache-hit、job-started、ready、failed、gate-blocked、cancelled、text-degraded 事件使用 trace_id 串联。日志记录错误类别，不记录音频、完整朗读文本、密钥或绝对路径。管理员强刷记录操作者。

## 自动验收记录（2026-11-23）

- SQLite 隔离后端、本机可控 TTS HTTP stub、headless Chromium：TTS 预生成 E2E 13/13 通过，覆盖扫描、ETag/权限、缓存命中、门禁、规则文本、跨学生复用、Profile 隔离与回切、管理员强刷、失败重试、取消迟到结果、固定进度区、旧 manifest 拒绝和文字降级。
- Go `go test ./... -count=1` 与 `go vet ./...` 通过；TTS 素材相关 race 用例通过。
- 真实 8090 模型平台、跨机器 resident adapter、真人课堂和 8 人容量仍是独立环境验收，不以 stub 结果替代。

## 驻留程序验收（2026-11-23）

- 执行器入口为 `server/cmd/speech-agent`。enrollment/agent token 仅由服务端保存哈希；驻留端凭据和 WAV spool 使用权限受限文件；HTTPS 校验证书，Model Console 仅允许 loopback origin。
- SQLite manifest revision、agent/job/attempt/lease、WAV digest、reservation 和 revoke 均有事务检查；续租覆盖合成、reservation 和上传。撤销或轮换后旧 token 请求被拒，活跃 claim 与 reservation 被 fence/release。
- 双独立 CLI 进程通过隔离 SQLite、TLS 测试服务及 loopback 模型 stub 完成课堂素材生成；相同缓存项只合成一次，全部 ready 后课堂可进入。
- 管理员驻留设备面板支持手工输入 service/model、选择 en/zh 能力、一次性 enrollment 显示、设备列表、令牌轮换和撤销。
- stub 结果不代表生产远端模型发音质量或真人课堂验收。

## 并发与关闭保证（2026-11-23 补充）

缓存文件写入使用 cache-key 粒度的本进程互斥与跨进程内核文件锁；获得锁后再次检查 SQLite 与 WAV 文件，避免共享目录上重复合成及元数据/文件散列错配。WAV 通过临时文件写入并原子改名。取消上下文可中止等待锁。服务器关闭时先停止接受请求、取消排队和执行中的素材任务并等待 worker 退出，然后才关闭 SQLite 连接池；等待超时会阻止连接池提前关闭。

回归覆盖：跨进程持锁/超时/持有者退出释放；关闭时队列中的 worker 取消且不领取任务；损坏或 failed 的缓存重新合成后以 upsert 恢复 ready 与 SHA/大小元数据。

## 本机模型服务冒烟（2026-11-23）

通过 `127.0.0.1:8090` 的实际 TTS HTTP 服务合成固定公开文本：英文 `cat`（21,036 bytes）与中文 `猫`（13,868 bytes）均返回 HTTP 200，RIFF/WAVE 头有效。该结果只证明本机 provider 连通并返回 WAV，不代表人工音质验收或课堂并发容量结论。

最终复验补记：13 条素材 E2E 全部重复运行一次，共 26/26 通过；前端 build 与 lint 通过。独立架构、前端/E2E、安全评审均未发现 P0/P1/P2。新增子进程合成测试覆盖共享 SQLite 和缓存目录，断言相同 key 仅触发一次 TTS 且 ready 文件散列/大小一致。真人课堂、主观音质和多设备容量仍需线下验收。

代码继续补充了缓存失效、强刷、provider 缺失/错误 WAV，以及素材 worker 成功/失败的回归用例。当前 Go API 包整体覆盖率实测为 77.5%，低于历史 80% 门槛；缺口主要分布在既有 WebSocket/教师分析等 API 路径，不能以本特性的测试通过替代该全包门槛。因此语音特性专项自动化通过，但仓库全量覆盖率质量门尚未封板。

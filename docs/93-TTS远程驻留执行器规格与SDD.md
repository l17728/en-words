# TTS 远程驻留执行器规格与 SDD

状态：设计评审完成；后端协议、驻留 CLI、管理员面板和自动化验收已进入实施。最终验证以文末实施记录为准。生产远端模型与真人课堂仍属于环境验收，不由隔离 stub 代替。

## 目标和边界

单节点远端叮当words服务通过本机主动建立的安全连接，使用受信任电脑上的 Model Console 合成公开词库中的中英文朗读素材。server 的 SQLite DB 与 cache volume 必须由同一主机上的所有 server process 共享；首版不支持多主机 server 集群/分布式文件系统。复用 `docs/90` manifest、cache key、预生成进度和课堂门禁。ASR 是实时输入，不由本执行器处理或缓存。首版不向驻留端发送音频文件、学生录音、账号凭据或任意命令。

驻留端只向管理员配置的服务 origin 发起 HTTPS 长轮询，适配 NAT/防火墙，不开放本机入站端口。生产环境必须验证 TLS 证书；禁止明文 HTTP 和跳过证书验证。无安全连接时执行器保持离线，学生可显式文字降级。

## 持久模型

- `SpeechAgent`：随机 ID、显示名、获批 `(service_id, model, language)` 能力、token hash、创建/心跳时间、撤销时间。token 仅注册/轮换时显示一次；服务端只存哈希。管理员可撤销、轮换 token、收窄能力。
- `SpeechAgentEnrollment`：服务实例绑定、获批能力、token 哈希、到期时间和消费状态。管理员签发与 agent 兑换分属不同接口；并发兑换在事务中只允许一个成功。
- `SpeechMaterialJob` 固化 manifest revision、service/model/voice/language、执行器能力、`agent_id` 和 attempt fencing 信息。有效提交须匹配 revision、running 状态、未撤销 agent、未过期 lease 和当前 attempt token。
- `SpeechAgentClaimRequest` 以 `(agent_id, request_id)` 唯一，保存领取响应引用；领取响应丢失后相同 request ID 返回同一 claim，避免重复领取或等待整个 lease 超时。
- `SpeechAgentResultReceipt` 以 `(job_id, attempt_token)` 唯一，记录 agent、内容 digest、accepted 状态和时间。提交响应丢失后，同 digest 重试返回原收据；不同 digest 冲突拒绝。
- `SpeechUploadReservation`：每次 result 上传先在 SQLite writer tx 中按 reservation ID 保留 agent、job、attempt、最多 10 MiB 和两分钟 expiry；同时保护全局 1 GiB 的 orphan+active-staging 配额和每 agent 两个并行上传名额。commit/reject 后释放；崩溃后 expiry 后可清理。跨进程限流计数在 `SpeechAgentRateWindow` 中按 agent/minute 原子递增。
- 执行器配置包括经证书验证的服务 HTTPS origin、enrollment/agent token、loopback Model Console 地址、获批能力和并发上限。密钥放 OS secret store 或权限受限配置文件，禁止进入日志。

cache key 仍由实际朗读文本、语言、服务、模型、声线和规则版本决定，不含 agent/device ID，因此相同能力跨学生、课程和设备复用。

## 协议与状态

1. 管理员通过 `POST /api/v1/admin/speech-agents/enrollments` 签发短时 enrollment token，并预先批准服务/模型/语言能力。agent 只连接管理员配置的 HTTPS origin，以正常系统根证书验证服务端，再通过 `POST /api/v1/speech-agent/enroll` 兑换。token 绑定服务实例、能力与过期时间，明文只返回一次。
2. agent 使用 bearer token 调用 heartbeat，报告版本、健康状态和并发数。服务端只派发能力交集内任务。管理员界面说明该设备可访问词库朗读文本和本机 Model Console，不可读取学生录音或课堂状态。
3. job 创建时固化该朗读项的 service/model/language。按能力路由：本地 provider 和远端 agent 可以同时在线，但都通过相同的 SQLite 条件 claim 竞争同一 pending job；只有 job 固化能力匹配的执行器能领取。claim 同时保存 agent-scoped request ID 与 claim receipt。相同 ID 在 lease 有效时重放返回原 claim；lease 已过期时返回 `claim_expired`，要求客户端使用新 ID。签发新的 attempt token 和 60 秒 lease；并发领取同一时刻至多一个有效 owner。
4. agent 每 15 秒调用 `POST /api/v1/speech-agent/jobs/:id/lease`，绑定 `job_id + attempt_token`。续租事务要求 agent active、claim owner/attempt 匹配、job running、revision 当前且 lease 尚未过期；每次续 60 秒。撤销与续租同 SQLite writer 串行。过期 attempt 必须停止/取消本机请求且不得上传；模型调用不支持及时取消时，只能丢弃结果，服务端 fencing 拒绝旧结果。
5. 任务只含 job ID、manifest revision、attempt token、语言、朗读文本（最多 4096 UTF-8 bytes）、服务/模型/声线、音频上限（10 MiB）和 90 秒 deadline。agent 仅访问本地配置的 loopback Model Console，拒绝未配置能力、命令、路径或任意 URL。
6. agent 先以 agent/job/attempt 身份请求上传 reservation。服务端用一个 SQLite writer tx 验证当前 claim，并原子预留最多 10 MiB，检查全局 `active staging + unindexed blob + reservation` 总额不超过 1 GiB，检查每 agent 两个并行 reservation 和 30 次/分钟请求限流。若配额或限流不通过，不创建 reservation。上传最多 10 MiB、30 秒读取时限，采用唯一 reservation ID 命名 staging 文件；接收后校验 MIME、完整 WAV、实际大小、SHA-256 和获批能力。使用不可变内容寻址文件 `<cache-key>.<audio-sha256>.wav`：写临时文件、fsync、原子 rename、同步目录；然后在同一 SQLite writer transaction 中检查 agent 状态、attempt、revision、lease 与 job 状态，再提交 asset 指针、ready 状态和 result receipt，并释放 reservation。旧 asset 指针在事务提交前不变。DB 提交失败或进程崩溃最多留下无索引 blob；无 DB ready 记录的文件绝不视为 cache hit。后台 GC 仅清理至少 10 分钟、未被 asset 引用且无有效 upload reservation 的 staging/blob，并在删除前再次检查数据库引用与 reservation。
7. revoke 与 result commit 通过同一 SQLite writer 串行化：result 先提交则资产有效；revoke 先提交则结果拒绝。相同 attempt 和 digest 的响应重试幂等；不同 digest、已过期或旧 attempt 拒绝。lease 过期且旧 agent 仍在推理时可能重复推理，只承诺同一时刻至多一个有效 attempt、只有一个结果可发布。
8. agent 本地用权限受限的 journal 持久保存 active request ID 和已合成但尚未获 receipt 的 WAV/digest。WAV 先写临时文件、fsync、原子 rename，再上传；agent 重启后先恢复 journal/spool。相同有效 claim 重试取回原 attempt；result 响应丢失时重传同一 digest，不重复调用模型。相同 digest 已有 accepted receipt 时，重复预留请求返回原 receipt 并释放新 reservation。收到 accepted receipt 后才删除 spool。失败仅使用固定类别（unavailable/timeout/invalid_audio/provider_error），不回传栈、请求头、密钥或 provider 原文。失败须用户显式 ensure/retry 后再排队；能力收窄先 fence 旧 attempt，再按新能力重排。撤销提交后立即阻止新 heartbeat/claim/lease/result。SQLite 写事务等待上限 2 秒；超限返回不含敏感信息的 retryable busy，heartbeat/claim 不能无限阻塞课堂 API。

## 安全、不变量和日志

- 管理员创建 enrollment、撤销、轮换端点要求 admin role；agent 端点仅接受有效 token，恒定时间比较。enrollment 签发限每管理员每分钟 3 次；agent 最多 2 个并发任务，长轮询最长 20 秒。
- 单 agent 结果提交限每分钟 30 次、最多 2 个并行上传；服务端无索引 blob 总占用达到 1 GiB 时暂停所有新的 result 接收并回传 retryable quota 类别，GC 回收后恢复。限额拒绝不写 ready。
- token 路由不写 request body/header；访问日志仅记录固定 route、状态、耗时和 trace ID。轮换响应丢失时管理员重新轮换，旧 token 立即失效；不提供读取旧 token 的接口。
- 所有请求限制 body、并发、等待时间、文本长度及音频大小。agent 不可读取学生、课堂或其他 Profile 数据；只获批能力可派发。
- SQLite 条件事务为 claim、revocation、结果提交的线性化点。cache 文件路径固定在服务端 cache root，不接受客户端路径。
- 过期元数据清理有界：rate-window 保留 48 小时；claim receipt 在 job terminal 后保留 30 天、非 terminal claim 至少保留 lease expiry 后 24 小时；enrollment 审计保留 90 天；过期 reservation/staging 可立即回收，但必须先确认无活跃 upload。
- 日志含 trace_id、agent_id、job_id、attempt、event、耗时和固定错误类别；不记录 token、音频、完整文本、绝对路径和任意上游错误。
- agent 失联不能推进课堂；等待状态由现有预生成 UI 展示，文字降级仍需用户明确操作。

## 运维启动步骤

1. 在驻留电脑确认 Model Console 仅监听 loopback，并从模型服务列表取准确的 service ID/model；管理员页输入这些 ID，选定语言与 voice 后签发一次性 enrollment。
2. 编译执行器：`cd server && go build -o speech-agent ./cmd/speech-agent`。运行时设置 `ENWORDS_SPEECH_ENROLLMENT_TOKEN`，并指定 `-server https://<server-origin> -console http://127.0.0.1:8090 -name <设备名>`。私有 spool 默认位于系统用户配置目录下的 `DingDangWords/speech-agent`。
3. enrollment 成功后，执行器在 spool 中保存权限受限的 agent token；移除 enrollment 环境变量后重启可复用该凭据。管理员轮换 token 后，使用 `ENWORDS_SPEECH_AGENT_TOKEN` 覆盖新 token 启动；旧 token 与旧 attempt 立即失效。
4. agent 在线后，管理员可在 model services/profile 页面选择其获批服务，应用 TTS profile；学生课前扫描并预生成素材。服务断连时课堂不会自动推进，用户可选择现有文字降级。

只把公开词库的朗读文本送到驻留端；不要用学生录音、密码或私人文本做验收。自签名服务证书应通过 `-ca-file` 添加 PEM CA，不得关闭证书验证。

## E2E 与验收门

测试使用隔离 SQLite、两个独立驻留进程、TLS 测试服务端、可控 loopback Model Console stub 和独立浏览器会话；仅用公开词条。

| ID | 场景 | 必须断言 |
|---|---|---|
| RA-01 | 注册与秘密 | 过期/并发重复兑换、错误角色拒绝；DB/日志无 token；轮换后旧 token 失效。 |
| RA-02 | 能力批准 | 只能领取获批服务/模型/语言任务；能力收窄会 fence 在途任务。 |
| RA-03 | TLS 与出站连接 | 错误证书拒绝；agent 仅出站连接且本机无监听端口（部署测试证明）。 |
| RA-04 | 多 agent 竞争 | 并发 claim 同时仅一个有效 attempt；响应丢失后相同 request ID 恢复同一 claim；lease 过期允许重复推理，但旧 attempt 被 fencing，只有一个结果入 cache。 |
| RA-05 | 成功与完整性 | 正确 WAV/hash/size 后 ready，进度完成，课堂门禁放行。 |
| RA-06 | 结果与上游失败 | 错误 MIME、畸形 WAV、大小/hash 不符、超限、provider/缓存写失败、原子 reservation 并发超额均不 ready；错误类别安全且可重试。 |
| RA-07 | 取消、租约与迟到 | 领取响应丢失可安全重试；取消/过期/reclaim 后旧 attempt 被拒；同 digest 重试返回原 receipt且释放新 reservation，本地模型只调用一次；agent 重启后从 durable spool 继续上传。 |
| RA-08 | 撤销与重放 | revoke/result race 按 writer 顺序线性化；重复 enrollment、不同 digest 重放和跨 agent token 拒绝。 |
| RA-09 | 断网降级 | 等待 agent 时课堂状态不推进；显式文字降级后刷新仍保持一致并有审计事件。 |
| RA-10 | 复位与关闭 | 服务/agent 重启后任务恢复；关停停止新领取，DB 不提前关闭。 |
| RA-11 | 隐私日志 | trace_id 可串联全流程，无 token、音频、完整文本或学生数据。 |
| RA-12 | 管理员设备操作 | Playwright 验证 enrollment 负载精确匹配 TTL/service/model/language/voice；令牌只显示一次，并覆盖轮换、撤销。 |
| RA-13 | 重放并发上传 | 已接受收据可同 digest 幂等重试；同一 reservation 的并发 PUT 在读 body 前拒绝；不同 agent 的所有 active/uploading 字节共享 32 MiB 上限。 |
| RA-14 | 上传期间 lease 到期 | 控制 body 读取暂停至 claim lease 过期；释放 body 后提交仍被拒绝，不写 SpeechAsset.ready。 |

课堂状态断言：RA-05/09 必须在等候 agent 前后对比相同 lesson 的 phase、turn/epoch、word/question ID、计时基准和持久化快照；素材未 ready 时这些字段严格不变。RA-09 刷新后必须继续使用同 manifest revision，agent 恢复后只完成缺失项。

文件恢复断言：分别注入临时文件写/fsync/rename 失败，以及 rename 成功后 SQLite transaction 失败/进程崩溃。前者不得产生 asset/ready；后者只能留下无索引不可播放 blob，旧 asset 指针不变；重启不得将 orphan 当 ready，宽限期 GC 只能删除未引用文件。并发上传测试在同一主机两个 server process、共享 SQLite 文件与 cache volume 下证明 quota reservation/rate-window 原子；active staging 有持久 reservation，崩溃后仅在过期时由 GC 回收。配额拒绝与幂等重复收据路径均断言 reservation 释放。

并发时限断言：claim、lease、revoke、result 均使用有界 SQLite writer 等待；竞争超过 2 秒返回 retryable busy，不得绕过 fencing、更新部分状态或饿死课堂请求。

通过门：架构/后端/前端/测试/安全/运维评审无遗留 P0/P1/P2；单测锁签发、哈希、撤销、能力过滤、结果验证与状态迁移；双进程 SQLite 集成覆盖 claim、replay、revoke、lease、提交响应丢失和崩溃窗口；Playwright 覆盖 RA-01/02/04/05/07/08/09/10/12，且等待 agent 时证明课堂不推进；全量 Go、race、前端 build/lint、E2E 通过。证书拒绝与无入站监听由执行器集成/部署测试证明。真实远程 TLS 部署和真人课堂是独立环境验收，stub 不代替它们。

## 评审记录

- 2026-11-23 最终复审：后端、前端、安全三个领域复审均为 GO；针对上传并发额度、慢 body 到期提交、token 轮换和双驻留进程认证路径的发现已修复并回归。无剩余代码级阻断项。
- 安全边界复核：agent token 只存本机受限 spool，管理面只展示一次性 enrollment/rotation token；日志不输出凭据/音频；TLS 校验保持开启；异常 SQLite contention 返回可重试 503，不再误报为认证失败。

## 实施与验证记录（2026-11-23）

- 已实现管理员 enrollment/list/rotate/revoke API 与管理面板；service/model 可手工指定，也可从已有服务建议填入，减少服务端未接入驻留端模型时的循环依赖。
- 续租持续覆盖完整上传生命周期；manifest revision 在领取/续租/预留/提交时 fencing；metadata 清理保留仍有活跃 lease 的 claim receipt；撤销后的 receipt replay 检查 agent 状态。令牌轮换事务使旧身份失效并重排活跃 claim。未开始上传的 reservation 立即释放；正在读取的上传保留配额直到完成或超时，提交事务会拒绝旧身份。
- WAV 上传总在途 reservation 限制为 32 MiB（每份最大 10 MiB），与总缓存 1 GiB 配额分离；所有 body PUT（含已接受收据重放）都先取得同一共享额度，单 reservation 不允许并发读取。该机制限制最大同时读取数，Go heap 分配因扩容可能高于 reservation 字节。
- （历史记录，2026-11-23 最终回归前）隔离双进程 TLS/SQLite 课堂 E2E 与定向后端回归通过；管理员 Playwright 用例经首轮 selector 修正后 2/2 通过，并断言精确 enrollment payload；前端构建通过。RA-13/14 增加了 API 集成回归。最终快照结果见下方“最终代码快照回归”。
- 新增锁定回归：`TestSpeechAgentUploadReservationsBoundAggregateInflightBytes`、`TestSpeechAgentHTTPUploadReservationValidationAndIdempotentReceipt`（含同 reservation 并发重放 body 未读取断言）、`TestSpeechAgentUploadExpiredLeaseDoesNotCommitAfterSlowBody`、`TestRotateTokenFencesAuthenticatedSnapshotAndRequeuesActiveWork`、`TestRunnerHeartbeatStopsOnRevokedCredential`。
- 未由自动化证明：真实远端 TLS 部署、实际硬件/模型发音质量、真人课堂声学和多人容量；按安排留待自动开发验证结束后现场试点。
- 2026-11-23 最终代码快照回归：`go test ./... -count=1` PASS（含 `internal/api` 94.692s、`internal/store` 40.323s，其余包全绿；`cmd/speech-agent` 无独立测试）；`go vet ./...` PASS；关键竞态集 `go test -race ./internal/api ./internal/speechagent ./internal/localmodel -run 'TestTwoResidentProcesses|TestSpeechAgentUpload|TestRotateToken|TestCleanupExpiredMetadata|TestRunner' -count=1` PASS（api 25.153s、speechagent 7.116s、localmodel 7.237s）。
- 最新前端回归：`npm.cmd run build` PASS（Vite 3138 modules）；`npx.cmd playwright test -c playwright.speech-agents.config.ts` PASS 2/2；`npx.cmd playwright test -c playwright.tts-materials.config.ts` PASS 13/13；`npm.cmd run lint` exit 0，仍有 10 条既存 warning（`LadderSettings.tsx` 8、`Classroom.tsx` 2），驻留面板新增 warning 已清除。
- Linux 交叉编译：在 Windows 本机以 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/server ./cmd/speech-agent` PASS。首次全量 race 在无进度输出时人工终止；随后 API 定向诊断中，`go test -race ./internal/api -run '^TestLessonAgentDurableSlowCompletionAndReplayAfterRemoval$' -count=1 -timeout=300s` PASS（4.963s），`go test -race ./internal/api -count=1 -timeout=600s` PASS（289.660s）。最终结构化全量回归 `go test -race ./... -count=1 -timeout=900s -json` PASS，约 6m20s（21:07:41–21:14:01）；26 个有测试的包全部通过，`cmd/speech-agent` 与 `internal/pilotsupervisor` 无测试文件。较慢包为 `internal/api` 357.484s、`internal/store` 215.57s；JSONL 证据保存在 `.test-artifacts/go-race-all-20261123.jsonl`，无失败事件或遗留测试进程。此前 110s 的 API 包超时是套件执行时间超过短时限，不是单测死锁，也未发现 race detector 数据竞争。与驻留认证、上传、双进程竞争直接相关的 race 集合也 PASS（见上）。
- 环境验收边界：本地 TLS 双进程测试使用测试证书和 stub Model Console；真实远端 TLS 部署、目标硬件上的模型/时延，以及真人课堂声学与多人容量仍属于现场环境验收，自动化结果不替代这些验收。

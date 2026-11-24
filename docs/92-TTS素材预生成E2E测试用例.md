# TTS 素材预生成 E2E 测试用例

测试使用 isolated server harness、隔离 SQLite、可控 Model Console HTTP TTS stub、固定词条和管理员/多学生账号，运行 headless Chromium。stub 提供 `/api/models`、`/api/tts/speech` 和 `/api/tts-en/speech`，可控制失败/延迟并计数合成调用；音频使用固定 WAV fixture。ASR 不在本套缓存断言中。每例使用独立词条，禁止依赖运行顺序。

| 编号 | 场景 | 核心断言 |
|---|---|---|
| E2E-TTS-01 | 全部命中 | 扫描显示 `ready=total`，课堂立即进入；上游合成调用数为 0。 |
| E2E-TTS-02 | 部分缺失 | 进入课程显示固定高度进度区和百分比；开始按钮禁用，完成后只启用一次。 |
| E2E-TTS-03 | 两名学生共享素材 | 相同有效服务参数只产生一次上游合成；两名学生均能开始且无跨用户课堂数据。 |
| E2E-TTS-04 | Profile 等价切换 | 切换到相同 TTS 服务/模型/声线/规则的 Profile 全部命中；不同模型产生新任务。 |
| E2E-TTS-05 | 规则或内容变更 | 朗读文本、规则版本或词条内容 revision 改变后必须 miss，旧音频不可冒充 ready。 |
| E2E-TTS-06 | 上游失败与重试 | 首次失败显示错误类别和重试；重试成功后恢复进度，不重复已完成项。 |
| E2E-TTS-07 | 中途刷新/重启 | 页面刷新或服务重启后按持久状态恢复，running 过期任务可重新领取，计数不回退。 |
| E2E-TTS-08 | 取消与迟到结果 | 取消后迟到音频不能覆盖新 revision；课堂保持门禁或明确进入文字模式。 |
| E2E-TTS-09 | 文字降级 | 语音服务不可用时，用户选择“使用文字继续”才可开始；记录降级事件，界面不显示语音就绪。 |
| E2E-TTS-10 | 管理员强刷 | 管理员按词条强制刷新后仅新 revision 重新生成，普通学生无刷新权限。 |
| E2E-TTS-11 | 轮询可靠性 | 进度响应乱序、重复或短暂超时不回退 UI；最终状态以 manifest revision 为准。 |
| E2E-TTS-12 | 观察证据 | 测试从 trace_id 读取服务端日志，验证 scan/job/cache-hit/ready/gate 事件链完整且不含密钥或原文。 |
| E2E-TTS-13 | 驻留执行器管理 | 管理员签发短时 enrollment、仅显示一次令牌；设备可轮换和撤销，普通用户无操作入口。 |

执行细则：Playwright 用例在 `web/e2e/tts-material-prewarm.spec.ts`，共 13 条，分别锁定扫描、状态 ETag 与课程权限、缓存命中、未就绪门禁、朗读规范化文本、跨学生共享、Profile 隔离/回切、强刷权限、失败重试、取消后迟到结果、固定进度区、旧 manifest 响应拒绝、文字降级。E2E-TTS-07 的 SQLite 关闭/重开过期 lease 恢复由 `TestSpeechMaterialProgressRecoveryReclaimsExpiredLease` 覆盖；E2E-TTS-12 的 trace 与脱敏由 `TestSpeechMaterialFailureLogsRetainTraceAndSafeCategory` 覆盖。旧 revision 不一致由浏览器响应拦截注入；完整并发/重启由 Go 测试补齐。

通过标准：Playwright 13/13 通过，Go 单测/集成/竞态用例通过，关键 Playwright 场景连续无 flaky 重跑 2 次；E2E-TTS-02、03、06、07、08、09、11 均须有对应 Go 用例。真实外部 TTS 服务和真人课堂属于另外的环境验收，不用 stub 结果替代。任何上游 429、网络断开或浏览器刷新不得造成重复计费式合成或课堂状态推进。跨学生共享和权限隔离是阻断门。

## 2026-11-23 自动化验收

- `web/e2e/tts-material-prewarm.spec.ts`：13/13 通过，headless Chromium + 隔离 SQLite + 本地 TTS stub。
- `go test ./... -count=1`、`go vet ./...` 通过；`go test -race ./internal/api -run 'TestSpeech(Manifest|Material|Cache)|TestLessonSpeechTexts' -count=1` 通过。
- 当前验收不覆盖本机 8090 模型平台的真实发音质量、resident adapter、真人课堂及 8 人容量。


## 新增后端回归用例

- `TestSpeechMaterialRoutesScanEnsureProgressCancelAndOwnership`：真实 API Router 扫描、预生成、失败进度、ETag/304、越权拒绝及取消路径。
- `TestSpeechCacheKernelLockSerializesAcrossProcesses`：子进程持锁时第二进程等待至超时；持有者退出后锁可再次获得。
- `TestSpeechCacheSharesSingleSynthesisAcrossProcesses`：两个独立进程争用相同 cache key，只调用一次 TTS，最终 WAV 和 SQLite ready/hash/size 元数据一致。
- `TestSpeechMaterialWorkerQueuedAtShutdownExitsWithoutClaim`：关闭会取消等待槽位的 worker，且关闭后不会再启动任务。
- `TestSpeechCacheRegenerationReactivatesCorruptFailedAsset`：缓存损坏后重生成可将资产元数据恢复为 ready，散列及大小正确。
- `TestSpeechMaterialsReadyRequiresEveryCurrentProfileWAV`：无持久层、未迁移表、全部素材有效、缺失文件四种门禁状态。
- `TestSpeechMaterialWorkerPersistsSuccessAndProviderFailure`：worker 成功与上游失败分别写入 ready/failed 状态，失败保留安全错误类别并递增 attempt。
- `TestSpeechCacheUnavailableAndInvalidProviderOutput`、`TestSpeechCacheRefreshReplacesExistingAsset`：缺失 provider、无效 WAV 拒绝、缓存命中与管理员强刷重新合成。

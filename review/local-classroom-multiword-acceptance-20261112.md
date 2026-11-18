# 本机多词课堂业务闭环验收（2026-11-12）

## 范围与结果

本轮使用隔离 SQLite、两个独立 Chromium 学生 context、8090 本机文本/ASR/中英文 TTS 服务，未发送真人音频或调用云端 ASR。`web/playwright.local-journey.config.ts` 的 7 条用例整组顺跑 **7/7 通过，0 retry，约 3.4 分钟**；机器可读结果在 `web/.test-artifacts/closure-1794481428688-13648/results.json`，服务日志在 `server/.test-artifacts/local-journey/local-journey-7-final.log`。浏览器关闭 trace，附件仅保存脱敏断言结果和计数。

MJ01：两个学生各自学完三词；六个独立英文 WAV 经真实 MediaRecorder 和本机 ASR，逐词 HTTP 200 且语义匹配；复教英文也被再次识别。中文冻结“我会了”经本机 ASR 触发首题正确；两人各 5/5 次 ASR 均匹配冻结 gold。后续同题两次“我不会”按 round 0→1 消耗机会，显式跳过恰好一次；旧 turn 重放被拒。两人各 24 段朗读均有 `playing→ended`、字幕和唯一 ACK，课报、统计、刷新及不同 ID 的下一课均通过。首跑用例误要求复教前后 round 相同，已改为断递增，随后独立复跑及整组均通过。

MJ02a/b：单次 ASR 503 暂停原 turn，文字恢复、重复点击和刷新只推进一次；独立持有的旧 200 响应在新 turn 到达后释放，未再发 `voice_text` 或重复开题。MJ02c/CONF-RAW：旧跟读 REST 晚于新题 WS 时，新题已填草稿和按钮保持；带标签的“我会了”被拒绝，纯整句唯一正确结课。修复前 trace 证明旧 REST 收尾清空了新题草稿，修复后专项与整组均绿。MJ03：疑问、引号和内部标点各以独立账号真实 UI 提交，均走普通判分并非正确。

Go 定点红测曾复现清洗后误确认；修复后定点、全包 `go test -p 1 ./... -count=1`、`go vet ./...`、api/classroom/judge/voicecmd 四包 `-race`、全包覆盖率和前端 `npm run build` 均退出 0。核心覆盖率 api 82.3%、store 81.3%、voicecmd 84.0%、wordbook 82.0%、classroom 90.2%、scheduler 91.6%、judge 97.2%、feedback 94.8%，均过本轮门槛；详情 `.test-artifacts/stage-acceptance/*closure-20261112.*`。独立架构/后端/安全及前端/测试/AI-Agent 复审发现的确认别名、清洗碰撞、旧 REST 草稿竞态、日志关联和用例断言问题均闭合，最终 P0/P1 为 0。旧 JSON `closure-1794480902459-9492` 的复教 gold 附件误标，已废弃，不用于本结论。

## 未验收边界

生产中文 TTS 当前仍是单 worker/短队列；隔离常驻 4 worker 的 N=4/8 HTTP p95 为 2,887/3,092 ms，高于冻结的 2 秒门。与 Go 编译并行时观察到一次 worker 6 秒超时/HTTP 500，空闲重试恢复；容量与长尾未据此通过。文本模型扩展首轮 75/83 中有连续 8 次 8 秒超时，完整复测 100/100 不能抹去故障。真人隔离课堂试点按用户安排在自动验证结束后进行，外放回声、噪声、插话和真人语音质量仍须现场验收。

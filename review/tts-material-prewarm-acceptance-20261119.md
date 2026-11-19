# TTS 预生成阶段验收记录（2026-11-19）

## 开发提交

- `ca72119`：规格、SDD、E2E 用例、评审记录、任务模型和基础 API。
- `7231d31`：生产课堂进入前的素材门禁和固定进度区。
- `96431f7`：持久化 claim、attempt token、lease、迟到结果保护和快速 E2E harness。
- `ea54a24`：语音准备失败时的重试/文字继续入口。

## 自动化证据

- `go test ./internal/api -count=1`：通过，67.293 秒。
- `go test ./internal/store -count=1`：通过，21.239 秒。
- `go test -race ./internal/api -run SpeechManifest -count=1`：通过。
- `npm run build`：通过。
- `npx tsc --noEmit -p tsconfig.json`：通过。
- `npx playwright test -c playwright.tts-materials.config.ts`：3/3 通过，覆盖 manifest 扫描、两学生共享 revision、课堂准备区域。

## 当前验收边界

本阶段自动化已验证本地隔离服务的素材扫描、任务合并、课堂准备显示和基本恢复路径。真实 TTS 上游成功生成、跨机器驻留程序、外部模型 API 和 8 人容量矩阵仍属于独立环境验收，不在本次本机自动化结果中冒充通过。

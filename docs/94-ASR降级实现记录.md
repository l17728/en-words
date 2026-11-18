# 本地 ASR 降级实现记录

日期：2026-11-18

## 目标

课堂语音识别不可用或上游转写失败时，学生仍能完成当前题目。系统不得自动循环重启录音或重复提交；客户端应明确提示可重试，并允许切换到文字输入。

## 实现

- `POST /api/v1/classroom/lessons/:id/transcribe` 在本地 ASR 未配置时返回 HTTP 503。
- 本地 ASR 请求失败时同样返回 HTTP 503。
- 两种失败均保留统一响应封装，并在 `data` 中返回：
  - `fallback: "text"`
  - `retryable: true`
  - `reason: "provider_unavailable"` 或 `"provider_error"`
- 服务端写入 `speech/audio_fallback_available` 结构化日志，包含 lesson、student、trace、原因和降级方式。
- 现有课堂页面继续使用文字输入完成当前题目；重试必须由用户显式触发。

## 验收证据

- Go 回归：不可用、上游错误、正常 ASR 转发三条路径通过。
- Playwright 隔离课堂：在双浏览器 Pilot 旅程中验证 503 降级协议，并继续验证命令幂等（1 passed）。

## 未包含

云端 ASR 自动切换、真实课堂声学矩阵和真人试听仍属于后续验收项；本次只固定本地失败后的确定性降级协议。

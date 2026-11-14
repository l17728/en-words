# 自动化补充验收记录（2026-11-14）

## 双学生真实媒体

- 用例：`web/e2e/two-student-real-media.spec.ts`
- 配置：隔离 SQLite、`-providers local`、本地 8090 模型服务、Playwright Chromium 无头
- 音频：第一名学生使用 `review/audio-fixtures/en/07.wav`（teacher）和 `zh/02.wav`（老师）；第二名学生使用用例内固定的 apple 音频
- 结果：`1 passed (53.6s)`
- 断言：两名学生均完成中英双语课堂、字幕顺序正确、4 段音频正常结束、4 个 `tts_finished` ACK 唯一、ASR/TTS HTTP 全部 200、首答率 100%

## ASR 并发长尾

- 固定音频：`review/audio-fixtures/en/01.wav`（Apple）
- 负载：50 轮 × 4 并发，共 200 请求
- 结果：200/200 HTTP 200，200/200 转写为 Apple，无失败
- 延迟：p50 196 ms，p95 326 ms，p99 431 ms，最大 490 ms
- 备注：独立容量探针显示第 9～10 个同时请求按服务配置返回 429；这是限流保护，不属于错误。

## 结论

本轮自动化补充通过。剩余验收仅为真人试听和真实课堂试点，不再有已知的自动化阻塞项。

# 双学生本机真实媒体课堂验收（2026-11-12）

## 范围与方法

隔离 Go 后端 `:18085`、Vite `:15178`，共用已运行的本机 8090 模型平台。两个独立 Chromium context、账号和课程同时学习不同单词：`teacher/老师` 与 `apple/苹果`。浏览器从冻结的独立 WAV 产生真实 MediaStream，经 MediaRecorder 编码后走本机 ASR；英文与中文朗读均请求本机 TTS。测试未替代真人发音和真实课堂试点。

## 最终配置结果

8090 中文 TTS `maxConcurrent=2`、FIFO 队列生效后，严格 Playwright 用例 **1 passed，33.6 秒**（总运行 36.2 秒）。[测试代码](../web/e2e/two-student-real-media.spec.ts)；逐学生原始证据在 [Playwright results.json](../web/.test-artifacts/closure-1794465415053-16412/results.json) 的 `two-student-real-media` attachment。

| 学生课程 | 首音 | 进入课堂至结课 | 本地播放 | 字幕 | ACK | ASR | 课报 |
| --- | ---: | ---: | --- | --- | --- | --- | --- |
| teacher/老师，lesson 15 | 1,722 ms | 29,726 ms | 4/4 `playing→ended` | teacher×3、老师×1，均在对应播放期间出现 | 4 个唯一回执 | 英文、中文各一次，2×200 | 首答率 100% |
| apple/苹果，lesson 16 | 1,678 ms | 29,722 ms | 4/4 `playing→ended` | apple×3、苹果×1，均在对应播放期间出现 | 4 个唯一回执 | 英文、中文各一次，2×200 | 首答率 100% |

每人 TTS HTTP 2×200；三次英文重复使用已验证的内存 WAV，每段仍独立播放到 `ended`。不同单词、释义、音源、账号与 lesson ID 的字幕和回执各自隔离。

## 先前失败与边界

队列修复前，双学生旅程两次均在约 29 秒结课，但一侧中文 TTS 返回 503，导致只有 3/4 本地播放并回退浏览器语音。第二次失败的 lesson 8 请求在 14:12:20.575 返回 `{"code":503,"msg":"语音合成失败"}`，`x-trace-id=tr-d467e59cf918414f`；[失败 trace](../web/.test-artifacts/closure-1794463926846-11880/browser/two-student-real-media-two-35f20-nd-ASR-lessons-concurrently/trace.zip)。中文服务加入有界 FIFO 并将 8090 并发门设为 2 后，上述双学生严格旅程通过。

本结果只证明 **两名学生、各一词** 的隔离合成媒体闭环。4 人、8 人首音与长尾容量矩阵，以及真人拾音、试听和真实课堂试点尚未通过本用例验收；不得据此宣称它们完成。

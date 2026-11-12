# 中文 TTS 常驻进程隔离协议验收（2026-11-12）

范围：仅验证 `127.0.0.1:18002` 与隔离 Go/Vite 端口 `18089/15179`；模型、二进制和证据在 `.test-artifacts/zh-tts-resident/`。未修改生产 `8002/8090`、其 supervisor 或 `tts.maxConcurrent=2`。

## P01–P07 结果

| 项 | 隔离验收结果 |
| --- | --- |
| P01 | 固定 EWTS v1 字节向量、分块写入及真实“猫” WAV 通过；原生协议 5/5。 |
| P02 | `teacher`、`苹果 apple`、2000 字符边界经 HTTP 通过；真实模型“猫／老师”HTTP 回环 2/2、RIFF/16000 Hz。 |
| P03 | 空/NUL/超长文本、非法 sid/speed、坏 JSON、坏 UTF-8、原生非法字段均被拒绝；随后正常请求可用。 |
| P04 | 半头／半体 EOF、错 ID、坏 WAV/采样率、重复 READY、超长响应触发失败并重建，分块帧解析通过。 |
| P05 | 4 执行＋4 等待时第 9 项返回 429；取消等待项不执行；杀槽后健康状态 `degraded=true`，恢复后为 `false`。 |
| P06 | 双学生双 lesson 的真实 Classroom 浏览器旅程 1/1 通过。A 合成中取消，迟到 A WAV 已返回但 A 的播放器、非空字幕及 ACK 均为 0；B 的真实 Audio `playing=1→ended=1`、字幕和唯一 ACK 均为 1。A/B SHA-256 分别为 `21284432983daccc7e1b5a15402c582da2e98175f9375af412cb0b55f732e087` / `bdb845948773064ed0bd019f6b77bb1cb3e6bacadf5c426f666b64b0bb6b05a3`。浏览器通过 route 注入 provider 能力并将合成请求映射到隔离 18002 fake worker；不代表生产模型音质。 |
| P07 | crash、>6 秒 hang、取消后 worker crash、合成中强杀、Node 连续三次重启均恢复且无 fake worker 孤儿；真实 worker 强杀后同槽重新 READY 并可返回 200。fake HTTP 8/8、真实 HTTP 2/2。 |

## 纠错与证据

首次真实协议测试发现正常请求返回失败码 `03 00`，仅记录的数字为 `n=7424 rate=16000`（“猫”）和 `n=7680 rate=16000`（“老师”）。模型实际输出 16000 Hz，原设计的 22050 Hz 假设已在 worker、Node 校验与 SDD 中改正并复测。Windows 强杀后命名 mutex 可能返回 `WAIT_ABANDONED`；worker 现接受该状态，真实强杀回归已通过。

复现命令：`py -3 -m unittest server.tools.test_zh_tts_resident_protocol server.tools.test_zh_tts_resident_http server.tools.test_zh_tts_resident_real_http -v`；浏览器：在 `web/` 执行 `playwright test -c playwright.resident-tts.config.ts`，设置 `E2E_SERVER_BINARY` 为本轮隔离构建。浏览器明细见 `.test-artifacts/zh-tts-resident/p06-browser-evidence.json`。

后续容量实测见 `zh-tts-resident-isolated-20261112.md`：N=4/8 均无 429/5xx，但 p95 分别为 2887/3092 ms，未达 2 秒目标。真实 8090 supervisor 三次重启门、混合负载、浏览器 N=4/8 首音矩阵与人工试听仍待验收。当前结果不能宣称生产切换或真人课堂试点通过。

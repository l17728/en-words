# 本机语音并发容量试点记录（2026-11-12）

## 范围与基线

仅使用 `review/audio-fixtures/zh/02.wav`、`en/07.wav` 等公开冻结合成样本及英文 `teacher` 短朗读；未传真人音频。按 [并发规格](../docs/60-本机语音多人并发规格与SDD.md)先测 8001 ASR 直连：短样本 8 并发×10 轮 80/80 HTTP 200，p95/p99 593/791 ms；较长冻结样本另 80/80，p95/p99 777/798 ms。显存约 4400 MiB，服务健康。

## 受控配置与首轮故障

配置前 8090 管理平台 `asr.maxConcurrent=1`、`tts-en.maxConcurrent=4`。完整注册表备份在 `.test-artifacts/models.pre-capacity-20261112.json`，SHA-256 为 `E1B8C63A23147C6E34EF621890A5AA086920D83AF01778716C61B29CF834AF2E`。试点改为两项均 8，并把 Piper sidecar 容量设为 8；其他服务配置未改。

首轮英文 TTS 在单独 8 并发为 75/80 HTTP 200、5 个 502；混合 8 ASR+8 TTS 为 62/80、18 个 502。失败请求未进入 sidecar 日志，定位为 Python `ThreadingHTTPServer` 默认连接 backlog 在突发请求时不足。将 `request_queue_size` 设为 32 后重启英文 sidecar。另对客户端断开导致的 `ConnectionAbortedError` 做无正文的预期处理，避免异常栈污染日志。首轮数据留在 `.test-artifacts/capacity_probe_20261112.jsonl`，不计入通过结论。

## 修复后二轮容量矩阵

每种单服务容量与混合组合均运行 10 轮。下表延迟仅统计 HTTP 200；首响应与总耗时相差不超过约 1 ms。

| 请求组合 | 成功/总数 | p50 | p95 | p99 | 最大值 |
| --- | ---: | ---: | ---: | ---: | ---: |
| ASR N=1/2/4/8 | 10/10、20/20、40/40、80/80 | 71/131/193/406 ms | 425/161/286/691 ms | 425/169/305/704 ms | 425/169/305/786 ms |
| 英文 TTS N=1/2/4/8 | 10/10、20/20、40/40、80/80 | 144/197/320/430 ms | 234/310/623/833 ms | 234/335/735/1082 ms | 234/335/735/1273 ms |
| 混合 4+4：ASR / TTS | 40/40、40/40 | 266/351 ms | 555/558 ms | 724/693 ms | 724/693 ms |
| 混合 8+8：ASR / TTS | 80/80、80/80 | 479/577 ms | 835/1072 ms | 982/1323 ms | 1117/1471 ms |

二轮所有非故意取消请求均为 HTTP 200，无 429 或 5xx；混合 8+8 两项 p95 均低于 2 秒、p99 均低于 5 秒。完整逐轮状态在 `.test-artifacts/capacity_probe_20261112_r2.jsonl`。故意断开各一条 ASR/TTS 请求后，8090 的 canceled 计数分别增加 1，active 均回到 0。GPU 已用显存在采样期间起/峰/末均为 4400 MiB；8001/8003/8090 监听进程存活，ASR、中文 TTS、英文 TTS、Picer 状态均为 ready。

## 可复现与回滚

`server/tools/piper_en_server.py` 的队列槽位和 backlog 均已落盘；`server/tools/install_piper_en.ps1` 默认安装两项容量 8，重复执行前后注册表、manager 与启停脚本 SHA-256 不变，`node --check D:\VL-models\manager\server.js` 通过。需要回滚时运行 `powershell.exe -NoProfile -ExecutionPolicy Bypass -File D:\en-words\server\tools\install_piper_en.ps1 -AsrMaxConcurrent 1 -EnglishMaxConcurrent 4`，然后重启 8090 管理进程；若要连 sidecar 槽位一起回滚，还需将 `BoundedSemaphore(8)` 恢复为 4 并重启英文服务。不要修改 Picer 或其他服务。

## 验收边界

本记录只证明公开合成短音频的本机 HTTP 容量和故障恢复，不能代表真人发音识别质量、扬声器首音、回声与背景噪声、多人浏览器课堂交互或长时持续负载。真实课堂门仍按 docs/60 的独立学生上下文、字幕、唯一 ACK、打断、ASR 降级与文字继续旅程验收。

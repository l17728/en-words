# 中文 TTS 常驻方案：隔离实施计划与协议夹具

状态：仅供设计与测试评审；不得据此改动生产 `8002`、`8090` 或 `tts.maxConcurrent=2`。目标和验收门见 `zh-tts-resident-workers-sdd-20261112.md`。

## 实施边界与顺序

1. 在 `.test-artifacts/zh-tts-resident/` 构建原生 `--serve` worker 与 Node 适配器，绑定 `127.0.0.1:18002`。启动时只读引用现有 Sherpa DLL、模型和词典的固定绝对路径；请求帧不得传文件路径。沿用现有 `sid=0..4`、`speed=0.6..1.8`、文本 ≤2000 字符且 ≤8000 UTF-8 字节。原 `tts-utf8-worker.exe` 与生产脚本保持原样。
2. 先单 worker 跑协议/故障夹具，再升至四个各 1 推理线程的独立进程。每进程只允许一项正在合成，Node 最多接受 4 执行+4 等待；单健康槽最多接受 2 项。取消等待项立即移除；取消执行项只弃结果，读完对应帧或确认子进程退出后才释放槽。
3. 在隔离端口跑公开冻结短词、混合中英词和较长释义 N=1/2/4 各 10 轮、N=8 至少 100 轮，记录每个状态、首字节、总耗时、p50/p95/p99、CPU、worker 总 RSS 与系统可用内存；N=8 的 p99 仍只称本次样本分位数，不外推到真人课堂。再跑 ASR/英文 TTS 混合负载和隔离浏览器首音/打断门。任何容量或资源门失败，停在现有生产版本并记录缺口。
4. 使用与 8090 相同启动命令、健康门和最多 3 次重启策略的**隔离 supervisor** 管理 `:18002`，验证“杀 Node→连续重启 3 次”：每代记录 Node/worker PID、槽 mutex、退出时间与 RSS，旧 worker PID 消失、每槽最多一个 worker、RSS 回归稳态、请求不串音。父进程句柄 watchdog 或命名 mutex 若未通过，先补进程治理；不得上线常驻版。分阶切换生产前，另对真正 8090 supervisor 做同样三次重启门；隔离测试不冒充该门。

## 原生二进制协议 v1 草案

stdin/stdout 只承载定长头加受限 payload；stderr 只允许事件码、request ID、耗时，不写文本或音频。每帧 20 字节头：`magic="EWTS"[4] | version=1[u8] | kind[u8] | flags=0[u16 LE] | request_id[u64 LE] | payload_len[u32 LE]`。ID 在一个 worker 生命周期内单调递增，READY 帧 ID=0。`kind=1` 请求 payload 为 `sid[u8] | speed_milli[u16 LE] | text_len[u16 LE] | UTF-8[text_len]`；speed 取 600..1800。`kind=2` READY payload 为 `slot[u8] | model_code[u8]`，仅预热成功后发送。`kind=3` 成功 payload 为完整 PCM16 单声道 WAV，大小 ≤8 MiB；Node 必须同时校验 ID、RIFF/WAVE、长度、采样率和 data 块。`kind=4` 失败 payload 为固定错误码 `u16 LE`，不包含正文。未知版本、kind、非零 flags、非法长度或错序 ID 均视为协议损坏，杀并重建该 worker；不可尝试流内重新同步。

## 冻结夹具与预期

| 编号 | 输入/故障 | 预期 |
| --- | --- | --- |
| P01 | ID=1、sid=0、speed=1000、`猫`（UTF-8 `E7 8C AB`） | 请求 payload 为 `00 E8 03 03 00 E7 8C AB`；返回同 ID 的有效 WAV |
| P02 | `teacher`、`苹果 apple`、2000 字符边界 | 短语可合成且不串请求；边界输入通过解析，但若超过 6 秒执行预算须明确超时，不得堵塞后续请求 |
| P03 | 空文本、NUL、无效 UTF-8、sid=5、speed=599/1801、8001 字节 | 拒绝且不调用 Sherpa；无正文日志 |
| P04 | 半个头/半个 payload 后 EOF、分块头/体、重复 READY、恶意 chunk、超 8 MiB 响应、错 ID、无效 WAV/采样率 | 有界超时或立即协议失败；当前请求失败，worker 退出后重建；解析器不依赖单次 data 回调 |
| P05 | 4 个执行+4 个等待，再发第 9 项；等待中取消一项；杀一个槽后恢复 | 第 9 项 429；取消项不进入合成；按 `ready_workers×2` 收紧容量并在重建后恢复；计数最终归零 |
| P06 | 冻结两段不同 hash 的 A/B WAV；两学生、两 lesson 并发，执行中取消 A 后强制 worker 回传 A，再处理 B | A 的旧 WAV 被读完丢弃，A 无播放/字幕/ACK；B 的 HTTP 音频 hash、浏览器 `playing→ended`、字幕及唯一 ACK 均只属于 B，不能仅检查 request ID |
| P07 | worker 睡眠 >6 秒、崩溃；隔离 supervisor 连续杀 Node 并重启 3 次，正式切换前再做 8090 真 supervisor 门 | 逐代 PID/互斥槽/RSS 可核，旧 PID 消失，无永占槽/孤儿/双驻留；健康槽继续服务或明确 503 |

首个固定帧的头为 `45 57 54 53 01 01 00 00 01 00 00 00 00 00 00 00 08 00 00 00`，再接 P01 的 8 字节 payload。实现时用字节级测试锁定此向量，并以分块写入同一帧验证流解析不依赖一次 `data` 回调收齐。

测试产物仅保存状态码、时延、哈希、采样率、进程/内存统计和脱敏 trace；冻结音频置于忽略目录，不提交正文或密钥。协议/性能通过不代替人工音色试听，也不代替生产双学生课堂验收。

执行参数冻结：协议半帧/EOF 在 1 秒内判失败，worker 单请求 6 秒超时后至多 1 秒确认退出并回收槽；本机 Sherpa 模型经真实输出核实为 **16000 Hz**，WAV 仅接受 PCM16、单声道、16000 Hz、数据段 ≤8 MiB。此前写的 22050 Hz 来自另一套 TTS 音频夹具，不能作为此模型的采样率依据。连续重启后旧 PID 须为 0，每槽活 worker≤1，30 秒内总 RSS 回到升级前稳态的 ±15%；无法回稳则该级失败并回滚。资源和浏览器时延门以主 SDD 为准。

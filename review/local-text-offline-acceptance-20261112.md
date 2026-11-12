# 本机文本模型离线回环与长跑诊断（2026-11-12）

## 范围

8090 的 `vl` 服务从本机已缓存 GGUF 启动，保留原有模型别名；ASR、中英文 TTS 未切换。评估调用生产 `judge.SystemPrompt`，只用仓库中已标注文本，不传真人数据。输出位于忽略目录 `.test-artifacts/`，不覆盖历史报告。

## 健康与产品接入

原 `D:\VL-models\start-qwen-vl.ps1` 使用 `-hf` 在线解析，网络失败导致 `vl=stopped`；本机模型和 mmproj 的 GGUF 缓存其实完整。已将启动参数改为固定本地 blob 路径，并加 `--alias Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M` 保持 8090 的模型身份校验与既有请求协议；旧脚本备份在 `D:\VL-models\service-backups\start-qwen-vl-before-local-cache-20261112.ps1`。PowerShell 语法检查通过，模型重启后 8090 报 `vl=ready`；没有修改 ASR、中文/英文 TTS 注册项。此项是本机外部服务配置，未包含在仓库 Git 提交中。

8090 `/api/status` 的 `vl` 为 `ready`。本轮源码构建的隔离 Go 后端在 `-testmode -providers local -port 18096` 下启动：`provider_discovered` 显示 `text/asr/tts/tts_en=true`，结构化文本探测 1472 ms 成功，`server_started` 总耗时 4452 ms，HTTP 首启状态 200；日志中没有 `local_text_degraded`。隔离进程已停止。证据为 `.test-artifacts/local-provider-probe-20261112.stdout.log`。

## 标注评估

| 运行 | 正确/尝试 | 超时 | 成功请求 p95 | 最大成功时延 |
| --- | ---: | ---: | ---: | ---: |
| 基础 20 条 | 20/20 | 0 | 1475 ms | 2696 ms |
| 扩展首轮（计划 100，故障后停止） | 75/83 | 8 | 1080 ms | 1427 ms |
| 首轮超时词独立复测（含基础 20） | 30/30 | 0 | 520 ms | 626 ms |
| 扩展完整复测 | 100/100 | 0 | 331 ms | 531 ms |

首轮在第 76–83 条连续出现 8 次 `TimeoutError`，每次约 8 秒，随后停止；未以完整复测覆盖这 8 次失败。llama 日志中 task 847 启动于相对时刻 `11.11.442`，到 `12.33.305` 才开始集中取消任务；该 task 的实际推理计时仅约 320 ms，说明这一段约 82 秒的等待不在模型推理计时内。服务随后恢复，失败词独立 30/30、完整复测 100/100，但不能仅凭一次成功复测宣称长跑稳定或 8 秒产品预算零超时。需在多用户负载与主机资源采样下定位进程调度/分页或代理等待，再确定生产文本能力门。原始证据：`.test-artifacts/local-text-eval-{base,100,failed-subset-result,100-r2}-20261112.{json,log}`。

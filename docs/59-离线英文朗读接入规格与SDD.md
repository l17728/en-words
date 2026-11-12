# 离线英文朗读接入规格与 SDD

## 背景与目标

2026-11-12 真实媒体旅程证明：本机 ASR 和中文 TTS 可用，但无本地英文浏览器 voice，三段英文朗读均以 `no_local_voice` 失败；现有中文 TTS 合成 `teacher` 返回 500。英文朗读是教学必需项，故新增独立、可停用的本机英文 TTS。不得使用云推理或将失败标为已朗读。

## 能力契约

8090 注册 `tts-en`（端口 8003，Piper en_US-lessac-medium，`/api/tts-en/speech` 代理 `/v1/audio/speech`）。仅绑定 `127.0.0.1`。`GET /v1/models` 在模型已加载且预热后才返回模型 ID；`POST /v1/audio/speech` 接受固定 model、1–300 字英文短文本，返回 PCM16 mono WAV。请求须带本机模型平台的 Bearer key；正文只接受 JSON，最大 4096 字节，拒绝控制字符、无拉丁字母或含中日韩文字的错误语言文本、错误 model。词库中带括号、斜杠、缩写和拉丁重音的词条必须能合成；这些标注的朗读自然度另做人耳评估。8090 最多允许 4 个未完成请求；sidecar 单实例合成并限制等待队列，等待超过 2 秒返回 429。取消等待者不参与合成，客户端收到迟到响应也不得播放旧音频。

`GET /api/v1/providers` 的 `tts_en` 只在 8090 报告服务 ready 时为真。课堂优先播放本地英文 WAV，段开始才显示字幕，播放完成才发送一次 `tts_finished`；中断、换题或离开课堂会停止播放并清旧字幕，旧回调不得推进课堂。服务停机时尝试已验证的浏览器本地 voice。仍不可用则暂停课堂并提示重试/文字继续，记录未听到的段，绝不伪造完成 ACK。服务器 TTS 超时预算覆盖合成、下载、播放及设置中的段间隔；旧段超时或迟到不可跨轮次 ACK。浏览器自动播放被拒时提示点击重试。

首段播放前若能力查询尚未完成，最多等待 3 秒取得 8090 实际状态，再决定本地音频或浏览器本地 voice。等待期间换题/离开则丢弃旧结果；能力查询超时走明确的朗读失败暂停，不应因为初始化竞态误判服务离线。

失败协议：客户端发送带当前 `lesson/epoch/turn/segment` 的 `tts_failed`，服务器校验活动连接与上下文后立刻暂停当前段、停止计时并广播 `lesson_paused`；重复/迟到失败忽略。`resume` 对原段重发 `play_tts`，不会推进词位。暂停页的“改用文字，跳过本段”发送独立 `tts_skip`，服务器记录 `tts_skipped` 原因 `explicit_no_audio` 后推进；这项只在用户主动确认时可用，不能由失败回调自动触发。服务端若先达到超时，也暂停并要求相同的重试或显式跳过。预算为当前段估计播放时长 + 配置段前间隔 + 本地合成/下载预留 20 秒（中英均适用）；确认新段或暂停后旧计时失效。

## 实现与配置

产品仓库提供独立 Python 3.12 HTTP sidecar（`piper-tts==1.8.0`），Piper ONNX 与 JSON 配置均校验冻结 SHA256；不提交权重、密钥或运行日志。8090 管理平台只增加该服务的注册和启停脚本，不改变现有 ASR、中文 TTS、Picer 服务。启停脚本记录 PID，模型路径/运行时路径可配置。sidecar 从本机受保护配置读取 key，日志仅写 trace ID、模型、耗时、状态，不写正文或密钥。8090 代理只转发匹配安全字符集且限长的 `X-Trace-ID`；浏览器日志再记录 lesson/epoch/turn/segment 以关联 ACK。产品 Go 客户端仅经固定 8090 代理访问英文 TTS，`/speech/synthesize` 按 `lang=en|zh` 选择服务。预热在服务开放端口前完成，避免首个学生承担约 14 秒冷启动。

本机安装：`server/tools/install_piper_en.ps1` 将启停脚本与注册项复制到 `D:\VL-models`；重启 8090 管理进程后使用 `start-english-tts.ps1` 启动。默认运行时、Piper 包及模型固定在 `C:\Users\a\enwords-runtime`，可分别用 `ENWORDS_PIPER_PYTHON`、`ENWORDS_PIPER_PACKAGES`、`ENWORDS_PIPER_EN_MODEL` 覆盖。模型权重的 SHA256 为 `5efe09e69902187827af646e1a6e9d269dee769f9877d17b16b1b46eeaaf019f`，JSON 配置为 `efe19c417bed055f2d69908248c6ba650fa135bc868b0e6abb3da181dab690a0`。下载源为 rhasspy/piper-voices 的 `en/en_US/lessac/medium`。安装脚本不复制密钥；启停脚本在本机读取管理平台已有配置。服务就绪探针：8090 `/api/status` 中 `tts-en=ready`，产品 `/api/v1/providers` 中 `tts_en=true`。

## 验收门

- Go：注册/ready/stopped、英文路由、限流与格式、租户权限；`tts_failed` 暂停且不推进、重复/迟到失败忽略、`resume` 原段重播、仅显式 `tts_skip` 推进并记录 `explicit_no_audio`、服务端先超时同样暂停、旧 ACK 拒绝；中文路径不回退。
- Python：模型加载/预热、输入边界、WAV 元数据、并发与取消；7 库 5568 词中含特殊标注的 87 条批量合成至少无错误且 WAV 可解码；日志不含正文。
- Playwright 无头：teacher／老师真实双语课堂完成，英文三段与中文一段都实际 `playing→ended`，字幕同步；字幕仅在 `playing` 后出现并在 `ended` 后清除；每段 `lesson/turn/segment`、请求、首音、结束、唯一 ACK 有关联证据。故障旅程断言 `tts_failed` 暂停、不推进、重试原段、显式文字跳过才推进，且旧音频/字幕/ACK 全清除。仅有 `ended`、没有 `playing/onstart` 时必须失败暂停，不得 ACK。
- 性能：热机单词合成目标 p95 <500 ms，首音、打断和多人长尾另列真实试点，不用 3 条合成样本外推。现有 Piper 探针 teacher/cat/apple 回识 3/3，热机 71–81 ms；2026-11-12 用户试听三条均确认清楚、准确、自然。

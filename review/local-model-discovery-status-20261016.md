# 本机模型发现与 ASR 剩余验收边界

本次仅一次 `GET http://127.0.0.1:8090/api/models`，timeout 5 秒、无重试，HTTP 200。未进行推理、启停服务、模型下载或加载。

白名单发现结果：

| 服务 ID | 名称 | 声明能力 | 模型标识 |
|---|---|---|---|
| vl | Qwen3-VL 8B | text, vision, video | Qwen/Qwen3-VL-8B-Instruct-GGUF:Q4_K_M |
| asr | Qwen3-ASR 1.7B | asr, audio | qwen3-asr-1.7b |
| tts | Sherpa VITS Chinese | tts, audio | sherpa-onnx-vits-zh-ll |
| picer-model | Picer 模型服务 | training, index-api | picer-face-training |

响应提取中没有 ready/running/available/status 等运行状态字段。因此可以确认本机发现接口可达、ASR 服务仍被列出，不能据此确认当前 ASR 推理健康；本轮没有再请求 health/status 或转写来补这个未知项。

现有最近证据：

- `review/asr-comparison-ab-20261014/` 与 `review/asr-comparison-cd-20261014/`：固定全 28 样本 A/B/C/D，共 112 成功响应、无传输失败；每组英文 15/16，短词 11/12，WER 1/31；中文 9/12，CER 5/46，关键句 3/4。四组规范化结果相同，前处理/语言名/温度变化未解决质量缺口。详见 docs24 §ASR 实测结果。
- 产品路由独立源：`review/local-asr-product-quality-20261014-recovered.json`、`review/local-asr-chinese-quality-20261014.json` 及对应 metrics。恢复前失败证据仍保留，不与恢复后分母混合。
- 浏览器真实链：`review/real-media-b-final78-20261014.json` 记录真实采集/转换/ASR 句子旅程；总体测试失败不能写成整链通过，音频传输与判分结果需分账。它不是独立单词或真人质量通过证明。
- 独立 CPU 候选：`review/funasr-cpu-frozen28-result-20261014.md`、`review/funasr-load-diagnostic-20261014.md`；加载未 ready，28 未尝试，不是另一个已可用 ASR 服务。

本机后续仍需要当前运行健康与真实请求证据、冻结质量门（尤其中文错误）和真人场景验证。已有合成源用户试听签收只覆盖记录中的样本，不能外推整个真人课堂。远端协议缺失不使本机既有质量失败自动变为通过，也不要求现在追加推理。

# 远端公开合成音频探针

日期2026-10-15。只使用独立合成声音，未上传真人音频；用户已授权现有HTTP的公开合成样本临时验证。未改服务、模型或产品路由。

## 格式依据和冻结

已实际读取[OpenAI官方音频输入示例](https://developers.openai.com/api/docs/guides/audio-chat-completions)，采用Chat Completions user内容数组中的`input_audio`，数据为完整WAV的base64、format为wav。也查阅[MiniCPM官方部署说明](https://github.com/OpenBMB/MiniCPM-o-Demo/blob/main/docs/en/deployment.md)及[Qwen3-Omni官方仓库](https://github.com/QwenLM/Qwen3-Omni)；这些上游能力说明不能证明当前网关已经提供同样的流式协议。

首批manifest `remote-synthetic-audio-manifest-20261015.json`，SHA a3606899159bf3dc9dcfbb217c1ad87aaae54e2dc0d762397847d0ee907017c8。4例为人审中文短词/长句、独立Zira英文单词、数字静音，各alias一次。

扩集manifest `remote-synthetic-audio-expanded-manifest-20261015.json`，SHA ec95ec8e23e6e7eb27a7cc0281db24540151a32475f2eb17aeb66cd0da41c82f。12条中文音频已有用户“清楚、正确、完整”签收，附1英文及1静音，各alias一次。两批有重叠，不合并称22个独立样本；原gold/hash未依据识别结果修改。

提示只要求转写原语言、静音返回空，不提供gold；无热词或答案提示。每请求1024tokens、8秒远端绝对截止，串行，不流式。识别输出只做NFC/小写/空白及标点忽略的探索性精确比较，不删除think或解释文字取绿。这种标点忽略评分不是产品双轨Agree规则。

## 三层结果

| 层次 | qwen-omni | minicpm-o |
|---|---|---|
| HTTP/音频输入格式 | 首8及扩28请求全部200/stop；扩集14/14 | 扩集14/14为200/stop |
| 音频被使用证据 | 首批长句准确转写、英文Apple，改变声音得到不同内容 | 英文内容出现Apple但带think；中文也有音频关联片段，不能据此推断完整可靠转写 |
| 扩集严格完整转写合同 | 中文9/12，英文1/1，静音0/1，共10/14 | 完整输出精确0/14；9条含think、2条空、3条其他文字。格式失败不能简单等同听力全部错误 |
| 单次耗时 | 扩集最大5635ms | 扩集最大6661ms |

首批独立留痕：qwen2/4，minicpm0/4严格通过。短词“苹果”两路均出现误识别；qwen静音输出`[Music]`，minicpm静音非空。静音控制未通过，不能自动把有文字当学生有效发言。n=1/例，不构成稳定延迟分位或准确率承诺。

## 边界与下一步

普通非流式`/v1/chat/completions`接受WAV已实证；原生双工、边录边回、取消ACK、增量终稿、真设备AEC和真人质量门仍未验证。当前结果不足开放自动教学，尤其短词、静音和MiniCPM输出格式。

可提交结果为 `remote-synthetic-audio-initial-20261015.json` 与 `remote-synthetic-audio-expanded-20261015.json`；脱敏公共响应逐条保存在忽略目录`.test-artifacts/remote-audio-{initial,expanded}/responses.jsonl`。网络鉴权密钥仅内存和SSH stdin，严格known_hosts、不跟redirect、不使用环境代理。报告不包含密钥或完整远端文档。

本次是synthetic exploratory轨，不并入历史Piper ASR比较、真人300样本、完整课堂或双工准入。后续扩短词/混合命令应先冻结音源和标签并保持上述三层分账，不能剥离失败内容改写原分母。

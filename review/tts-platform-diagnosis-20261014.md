# 中文TTS平台故障定位

更新：下文保留首次诊断过程。后续同模型UTF-8隔离对照及实际候选HTTP服务已成功，编码边界得到实证；见`tts-utf8-isolated-20261014.json`、`tts-utf8-candidate-http-20261014.json`及`platform-tts-utf8-application.md`。共享平台尚未应用，仍待用户确认；混合英文内容缺失与人工试听不因编码修复而通过。

## 证据与范围

2026-10-14，按主代理授权在其他媒体任务停止后，对127.0.0.1:8090进行一次有效“猫”请求，保持生产model/voice/response_format及20秒上限；没有更换模型、修改平台或重启服务。

首次诊断因PowerShell向Python管道传源码时中文变为问号，实际输入为`?`，HTTP200、2092字节、10.110秒。原始记录保留在`tts-cat-diagnostic-20261014.json`，该样本无效，不能声称“猫”合成成功。随后用ASCII源码Unicode转义明确构造“猫”，结果为HTTP500、7.890秒，见`tts-cat-valid-diagnostic-20261014.json`。

有效错误摘要（本地路径已屏蔽）：

```text
Ignore OOV 'è'
Failed to convert è to token IDs
Error in generating audio. Please read previous error messages.
```

## 已定位与尚待证实

实际8090进程使用`D:/VL-models/manager/server.js`，按配置动态代理到8002的`manager/tts-server.js`。后者每请求通过PowerShell EncodedCommand启动Sherpa离线TTS，把正文作为命令行参数；失败stderr随HTTP500返回，没有写入TTS日志（当前日志为空）。

平台词典`models/sherpa-onnx-vits-zh-ll/lexicon.txt`确有“猫”的读音条目。因此已定位为平台Sherpa文本到token转换失败，而非本产品20秒超时、浏览器音频播放失败或词典缺少“猫”。错误字符与输入不符，强烈指向Windows原生命令行/字符编码边界，但具体UTF8/ANSI转换位置仍需平台专门验证，不能仅据OOV断言唯一底层原因。

## 最小后续方案

1. 在独立平台任务中捕获非敏感固定文本经过HTTP JSON、PowerShell和CLI入口的码点/字节摘要，确认损坏位置；不能记录真实用户正文或密钥。
2. 优先使用Sherpa支持的显式UTF8文本输入方式或原生Unicode绑定，避免依赖系统代码页的命令行文本传输；具体API能力需对当前二进制/版本先核验。不应在本产品把中文改拼音、延长超时或伪造成功ACK来掩盖故障。
3. 增加“猫”“苹果”、中文短句、标点和中英文混合的固定平台回归；每条断言HTTP成功、WAV结构/非空及人工试听状态。日志保留trace、exit code、错误类别和耗时，错误正文脱敏限长。
4. 平台修复后重新执行本产品真实媒体旅程及独立TTS质量集。当前Go/mock/浏览器故障处理通过不等于真实TTS已可用；人工可懂度仍需单独验收。

# Picer 与叮当words 的本机语音对照

2026-10-14只读代码比对：Picer `FullDuplexVoiceSession.cs:281` 使用 `sherpa-onnx-vits-zh-ll`、voice 0、speed 1.05；其通用文本路径使用speed 1.0。叮当words当前也是同一平台模型，voice `default` 在平台映射为0，未传speed时平台默认1.0。因此不能将此前用于ASR独立音源的Piper当作叮当words的实际TTS引擎。

主代理只读复核还发现Picer有NormalizeMarkdown、空白清理及完整句子合成路径；断句和输入规范化可能影响听感，不能仅按模型名推断体验完全一致。用户指出发音不清的Piper“猫”只是独立ASR旅程夹具，已判无效，不是课堂TTS声源。

以上“同平台引擎”仅限中文正常链路。`web/src/audio/local.ts` 的 `playLocalChinese` 与 `Classroom.tsx` 表明：课堂英文仍用浏览器匹配的英语voice，rate 0.8；中文平台失败时的浏览器降级rate为0.95。不能把本次两段中文对照外推为整个课堂均使用zh-ll，也不能把降级链路算成本次平台请求。

未改任何服务或产品配置，只向8090串行发送相同中文“现在开始今天的单词学习。”，voice0、model不变，仅speed参数不同。两次均20秒预算内HTTP200，PCM16单声道16kHz。请求耗时与音频时长是不同指标，单次时延差异不能推断模型优劣。

| 参数 | 试听 | 音频时长 | 请求耗时 |
|---|---|---:|---:|
| speed 1.0 | [同句录音](picer-tts-speed-20261014/speed-1.0.wav) | 1.9094秒 | 11.922秒 |
| speed 1.05 | [同句录音](picer-tts-speed-20261014/speed-1.05.wav) | 2.032秒 | 6.172秒 |

完整SHA及参数见 `picer-tts-speed-20261014/report.json`。本组哪段更自然待用户试听，不以speed数字大小或非静音代替主观判断，也不由此修改默认语速。原12条TTS人工“清楚、正确、完整”的签收是另一组固定音频的证据，不自动延伸到本对照。

本次1.05的音频反而略长，不能声称1.05更快；两次请求延迟也不是性能基准。

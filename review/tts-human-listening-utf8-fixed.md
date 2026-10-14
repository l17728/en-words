# 修复后中文TTS人工试听清单

原冻结12条标签未改，真实合成12/12、PCM完整且非静音12/12。自动检查只证明传输和信号存在，不证明发音正确；用户已逐套确认“12条全部清楚、正确、完整”。独立签收记录及各录音 SHA 见 `tts-human-acceptance-20261014.json`；原始自动报告仍保留采集时的 pending，不回写历史。

| 编号 | 预期内容与录音                                                                   | 人工结论 |
| -- | ------------------------------------------------------------------------- | ---- |
| 01 | [苹果](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-01.wav)            | 用户确认：清楚、正确、完整  |
| 02 | [老师](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-02.wav)            | 用户确认：清楚、正确、完整  |
| 03 | [美丽](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-03.wav)            | 用户确认：清楚、正确、完整  |
| 04 | [放弃](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-04.wav)            | 用户确认：清楚、正确、完整  |
| 05 | [苹果是一种水果。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-05.wav)      | 用户确认：清楚、正确、完整  |
| 06 | [老师是教学生的人。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-06.wav)     | 用户确认：清楚、正确、完整  |
| 07 | [美丽的意思是漂亮。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-07.wav)     | 用户确认：清楚、正确、完整  |
| 08 | [放弃的意思是不再继续。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-08.wav)   | 用户确认：清楚、正确、完整  |
| 09 | [请跟我朗读这个单词。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-09.wav)    | 用户确认：清楚、正确、完整  |
| 10 | [现在开始今天的单词学习。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-10.wav)  | 用户确认：清楚、正确、完整  |
| 11 | [请说出这个词的中文意思。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-11.wav)  | 用户确认：清楚、正确、完整  |
| 12 | [今天的课程结束了，明天见。](local-tts-quality-20261014-utf8-fixed.json.TTS-ZH-12.wav) | 用户确认：清楚、正确、完整  |

报告及各音频SHA见 `local-tts-quality-20261014-utf8-fixed.json`，自动信号检查见同名前缀`-metrics.json`。这些音频由待测平台生成，与ASR独立Piper输入录音不同，不可混为ASR正确率证据。旧10/12报告及其失败录音完整保留。

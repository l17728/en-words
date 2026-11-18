# 国内开放权重模型候选调查（2026-10-14）

## 范围与结论

按本轮官方发布页、模型卡、权重提交历史和许可证核查。以下是值得比较的能力上限候选，**不是已经在叮当words上验证的质量排名**。厂商评测任务、工具框架和推理预算不同，不能据一张榜单宣布唯一最强。本轮没有下载、替换8090模型或调用云推理。

对当前RTX4060Ti 16GB显存、16GB系统RAM，优先比较Qwen3.5-9B量化与Qwen3-14B量化；Qwen3.8-27B仅列资源边界实验。旗舰开放权重不等于可在这台电脑运行，MoE激活参数不等于需存储的全部权重。

## 能力上限候选

日期区分官方公告与权重提交，提交日不冒充首次发布日。

| 候选 | 日期证据 | 与本项目相关的比较理由 | 许可与资源边界 |
|---|---|---|---|
| GLM-5.3 | [官方权重记录](https://huggingface.co/zai-org/GLM-5.3-BF16/commits/main)，2026-08-27/28 | [模型卡](https://huggingface.co/zai-org/GLM-5.3-BF16)侧重长程Agent、编码与工具任务；值得作为系统Agent能力上限比较 | 自定义[GLM-5.3许可](https://huggingface.co/zai-org/GLM-5.3-BF16/raw/main/LICENSE)，有特定MaaS商业附加条件，不能沿用旧版本MIT结论；非本机候选 |
| Kimi K3 | [官方权重记录](https://huggingface.co/moonshotai/Kimi-K3/commits/main)，2026-07-27 | [官方介绍](https://www.kimi.ai/blog/kimi-k3)：长程工具编排、知识任务、原生视觉；2.8T总参数，官方建议64张及以上加速卡部署 | [自定义许可](https://huggingface.co/moonshotai/Kimi-K3/raw/main/LICENSE)，含MaaS和商业展示条件；不能按宽松开源许可概括 |
| DeepSeek V4.1 Flash | [官方权重记录](https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash/commits/main)，2026-09-10 | [模型卡](https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash)：工具/推理候选；552B骨干另有196B条件记忆，不能只看8B/16B激活量 | MIT；附加模块也占资源，远超16GB。V4 Pro-0813也可作服务器档候选，Flash并非所有能力都必胜Pro |
| Qwen3.8-2.4T-A95B | [官方权重记录](https://huggingface.co/Qwen/Qwen3.8-2.4T-A95B/commits/main)，2026-08-09上传、08-12许可更新 | [模型卡](https://huggingface.co/Qwen/Qwen3.8-2.4T-A95B)：Qwen旗舰，2.4T总参数、95B激活；作为中文任务与工具能力比较对象 | [qwen3.8-max自定义许可](https://huggingface.co/Qwen/Qwen3.8-2.4T-A95B/raw/main/LICENSE)，不能套用小型号Apache；非本机候选 |
| MiniMax M3 | [官方公告](https://www.minimax.io/blog/minimax-m3)，2026-06-01 | [模型卡](https://huggingface.co/MiniMaxAI/MiniMax-M3)：428B总参数、23B激活，长上下文Agent和多模态 | [MINIMAX COMMUNITY LICENSE](https://huggingface.co/MiniMaxAI/MiniMax-M3/raw/main/LICENSE)，商业通知/展示及规模条件；不是旧M2的MIT口径 |
| Tencent Hy3 | [官方公告](https://www.tencent.com/en-us/articles/2202386.html)，2026-07-06 | [模型卡](https://huggingface.co/tencent/Hy3)：295B主体、21B激活，含额外MTP；作为宽松许可服务器档补充比较 | Apache-2.0；官方部署建议多卡，不适合本机 |

“开放权重”描述可获得权重；Apache/MIT与自定义商业限制分列。上述名单均有官方权重，不把只有API的闭源模型混进名单；开放模型通过云API使用仍属于云部署，不是本地离线。具体上线需审读对应版本许可全文，不能从同一家旧型号推断。

## 本机实际试跑顺序

| 顺序 | 候选与官方依据 | 资源估算与限制 |
|---|---|---|
| 1 | [Qwen3.5-9B](https://huggingface.co/Qwen/Qwen3.5-9B)，Apache-2.0；[2026-02/03官方提交](https://huggingface.co/Qwen/Qwen3.5-9B/commits/main)。优先测中文多意图、日期、否定和结构化槽位 | 4bit裸权重约4.5GB，实际量化文件及运行开销更高；先4K上下文、单并发，为ASR/TTS留余量。BF16约18GB裸权重不适用 |
| 2 | [Qwen3-14B](https://huggingface.co/Qwen/Qwen3-14B)，Apache-2.0，2025代际；模型卡列工具调用与关闭thinking模式 | 14.8B总参数，4bit裸权重约7.4GB；短上下文量化可作为本机候选，不能预先认定比新9B强；运行时/模板兼容需验证 |
| 3 | [Qwen3.8-27B](https://huggingface.co/Qwen/Qwen3.8-27B)，Apache-2.0；[2026-08-13/14权重与许可记录](https://huggingface.co/Qwen/Qwen3.8-27B/commits/main) | 仅27B语言部分4bit已约13.5GB，另有视觉与量化元数据、缓存、计算缓冲。16GB显存全GPU紧张，RAM也只有16GB；不推荐直接替代生产或与语音常驻并发 |

上述内存是参数数×位宽的**工程下界估算，不是本机实测**；量化格式、混合精度、KV/循环状态、CUDA及Windows占用均额外计算。GPU显存与系统RAM不是统一32GB内存。先记录实际峰值与OOM、吞吐/首字延迟，再决定是否可用；不建议以SSD换页宣称旗舰“可跑”。

[Qwen3.8-Flash-Next](https://huggingface.co/Qwen/Qwen3.8-Flash-Next)虽仅6B激活，但官方给125B骨干、51B ngram及4B MTP，总约180B，不能当6B模型塞进16GB；35B-A3B类同样不能按A3B算权重大小。

## 叮当words应如何选型

1. 本项目关键是7字段JSON、中文多意图、数量/日期/否定与需要确认边界；编码或通用工具榜单只用于入围，不能代替本项目评测。模型只建议意图，Registry授权、冻结事实、幂等与原文约束仍由产品执行。
2. 下载前核对8090后端支持该架构、量化和官方chat template。冻结仓库revision、量化文件SHA、推理参数；思考输出与JSON正文分开，受支持的非思考模式可单独比较延迟。模板不匹配不能直接判模型能力不足。
3. 原64条和已运行28条留出现在都属于已见回归；候选比较须另外预先冻结未见样本，含省略日期、ISO、跨句备选、跨kind越界和取消。不能看模型答案后改标签。
4. 同一次原始响应分别评分模型与生产Parser；网络错误、原始协议合法率、槽位语义、禁止动作、产品完成率、p50/p95延迟、显存/RAM峰值分别报告。JSON约束解码只能约束格式，不能证明日期或动作正确；产品规则纠正不能算模型答对。
5. 先本机小型号A/B；若真实语义仍低于docs15门槛，再给服务器部署/云托管的性能、成本和数据边界方案，由用户选择。当前未因调研而授权执行云调用或替换平台模型。

结论：旗舰能力对比优先GLM-5.3、Kimi K3、DeepSeek V4.1/Pro、Qwen3.8；本机落地优先Qwen3.5-9B与Qwen3-14B量化实测。它们是否胜过当前平台模型仍待公平测量，不能仅凭新版本号或参数量宣布达标。

## 中文识别与合成候选补充

语音不能按文本榜单选型。以下是2026-10-14核对官方仓库后的独立候选，未下载或替换平台服务，也不是本项目实测排名。

| 用途 | 官方候选 | 本项目比较重点 |
|---|---|---|
| ASR | [Fun-ASR-Nano-2512](https://huggingface.co/FunAudioLLM/Fun-ASR-Nano-2512)，800M、Apache-2.0 | 中文/英文/日文及中文方言口音；可与现有Qwen3-ASR-1.7B比较短词、否定、数量和口令。不要把31语言MLT变体能力混入基础Nano |
| ASR | [FireRedASR2S](https://github.com/FireRedTeam/FireRedASR2S)中的FireRedASR2 | 官方面向中文、英文、混说、方言及语音/歌声；列为准确率候选。2S是ASR/VAD/LID/标点系统，须明确实际选用checkpoint和运行组件，不能当单一模型下载量估算 |
| TTS | [Qwen3-TTS-12Hz-1.7B-CustomVoice](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice)，Apache-2.0 | 固定老师音色、中英朗读及短词完整性；官方另有0.6B型号可比较资源。CustomVoice、Base克隆、VoiceDesign用途不同，本项目不必为朗读先引入克隆流程 |
| TTS | [Fun-CosyVoice3-0.5B-2512](https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512)，Apache-2.0 | 中英跨语言、发音控制和内容完整性；0.5B不等于整套运行时内存，声码器/分词器也需计入。这里只是候选，不自动撤销旧D-17产品供应商决策 |
| TTS | [MOSS-TTS-v1.5](https://huggingface.co/OpenMOSS-Team/MOSS-TTS-v1.5)，Apache-2.0 | 官方提供中英等31语言、拼音/IPA发音及停顿控制；作为发音和自然度比较候选。先验证完整模型加音频分词器的资源和延迟，不承诺本机能与文本/ASR同时常驻 |

本机试跑优先级是工程建议：ASR先Fun-ASR-Nano与当前模型对照，再测FireRedASR2；TTS先Qwen3-TTS较小型号/1.7B与CosyVoice3，MOSS作为补充。依据是任务贴合度和部署成本，**不是声称前者质量必胜后者**。先修现有中文TTS编码错误，再用相同原始样本评估；否则把编码损坏误归因于模型能力会做出错误替换决策。

资源补充：MOSS-TTS-v1.5的8B BF16裸权重下界约16GB，尚未计入音频分词器及计算缓冲；本机16GB显存与16GB RAM不能据此承诺全GPU运行。量化、卸载的兼容性、完整内存峰值及延迟需要单独实测。

ASR分别报告中文CER、英文WER、关键否定/数字/口令误识别，保留真实MediaRecorder路径与独立合成源。TTS报告缺字/漏词、读音、可懂度、首音延迟及峰值内存，人工试听与自动WAV校验分列。合成服务不得兼任其自身ASR唯一出题音源；候选评估需要新增冻结样本，不能针对已知“安静/跳过/五十”失误只加答案热词便宣称全面提升。

附节已由独立架构/AI代理重新核验上述五个官方来源并复核通过；其提出的MOSS BF16内存下界已补入。该评审证明调研表述和边界有依据，不等于任何候选已在本机通过模型质量门。

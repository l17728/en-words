# Fun-ASR-Nano-2512资源与兼容性只读调查

日期：2026-10-14。仅查询官方文件元数据、公开代码和本机包版本；未下载权重/安装依赖、未加载模型、未停止或替换共享ASR。建议是隔离试验计划，不是本机性能或准确率结论。

当前授权与进度更新：用户继续任务后，29规格已获主审，独立CPU下载、隔离venv构建/安装及受限测试已在授权范围；不修改共享ASR、系统Torch或Piper。本文下方“获准前”等为初始调查历史，不代表仍等待用户授权。现已完成资源守卫9条、安装入口3条离线测试，正在解析完整官方依赖清单；sdist缺wheel是隔离构建工作，不是审批阻塞。安装与模型质量尚未验收，当前实际状态以29及后续安装报告为准。

## 文件与下载边界

[官方模型仓库](https://huggingface.co/FunAudioLLM/Fun-ASR-Nano-2512/tree/272c57b82523ada6fd87095e955f8e29100979ab)本轮API元数据revision为 `272c57b82523ada6fd87095e955f8e29100979ab`。完整21文件总量 **1,989,178,168字节（1.989 GB，约1.853 GiB）**：model.pt为1,971,149,431字节；其余18,028,737字节包含Qwen3-0.6B配置/分词器、multilingual.tiktoken、配置、说明、图片和示例音频。此总量是模型仓库全部文件，不漏分词器，也不含Python运行环境。

[配置](https://huggingface.co/FunAudioLLM/Fun-ASR-Nano-2512/blob/272c57b82523ada6fd87095e955f8e29100979ab/config.yaml)引用仓库内Qwen3-0.6B，输入前端16kHz，LLM为bf16。[官方实现](https://github.com/FunAudioLLM/Fun-ASR/blob/main/model.py)用from_config构建LLM后加载checkpoint；不能看到0.6B名称便额外下载另一个完整Qwen权重。实施时须固定代码revision，并检查加载缺失键和离线访问，防止隐藏下载或随机初始化被误当模型能力。

运行依赖另计：[官方requirements](https://github.com/FunAudioLLM/Fun-ASR/blob/main/requirements.txt)要求torch/torchaudio≥2.9.0、transformers≥4.51.3、funasr≥1.3.26，另有中文转换、Whisper规范化、日本语前处理等。元数据查询显示Windows CPython3.12的PyPI torch2.9.0轮子109,280,887字节；这不能冒充CUDA包大小。官方[cu128索引](https://download.pytorch.org/whl/cu128/torch/)中对应torch2.9.0+cu128轮子HEAD为 **2,860,231,824字节**。仅模型全集加该CUDA轮子已 **4,849,409,992字节（4.849 GB）**，仍需torchaudio、其余传递依赖和源代码；因此不得将“约2GB下载”宣传为完整GPU环境总量。

完整环境的精确总下载尚未求解：依赖存在范围约束，CUDA/CPU索引不同；需在获准安装前固定所有wheel版本并以解析清单逐文件求和。可暂按5至7GB下载和更大的安装/缓存空间规划，但这是工程预算，不能作为已核实总数。CPU核心四包PyPI轮子合计121,280,971字节，也不含全部传递依赖。

## 本机现状与20秒预算

只读importlib.metadata：系统Python3.12.10，torch2.6.0+cu124，缺torchaudio/transformers/funasr；独立piper-venv同Python版本但没有这些包。当前torch不满足所查官方最低版本；不可直接升级全局或Piper环境，以免破坏已有语音。若后续获准，使用新的独立venv和固定依赖，先验证Windows轮子、CUDA/驱动及模型加载，不先改8001/8090。

CPU为Intel i3-4130，显卡/系统RAM分别16GB。800M参数比现有1.7B小不等于CPU必在20秒完成：bf16支持、编码器/自回归生成、线程和并发负载均影响延迟。CPU只适合初步加载兼容探针，不能承诺实时；GPU优先作为资源允许时的候选，仍需实测峰值，不能在现有模型占用上直接叠加常驻。需要GPU释放时必须另行核定具体服务窗口；本任务没有授权停共享ASR。

试验服务应只绑定独立loopback端口，启动和首次预热耗时单列；每个已预热请求保持20秒硬预算、batch=1、不设置答案热词。先固定完整28条已有独立音频做同样CER/WER与关键句评分，再用新增未见音频验证；人工已确认清楚的04/08/10错误不能归因音源。持续失败停止并保留分母。通过独立接口也不能替代真实浏览器MediaRecorder→WAV路径验收。官方Nano基础为中英日，不能借MLT31语言型号能力为该checkpoint背书。

## 当前结论

权重及附属文件约1.989GB，GPU环境仅已知主要文件下限约4.849GB；本机依赖不齐且torch版本不符，20秒能力未测。具备进一步隔离试验价值，但当前没有下载、安装或服务替换成果可报告。

## Torch复用方案复核

再次直接读取上述模型仓库requirements，第1、2行明确为 `torch>=2.9.0`、`torchaudio>=2.9.0`，不是pip解析器自行偏好最新版本造成的大下载。FunASR工具包1.3.26的PyPI metadata没有相同Torch下限，transformers4.51.3的可选Torch依赖也较宽；两者不能覆盖具体模型项目的明确要求。

因此复用全局2.6+cu124、配torchaudio2.6并以system-site-packages隔离，只能是偏离官方支持基线的兼容实验，不能承诺能加载或同等正确。该模式也会继承全局NumPy等包，不是完全独立锁定环境。主代理确认无需为此进行dry-run；本轮没有创建venv、解析安装计划或安装软件。建议后续获准时采用全新隔离环境固定官方支持版本，先解析全部依赖体积，再决定下载。峰值RAM/GPU和20秒达标尚无实测，也无可信最大值；部署前须在独占资源窗口实测，不能把约0.8B参数推算当峰值上限。

## 二阶段准备进度（上述未解析状态的后续记录）

D盘本次只读检查可用180,757,045,248字节。公开代码固定revision `c0a20e2bb698bbad11d143bdccd6c0760e9d6e5b`，逐文件SHA见`funasr-code-manifest-20261014.json`；只下载公开源文本检查，没有执行模型代码。核心model/ctc/utils未发现显式子进程或任意命令执行；仍存在checkpoint加载与FunASR自动加载器，后续必须固定可信模型SHA、关闭更新/在线下载并验证本地路径，不能据此宣称所有传递依赖已安全审计。固定HF checkpoint是无CTC版本，不承诺时间戳；当前utils使用的torchaudio forced_align也须安装后兼容验证。

已进行两轮`pip --dry-run --ignore-installed --only-binary=:all:`元数据解析，未安装、未创建venv、未加载模型/GPU。第一轮因官方`openai-whisper`没有适配wheel失败；第二轮用其公开依赖替代以诊断传递依赖，又因FunASR的`jieba`没有wheel失败。输入见`funasr-requirements-plan.txt`，它不是完整锁文件，不能直接声称可复现安装。详细日志位于忽略目录`.test-tools/funasr-plan/`。

公开PyPI源包只读检查：openai-whisper20250625为803,191字节，SHA `37a91a3921809d9f44748ffc73c0a55c9f366c85a3ef5c2ae0cc09540432eb96`，pyproject使用setuptools>=61.2、动态读取version；jieba0.42.1为19,214,172字节，SHA `055ca12f62674fafed09427f176506079bc135638a14e23e25be909131928db2`，setup.py使用distutils声明包/数据，Python3.12需setuptools兼容支持。源包仅内存读取构建描述，没有执行构建。后续需独立构建venv、明确SHA白名单、构建wheel后再完整求解/哈希/求和；不应全局放开任意sdist自动执行。另已确认pyopenjtalk-plus存在CPython3.12 Windows wheel，但不等于全部环境兼容通过。

当前未完成完整传递依赖锁定、总下载精确求和或安装脚本验收。主代理因真实候选暴露反馈授权漏洞，已将docs26优先于此准备任务；本节保留明确进度，不能把部分解析失败写成安装成功。共享ASR服务、系统Torch和Piper均未修改。

### CPU 隔离准备续查（历史阶段）

在 docs28 判分修复优先期间，仅继续依赖元数据解析：为已核来源的 openai-whisper/jieba 制作了仅含公开 Requires-Dist 的临时元数据占位包，位于忽略目录 `.test-tools/funasr-plan/metadata-wheels`，只供解析器诊断，**禁止安装，也不能作为真实 wheel 哈希锁**。第三轮二进制限定解析在 FunASR 的 `oss2` 依赖处停止（无匹配 wheel），未执行任何 sdist 构建或模型推理。故尚不能提供完整 CPU 下载总量或可验收安装脚本。

后续可执行顺序为：逐项审查缺 wheel 的固定源包及构建依赖；在新的独立构建 venv 内按 SHA 白名单生成真实 wheel；对真实 wheelhouse 完成全量版本/哈希锁和体积求和；再在另一个不继承系统包的运行 venv 安装、离线 import 验证。此流程不升级系统 Torch、不触碰 Piper、不停止共享服务。实际构建/安装及模型加载仍未执行；元数据占位目录必须从安装输入中排除。CPU 20 秒预算及 RAM 峰值只能在后续真实兼容测试中测量。

### 当前进度：安装与下载完成，CPU加载门未通过

上述解析阻塞已解决：89个官方依赖文件完成哈希核验，5个源包离线构建，86个真实wheel经独立审核后安装。`pip check`和CPU导入通过，Torch/Torchaudio为2.9.0+cpu，Transformers为4.51.3；系统Torch、Piper及四个共享服务身份未变。证据见 `funasr-isolated-installation-20261014.json`。

固定21个模型文件已全部下载并重新核验，累计下载2,513,475,891字节。原评测120秒加载超时，28条全部未尝试。随后修复Windows源码路径与线程数适配，完整离线回归65/65及独审通过；同预算仅加载诊断仍超时，三次线程栈定位在官方Qwen参数初始化。两次均无识别质量结果，不能将未尝试计作识别错误。当前结论为未满足已声明CPU加载时限，没有启用候选或修改共享服务；详见 `docs/29-Fun-ASR独立CPU验证规格.md`。

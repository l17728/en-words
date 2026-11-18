# 29 Fun-ASR 独立 CPU 验证规格

## 当前最终证据（2026-10-14）

原冻结28运行与后续仅加载诊断分别保留，均未通过120秒加载门。原运行0尝试/28未尝试，不是28次识别错误。适配修正后的诊断已验证固定模块和注册类来源，30/60/90秒堆栈均位于官方 `model.py:124` 的Qwen3构造参数初始化，120109ms停止；没有ready，因此实际实例来源及最终线程后置验证尚未完成，未运行预热或generate。该证据指向CPU初始化耗时，不能当作转写质量或bf16兼容性结论。

Windows远程源码参数改正斜杠，显式ncpu2并保持原intra2/inter-op1；新增6条合同测试，完整Python离线族65/65、0跳过、30.378秒通过（r9）。独审通过后运行唯一load-only诊断，峰值commit3.250GB、采样RSS2.897GB；子树与磁盘线程清理、文件封口一致。共享8000/8001/8002/8090的PID及创建时间前后一致。完整报告见 [加载诊断](../review/funasr-load-diagnostic-20261014.md) 和 [原冻结集结果](../review/funasr-cpu-frozen28-result-20261014.md)。不改变第三方初始化、不扩预算、不重跑28；下文未完成阶段文字为历史记录。

日期：2026-10-14。当前状态：隔离安装、CPU import及21个固定模型文件校验通过；唯一一次原定 CPU bf16 运行在120秒加载期限停止，未进入预热或正式识别，28条全部未尝试。运行门未通过，识别质量尚未测得，不能把保守空假设计账解释为模型准确率0%。资源已清理，共享服务身份不变；没有自动重试或放宽预算。完整结果见 [实际执行报告](../review/funasr-cpu-frozen28-result-20261014.md)。下文准备阶段记录保留历史；不停止共享 ASR、不修改系统 Torch/Piper、不更换平台模型。

本次父钟停止事件120062ms，受控进程120187ms；峰值Job commit 2,928,939,008字节、采样RSS 2,582,761,472字节，均低于5GB门。独立磁盘扫描最大1391ms，主RSS实际最大采样间隔172ms；进程树及磁盘线程清理均确认，源码封口一致。现有加载日志仅初始化提示，缺少阶段定位，不能据此判断是CPU构造、权重读取还是隐式网络；下一步需先只读分析，不启动新一轮。

## 固定输入与资源门

- 官方代码 revision：`c0a20e2bb698bbad11d143bdccd6c0760e9d6e5b`，逐文件哈希见 `review/funasr-code-manifest-20261014.json`。
- 官方模型 revision：`272c57b82523ada6fd87095e955f8e29100979ab`，完整21文件1,989,178,168字节；`model.pt` SHA256：`55ae0d2fee369f0f11cce0795f6927934ad17cf11b278a7e56a51272074160bb`。不额外下载另一个 Qwen 权重，不承诺无 CTC 模型支持时间戳。
- 累计新下载上限5 GB、隔离根实际磁盘占用上限10 GB（十进制）；下载前读取长度，流式计数，失败残片也计入预算。源文件、wheelhouse、构建缓存、运行环境、模型、报告均计磁盘；不得把缓存移到系统目录逃避计量。
- 推理子进程树总提交内存限5 GB（Windows Job Object 的 JobMemoryLimit，且退出时 kill-on-close）；RSS名义100ms采样，报告实际最大采样间隔，超5GB终止。提交内存与含文件映射/共享页的RSS是不同口径，不能声称前者必然更保守，也不能将采样称为瞬时硬RSS上限。磁盘由独立线程每次扫描完成后等5秒再扫，报告实际最大间隔及扫描耗时，超限/异常在主循环下一tick传播；可能存在扫描间短暂超额，不是硬磁盘限额。下载在写入前按已知占用与流量硬拒绝。模型就绪允许独立最多120秒，预热和每条请求各20秒预算，超时终止整个子进程树；不放宽预算换成功。

## 隔离安装步骤

1. 核对D盘空间、目标绝对路径位于 `D:\en-words\.test-tools\funasr-cpu`，使用新的独立构建和运行 venv，均不继承 system-site-packages。记录系统 Torch/Piper 版本前后不变；环境禁用用户 site，缓存/TEMP全部定向隔离根。
2. CPU输入固定 Python3.12、torch2.9.0与torchaudio2.9.0，显式官方CPU索引/对应可信wheel身份，不允许解析为CUDA包。其余起点沿已读官方requirements（transformers4.51.3、funasr1.3.26等）；解析所有传递依赖后冻结具体版本、来源URL、尺寸与SHA，再开始运行环境安装。当前部分依赖尚未锁全，不能把起点文件叫完整锁。
3. `openai-whisper`、`jieba`、`oss2` 等无wheel依赖可在独立构建venv正常构建；只接受官方PyPI对应项目固定版本的sdist，先记录SHA及构建依赖。构建本身执行第三方代码，限定目录与网络来源，逐包留日志；构建依赖也纳入下载/磁盘预算。超预算或Windows编译依赖缺失即失败，不修改全局编译环境。
4. 只从真实wheelhouse以 `--no-index --require-hashes` 安装运行环境，保存完整锁和构建产物SHA。此前 `metadata-wheels` 是解析占位，禁止加入wheelhouse或安装输入。验证所有wheel有实际包文件，运行 `pip check`、独立import与版本检查。
5. 下载固定模型文件并核SHA；代码只从已审固定本地目录加载。关闭更新与隐式在线下载，使用离线缓存设置；显式device=cpu，禁止CUDA设备初始化。官方bf16在本机旧CPU兼容性未知，先按原配置验证；若只能float32，作为单独声明的兼容变量，重新计算内存预算，不悄悄转换并宣称官方默认通过。

以上步骤形成可审查脚本后再执行；脚本必须拒绝已有输出目录，支持中断并保留失败报告，不自动删除任何共享文件。无需平台部署权限，因为此阶段没有平台变更。安全底座 `review/funasr_cpu_guard.py` 已有9条离线检查通过（`.test-artifacts/funasr-cpu-guard-tests.txt`）：真实Windows Job内存拒绝、RSS/磁盘超限终止、超时连同子进程清理、下载预算/哈希/失败流量计账等。此结果不代表依赖安装或模型可运行。`PeakJobMemoryUsed`在本机可包含被拒的分配请求，不当作RSS或已成功分配证明；内存测试以实际MemoryError及无成功标记为准。

## 验证与评分

准备入口为 `review/prepare_funasr_cpu.py`，依次使用 `--stage download/build/lock/install`。每次必须提供完整artifact计划文件及 `--expected-sha256`；计划需明确 `approved_complete_artifact_plan`、每个真实wheel/sdist的官方URL/字节数/SHA，以及固定构建与运行依赖。构建只读取已核artifact，`--no-index --no-build-isolation`，缺依赖即失败保留日志。`lock`生成真实wheel哈希清单，`install`还要求 `--runtime-lock-sha256`，运行环境用 `--require-hashes`离线安装与pip check。占位metadata wheel被文件内容检查拒绝。

完整artifact清单已形成 `review/funasr-cpu-artifact-plan.json`，89项387,249,238字节，独审逐项对官方PyPI文件URL/size/SHA核验通过；含模型与历史128MiB保守下载账共2,510,645,134字节。真实5个sdist的构建代码仍须下载后审查，下载门不等于build门。入口早期三个离线校验记录见 `.test-artifacts/funasr-cpu-prepare-tests.txt`，后续增量见本节末；均不表示已经安装。源包构建执行第三方代码，固定来源/哈希和Job资源控制并不是安全沙箱；不得宣称隔离venv能阻止任意第三方代码访问网络或文件系统。模型持久worker与28条顺序评测入口已实现，实际加载仍待环境兼容与资源窗口，不能用每题重载模型的总时间冒充热态20秒指标。

先离线验证路径/哈希/资源限制/超时清理（含故意超内存、挂起子进程夹具），再确认独占CPU测试窗口。模型加载成功后顺序运行原冻结英文16、中文12音频，不设置答案热词，不修改标签。每条20秒，连续5个运行错误停止，未运行仍保留完整分母。输出同次原始识别、规范化文本、WER/CER、关键句准确率、耗时、峰值内存与退出原因；人工已确认清楚的中文错误不能归因音源。

固定资源下加载或20秒失败也是有效结论，不改现平台，不调用云，不借较小参数量承诺本机实时能力。独立CPU通过仅证明该环境与冻结集表现，不能代替未见录音及真实浏览器采集验收。此阶段不向产品添加新的供应商配置；后续集成需另写规格与完整回归。

## 日志与失败矩阵

事件包含 `run_id/stage/case_id/model_revision/reason/elapsed_ms/bytes/rss_bytes`；枚举原因覆盖来源/哈希拒绝、依赖解析失败、构建失败、安装冲突、下载/磁盘超限、加载错误、内存上限、请求超时、无有效转写、清理失败。正文仅允许冻结合成样本在独立评测报告中出现；普通定位日志不记录音频、密钥或系统配置全文。下载失败保留已用预算，未安装完整不得标ready；模型退出与清理失败不得伪报资源已释放。

## 持久 CPU worker 与全28条入口（主审通过，工具已实现，真实运行待依赖就绪）

### 代码与调用边界

已新增 `review/funasr_cpu_worker.py` 和 `review/evaluate_funasr_cpu.py`，不修改产品路由或共享平台。固定官方代码位于 `.test-tools/funasr-plan/source`；入口先核对源码、完整模型与运行wheel锁的SHA，拒绝缺文件和错误revision，模型manifest必须精确为 `approved_fixed_model_manifest`。只启用本地FunASR `AutoModel(model=<本地模型绝对目录>, remote_code=<已核model.py>, trust_remote_code=True, device="cpu", disable_update=True)`，不使用README另一套Transformers checkpoint。`trust_remote_code`仅用于已核本地代码，不授权在线代码获取；依赖库是否真正支持禁更新参数在安装后核实，缺离线路径则停止，不能退回模型ID下载。

使用新runtime venv，清理Python路径继承，HF/Transformers离线、CUDA不可见、CPU线程2。读取并记录模型配置实际dtype，保持固定模型配置，不自动在bf16失败后改fp32。初始化和每次调用后检查模型参数device；不得初始化CUDA。一个进程只加载一次模型，随后串行请求，禁止每样本重载或自动重启掩盖失败。

官方 `model.py` 的 `inference` 读取 `hotwords/language/itn/llm_kwargs/max_length`。固定调用为 `generate(input=[wav_path], cache={}, batch_size=1, hotwords=[], language="英文"或"中文", itn=True, max_length=512, llm_kwargs={"do_sample":False})`；每次新cache，无跨样本上下文。语言仅来自manifest语言分组。最大长度512沿官方默认；确定性解码是本轮预声明设置，不声称与平台Qwen参数完全等价。不向worker传reference、答案、关键字列表或自定义提示。

### 进程、协议和时限

父进程在Windows Job中以挂起方式启动worker，加入5GB提交内存限制后恢复；沿用受审的RSS/磁盘轮询和进程树清理能力。现有 `run_bounded` 固定总超时不足以表达多阶段期限，实施时需要单独受测的持久会话监管接口，不能通过不断延长一个总超时替代请求期限。

不开放HTTP端口。父子通过隔离run目录的独占JSON文件通信：父写临时文件后原子发布 `request-0001.json`，子写相同序号的response；另用独占事件JSONL记录ready/started/completed。输入仅含序号、case_id、已核音频绝对路径和language；拒绝重复/越序/未知ID和超限文件。第三方stdout/stderr写独立文件，不作为协议解析来源，避免库打印污染或管道继承死锁。协议写入检查flush/fsync/close，父进程仅采信自己的单调时钟。

- 启动至ready总计最多120秒，包含Python/import/模型加载；ready只能在实际构造完成且device检查通过后发出。
- ready后仅一次独立1秒16k单声道PCM16静音预热，固定SHA、无文字标签，20秒预算。静音正常返回的空text允许通过，不能按正式样本的非空文本要求拒绝；仍检查正常调用、时限和CPU设备。预热不计28质量分母，但失败则本次质量集28条均未尝试，不偷偷改用测试样本预热。
- 每条20秒从父进程发布请求起算，包括文件读取、音频前处理、模型生成和结果落盘。仅生成耗时可另列，不能替代端到端预算。超时或worker崩溃立即终止Job并确认全部子进程退出，当前条记失败，余下未尝试；不重启继续取绿。
- 可恢复的逐条异常/空结果累计连续5次即停止；错误识别但返回有效文本属于质量失败，不是运行错误，不触发提前停。资源超限、协议错误、输入SHA变化和清理失败立即停，不等待5次。

### 冻结音频、评分与报告

入口只接受既有 `review/audio-fixtures/manifest.json` 英文16条及 `manifest-zh.json` 中文12条，核旁SHA、精确frozen状态、唯一ID、所有音频SHA及路径边界；执行顺序为英文原顺序后中文原顺序，各一次。原始WAV直接交官方前处理，记录采样率/声道/位深/时长及实际输入SHA，不新增选择性裁剪、调速、答案热词或标签分支。若官方前处理不能支持现有输入，整轮记兼容失败，修改前处理须另审单变量全28方案。

创建新输出目录且拒绝覆盖，开跑前写 `expected_cases=28` 和全部case清单。每条保存同次完整可序列化原始结果（有界大小）、选取的text、归一化文本、来源哈希、开始/结束耗时、固定失败reason；不做第二次推理评分。原始结果必须是单条包含字符串text的列表，否则记协议失败，不把任意对象字符串化成成功。记录峰值Job commit与采样RSS各自口径、源代码前后SHA及cleanup状态；源码变化使整轮验收无效。每条JSONL落盘后同步summary，异常退出仍能恢复已完成证据。

复用既有语音评分器的规范化和编辑距离定义，英文主要WER、中文主要CER；中文空格WER仅作附录，不作为质量门。错误和未尝试在完整16/12/28准确率分母中保留；另列仅已返回文本的micro CER/WER及覆盖数，不能用该子集冒充全量质量成功。失败行以空假设计算编辑距离的全量保守指标单列，说明未尝试不代表模型真实空输出。关键句用原manifest预先冻结分组与全文exact规则，不按本轮结果重分类。音源人工签收沿独立既有记录，尤其中文04/08/10不能再归因未试听。

候选通过要求先满足完整28运行成功、每请求≤20秒及资源清理成功，再按docs15既有语言/关键句质量门比较；加载与预热时间单列。即使通过，也仅是独立CPU与合成冻结集证据，不自动启用产品供应商，不替代未见真人录音或真实浏览器验收。

### 实施前离线回归矩阵

使用不加载模型的假worker覆盖：ready只在构造后、加载超时、预热超时、同PID多请求且只构造一次、请求超时及孙进程清理、连续5错误与未尝试分母、有效错误文本继续执行、空/畸形/超大结果、重复响应、路径/音频/源码哈希变化、输出独占和写盘失败。断言请求文件不含reference/热词，验证英文/中文评分失败分母、零次二次推理和报告恢复。涉及进程资源的测试由后台子代理执行；模型加载和实际28条必须等待浏览器重负载与其它推理窗口释放。

### 实施记录与执行门

独立脚本审查闭合缓存路径、junction和Windows wheel成员盘符三项边界，见 `review/funasr-cpu-isolation-independent-review.md`。新增monitor复用原Windows Job循环；后台测试暴露monitor异常后主进程退出与日志句柄释放竞态，已在finally杀树后显式等待主进程signaled，红报告 `funasr-worker-offline-first.txt` 保留，修后原guard用例与新worker用例全绿，详 `.test-artifacts/funasr-worker-offline-final.txt`（该次guard10+worker13+installer5）。后续封口哈希/质量门增量以最新回归文件为准，不混称旧报告已测新增用例。

运行错误使用固定原因：`load_timeout/request_timeout/protocol_error/worker_safety_error/warmup_failed/five_consecutive_errors/worker_exited`。CPU不变量失败和非法结果schema立即停止；空正式转写是可恢复运行错误。父进程按自己已发布的case_id记录超时、崩溃及畸形响应失败，不采信伪造ID；ready失败为28未尝试，预热失败单列且28未尝试，正式首条失败为1已尝试失败+27未尝试。`events.jsonl`记录ready/started/completed/stopped，不写原文；`cases.jsonl`保留冻结合成样本同次raw。收尾复核官方源码、工具代码、runtime锁、模型manifest和全部输入音频/manifest SHA，变化则门失败。

入口要求 `ENWORDS_FUNASR_CPU_EVAL=1`，参数为 `--run-id <新目录名> --source-sha256 <已审源码清单SHA> --model-manifest review/funasr-cpu-model-manifest.json --model-manifest-sha256 <已审模型清单SHA> --runtime-lock-sha256 <实际构建后审定SHA>`。真实runtime lock尚未生成时不得用占位哈希运行。结果写隔离根 `evaluations/<run-id>`，目录存在即拒绝。`runtime_gate_passed`与`quality_gate_passed`分列；质量沿docs15短词/词组≥90%、关键句100%，并要求完整运行门通过。CLI仅质量门全通过返回0，采纳/产品集成仍单列未评估。

最新清单/质量门增量回归：`.test-artifacts/funasr-worker-offline-inventory.txt` 中guard10+worker15共25条全部通过（9.268秒）；installer5条前次通过且代码未受此增量修改。精确模型清单门为21文件、1,989,178,168字节及必要资产路径；清单批准SHA为 `437c18e52cac12cb514de29526c4ea5cd648718f2d63e9997b2c1a6805db73b0`。报告 `quality_gate.languages` 明列每语言短词和关键句分子/完整分母、阈值及各自通过状态，英文WER/中文CER主指标不混用。原配置已只读确认LLM dtype为bf16，未切换fp32；尚无真实加载或质量结果。

随后完整review Python离线族50/50通过、0跳过（25.991秒，`.test-artifacts/review-python-offline-all-20261014-r2.txt`）。此前真实依赖准备暴露官方setuptools内嵌vendor METADATA被误判多主身份，已补红例并修为只选顶层wheel身份、仍检查全部成员路径；新增合法vendored与双顶层冲突两分支锁定。首轮49绿1红报告保留，不覆盖历史；此门通过只放行继续隔离构建，不表示runtime import或ASR模型质量已通过。

大目录监控补充：原磁盘扫描每文件重复检查全部祖先，2048文件夹具实测14.422秒、2112次根/祖先验证，足以影响父时钟监控。改为 `os.scandir` 栈遍历、每entry一次不跟随链接stat、任何reparse/symlink立即拒绝；根路径仅校验一次，同夹具扫描0.015秒且字节总量一致。该数值是本机单次对照，不是通用性能保证。磁盘扫描不再跟随RSS每轮执行，独立1秒调度，新增 `rss_sampling_nominal_ms/rss_sampling_max_gap_ms/disk_sampling_nominal_ms/disk_sampling_max_gap_ms/disk_scan_max_duration_ms`；请求20秒不变。红/绿证据为 `funasr-disk-scan-red.txt`、`funasr-disk-scan-green.txt`（guard+worker28条通过，保留超盘/路径/进程树回归）。

监管最终全族为 `.test-artifacts/review-python-offline-all-20261014-r5.txt`：54/54通过、0跳过，30.088秒。正常pip并发删除临时子项时，仅跳过子entry/子目录的FileNotFoundError，根缺失、权限错误、reparse仍失败。r4的两个夹具问题（scandir上下文模拟接口不匹配、极短扫描时间被时钟量化为0）已分别按真实context协议和可控20ms扫描成本修正，失败报告保留。runtime实际86 wheel锁已主审通过，SHA `5e0992db0018f13444c4c8f9542e0acd87602eb4b6e107f41797518c48fed636`，此时仅放行受控安装及import检查，尚未加载ASR模型。

实际安装已通过pip check及Torch/torchaudio2.9.0+cpu、transformers4.51.3、FunASR import，CUDA构建值None，未修改全局环境。大venv下扫描仍耗1.328秒、RSS最大间隔1.437秒，故上段1秒主循环磁盘轮询仅为历史版本。当前 `DiskMonitor` 将扫描移到daemon线程，完成后等待5秒；主线程不等待遍历，继续父时钟20秒及RSS监控。首次磁盘核验在受控进程启动前；退出先停止子进程树，再Event取消逐entry扫描并最多join5秒，仍存活则明确 `disk_monitor_cleanup_failed`，不伪称清理完毕；最终同步核盘在子进程停止后，不算活跃请求期限。报告使用 `disk_pause_after_scan_ms=5000`、实际max gap/max scan/final scan耗时及线程清理确认。CPU import已通过无需为此重装；模型只在监管最终回归和独审通过后加载。

线程版已通过架构实际代码复核，完整Python离线族59/59、0跳过（33.501秒，`.test-artifacts/review-python-offline-all-20261014-r6.txt`）。新增慢扫描等待取消时父期限仍可到期、跨线程错误/超盘发布与线程退出检查；模型下载另补共享operation.lock与精确官方HF CDN主机校验，仍冻结每文件SHA，不允许任意hf子域。此时模型权重仍在下载，无真实ASR运行结果。

### 2026-10-14 依赖下载与源构建审查完成

89 个真实官方依赖文件已受控下载完成，下载阶段退出 0；累计下载账本为 524,297,723 字节，包含先前诊断下载的保守计账，不能仅把最终文件容量当网络成本。下载时计划已归档 `review/funasr-cpu-artifact-download-plan.json`（SHA `c472b56b1ff936b48cc905dfdaa20ac5cbc6e48002b17c8892581b3562c39894`），结果见 `.test-tools/funasr-cpu/download-report.json`。

五个源包 antlr4-python3-runtime、crcmod、jieba、openai-whisper、oss2 已完成两方实际归档内容审查，入口及归档哈希记录在 `review/funasr-source-build-review.json`。允许既有离线构建环境正常构建；crcmod 可使用官方纯 Python fallback，不安装全局编译器。清除待审项后的构建计划 SHA 为 `71945b525c5caf839fdef9e2860c63fde3f229dbc8b806de2c60c442094761b9`。当前尚未构建、安装或加载模型；为避免干扰浏览器收尾，等待测试资源窗口。后续顺序仍为真实 wheel 构建及锁审查 → 独立安装/CPU import 验证 → 固定模型下载 → 独占窗口 28 条验证。

### 独立构建完成，等待安装门

首轮构建前预检错误地将 setuptools 的嵌套 vendored METADATA 计作多个主包，未执行包构建即失败。已修为只读取顶层 `.dist-info/METADATA`，仍校验所有层级成员路径；新增 vendored 正向及双顶层负向回归，独立全 Python 族 50/50 通过。原失败报告保留为 `build-wheel-identity-failure.json`，没有覆写成成功。

修后离线构建成功，五个官方源包均生成真实 wheel，未安装全局编译器。实际 runtime 锁含86个 wheel、385,798,509 字节，SHA `5e0992db0018f13444c4c8f9542e0acd87602eb4b6e107f41797518c48fed636`，位于隔离根 `runtime-wheel-lock.json`；待独立核对后用于哈希锁定安装。此时未安装runtime、未下载2GB模型、未加载模型。监管磁盘扫描性能增量另由后端代理验证，安装前使用其已审保存点。

### 独立安装与CPU导入通过

按已审86-wheel锁离线安装退出0，pip check无破损依赖。实际导入Torch2.9.0+cpu、Torchaudio2.9.0+cpu、Transformers4.51.3及FunASR成功，CUDA版本为None；尚未构造模型，不等于推理兼容/20秒质量门通过。可提交证据 `review/funasr-isolated-installation-20261014.json`。系统Torch2.6.0+cu124与入口SHA、Piper解释器/config及包清单、四共享服务PID/创建时间/exe均前后一致，未采集或输出带密钥命令行。

安装后真实目录扫描最大1.328秒，暴露磁盘巡检与父钟/RSS共线程会干扰20秒验证；已交监管实现独立线程与较低频巡检，保持识别预算不变，模型加载须等新实现测试通过。固定模型下载首个小文件成功后遇官方HF重定向精确域未授权，保留失败记录，获主审准入 `us.aws.cdn.hf.co` 后仍按HTTPS/冻结SHA/size下载，不使用域通配。

## 首次加载失败后的适配修正与独立诊断设计

唯一原定28条运行 `cpu-bf16-frozen28-20261014` 在120062毫秒未ready而终止，0尝试、28未尝试，无内存超限证据。原报告和SHA保留，不将未尝试称模型错答，也不能据此断言识别能力不足或纯CPU太慢。

只读代码发现两项适配缺陷：FunASR dynamic_import 按 `/` 拆模块名，而Windows `str(Path)`使用反斜杠，导入异常又被第三方吞打印，可能退到已注册内置类；AutoModel 默认 `ncpu=4` 会覆盖worker提前设置的2线程。尚无堆栈证明它们就是此次超时根因。日志“download models from model hub: ms”在解析本地目录时也会输出，不证明发生联网；本地目录存在会跳过下载，configuration.json已将LLM/tokenizer路径绑定本地，模型目录没有requirements.txt，未走自动pip分支。

修正合同由主审批准，限评测适配器，不改第三方/官方模型源码、权重、dtype或共享服务：

- AutoModel前将固定source目录置首并显式导入其model模块；核模块`__file__`及注册的FunASRNano类`inspect.getfile`解析后的真实路径均等于已核source/model.py。导入异常或来源不符立即失败，不能依赖FunASR吞错后的内置fallback。
- 传`remote_code=Path(...).resolve().as_posix()`、显式`ncpu=2`，device仍CPU，dtype仍官方bf16。ready前再核实际实例类来源，torch intra-op为2、inter-op为1（沿原worker设置）；不符不发布ready，不将内置类算固定源码。
- 增加flush阶段事件：imports开始/完成、固定源码导入开始/完成、AutoModel开始/完成、来源与线程核验完成。事件仅固定标识/耗时/已声明路径，不含音频或凭据。异常记固定阶段与类型并失败。
- 单独新增一次load-only诊断入口/新run目录，在原120秒加载预算内每30秒用faulthandler输出各线程堆栈，正常退出取消定时堆栈。只构造并核验，不预热、不generate、不跑28、不覆盖原报告。父进程仍Job/RSS/磁盘监管与杀树清理，不能靠增大超时让诊断变绿。
- 离线回归必须先锁Windows路径、显式ncpu覆盖、模块/注册类/实例来源不符、线程被改、导入异常立即失败、load-only零generate及定时堆栈取消。新增适配器与诊断由后端子代理实施/全Python回归，实际代码再交叉复核；通过后才执行唯一新加载诊断。

阶段可观测性此前不足，诊断结论必须引用实际栈；“没看到联网日志”不等于OS层网络审计。HF/Transformers离线环境保留，本次不建立新的网络许可或自动下载分支。

# CPU 加载诊断：参数初始化阶段超时

本次仅加载诊断 `cpu-bf16-load-only-20261014` 与原冻结28评测分开保存，没有预热、generate或新质量分母。修复Windows remote_code路径后，固定源码模块及注册类来源验证通过；AutoModel显式ncpu=2，保持原intra2/inter-op1。最终实例尚未完成，不能宣称其来源和最终线程后置验证已通过。

诊断仍在原120秒上限停止：受控120109ms，退出124，外层总128.141秒退出1。峰值Job commit 3,250,491,392字节、采样RSS 2,896,572,416字节，均未达5GB限制；主采样最大间隔172ms。进程树、磁盘线程清理确认，7个工具及清单/锁文件封口一致。

30、60、90秒faulthandler栈均位于固定官方 `model.py:124` 的 `AutoModelForCausalLM.from_config` 参数初始化链：30秒为Embedding的normal_，后续为Linear初始化及Qwen3 post_init/_init_weights。这是实际CPU初始化工作的证据，未显示HTTP等待；不能外推整个运行绝无网络，也不证明bf16推理兼容性。官方源码与dtype均未修改。

原始 `report.json`、flush阶段日志 `worker.log` 位于 `.test-tools/funasr-cpu/diagnostics/cpu-bf16-load-only-20261014/`。外层日志及终态位于 `.test-artifacts/funasr-load-only-20261014-console.log` 与对应 `.summary.json`。共享4端口PID和创建时间后验存于 `.test-artifacts/funasr-shared-service-after-load-only.json`，与原基线一致。

适配红测3/3真实失败已保留；最终全部Python离线族65/65通过、0跳过、30.378秒，见 `.test-artifacts/review-python-offline-all-20261014-r9.txt`，新增6条来源/线程/仅加载合同用例；实际独审通过后才执行诊断。未再跑Go/浏览器，无产品代码修改。

结论：这套原样CPU构造路径在现有120秒预算内未完成；不应直接归因识别模型能力，也不应扩大预算后覆盖原失败。进一步消除将被checkpoint覆盖的随机初始化需要先证明权重覆盖完整及缺失键处理安全，属于新的设计与验证任务，当前未实施。

## 冷启动优化只读复核

no_init_weights不是可直接启用的安全小补丁：固定Transformers会全局替换torch.nn.init，可能影响全部AutoModel模块；现FunASR加载器torch.load后deepcopy并把未匹配键保留为dst_state旧值，所以末尾strict=True不证明完整权重覆盖。RoPE inv_freq等非持久buffer不在checkpoint，CTC还有按loaded_keys有意关闭缺权重分支的政策，均需独立证明。后续应先设计严格键/尺寸/来源覆盖及buffer/别名/dtype处理，用小型合成checkpoint对照验证正确性，再审真实checkpoint覆盖；未实施优化、未追加加载。详细独审见funasr-cpu-isolation-independent-review.md最终节。
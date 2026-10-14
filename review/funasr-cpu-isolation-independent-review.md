# FunASR CPU preparation: independent review

Reviewed on 2026-10-14 by the backend/test reviewer. Scope: `funasr_cpu_guard.py`, `prepare_funasr_cpu.py`, and their offline regression tests. No shared service, product Go code, model inference, or global Python installation was changed for this review.

## Findings closed

1. `pip --isolated` does not honor the cache environment override. All pip invocations now explicitly use `--no-cache-dir`; temporary files remain under the isolated root.
2. Existing junctions in temporary/cache directories could redirect writes before the process monitor detected them. `isolated_env` now validates each directory before and after creation; the process launcher validates its root before resolving it. A real Windows junction regression verifies rejection and no writes to its target.
3. Wheel validation accepted Windows drive-qualified archive members. Validation now rejects drive-relative, absolute, UNC, and parent-traversal members using Windows path semantics; valid wheel metadata and payload remain required.

## Independent execution

Evidence: `.test-artifacts/funasr-isolation-independent-tests.txt`.

- Guard tests: **9 passed**, 5.471 seconds.
- Installer tests: **5 passed**, 7.287 seconds.
- No failures or skips. Tests cover failed-download accounting, budget rejection before network access, mismatched size/hash, descendant cleanup, committed-memory enforcement, sampled RSS/disk excess, reviewed-plan pins/hash, wheel traversal, and junction rejection.

## Approval and limits

The reviewed scripts may proceed to review of the complete artifact manifest and staged isolated preparation. This is not evidence that dependency installation, model loading, ASR accuracy, or the 20-second inference budget has passed.

Follow-up on actual preparation: the installer incorrectly counted vendored nested `*.dist-info/METADATA` as a second wheel identity, rejecting official setuptools before build. The fix restricts identity selection to top-level metadata while retaining safety checks for every archive member. Independent review confirmed the positive vendored fixture and negative two-top-level-identities fixture. The complete review Python suite then passed **50/50**, zero skips, in **25.991 seconds** (`.test-artifacts/review-python-offline-all-20261014-r2.txt`). The first full-suite failure remains preserved in the corresponding unsuffixed report. The suite includes the original resource guards, new persistent worker tests, installer, prior candidate proxy/lifecycle tests, ASR protocol, and speech scoring; it makes no model-quality claim.

The 5 GB committed-memory cap is enforced by a Windows Job before execution. RSS and disk caps are monitored by polling and may be exceeded transiently; they are not instantaneous hard limits. The 5 GB download ledger covers the controlled downloader, including failed or partial attempts. Offline pip flags and reviewed artifacts prevent normal dependency resolution from downloading additional packages; this is not an OS network sandbox for arbitrary build code. Build and runtime environments remain separate, with reviewed wheel hashes required before runtime installation. Shared ASR and other services remain untouched.

## 最终独立磁盘监管代码复核（2026-10-14）

architecture_review 已实际审阅最新 DiskMonitor、run_bounded 和评测异常记录接线。首次核盘在子进程启动前执行；后续扫描在唯一协作线程中运行，每次扫描完成后等待5秒，主线程只读取已发布结果及异常，避免大目录扫描阻塞父钟和RSS采样。每项文件仍拒绝符号链接/reparse，仅忽略已删除的临时子项；根缺失、权限及其它错误不吞掉。

退出顺序是停止并确认拥有的进程树退出，再取消扫描线程并有界join；仍存活明确失败，退出前poll不丢最后发布异常，最后核对目录占用。报告分别保留扫描时长/间隔、名义与实际RSS采样间隔及清理确认，磁盘/RSS轮询不是瞬时硬上限。Windows Job提交内存限制维持硬门；每条识别20秒父钟预算不扩大。当前已发请求遇监管/进程异常会记失败，不归作未尝试。

最终全review Python离线回归59/59通过、0跳过/失败，33.501秒，证据 `.test-artifacts/review-python-offline-all-20261014-r6.txt`。固定HF下载仅新增精确HTTPS域 `us.aws.cdn.hf.co`，相似域/HTTP拒绝仍有测试；下载入口与installer共用operation.lock，已有锁阻止任何下载且不删除他人锁。此前50/50记录保留为历史版本证据。

结论：此保存点无新增脚本阻断，可用于已授权独立CPU验证。安装import通过与监管离线测试不代表模型能在120秒加载、20秒识别或满足质量门；实际模型28条尚未执行，不改共享服务，不改变dtype。
## 最终适配及加载诊断独审

architecture_review 已逐项核对实际report.json、30/60/90秒三份栈、flush阶段事件和65条离线回归尾部。适配已锁定Windows as_posix、显式ncpu=2及原intra2/inter-op1，模块/注册类/实际实例三层来源检查、导入异常前置失败；load-only没有预热或generate，30秒栈定时器finally取消，父120秒与进程树/扫描线程清理保持。报告7工具和清单/锁SHA已重新对当前文件计算一致，4共享端口的PID/名称/创建时间前后完全相同。

实际阶段只到source_import_verified和automodel_construct_started，未ready；因此只能证明固定模块及注册类核验通过，不能宣称实例来源/最终线程后置检查完成。120109ms超时与峰commit3,250,491,392、RSS2,896,572,416字节均与原始报告一致；65/65、0跳过、30.378秒的离线结论匹配r9。三份栈确在固定source/model.py:124的Qwen3 from_config参数初始化链，而不是所采样时刻的HTTP等待；不外推全运行绝无网络，不推断bf16推理质量。原28仍0尝试28未尝试，独立加载诊断没有增加质量分母。

### 冷启动优化可行性边界（仅代码分析，未实施）

固定Transformers的no_init_weights会全局暂替torch.nn.init多种函数，并跳过其自身初始化；若围住整个AutoModel，影响不只LLM。现FunASR loader先torch.load再deepcopy，缺键/尺寸不符时保留dst_state原值，最后strict=True也不能证明checkpoint覆盖完整。官方模型还会按实际loaded_keys关闭缺权重CTC支路，不能无条件把这种有意缺失当可忽略。Qwen3 RoPE的inv_freq是persistent=False且由构造公式生成，不在checkpoint state_dict；其它非持久缓冲区、别名/共享权重及非参数状态也须分别核对。

故不批准把no_init_weights直接包在现loader外当小修复。可执行后续路线是先独立设计严格加载器：规范化checkpoint键并拒绝冲突/缺失/尺寸错误；核所有被跳过初始化且实际会使用的参数与持久buffer均有已核权重来源；逐项证明非持久buffer/常量仍被正确构造；明确有意禁用CTC与共享权重例外，不能泛化豁免；固定dtype/cast与有限值规则，避免deepcopy造成内存峰值。用小型同结构合成checkpoint做普通初始化加载与跳初始化加载的逐参数/缓冲区及确定性输出对照，覆盖缺键、NaN、尺寸、别名、CTC与finally恢复全局init函数，再独立审查真实checkpoint覆盖证明。保持现5GB/120秒/20秒门；通过前不加载或推理、不修改第三方源码/模型权重。当前结果可作为已完成诊断提交，但模型质量未通过、优化未实施。
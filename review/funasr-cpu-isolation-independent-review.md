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
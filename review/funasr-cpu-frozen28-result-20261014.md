# Fun-ASR CPU 冻结集执行结果

唯一授权运行 `cpu-bf16-frozen28-20261014` 未通过加载门。父钟在 120062 ms 记录 `load_timeout`，受控进程耗时 120187 ms、退出码 124；外层命令总计 129.875 秒、退出码 1。未收到 ready，未进行静音预热，正式音频 **0 条尝试、28 条未尝试（英文 16、中文 12）**。没有自动重试、改 dtype、扩容或切换 GPU。

这证明当前固定环境未在 120 秒内完成加载，不能据此断言模型转写质量或 bf16 不兼容。完整分母保留；空假设的保守 CER/WER=1 只是失败计账，实际返回文本 CER/WER 均为 null，不能称为 28 次识别错误。

后续静态审查发现 Windows remote_code 分隔符和 FunASR 默认 ncpu 覆盖两个适配缺陷；本报告的 source_unchanged 只说明文件SHA未变，不证明实际实例来自该源码。因此本次也不能作为已正确执行固定源码与2线程合同的性能基准。修复后仅加载诊断另行记录，不覆盖此结果。

## 证据

- 原始报告：`.test-tools/funasr-cpu/evaluations/cpu-bf16-frozen28-20261014/summary.json`；同目录 events.jsonl、worker.log、worker-config.json、warmup.wav 保留。
- 外层运行记录：`.test-artifacts/funasr-cpu-bf16-frozen28-console.log` 及 `.summary.json`。
- 固定模型 21 文件共 1,989,178,168 字节全部预检通过，清单 SHA256 `437c18e52cac12cb514de29526c4ea5cd648718f2d63e9997b2c1a6805db73b0`。
- runtime lock SHA256 `5e0992db0018f13444c4c8f9542e0acd87602eb4b6e107f41797518c48fed636`；源码清单 SHA256 `803f4556e14a42825c2e7a91388f9a4c7c959d9862eb8ab17c506680f6eeb7f0`。
- 进程树峰值 committed memory 2,928,939,008 字节，采样 RSS 峰值 2,582,761,472 字节；均未触及 5,000,000,000 字节限制。
- RSS 名义采样 100 ms、实际最大间隔 172 ms；磁盘扫描最大 1391 ms，独立线程最大扫描间隔 6406 ms；最终核盘 4,353,558,434 字节。
- `cleanup_confirmed=true`、`disk_monitor_cleanup_confirmed=true`、`source_unchanged=true`；运行与质量门均为 false。
- 8000/8001/8002/8090 的 PID、进程名和创建时间与执行前完全相同，见 `.test-artifacts/funasr-shared-service-baseline.json` 与 `funasr-shared-service-after-frozen28.json`。未修改共享服务。

## 结论与边界

当前候选不满足已声明 CPU 加载时限，不能采纳；需要另行设计只读加载诊断或新的资源方案，不能把延长预算后的结果覆盖本次失败。现有日志最后只有本地 AutoModel 初始化提示，尚不足以区分初始化具体阻塞阶段。后续行动等待主审，不自行重跑。

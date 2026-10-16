# 公开合成音频角色对照：独立离线复核

复核对象为 `probe_remote_transcription_roles.py`、v2 冻结清单及其离线测试。本轮未连接 SSH、未请求模型、未访问凭据文件；只读核验本机三条公开音频。

- v2 manifest SHA256：`f1a674ac99103b81714032f1602daa119d2a285949f41615cd63046bcade5df9`。
- 工具 SHA256：`974dbe5f769f3e1b255248cd5892e37326364be3cb1139305ed501d42714da3f`。
- 三条音频 SHA 均与冻结 manifest 一致；模型别名 × 三输入 × 两角色方案共 12 个唯一请求；8 秒、1024 tokens、temperature 0 校验通过。

首次离线运行 8 绿 / 1 ERROR：Windows 本地没有 Linux 的 `signal.setitimer`，测试在执行 REMOTE 之前即因 mock 目标缺失失败。证据 `.test-artifacts/remote-transcription-roles-offline-20261016.txt`。经主审准入，只修改测试：显式 fake `setitimer`、SIGALRM=14、ITIMER_REAL=0；没有改变远端真实 8 秒计时逻辑、工具或清单。

第二次完整 9/9 通过，0.874 秒，证据 `.test-artifacts/remote-transcription-roles-offline-r2-20261016.txt`。测试覆盖完整分母与唯一 ID、错误格式和静音、预算、请求状态顺序、原子报告失败、音频 hash 与 gold allowlist、实际 REMOTE 在假 opener 下固定 12 次且无重试、SSH 连接前失败与派发后未知状态。

静态审查通过本次受限实验边界：gold 不进请求；密钥只用于内存配置与 SSH stdin，不进报告；SSH 主机 key 拒绝未知；远端请求禁代理和重定向；单次 read cap 与 Linux alarm 保留；错误不改成语义成功；断连后已派发但未见事件记 unknown；完整 12 分母始终保留；每步落原子 checkpoint。原始公开响应仅写隔离 journal。

本对照是公开合成材料的角色/提示探索，不是非思考模式已支持、真人 ASR 质量、原生双工或端到端加密证明。normalized 指标会忽略标点/空白，仅为明确标注的探索指标；严格原始文本与空静音另列。v2 状态字符串仍为 `frozen_before_outputs_pending_independent_tool_review`，本记录完成独立工具审查但不改其冻结字节；真实 12 次调用由主审另行调度，不能把上述离线通过写成真实模型通过。

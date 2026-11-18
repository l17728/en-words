# 简单转写接入缺口与下一次有限实验设计

状态：只读调查与候选设计，待主审批准后才调用模型。2026-10-16未发送模型请求、未修改服务，也未读取无关远端文件。

## 当前可证明事实

`probe_remote_audio.py`已经发送标准Chat Completions `input_audio`（base64 WAV）、temperature=0、max_tokens=1024、非流式及8秒截止。提示只要求原语言转写、无解释、无语音返回空串，**没有把gold发给模型**。所以“改成简单指令”并非尚未尝试；max_tokens也不是已验证的关闭思考开关。

扩集14例每alias一次，28次全200/stop且预算内：Qwen中文9/12、英文1/1、静音0/1；MiniCPM完整严格匹配0/14，包含think包装等格式失败，不能全称听力错误。独立边界组12标签中猫源失败，实际每alias11次；Qwen完整匹配6/12、MiniCPM1/12，静音与脉冲均0/2。新边界语音未人工试听，不能将源音错误全归ASR。原报告与失败保留，不与重叠样本合并统计。

公开文本结构探针已经使用response_format json_object与1024tokens，仍分别收到markdown/think，故不能把HTTP200当参数已生效。普通WAV输入路径可用与精确转写合同达标是两件事。现有远端别名不证明底层revision、推理框架或非思考模式。

## 本次只读契约调查

经既有SSH主机校验仅重新读取两个已授权文档：

- `/root/llm-api-connection.md`，SHA256 `0af7650c2c40bb35e9817ec4fd38a88b57367f28f740f801cd32d7cb3b3d6c13`。
- `/root/ai-full-duplex-design.md`，SHA256 `3916ecacd8a4620ee3c49dd04f6822b524b46753f510f4e353d518b8f7993d33`。

两者与前次相同，均未给出网关实现文件路径、revision、vLLM/SGLang或enable_thinking、thinking_budget、chat_template_kwargs、reasoning_effort契约。没有扫描root目录、进程命令行、环境或上游机器；不能从通用官方模型介绍推导此网关支持某参数。下一步真正关闭思考需要网关实现/透传配置或实际版本对应的明确接口证据，当前信息不足以安全选一个专有参数。

## 可先评审的12次候选实验

无需猜专有开关，可以先测试**已有Chat角色接口下的转写指令位置**。仅比较当前user内指令（A）与相同转写约束放system、user仅要求转写所附音频（B），其余音频、模型别名、temperature、1024token及8秒硬预算不变。B只是提示适配候选，绝不命名为已关闭思考。若主审选择等待实现契约，则这12次不执行，禁止用猜测参数替代。

预先固定三份原有音频：已人工确认的TTS-ZH-01“苹果”、原英文清单首条及原数字静音。先核现存manifest中的原gold/音频SHA/来源、人审状态并冻结新实验manifest；英文仍按原人审状态记录，不补写认可。两alias×A/B×三音频=**12次实际调用**，各一次、串行、无自动重试，无新TTS/安装/真人上传。按预先固定交错顺序发同音频A/B，保持历史结果不覆盖；提示不含gold。

分别记：请求/格式成功率（单choice、stop、纯转写，无think/fence/解释）、完整内容匹配、静音必须空串、耗时原值与超时。格式失败不得剥think提取答案当成功；内容仅沿冻结NFC/大小写/空白/标点口径统计，另保原文格式结果，不能拿该宽松探索归一化驱动课堂命令。单个样本重复不构成独立质量分母，n小不报告可靠p95。

只有候选在这三对中不增加错误且静音不产生文本，才讨论扩大冻结样本；这仍不是生产准入。若静音或命令风险仍失败，保持现有文字回退与服务端精确RepeatMatch，不让模型自由文字直接触发skip/成绩。远端非思考/纯ASR专用契约、真实取消、保护传输和真人声学门分别保持未过。

## 执行前工具边界

继续SSH受信主机、密钥只在内存/stdin、固定端点、禁redirect和环境代理、公开合成HTTP窄授权。新工具需独占report+journal并在中断时保留完整12计划分母、attempted/failed/unattempted，不沿用旧脚本只在所有请求结束才写总报告的弱点；严格校验返回case/model恰为预声明集合。先离线测试预算/异常/不泄gold与凭据，再由子代理运行已批准实验。不得改生产HTTPS约束或共享模型部署。

### 工具评审保存点（未调用模型）

主审已批准上述12次候选设计，当前只完成脚本与离线用例准备，真实调用必须等docs37浏览器/A-B资源窗口结束及工具测试通过。入口`probe_remote_transcription_roles.py`；测试`test_remote_transcription_roles.py`由独立测试代理执行，不将准备manifest计作模型运行。

首版预评审manifest `remote-transcription-role-manifest-20261016.json`（SHA `da03c0a8c1d16177b1b2a17170d1c704a198247bee30672f1ee3aab2cd030e34`）保留历史，未取得任何输出。主审发现SSH中断未观测不能算未尝试、原地截断报告不抗写中断，修正工具后另冻结v2 `remote-transcription-role-manifest-20261016-v2.json`（SHA `f1a674ac99103b81714032f1602daa119d2a285949f41615cd63046bcade5df9`）。音频/gold/12请求集合不变，旧冻结不覆盖。

v2在派发前写完整计划，并将所有可能派发项设`dispatch_unknown`；接到started为`inflight_unknown`，接到result才completed。只有派发之前的确定失败才能保留unattempted。SSH中断不以缺失事件推断未调用。checkpoint在同独占目录写临时文件+fsync+atomic replace，故替换前失败仍有上一份完整12行报告和journal；不自动续跑未知请求。耗时严格整数非负，超过8000毫秒不能通过`accepted_within_budget`，内容和传输另列；静音即使只有标点也不算空串成功。

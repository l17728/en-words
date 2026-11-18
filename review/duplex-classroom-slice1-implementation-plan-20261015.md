# docs36 切片1实施定位清单

状态：2026-10-15静态代码定位和测试设计，**尚未实现、尚未执行测试**。G1冻结基础不等于已接入Room。本清单只准备隔离SQLite试点的持久模式/授权身份/入口分流；开发仍须主审明确批准。

## 文件与职责

新增 `store/classroom_pilot_*.go`：显式v2迁移、pilot state/transition receipt/answer claim/active command/outbox/job schema；按docs36分离命令幂等与逻辑机会唯一性。不能改G1 `duplex_receipts` 的既定结构冒充整课存储。模式、授权代次、逻辑rev、当前在途指针均由持久CAS仲裁。

新增 `api/pilot_mode.go`（建议名）：单一课次模式查询/分流、接管及撤权接口；失败不得默认legacy。`ClaimPilotConnection`重新校验owner/账号/课次后原子递增auth_generation并生成epoch，禁止因此递增logical_input_revision。旧epoch即使拿到最新checkpoint版本也不得写。内部调用使用受信类型，不能以客户端JSON role授权。

切片1尚无教学coordinator时：pilot写请求明确blocked/unsupported，不能落旧handler；只允许鉴权模式/身份/state读取与已实现的安全接管。独立测试显式生成试点基线，不能向用户开放半接线课堂。非pilot沿原合同。完整Teacher计划、tx工具、outbox投递及结算worker另片接入。

## 实际入口与最低回归映射

下列行号是本次冻结源码静态位置，后续编辑会漂移；方法名为稳定定位。逻辑测试与用户E2E分别落实，不能以内部无用户路径的方法伪造浏览器覆盖。

| 入口/文件 | 首片必须保证 | 最低Go/SQLite负例 | 最低用户E2E映射 |
|---|---|---|---|
| router.go:303 Enter | mode查询在Start/Restore修改之前；不能先建旧Room再判断 | pilot enter不调用旧Start/RecoverJudgment、不写snapshot；mode查询失败不走legacy | pilot入口明确未就绪，旧课堂正常；跨学生拒绝 |
| router.go:337 events、369 voice-text | 所有旧输入路由统一分流；裸旧context不能写pilot | old event/voice payload在pilot零Judge/工具/claim/状态写 | 两用户试点/旧课并行，旧接口提交不推进试点题目 |
| router.go:391 Room.Resume | 不能保留无新身份的裸Resume绕过 | pilot裸resume不改Paused/版本；legacy允许原合同 | 旧恢复按钮进入试点应提示同步，不伪成功 |
| ws.go:177 Enter、182 Join | 连接成功≠授权取得；持久generation接管后才接事件 | 两独立App接管，旧App携新version旧generation仍拒写；旧Join不激活delivery | 两窗口接管、旧窗不可播放/提交，新窗可查询历史事实 |
| ws.go:228 events、249 voice_text及resumeClient | 业务worker执行前再次mode/身份核验，排队旧帧不回落 | 入队后换generation/retire再执行，零副作用 | 断线/接管期间旧回调不推进；heartbeat独立 |
| ingress.go HandleClientEvent/Voice及所有Context/Background包装 | 包装不能旁路同一gate | 直接调用每个wrapper针对pilot拒旧身份；source缺失不授内部身份 | 对应REST/WS用例覆盖用户入口，wrapper另由Go锁定 |
| hub.go HandleEvent/HandleVoiceText及handle*Locked | 内部便捷入口不能绕过 | pilot内部裸事件/语音不调用Machine写入或Judge | 由旧REST/WS负例证明实际调用链；不新增测试后门 |
| hub.go FireTimeout/Tick/SessionTimeout/Resume；roomcontext.resumeClient | 定时、暂停及恢复也是版本化写；首片pilot旧timer不得推进 | timed-out pilot不写旧snapshot、不结算；legacy计时保留 | pilot等待不被旧timer跳题；legacy原课堂照常推进 |
| hub.go Enter/Join恢复、broadcast* | 未接新coordinator时不能生成旧恢复动作 | pilot恢复不DrainActions、不重选话术/鼓励计数、不发旧play_tts | 刷新不重复旧播放；明确试点未接线状态 |
| hub.go:725 Drop与overdue.go:301直接retired赋值 | 退休同时持久撤权；不能只改本App内存 | 两App退休后另一App旧generation不再写；数据库故障不能假称持久撤权完成 | 过期旧窗关闭/新窗拒写；管理员/其他学生不受影响 |
| app.go:540 saveLessonStateContext；调用于roomcontext:35、hub:326/380 | 旧snapshot写封口，不能覆盖pilot同事务兼容镜像 | pilot旧保存拒绝且旧runtime值不变；所有三个调用点锁定 | 故障/重连后历史成绩不倒退；组件级封口Go验证 |
| lessonexecution.go commitLessonWithAgents、hub.maybeFinishLocked | pilot不能旧路径直接结算；首片未接job应阻断 | pilot零cards/memory/report/notice写，legacy结算不退化 | pilot不显示假课报完成；旧完整课堂生成课报 |

扫描结果：生产代码无 `Room.Machine()` 调用；该公开可变指针辅助入口主要在测试中。记录其未来绕过风险，但不为不存在的生产调用扩大首片改动。后续新coordinator不得用该指针先改活动Machine再事务保存。

## schema和身份最低确定性用例

- 模式迁移只允许无活动请求的非试点课；schema损坏/未知版本失败可见；不能从旧snapshot无声回退。显式隔离迁移失败整事务回滚。
- owner检查和receipt查询必须鉴权；同command同fingerprint返回原事实，不同fingerprint拒绝。receipt读取不恢复旧媒体lease。
- 命令receipt允许同机会多个纯控制命令；answer claim唯一键lesson+logical_input_revision不含epoch。换utterance/连接/新command不得再次消费旧机会。
- state.active_command_id与active行同writer CAS；同command不二次Judge；旧token在新worker回收后不能提交。committing不可仅凭租约过期或not_found解除。
- 接管/禁用/退休更新auth_generation不更新机会rev；溢出明确拒绝。两独立pool竞争必须验证，不以进程mutex代替。
- 旧入口分流查询DB失败时fail-closed，不能以“未知模式”进入legacy。所有失败日志固定reason、trace与课次摘要，不回显原文/音频/凭据。

## 后续切片依赖

第一片通过只证明“旧入口不能绕新门”和持久身份可靠，不开放自动教学。第二片再实现DetachedLessonState、Teacher冻结计划与tx-bound执行器；第三片连接checkpoint/compat snapshot/audit/receipt/outbox同事务；第四片完整输出恢复及completion job、模式排空。只有全入口功能等价和故障E2E完成才能开放mock试点课堂；真实音频/自动教学继续受G2/G3独立门控制。

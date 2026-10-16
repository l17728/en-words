# 课堂提交阻塞期间的体验与打断保护（只读盘点）

2026-10-16：基于当前冻结生产源码；未改代码、未运行测试。SUB02 原始 `start_repeat` finalize=21678ms 仍是失败证据；单次诊断70ms不能关闭它。下面区分已实现legacy行为与docs32–36尚未接入的试点合同。

| 场景 | 当前真实行为与代码依据 | 差距/边界 |
| --- | --- | --- |
| TTS结束后提交20秒 | Classroom.tsx:213–230 本地complete先清字幕、置completed，再发tts_finished；因此已结束音频不会因服务器慢提交继续播放。服务端hub.go:333–358先调用持久Teacher工具，再emit业务动作。 | 同一tts_seg旧界面可继续显示而字幕空白，没有“正在保存/等待下一步”状态。不能以页面已连接推断教学可推进。 |
| 播放期间学生插话 | cancelTts在Classroom.tsx:172先递增epoch/reset字幕，再abort本地Audio、清队列与定时器、cancel浏览器播放；收到暂停/非播放phase/新业务动作、断线或卸载会调用（:354–365、:501、:506）。 | 当前课堂没有接入VAD起音→本地cancel；自动识别只在repeating/wait_answer授权后启动（:535、:697），不是边播边听。仅发送控制/答案也不会同步调用cancelTts（:585–635）。后端提交卡住且尚未发取消动作时，不能宣称插话立即停声；现有本地watchdog仍可结束失败段。 |
| 作答等待 | wait_answer typed与voice共用AnswerFlight，同步锁重复输入、停止采集并显示“正在判分，请稍候”（:590、:626、:866–876）。 | 该提示不区分计算/落盘/未知结果；tts_finished、重复匹配及一般控制没有对应pending。没有当前课堂持久command查询/committing_unknown恢复UI，不能承诺刷新会知道原提交是否完成。 |
| 迟到数据与音频 | :345–352拒绝非当前epoch和不递增seq；新context撤输入ready；native/local采集固定capture/token/实例守卫；cancelTts失效媒体epoch/字幕lease，旧onstart/end不能重启已取消音频。 | 这是当前连接/媒体保护，不是docs36 logical_input_revision、持久claim、outbox和effect lease。未收到新phase/epoch前，不能凭客户端推断旧结果已经失效或事务已回滚。 |
| 文字fallback/重连 | router.go:337/369的REST和ws.go:228/249的WS均进入ingress.go:60–69、86–101，同一Room.mu下执行。hub.go:348工具提交也在该锁调用链内。WS读泵heartbeat可独立ACK（ws.go:312），答题发送失败不自动REST重放（Classroom:590–591、:626–627）。 | REST文字并不绕过Room提交锁；UI“已连接”可能在业务卡住时一直存在。退出导航会本地清音频/麦克风，但不等于撤销已经开始的数据库提交。 |

## 已有规格承诺（不能写成已实现）

- docs34:46、86：近端speech-start本地立即可逆停播，不等ASR/服务端；软件VAD→cancel p95≤120ms，物理静音独立验收。当前VoicePreflight仅观测音频/VAD，没有课堂输出接线（App.tsx:30，VoicePreflight.tsx:45–51）。
- docs34:21、58与docs36:166、186、244：转写final不等于提交；typed/voice统一逻辑机会，committing未知保留原稿/ID，只查receipt/state.sync；not_found不能解锁，active_input.can_submit由服务端事实决定。
- docs36:190、198：effect.received/playing/ended分开，取消/失败不是完成；新lease恢复，旧播放不得复活。docs36:94：最终答案已存与结算待完成分账。
- docs34:89、141明确计算8秒和事务COMMIT分别取点；不允许把数据库慢提交算成未写，或靠增加等待门通过响应速度验收。

## 最小可测试场景（建议，尚未执行或开发）

1. **Teacher工具提交受控阻塞20秒**：独立测试harness在真实提交阶段设有界屏障；真实浏览器先完成音频并发送tts_finished。断字幕立即清空、零重复ACK；同时记录页面旧相位/连接状态及后续真实推进时间。保原15秒门，当前长尾应失败，不在测试中延长。
2. **播放中本地打断与慢提交并存**：未来pilot接线后，真实播放+受控近端起音，服务器提交屏障仍未释放；断本地cancel/字幕清空无需等待服务端、旧playing/end零推进、新授权lease才恢复。mock只证明协议/生命周期，不证明真人AEC或物理静音。
3. **提交未知后文字与刷新恢复**：未来统一提交接线后丢commit响应，typed/voice按钮共用gate；刷新state.sync返回active_input后仍保原ID/正文，not_found且仍committing不得再答；真实receipt到达仅一次推进。另学生课堂仍有独立前端响应，但数据库共享writer的速度不能预先保证。
4. **当前双通道锁边界**：同一课WS提交卡住时，经真实认证REST发送文字，记录其同Room等待及取消结果；心跳仍可通。断前端不会自动把未送达答案转REST重写、退出清媒体。此测试锁事实与保护边界，不把REST称为数据库阻塞逃生通道。

SUB03本次只是held媒体夹具同步修正，不能替代以上真实阻塞/插话/持久提交UX验收。SUB04未出现新失败，未修改。

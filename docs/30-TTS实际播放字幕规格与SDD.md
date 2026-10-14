# TTS实际播放字幕规格与SDD

日期2026-10-14。状态：设计及实际代码独审通过，定向回归通过；最终86条分批完整验收及真实字幕1条通过。用户要求在TTS说话时显示字幕；不改Go协议、模型提示、朗读内容、播放次数或成功回执政策。

## UI与用例

课堂主内容中，单词/释义下方、老师气泡之前，增加当前朗读字幕区。实际播放时显示“正在朗读”及当前完整段原文（英文或中文），无音频播放时为空；保留适当最小高度，减少空态与短字幕切换时的布局变化，长句允许自然增高。`role=status`、`aria-live=polite`、`aria-atomic=true`供辅助技术；文本用React节点转义，不用HTML插入。宽度不超过容器，长句/长单词自动换行；流布局而非fixed浮层，不遮挡作答控件。字幕是实际媒体状态，不复用老师气泡或snapshot文本，不因下载/合成已发起而提前显示。

1. 英文重复/中文朗读逐段出现当前text，onend后清除，段间间隔不显示上一句。
2. 本机中文合成加载及Audio.play() Promise等待时不显示；仅HTMLAudio.playing触发显示。中文失败进入浏览器降级时先清除旧尝试，只有新utterance.onstart才显示。
3. onend/error/timeout/cancel、pause、离开当前业务相位、断线、切课程或账号、卸载时清除。旧播放回调迟到不得恢复旧字幕或清除新字幕。
4. 字幕仅当前页面当前会话；学生A与B各自媒体字幕不串，登出或换号不保留A文本。

## 实现边界

新增小型字幕状态控制器：begin分配严格递增lease，show(lease,text)仅接受当前lease，clear(lease)只清本次；reset失效所有旧lease。DOM更新经受控订阅或回调写React state，cleanup清监听。实际播放尝试与字幕lease绑定，不使用文本作为identity（相同词重复也各自独立）。

Classroom串行队列每次实际媒体尝试申请lease。原epoch、context、fired守卫保持；浏览器onstart后show，complete先clear后推进下一段；cancelTts reset。平台音频失败准备降级时结束旧lease并为浏览器分配新lease，旧平台回调不影响新播放。phase变化清字幕但不人为发送成功ACK；正常业务帧仍唯一启动源。卸载后的异步回调不可写新组件。

`playLocalChinese`增加可选第四参数onPlaying（第三参数lessonId保持兼容），仅player.playing且当前token/signal有效、尚未settled时调用。成功/错误/超时/abort任一路径统一幂等settle、撤销事件监听及timer，finally pause/revokeObjectURL；回调不得在网络response/blob阶段执行。其他调用不传参数行为不变。

## 定位与测试

保留原[playback_completed]/[playback_failed]及长度/语言/segment日志，不打印字幕正文。可测DOM显示即可，不新增含正文的全局探针。

Node：字幕lease顺序、多次同文本、旧show/clear/reset、独立实例隔离；音频回调仅playing、late playing/ended/error在settle后无效。Playwright新增独立字幕族：可控真实浏览器媒体协议夹具，验证加载无字幕→start显示→end清除、多重复英/中文顺序、平台Audio.playing与失败后浏览器降级、timeout/cancel/phase/pause/断线清除、旧回调及双学生隔离。先新E2E旧产品红，再修复绿；不把媒体stub通过当真实音质通过。真实B媒体仍按原音源/严格评分另验。

最终完整Node族、精确总数E2E、lint/build随本阶段所有产品改动统一冻结执行；模型对照独占期间不运行重CPU全量或真实推理。所有BUG回归继续保留，AGENTS不改。

独审补充：UI onPlaying抛错必须幂等失败并清理事件/URL，不能假成功ACK；reset/clear先失效lease再cancel媒体，防同步onend重入。

历史实施保存点（现已完成接入）：新增当时尚未接入的audio/caption.ts与audio/playback.ts纯控制模块，Node8条离线测试已通过（真实playing门、异常/取消/timeout幂等清理及lease隔离）；当时Classroom/local.ts尚未改；之后SUB01旧前端红已锁并完成接入绿测。

主审批准恢复边界补修：state_snapshot进入非tts_seg/reteach_show相位与phase_changed共用cancelTts，先失效字幕lease再停止旧音频；同朗读相位不取消/不启动音频，暂停仍取消。SUB03首轮默认15秒等待弱断言可能等到看门狗被判绿，不能当修复证据；收紧同步UI取消断言500ms后真实红subtitle-snapshot-red-r2.json（字幕仍cat）。补共享相位决策Node及E2E媒体cancel计数，不只藏字幕。


## 实际回归与证据边界

新增Node10：lease生命周期/同文本重复/旧回调/独立实例、snapshot共享相位取消、Audio实际playing门、结束/取消/异常/超时、observer抛错及falsy rejection均通过。加原Node17后正式27条。字幕确定性E2E六条：SUB01双语2+2序列与间隔；SUB02平台ended/fallback两条；SUB03snapshot离相位/暂停两条；SUB04断线/换号。原G09四种失败路径额外断言字幕为空。JD28两条亦已真实红转绿，正式全套86=41+1+26+9+1+6+1+1。

红证据：subtitle-ui-red.json缺字幕区；subtitle-snapshot-red-r2.json验证离相位后500ms仍残留cat。后者原15秒弱断言可等到看门狗自然清除，首次误绿报告保留但不作修复证据。semantic-caption-targeted.json五条全绿；subtitle-recovery-green-r2.json三条全绿。上一恢复夹具失败：B故意hold后等待A登录，B自己的看门狗自然清空；改为仅冻结B的测试时钟，不改产品超时。换号后的终止视图已移除字幕区，断言按契约改为区域不存在、旧词不存在、迟到回调无法恢复。

实际字幕单独验证使用playwright.subtitle-real.config.ts与subtitle-real.spec.ts：真实英3段cat，真实中文1段“猫是一种常见的家养动物。”；该句为独立字幕/截图夹具，不是已知B答案，也不声称孤立猫音质验证。真实NativeAudio playing/end与DOM字幕时序逐段核对，加载及段间为空，截取正常中文发声中页面，未暂停音频取图。到跟读即止，零ASR、零学生作答，不抵消真实B整课0/1或模型安全门。

旅程夹具审计：最终86首轮四段均completed，start_repeat在原整体15秒截止前仅215ms到达，UI轮询未及时观察；不是字幕导致取消。主审批准按四个实际completed阶段各自原15秒同步后再等跟读UI，不改播放次数/时长/间隔或产品；详见docs17。前77通过保留，余9另批，最终不伪称单次运行全绿。

## 当前字幕与语义路由冻结树验收（2026-10-14）

确定性 **86/86通过、0失败/跳过/flaky**，Node **27/27**，lint **0错误0警告**，八套TS/Vite构建通过。此为同生产SHA的分批合成证据：`.test-artifacts/2026-10-14-final86` 已通过 recovery41/overdue1/feedback26/legacy9=77；共享旅程夹具经主审和架构独审修正阶段同步后，`final86-r2` 的journey1/firstuse1/playback1/closure6=9通过。首轮journey计时失败保留，未声称一次全86绿；产品未为该夹具改动。

唯一机器汇总 `review/runtime-browser-acceptance-20261014-final86.json`；生产非测试Go+web/src SHA `54c462c90dd7581826fdf7f0163d561902e8cbe893fa274bda7cbcd9678984c9` 各批前后一致。server SHA `13ebca11a31692efad58b32fa0f8d330758a816fc397119674deccda19ee35a5`；harness SHA `c18bd11587d02834eb91ae6de6e5e7b5f2ea8b94da81c4bc6a6894ced37e4959`。二进制由后端代理冻结构建，该源hash不声称涵盖所有依赖/构建输入。8页面路由与FR A–I（含H配置）沿既有映射全覆盖，不冒称浏览器语句覆盖采样。

真实字幕独立 **1/1通过**：3段英文cat与1段完整中文“猫是一种常见的家养动物。”，实际8090合成200、NativeAudio playing/end与字幕时序一致。报告 `review/real-subtitle-acceptance-20261014.json`；发声中截图 `review/actual-chinese-subtitle-20261014.png` 已人工查看位置。范围仅TTS字幕，零ASR/学生答案/判分；不是孤立猫音质证明，也不抵消原 `review/real-media-b-final78-20261014.json` 的完整B课堂0/1或模型安全门失败。原失败均保留，没有重跑B挑绿。

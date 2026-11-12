# 本机多词课堂与 ASR 故障恢复规格及 SDD

状态：实施前多域评审已通过，进入实现；评审问题与范围见 `../review/local-classroom-journey-design-review-20261112.md`。本片只验当前 legacy 课堂；Pilot v2 产品接线另属下一阶段。

## 目标和边界

`firstuse-journey.js` 已证明三词 Mock/文字业务，`two-student-real-media.spec.ts` 已证明本机双学生各一词媒体；本片验两名学生各三词、本机 ASR/TTS、最终测、课报统计和下一课。合成 WAV 不代表真人发音、回声或真人试点。每次隔离 SQLite、Go/Vite 进程和独立账号，`-providers local` 指向 8090；不使用开发服务 8080/5173。

## 冻结词与音源

A 词库 `apple/苹果`、`banana/香蕉`、`orange/橙子`；B 词库 `cat/猫`、`dog/狗`、`teacher/老师`。教学跟读英文 WAV 均来自 `review/audio-fixtures/manifest.json`，先校验 gold、路径与 SHA-256：

| 学生 | 词 | WAV | SHA-256 |
| --- | --- | --- | --- |
| A | apple | `en/01.wav` | `4c7d98e368f9d06f1b0d9b64979ec032254a341025b3cec7f489cf03bdc1585f` |
| A | banana | `en/02.wav` | `6e9d180a8b51265b52efec7feeead8313fe0f3f40cd98fe25212c77488812c4b` |
| A | orange | `en/03.wav` | `8fd1ec9af5dd7ed18672a76a2d9db73f1f819b87c4ff6458aeb8070bf6491caf` |
| B | cat | `en/05.wav` | `def819a48d66c1058257c227245f267714ff74547e122952d8f6e2a1adb2eca5` |
| B | dog | `en/06.wav` | `446bff2b7ee7cafae8557928ddddb5d54d8cf8adb3decf842d53955c2ebaa723` |
| B | teacher | `en/07.wav` | `ece5159f3adad1f0714bebc1bb1134951375709b79af7d895c9c09fd5618aaf4` |

中文 `zh/06.wav` 的 gold“我会了”与 SHA-256 `86d422eb84386a90eed65f9fc49cc454f043d4f78c1c73cec6c3377a34cc1476` 单独由 `review/audio-fixtures/manifest-zh.json` 校验。先修复下节 BUG、预检本机转写确为整句“我会了”，才可在任意实际 final 题中用于正向自述；它不是该词的中文释义录音。修复前绝不能把 HTTP 200 冒充答对。中文教学 TTS 仍由平台生成。

## BUG：自然整句会／不会确认未命中

课堂提示要求学生确认会或不会，但现有精确名单缺少自然说法“我会了”“我不会”。在 `wait_answer` quiz 相位，完整“我会了”应走确定性正向自述，完整“我不会”应走确定性负向自述；不调用 LLM。“我不会”仍遵守两次机会：首次进入重教，第二次记未掌握并推进。“我会了”对当前题恰好记一次正确。不得全局增加模糊匹配或子串匹配；`repeating` 阶段二者均不能替代目标英文跟读，也不能绕过原指令权限。

**先收紧归一化，再加入别名。** 当前 `judge.normalizeZh` 与 `voicecmd.MatchesNormalizedAnswer` 会删掉所有标点，令“我会了？”、引号包裹或内部标点可能被误当完整陈述。quiz 自述须另设受限归一化：允许安全空白、单个末尾陈述句号/感叹号和既有单个语气词；遇疑问号、引号、内部标点、重复末尾标点或额外语句直接返回未确认，进入原普通答案/澄清路径，不授予正负自述。“我会了吗”“我不会吗”“不是我会了”“我不是不会”同样不得直接确认。否定优先；后端 `judge` 与 `hub` 问答入口使用一致合同，语音/文字不能分叉。`event_received` 记 lesson、event_id、提交时 epoch/turn 与 trace；`judge_decision` 只记固定来源/原因/结果与 trace；同一 trace 下的 `quiz_judged` 记 lesson、word_id、kind、round、quiz_seq、判分时 epoch/turn。两条 turn 分别标记输入与判分相位，供定位，不记原始转写正文。

输入清洗不能改变判分语义。若答案含 HTML 标签、控制字符或被截断，导致 `sanitizeText` 与去首尾空白后的原文不同，语音和文字入口都返回 `event_rejected(answer_text_changed_by_sanitizer)`，保持同一 turn；日志只记原因、相位和长度。“我会了<不是确认>”与“我不会<不是确认>”不得先清洗成确定性自述。之后学生可重新提交纯净整句。

先用 Go 单测重现“我会了/我不会”未命中及标点误判，再修复并验证正负、疑问/引用/内部标点/否定/长句负例、相位权限、第一次/第二次机会、重复 event ID 与迟到 turn。Playwright 分两类：冻结 `zh/06.wav` 经真实 MediaRecorder→本机 ASR 后，在任意实际 final 题确认正向结果与一次推进；文字 UI 提交“我不会”两次，断言重教及未掌握结算，另逐项锁定疑问、引号和内部标点不直接判正确。若 ASR 不是整句“我会了”，保留质量错例并由文字路径单独验证业务语义，不篡改 gold 或放宽匹配。每个 BUG 同时保留 Go 与 Playwright 回归用例。

## MJ01：双学生多词旅程

媒体夹具按学生、相位、当前 `word/quiz_word` 和 turn 选 WAV，缺映射直接失败；final 的 `zh/06.wav` 只表示正向自述，不按词义映射。教学阶段按实际顺序教完三词，每词英文跟读通过真实 MediaStream→MediaRecorder→Go `/transcribe`→8090 ASR；若某词预检不通过，记录错例并用 UI 文字继续，不能静默换夹具，也不能将该学生的“三词真实 ASR”计为通过。**全部教学结束后**才进入唯一 final；题池打乱，按实际 `quiz_word` 决定答题，绝不假设首教词就是首题。final 至少一题用经预检的独立中文自述 WAV；其余用 UI 文字覆盖首答正确、首答“不会”重教、第二答“不会”记未掌握、显式“跳过”只推进一次。每个真实 TTS 段要求 Audio `playing→ended`、对应字幕只在播放期间出现、唯一 `tts_finished`；两个账号的音频、字幕、ACK、转写与课报不串用。WS seq 单调、event ID 唯一，旧 turn 重放不再判分。

课报和统计刷新后仍一致。`scheduler.go` 的 `SettleLessonFinished` 将本课新卡转为 learning 并设置 `NextDueAt`；`planNext` 纳入 `NextDueAt ≤ now+24h` 的到期 learning 卡。现有 `firstuse-journey.js` 已用三词、其中至少两词未掌握，实际进入不同 ID 的下一课，构成本 fixture 的先例。首课结课后才请求调度并核对下一课非空 batch、至少一词和不同 lesson ID，再实际进入；首课尚未结时不得预建第二 scheduled 课。`wordsPerLesson` 生产有效范围 50–100，`-testmode` 只拨已生成课开放时间，不缩小该范围。若策略或时钟导致无下一课，记失败和策略差异，不造空课。

## MJ02：语音失败、迟到与文字恢复

**MJ02a** 仅对一条 `/transcribe` 注入 503，其他 ASR/TTS 真实运行。原 turn 暂停，展示重试和“继续并使用文字”；故障不记错误答案、不推进。文字提交后原 turn 恰好消费一次，重复点击及重连无二次推进，后续朗读和课报完成。**MJ02b** 单独持有一条 200 响应，待原 turn 因超时或明确切换失效后释放，断言旧转写不判分、不推进；同一请求不能既返回 503 又迟到成功。若 8090 整体停机连 TTS 一并失效，则记环境故障；TTS 暂停/重试沿用 `interaction-boundaries.spec.ts`、`recovery-regression.spec.ts` 的证据。

**MJ02c 旧文字跟读响应保护下一题草稿。** 跟读文字的 REST 结果可能晚于课堂 WebSocket 的 `show_quiz_item`。客户端先按 WS 授予新题输入；REST 结果收尾只能清除/报错其原 `repeating` turn 的草稿，不能清除新 `wait_answer` turn 已填写的文字或让回答按钮禁用。浏览器故意持有旧 `/voice` 响应，在新题中填答后才释放，并断输入值、按钮、唯一判分；Go 集成锁定同一 `epoch/turn` 的请求只消费一次及返回帧所属上下文。首次 trace 的快速填答触发草稿丢失，作为修复前红灯保存。

## 测试实现和诊断

复用 `isolated-fixtures.ts` 与现有媒体注入，新增专属 Playwright testMatch/config。`Classroom.tsx` 的 `?e2e=1` 禁自动本机录音且没有手动录音按钮，所以MJ01/MJ02a/MJ02b 使用正常课堂 URL；MJ02c 的纯文字竞态用 `?e2e=1` 禁止自动拾音。需要 UI 文字输入的轮次由夹具持有可控 `getUserMedia` Promise；每次 turn 离开后拒绝为 `AbortError`，未创建 MediaStream 时活跃轨数必须为零；若晚 resolve 出流，立即 stop 全部轨。测试结束释放全部待定 Promise。不得调用页面私有录音函数或用 REST 代替 UI。Go 单测/集成验证幂等、迟到和故障语义。

证据逐学生保存脱敏 lesson ID、turn/epoch、WS seq、段号、HTTP 状态/时延、Audio `playing/ended/pause`、字幕时刻、ACK/event ID、判分与报告计数。日志以 `trace_id` 串起 `audio_rejected`、`tts_segment_scheduled`、`playback_completed`/`playback_failed`、`event_rejected`、`judge_decision` 和结课；不保存密钥、原始转写或真人音频。非注入请求不得有 429/5xx，失败 trace 必须保留。N=4/8 容量、文本模型复杂语义质量和真人课堂各按独立门槛验收。

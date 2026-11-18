# 简单语音确认首片独立架构、安全与Agent复核

日期：2026-10-15。范围：docs38批准的旧课堂RepeatMatch与精确控制口令；独立只读检查当前生产代码、测试及01–08规格一致性。冻结binary前缀282bd57；后端r3定向通过由后端回收，本记录不代替完整Go/浏览器验收。AGENTS未改。

## 实际检查

- `judge/repeat.go`独立目标词法：NFC/Latin、词内直/左右弯撇号和连字符、仅目标侧占位骨架；完整匹配/重复/冠词，不查meanings/syn、不调用LLM。unsupported_target与no_match区分，不伪报成功或发音质量。
- `api/ingress.go`保留原文给控制/跟读，只有quiz使用原sanitize，避免删标签/截断后造出合法指令。反馈前缀及完整正文合同独立保持；quiz评分与两次机会政策未改。
- `api/hub.go`跟读在Room锁内完成，无网络调用/锁外迟到结果；入口仍核请求取消、当前连接、epoch/turn及event去重。移除跟读判前pending/snapshot不改变原成功EvRepeatDone→broadcast/持久化链。暂停/退休/旧上下文不得成功推进。
- `voicecmd`控制NFC/小写/空白折叠后整句匹配，最多单末尾句号或感叹号；问号/连续混合标点/引用/否定/附加文本保留。撤前缀、距离和语气词猜删；明确兼容变化，phase权限保持。
- repeat_evaluated固定purpose/source/result/ID/耗时，同lesson trace；repeat_verified改word_id，不含目标词或学生正文。未命中控制在quiz仍普通答题，不能宣称保机会；本片不新增澄清。

## 发现与闭合

1. 首版复用ASCII词法会使真实café失能，宽松撇号还可吞首尾引用。后续独立NFC Latin词法支持café/NFD、don't及词内连字符；词内左弯撇号遗漏经真实seed `seek one‘s fortune`、`help sb to one‘s feet`补映射/正例，首尾引用负例保持。复杂括号/斜线/OCR目标不猜清洗；设计侧5568条中97条不支持口径及限制见38，不能称全词库精准跟读。
2. 原入口sanitize先删标签/截前200字节，会将带附加内容的转写变成目标或命令。已改raw/control/repeat与legacy quiz sanitize分离；真实ingress标签/超长红例及修复由后端记录，浏览器另覆盖cat附加标签、skip附加标签。
3. 首版repeat_verified仍记word原文，现改ID；当前固定摘要符合日志合同。
4. docs01 FR-L1、D21后继说明、02§12.1/12.3及03 JD28旧口径已直接修正，不仅追加结尾。历史L1验收注明当时范围；现跟读RepeatMatch，quiz保持。邻接SDD旧假tts_finished/不录音上传叙述同步为既有真实ended及本机WAV/文本两路径事实。

两轮发现均已在当前保存点闭合，**当前批准范围代码独审0项新增阻断**。规则文本吻合不证明真人来源/回声抑制/发音质量；Picer阈值、文本长度过滤与8秒去重未移植。36统一提交、37迁移、真人声学和远端模型模式不在本片实施范围。

## 验收边界

RP01/02实际测试设计核查通过：真实HTTP转写→WS/UI，故意固定L2正确用于证明零调用；日志明确结果而非空集合通过，B状态前置防自然超时假绿。RP03设计通过：真实导入Unicode/撇号/unsupported，先核词面及顺序，逐段原门，显式skip恢复，另一学生不入课验证隔离。不冒声学ASR证据。

以上为专项保存点的历史检查；当时尚未完成完整验收，不以r3定向代替全量。

### 最终证据收尾（2026-10-16）

本片完整验收已回收并独立核对：[最终127汇总](simple-confirmation-browser-final127-20261015.json)为原10个完整族85例加同步日志合同后的完整recovery42例，接受族全部通过、0 skip/flaky，Node74、Python runner6、lint0。首轮125通过/2失败保留，失败原因是JD28仍要求跟读产生旧judge_decision日志；修订仅锁定新repeat_evaluated与quiz两条L2日志，生产及两个binary保持一致，不拼单例冒充整族。RP03专项及红绿范围见[浏览器复核记录](repeat-contract-browser-20261015.md)。

后端r13 coverage/vet与race同树均通过：516顶层、1184含子用例PASS，7显式opt-in SKIP、22测试包；覆盖率全部达门。原始汇总为`server/.test-artifacts/final-runtime-coverage-r13-summary.json`及`final-runtime-race-r13-summary.json`，可提交指标与源SHA见[docs38后端r13补记](../docs/38-简单语音确认与非思考适配设计增补.md)。当前批准范围独审新增阻断0。本片验收不关闭新库启动/COMMIT长尾、复杂词条spoken_target、真人声学与模型质量门，也不代表docs36整链已经实现。

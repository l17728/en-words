# 全双工 G1 基础组件独立架构、安全与 Agent 复核

状态：仅基础组件与本地设备预检准入；不是 Room 整链或真实自动教学验收。AGENTS、既有教学策略和生产模型路由未改。

## 设计与实际代码

- 32/33/04/35 已统一：NFC、小写、空白折叠、仅单个终末中英文句号；问叹号、省略号及连续句号不能删除。持久终态唯一键为 lesson+epoch+input_revision，utterance 不能绕过同机会唯一性。
- `duplex/session.go` 实际复核：精确控制字段、null 拒绝、UTF8/长度、单句限时和确认共享预算；发现的撤销后旧输出租约复活、final 未校验和句尾语义问题均有后端红例并修复。逻辑提交阶段仍由后端补充最后状态合同，最终证据以其定向及全量报告为准。
- `store/duplex_repository.go`：独立迁移，固定物理连接事务；receipt 查重先于新 CAS，owner 校验、逻辑机会唯一键、状态/receipt/outbox 原子提交。当前 trusted callback 是内部接线缝，不是模型任意写入口；未接 Room，不能宣称课堂 exactly-once 已交付。
- `capture.ts`：授权失效后的资源清理与 UI 通知分离，异步初始化失败释放同代资源，旧代不能停止新代；worklet 单在途80ms批、源采样Float32待消费缓冲≤2秒，溢出停止且可见。该队列不是网络PCM字节预算，不上传音频；能量仪表不代表已验证声学VAD/AEC。
- `duplex_preflight.go`及main/router：学生ownLesson权限、默认关闭，仅 ENWORDS_DUPLEX_PILOT=1显示预检；始终native_duplex=false、automatic_teaching=false。docs00/03明确基础测试不能替代真实课堂与物理声学验收。

## remoteprovider 严格适配器

独立测试覆盖 origin/HTTPS或显式字面loopback、key/model边界、禁止redirect和泄密、Models格式、finish/role/toolcall、深度/重复键/大小写别名/null/未知字段、失败不部分改写目标、头和正文deadline、取消及1MiB上限。发现 null 被Go静默解码零值的真实红已修。required字段及业务语义仍由调用者验证，不宣称完整JSON Schema执行。

最终定向 `server/.test-artifacts/remoteprovider-final-r2.txt`：通过，3.277秒，语句覆盖率96.7%。日志使用 response_received 表示收到HTTP体；validation_failed记录固定reason/endpoint并保留trace，不含key、提示或响应正文。日志测试按每条JSON event断言唯一事件；最初字符串计数把msg与event重复计入的夹具错误保留于final.txt。

## 真实远端窄范围探针

用户授权仅现有HTTP公开合成样本；通过已知主机SSH读取连接信息，密钥只内存/远端stdin。未传真人音频、未改远端服务、未改变生产HTTPS要求。

- `remote-gateway-public-text-probe-20261015.json`：模型列表200，3模型含qwen-omni与minicpm-o；各一次64token文本仅作为初探，不评价质量。
- `remote-gateway-public-text-structured-20261015.json`：同公开JSON任务，1024tokens、response_format=json_object、8秒绝对deadline。qwen 1117ms/stop但markdown包裹；minicpm 2838ms/stop但包含think标记；均不满足当前严格裸JSON合同。
- 2026-10-15早期文档调查：连接说明无input_audio/audio_url/realtime示例关键词；当时音频schema尚未验证，这是说明缺失而非服务不支持。当天后继公开合成探针已证实普通非流式Chat的input_audio WAV被接受，见下段；流式音频、原生双工、真人声学质量和真实教学自动提交仍未验证。
- 后续探针工具已要求显式 `--allow-public-http-synthetic`，SSH连接/认证/banner各15秒，禁SSH agent及HTTP环境代理。该入口加固发生于上述两次报告后，没有为加固重复请求。

以上结论不把文本HTTP可达或基础组件绿替代 G2/G3 课堂集成、声学与质量门。

2026-10-15音频后继证据：首批4例×2别名=8请求，扩集14例×2=28请求，两组重叠，不合并为22个独立样本。扩集全200/stop且8秒内，qwen中文9/12、英文1/1、静音0/1；minicpm严格完整格式0/14，包含think/空输出等，不能解释成听力全错。新边界组12标签×2=24计划请求，猫源本机合成超时使2请求未尝试，22实际请求全200/stop；qwen6/12、minicpm1/12，静音/非语音脉冲两者均0/2。新语音源无人试听，不能归因所有错误为远端识别，也不能替代真人门。报告见remote-synthetic-audio-review-20261015.md、remote-synthetic-audio-boundary-review-20261015.md；所有gold/hash先于识别冻结，提示未含答案。

末版补充：BeginCommit在verified进入committing前复核共享8秒截止；DB结果未知保持committing，只有确认rollback才CommitFailed终止。Cancel不伪造未提交，且永久撤输出；实际独审无新增阻断。远端文档索引见remote-gateway-contract-index-20261015.json：只有架构草案和连接说明，草案事件名不是已实现音频协议，停止盲试路径。ChatJSON缺字段语义另由专门测试锁定并在33明确，新增测试随最终全量验收。

## 2026-10-15验收工具实际复核补充

`run_browser_acceptance.py`冻结recovery发现的42个specID+projectID，再按21/21、单worker完整执行；逐组零skip/flaky及通过数，最终精确ID并集、每ID仅一次passed/retry0，组间源和两binary SHA不变。六条工具测试覆盖集合缺失/重复/意外、非通过/重试与超时清理调用顺序。900秒runner超时只终止其自建进程树，再15秒等待，仍按失败处理；mock单测不等于真实Windows所有子孙回收实证。

实际G09保存点为enter响应原30秒门→首play_tts→原媒体失败门，保留零成功ACK；JD28逐四段真实completed后再等repeating，未减少次数/间隔或改判分断言。REC04仍有导航后直接等B重复的同类取点，建议按各自enter及B四段completed分别同步，保留A断网/B继续/admin与双学生隔离。这些是测试阶段归属修正，不是数据库性能修复。最新r4第一组19/21，冷启动30秒和REC04等待失败留痕，124全族尚未通过；22.594秒课堂COMMIT长尾仍为明确限制。

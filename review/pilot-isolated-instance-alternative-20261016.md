# 独立实例课堂试点：与共享库首片的最小替代比较

状态：供主审选择的设计替代，未修改docs36已批准合同，未实施、运行测试或远端请求。

## 判断

用户需要可真实使用的隔离课堂试点，并不必然要求试点与全部历史课堂共用同一SQLite。独立App进程、独立SQLite、独立用户与认证信任域、独立端口，可以直接切断历史孤儿runtime、scope迁移、旧多阶段摸底与取消选库的耦合。相比继续扩共享scope删除generation协议，更适合先验证统一提交课堂闭环。

但“独立数据库+照旧启动全套NewApp.Router”不是足够隔离。旧REST/WS、Timer、Overdue、工具注册表或默认数据库路径仍可能写试点数据或误开旧库。必须把它作为受限制的产品部署模式，测试harness只是验证手段。

## 最小实施范围（待主审冻结）

1. 新独立composition入口（拟`cmd/pilot-server`或明确独立启动函数），显式pilot DB路径和端口，无默认回落legacy路径。先核DB身份：新库同事务创建完整pilot schema及固定实例marker；已存在legacy库/混合schema/marker不一致拒绝。路径别名、链接和误配同文件需要实际文件身份验证；不得自动AutoMigrate任意指定旧库。
2. 独立认证用户/签名信任域与audience，pilot账号不自动映射旧学生。至少证明旧JWT、旧ticket、跨实例lessonID均拒绝。前端独立origin或独立session namespace，明确“试点账号/成绩独立”，不能用同token造成假单点登录。密钥继续外置不记录。
3. 专用allowlist Router：仅认证、受控试点课列表/入课、输入提交/状态同步/receipt查询、媒体outbox ACK与退出。未实现的路径默认拒绝；不挂legacy enter/event/voice/resume/选库/摸底/反馈写/后台配置导入路由，不用隐藏按钮冒充限制。专用工具注册表只给已实现tx-bound工具，不调用旧callAgentTool循环旁路。
4. 不启动legacy Room恢复、RunTimers/RunOverdue、AutoSeed写逻辑。采用pilot专用协调器/后台job worker；保存created/active身份、auth generation、逻辑机会claim、计算占位fence、receipt/checkpoint/outbox与冻结结算job合同。取消、重连、崩溃后的终态一致性仍必须做，并未因物理隔离省略。
5. 内容用独立导入的冻结词序/释义/config快照，源文件/版本/hash可审；不共享可写卡表。初始仅受信创建服务生成计划，后续试点UI调用已鉴权服务，不能让用户上传任意checkpoint/成绩。成绩独立保存/展示，不同步到legacy卡/课/配置；跨库导出或映射另立设计。
6. 先mock完成typed真实浏览器整课、暂停/重连/进程恢复、重复提交和outbox等课堂逻辑；再按已证明音频协议接入公开合成试验。远端格式、静音、非思考与声学质量未达门时不启真实语音自动教学，文字回退仍能实际使用。不能把当前普通WAV请求成功等同流式双通路已可用。

首片可暂不提供逐词库删除、既有课模式转换、跨实例成绩合并与自助配置变更。退出登录/断连接不删除持久数据。若需清理整实例，由显式运维流程在进程停止后备份/删除专属目录，不能在产品请求中递归删除可配置路径。

## 可复用与可删除的依赖

可复用：docs36可信计划、created codec、版本/代次边界、严格schema检查、统一提交、逻辑输入与媒体lease分离、原子连接清理、固定日志、RepeatMatch/现quiz评分以及已批准教师机会策略。G1设备能力预检与纯组件复用不等于自动教学准入。

可从这一替代首片移除：legacy runtime桥接迁移、全库历史归属扫描、所有旧scheduler写入口的scope预留、旧DeleteStudentBook跨实例generation屏障、非零Save兼容改造。旧产品已确认的ladder/删除缺口仍应独立修复，不能因选独立试点而抹掉台账。

## 必须实际验证的隔离门

- 用真实legacy DB副本启动pilot必须拒绝且逐表/hash零改变；两个路径指向同文件也不能误准入。
- pilot正常操作时监测真实legacy DB零写，旧服务身份/监听不变；反向旧客户端无法调用pilot教学写。
- 枚举旧REST/WS/管理写端点全部不可达或固定拒绝，不能只抽查首页。身份越权、旧token/ticket、跨实例课次ID都拒绝。
- pilot进程重启仍恢复本实例receipt/outbox/job，不从legacy snapshot借状态；迟到回调/旧连接不能复活发声或二次教学。
- 两个学生真实UI路径并行，整课决策/机会统计保持既有Teacher合同；mock与真实模型门分账。

该路线能缩短产品闭环路径，但仍需新的composition/专用UI/schema身份和验收；不是“复制测试数据库即上线”。若主审采纳，应在docs36明确替代分支及冻结范围，再编码，避免同时维护两个首片方案。

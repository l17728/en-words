# 隔离 Pilot 与冻结语音基础代码安全终审

2026-10-16，独立安全域只读复核。结论：**本次限定范围新增安全阻断 0**。这不是全产品安全认证，也不是批准启动真实课堂试点。本轮没有运行测试、模型请求、迁移或网络探测，没有修改生产源码。

## 复核范围和证据

冻结代码重点逐函数核对：`server/internal/store/startup.go` 的 `OpenContext`、`initializeSchema`，`server/cmd/server/startup.go` 的 `ownedRuntimeCleanup.Close`，`server/internal/remoteprovider/client.go` 的 `New`、`request`、`completion`、`ChatJSON`，`server/internal/api/duplex_preflight.go`，`server/internal/judge/repeat.go`，以及 `web/src/audio/duplex/` 的权限代次、释放和能力声明。参照 `frozen-startup-repeat-g1-architecture-20261016.md`，未以其中历史测试状态替代当前浏览器结果。

隔离组件核对实际 `.test-tools/pilot-component-work/server/internal/store/pilot_*.go` 中的创建身份、计划、重放、schema、verifier、created 审计及关联测试。精确 16 文件及变换哈希以 `pilot-component-isolated-targeted-20261016.json` 为准；已有执行记录是 31 顶层／216 含子项通过，不是本审阅者再次执行所得。

## 已核实的安全边界

- `CreateIsolatedPilotLesson` 在取得 SQLite writer 后，首次创建和 nonce 重放均查询当前学生角色及 active 状态，再比对 owner、book、计划指纹。重放校验原课程、永久 scope、created checkpoint 与冻结内容，不以当前词库或配置重新解释历史成功。COMMIT 失败返回零 receipt，observer 不发布成功。
- `PilotCreationPlan`、principal 字段不可由普通 JSON 请求直接赋值。**`BindPilotSystemPrincipal` 本身不是授权检查**：其调用者必须完成真实 task/scope 校验。目前构造和迁移均为包私有，没有 App、HTTP、Agent 接线，因此不能把该未来适配器前置条件误报为已存在的公开越权入口，也不能声称该授权边界已实装。
- `pilotSchemaVerifier` 绑定具体外层 pool；只有已结束的成功审计事务发布 schema cookie 缓存。未知 schema 在当前事务重新检查，不发布未提交证明。当前 marker 和 scope 不使用成功授权缓存；SAVEPOINT 回滚及完成事务不能沿用预留结果。大小写对象检查与 SQLite 名称语义对应。
- 私有显式迁移先取得 writer、审计旧事实，再原子建立对象和 marker。普通未迁移启动不新增孤儿 runtime 全表拒绝；ready 状态仍严格审计。created-only 审计拒绝尚未实现的活动状态及非空未来事实，不推测恢复教学状态。
- 新增日志观察对象采用固定事件、原因和数值摘要，不携带 SQL、原始错误、音频、密钥、epoch 或正文。原始数据库错误仍可能向可信调用方返回，未来 API 适配器必须保留现有错误清洗策略，不能直接输出给客户端。
- 远端 client 禁止重定向、环境代理、URL 用户信息和非受控 HTTP；HTTP 例外只允许显式启用的字面 loopback，**不证明后续跳转加密**。请求有总时限及响应体上限；结构化响应拒绝重复键、null、未知字段、非完整 completion，并在验证后才写 target。字段必填与业务语义仍由业务层承担。
- preflight 需要学生身份与课程所有权，始终声明 `native_duplex=false`、`automatic_teaching=false`。设备约束只报告请求、支持和实际设置，不等价于声学 AEC/VAD 验收。跟读文本匹配明确不是发音质量或说话人来源认证。
- startup 失败清理自有连接池；资源关闭先停止监听及后台工作，后台超时不并发关闭仍可能使用的池。该保证不延伸为所有 hijacked WebSocket 的优雅关闭证明。

## 保留的验收限制

隔离组件尚未进入正式 `server/`，旧 writer、Room、Teacher、outbox、统一提交与取消选库生命周期尚未接通；不能对混合运行的生产库启用迁移。实际接线后需重新进行授权、全量回归、覆盖率及并发验收，不能复用旧 r15 哈希结论。

本次不验证真实麦克风噪声误触发、远端模型质量、实时音频取消或尾延迟。公开合成样本 HTTP 临时验证的授权不包含真人音频。浏览器恢复族最终结果仍由对应完整清单验收；既有失败记录不可被本次零新增安全发现覆盖。

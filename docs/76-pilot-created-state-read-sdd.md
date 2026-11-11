# Pilot 已创建课次只读状态：隔离切片 SDD

> 评审补充：`PilotSystemPrincipal` 的构造不是权限证明，调用者须先完成受信任务鉴权，禁止从 HTTP 参数直接绑定。读取和校验必须在同一个仅执行 SELECT 的 SQLite 一致性事务中完成；先检查 `verifier.inspectCurrentTx` 为 Ready。架构与安全评审均同意该只读边界。

## 范围

本切片仅为隔离 Pilot v2 课次提供可信的恢复起点。受信系统调用 `PilotStore.LoadCreatedState`，读取已提交的 `created` 状态元数据；不开放 HTTP/WS，不启动 Room，不判分、不生成音频或工具效果。它不是全双工课堂验收。

## 读取合同

输入为服务端绑定的 `PilotSystemPrincipal` 和正数 `lessonID`。同一 SQLite 只读事务内检查当前学生仍为 active、课次归属、`pilot` 模式和 schema v2，解码冻结创建计划，并复用创建期严格校验验证 scope、lesson、checkpoint、词条与配置指纹。只返回 `lesson_id/student_id/book_id/schema_version/lifecycle/phase/checkpoint_version`；不返回课程正文、配置、令牌或数据库错误文本给产品 UI。缺课、错 owner 和失效账号统一拒绝；损坏或不支持的生命周期固定失败。只读调用不预留 scope，不更新 `first_entered_at` 或 checkpoint。

## 验收与后续门

单测覆盖正常读取、重启后读取、错 owner、失效账号、篡改 checkpoint、非 Pilot 模式及零 DML。此切片无浏览器入口，不计作 E2E。下一步必须先实现 `created → active` 的受信 `lesson.start`、active 状态编解码与启动审计，再做 `answer.text` 的持久命令占位、机会 claim、checkpoint、receipt/outbox 同事务提交；必须通过双连接争用、故障回滚和浏览器旅程后才能称作文字课堂闭环。原生语音仍受模型音频协议和真实课堂试点门约束。

本轮架构、安全、测试复核确认只读范围；定向 `TestPilotCreatedStateRead*` 和全量 Go 回归通过。下一里程碑必须先在 SDD 固定内部 `lesson.start` 命令身份、首次 `show_word` 效果、重放和失败恢复合同，再扩展 active 初始状态 codec 与混合课次启动审计。`created` 状态本身不表示已开始教学。

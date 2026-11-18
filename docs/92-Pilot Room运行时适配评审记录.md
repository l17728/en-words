# Pilot Room 运行时适配评审记录

## 评审结论

本设计通过实现前审查，允许按阶段开发。关键约束是 Pilot 与 legacy Room 双路径隔离，所有教学决策提交必须先持久化 receipt/checkpoint/outbox，再发布 UI 事件。

## 已识别风险

- 不能使用 `runtime_records` 作为 Pilot checkpoint。
- 不能让客户端提交 lesson ID、revision 或 decision 指纹。
- 不能在事务提交前发送 `start_tts`、判分或结算成功消息。
- 重复 command 必须返回同一 receipt，不能重新计算。

## 阶段门

先完成 store transition API 和回滚测试，再接 Hub；Hub 接线通过后才创建双进程 Playwright；性能矩阵最后运行。任何一阶段失败都不宣称 Pilot classroom ready。

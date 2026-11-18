# Pilot Room 运行时适配 SDD

## 目标

将已创建的 Pilot lesson 接入课堂入口、状态迁移、事件提交和结算，同时保留 legacy Room 路径不变。Pilot 运行时必须使用 `classroom_pilot_states` 作为状态事实源，不能写入 `runtime_records` 冒充 checkpoint。

## 适配边界

`Hub.EnterContext` 先读取 lesson mode。`legacy` 继续进入现有 `Room`；`pilot` 进入 `PilotRoom`。两条路径不能共享可变状态对象，也不能互相回退。

## 事务合同

每个用户输入带有 `command_id`、`logical_input_revision`、`question_key` 和请求指纹。写入顺序固定为：

1. 在同一 SQLite writer transaction 内校验 owner、mode、revision、deadline 和 active command。
2. 创建或重放 `classroom_active_commands`，重复 command 返回原 receipt。
3. 计算确定性 decision，写入 `classroom_transition_receipts`。
4. 写入下一版 `classroom_pilot_states` 与 effect outbox。
5. 事务提交后才发布 Room 消息；提交失败不得发布成功结果。

## 恢复与结算

重启时先校验 checkpoint、receipt、active command 和 outbox 的一致性，再恢复 Room。最后一题只创建一个 completion job；worker 依据 decision 指纹幂等结算。未知提交结果显示“待同步”，禁止自动重答。

## 验收门

- 单测：revision、重复 command、旧 epoch、事务回滚、outbox 重投、completion 幂等。
- 集成：两个独立 SQLite pool 并发进入同一 lesson，只有一个有效 owner。
- E2E：双进程登录、进入、回答、重启恢复、结算。
- 性能：冷启动、热路径、首音、打断、提交 p95/p99。

真人声学与现场课堂仍属于独立人工验收，不纳入本 SDD 的自动通过条件。

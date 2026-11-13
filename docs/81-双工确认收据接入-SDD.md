# 双工确认收据接入 SDD

## 目标

把 `voice_confirm` 的一次教学提交与 SQLite 收据绑定，支持重复确认返回原收据、进程重启后查询结果，并保证确认前不修改课堂状态。

## 事务边界

1. WS 层完成身份、revision、文本哈希和课堂 generation 校验。
2. 先在课堂协调器内串行化同一 `(student_id, lesson_id)` 的确认请求。
3. 使用 `DuplexRepo` 的 `committing` 记录占位，提交请求只允许一个执行者进入。
4. 判分和课堂状态写入必须使用同一个 SQLite 事务；禁止在 `DuplexRepo.Commit` 回调中调用网络或再次进入 Room。
5. 事务成功后返回 `commit_receipt`；重复请求只查询并返回原收据，不重复判分或推进。

## 失败与恢复

- 哈希、generation、owner 或 revision 不匹配：返回错误，不写收据。
- 判分失败：回滚事务，允许同一 utterance 重试。
- 事务结果未知：保留 `committing`，重连先查询，不创建新 utterance。
- 进程重启：从 checkpoint/receipt 恢复，未决请求降级为文字输入。

## 验收

- Go：重复确认、并发确认、重启恢复、未知结果和跨学生隔离。
- Playwright：确认前无 judge/progress，重复确认只出现一次结果，刷新后返回原收据。

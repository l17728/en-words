# 首音课堂审计行 Guard 实现评审

日期：2026-10-28
范围：`docs/80-first-audio-audit-row-guard-sdd.md` 定义的测试专用 SQLite 审计写入切片。

## 范围边界

本实现只在 `_test.go` 中使用 mattn SQLite、GORM、authorizer 和固定触发器证明三种成功审计写入的行契约。它没有开放生产 `api.App`、发布 `JOURNEY`、启动监听器或完成双进程课堂旅程。真实 App、Playwright、性能矩阵和真实课堂仍按 `docs/74-first-audio-matrix-isolated-backend-sdd.md` 与 `docs/77-first-audio-write-callsite-inventory-sdd.md` 单独验收。

## 评审发现与处理

首轮架构、安全、后端/测试性审查发现回滚 tuple 缺失、permit 重用风险、预期行未按固定契约预检、authorizer manifest 不做精确集合比较，以及 UPDATE/DELETE、重复 ID、REPLACE、序列边界和失败后持久化状态缺少执行回归。

修订后，authorizer manifest 加入 `ROLLBACK/AUDIT_CLEANUP` 并与运行时捕获集合精确比较；permit 按事务 epoch 消费一次，事务 Begin 成功后才启用写操作，清理失败会 retire 连接并关闭池。预期行在 SQL 前校验，行触发器仍逐字段比较 SQLite storage class 和原始 TEXT。新增了三个允许工具的正例、每字段变异、BLOB 类型、wrong-row replay、ID 序列/错配、UPDATE/DELETE/duplicate/REPLACE 快照、ID 查询/COMMIT 失败回滚、epoch wrap 和部分 schema extension 拒绝用例。

一轮代码复审又发现 commit 错误后的 defer 过度依赖 `tx.Error`，以及 UTC/5 秒时长限制超出 Registry 事实。现已改为依据活动事务尝试回滚；不确定时 retire 连接。时间按瞬间比较冻结窗口并保留 SQLite 时间字节，允许本地时区偏移；DurationMs 遵循 Registry 的 `>=1ms` 合同，不设人为上限。二轮架构、安全、后端/测试性复审均通过。

## 验证证据

- `go test ./internal/api -run '^TestFirstAudioJourney' -count=1 -timeout=90s`：通过，约 21 秒。
- `go test ./internal/api -run '^TestFirstAudio' -count=1 -timeout=3m`：通过，约 57 秒。
- 本机 `go test -race ./internal/api -run '^TestFirstAudioJourneyAuditGuard' -count=1 -timeout=3m`：Windows 上等待超过 5 分钟仍无结果，手动停止；不记为通过。
- 本机 `go vet ./internal/api`：未能在本机干净完成；不记为通过。
- GitHub Actions（提交 `b58594e30f9ef78ac8e00f73c76e6a5e5a76e423`，运行 [40666344798](https://github.com/l17728/en-words/actions/runs/40666344798)）：`protocol` 与 `windows-boundaries` 均成功。`protocol` 在 Linux 上完成定向 journey 测试、审计 Guard race 测试和 `go vet ./internal/api`；Windows job 完成定向边界测试。

评审结论仅覆盖该 SQLite 行 Guard 切片。它不构成 guarded `api.App`、双进程 Playwright、跨复位竞态、课堂性能或真人试点验收。

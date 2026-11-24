# Atomic batch repository tests：后端复审 8

评审对象：最新版 `server/internal/store/atomic_batch_repos_test.go`，对照 `import_atomic.go`、`placement_atomic.go`、`store.go` 与 `wordbook.PrepareImport`。只读；未运行测试或修改文件。

## 结论

**PASS：review7 的编译阻断和主要断言缺口均已修复。** `ListEntries` 现在正确接收三项返回值；词面和规范化 key 分开核对。批量 nil 卡片用例确认前置合法新卡不会部分落库；取消 placement 后检查原状态及 draft 均保持不变。当前 fixture 与 Repo/API 语义一致。

## 复核结果

| 用例 | 状态 | 依据 |
|---|---|---|
| 原子导入顺序、跳重和非法项 | PASS | `atomic_batch_repos_test.go:20-39` 接收 `entries, _, err`；按插入顺序核对 `Word` 为 `apple`、`blue-green`，并从 DB 检查 `norm_key` 为 `apple`、`bluegreen`。输入中的大小写重复和空词按 `PrepareImport` 规则依序出现在 Skipped；不存在词书即使输入为空也返回 `ErrBookNotFound`。 |
| 导入取消 | PASS | `:42-57` 使用已取消 context，断言返回 `context.Canceled` 且词条数为 0。此用例覆盖取消前置检查，不覆盖事务执行中取消。 |
| 卡片幂等及整批拒绝 | PASS | `:60-78` 重复创建同一复合键后总数为 1；单 nil 拒绝；随后提交“新卡 + nil”仍拒绝且总数未增加，锁定实现先校验整批再 Create 的事务语义。nil slice 是空操作。 |
| 摸底状态范围和 Draft | PASS | `:81-111` 覆盖目标学生/词库中选中 NEW→MASTERED、未选 MASTERED→NEW，以及其他学生不变；变更数为 2，Draft 限定在目标 student/book 并保存 `[10]`。取消调用返回 `context.Canceled` 后再次读取，状态和 Draft 都与提交前一致。 |

本轮无新增实现或断言阻断。上述均为静态核对结论，未执行测试。

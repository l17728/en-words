# Atomic batch repository tests：后端复审 7

评审对象：`server/internal/store/atomic_batch_repos_test.go`，对照实现 `import_atomic.go`、`placement_atomic.go` 及当前 Repo/API 语义。只读；未运行测试或修改文件。

## 结论

**BLOCK / P1：新测试文件无法编译。** `WordbookRepo.ListEntries` 返回三项 `([]*wordbook.Entry, int64, error)`，但测试只接收两个值。Go 会报 assignment mismatch，因此该新增覆盖门不能通过编译。

## 定向发现

| 项目 | 状态 | 依据与建议 |
|---|---|---|
| ImportEntriesAtomic 顺序、归一化去重、invalid/duplicate 结果 | BLOCK / P1 | `atomic_batch_repos_test.go:29` 写作 `entries, err := repo.ListEntries(...)`；真实签名在 `store.go:309` 有三项返回。改成 `entries, _, err := ...`（或接收 total 并断言为 2）后，`apple`/`Apple` 规范化重复、空词跳过、`blue-green` 得到 `bluegreen` 的断言与 `PrepareImport` 语义一致。 |
| ImportEntriesAtomic 不存在词书及取消回滚 | PASS（取消用例范围有限） | 实现先在事务中更新目标词书行以取得 writer；缺行返回 `ErrBookNotFound`。取消测试使用调用前已取消的 context，能锁定无写入；建议额外断言返回结果为 nil。它没有覆盖事务中途取消，但实现通过 `atomicConnection` 在提交前检查 context。 |
| CreateCardsIfAbsent 幂等与 nil 输入 | PASS（原子性断言可加强） | 唯一键为 `(student_id, book_id, word_id)` 且 `ON CONFLICT DO NOTHING`，重复调用后计数为 1 合理；nil slice 是空操作，单 nil item 会事务报错。当前 nil 测试只传 `[nil]`，未验证 `[valid-new-card, nil]` 不会留下前置行；建议补该批次回滚断言以锁定“atomic”。 |
| MarkPlacementAtomic 的范围和 draft | PASS | fixture 按 `student_id, book_id` 区分目标/非目标：选中目标 student 的 NEW 卡转 MASTERED，未选中的 MASTERED 转 NEW，别的 student 不变，返回改动数 2；draft 对应 `(student_id,book_id)` 并保存 `[10]`，符合实现的 scoped 更新和 upsert 语义。 |

## 修订建议

先修正 `ListEntries` 的接收值，再运行该包测试。随后可将 `CreateCardsIfAbsent` 的 nil-case 改成“先合法新卡、后 nil 卡”，并断言行数不变，以实际验证批量事务不会部分提交。当前 `MarkPlacementAtomic` 的取消断言可补上 draft/status 不变检查，但其预先取消 fixture 当前应在 `Conn(ctx)` 阶段直接失败。

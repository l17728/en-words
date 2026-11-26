# lesson.start 持久迁移内核 SDD：后端复审 19

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 第 19 轮。范围为 case 生命周期锁覆盖区间、cleanup 锁持有时长、case/catalog 锁配合以及 cleanup-wins/opener-wins 两种有界交错。只读检查；未修改产品代码/设计文档，未运行测试。

## 结论

**PASS（附实现验证门）；review17 的旧快照 P1 已关闭。** case 生命周期锁现从第二阶段 catalog 授权一直持有到整次操作完成、SQLite 连接及 DB/sidecar/key 句柄关闭。cleanup 期间同一锁覆盖 quarantine 记录与 Sync、全部文件/目录删除、父目录 Sync、永久 tombstone 与 Sync。catalog 锁仍只覆盖短暂的验链、generation 分配、单记录 append+Sync，不会包住 DB/删除或等待 case 锁，因此锁序无环且不同 case 可并发。

新增的两种有界竞争把互斥关系说清楚：cleanup 先赢时，旧快照 opener 获锁后二次检查拒绝、DB open=0；opener 先赢时，cleanup 必须等待 opener 关闭连接和句柄，之后才能 quarantine/删除/tombstone，后续 opener 再被拒绝。未发现新的设计级阻断。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| 生命周期锁持有范围 | PASS | §4 明确 case OS lock 从第二阶段授权贯穿操作完成及 SQLite/DB/sidecar/key 所有句柄关闭；不能在授权或 OpenVerified 后提前释放。Start/receipt/restart 的状态快照在锁释放后不得继续使用。 |
| cleanup 持锁与 catalog 临界区 | PASS | cleanup 持 case 锁完成 quarantine+Sync、文件/目录删除、父目录 Sync、tombstone+Sync；失败按已持久状态 fail closed 后再释放 case 锁。catalog 锁只包记录链读取、generation 分配、append+Sync，明确不包文件删除或 DB 工作。 |
| cleanup-wins 交错 | PASS | §93 中 cleanup 持 case 锁完成 quarantine/tombstone 并释放后，等待中的旧 opener 必须持锁重读 catalog，拒绝且 DB open=0。 |
| opener-wins 交错 | PASS | opener 完成二次授权后在 OpenVerified/受保护句柄使用期间持 case 锁；cleanup 不得先写 quarantine 或删除，直到 opener 关闭全部资源并释放锁。随后 cleanup 才能完成完整删除及 tombstone；之后的新 opener 拒绝。用例有有界完成要求。 |
| Windows/POSIX 锁与 reader consistency | PASS WITH IMPLEMENTATION GATE | 持有 OS 生命周期锁跨越较长的 SQLite 操作和删除是合理的；同 case 序列化是目标语义，不同 case 仍并行。全局 catalog OS 锁为短临界区，可用稳定文件对象上的 Windows byte-range lock 与本机 POSIX flock/fcntl 实现。需实现时确认跨进程而非 mutex、进程退出释放、锁等待取消及实际文件系统语义。 |
| 锁顺序与旧快照 P1 | PASS | 先 catalog 乐观读取并释放，再取得 case 锁，最后重取 catalog 锁重新授权；catalog 锁持有者绝不等待 case 锁。二次检查在持 case 锁期间完成，锁一直保留至 DB open/使用/关闭或 cleanup 全流程完成，消除了“二次授权后 cleanup 抢先”的窗口。 |

## 实现验证门

1. 使用两个独立监督器 OS 进程测试 cleanup-wins 和 opener-wins；不得用进程内 mutex 替代文件锁。所有等待都设置界限并记录哪个进程/锁阶段持有者。
2. opener-wins 中断言 cleanup 在 opener 关闭所有 SQLite/辅助文件句柄前既不 append quarantine、也不删除文件；cleanup-wins 中断言 tombstone Sync 后 opener 的 DB open 计数为零。
3. 验证 case 锁在所有错误/取消/panic/COMMIT unknown 分支均通过 defer/RAII 正确释放，但释放前句柄已关闭；catalog append/Sync 失败时不越过规定的删除屏障。
4. 在目标 Windows/POSIX 本机文件系统上确认文件锁崩溃释放、共享 handle 和目录权限实际行为；测两个不同 case 的业务临界区确实可并行。

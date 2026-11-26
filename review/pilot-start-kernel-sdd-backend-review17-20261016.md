# lesson.start 持久迁移内核 SDD：后端复审 17

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 最新稿。范围为 catalog 跨进程锁、case→catalog 锁序、短临界区、不同 case 并发与重启验链、Windows/POSIX 锁及 Sync 失败。只读检查；未修改产品代码/设计文档，未运行测试。

## 结论

**BLOCK：1 项 P1。** 文件级跨进程锁在本机 Windows/POSIX 文件系统均可实现；catalog 快照、generation 分配、单条追加与 Flush/Sync 全程持锁，也能使不同 case 的 DB/sidecar 工作并行。恢复校验与 Sync 不确定时全局 fail closed 语义一致。仍缺少锁序要求下的启动/打开协议：由于规定 case→catalog，调用方不能持 catalog 锁等待 case 锁；必须先短暂读取 catalog、释放锁，再取 case 锁并重新持 catalog 锁核验 case 状态，然后才可打开 DB。当前文档要求“先查 catalog”但没明确这次二次核验，容易出现清理与启动竞争。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| OS 跨进程排他锁可行性 | PASS WITH IMPLEMENTATION GATE | §42 要求锁在已验证 catalog handle 上，避免锁文件 inode 被替换；Windows 可用 handle byte-range lock（如 LockFileEx），POSIX 本机文件系统可用 flock/fcntl。需冻结具体原语、锁区间、等待/取消行为与 handle 生命周期，并确认 catalog 位于支持所选锁语义的本机文件系统。 |
| case→catalog 锁序与清理竞争 | **BLOCK / P1** | 文档固定先 case lock、后 catalog lock，并禁止持 catalog 锁等待 case lock；但开 case 前又要先检查 catalog。安全的具体协议应为：短暂持 catalog 锁读取并释放；取得 case 生命周期锁；重新持 catalog 锁读取最新完整链并验证 case 仍允许打开；释放 catalog 锁后才 OpenVerified/打开 DB。缺第二次核验时，启动方可能在清理方已 quarantine/tombstone 后沿用第一次的旧快照。应将此序列及“无锁间隙内不得基于陈旧状态开 DB”写入合同。 |
| catalog 临界区与不同 case 并发 | PASS | catalog 锁仅覆盖链读取、generation 分配、单条 frame append 与 Sync，不覆盖 Planner/DB/删除；因此不同 case 的 SQLite 和 sidecar 工作可并发。catalog 全局快照受同一锁保护，不读半条 append。 |
| catalog重启验链与 Sync 失败 | PASS | 重启在排他锁下完整验链；generation 按当前有效最大值分配。frame 部分写、序号/格式错、Sync 不确定均全局 fail closed，不回退。cleanup quarantine 先完整 append+Sync 才删文件；零字节失败保留旧状态并允许重检后重试，部分尾/unknown 要人工处理。此选择避免错误地丢失隔离记录。 |
| reader consistency | PASS | 所有快照读取均持 catalog 锁，generation 与记录帧 append 同锁，读者不会观察半条写入；跨进程并发追加串行化。释放锁后使用的数据必须再次核验，如本轮 P1 所述。 |

## 必须补充的合同与实现验收门

1. 写明 case 打开路径的双阶段校验：catalog-only 乐观查验并释放 → case OS lock → catalog lock 下重新查验 → 释放 catalog 锁 → 每次 DB open 前执行 OpenVerified。所有清理、启动、receipt 与重启路径统一使用此顺序。
2. 使用两个监督器进程竞争同 case 的 cleanup/open，注入 quarantine/tombstone 与启动交错，断言旧 catalog 快照不能授权 DB open；同时让两个不同 case 并行走完 DB/sidecar，确认只在 catalog append 处串行。
3. 冻结 Windows/POSIX 锁原语及临界区；确认进程崩溃自动释放锁、等待/超时不能造成锁序反转，并在目标本机文件系统验证 Sync 语义。
4. 并发追加后重启验链；在 append 的零字节、部分 frame、Flush/Sync 前后注入崩溃。任何不确定尾部都必须按规格全局 fail closed，而不能猜测旧/新状态。

# lesson.start 持久迁移内核 SDD：后端复审 18

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 第 18 轮。只读复核 BE17-1 的 case/catalog 双阶段授权与竞争用例，并评估锁原语、catalog reader consistency 和 Sync 失败语义。未修改产品代码/设计文档，未运行测试。

## 结论

**PASS（附实现验证门）；BE17-1 已关闭。** §4 的双阶段协议现在明确：在 catalog 锁内乐观检查并释放 → 获取 case OS 生命周期锁 → 在 catalog 锁内重读最新完整链并二次授权 → 释放 catalog 锁后才 OpenVerified/打开 DB。Start、receipt、restart、cleanup 共用该序列，case 锁释放后不得继续使用旧快照。新增双监督器竞争用例会让 opener 持旧快照等待 cleanup 完成 quarantine/tombstone，再断言二次检查拒绝且 DB open=0，直接锁定此前缺口。

跨进程 OS 文件锁、短 catalog 临界区、不同 case 并发、重启全链验签/验帧以及 Sync 不确定时 fail closed 的合同彼此一致；未发现新设计阻断。OS 锁具体 API 与目标文件系统支持仍属于实现验收门。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| BE17-1：case/catalog 锁序和旧快照 | **PASS** | §4 给出完整两阶段协议并明确所有 Start/receipt/restart/cleanup 路径遵守；二次检查后才 OpenVerified，case 锁释放后禁止继续使用旧 snapshot。锁序仍是 case→catalog，catalog 锁持有者不等待 case 锁，无反向等待环。 |
| 同 case open 与 cleanup 竞争覆盖 | PASS | §93 两监督器用例安排 opener 在乐观读取后等待，cleanup 先持 case 锁写 quarantine/tombstone 并释放；opener 取锁后重读 catalog，必须拒绝且 DB open 计数为 0。该断言覆盖 stale-read 情形。 |
| Windows/POSIX 文件锁 | PASS WITH IMPLEMENTATION GATE | 以已验证 catalog handle 对应的 OS 排他锁可实现：Windows 可用 LockFileEx，POSIX 本机文件系统可用 flock/fcntl。须固定统一锁区间/获取释放/崩溃释放语义；case lock 与 catalog lock 也应使用同一进程间可见的 OS 锁而非仅 mutex。 |
| Reader consistency 与不同 case 并发 | PASS | 所有 catalog 快照都在全局锁下读取，追加/generation 分配/Sync 在同一锁内，避免半帧观察与 generation 重复。锁只保护小型 catalog 临界区，不包业务、DB 或文件删除；不同 case 的 DB/sidecar 工作保持并发。 |
| Sync 错误、崩溃和重启验链 | PASS | 完整 append+Sync 才确认状态迁移；当前进程对 Sync 错误 fail closed。重启完整读取最新 catalog：有效持久记录生效；部分尾、格式/序号错误或仍不确定则全局 fail closed。cleanup 先持久 quarantine，零字节失败不删并允许重查重试；部分尾需人工检查，不回退旧状态。 |

## 实现验证门

1. 固定 Windows/POSIX 的 OS 文件锁原语、锁定区间及获取超时/进程崩溃释放行为；验证目标为本机文件系统且锁基于稳定 inode/file ID。
2. 用两个真实监督器进程执行 §93 的 open/cleanup 竞争，不用进程内 mutex 模拟；确认 cleanup 的 tombstone/quarantine 已 Sync 后，stale opener 的 DB open 计数为 0。
3. 两个不同 case 并行执行 DB/sidecar 操作并竞争 catalog append；确认仅 generation/append 临界区串行，重启后链完整且 generation 唯一递增。
4. 注入零字节、部分 frame、Flush/Sync 错误及进程退出，验证重启后的权威状态与文档一致；不确定尾状态必须 fail closed。

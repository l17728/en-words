# lesson.start 持久迁移内核 SDD：后端复审 15

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 最新稿。范围为直接消费 OpenVerified handle、append-only catalog 与跨重启 WAL/SHM/journal。只读检查；未修改产品代码/设计文档，未运行测试。

## 结论

**BLOCK：1 项 P1。** SDD 已把 SQLite 主库身份绑定到已验证句柄，并将身份检查推进到页访问/SQL 之前；catalog 的固定 inode、append+Sync 和损坏时全局 fail closed 有一致的恢复语义；WAL/SHM/journal 也已定义“缺失时安全创建、存在时验证后安全重开”，review14 的语义冲突已关闭。但仓库锁定的 modernc v1.23.1/glebarez 驱动没有可直接注入已打开文件句柄的公开可写 VFS API。按当前依赖使用普通 DSN 无法满足新合同，必须把 fork/自维护 writable VFS 或替换驱动明确纳入设计。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| DB handle 在页访问/SQL 前身份校验 | PASS（合同）/ **BLOCK（现有驱动落地）** | §42 明确 pilot 专用 VFS 直接消费 OpenVerified handle，在任何页 I/O/SQL 前校验同一对象 file ID，禁止 path/DSN 重开；无法证明则 fail closed。该语义清楚，但当前 `server/go.mod` 通过 `github.com/glebarez/go-sqlite` 使用 `modernc.org/sqlite v1.23.1`。现代c公开 `sqlite/vfs` 只支持只读 `fs.FS`，不能承载写事务、WAL 或既有 OS handle。要实现需自维护/分叉并构建 writable VFS，或换用提供公开可写 VFS/handle 注入能力的驱动；SDD 当前没有冻结这项驱动变更、fork 来源/版本和 pilot-only 构建边界。 |
| Append-only catalog identity 与恢复 | PASS | 首次在受保护父目录 `CREATE_NEW/O_EXCL`，文件及父目录 Sync；进程持 verified handle/file ID，后续仅 append+Sync，不替换/截断/改名。重启由受保护目录 ACL、固定路径、单链接/non-reparse 重新建立锚点。每行有界、严格 schema、单调 generation 与 frame marker；截断、序号错误、Sync 不确定即全局 fail closed，不回退/丢弃隔离事实。该恢复策略保守且一致，无 HMAC 与当前可信账号威胁边界冲突。 |
| WAL/SHM/journal 跨重启 | PASS（合同）/ **同受 VFS 阻断** | §42 已规定缺失时相对 verified dir no-follow + CREATE_NEW，存在时先安全打开、核 owner/ACL/link/reparse/file ID 再交同一 VFS 恢复；明确禁止将已存在文件一概拒绝，也禁止路径裸重开。和跨 child 重启复用 case 的要求一致。没有可写、可注入句柄的实际 VFS 时仍无法落实。 |
| 新矛盾扫描 | 无额外发现 | 首条 SQL 改为页访问前 VFS 身份验证后，不再有 PRAGMA 自证的循环；append-only catalog 的永久 tombstone 与不复用 ID 一致。需要注意 append 中途崩溃会使 catalog 无法解析并全局 fail closed，这是规格明确选择的安全退化，不是恢复歧义。 |

## 阻断关闭条件与实施门

1. SDD 需冻结 pilot-only SQLite 实现路线：维护一个 writable modernc VFS fork/内部适配层（明确上游版本、补丁持有方、build tag 与升级方式），或替换为可公开实现 writable VFS 且可接收现有 OS handle 的 SQLite driver。当前只读 `fs.FS` VFS 不满足条件。
2. 该实现必须让主 DB、WAL/SHM/journal 的每个 open/read/write/lock/shared-memory/delete 回调均使用已验证文件/目录对象；禁止任何默认 OS VFS 或 DSN fallback。
3. 在 Windows/POSIX 验证对象身份校验早于首次页访问，及辅助文件缺失创建、既有安全 reopen、崩溃恢复、锁/SHM 和目录项替换。失败必须关闭 case。
4. append-only catalog 实现需覆盖每条迁移 append 前后崩溃、半帧/Sync 不确定、永久 tombstone 重启读取；出现任何不确定状态不得回滚到旧 generation。

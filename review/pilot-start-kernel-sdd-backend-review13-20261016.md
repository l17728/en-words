# lesson.start 持久迁移内核 SDD：后端复审 13

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 最新稿。范围为 BE12-1/2 修订、modernc SQLite VFS 跨平台合同与新阻断扫描。只读检查；未修改产品代码/设计文档，未运行测试。

## 结论

**PASS（设计层面）；实现仍需平台验证。** BE12-1 的 TOCTOU 缺口已由“OpenVerified 返回已验证 handle/file ID，pilot_e2e modernc VFS 直接消费、禁止 DSN/path 重开”明确关闭。BE12-2 的 catalog 访问和 quarantine 顺序也已闭环：先验证 catalog、创建/打开 case 前先查、清理先持久隔离、全局 fail closed、成功清理后保留永久 tombstone 且 ID 不复用。未发现新的设计级阻断。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| BE12-1：验证对象与 SQLite 实际对象一致 | **PASS** | §3 明确 OpenVerified 返回已验证 DB handle 和 file identity，由专用 modernc SQLite VFS 直接消费；禁止 path 重开及 DSN 重解析；连接复用固定 DB/VFS 对象，前后核对 file ID，路径项与 handle 不一致则隔离。§93 加入校验到连接之间替换目录项的故障注入，并断言只用原 handle 或拒绝。校验与使用的对象关系现在明确。 |
| VFS 跨包与构建隔离 | **PASS** | VFS/Store 实现限定在 `pilot_e2e` 测试专用依赖图，harness 单独装配；生产 `go list ./...` 与 server 不含该依赖/route。可将已打开文件对象封装为 VFS file，并让 SQLite 连接使用固定 VFS 名称；这是一项专用实现，不要求修改生产 Store。 |
| Windows/POSIX 文件与 WAL 边界 | **PASS WITH IMPLEMENTATION GATE** | 文档分别规定 Windows DACL/reparse 检查与 POSIX mode/umask/no-follow，并指定以同一目录 handle 安全创建 WAL/SHM/journal。合同方向可实现，但 VFS 必须覆盖 SQLite 文件锁、删除/访问回调、WAL shared-memory 等行为；仅把主库 `os.File` 交给 VFS 并不足够。实现时需证明这些回调均相对已验证目录句柄操作，并在 Windows 与 POSIX 做替换/并发/恢复验证。 |
| BE12-2：catalog、quarantine 与 tombstone | **PASS** | catalog 自身先做 owner/ACL/file-ID/schema/Sync 验证；catalog 异常即全局 fail closed。创建/打开 case 前先查随机且永不复用的 ID。cleanup 在生命周期锁内先原子持久化 quarantine，失败不得删除/重开；删除失败保持隔离；只有所有句柄关闭、DB/WAL/SHM/journal/sidecar/key/case 删除并父目录 Sync 后才写永久 deleted tombstone。 |
| 新矛盾/遗漏扫描 | **无新增阻断** | OpenVerified、receipt-first、case 生命周期锁、目录项校验、跨重启复用与永久 tombstone 的先后关系一致。建议实现规格再列清 catalog 首次创建的 CREATE_NEW/目录同步步骤及 catalog 锁与 case 锁的固定获取顺序，避免实现者自行决定；目前不构成 SDD 阻断。 |

## 实施验收门

1. modernc VFS 必须有独立的 open-handle 注入入口，禁止接受数据库路径/DSN 作为可信身份来源；同一个连接池连接不得静默另开 path-backed 连接。
2. WAL/SHM/journal 的文件创建、锁和共享内存操作都要落在已验证目录对象上；两平台均注入路径替换、符号链接/reparse、硬链接和并发打开，验证绝不访问替换对象。
3. catalog 首次创建、原子替换、Flush/Sync 失败、进程崩溃恢复与锁顺序均需实现级故障注入；任何 catalog 不确定态不得允许 case 打开。

这些是实现与验收门，不改变本轮设计评审 PASS 结论。

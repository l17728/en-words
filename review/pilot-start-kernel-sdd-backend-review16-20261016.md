# lesson.start 持久迁移内核 SDD：后端复审 16

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 第 16 轮稿。检查 modernc 路径连接与威胁边界、连接 hook 的后置诊断、WAL/SHM/journal、catalog cleanup 的零字节/部分尾记录恢复。只读；未修改产品代码/设计文档，未运行测试。

## 结论

**PASS（在当前明确收窄的威胁模型内）；无须为了 OpenVerified handle 注入而强制更换 SQLite 驱动或引入可写自定义 VFS。** 规格不再声称现代c connection hook 能在 SQLite 首次页访问前证明身份，而是把 PRAGMA 明确作为连接后的诊断；真正防 path replacement 的边界是 Windows 持有禁止 DELETE/RENAME 的验证句柄、POSIX 0700 父目录，以及排除同账号攻击。DB 和辅助文件均位于非信任主体不能写入的 case 目录，故按路径连接是可实施方案。catalog 写失败顺序和重启状态也已有明确分支。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| modernc 路径连接及安全边界 | PASS | §42 明确使用仓库锁定的 modernc 驱动、固定 canonical path、`mode=rw`，不自动创建；安全依赖 owner-only case 父目录及平台权限阻止其他账号替换，不依赖未公开的 writable VFS。威胁模型明确排除监督器同账号攻击，因此不要求 driver 支持既有 handle 注入。Windows 持有的 OpenVerified handle 禁止 delete/rename，POSIX 0700 阻止其他 UID 改目录项。 |
| 连接身份诊断时序 | PASS WITH IMPLEMENTATION GATE | connection hook 的首条 SQL 为 `PRAGMA database_list`，其后用 OS file ID 对比 OpenVerified 记录；文档明确这发生在 SQLite 打开连接之后，不声称早于 SQLite 打开或页读取，负例也只断言无应用 SQL/receipt/课堂迁移。语义诚实，不再要求一个无法由 hook 保证的 pre-page 时点。实现需关闭 GORM/driver 自动 ping 和连接初始化 SQL，并验证 hook 确实是应用查询前首条 SQL。 |
| WAL/SHM/journal | PASS | §42 改为由 SQLite driver 在受保护 case 父目录内按路径管理；连接前检查既有辅助文件 owner/ACL、链接/reparse 与 file ID，缺失时允许 SQLite 创建；安全依赖父目录权限阻止其他账号插入或替换。相较上一轮，不再要求与默认 VFS 不兼容的目录 handle 接管。实施仍需确认生成文件权限符合 Windows/POSIX owner-only 门槛，以及残留 WAL/SHM 在重启时的正常恢复路径。 |
| cleanup quarantine 追加失败：零字节 | PASS | 只有完整 `cleanup_quarantined` 记录追加并 Sync 后才删除数据。零字节持久化失败时本进程 fail closed、不删除；重启时旧 catalog 状态继续权威，只能在不删除前重新检查 case 后重试 cleanup，顺序安全。 |
| cleanup quarantine 追加失败：部分尾记录/Sync unknown | PASS | 重启发现部分帧、格式/序号错误或 Sync 不确定时全局 fail closed，不回退旧 generation、不丢隔离事实；需人工检查。删除失败时写入持久隔离；全部删除且父目录 Sync 后才追加永久 tombstone。处理虽保守，但状态与恢复后果明确。 |
| 是否需新 SQLite driver / writable VFS | 不需要（有条件） | 在“不抵抗同账号或管理员”且目录权限为安全边界的前提下，当前 path-based modernc driver 可实施。若后续把同账号竞态纳入威胁模型，或要求身份校验早于 SQLite 首次页访问，则本设计结论失效，必须另行评估 writable VFS/driver 支持。 |

## 必须保留的实现验收门

1. 在 Windows 和 POSIX 用实际测试账户验证 case 父目录及每类 SQLite 文件（主库、WAL、SHM、journal）的 owner、DACL/mode、reparse/link 与创建权限；另账号在连接及重启期间不得替换或注入文件。
2. 注入连接初始化，证明无 automatic ping/隐藏 SQL；每次新连接的第一条 SQL 是身份 PRAGMA，身份不符则无应用 SQL和 receipt 查询。记录这是后置诊断，不宣称阻止 SQLite 在 hook 前读取页。
3. 对 WAL/SHM/journal 做跨 child 重启和未 checkpoint 崩溃恢复，确认既有文件会被验证并可正常使用、缺失文件由受限目录内创建；权限/身份异常时 fail closed。
4. 分别注入 catalog cleanup quarantine append 的零字节失败、半帧、Sync unknown、文件删除失败、tombstone 前后崩溃；核对报告规定的本进程与重启状态。catalog 不含 HMAC 是有意设计，不把旧 HMAC 测试断言带回。

这些是实现和测试门，不构成本轮设计阻断。

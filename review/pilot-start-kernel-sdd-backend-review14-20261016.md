# lesson.start 持久迁移内核 SDD：后端复审 14

评审对象：`docs/41-试点课堂Start持久迁移内核SDD.md` 最新稿。范围为 OpenVerified handle 持有、逐连接首条 SQL 身份校验、SQLite 辅助文件及 catalog/quarantine 语义。只读检查；未修改产品代码/设计文档，未运行测试。

## 结论

**BLOCK：1 项 P1。** 持有主 DB handle、限制路径替换，再用首条 `PRAGMA database_list` 与 OS file ID 做逐连接比对，是可实现的防护合同；catalog 不含 HMAC 与限定的威胁模型一致。但 WAL/SHM/journal 的“no-follow、CREATE_NEW/O_EXCL”与跨子进程重启复用同一 case、恢复既有 WAL/SHM 的要求冲突；而默认 modernc VFS 的路径型打开不能单靠 connection hook 实现目录句柄相对/no-follow 创建。需明确安全打开算法及存在/不存在两种分支，或为 pilot_e2e 指定并验证自定义 VFS。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| OpenVerified handle 与主 DB | PASS | §42 要求一直持有已验证 DB handle 至所有 SQLite 连接关闭；Windows 禁止共享 DELETE/RENAME，POSIX 使用 0700 父目录并将同 UID 监督器列入可信边界。不能满足 hold+identity 时 fail closed。 |
| 每连接首条 SQL 校验 | PASS WITH IMPLEMENTATION GATE | `PRAGMA database_list` 可作为 connection hook 的首条 SQL，随后将返回路径及其 OS file ID 与 OpenVerified 记录比对，失败即关连接并隔离。需在实现合同中说明 driver/ORM 初始化阶段不先发其他 SQL/自动 ping；同时确认 file ID 是在受保护目录项下取得，不能只依赖字符串路径相等。 |
| WAL/SHM/journal 安全打开和重启 | **BLOCK / P1** | §42 要求这些文件通过 `CREATE_NEW/O_EXCL` 创建；但 harness 明确跨 child 重启复用 case/DB，崩溃或 pending_unknown 场景可能已有有效 WAL/SHM/journal，重启需安全 reopen，而 O_EXCL 必须失败。若将其改成“存在时验证并 no-follow 打开，不存在时原子 CREATE_NEW”，还需解决 SQLite 默认 modernc VFS 会按路径自行创建/打开辅助文件的问题。建议规定专用 VFS 在已验证目录 handle 相对 `openat(O_NOFOLLOW)`/等价 Windows handle API，先拒绝链接/硬链接并核身份；缺失时 CREATE_NEW，存在时验证后复用；连接/恢复/清理均使用同一目录句柄。不要把 precreate 后普通 SQLite path open 当作满足条件。 |
| catalog 无 HMAC | PASS | 当前威胁模型排除可信监督器账号被攻陷；catalog 有 ACL/owner/file-ID/schema 校验、原子替换及 Flush/Sync，篡改保护不依赖 HMAC。review13 中有关“catalog 坏格式/HMAC”的旧检查应确认 HMAC 断言已移除；本稿 §93 目前写坏格式而未写 HMAC，与用户说明一致。 |
| catalog/quarantine 生命周期 | PASS | catalog 在任何 case 创建/open 前验证并查状态；catalog 读/格式/身份/Sync异常全局 fail closed。cleanup 在锁内先持久 quarantine，失败不删不重开；清理成功、父目录 Sync 后记永久 deleted tombstone；ID 不复用。失败语义一致。 |

## 必须的实现验证门

1. 指定连接 hook 的具体时序，证明 `PRAGMA database_list` 是新 SQLite connection 上任何 SQL/自动 ping 前的首条查询，并验证每个连接都运行该校验。
2. 实现并故障注入测试辅助文件的“缺失时原子安全创建、已存在时验证后安全 reopen”两条路径；覆盖 WAL 恢复、SHM 锁定、journal、崩溃重启与目录项替换。
3. 在 Windows 和 POSIX 验证现代c VFS 实际调用的是受控目录句柄相对/no-follow 文件操作。若默认 VFS 无法注入已验证目录操作，pilot_e2e 必须使用专用 VFS；不能声称只靠主 DB handle 和 SQL hook 覆盖 WAL/SHM。
4. catalog 测试聚焦 ACL/owner/file-ID/schema、原子替换、Flush/Sync 与 quarantine 崩溃恢复，不再要求 catalog HMAC。

# Pilot catalog journal：后端设计复审 R3

评审范围：docs/41 §109 最新完整合同，特别核对 bootstrap lock、首次 CREATE_NEW + provisioning 首帧、启动 replay/identity/Sync、stable case lock、cleanup resume、锁顺序与故障矩阵；对照 §§42、56、114。只读；未修改文档/代码，未运行测试。

## 结论

**PASS（设计/流程可实现；平台与监督器运行验收仍未完成）。** bootstrap lock 串行化首次创建与启动恢复；首个 `provisioning` frame 和目录项持久后才创建 case 内容；startup 在 bootstrap+catalog 锁下验证完整 journal，随后释放两把锁再进入 case 锁；case 操作和 cleanup 固定按 case→catalog 获取，不形成锁环。首次创建和各 case 迁移都存在完整合法的状态路径，失败后恢复/清理路径可达。

## 复核结果

| 项目 | 状态 | 依据 |
|---|---|---|
| bootstrap lock 首启竞争与锁顺序 | PASS | §109.1 要求永久 bootstrap lock 经 OpenVerified 打开、跨进程互斥、禁止 unlink/recreate；启动顺序为 bootstrap→catalog。正常 case/cleanup 操作不取 bootstrap。startup 释放 catalog、bootstrap 后才获取 case lock；case 操作仅 case→catalog。所有持 bootstrap/catalog 的路径均不等待 case lock，因此没有环路。 |
| 首个 catalog CREATE_NEW + provisioning frame | PASS | 首次创建在 bootstrap lock 内执行，CREATE_NEW 创建 catalog 并直接写第一条完整 `provisioning` frame，文件 Sync 后再 Sync 父目录；两者完成前不建 case lock/目录/DB/sidecar/key。现有空文件或部分帧失败关闭；这避免将空 catalog 当作有效初态。 |
| startup catalog replay、identity 和 Sync | PASS | startup 对已有 catalog 在 bootstrap+global catalog 锁下完整 replay、校验 identity/file ID，再 Sync 文件和父目录；全局锁释放前状态不可被其他 append 改写。校验失败不授权任何 case。对于完整 frame 但响应丢失，以持久状态为准，不推断 HTTP ack。 |
| provisioning 恢复与 stable case lock | PASS | 首帧 durable 后 case ID 已不可复用；provisioning recovery 在释放 startup 锁后原子创建/打开稳定外部 case-lock 对象，竞争进程需汇聚同一身份并先取得锁，再检查固定初始成员。全项完整 durable 才 provisioning→ready，否则 quarantined→cleanup；恢复路径不启动课堂或 Agent。 |
| ready、start、失败与cleanup迁移 | PASS | `ready→open_recovery_required` 必须在任何 lesson DB/sidecar 副作用前持 case lock Sync；known result 才能写 committed/terminal_failed。ready 未启动 case 可以在证明未开始/无 lesson 写入后进入 cleanup_quarantined。provisioning 失败先 quarantine；cleanup_quarantined 恢复只删固定成员、同步父目录后写 deleted；已删 ID 永久保留。无死路迁移。 |
| crash/fault matrix | PASS（可执行） | 矩阵覆盖无 catalog 首启、空/部分首帧、完整帧 Sync 不确定、parent Sync、replay/identity 失败、cleanup 每个删除边界、写故障及多进程争锁。完整有效 frame 是确定的权威状态；partial frame 全局失败；同步错误后的完整链也按该 oracle 恢复。命名 barrier 和 open counter 使验收可确定重现。 |
| Windows/POSIX 可实现性 | PASS（强制平台门） | 主体使用可实现的排他文件锁、固定文件身份、目录保护、文件/目录 Sync 和 append-only 文件。POSIX 可用 flock/fcntl 与 mode/dir Sync；Windows 需 verified handle 上的 LockFileEx、ACL/identity 和平台目录持久化实现。文档把平台专属行为留作独立 gate，没有假设 Go `os.File` 能统一解决。 |

## 尚未关闭的运行门

此 PASS 只表示设计合同没有发现新的不可实现步骤或锁环，不代表实现存在。首次创建、崩溃恢复、stable lock 身份、Windows/POSIX ACL/file ID/目录 Sync、OS 锁进程死亡释放、容量边界、cleanup resume 和 SQLite/sidecar 禁止越权 open，仍须由独立 supervisor 与 child-process 双平台验收证明。

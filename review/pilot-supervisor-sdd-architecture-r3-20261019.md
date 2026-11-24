# Pilot Supervisor SDD 架构复审 r3

**BLOCKED：2 项 P2 验收合同歧义；无新的 P1 架构阻断。**

审阅 `docs/42-Pilot隔离运行监督器SDD.md`，SHA-256：`FC90528BA994BC999AC27119533EB90784D22599CD25126D70A8622AACB001B0`。重点对照 docs/41 §109 的 catalog/状态/cleanup 合同及 docs/42 §§7–10、FI 表。末次复核确认期间新增的 SQLite handle identity 边界说明与 docs/41 一致，且未改变本轮所审锁序/状态/FI 条款。只读评审，未改文档或代码，也未运行测试。

## 复核结论

- **catalog 锁对象唯一及初始化线性化：通过。** §7 明确 bootstrap lock 是启动初始化互斥对象；已存在 catalog 的全局锁绑定到已验证 `catalog.log` handle，不另设同义锁文件；首次创建时由 bootstrap lock 独占 CREATE_NEW，再锁定新 catalog handle，完成首帧及文件/根目录 Sync 后才允许 contender 读取。已有 catalog 启动在 bootstrap→catalog 顺序完整 replay/sync；正常操作只取 catalog 锁。正常写入者不等待 bootstrap lock，因此没有反向锁边。
- **snapshot→case lock→catalog 复核：通过。** §2、§8 都要求两次 catalog 读取分别持锁，第一次只作 advisory，释放后取得 stable case lock，再持 catalog lock复核并释放，之后才 OpenVerified。持 catalog lock 时不等 case lock；状态变更也持同一 case lock，故第二次复核至成员打开之间不会发生同 case 状态竞态。
- **状态 capability 与 docs/41 §109：通过。** §8 覆盖全部状态；ready-only Start、recovery 不重放 Planner/Registry、committed receipt-only、terminal_failed catalog-only、quarantined cleanup-only、cleanup_quarantined cleanup-resume、deleted tombstone 拒绝均与 §109 相符。Provisioning recovery 不调用 Coordinator，固定成员完整才 ready。
- **case 锁与清理/崩溃恢复：通过。** stable case lock 位于 case directory 外且永不 unlink/recreate；cleanup resume 使用同一 identity。case 删除后、tombstone 前的重启路径可以在目录确认不存在后写 deleted。FI 表总体按 write/Sync/kill/restart 区分完整帧、旧状态与 partial-frame fail-closed，未发现不可实现的锁序矛盾。

## 待修发现

1. **SUP-SDD-R3-01（P2，§8 provisioning recovery 与 FI-DB-01）—DB open 计数自相矛盾。** §8 明确 provisioning recovery 在 DB 已存在且身份通过后，用 `mode=ro` 单连接执行 `PRAGMA database_list` 和 `PRAGMA integrity_check`。FI-DB-01 的故障/重启矩阵最后却要求 `DB opens=0`。对“DB 写入及 Sync 已完成、进程随后崩溃、重启进行 provisioning recovery”的分支，这两个要求不能同时满足。
   **要求：** 将 FI-DB-01 的计数拆为本进程创建/写入失败路径（不再打开 DB）和重启恢复路径（仅允许一次只读验证 open，且仅允许两条固定 PRAGMA；identity/完整性失败后不得执行应用 SQL）。若某个注入点不进入 recovery，应明列其精确分支与零 open 断言。

2. **SUP-SDD-R3-02（P2，§109 cleanup 来源状态与 FI-CLEAN-01）—cleanup-quarantine append 未覆盖所有原状态的重启裁决。** docs/41 §109 允许 `ready`、`open_recovery_required`、`committed`、`terminal_failed`、`quarantined` 在获授权 cleanup 下转入 `cleanup_quarantined`。FI-CLEAN-01 只说明重启后 catalog 为 `quarantined` 时重做 transition，未说明从其它来源状态开始 cleanup、transition 零字节/无帧时应如何处理。不能从旧状态推断被中断的 cleanup 请求仍获授权，也不能把这些来源状态误判为 cleanup 已开始。
   **要求：** 为每个合法来源状态分别规定：cleanup intent 尚未形成任何完整 frame 时，保留原状态并等待新的显式 cleanup 请求；完整 `cleanup_quarantined` frame 时只 resume；partial frame 时全局 fail closed。加入所有来源状态的零字节、完整帧、partial-frame故障用例，并断言未 durable quarantine 前没有成员删除。

这两项是验收矩阵的合同澄清，不改变已通过的锁序和状态访问架构。关闭后可进行下一轮文档复审；目前不应以这份 FI 表作为实现验收 oracle。

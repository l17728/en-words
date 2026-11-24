# Pilot catalog journal：后端设计复审 R2

评审范围：docs/41 §109 的 catalog framing、状态机、锁/Sync 顺序及验收矩阵；对照 §§42、56、114。只读设计复审；未修改文档或代码，未运行测试。

## 结论

**PASS（设计合同层；实现与平台验收仍待完成）。** R1 的两项阻断均已闭合：完整合法 frame 的文件及首次创建所需目录 Sync 是持久迁移的线性化点，恢复不再猜测 HTTP 响应是否送达；未启动 `ready` case 可在证明无启动/lesson 写入后受锁清理。当前 frame 编码、全局 generation、状态迁移及验收矩阵可由 Go supervisor 实现。

## 复核结果

| 项目 | 状态 | 依据 |
|---|---|---|
| R1 P1：acknowledged-frame 不可判定 | PASS | §109.1 明确 Sync 为 durability linearization point，完整合法 frame 在重启时权威，无“acknowledged bit”，调用方通过读取状态收敛；§109.3 仅接受完整合法链并拒绝 partial frame，不再要求推断响应送达。同步错误仍使当前 supervisor 全局 poison、不回复成功，满足未知持久化期间禁止继续工作的合同。 |
| R1 P2：ready case cleanup 不可达 | PASS | §109.2 增加 `ready → cleanup_quarantined`，并要求 lifecycle lock 下证明无 start attempt 或 sidecar/DB lesson write；后续删除、父目录 Sync、`deleted` tombstone 路径与已有 cleanup 顺序一致。 |
| 帧编码可实现性 | PASS | DWC1/trailer、big-endian 长度、CRC-32C/Castagnoli、payload 限长、重复长度、canonical ASCII JSON、generation 连续及总上限都定义明确，可直接实现并以 golden bytes/CRC 测试锁定。 |
| 锁与耐久顺序 | PASS（平台门） | 全局 OS 排他锁保护整链读取、转移校验及 append/Sync；与 §56 case-lock→catalog-lock 次序相容。Windows handle lock/ACL 与 POSIX 文件锁、权限及目录 Sync 有明确独立平台门，未声称纯 Go `os.File` 能统一提供目录 ACL/锁行为。 |
| 状态迁移覆盖 | PASS | 初始化 provisioning、ready、start open/recovery、committed/terminal_failed/quarantined、cleanup_quarantined、deleted 的主要路径完整；provisioning 必须先 quarantine，deleted 永不迁移，ready 清理具备安全前提。未知/损坏状态按全局 fail-closed。 |
| 验收矩阵 | PASS（可执行） | Golden frame/CRC、canonical JSON 负例、跨 case generation、全迁移表、大小/帧数边界、写/Sync 故障、frame boundary crash、两进程追加及双平台安全门均可独立实现。§109.3 已把恢复断言改为每个 frame boundary 接受完整链、partial frame 全局拒绝，oracle 可观察且确定。 |

## 仍待实现验收

此 PASS 仅确认设计合同无新增阻断，不表示 catalog 或 supervisor 已实现。首次创建竞争、OS 锁行为、verified-handle ACL/file identity、POSIX/Windows parent-directory durability、Sync 故障后的进程 poison 与重启恢复，以及 16 MiB/65,536-frame 极限都须通过 §109.3 的子进程和双平台测试后才可宣称运行时通过。

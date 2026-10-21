# Pilot Supervisor SDD 架构复审 r4

**PASS：R3 两项 P2 已关闭；未发现新的阻断或不可实现矛盾。**

审阅 `docs/42-Pilot隔离运行监督器SDD.md`，SHA-256：`0B3019C4DDE3CA42F3BF05D29768332D72AC72C7259DDEE4F8A7372E6F9AE5A1`，并对照 docs/41 §109.2–109.3。复核期间的末次修订将 DB 计数统一命名为 `db_open_rw`，并为底层 close 状态 unknown 明确规定专属 child 退出门；这些条款均纳入本结论。只读复审；未修改设计/代码，未运行测试或构建。

- **SUP-SDD-R3-01 — CLOSED。** §44 将普通请求拒绝计数与受限恢复核验计数分开。FI-DB-01a/01b 对应用 DB open 断言为 0，同时允许 provisioning recovery 最多一次 `mode=ro` 连接，且只执行 `PRAGMA database_list` 与 `PRAGMA integrity_check`；和 §81 的恢复协议一致。FI-DB-01c 在身份失败时不允许 SQLite 打开不可信对象，必要的格式核验也限定在 case/catalog 锁后只读 PRAGMA。
- **SUP-SDD-R3-02 — CLOSED。** FI-CLEAN-01 明列 docs/41 允许的全部 cleanup 来源状态，并区分零字节（原状态保持，重新执行来源状态授权）、完整 frame（恢复 cleanup_quarantined 并 resume）、partial frame（全局完整性失败）。删除只能在 cleanup 状态持久化后发生。
- **catalog lock 唯一性与初始化线性化 — PASS。** §63 将 `catalog.log` 的已验证文件 handle 定为唯一 global catalog lock 对象；首次 CREATE_NEW 由永久 bootstrap lock 串行化，再锁新 handle 并完成 Sync，避免空/半初始化 catalog 被读取。普通 catalog 操作不等待 bootstrap 锁，未形成反向锁顺序。
- **双阶段授权及死锁检查 — PASS。** advisory catalog 快照在 global lock 下读取后释放；随后取得稳定 case lock，再取得 global lock复读/裁决并释放，最后才 OpenVerified。持 global lock 不等待 case lock；case 状态写入在稳定 case lock 下完成，因此复核后不会有同 case 的状态竞态。
- **状态 capability 与 docs/41 §109.2 — PASS。** `ready` 才能首次 Start；恢复态不重放 Planner/Registry；committed 只读 receipt；terminal_failed 只状态/清理；quarantined 只显式清理；cleanup_quarantined 仅 cleanup resume；deleted 为永久 tombstone。cleanup 来源范围也覆盖 §109 允许的 ready、open_recovery_required、committed、terminal_failed、quarantined。
- **cleanup absent-directory 与 docs/41 §109.3 — PASS。** §78 区分目录存在和已删除两种情况：前者核验空目录、经 pinned parent 删除并 Sync；后者在 pinned parent 下复核不存在并 Sync；两条路径都先于 deleted tombstone，稳定 case lock 在外部目录继续保留。
- **句柄关闭与 cleanup — PASS。** 普通 `CaseHandle.Close` 按 DB→sidecar/key→case-dir→case lock 关闭，Supervisor 共享句柄仅由 Supervisor.Close 释放。CleanupCase 在持有 case lock 时先关闭会阻止删除的资源句柄，完成成员/目录删除和 tombstone 后再释放 case lock；关闭不确定时保留 lock 并 fail closed。该顺序与 §89、§93、§95 及跨进程 cleanup 线性化一致。
- **unknown close 进程恢复 — PASS。** 最新 §32 要求 close unknown 时标记 restart_required、专属 child 非零退出，并由外层 harness 等待并确认同一 PID 已退出后才可启动新 child；§95 禁止同进程重试 unknown OS close。这避免了共享锁仍可能持有时在进程内替换 Supervisor 的矛盾。

文档架构复审通过。此结论不代表监督器代码、Linux/Windows 实机运行或 Playwright/E2E 课堂验收已完成。

# Pilot Supervisor SDD 架构复审 r2

**PASS：原 4 项发现和复审发现均已关闭；无开放问题。**

审阅当前 `docs/42-Pilot隔离运行监督器SDD.md`，SHA-256：`8A651B4D5193C2DD047BEAE85379C631FA617FF74230A6298D6DB495E980FC51`；对照 `docs/41-试点课堂Start持久迁移内核SDD.md` §109。只读设计复审；未改设计/产品代码，也未运行测试或构建。

上一轮 findings：

- **SUP-SDD-01 — CLOSED。** §7 分开定义首次 catalog bootstrap、已有 catalog 启动与后续 case 创建。首次 bootstrap 用永久 bootstrap lock 覆盖 CREATE_NEW、首个 `provisioning` frame、catalog 与根目录 Sync；在 durable frame 后释放 bootstrap/catalog 锁，再创建 stable case lock。后续 case 则先在外部 `case-locks/` CREATE_NEW 并持有稳定锁，再按 case→catalog 顺序写 reservation。§8、§10 补充了并发 opener、Sync 失败重启和屏障验收。该方案与 §109.1–109.2 的 bootstrap 特例及正常锁序一致。
- **SUP-SDD-02 — CLOSED。** §8 按 absent、provisioning、ready、open_recovery_required、committed、terminal_failed、quarantined、cleanup_quarantined、deleted 列出操作能力和恢复边界。`open_recovery_required` 恢复要求 receipt/sidecar 独立核验，禁止重放 Planner/Registry；清理恢复仅固定成员删除、目录 Sync、再写 tombstone。与 §109.2–109.3 状态约束一致。
- **SUP-SDD-03 — CLOSED。** §7 将稳定 case lock 放在可删除 case 目录之外，规定 CREATE_NEW 后永不 unlink/recreate，删除 tombstone 后仍保留身份；cleanup resume 重开并验证同一 lock identity。与 §109.3 一致。
- **SUP-SDD-04 — CLOSED。** §8 定义 ReadyCase、ReceiptCase、RecoveryCase、CleanupResumeHandle、StatusCase 等 least-capability 接口，并声明拒绝状态在任何 DB/sidecar/key open 前失败。§9 明确 Supervisor 持有 root/bootstrap/catalog/global locks，CaseHandle 只持 case-scoped 资源；单 case Close 不会关闭共享 catalog。与 §109 状态访问边界一致。

复审发现 **SUP-SDD-R2-01 — CLOSED。** §2 不变量1现统一规定 16-byte/128-bit、32 位小写 hex，并直接引用 docs/41 §109.1；与 §7 的 `<32hex>.lock` 命名及 catalog schema 一致。当前文档未发现新的锁循环、状态迁移越权、恢复重放或句柄生命周期阻断。文档架构门通过；这不代表监督器实现或平台/Playwright 验收已完成。

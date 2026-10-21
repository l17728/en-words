# Pilot Supervisor SDD 架构评审

**BLOCKED：2 项 P1、2 项 P2。**

审阅文档：`docs/42-Pilot隔离运行监督器SDD.md`（SHA-256 `D5E6EA5BB8C35EEB874080D3742EAD2C3DBA6B7A55A4D35F642EBADD120FBFD5`），并对照 `docs/41-试点课堂Start持久迁移内核SDD.md` §109.1–109.4。只读设计评审；未修改产品代码，也未运行测试或构建。

1. **SUP-SDD-01（P1，docs/42 §3 CreateCase、§4；docs/41 §109.1、§109.2）—首次建 catalog 的锁顺序/原子初始化未定义且与 §109 冲突。** §42 要求 `CreateCase` 先持 case lock 再耐久写 `provisioning`，但 §41 首次 bootstrap 要在永久 bootstrap lock 下先将首个完整 `provisioning` frame 写入并同步 catalog 文件和父目录，之后才创建稳定 case lock 与 case 文件。§42 的 `OpenPilotSupervisor` 也未说明 bootstrap lock 如何覆盖 CREATE_NEW 到首帧及父目录 Sync。若照 §42 字面实现，会在 case lock 尚不存在时无法遵守“case lock → catalog lock”，或让并发 opener 观察到空/半初始化 catalog。
   **要求：** 明确区分首次 catalog bootstrap 与已有 catalog 的新 case 创建；逐步定义 bootstrap lock、catalog lock、stable case lock 的获取/释放顺序。首次 bootstrap 必须沿用 §109 的 bootstrap 特例，初始化期间 opener 不得检查中间文件；后续 case 创建必须有稳定 case lock 后再按 case→catalog 顺序追加 `provisioning`。父目录 Sync 失败后重启须在任何 case/lock 产物前重新验证并成功 Sync。

2. **SUP-SDD-02（P1，docs/42 §4）—重启恢复只覆盖 `provisioning`，遗漏 §109 的运行中及清理恢复。** 文档写“只有完整 `provisioning` 可恢复为 ready”，未定义 `open_recovery_required` 的 receipt/sidecar 核验与终态迁移，也未规定 `cleanup_quarantined` crash 后的 cleanup-resume。§41 要求前者不得重放 Planner/Registry，须独立核验后记录 `committed`、`terminal_failed` 或隔离；后者只能在稳定 case lock 下删除固定成员、同步目录并追加 `deleted`，不得打开 DB/sidecar/key。缺失这两条恢复路径会令已存在状态既无法安全恢复，也无法实现声明的完整 supervisor 生命周期。
   **要求：** 增加逐状态启动/恢复矩阵，覆盖全部 §109 状态和每个恢复操作允许打开的文件、允许追加的转移及未知结果处理；`open_recovery_required` 不得被当作 `ready`，清理恢复不得重新进入普通 OpenCase。

3. **SUP-SDD-03（P2，docs/42 §2 不变量4、§3 CleanupCase；docs/41 §109.3）—稳定 case lock 的存放及身份生命周期未落地。** §42 只称“case OS lock”，没有规定其位于独立受保护 lock root、保持跨进程/重启稳定，也没有禁止 cleanup unlink/recreate lock 文件。§41 明确锁对象必须位于可删除 case 目录之外，且清理及崩溃恢复期间不得替换身份；否则等待者可能锁住旧 inode/文件对象，而新进程锁住新对象，破坏同 case 串行化。
   **要求：** 明定固定 lock root、文件命名/权限/身份验证、原子首次创建和永不 unlink/recreate 规则；cleanup-resume 必须复用同一已验证锁身份。

4. **SUP-SDD-04（P2，docs/42 §3 OpenCase/CaseHandle.Close；docs/41 §109.2）—授权打开能力和句柄所有权语义含糊。** “`ready` 等 §109 明确可打开状态”未明确区分 `committed` 的 receipt-first read、`terminal_failed` 的状态/恢复路径、`quarantined` 的 receipt-first recovery/cleanup，以及只有 `ready` 可启动新 Start。若 OpenCase 返回可访问 DB/sidecar/key 的通用句柄，可能把受限状态误当普通 case。另 `CaseHandle.Close` 要关闭“catalog 相关句柄”，但 catalog 是 Supervisor 全局生命周期句柄；由某 case 关闭它会破坏其它并行 case。
   **要求：** 为每个状态定义能力最小化的 typed handle/操作（普通 Open、receipt-only、recovery-only、cleanup-only），指定拒绝发生在任何 DB open 前；明确 catalog/root/bootstrap 句柄归 Supervisor 所有，CaseHandle 只关闭自己的 DB/sidecar/key 与 case lock，Supervisor.Close 才关闭共享句柄。

**通过项：** §42 明确保持 test-only 范围、禁止调用者重定向路径/DSN/key、坚持 fail closed，并要求 Windows/Linux 实机验收与浏览器旅程独立过门；这些边界与 §41 的隔离内核定位一致。§42 的 case→catalog 双阶段复核原则与 §41 正常 case 操作锁序一致，但需按 SUP-SDD-01 补上 bootstrap 特例。

这是设计门结论，不代表已有实现存在上述故障，也不代表这些验收用例已经执行。修订并关闭发现后，建议架构与安全域复审，再进入监督器实现。

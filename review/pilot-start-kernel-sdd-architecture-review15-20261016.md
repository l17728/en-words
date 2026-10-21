# 架构复审 15：Start 持久迁移内核 SDD

- 日期：2026-10-16
- 范围：复核 `docs/41-试点课堂Start持久迁移内核SDD.md` 当前双阶段 case 授权、catalog generation 锁和 open/cleanup 生命周期；未运行测试。
- 结论：**PASS**。architecture14 的 A14-01 已关闭；本轮未发现新的架构阻断。

## 核查结果

§3 已给出规范性双阶段协议：先持全局 catalog 锁做乐观检查并释放；取得 case OS 生命周期锁；持 catalog 锁重读并验证完整最新记录链及 case 状态，再释放 catalog 锁；只有第二次检查允许时才执行 OpenVerified 并打开 DB/sidecar/key。case 锁贯穿操作直到 SQLite 连接和所有句柄关闭，cleanup 也持锁直到 quarantine、删除、父目录 Sync 和永久 tombstone 完成。明确禁止释放 case 锁后继续用旧快照。§42 的通用 open 描述现在可由 §3 的具体算法解释，没有相反锁序要求。

catalog 全局 OS 排他锁仍覆盖链读取、generation 分配、单条追加与 Sync；所有写入/快照均受锁保护。锁序固定为 case→catalog，catalog 锁持有者不得等待 case 锁，且不覆盖 Planner、Registry、SQLite 事务或删除。不同 case 的业务阶段可并行。

§97 同时覆盖 cleanup-wins 与 opener-wins：前者验证 opener 等锁后按最新 catalog 状态拒绝且 DB open 计数为零；后者验证 cleanup 在 opener 关闭全部句柄前不能 quarantine 或删除，释放后清理完成，后续 opener 必须拒绝。两项均要求有界完成，足以锁定互斥与顺序。

此前安全域17涉及的存储边界仍一致：OpenVerified 身份拒绝发生于 DB 使用前；SQLite PRAGMA 明确只是连接后的诊断，不声称消除同账号竞态；威胁模型依赖受保护目录和可信监督器账号，并明确排除同账号/管理员被攻陷。未发现把该机制扩大为不成立的抗同账号保证。

## 复审边界

本次只读复审 SDD，不运行代码测试；实现与测试是否兑现该合同需在编码后的阶段验收中确认。

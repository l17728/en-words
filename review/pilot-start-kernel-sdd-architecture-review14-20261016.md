# 架构复审 14：Start 持久迁移内核 SDD

- 日期：2026-10-16
- 范围：只读复核 `docs/41-试点课堂Start持久迁移内核SDD.md` 的存储边界、catalog 锁与双阶段 open/cleanup 协议；未运行测试。
- 结论：**BLOCK**（1 项 P1）。

## 已确认

- §42 明确 catalog 是固定 inode、append-only generation 日志；跨进程 OS 排他锁下重读完整链、分配全局 `max_generation+1`、追加并 Sync。case 生命周期锁先于 catalog 锁；持 catalog 锁时禁止等待 case 锁，临界区不包括 Planner、SQLite 事务、删除或网络调用。§97 的两个 case 并发追加及重启用例验证 generation 唯一连续和重读恢复，锁序本身一致，没有发现新的死锁或无界串行化问题。
- §97 已写入同 case open/cleanup 竞争验收：opener 保留旧快照并阻塞；cleanup 完成 quarantine/tombstone、释放 case 锁后，opener 取得 case 锁并在 catalog 锁下复核最新状态，必须拒绝且 case DB open 计数为 0；同时要求 receipt/restart/open 路径使用双阶段协议。这关闭了“测试没有覆盖过期快照”的缺口。

## 阻断

**A14-01 · P1 · §42 与 §97 的 open 算法合同不一致/不完整**

§97 定义了期望结果和竞态测试，但 §42 的规范性操作描述仍是“先在 catalog 查验该随机 case ID 状态并用 OpenVerified ... 打开”，没有明确要求：先做乐观 catalog 检查并释放锁，再获取 case 生命周期锁，随后持 catalog 锁重读最新状态，释放 catalog 锁后才 OpenVerified/连接 SQLite；也未明确旧快照只能用于初筛、不得授权打开。若实现按 §42 字面顺序执行并在等待 case 锁后复用初筛结果，cleanup 可先 quarantine/tombstone，而 opener 仍可能打开 case DB。测试能发现实现偏差，但设计正文未冻结实现必须遵循的协议，且“catalog锁只覆盖...单条追加与Sync”与“catalog读取快照均持锁”需区分读取锁的释放边界。

**要求**：将 §97 的双阶段顺序提升为 §42 的规范性步骤，并明确 OpenVerified/DB open 只能发生于取得 case 锁且 catalog 最新状态复核通过后；catalog 锁须在 OpenVerified 前释放，禁止反向锁序。保留 §97 的同 case barrier 测试，并断言被拒绝路径的 OpenVerified/DB open/SQL/receipt 计数均为 0。

## 复审范围与限制

未运行代码或测试。其余已读合同（receipt-first、quarantine/tombstone、固定 inode catalog、generation 全局锁及 §97 对应并发验收）未见本轮新增架构阻断。

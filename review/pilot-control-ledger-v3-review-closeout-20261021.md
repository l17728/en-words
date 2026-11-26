# Pilot Control Ledger v3 设计阶段评审收敛记录

日期：2026-10-21  
范围：docs/51–53、literal DDL、11 条 invariant SQL、bootstrap seed、DDL/capability manifest 与 Python SQLite acceptance harness。仅关闭设计与 SQL 候选评审；未实现产品运行时代码。

## 评审结论

架构、后端/事务、安全三个领域对相同冻结哈希快照均为 **PASS**，未发现设计层遗留阻断项。评审覆盖 SQLite 对象清单、事务边界、EEXIST 失败 ACK、关闭中间态、prepared cancellation、终态 close receipt 绑定、连接 authorizer 与能力 scope 设计。

关键收敛项：

- INV-06 接受 created root 在 `terminating` / `containment_empty` 关闭过程中的合法状态，同时要求精确 lease owner；合法关闭完成后允许 abandoned 终态。
- EEXIST ACK 只确认失败回执，不生成 root state、不晋升 lifecycle；allocation 和 terminal 均为 indeterminate，精确 lease 保持占用。
- INV-07 按当前 run epoch 检查 lease，并对 normal close、prepared cancellation、no-root abort 的终态 close receipt 执行 operation、epoch、principal、payload digest 和 receipt digest 精确绑定。
- prepared cancellation 在取消事务中同步 abandon root operation 与 allocation；terminal state、close completed 和 lease release 仅在 finalization 同事务提交。

## 冻结快照

下列 SHA-256 标识三域共同复审的设计候选：

| 产物 | SHA-256 |
|---|---|
| docs/51 SDD | `5F7A9C8F6DA961FD42B4006C5DB63B38947BBD592B6E4C76FC990E63B580A394` |
| docs/52 物理模型决策 | `46C05FA4D1D0430F04BC377DD3CD9769869BDBD6FB9DC5BFE529EC595F362D7C` |
| docs/53 schema matrix | `45F89800D271D98D23545A94C3C4C347169316BDBEEA731D3B3521CF79C0999B` |
| Literal DDL | `C56D3EFB7C4A8AAC1811030464D138487CAE42EB5F74DA88182383007EDB70A3` |
| Invariant SQL | `5FBCB2D7075E482A2D6F5222339511EAD34ED5F8A99FDD635D56B7D45DBD0C8A` |
| Bootstrap seed | `D86709524A1367A26BA4526FA32B88B665D271CB9A0F3AF6AA42DD7E8142EB2D` |
| DDL manifest | `E12911D8D5AE537145B3A429B38E93F2AE43A64EA6C4EAA198EAF51FA7598A45` |
| Capability manifest | `87EEFDEE088C8E0A3A5A491F54445337A67424A5D314778DE6AB3064FD3C4553` |
| Python acceptance harness | `8A350CB31F2DB2F114598C777F26F805D26FF5DE8E211AAB9239548805C1E9D1` |

DDL manifest 列出 92 个显式 SQLite 对象（21 tables、2 indexes、69 triggers）；其 `source_hashes` 全部与当前源文件匹配。

## 验证证据

`python review/generate_pilot_v3_ddl_manifest.py` 连续生成的 manifest 哈希一致。`python review/test_pilot_v3_ddl_draft.py` 通过，覆盖初始化原子回滚、foreign-key 检查、INV-01..11、no-root、normal close 各提交边界、prepared cancellation、EEXIST ACK 正向路径，以及缺失回执/finish、孤立终态、EEXIST lease 丢失和错误晋升等故障注入。`git diff --check` 通过。

## 后续实施门

本评审不代表 v3 已实现或可用于真实课堂。实现必须采用隔离的 SQLite pool，并在每个物理连接安装 authorizer；Go/CGO 多连接集成测试需验证 scope 清理、签名与能力顺序、跨 scope statement 隔离及故障回滚。Linux/race、并发/容量压力与一百万 allocation 基准、产品 API/恢复集成及真实试点验收仍未完成，须在实现评审中逐项通过。Python SQLite harness 仅验证 SQL 和状态不变量，不能替代这些运行时门。

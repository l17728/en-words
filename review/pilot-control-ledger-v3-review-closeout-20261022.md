# Pilot Control Ledger v3 DDL 草案评审收敛记录

日期：2026-10-22  
范围：docs/50–54、docs/62–63，v3 literal DDL、bootstrap SQL、固定 seed manifest、schema inventory、capability policy、48 条 schema 故障用例及 Python harness。此记录只验收设计/DDL 草案，不授权或证明产品运行时实现。

## 结论

架构、backend/storage、安全三个领域按末版精确 SHA-256 快照复核通过。测试设计专家首轮指出的 marker 最终写入失败、manifest/inventory交叉校验、DML覆盖、异常清理和拒绝码判别缺口已补入 harness；最终回归通过，三个领域复核也核查了补强。测试设计专家未能对最后一次小幅 harness 更新重新签发单独 PASS。所有运行时和 Linux/VFS 门仍保持未通过状态。

复核中发现并已修复：

- `pilot_control_generations` 文档字段名曾为 `created_at`，DDL 为 `consumed_at`；docs/51 已统一为 `consumed_at`。
- principal alias 唯一键合同已统一为 `(tenant_id, principal_ref, identity_key_version)`，保留 HMAC key 轮换期间的旧版本映射。
- authorizer 原本对 capability 表之外的 protected table fail-open；现在以 inventory 中所有表为保护集合，缺表/方法映射默认 `SQLITE_DENY`，容量统计 trigger 是唯一获准的 trigger-originated DML 来源。测试直接检查拒绝结果码和所有 protected table 的未授权写入。
- `pilot_control_generation_advance` 曾作为 trigger UDF，却无法在 `trusted_schema=OFF` 下证明 innocuous 注册；现已从 DDL/docs/53 删除。DDL 只要求八字节 BLOB 严格递增，typed startup capability 负责 checked uint64 `n+1`、溢出拒绝、回读与提交后再开放 RPC。
- 初始化 marker 及 seed 写入顺序、inventory 字节摘要、canonical fixed-seed 字节摘要已绑定；`user_version=3` 先写，marker 是最终 DML，与 schema/seed 同事务。

安全评审指出 docs/62 的 VFS/WAL 持久性属于相邻但独立的前置门，不由本次 SQL 草案覆盖；该限制已在评审委托范围明确。Python harness 不能证明 Go SQLite driver 的真实 authorizer 回调、跨连接/进程行为、文件系统持久性或崩溃恢复。

## 精确快照

| 产物 | SHA-256 |
|---|---|
| DDL manifest | `f1f0ed32f1cf34cfbb6df4dbd54e29743548f556dff8395f14add6b41a7769fa` |
| schema inventory draft（103 objects） | `e537af51414816adacb038bb6b401d6f495c6021bb867f41e750f6703c671911` |
| schema-fault manifest（48 cases） | `91dc6d1dec1566ac527a976ed42d30eb6a776f49badc3d641bcbb1e1871822cd` |
| literal DDL draft | `cce1213a56eb03d0f0540d425f5af517e6b9104b440ad3d13e9de56145b901cf` |
| capability manifest | `7ca88ac2f1ffb333f43f5d1a1c9ee1270052193450584e98d981f17661164efc` |
| DDL acceptance harness | `262dd5fadb629468ddae6ef3abcc49477f92eca8a52ef0e47e2d9b8b19339d34` |
| docs/51 | `236af90bc914e325b62367338328bcd769f769f85fcfcbe8972410b0b58c363a` |
| docs/53 | `a1074766827e010f7247c985c4e957ad3f193b8a38f0503be052fa2aa0ca5dac` |

## 验证

- `python review/generate_pilot_v3_ddl_manifest.py` 生成 103 条 DDL statements、103 个显式对象、11 条 invariant；inventory digest 为上表值。generator 同步 schema/product manifest source bindings；quota manifest source bindings 另行刷新。
- `python review/test_pilot_v3_ddl_draft.py`：退出码 0。覆盖 policy seed 中途失败及 marker 最终写入失败的整事务回滚、canonical seed 和唯一最终 marker、manifest/source/DDL/inventory交叉绑定、INV-01..11、所有 protected table/action 的无权限拒绝、缺失/错误 capability、startup/bind/expiry 权限、严格递增、uint64 溢出、正常/异常路径 scope 清除、trigger 来源约束及 DDL/PRAGMA 拒绝；拒绝断言要求 SQLite `SQLITE_AUTH`。
- `python -m unittest review.test_pilot_runroot_manifest`：18 项通过，包含 product/schema/quota manifest 完整性与来源哈希检查。
- 架构、backend/storage、安全专家对上表末版精确快照给出 PASS。测试设计专家首轮提出的问题已落实并由末版回归锁定；未获得该专家对最后改动的单独重签意见。

## 后续实施门

本次草案评审不是 schema 实现准入。执行前仍需完成私有 pinned-handle SQLite VFS、精确 pinned SQLite source/runtime goldens、Go/CGO 每物理连接 authorizer 和 capabilities、真实文件系统 crash/reopen/race 测试及 Linux 验收。GitHub Actions Linux runner 曾因账户 billing/spending limit 在任务启动前被拒；需恢复可用 runner 才能通过该门。随后仍有 A0/A1/A2、Stage B ledger/RunRoot/outbox/closure、产品 API/UI/E2E、多用户旅程及人工监督的隔离课堂验收；进度以 docs/50 和各 acceptance manifest 为准。

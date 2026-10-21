# Pilot Supervisor 后端复审（R4）

复核文档：
- `docs/41-试点课堂Start持久迁移内核SDD.md` SHA-256 `EDCCD7B52ACE3300912796B69730B7B91D6C47542D959B9471798D173E10BE34`
- `docs/42-Pilot隔离运行监督器SDD.md` SHA-256 `BB41520912F0F81077DFE1905654599C4E5E369C5F81C8F58569C6F33B82FCB8`

## 结论

**后端复审 BLOCKED，剩余 1 项 P2。** 前轮关闭状态已核实：生产 close error 统一 unknown 并由 parent harness reap；schema 初始化改为完整只读模板；read-only recovery SQL 清单、catalog FI、`operation_canceled` 映射与日志生命周期均已补齐。

## 阻断项

### P2 — 模板恢复在写入 `ready` 前缺少重新 Sync

§8 和 FI-DB-01a 允许新进程看到同一 DB 身份且模板 SHA/长度完整后直接认定 provisioning 可 ready，但没有要求对重新打开的已验证 DB handle 及 case directory 再执行 `Sync`。完整 hash 只能证明当前可读字节一致，不能证明前一次崩溃前的文件/目录 Sync 已建立耐久性。这样 catalog 的 durable `ready` 可能先于模板数据的 durable boundary。

建议 recovery 顺序改为：OpenVerified → 验证身份/长度/SHA → Sync DB handle → Sync case directory → 仅在两次 Sync 成功后 append+Sync `ready`。任一 Sync 错误继续全局 poison/`io_uncertain`，并为这两个 recovery Sync 增加强杀与错误注入 oracle。

## 已复核并关闭的前轮问题

- **Close adapter：** 生产只允许 `{closed,nil}` / `{unknown,error}`；`os.File.Close` 和 `database/sql.DB.Close` 错误均为 unknown；`still_open` 仅供可证明原句柄未关闭的 fake；harness 必须确认专属 child PID 已退出后才重启。
- **空库初始化：** 改为复制完整嵌入模板。现有 `startupModels` 包含基础表（如 `runtime_records`、`lessons`），`pilotV2Objects` 是增量 schema；SDD 已要求离线 generator 合并两者与 V2 marker，不在运行时从零执行不完整 DDL。
- **恢复 SQL：** 白名单、schema marker、对象 manifest、规范化 SQL digest 和各 DB 计数已明确。
- **catalog FI：** write 前/短写/完整帧后报错与 file Sync/parent Sync 分项，并由 fresh replay 决定状态。
- **错误码与日志：** `context.Canceled`、deadline/cap 映射明确；日志 bootstrap、Sync、append、重启和大小上限均有规则及 FI。
- **§112 兼容性：** `PRAGMA database_list` 仅校验 canonical path；HMAC key 留在 Supervisor；与 docs/41 威胁边界一致。

JSON 报告已保存为 `review/pilot-supervisor-backend-r4-20261019.json` 并验证有效。未修改代码、未运行测试。
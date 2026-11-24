# Pilot Supervisor 后端复审（R5）

复核对象：
- `docs/41-试点课堂Start持久迁移内核SDD.md` SHA-256 `EDCCD7B52ACE3300912796B69730B7B91D6C47542D959B9471798D173E10BE34`
- `docs/42-Pilot隔离运行监督器SDD.md` SHA-256 `EC5CB5D5F1F7AA175CD0B571FB3CEA53CC5B08B1951FA87F8CBA60B344F168B4`

## 结论

**后端设计复审 PASS，0 阻断。** R4 留下的 provisioning 恢复耐久性问题已关闭：验证模板身份、长度和 SHA 后，必须对已验证 DB handle 与 case directory 各 Sync 一次并复核目录，成功后才能写 `ready`；FI-DB-01a 锁定了恰好一次的两个 Sync 及错误 oracle。

空库模板方案与代码边界匹配：仓库 `startupModels` 提供基础表，`pilotV2Objects` 是增量课堂 schema。文档要求离线合并基础 schema、Pilot V2 对象、索引/触发器和唯一 marker，固定模板字节 SHA 与 canonical schema digest；运行时只通过已验证 CREATE_NEW 文件句柄复制，不再以不完整的 pilot DDL 初始化空库。

恢复查询顺序和计数现已清晰：只读连接先执行 `PRAGMA database_list` 并校验 canonical path，再启用 `query_only`，之后只运行明确白名单。schema 验签与 receipt-first R-* 数据读取分属 `db_schema_validation_sql` 和 `db_recovery_data_sql`；模板复制、只读连接及应用 SQL另有独立计数与上限。SQL 投影、参数、行数和拒绝条件引用 docs/41 §70，恢复路径禁止 DB 写入、Planner 和 Registry 副作用。

生产 close error 统一为 unknown 并要求 parent harness 终止、确认 PID 退出后才可重启；catalog write/Sync oracle、取消/超时错误映射及日志 bootstrap/追加/容量边界也已收敛。docs/41 §112 的 driver file-ID 与 HMAC key 勘误和 docs/42 一致。

JSON 记录在 `review/pilot-supervisor-backend-r5-20261019.json`，已验证有效。此次仅为静态设计复审，**不代表 supervisor 代码、平台测试或隔离课堂试点已经完成**。
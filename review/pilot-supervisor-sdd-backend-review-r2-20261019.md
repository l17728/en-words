# Pilot Supervisor 后端域复审 R2

- 文档：`docs/42-Pilot隔离运行监督器SDD.md`
- SHA-256：`0D915174617EC8FE65AA42D2A2495554DD8FC7D03038404860AC65EAEE355650`
- **结论：PASS；此前 B42-01、B42-02 均关闭，新增后端设计阻断 0。**
- 仅静态复审；读取前后哈希一致，未修改被审文档/产品，未执行测试。前轮 FAIL 报告保留。

## B42-01 关闭证据

§3/§7 将 OpenPilotSupervisor 限定为 root、日志、控制锁准备及已有 catalog replay；catalog absent 时不创建 catalog、不分配 case ID。首个有效 CreateCase grant 在验证 peer、签名、epoch、scope、digest 和请求绑定后，持 bootstrap 锁创建首帧。首建完成后并发请求走后续 case 协议，避免构造器生成无 principal/request_id 的首个 case。

未知 create 结果废弃整个 run，与已绑定 case 的同 Gateway worker 重启分开；catalog absent 的 resume 须由 Gateway 证明从未 dispatch create。owner 与幂等映射均成功绑定后才可返回 selector。强制单一合法 Idempotency-Key、同会话同 key/digest 合并、跨会话隔离和错 scope/digest 拒绝已有明确 FI/E2E 映射。

## B42-02 关闭证据

§9.1 以固定 `ORDER BY id LIMIT 2` 查询发现唯一 lesson；0/2 行拒绝。R-TRANSITION 只以已发现 lesson_id 查询最多两条记录，成功 profile 恰一条，从该行取得 command_id 后重算 command_key_hmac，与已验证 sidecar 比对，继续验证 command/plan HMAC、fingerprint 和完整关联事实。该协议不要求从 HMAC 反推 ID，也不接受 HTTP 自报查询键。

冻结创建参照来自 checkpoint 的严格 codec/canonical fingerprints 与 reservation/lesson 的交叉核验；完整成功还需 sidecar 与 receipt/审计/outbox 对应。无 receipt profile 不需要虚构 command_id，仅检查 created 与所有后继事实不存在并返回 pending_unknown；不重规划、不重放 Registry。新增发现 SQL 已纳入白名单、计数及损坏用例。

## 非阻断编辑建议与实施门

§3/§7 的“按§28”应改为“按§3授权边界与进程协议”，R-TRANSITION 的“docs41 §43”应明确指实际 HMAC 公式所在条款，避免把段落/行号写成章节号。§10 概括表仍有“首个 case lock 无 catalog member”的旧表述，应明确该行只适用后续 case：首次 case 的首帧已存在。§7 已明确优先于概括锁叙述，实施必须沿首帧先于首次 stable lock 的新合同，不可据概括表实现另一分支。

这次 PASS 仅放行所述隔离监督器设计进入实现审查，不证明真实平台能力或运行效果。必须用真实 Windows/Linux job 验证文件系统/锁/父目录 Sync，固定 SQL golden 使用实际 SQLite 列名；首建并发、未知 create 废弃、worker resume、无 receipt 与完整 receipt 恢复、SQL 早停计数仍需真实测试。不得接通默认 App/Room/模型/音频或省略后续 E2E 门。

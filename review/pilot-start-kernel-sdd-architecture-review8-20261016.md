# Start 内核 SDD 第八轮定点复核

2026-10-16；只读版本 SHA256 `d95c19157b949330814adc00468b8ac270d6ecba653d760499aff1777f8cc9b0`。

结论：**PASS，本次定点复核0项阻断。** review7 的两项P2及所列实施同步项已闭合。未运行测试、构建或服务，未改代码/规格。

| 核查项 | 结果与证据 |
|---|---|
| START7-01 拒绝枚举 | §3 sidecar result_class已包含unauthorized、not_supported、schema_invalid及其它明确writer拒绝，事件可编码，不再要求把未知HTTP reason写入严格schema。 |
| committed无receipt | §3崩溃恢复表明确“合法committed事件无receipt为integrity_error”，不能当新命令、普通unknown或not_committed。 |
| START7-02 say/receipt | §4改为say payload.text对room_facts.teacher_text；receipt独立校验身份、版本及outbox_count，并从既有冻结事实、Start时间与效果重建canonical request fingerprint。没有新增receipt正文。 |
| case生命周期 | §5明确监督器创建/保留case并拥有清理；子进程不得删恢复数据，所有child退出且DB关闭后才清理。 |
| 两类锁 | OS独占锁覆盖case生命周期；append mutex仅保护seq/hash/append/Sync，不能跨SQLite事务；lesson锁与不同课并发边界清楚。 |
| 并发验收 | §6.1分开同lesson Coordinator排队与跨lesson独立pool writer竞争，去掉原不可到达barrier条件。 |
| panic边界 | §6.2区分SQL事务外Planner/handler panic和SQL callback panic，前者无生命周期写入，后者由事务边界回滚。 |
| cache预留 | §3明确attempt_started前原子预留32项容量，满时Prepare/Registry均不执行，不驱逐unknown。 |

本版同时调整了已提交数据的恢复优先级：先当前授权与DB完整lifecycle校验；有效receipt可在COMMIT后sidecar尾损坏时只读重放，case进入sidecar_degraded并禁止新命令；无receipt的坏sidecar仍失败关闭。此行为与旧版“坏sidecar无条件阻断receipt”不同，但已在恢复表、案例与E2E要求中一致表达，本次接受该明确且受限的只读恢复政策。不得将degraded视为允许修复日志或继续教学。

实现验收仍必须锁定：两个学生并发下sidecar链与各自receipt关联、各崩溃切点、真实COMMIT故障、严格codec、case降级后的新命令拒绝。本文只给上述设计修订通过结论，不代替代码评审/覆盖率/E2E，也不批准旧Room接线、outbox消费或真实模型/音频。

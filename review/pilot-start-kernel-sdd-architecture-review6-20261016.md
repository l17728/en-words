# Start 内核 SDD 第六轮架构评审

2026-10-16，只读；版本 SHA256 `4c18e16156f64bfa09d01bee7c32426ef8bf5fd345b2d54b6a0547cbf4b27a88`。未运行测试/编译/服务，未改规格和代码。

结论：**BLOCK，3项P2。** 持久 attempt_started 已覆盖上轮“工具执行中崩溃但没有尝试记录”的方向性缺口，但下列具体合同仍不能直接实施。

## START6-01：intent 指纹存在时间依赖环

§3 指纹包括 `say.text`，其值由 Planner 中新 Agent 渲染 API 产生（含模板缺失 fallback）。第42–43行要求 attempt_started 中完整 command_hmac 绑定此 fingerprint，Sync 之后才允许调用 Planner；第49行也确认这一顺序。因此首次请求在调用 Planner 前无法构造必填 HMAC。重启只存 HMAC 而不存候选时，若恢复请求仅带 command ID，也不能重新生成 say 来比对且不应重新调用 Planner。

最小修订任选其一并冻结：拆分“规划前可信输入的 intent fingerprint”和“规划后完整 bundle/result fingerprint”，attempt_started 绑定前者，bundle_ready/receipt 绑定后者；或明确把纯话术计算与 Registry 执行拆为两个阶段，完整候选在 intent 前形成，而只禁止 intent 前调用工具。两种方案均须让重启只读查询可凭 lesson+command 在当前授权后返回 receipt，不要求重做渲染；提交重放冲突检查与只读查询分开。

回归：首次命令、OP-1缺失 fallback、intent落盘后崩溃、无bundle重启查询、同command不同输入均走可构造的指纹链。

## START6-02：single-flight 键与保证矛盾

第49行优先合同明确按 `(lesson,command)` 加锁，末尾却保证“防同课不同command并行重复规划”。两个不同 command 会取到不同锁，可同时生成两个任务/工具bundle；最终 SQLite CAS 虽能保证只有一个持久Start，却不能满足零多余工具尝试的承诺。

如保留该承诺，键应为 `(case/store identity, lesson_id)`，覆盖预检、intent、规划、最终提交与本次resolve。同课后继请求等待后先读 receipt/active，不能先生成task。不同学生/不同lesson可并发预检/纯规划，只有各自最终写入被SQLite全库writer串行化；共享cache锁只能用于短临界区。测试需区分同Coordinator排队（不应等SQLite writer）与独立pool/coordinator竞争（最终CAS）；不能要求同一把single-flight锁下的第二请求同时进入writer等待。

## START6-03：sidecar 严格 schema 与恢复终态仍有冲突

第42行唯一字段白名单仅允许 `schema_version,event_seq,event_type,command_key_hmac,command_hmac,attempt_id,attempt_no,tool_name,result_class,event_time_ms,prev_event_hmac,event_hmac`；§4“失败工具尝试审计”又强制每条包含 risk、ActorType、task/ActorRole、TraceID、OK、TS/DurationMs、scope 等不在白名单中的键。严格实现无法同时通过两套验收。应统一一个带版本的事件schema，或显式分开两类记录，给出每种event所需/null字段及完整迁移。

还需在同一恢复表明确两条冲突分支：

- `committed` sidecar有效但数据库无receipt：应固定完整性失败，不能当新命令、普通not_found或not_committed。
- DB已提交，但写终态时留下坏/截断sidecar：当前一处要求启动先全量sidecar校验失败关闭，另一处说receipt优先恢复committed。请选择并明确可达行为；可保持case失败关闭且通过独立只读证据确认DB事实，但不能同时承诺该harness正常恢复UI。

末尾完整行截断也不能仅靠逐行HMAC链全部识别：有效前缀本身仍验签通过。若要承诺检测尾部整行删除，需要监督器保留可信head/长度锚；否则应明确仅检测中间删改/坏尾行，不能宣称任意删除都能发现。不建议为本测试slice无界扩展日志系统。

## 已通过与实施注意

- case目录/SQLite/key由监督器创建并跨子进程重启复用；子进程不重置key，边界方向正确。§5仍写“harness每次创建/失败清理”，应与监督器拥有case生命周期同步，子进程异常不得删除待恢复证据。最终清理由监督器确认全部子进程/DB句柄关闭后执行。
- 事务外纯Planner/Registry、sidecar Sync；writer内身份/版本/receipt二次校验与原子写入，解决文件I/O占writer的问题。不能声称普通context能取消阻塞Sync。
- receipt命中优先于旧task expiry/昵称变化，当前账号/owner仍须核验；未知查不到不判rollback。此规则通过，损坏case例外按上项单独收敛。
- 全库完整性与逐课created/active分派、全局孤儿校验及 A active+B created 矩阵通过。ordinal按学生owner的reservation集合统计，A活动不应排除B首课，文本和用例已明确。
- case级cache32项且不淘汰unknown通过；需在attempt_started/Registry前原子预留容量，避免不同课并发超过32后生成无法保存的bundle。不要持cache全局锁等SQL或Sync。
- active State/room_facts、两工具两审计两outbox、时间字段与DDL仍无新增冲突。

本轮是架构合同审查，不证明实现、E2E、真实模型/声学或旧Room接线可用。

# Start 内核 SDD 第五轮架构评审

2026-10-16。只读复核 docs41 的生命周期拆分、writer/single-flight/bundle 与 sidecar 合同。末次读取文件 SHA256：`bbb35b61c06349788a363cc5ef8d010ad4389fbb2b18d4c3a89ec5bc52719217`。未运行测试、构建、服务或修改规格/代码。

结论：**1 项 P2 恢复合同阻断；全局审计拆分通过。**

## 已闭合

- START4-01：数据库级 reservation/state/scope/FK/孤儿完整性检查与 lesson-scoped created/active verifier 已分开，不再逐课调用原全库 created-only 函数。A active+B created、B nonce replay、B Start 的明确矩阵覆盖原冲突。全局 answer_claims/active_commands/completion_jobs 仍为空，不能漏过孤儿或未知生命周期。
- START4-02 的执行时点和同进程所有权部分：首次 Registry 明确位于 writer 内，且仅纯本地 bounded handler；single-flight 覆盖生成、提交、resolve；缓存深拷贝及 digest、容量32、同 task/budget 不重生成，receipt replay 不调用 Planner。身份重核不沿用旧请求取消态，已提交 replay 不受后续昵称变化影响。
- 两工具/两 audit/两 outbox、active machine/room_facts 与时间字段映射保持一致。Registry 的 ok、receipt 的 queued、outbox pending 正确分账。第三轮计数 P3 已关闭。

## START5-01：重启后无法据现有记录区分新命令和已尝试但丢失的命令（P2）

§3 的优先补充要求：重启后无 receipt 且 bundle 丢失必须 pending_unknown，禁止重新调用 Planner；同时 cache miss 的新命令又允许调用 Planner。当前 sidecar 记录完成/失败尝试，但没有覆盖“Planner 已开始、进程在完整 bundle 或 terminal_failed 落盘前退出”的持久前置事实。

可重现时序：旧进程收到 C → Registry 首次调用开始 → 子进程终止，尚无 receipt/bundle/terminal_failed → 新进程收到 C。若仅按 receipt/cache/sidecar miss，C 与从未执行的新命令不可区分，可能再次调用 Registry，违反本片单次生成合同。

最小闭合可不加产品表：由当前进程受信协调器先生成并登记命令身份，只有本进程登记的命令可以首次进入 Planner；外部请求不能凭空登记任意旧 ID。重启后不在本进程登记的 ID 仅做授权 receipt 查询，无记录返回 pending_unknown。显式新决策必须重新由协调器生成新 ID。或者定义先于 Planner 持久化的 attempt-start 标记及恢复规则，但不要以已有失败终态 sidecar 冒充覆盖了该窗口。

测试应在 Registry 第一次调用中止子进程，确保没有终态 sidecar/receipt，重启后原 ID 的 Registry 调用仍为0；当前进程新登记 ID 正常运行。单纯手工构造“已有 terminal_failed”不能锁此缺口。

## writer 与审计实施约束

1. SQLite writer 是整个数据库的写锁，并非 lesson 独占锁；同 DB 的 B 写操作会等待。single-flight 只锁相同 command，不能拿全局 cache mutex 跨 SQL/handler/resolve。
2. sidecar 的 Flush+Sync 是文件 I/O，不属于纯工具 handler。应在 SQL 事务完成回滚/提交并释放连接后写出内存审计摘要、再响应请求；不得在仍持 writer 的 panic/失败分支等待磁盘 Sync。普通 context 不能保证正在运行的 Sync 被取消；由 owned harness 外部终止预算留痕，不能宣称硬实时有界。
3. cache 容量必须在首次调用 Planner 前预留，满32项时固定拒绝，不能执行完才发现无处保留 bundle，也不能逐出 unknown 条目。缓存证据不得跨其他协调器实例自动视作共享。
4. 全局 audit 孤儿检测应明确限定本协议或本隔离库；不能把不相关的既有工具 audit 当本课后继事实。active verifier 必须继续核创建冻结内容，不仅检查两条 outbox 数量。

文字同步：§1 仍称规划在事务外，与后文优先合同的 writer 内规划冲突，应原位同步；“工具参数、审计都携带释义”与 index-only 参数/summary audit 不符，应限定为冻结 effects context 与 outbox。上述不要求扩大功能。

以上为设计结论，不是实现或运行验收；共享旧 Room/写入口、媒体/模型、真人课堂仍不在范围内。

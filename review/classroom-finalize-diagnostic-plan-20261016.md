# SUB02 课堂事务收尾长尾：证据与一次限定诊断方案

状态：只读定位与待主审的测量方案；尚未修改产品、执行新业务或调整同步可靠性/等待门。

原证据：`web/.test-artifacts/closure-1792127091876-18588/browser/recovery-regression-SUB02--6d947-nd-invalidates-old-attempts/server.log`，同目录 `trace.zip`。候选 binary 为冻结 `9496f4e1…`。日志第 70 行收到 `tts_finished`（13:07:16.668），第 71 行 `classroom.start_repeat` 提交成功（13:07:38.348），`transaction_ms=21678`、`finalize_ms=21678`，其余 begin/config/audit/agent-lock 指标为 0。0 表示毫秒精度下的结果，不等于没有审计 INSERT。之后 phase 改为 repeating，第 73 行 snapshot 保存于 38.504。

## 计时边界核验

- `server/internal/api/agenttransaction.go:58–88`：finalize 起点是事务 callback 的 defer，终点是 `gorm.DB.Transaction` 返回。它不是整个课堂耗时，也不是纯 fsync 时间。
- 本次 outcome 为 committed/ok。GORM v1.31.2 的成功分支在 callback 返回后执行 `tx.Commit()`；没有走失败备用审计。失败时同字段也可能含 rollback，不能在所有报告中改名为 commit。
- `database/sql.Tx.Commit` 包含关闭读锁等待、driver connection 锁、驱动 Commit、prepared statement 清理与连接归还。因此当前证据尚不能仅凭 finalize 区分这些子阶段。
- glebarez/go-sqlite v1.21.2 `sqlite.go:714` 的驱动 Commit 使用 `context.Background()` 执行 `sqlite3_exec("commit")`。事务开始前的 context 检查不代表驱动 COMMIT 中途可按原请求 deadline 中断；不能用 busy_timeout=5000 推导整个 COMMIT 必小于 5 秒。
- `teachereffects.go` 的 start_repeat handler 校验冻结 action 并返回 action，无模型/媒体网络调用；它及 config 读取、audit INSERT 都在 callback 内。审计 MemAuditor 和 `tool_called` 的日志输出在 Transaction 返回以后，不能解释本条已测得的 21678ms finalize。日志输出仍可能另加后续延迟，不能泛称所有 stdout 永不阻塞。
- `startup.go` 设置单连接池、WAL、busy_timeout=5000、foreign_keys=ON；此为配置源码，不是本次活连接的 synchronous/wal_autocheckpoint 实测。`atomicConnection` 的失败 COMMIT 清理不在当前 callAgentTool 路径中。
- Room 锁和 agentWriteMu 覆盖该调用，故收尾等待会向当前课堂/其它依赖写工具传播；本条 lock_wait_ms=0 仅排除进入 agentWriteMu 的等待。重连后第二条 start_repeat 在 13:07:39.053 完成，属于迟滞后恢复，不是首条 finalize 中重复执行两次工具的证据。

## 最小可执行测量（待批准，仅一次 SUB02）

1. 保留旧失败和冻结 binary；以同 D 盘、同 fixture/payload、同 15 秒 UI 门运行一次 SUB02，不新增并发业务。不改 WAL/synchronous/busy_timeout、不调用 WAL checkpoint、不迁移 DB 到 C 盘。为避免“只重跑一次恰好绿”不能定位，先建立以下隔离诊断构建；该构建不替换冻结发布产物。
2. 主审采纳更窄方案：不包装 driver，不改变可选接口/取消路径；仅隔离副本插入首次 `classroom.start_repeat` 的内存采样。driver 内外分界若仍有必要，另行设计，不在本轮实现。
3. 在 owned 进程内总计最多 10 个样本，每次采集结束后再等至少 1 秒：只保留相关 goroutine 函数名（不保存参数、完整栈正文）与 DBStats。原始栈仅内存解析，限 512KiB，截断明确标记。首次交易完成或样本到限后一次写独立文件，避免逐样本磁盘写；限 10 次是为尽量早于 15 秒失败后的进程清理保留证据，仍不保证覆盖 21 秒长尾全程。每次采样开销单独记录。栈采样会扰动调度，因此仅为诊断，不作为最终性能验收。
4. 同一 owned 进程建立连接后、正式操作前，读取实际 pool 连接的 `journal_mode/synchronous/busy_timeout/wal_autocheckpoint`；不以另一 SQLite 连接查询替代，也不宣称后续一定没有更换连接。采样时调用 Win32 `GetProcessIoCounters(GetCurrentProcess())`，只取 owned 进程计数。本轮不调用 CIM，D 盘队列/延迟记 unknown。若后续获准外部指标，总计不得超过 30 次且间隔至少 1 秒；进程退出记缺测，不追其它 PID，不输出命令行/环境。
5. 若 driver Commit 明确占满长尾且栈停在同步写，也仍只获得同步路径关联，未量出单次 Windows FlushFileBuffers 时长。只有可用且获准的文件 I/O tracing 能进一步证明具体 owned DB/WAL 的 flush、锁等待或其它进程占用；本轮不安装工具、不启用全机无界追踪，不据此推断 AV 或硬盘根因。

输出：原始固定摘要、各阶段单调时间线、诊断源码/二进制 SHA、样本缺失率、原测试结果、owned 清理证明。失败、未复现、测量工具不可用均保留，不能自动重试扩大样本或放宽门。若只证明 COMMIT 长尾而未定位子因，结论必须继续标“原因待定”；新库 fresh 初始化优化与本次热事务长尾分账。

浏览器已于本次调查后明确清理 owned 进程并释放窗口；尚未使用该窗口执行上述方案，等待根因测量设计评审。

主审已批准窄方案，架构实际审查插桩后要求固定终止原因改为 `tool_call_finished`：defer 位于整个 callAgentTool 返回处，可能含提交后审计转发和日志，采样必须与既有 finalize 时间线对齐，不能将所有采样都归于 COMMIT。10 次后的一次文件写也可能与尚未完成的 COMMIT 重叠，这是诊断扰动而非无侵入性能基准。runner 必须确认样本文件存在且为完整 JSON；缺失记测量失败，不凭用例绿色宣称诊断成功。

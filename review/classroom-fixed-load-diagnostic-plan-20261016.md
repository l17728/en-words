# 课堂持久工具固定负载诊断（待评审执行）

目的：定位已反复出现、尚未关闭的热事务收尾长尾。历史 G09 `classroom.say` finalize 22.594 秒见 `duplex-browser-g1-review-20261015.md`；本轮 SUB02 `classroom.start_repeat` finalize 21.678 秒。二者同属成功事务收尾延迟，尚不能认定共同物理根因。单次 SUB02 诊断仅得到 70ms、0 慢样本，不能关闭此问题。

## 固定负载与应用路径

建议一个隔离 **package api opt-in 测试 harness**，编译为独立测试 binary，使用 D 盘全新、独占目录和数据库。比再启动 HTTP/浏览器 server 更窄：直接调用真实 `App.callAgentTool`，不替换成裸 SQL，也不模拟成功审计。无模型、音频、字幕或外部网络。

预声明 **50 次顺序 start_repeat 工具调用**，每次成功后按原课堂顺序再调用一次真实 snapshot 持久化，两个阶段分别计量；正常情况下为 50 条工具审计和 50 次 snapshot 保存。每轮使用真实 Room.mu 外层锁、真实 agentWriteMu、可信 newAgentTaskContext、withTeacherEffects 冻结的合法 ActStartRepeat 与真实机器 snapshot，走注册表 scope/schema/handler/MemAuditor→persistToolAudits→原 GORM.Transaction。不得手工拼造 agentTask 绕过授权，也不使用旧 timing 测试里的 app.DB 后装混合内存仓储方式。

初始化全部使用 `store.OpenContext` 和真实 `NewApp`、持久学生/词库/课数据；创建一个合法 Room 后停止在测量所需状态。不得启动周期 Timer、真实媒体或调度并发。重复调用同一合法冻结 effect 是工具事务负载，不冒称 50 个真实课堂机会或完整状态迁移；snapshot 使用同一合法状态，可反映重复审计+兼容快照的持久成本，但与原课堂事件频率/内容大小不同。不能据其分位数给产品 SLO。

首次校验实际连接 PRAGMA 为 WAL/synchronous=2（FULL）/busy_timeout=5000；记录 wal_autocheckpoint。若不符直接停止，**不改 PRAGMA 来使环境符合**。后续每轮不再取 pool.Conn，避免探针争用唯一连接。固定 50 分母不因某几次快慢增减、重试或重新建库。

## 预算与失败账

总预算 **180 秒**，从父进程启动已编译的 harness 计时，包含初始化与取样，编译另计。每轮开始前检查剩余预算；同步调用中的 COMMIT 可能不响应 context，父进程到限仅终止本次 owned 子树，不能再等无限后台 goroutine。原生产超时、driver、同步可靠性全部不变。

为保证到限被终止仍有证据，子进程只向父进程匿名管道输出有界结构摘要，父进程持续读入内存（上限 8MiB），结束或总预算触发后一次落盘。不是每轮写磁盘；管道传输位于测量区间之外，其耗时另计。原 obs 日志写入有界内存 sink，不打印正文/SQL，不把 stdout 文件日志引入 COMMIT 计量。

每轮先发 started 摘要，完成后发 finished 摘要。若到限只知 started，记 `in_flight_outcome_unknown`，不能记 rolled_back 或成功；保留 completed、failed、in_flight、not_attempted，合计必须为 50。首个业务/存储错误即停止后续调用，不偷偷恢复重试。初始化失败为 50 未尝试。预算后如需只读计数须另列事后核验，不把 SQLite 恢复打开副作用称原事务证据。

工具返回即单独发 `tool_outcome`，随后才执行 snapshot。tool 已提交但 snapshot 失败/到限时，保留 `tool_outcome=committed` 与 `snapshot_outcome=failed/unknown`；外层轮次失败或在途不抹掉已知提交。末核 audit 增量按已知工具提交数计算，扣除初始化审计基线，不按完整轮数计算。50 轮 finished 但末核超时，必须记 `workload_complete=true`、`validation_complete=false`，不得宣称整体诊断有效。

父管道累计超过 8MiB 时记固定 `diagnostic_overflow` 并终止 owned 子树，继续丢弃式 drain 到有界退出或关闭管道，不让子进程阻塞，也不把截断摘要冒称完整。日志内存 sink 同样有明确容量/overflow 状态。阶段 hook 与采样状态使用 mutex/atomic 同步，只观察，不引入数据竞争；采样 goroutine 退出和父进程清理均单列核验。

## 每轮计量与有界慢栈

| 指标 | 方法及边界 |
|---|---|
| 工具阶段 | 原 lock_wait/begin/config/audit/transaction/finalize；隔离窄 hook 增加相同边界单调纳秒值和当前阶段标签，不包 driver，不改提交逻辑 |
| DBStats | 轮前/工具后/snapshot 后读取同 pool 的 Stats，记录 WaitCount/WaitDuration/InUse/OpenConnections 差；不发查询 |
| 自身 Win32 IO | 同三点 GetProcessIoCounters 当前 PID，记录操作/字节累计值和差值，不能把整个进程 IO 等同 fsync 次数 |
| WAL 文件 | 同三点仅 stat 已知 owned `db-wal` 大小，记录不存在/失败为 unknown；不读正文、不锁文件、不运行 wal_checkpoint(PASSIVE) |
| 慢栈 | 当前工具阶段连续超过 1 秒才采，间隔至少 1 秒、全程最多 30 次。512KiB 原始栈仅内存，输出固定相关函数名/阶段/单调时间/采样耗时/截断标记；不保参数与路径 |
| snapshot | 单独 duration/outcome/IO 差，不能混入工具 finalize，不能虚称每轮只一笔数据库事务 |

WAL 文件长度是分配/保留的大小，不能等同本轮新增帧、有效提交帧数或已触发 checkpoint。文件 stat、Win32 调用和栈采样均有扰动：轮前后探针在主工具计时之外；慢栈会暂停/争用调度且发生在慢阶段内部，需报告最大/累计采样耗时。单线程固定负载不测其它学生延迟，不设伪并发性能结论。

## 最小实现切片与判读

只在新 `.test-tools` 隔离副本新增 opt-in `_test.go` harness、函数名采样 helper，以及 `agenttransaction.go` 的只观察阶段 hook（callback/finalize 起止，nil hook 默认无行为）；不替换 driver、不编辑正式 server。父进程 runner 固定一次执行、目录拒绝覆盖、记录原/诊断源码与二进制 SHA、完整数据分母和 owned 清理。正式跑前只做 helper 的摘要脱敏、预算/上限和 started 未 finished 归类短离线测试。

正常收尾检查数据库 audit 行数与成功调用相符、snapshot 最终存在、任务来源与 trace 对齐；检查在 180 秒剩余预算内完成且单列耗时，不能扩大总预算。若对应审计错误或数目不符，诊断有效性失败，不用于速度结论。

判读只允许：慢栈落在 sql.Tx/driverConn 锁可支持 Go/连接锁等待线索；落在 SQLite COMMIT/WAL/checkpoint/系统同步函数可支持驱动内路径线索。没有 syscall 单次时长或 OS 文件跟踪证据，不能声称已量出 fsync/AV/设备故障。两次约 20 秒与 busy_timeout/autocheckpoint 的关系均待证；不得按相近数字推导重试次数。若固定负载未复现，完整保留 50 次结果，不追跑，原长尾仍未关闭。

本文件仅设计，等待架构实际审查与主审执行批准。

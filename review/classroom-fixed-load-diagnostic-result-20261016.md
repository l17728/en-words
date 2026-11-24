# 真实课堂工具固定负载诊断结果

**唯一一次固定负载已完成，未复现长尾；历史 G09 22.594 秒与本轮 SUB02 21.678 秒收尾延迟仍未关闭，物理根因未知。** 本报告不是产品 SLO 或完整课堂验收。

按主审批准和架构实际代码复审后的方案，在 D 盘独立 DB、原驱动和 WAL/FULL 下，执行 50 次顺序 `classroom.start_repeat` 真实 callAgentTool，每次成功后调用真实 snapshot 保存。总预算 180 秒，没有换盘、降低同步、修改等待门、重试或追加负载。最终 50 完成、0 失败、0 在途、0 未尝试，owned 进程退出 0，总 4.453 秒，初始化 0.901 秒，末核 4.712 毫秒。

## 阶段测量

| 指标（毫秒） | 样本 | 中位 | p95（nearest rank） | 最大 |
|---|---:|---:|---:|---:|
| 工具整个调用 | 50 | 26.452 | 125.075 | 155.496 |
| 事务 | 50 | 25.5 | 125 | 155 |
| finalize | 50 | 24 | 124 | 155 |
| begin | 50 | 0 | 0 | 1 |
| config load | 50 | 0 | 1 | 2 |
| audit INSERT | 50 | 0 | 2 | 7 |
| agentWriteMu 等待 | 50 | 0 | 0 | 0 |
| snapshot 独立阶段 | 50 | 0 | 3.075 | 54.998 |

0 是时间计量/取整结果，不等于没有调用或没有写。finalize 最小 8ms，累计 1863ms；它包含 callback 返回到 GORM.Transaction 返回期间的收尾，不是直接量出的 fsync 时长。没有阶段超过慢栈阈值，函数名栈样本 **0**，不能凭无栈断言原事件不存在锁或 I/O 问题。

实际初始化 pool.Conn 读到 journal_mode=wal、synchronous=2、busy_timeout=5000、wal_autocheckpoint=1000；没有修改值。连接池 MaxOpenConnections/OpenConnections 均为 1，轮前后 WaitCount/WaitDuration 都为 0。这只说明该串行负载中没有观察到池等待，不排除原浏览器负载的不同并发条件。

WAL 仅文件 stat：首轮前 358472 字节、末轮后 997072 字节。未运行任何 wal_checkpoint，不读 WAL 内容；长度不等于有效 frame 数、checkpoint 触发次数或当时正在同步。不能将两次约 20 秒与 busy_timeout/autocheckpoint 建立未经验证的因果关系。

同 PID Win32 IO 轮前后累计差：WriteOperations 458、WriteBytes 720431、OtherOperations 200，ReadOperations/ReadBytes 为 0。它们是整个进程计数，包含诊断匿名管道等 I/O；**不是 fsync 次数、单个 WAL 的物理写量或设备延迟**。每轮工具和快照各自的前后差在 JSON 单独保留。没有 D 盘队列、文件系统 syscall 或 AV 归因证据。

## 真实路径与有效性

使用真实 store.OpenContext、NewApp、持久学生/词库/卡/课和 Room；没有把内存 App 后装一个 DB 冒充持久应用。每次在 Room.mu 内创建合法可信 task、withTeacherEffects，再通过原注册表/授权/schema/handler/agentWriteMu/持久审计事务调用。无网络、HTTP、Timer 或真实媒体并发。

50 次工具分别拥有新可信 task ID，但共享同一个 180 秒总 context；newAgentTaskContext 不另开 8 秒预算，也不按轮次重置总期限。末核得到新增 audit **50**，逐条 tool/actor/role/trace/OK 符合，且工具日志与 50 个可信 task、同课 trace 对齐。AuditLog 本身没有 task ID 列，不能声称数据库内有逐条 task 外键。最终 snapshot 的完整序列化内容与最后合法机器状态相同，不只是验证 Enter 时已有那一行存在。

所有 snapshot 使用同一个合法 repeating 状态，未伪造 50 次相位推进。重复 upsert 可能没有真实状态变化相同的脏页/同步成本，snapshot 中位为 0 更不能解释为课堂保存已无成本。本负载主要验证每次新增工具审计的真实提交路径；固定密集调用的频率、状态和审计内容与原 SUB02/G09 不同。

探针包含每轮 DBStats、当前进程 Win32 IO、WAL stat，以及隔离源码中的 4 个只观察阶段 hook。慢样本向父进程匿名管道发送摘要可能争用调度/输出锁，capture_ns 不包含全部管道耗时；此次慢样本 0，不应把该潜在扰动泛化为不存在。普通摘要在工具调用计时之外传输。父进程内存聚合，上限 8MiB，实际接收 83081 字节，解析错误 0，最后一次落盘。

## 代码审查、回归与产物

架构指出并关闭的测量缺口包括：launch/reader/taskkill/wait 异常仍保留摘要与有界清理；missing binary 的 hash 读取不再二次抛错丢报告；tool 已提交但 snapshot 未完成独立记账；末核不满足即 validation_complete=false。这些是诊断工具可靠性修订，不是课堂产品性能修复。

Python helper 最终 **6/6** 通过（0.090 秒），包括未完成分母、部分提交、快照失败、启动失败、taskkill 超时及 missing binary。Go helper **3 个子例**通过，覆盖栈参数/路径脱敏、日志缓冲上限、阶段状态副本；最后 helper 阶段 35.656 秒（含编译），测试 binary build 26.719 秒。首次 helper/build 的通过记录保留为 r1，新增末核后的最终记录另存；50 轮负载只执行了一次。

诊断 binary SHA256：`ee301db6f5eed1f87e76c9ae29a0feb041b20335686485a365103dc62babfa4f`。

原件目录 `.test-artifacts/fixed-load-diagnostic-20261016/`，包含唯一 `run-reserved.json`、`run-summary.json`、`run-output.txt`、初始化输入哈希和 helper/build 证据。[结果 JSON](classroom-fixed-load-diagnostic-result-20261016.json) 保存 50 轮阶段值/IO差/DBStats差/WAL大小、诊断源码与原件 SHA。临时 runner、helper 和隔离副本均在 `.test-tools`，不冒称 clean checkout 已包含正式诊断模块。

生产源码字节前后相同。期间根目录提交 e1da932 只改变 git 元数据，不能据新 HEAD 改写既有来源或宣称该提交全部验收通过。测试 session 41034 已结束，owned PID 17948 已退出，reader 完成、cleanup_errors 为空；本 harness 未监听任何端口。窗口已交还，停止追加测量。

下一步仍需基于已有失败选择有针对性的取证方案或产品规格评审。此次固定负载未给出 OS 同步与 Go 锁/调度之间的慢事件分界证据，不能以这 50 次正常结果消除原长尾，也不能据此调整可靠性配置。

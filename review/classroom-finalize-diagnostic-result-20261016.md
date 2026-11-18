# SUB02 单次收尾诊断结果

结论：**未复现长尾，原 21.678 秒失败仍未关闭，根因待定。** 本次不是最终验收替代，也没有据此批准改持久化策略或等待门。

主审批准、架构实际审查后的唯一运行已完成：SUB02 fallback 1/1 通过，用例 10.4 秒，Playwright 51.0 秒，外层 runner 55.828 秒。`classroom.start_repeat` 收到事件于 13:22:16.960，提交日志于 13:22:17.032；`transaction_ms=70`、`finalize_ms=70`，begin/config/audit/lock_wait 均为毫秒取整 0，outcome committed。snapshot 随后在 17.105 保存。

首次 start_repeat 在首个 1 秒采样点前结束，`owned-samples.json` 完整但 `samples=[]`、终止原因为 `tool_call_finished`。因此本次**没有慢阶段函数栈或进程 IO 样本**，不能据空样本断言无锁等待、无 fsync 或 I/O 正常。没有重复运行以追求复现或绿率。

启动后、业务前实际 pool.Conn 的只读值：journal_mode=`wal`、synchronous=`2`、busy_timeout=`5000`、wal_autocheckpoint=`1000`。这不保证稍后没有更换物理连接，也没有量出原失败时的 flush 耗时。D 盘队列/延迟未采，明确 unknown；没有 CIM 高频采集或共享进程检查。

## 输入与隔离边界

- 诊断 binary SHA256：`690c6cb9389bacc1b32ed572cd48314da444e45bb48d9febc16760c954a69c6c`。仅 `.test-tools/finalize-diagnostic/server` 中 main 增加启动 PRAGMA、agenttransaction 增加窄采样入口，以及新增诊断 helper/test；正式 `server/` 前后哈希一致。
- 复用当前 dist，前后文件哈希一致。保持 D 盘 fixture DB、原 SUB02 内容、单 worker、零重试、原 15 秒断言、18081/15174 独占端口。仅诊断 config 将原构建+预览改为预览已核 dist，避免再构建；这与失败 batch 的前置负载安排不同，不能作为严格性能 A/B。
- 无 driver 包装；无 WAL/synchronous/busy_timeout 修改；无模型调用、真实音频或额外并行业务。
- 插桩最多 10 次，采完后等待至少 1 秒，仅内存解析完整栈后保留函数名；函数参数和文件路径不落盘。调用结束或样本到限后一次写诊断文件，若调用仍慢则可能与 COMMIT 重叠产生扰动。已有 finalize 时间不包括日志输出，但采样终止覆盖整个工具调用，二者不能混称。
- 两条 helper 离线测试通过（函数名提取不含参数/路径、无关栈排除），Go 包体 1.529 秒；测试启动/编译阶段 47.281 秒，独立 build 28.969 秒。这些不是产品响应时间。

## 证据与清理

目录 `.test-artifacts/finalize-diagnostic-20261016/` 保留 `sub02.log`、`results.json`、`owned-samples.json`、`execution-inputs.json`、`execution-summary.json`、helper/build 日志与二进制。具体服务日志在 `browser/recovery-regression-SUB02--6d947-nd-invalidates-old-attempts/server.log`。

[机器记录](classroom-finalize-diagnostic-result-20261016.json) 含诊断源码 SHA、binary、各证据 SHA、工具阶段时间及输入不变核验。runner 标记 diagnostic_complete 仅表示 JSON 完整，不表示取得长尾根因样本。临时构建/执行辅助在 `.test-tools`，未作为可独立 clean-checkout 的正式工具交付。

session 12213 已退出 0；owned PID 17684 已退出；只读检查 18081/15174 均无监听。没有剩余诊断负载，重测试窗口已释放。原始失败仍保留在此前 SUB02 目录，进一步测量或产品修改须新的明确方案，不自动重跑。

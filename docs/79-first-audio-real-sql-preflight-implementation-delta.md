# 首音真实 SQLite PREFLIGHT 实现差异记录

## 范围

本记录跟踪 `docs/78-first-audio-real-sql-preflight-amendment.md` 中真实 SQL PREFLIGHT 的实现证据。当前正向集成切片覆盖三条同时 pin 的物理连接；原规格保持不变；本记录不扩大到 `JOURNEY`、浏览器验收或课堂验收。

## 已实现与验证

- 在隔离的 SQLite 文件上播种合成用户、词库和课程，安装固定启动拒绝触发器及三条课程探测触发器，并保存完整数据库快照。
- 通过真实 mattn SQLite/GORM 连接池启动 guarded `api.NewApp`。controller 顺序经过 `DRIVER_PROBE → STARTUP_PROBE`，启动事务完成后才固定唯一物理连接；启动后快照与准备快照一致。
- startup permit 仅包围同步 `NewApp` 调用，并要求底层 authorizer 恰好观察一次 BEGIN 和一次 COMMIT/ROLLBACK；startup permit 之外的事务元组拒绝。
- `PREFLIGHT` 中仅用一次性 permit 读取快照、开始事务、消费同连接挑战、读取 18 列类型化课程投影、执行固定 `id=id` 更新并验证恰好一次 OLD/NEW 回调，最后显式回滚并核对完整快照哈希和规范内容。
- PREFLIGHT context 显式固定逻辑或物理 probe 模式；receipt 注册时必须与该模式一致，clone/publish 会保留该标记，防止把未执行物理读回的逻辑 fixture 当作真实候选。
- tracked driver 为每次成功 `BeginTx` 分配递增 epoch；token/UDF 与 row-guard 回调必须匹配同一活动 epoch。专用 cleanup 方法持有唯一 `sql.Tx`，回滚后验证 autocommit 和 inactive；无法确认时通过 `driver.ErrBadConn` 驱逐物理连接。
- receipt 状态依次经过回滚确认和快照读回确认；`completeFor` 只接受读回终态。真实 SQL challenge 从创建时固定要求物理读回，只有 authorizer 的真实快照比较路径可完成它；该路径绑定前后快照摘要、challenge 事务 epoch、原物理连接及 registry 活性。纯状态机 fixture 使用分离的测试辅助路径，不能完成物理 challenge。typed REAL 比较按 IEEE-754 位模式处理，覆盖 `+0` 与 `-0`。
- 真实连接的读回终态 CAS 在 registry live lock 内完成；后续发布 JOURNEY 时，controller 会再次验证所有真实 readback 连接，并在发布上下文期间保持注册表锁，防止 retire 与发布之间竞态。
- schema manifest 读取保留 SQL NULL 的规范化语义；仅接受三个经专用 DDL 校验的 `fa_lessons_guard_*` 触发器，额外同命名空间触发器会被拒绝。
- 快照规范中的表列集合与批准的 schema 读取列清单做精确比较，锁定缺列、额外列和漏表。
- 作者化器拒绝元组由确定性用例覆盖；捕获器注释明确其总是放行、不得作为安全策略安装。

验证命令（Windows CGO）：

```powershell
go test ./internal/api -run '^TestFirstAudio' -count=1 -timeout=5m
go test ./internal/api -count=1 -timeout=10m
```

截至 2026-10-28，针对本功能的 `go test ./internal/api -run '^TestFirstAudio' -count=1 -timeout=5m` 通过。全包 API 回归及独立架构、安全评审须在本次最终改动后重新执行并记录；此前通过的回归不覆盖后续改动。

## 明确未验收

本正向切片不证明 §5 真实 SQL 负例隔离子进程、取消/超时与 drain 竞态、失败回滚/连接销毁注入、双进程 Playwright journey、跨复位 stale callback 隔离、性能矩阵或真人课堂效果。不得据此标记首音课堂矩阵通过。

## 三物理连接正向 PREFLIGHT 增量（2026-10-28）

`TestFirstAudioMatrixRealSQLProbeTransactionAndReceiptUnderExactPermits` 现在固定池容量为 3，并在启动前及 PREFLIGHT 前分别同时 pin 这三条连接。测试从每个 `Conn.Raw` 获取 tracked raw 指针，要求注册 ID 唯一且 hook 注册总数恰为 3；每个 ConnectHook 创建独立 authorizer、session、permit 和连接级 UDF。NewApp 的 startup permit 按连接实例分别开启并汇总断言恰好一组 BEGIN/结束事务。

PREFLIGHT context 一次发布三个 connection-bound challenge/contract/receipt。协调器逐连接消费各自 token、读取固定投影、执行唯一 no-op UPDATE、显式回滚并在同一 pinned raw 连接完整读回 snapshot。每个 receipt 必须到达物理 readback 终态；所有探测后重新核对全库 setup baseline，释放 pins 后再次确认相同 raw/ID 仍在 registry。TX_CLEANUP authorizer tuple 同时要求本地注册 ID/raw、活动 session、tracked 连接及事务 epoch 一致；token slice 在创建挑战前注册统一清理。

提交 `f1db42e` 的 CI：[Linux/Windows 控制协议检查](https://github.com/l17728/en-words/actions/runs/40635891277)。工作流还包括 CGO API 定向回归、Linux race、vet、Web build/typecheck 与生产符号检查。CI 通过只证明该工作流内检查通过，不代表 docs/78 §5 完整负例、JOURNEY、Playwright 双进程旅程或性能/课堂验收通过。三领域评审记录见 `review/first-audio-real-sql-preflight-multiconnection-review-20261028.md`。

## Review 与后续

实现评审完成后记录评审者、结论、问题及关闭证据。下一阶段继续依照 docs/78 的失败候选清单接通隔离候选 harness，再逐项完成数据库双进程旅程与性能矩阵；真人音频仍须等加密链路、服务端音频协议和课堂授权条件满足后单独验收。

# 双 Pool 启动竞争测试断言调整规格

## 背景与证据

`TestStartupTwoIndependentPoolsNeverPublishPartialSchema` 加固后，定向 `-race` 运行越过双方 `schema_probe` 屏障并收回两个 worker，但因新增断言只接受 `ErrInitializationConflict` 而失败。原始日志未打印败方的固定错误分类，不能断言本轮具体发生了哪种 SQLite 错误。`docs/37-新库启动性能规格与SDD.md` §3/§4 明确允许写升级 `BUSY`/`SQLITE_BUSY_SNAPSHOT` 返回初始化冲突或存储错误；当前测试断言窄于该已评审合同。

## 目标与边界

调整测试断言和失败诊断，不改生产启动逻辑、超时、事务、Schema 或错误映射。测试仍须证明：两个独立连接都观察到空库；最终且仅有一个 `OpenContext` 成功；失败者仅可为 `ErrInitializationConflict` 或本项目实际 SQLite driver `github.com/glebarez/go-sqlite.Error` 的 BUSY 主码家族（`Code() & 0xff == 5`，包含 `SQLITE_BUSY_SNAPSHOT` 517）；`SQLITE_LOCKED`、取消、constraint 与普通错误仍失败。不得改用不同 driver 的同名 Error 或接受任意带 `Code()` 方法的错误。成功库及重启恢复库均须与独立建出的完整 Schema 等价；所有 worker 和 pool 都被回收。

## 诊断与敏感信息

所有终态断言必须附 worker ID、固定错误类别、最多 64 个 `stage:event:reason:duration` 摘要和溢出标志。只输出已枚举错误类别，不输出原始 driver 错误、SQL、数据库路径或连接串。测试阶段失败仍先完成 worker join、pool close 和临时目录清理；无法确认清理时保留诊断目录并报告固定 `cleanup_incomplete`。

## 用例与验收

1. 受控屏障：两个 worker 都 ready；验证恰一成功，败方 sentinel 冲突或实际 `glebarez/go-sqlite.Error` BUSY 主码家族可接受。
2. 负例：败方返回 `SQLITE_LOCKED`、取消、普通数据库错误或零值时必须失败，并包含安全摘要；不通过字符串匹配或任意 `Code()` 接口放行。
3. Schema 与清理：胜者 Schema 与独立期望一致，恢复打开一致；正常路径所有 pool 关闭且临时目录删除，join/close/remove 失败均可定位且不谎报清理完成。
4. 运行定向 `-race`，随后受影响 store 包 `-race`、全量覆盖率和 vet；若定向失败立即停并保留日志。已有 ST06 浏览器竞争启动用例不变；这是测试合同/诊断修正，不引入新产品行为。

## 评审状态

本规格先于代码调整；架构/测试评审通过后才实施。原 r2 失败报告及日志只读保留，新一轮输出使用新文件名。

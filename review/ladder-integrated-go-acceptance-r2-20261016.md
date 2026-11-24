# 合入后 startup 修订测试复验 r2

结论：定向 race 失败，按授权立即停止；store race、全量 coverage、vet、全量 race 均未启动。未修改源码、未重跑，上一轮完整失败证据保留。

- 工作目录：`D:/en-words/server`。
- 命令：本机 Go 执行 `test -p=1 -count=1 -json -race -run ^TestStartupTwoIndependentPoolsNeverPublishPartialSchema$ ./internal/store`。
- exit 1，命令耗时 18.64 秒；1 个顶层测试失败，测试体 0.24 秒，0 skip；无 DATA RACE 报告。
- Go 源树 SHA256：`8747aa31f3db6c72c1fe16325015bb2686b32114545f5a546996180dbbf9c788`，前后相同。批准的 34 文件 manifest 仍匹配。
- 原始命令与终态：同名 `.json`；原始日志：同名 `.logs/targeted-startup.log`；源码清单也在该目录。

## 本次实际观测

失败位置为 `startup_matrix_test.go:610`，断言 `losing worker must report initialization conflict`。这是完成屏障和结果收集后的新增错误类型断言，不是之前的 20 秒 ready 超时。该位置之前已通过两方 ready、两个结果回收和关闭结果检查；本次没有 cleanup 错误。专属 `enwords-startup-two-pools-*` 目录在运行前后均为空，实际清理完成。

本次日志没有 worker 逐阶段序列：当前代码只在 ready/result/清理失败时打印它们，错误类型断言失败不输出已捕获的 stages。不能虚构每个 worker 的阶段耗时或失败错误；最多可依据执行位置确定前述屏障及回收检查已通过。

## 静态合同核对

docs37 第 32 行明确允许内层非空导致初始化冲突，以及写升级 BUSY/SQLITE_BUSY_SNAPSHOT 导致固定初始化冲突或存储错误。`startup.go` 只在内层 probe 发现非空时返回 `ErrInitializationConflict`；实际创建表失败直接传播底层错误。因此当前仅接受 `ErrInitializationConflict` 的断言比已批准合同更窄。

这能证明断言存在合同不一致，不能证明本轮具体错误一定是 BUSY。需要在所有终态输出已有的固定 worker stage/error 摘要，且若要认可存储竞争失败，应明确核验可允许的 SQLite 错误类别与完整 schema/失败零副作用，而非任意错误一律接受。测试调整与下一次运行须经主审；本轮没有修改断言。

JSON 中 `candidate_missing_passed` 列出 25 个梯度测试，是共用统计器对单一 startup 专项的未执行项，不是额外 25 个失败；本次没有声称复验了这些用例。

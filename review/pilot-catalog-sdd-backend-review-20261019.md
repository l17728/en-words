# Pilot catalog journal：后端设计复审

评审范围：docs/41 §109（catalog frame、状态机、接受矩阵），并对照 §§42、56、114 的锁、创建、cleanup 与恢复合同。只读设计评审；未修改文件、未运行测试。

## 结论

**BLOCKED：1 项 P1 不可判定的恢复规则，1 项 P2 状态迁移遗漏。** 帧编码和同步步骤总体可由 Go 在 Windows/POSIX 实现，且与可信 supervisor/verified-handle 威胁边界相容；但当前验收要求不能由 catalog 本身实现：完整且已 Sync 的 frame 若进程在响应确认前崩溃，重启无法判断该 frame 是否“acknowledged”。此外，未使用过的 `ready` case 没有到 cleanup 的合法路径。

## 发现

| ID / 级别 | 位置 | 发现与建议 |
|---|---|---|
| CAT-BE-1 / P1 | §109.1 append 与 §109.3 前缀恢复（约 128、153 行） | 规则要求重启只接受“acknowledged append boundaries”产生的完整前缀，并把完整但未确认的 frame 视作全局损坏。对 append-only 文件而言，若 frame 已完整写入并 `Sync` 成功、进程却在给调用方确认前崩溃，磁盘字节与“已确认后崩溃”完全相同；没有第二个独立持久化事实可用于区分。若再加 ack frame，也会出现 ack frame 自身 Sync 后、响应前的同一不确定窗口。建议冻结线性化点为 frame 的完整 `Sync`：重启接受任意完整、校验通过且状态迁移合法的 frame；未确认的调用方通过读取当前状态/幂等操作收敛。只将短写、部分 frame、CRC/长度错误及 Sync 结果不确定且文件未形成完整有效链的情形定义为全局 fail-closed。同步失败即使最终留下完整 frame，也按“检查链并以 durable state 收敛”，不可声称能判定其 ack 状态。 |
| CAT-BE-2 / P2 | §109.2 状态表（约 136–147 行） | `ready` 表示已创建并验证、但尚未开始 Start 的 case；表中 `ready` 只能迁移到 `open_recovery_required`，没有 `ready → cleanup_quarantined` 或 `ready → quarantined → cleanup_quarantined`。因此未使用的 case 无法按正常授权清理，可能永久占用目录和 catalog frame 预算。定义一个明确的 ready-case cleanup 路径，并说明如何在持 case lock、catalog lock 顺序下完成。 |

## 可实现性与覆盖矩阵

`DWC1`/`1CWD` framing、big-endian 长度、CRC-32C/Castagnoli、重复长度、固定 canonical ASCII JSON、全局连续 generation 及有界 payload 均可用 Go 标准库/`hash/crc32` 实现。追加在全局 OS 排他锁内完成 seek/read/validate→transition check→append→`File.Sync`；POSIX 文件锁可由 flock/fcntl 系列实现，Windows 可由基于已验证 handle 的 `LockFileEx` 实现。目录同步、ACL、identity 与锁原语仍须按 §109.3 分别做平台验收，不能由普通 `os.File` 权限位测试代替。

表中的 provisioning、ready、open/recovery、terminal、quarantine、cleanup、deleted 状态具备明确的基本语义；generation/总容量耗尽时全局 fail closed 与 append-only tombstone 保留也一致。Golden frame/CRC、严格 JSON 拒绝、交错 case generation、全转移表、限额、写/Sync 注入、子进程锁竞争都可构造成确定性测试。需先关闭 CAT-BE-1 才能写出可执行的 crash-prefix oracle；当前措辞把外部“请求是否已收到响应”当作 journal 可观测事实，测试虽能借注入器知道，却不能代表生产恢复逻辑。

## 需先修订的验收条款

1. 将 frame `Sync` 定义为 catalog 状态迁移的持久化线性化点；成功后即使进程未回复，重启应以已验证完整 frame 为准。明确请求重试依据当前 case 状态收敛。
2. 分开处理 `Sync` 返回错误但 frame 留存为完整有效链的情况：该次进程全局 poison、不回复成功；重启仅按完整链校验并恢复状态，不能声称检测“是否已确认”。
3. 为未启动的 `ready` case 增加可达且受锁保护的 cleanup/disposition transition，并纳入全部允许/禁止状态迁移测试。

在上述两项闭环前，不建议将 §109 冻结为可直接实现的 supervisor wire/state-machine 合同。

# Pilot Supervisor SDD 后端复审（R2）

评审文档 SHA-256：`8C74F0B709D2BE92056B357688BA6A18C31E056A80E857245B2A7E74368A9073`。范围为 docs/42 §§7–12 新增修订与既有 §§1–6 的一致性；只读，未运行长测试。

## 结论

**前一轮 1 项 P1、4 项 P2 全部关闭；仍有 2 项新的 P2，暂不建议标记后端域 PASS。** 新增条款大幅提高了可实现性，特别是 bootstrap 锁顺序、typed case capability、Linux/Windows 能力矩阵、有界 TryLock、Close 资源归属和 SQLite driver 限制。

## 前轮问题状态

| 前轮问题 | 状态 | 复核证据 |
|---|---|---|
| P1：Case ID 256-bit 与 docs/41 的 128-bit 不兼容 | CLOSED | §2.1、§7 均明确 16-byte/128-bit、32 位小写 hex，并指向 §109.1。 |
| P2：锁等待无 context/deadline 契约 | CLOSED（设计层） | §9 规定 TryLock 轮询、5 秒上限、context 取消、持锁后取消语义及 Sync watchdog 限制。实现时还需与 docs/41 journal lock API 对齐。 |
| P2：FS verified-handle adapter / capability matrix 未定义 | CLOSED（设计层） | §9 定义 root/dir/file/lock 窄接口、allowlist 与 Linux/Windows 支持矩阵及拒绝环境。 |
| P2：Supervisor/CaseHandle Close 所有权不清 | CLOSED（设计层） | §3、§9 明确共享句柄与 case 句柄归属、lease、关闭顺序、部分失败所有权。 |
| P2：SQLite DSN/pool/driver 路径限制不具体 | CLOSED（设计层） | §9 指定 glebarez/modernc、rw URI、参数限制、单连接与禁 ping/retry/migration，并承认路径 reopen 不等于 OS handle 绑定及其可信目录限制。 |

## 新发现

### P2 — Close API 签名与超时契约矛盾

§3 声明 `(*CaseHandle).Close()` 与 `(*Supervisor).Close()` 均无参数；§9 却要求 close drain 使用 `ctx`，等待超时返回 `close_pending`，且允许所有者重试。当前签名无法传递取消/截止时间。请将签名改为 `Close(ctx context.Context) error`（或另定义明确的有界关闭策略和 context-free API），并统一 §3、§9。

### P2 — 稳定错误枚举未与各节错误集合统一

§3 声明稳定返回类别仅包括 `case_not_found`, `case_not_ready`, `case_integrity_error`, `supervisor_poisoned`, `io_uncertain`；§8–§10 又要求 `lock_timeout`、`close_pending`、`recovery_integrity_error`、`cleanup_pending`。调用方与日志无法依照单一稳定契约实现。请在一个位置列出完整错误枚举及其可重试性/是否 poison，并由各节引用。

## 一致性检查及建议

- §2.4 的概括锁流程与 §7 bootstrap 特例、§8 `provisioning` 恢复例外能够兼容；bootstrap 不持 catalog 等 case lock，避免反向等待。
- §8 typed capability 把 `ready`、receipt、recovery、cleanup 权限分开，且明确禁止重放 Planner/Registry，满足后端最小权限边界。
- §9 明确持锁后 I/O 无法保证 context 可中断，并把内核挂起交给 runner watchdog，避免虚假硬实时承诺。
- SQLite 部分接受同账号排除的信任边界，未把 PRAGMA 宣称为 handle binding；该限制与威胁模型一致。建议实现前以单测 golden 固定 Windows URI 转义及真实 driver 第一语句顺序。
- OS adapter 当前代码已有 `TryLock`，但 journal 仍使用无 context 的 `Lock()`；本轮只评 SDD，不据此判设计不一致。实现应改造 journal 调用并为真实 Linux/Windows runner 留专门验收门。
- 本轮没有发现新的 P1 或与 docs/41 状态机 wire format 的阻断冲突。

关闭上述两个 P2 后，可再评估后端设计域是否 PASS；整体 SDD 仍需架构、安全、测试域复审。

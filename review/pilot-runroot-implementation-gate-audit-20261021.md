# Pilot RunRoot/logger 实施门现状审计

日期：2026-10-21  
结论：**端到端实现未完成；Pilot 保持关闭。**

## 目标与证据边界

目标链路为已认证用户意图 → 可信 launcher 身份交接 → 单一持久化控制账本 → RunRoot 分配 → Logger 持久 ACK → 产品 API/UI。此审计核对当前代码和现存精确快照验收；组件测试通过不代表整条链路可用。

| 切片 | 当前实现/证据 | 尚未证明 |
| --- | --- | --- |
| 身份交接 | `pilotsupervisor/bootstrap_handoff*` 有严格 BOOTSTRAP codec、Linux SCM_RIGHTS/pidfd 传递和 fake-ledger 测试；Windows tagged package 与全仓 Go 检查通过（`review/pilot-bootstrap-handoff-validation-20261021.md`）。 | 没有可信 launcher 提供已固定的子进程身份；transport acceptance 不等同于 clone3/pidfd、cgroup/systemd 生命周期或真实账本交接。 |
| 持久化 Logger | `logger_store_linux.go`、`logger_service_linux.go`、客户端及有界队列已实现；队列有 Linux ext4/XFS 验收，服务组件有 Linux tagged/race 证据。 | 尚未由 launcher 启动并认证 READY/REGISTER，也没有与 RunRoot dispatch/outbox 的真实 ACK 闭环。Logger 不得修改账本。 |
| 分配器/账本 | `runroot_linux.go` 提供受限 mkdir/身份验证原语；`RunRootLedger` 有 Prepare/Claim/Recover/Finish 和独立验收。 | 没有 `CreateRunRoot` 的集成协调器、原子 v3 control ledger/finish outbox、与 Logger ACK 绑定的端到端分配。 |
| 产品接入 | 当前 Go/React 源码未注册 `/api/v1/pilot/*` 或 `/pilot`；默认产品链路不暴露 Pilot。 | docs/50 的 106 项产品清单仍是 `BLOCKED_NOT_IMPLEMENTED`；没有多用户 Playwright 旅程或课堂试点。 |

## 前置门与验收顺序

docs/54 要求先通过 docs/55 pinned VFS，再依次完成 A0 resident authority、A1 Logger readiness、A2 launcher/handoff；Stage B 才能接 RunRoot 业务，之后才做 API/UI。docs/55 的设计复审已通过，但私有 pinned-handle VFS 尚未实现，80 个 callback 行为仍未执行，四个 runtime trace 仍是非规范候选。最新候选验收包 SHA-256 `49DD58254502E271CF51B1F5F82AAE015CB74919B0763B8AB0211B3FFE872043`；`VFSFLAGS-SIDECAR-EEXIST-REOPEN` 为 **NOT RUN**，因为 host `unix` VFS 未发出目标 WAL 路径的 `O_CREAT|O_EXCL`，不得据此宣称 EEXIST 行为通过。

禁止将当前 path-based `pilotcontrol/sqlite.go` 接入 A0，也不得提前启用产品路由。下一阶段先解决 runtime golden 的 EEXIST 证据归属（host VFS 观测或私有 VFS 注入层）并完成规范 trace/manifest；随后实现、复审并验收私有 VFS。之后按 A0→A1→A2→Stage B→API/UI 逐门实施。每一门需冻结源码哈希、执行对应 manifest、覆盖率和日志隐私审计，并在 Linux amd64/arm64、ext4/XFS 及所需特权环境留下可复核证据；Windows 暂不支持该 Linux VFS。真实课堂仍需独立的人类监督验收，不由自动化或候选 trace 替代。

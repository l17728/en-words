# lesson.start 持久迁移内核 SDD：后端复审 12

评审对象：docs/41-试点课堂Start持久迁移内核SDD.md（2026-10-16 最新稿）。只读检查 OpenVerified 的 ACL/path/file identity 顺序、cleanup quarantine/catalog、跨包 PilotRegistryOnce、loopback Host/Origin/CSRF 是否在 Coordinator 前；并核对 receipt recovery 与现有 SQLite/API 分层。未修改文件或代码，未运行测试。

## 结论

**BLOCK：1 项 P1，另有 1 项 P2。** OpenVerified 明确排在 Coordinator receipt 路径和每次 DB/sidecar/key open 之前，拒绝时不打开数据库、不查 receipt、不运行 Planner；但尚未保证身份校验对应的文件对象就是 SQLite 后续实际使用的对象。规格要求测试“校验后替换”，因此需要明确使用同一已验证句柄/文件身份打开 SQLite，或采用等价的原子 no-follow/open 方案。否则 path-based reopen 仍可能留下 TOCTOU 窗口。Loopback 写入校验与 one-shot Registry wrapper 合同清楚。

## 逐项结果

| 检查项 | 状态 | 依据与建议 |
|---|---|---|
| OpenVerified 在 receipt 之前 | PASS | §3 明确每次 Coordinator 启动、receipt 处理（含命中）以及 DB/sidecar/key 实际 open 前先执行监督器 OpenVerified；失败不打开 DB、不查 receipt、不调用 Planner。receipt-first recovery 受此总门约束。 |
| Path/ACL/file identity 与 SQLite open 的连续性 | **BLOCK / P1** | 文档规定 OpenVerified 检查 canonical path、owner、ACL/mode、reparse/link 与 file identity，然后再以目录句柄相对打开 SQLite/sidecar/key。没有说明该预检返回的 verified handle 直接交给 SQLite，还是之后按 path 重新打开；若后者，校验后替换测试可在检查与 SQLite open 之间替换目标，前面检查保护不到实际 DB 文件。需定义 OpenVerified 原子安全打开并返回不可替换的对象/handle（SQLite 使用同一对象），或证明从校验到实际 open 的目录/文件不可被替换，并覆盖 Windows 与 POSIX driver 行为。 |
| Cleanup quarantine/catalog | PARTIAL / P2 | 删除失败后将 cleanup_quarantined 持久写入 owner-only catalog，阻止复用，只有确认 DB/WAL/SHM/journal/sidecar/key 全部删除才移除，状态语义合理。仍需明确 catalog 本身如何做 ACL/file identity 校验、监督器在创建/打开 case 路径前先查 catalog，以及 quarantine 记录写入/同步失败时 supervisor 保持 fail closed 的行为；否则进程崩溃或 catalog 损坏可能丢掉阻止复用的事实。 |
| PilotRegistryOnce 与 package 边界 | PASS | API pilot_e2e wrapper 独占裸 tools.Registry；绑定校验后先消费共享 CAS permit，再恰调用一次底层 Registry。Planner/HTTP/Coordinator 无裸 Registry 或旁路 handler 引用；Store/API adapter 分包、无反向 import。复制、错绑、panic 复用都有 handler 前拒绝用例。 |
| loopback Host/Origin/CSRF | PASS | §5/Playwright合同只绑定 127.0.0.1 随机端口；写请求精确匹配 Host 与同源 Origin、Sec-Fetch-Site、POST+JSON、Authorization Bearer 及内存 CSRF token；不接受 cookie/GET/表单/缺 Origin/CORS/OPTIONS 写预检。缺项在 Coordinator 前拒绝，测试断言 Planner/Registry=0。 |
| DB receipt recovery 顺序 | PASS WITH IMPLEMENTATION GATE | 收到 receipt 前仍先做 OpenVerified；然后授权并验证 active lifecycle、audit/outbox/frozen payload。sidecar损坏但DB一致时返回原 receipt 并 quarantine case；receipt不可读或完整性不确定时不写。SQLite rollback proof仍须实现专用 typed outcome。 |

## 修订建议

1. 让 OpenVerified 的契约描述“验证并打开”的原子边界，明确把同一已验证 DB handle/VFS 对象交给 SQLite，禁止校验后路径重开；或者把目录 ACL/独占锁作为可证明的不可替换前提，并说明如何通过校验后替换故障注入。
2. 将 owner-only catalog 纳入同样的 OpenVerified/文件身份检查；case 创建前先读 catalog，清理失败时先持有生命周期锁并同步 quarantine 事实。catalog 读写失败、坏行、身份改变时整个 supervisor fail closed，不能继续创建或打开 case。

本轮不代表实现或测试已完成；尤其是 SQLite driver 的句柄打开方式、Windows ACL/reparse 与 POSIX no-follow 语义必须由实现验证。

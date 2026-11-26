# 试点运行监督器 SDD 安全域复审 R2

- 结论：**PASS**。R1 的 8 项均已关闭；本轮新增的 mount ID handle 绑定、root ancestor pinning、锁库存对账及清理目录已不存在时的 resume 收敛步骤均已补齐。未发现未关闭 P1/P2。
- 文档：`docs/42-Pilot隔离运行监督器SDD.md`
- 最终复核 SHA-256：`ADE1BCAF7E2E883534B58FE4E6AB0BB1079140F50B16175BBC10580C38EA3DA1`
- 范围：只读复核 §2–§12 及 R1 安全 finding；未审代码，未运行测试。

## R1 finding 状态

| R1 finding | 级别 | R2 状态 | 依据 |
|---|---:|---|---|
| SEC-SUP-01 同设备 bind mount | P1 | **已关闭** | §2.2/§9 使用 `RESOLVE_NO_XDEV`；fallback 用父/子已打开 handle 的 `STATX_MNT_ID`，并对返回 handle 再次 `fstat/statx`；无法证明则 fail closed。 |
| SEC-SUP-02 catalog/global lock 身份 | P1 | **已关闭** | §7.63 明确永久 `catalog.bootstrap.lock` 和唯一 `catalog.log` global-lock handle，精确 allowlist 及启动 inventory 对账。 |
| SEC-SUP-03 SQLite 实际 file ID | P1 | **已关闭** | §2.6/§9.95 明确 PRAGMA 只有 canonical path，不给 OS file ID；限定可信私有目录/其他账号不可写/排除同账号攻击，不声称 handle binding。 |
| SEC-SUP-04 case HMAC key 下发子进程 | P1 | **已关闭** | §2.7 key 留在可信 Supervisor；子进程只能请求 allowlist MAC 操作；日志伪名 key 独立。 |
| SEC-SUP-05 无锁快照 | P2 | **已关闭** | §2.4/§8.67 要求所有状态读取持 catalog lock，advisory 读取不能授权或判损坏。 |
| SEC-SUP-06 Windows handle-relative cleanup | P2 | **已关闭（文档级）** | §2.3/§9.89 规定逐级 parent-handle-relative 打开、同一 DELETE handle 身份复核和 handle-based disposition。仍须平台实测。 |
| SEC-SUP-07 SQLite sidecar allowlist | P2 | **已关闭** | §7.63/§9.87 精确列 WAL/SHM/journal 并禁止 glob/前缀删除。 |
| SEC-SUP-08 日志伪名 key/ACL | P2 | **已关闭** | §2.7 定义独立用途 key、Supervisor-only mode/DACL、轮换 epoch。 |

## 本轮新增问题与关闭情况

- **SEC-SUP-R2-01（P2）mount ID 与打开 handle 的绑定：已关闭。** §2.2 明确比较父目录与已打开子目录 handle 的 mount ID，并对返回 handle 再次读取 `fstat/statx`。
- **SEC-SUP-R2-02（P2）case-lock inventory 双向对账：已关闭。** §7.63 按 catalog state 定义 lock/directory 允许形态，并规定缺失、重复、未知和 identity drift 的拒绝行为。
- **SEC-SUP-R2-03（P2）固定 root 祖先路径 pinning：已关闭。** §2.1 要求 POSIX 从 trusted anchor directory handle 逐级 pin 祖先；Windows 同样逐级 pin parent handle，并验证权限和身份。

### SEC-SUP-R2-04 — P2：清理目录已经消失时，resume 如何写 tombstone（已关闭）

**条款：** §7.63、§8（§78）、§10（§113–§114）。

§7.63 允许 `cleanup_quarantined` case directory 存在或不存在；目录不存在是合理的崩溃窗口，例如目录删除及父目录 Sync 已完成、`deleted` frame 尚未 durable。§8.78 已明确分两支：目录存在时验证为空后通过 pinned parent handle 删除并同步；目录不存在时在 pinned case-root/父目录复核 absence 并同步。两条路径成功后才追加 `deleted`，不会重建目录。

**R2 状态：已关闭。** 该分支现有条文；仍需实现测试覆盖“删除成功后、tombstone 前崩溃”，按 §10/§11 的平台证据门执行。

## 结论边界

本轮未发现未关闭 P1/P2。报告只评价设计文字；Linux/Windows 的 ACL、mount ID、lock identity、handle deletion 仍须按 §11 在真实平台验证。没有测试结论，也不批准生产接线或真实课堂试点。

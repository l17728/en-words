# 试点运行监督器 SDD 安全域复审 R3（快速确认）

- 结论：**PASS WITH P2**；未发现新增 P1。针对本轮指定的四项更改，前三项闭合且未见安全回归；Close unknown-state 的资源保留策略是 fail-closed，但缺少强制进程退出/重启的操作闭环，列 1 项 P2。
- 文档：`docs/42-Pilot隔离运行监督器SDD.md`
- 目标及当前 SHA-256：`4775CF8BE9D5D69314B01F542EA318E5218A2CB482E244F578F06192766F9352`（完全匹配）
- 范围：只读快速审查 case inventory/bootstrap、cleanup absent-directory、file Close unknown-state、DB recovery counters。R2 PASS 的其他条款继承，不重审；未审代码、未运行测试。

## 本轮新增更改核对

| 项目 | 结果 | 依据 |
|---|---|---|
| case inventory/bootstrap | **通过** | §7.57 定义首个 `provisioning` ID 无 lock 的唯一窗口，并规定 recovery 用 `CREATE_NEW` 或 `OpenVerified` 收敛到同一永久 lock；§7.63 明确各 catalog state 的 lock/directory 形态与 fail-closed 条件。未发现新路径授权或 lock identity 回归。 |
| cleanup absent directory | **通过** | §8.78 对目录存在和“删除+Sync已成功、deleted frame 未写”两支分别处理；不存在时核验 pinned case-root absence、Sync parent 后才追加 deleted，不重建目录。 |
| file Close unknown-state | **通过但有 P2** | §9.95 以唯一 handle generation 绑定 `closed/still_open/unknown`；仅 closed 推进，still_open 由原 owner 重试，unknown 不二次 Close/Unlock、poison 并保留锁及引用，符合避免 double-close/unlock 的安全原则。缺少未知状态下要求退出监督器进程的明确动作，详见 SEC-SUP-R3-01。 |
| DB recovery counters | **通过** | §10.101 定义 `db_open_app` 与 `db_open_recovery`，对允许状态/只读 SQL 限定一次恢复连接，并要求状态拒绝、第二次 Start、身份失败时两项均为 0；计数能区分获准恢复核验与普通应用 DB open。 |

### SEC-SUP-R3-01 — P2：Close unknown-state 后没有强制进程退出/重启闭环

**条款：** §3.5、§3.7、§9.95。

§9.95 正确规定 handle close 状态为 `unknown` 时不再调用 OS Close/Unlock，保留 case/catalog lock 和 handle reference，直到进程退出由 OS 回收。但接口同时将关闭错误表现为可重试的 `close_pending`，未规定 unknown 是终态、必须触发监督器进程退出/由外部 runner 重启，还是允许服务进程继续运行。若进程继续运行，Supervisor 会永久 poison、OS lock/句柄可能长期占用；对 `unknown` 反复调用 Close 又违反本节安全规则。

**建议：** 明确 `unknown` 为终态：返回不可重试的固定错误类别，发出不含敏感信息的 fatal shutdown 事件，并要求宿主停止接收新请求、结束监督器进程，由外部进程管理器重启。`still_open` 才允许原所有者以新 context 重试。增加一个 close adapter 返回 unknown 的生命周期测试，断言无二次 Close/Unlock、无 cleanup，进程监督器收到终止信号。

## 继承状态与边界

R2 PASS 的路径、mount ID、SQLite 明示限制、key 留存、日志隐私、Windows handle deletion 等条款在本轮指定改动中未见回归，按 R2 结论继承。报告仅覆盖 SDD 文本；不代表 unknown-close 的实现已安全，也不替代 Linux/Windows 平台测试、代码 review、生产接线或真实课堂试点验收。

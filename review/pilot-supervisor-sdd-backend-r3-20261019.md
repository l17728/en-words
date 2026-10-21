# Pilot Supervisor SDD 后端复审（R3）

评审对象：`docs/42-Pilot隔离运行监督器SDD.md`  
SHA-256：`AEF772E4D71633690E4E3F4C7498837552088D5BD385AF1875DE820E1B9AA447`  
范围：只读检查关闭适配器、重试及锁释放、unknown 退出/reap、DB 计数、FI 恢复 oracle、catalog append 和 SQLite 边界；未改代码或运行测试。

## 结论

**后端设计复审仍为 BLOCKED：2 项 P1、1 项 P2。** R2 的 context 签名、错误枚举、128-bit case ID 和 SQLite file-ID 声明均已在设计层关闭；但 close 状态和 SQLite provisioning 计数仍有实现矛盾。

## 发现

### P1 — Go close API 无法可靠兑现 `still_open` 同句柄重试

§9.4 允许原 owner 对 `still_open` 句柄重试；§9.5 又要求无法证明 DB 句柄关闭时映射 `unknown`。FI-CLOSE-01a 先把 DB.Close 错误归为 `close_pending`，随后又要求按 `still_open/unknown` 分支，结果不唯一。`database/sql.DB.Close()` 不暴露底层连接逐个是否关闭，`os.File.Close()` 错误也没有跨平台的同句柄可重试保证。若重试或释放 case lock 时猜错，可能发生双重 close 或并发 owner。

请将 `still_open` 限定到能由具体平台 API 正向证明仍打开的原生句柄；通用 `os.File` 和 `database/sql.DB` 的关闭错误默认应为 `unknown`，除非锁定的实现能证明更强语义。统一 FI-CLOSE-01a 的错误结果，并明确由 harness 终止、等待并 reap 专属 child；所有 owned handles 确认关闭前不得释放 case lock。

### P1 — SQLite 初始化与 DB open 计数互相矛盾

§8 要求 `CREATE_NEW` SQLite 文件，但 §10.1/FI-DB-01a/b 要求 `db_open_rw=0`，同时 FI-DB-01b 又要求 SQLite initialization/write。一个新建空文件如何形成有效 DB/schema 尚未定义：通过 SQLite 初始化会产生 mode=rw 连接；只允许 ro recovery 则无法初始化写入；复制预制模板又没有定义模板、成员 allowlist 和复制/fsync 身份协议。

请明确使用受审模板复制，或允许一个仅限 provisioning 的 rw 初始化连接并单独计数；同时规定 DB schema、初始化允许 SQL 和 recovery PRAGMA 计数，修订 FI-DB-01a/b 的 oracle。

### P2 — catalog FI 的故障点仍未逐项绑定到唯一重启 oracle

§13 要求记录 generation 和 frame byte range，但 FI-CAT-02/03 未把“写入前、partial write、完整写后 Sync 错误、file Sync、parent Sync”逐项绑定到预期字节、当前 poison 范围和重启结果。FI-OPEN-01 也把 read、seek、stat/identity 与 CRC 错误合并，未说明各自是否 poison 整个 Supervisor。建议按 syscall 屏障拆分用例；只有 fresh-process replay 才能裁决 uncertain Sync 后可见的完整 frame。

JSON 结构化报告已保存为 `review/pilot-supervisor-sdd-backend-r3-20261019.json`。尚未通过后端设计门，不应开始监督器实现。

## 跨文档兼容性

同时复核了 docs/41-试点课堂Start持久迁移内核SDD.md §112，SHA-256：7890FB542818C77F131CD06D09845120406EB7C1567AF1BDFA83C92FA51D3867。driver 勘误与 docs/42 §§2.6、9.7 一致：只验证 PRAGMA canonical path，不宣称 SQLite OS-handle file-ID 绑定；可信私有目录和排除同账号攻击的边界明确。HMAC 勘误也与 §§2.7、日志约束一致：key 留在 Supervisor 进程内，后续签名只经窄化内部 capability，IPC 拆分须另行评审。两项均与当前 test-only 后端实现兼容，且没有把 catalog 内核误当成已实现/已验证的 supervisor 能力。


### P2 — 日志 allowlist 已同步，但生命周期和失败语义未定义

最新§7/§9.1已统一 root/case/log allowlist，先前发现的目录 inventory 不一致已关闭。仍未定义 `logs/`、`events.jsonl` 和 `log.pseudonym.key` 是在 bootstrap 还是惰性创建，如何 CREATE_NEW/设 ACL/Sync/校验身份，日志 append/Sync 失败是否阻断操作，启动时 absent/partial 文件如何恢复，以及 root epoch 轮换、保留和 cleanup 规则。由于§2.7要求 bootstrap 等关键事件都可观测，且 pseudonym key 决定 case hash，当前缺少可执行的唯一生命周期和故障 oracle。请补齐创建、权限、身份、耐久、轮换、保留、错误处理与重启测试。

跨文档检查：`docs/41-试点课堂Start持久迁移内核SDD.md` §112，SHA-256 `7890FB542818C77F131CD06D09845120406EB7C1567AF1BDFA83C92FA51D3867`。driver file-ID 与 key-to-child 两项勘误均与 docs/42 §§2.6-2.7 相容；它们收窄并明确本 test-only 威胁边界，没有与当前后端接口冲突。两项仍未由 catalog 内核实现或证明。
### P2 — `operation_canceled` 没有对应的稳定错误映射

§3 将 `operation_canceled` 列入稳定错误码，但§9.2把 context cancellation 与等待超时都映射为 `lock_timeout`。调用方不能区分取消与 deadline，测试也无法验证完整错误契约。请统一规定 `context.Canceled` 与 deadline/cap 的映射（或移除未使用错误码），并覆盖所有有界等待。

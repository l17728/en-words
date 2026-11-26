# Pilot Supervisor SDD 后端评审

评审范围：`docs/42-Pilot隔离运行监督器SDD.md` 的后端可实现性、接口边界、文件与锁抽象、SQLite接入顺序、平台依赖，以及与 `pilot_e2e` catalog journal 的一致性。只读评审，未运行长测试。

## 结论

**暂不通过（1 项 P1、4 项 P2）；建议先修订设计再进入实现。** 目标隔离边界清楚，且明确不接生产 HTTP/课堂，适合作为独立监督器的设计基线。但关键标识格式与既有 journal 不兼容，锁等待的“有界/可取消”承诺也没有可落地的接口契约。

## 发现

### P1 — Case ID 位数与 journal wire format 冲突

`docs/42` §2.1 要求 256-bit 随机 ID；`docs/41` §109.1 与当前 `pilotCatalogRecord`/`validPilotCatalogCaseID` 固定为 16 bytes、32 个小写 hex 字符，即 128-bit。监督器按 256-bit 生成的 64 字符 ID 将被 journal 拒绝，且改变 journal schema 会破坏已冻结的协议与测试向量。进入实现前必须统一为既有 128-bit 格式，或先独立评审并版本化 journal 协议；不可在监督器内静默截断/映射 ID。

### P2 — `context.Context` 与“有界锁等待”没有对应的可实现契约

§3 所有操作收 `ctx`，§2.5/§3 又要求操作有界；但 §4 只说 OS 锁，未定义锁接口、deadline/取消语义或超时错误。现有 `pilotCatalogLock.Lock()` 无 context：POSIX `flock(LOCK_EX)` 与 Windows `LockFileEx` 都是阻塞调用，`pilotCatalogJournal` 的调用不会响应 context。设计应规定非阻塞尝试+有界退避/截止时间，或者明确一项硬上限，并定义取消、超时、unlock 结果不确定时的 poison 行为；所有目录/文件 I/O、Sync 也需说明哪些可以被取消、哪些只能在返回前等待完成。

### P2 — 文件系统与已验证句柄 adapter 的接口/能力检测仍停留在算法描述

§2.2–2.3 写明 `openat/openat2`、`CreateFile` flags、身份字段等正确约束，但 §3 没有定义 store 内部的窄接口（例如固定根句柄、相对目录打开、原子私有目录创建、身份快照、Sync parent、删除相对句柄、关闭生命周期）。也未说明 unsupported `openat2` 如何探测、哪些 Linux/macOS/BSD 能力组合支持，或如何确保 Go 的 `os.File`/驱动层仍持有同一 OS handle。建议把 API 形状和 capability matrix 列为实现前置规格；字符串路径仅作为固定根初始化参数，不得流入 case 生命周期操作。

### P2 — `Supervisor.Close` 与 case handle 的所有权/并发关闭未定义

§3 要求 `CaseHandle.Close` 关闭 sidecar/key/catalog 相关句柄，而 catalog 是 supervisor 共享资源；同时 `Supervisor.Close` 要关闭所有 case。没有规定句柄归属、引用计数/注册表、并发 Close/操作如何线性化、部分关闭后谁负责重试或永久 poison。按字面实现可能让一个 case 关闭共享 catalog，或 supervisor 和 case 双重关闭同一 OS handle。应定义 supervisor 拥有 root/catalog/global lock，case 拥有 case lock/DB/sidecar/key；明确关闭顺序、并发操作排空和关闭错误后的唯一所有者。

### P2 — SQLite `mode=rw` 与“先验证再打开”的 driver 接线顺序不够具体

§2.6 正确指出 `PRAGMA database_list` 是打开之后的检查，但尚未定义将已验证 OS 文件句柄交给 SQLite 的路径。当前仓库锁定的是 `github.com/glebarez/sqlite`（现代cgo-free实现），不是直接导入 `modernc.org/sqlite` 的独立连接层；driver 可能按 DSN 自行打开路径，无法仅靠预先验证的 `os.File` 证明它打开的是相同对象。应明确固定 DSN 的构造/URI 转义、禁用 create/cache/自动重试、连接池必须单连接，`PRAGMA database_list` 的实际调用时机及 file-ID 取得方式，并写明现有限制：DB打开和首条SQL之间的窗口只在可信私有目录威胁模型内成立，若要求 handle 绑定则需 driver/VFS 能力验证后另设门。

## 已确认的一致点

- §1 将 HTTP、SQLite/Coordinator 与产品路由分阶段，默认构建排除试点代码；符合现有 `//go:build pilot_e2e` kernel 隔离方式。
- §2.5 的 append-only、CREATE_NEW、文件与父目录 Sync、失败 poison 与 §109 journal 的状态迁移/耐久原则一致。
- §4 全链路日志字段及禁止敏感值的要求明确，适合作为实现验收项。
- `golang.org/x/sys` 已列为 `server/go.mod` 直接依赖，现有平台锁 adapter 使用它；但这不替代上面的 filesystem adapter 能力规格。
- §5 要求 Linux 与 Windows 实跑、交叉编译不能代替平台验收，方向正确。

## 进入实现前的最小修订

1. 统一 ID 位数到 docs/41 当前协议，或先完成 journal 协议版本升级评审。
2. 给文件/锁 adapter 明确 Go 接口、句柄所有权、deadline/取消与 poison 语义，并约定 Linux/Windows capability matrix。
3. 定义 supervisor/case 资源所有权和并发 Close 生命周期规则。
4. 将 SQLite driver 的路径打开与 `mode=rw` 验证步骤写成可测试顺序，并明确路径检查不等于 OS handle 绑定。

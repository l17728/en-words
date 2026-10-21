# 试点运行监督器 SDD 安全域复审 R1

- 结论：**BLOCKED（修订后复审）**。当前 SDD 有明确 fail-closed 意图，但关键 OS 身份、并发读取、SQLite 对象证明和 key 信任边界仍有契约缺口；不应据此开始对应实现。
- 文档：`docs/42-Pilot隔离运行监督器SDD.md`
- 复审时 SHA-256：`8A651B4D5193C2DD047BEAE85379C631FA617FF74230A6298D6DB495E980FC51`
- 范围：只读审查 §2、§3、§7–§9、§10–§12，重点检查路径与句柄竞态、POSIX/Windows 身份及权限、bootstrap/case lock、cleanup、Sync/poison、SQLite 实际对象、key 和日志。未审查实现代码，也未运行测试。

## 阻断项

### SEC-SUP-01 — P1：POSIX fallback 的设备号检查无法拒绝同设备 bind mount

**条款：** §2.2、§9 支持矩阵。

fallback 要求逐段 `openat`、`fstat` 并验证同一 `st_dev`，同时宣称拒绝挂载点/设备跨越。但 bind mount 可在同一文件系统设备号上跨越挂载边界；仅比较 `st_dev` 不足以实现该保证。若路径组件在检查与后续操作间发生挂载拓扑变化，句柄仍可能落在预期根之外。

**修订建议：** 对 Linux 使用 `openat2(RESOLVE_NO_XDEV|RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)`；fallback 要定义可验证挂载边界的方法（例如逐级 `statx` mount ID 并锁定/验证父子 mount ID），并明确无法证明时拒绝启动。增加同设备 bind mount 与挂载变化测试，不能把 `st_dev` 等同于“未跨挂载点”。

### SEC-SUP-02 — P1：catalog/global lock 的锁定对象与身份生命周期未闭合

**条款：** §7（§57–§63）、§9（§87）。

首次 bootstrap 描述在 catalog 不存在时锁“新 CREATE_NEW handle”，但该句没有定义锁定的是 catalog 文件本身还是独立永久 lock file；后续又将其称为共享 `global catalog lock`。§87 的编译期文件名 allowlist 没有 `catalog.bootstrap.lock` 或可辨认的 catalog lock 名称。并且 catalog 可通过 `CREATE_NEW` 创建并 Sync，却要求先取得“catalog lock”；对象创建/锁定先后、竞争者如何取得同一 OS lock identity、重启如何验证其身份均不够明确。

**修订建议：** 明确全局锁唯一、永久、不可 unlink/recreate 的文件名和对象身份；规定 bootstrap 与 catalog 锁的创建、验证、取得、持有及释放顺序，以及新建 catalog 时锁住的精确 handle。把所有控制文件和 SQLite 派生 sidecar 加入有界的编译期 allowlist，并规定 lock-root 中未知、缺失、重复或身份不匹配文件的 fail-closed 行为。补充双进程首启/重启/替换锁文件 schedule。

### SEC-SUP-03 — P1：SQLite 文件身份承诺超过验证接口能提供的证据

**条款：** §2.6、§8（§81）、§9（§95）。

`PRAGMA database_list` 返回数据库名和路径，不返回 SQLite 当前打开文件的 OS file ID。§2.6 声称比较 canonical path 与 OS file ID；§95 又明确现代 driver 按路径 reopen，且无法保证与预验证 handle 绑定，也承认平台可能拿不到 driver 实际文件 identity。仅路径相同不能证明驱动打开的是此前验证的同一对象，尤其文件替换发生于 URI 打开与 PRAGMA 之间时。

**修订建议：** 在启动实现前作出二选一的明确裁决：引入能从实际 SQLite VFS/file handle 取得并核对 file ID 的已评审机制；或删去“逐次比较 OS file ID/同一对象”的保证，将其明确降为依赖私有目录 ACL、无不可信写入者及排除同账号攻击的路径级保证，并为未能取得对象身份的场景定义拒绝或接受条件。不得把 `PRAGMA database_list` 当作 file identity 证明。

### SEC-SUP-04 — P1：case HMAC key 下发给子进程与子进程不可信边界矛盾

**条款：** §2.3–§2.7、§2.13、§2.21、§8、§9。

威胁模型将子进程消息视为不可信，并要求受限子进程 SID；同时每 case 的完整 HMAC key 允许通过继承 handle/匿名管道交给子进程。文档没有说明子进程代码是否可信、哪些进程能获得 key、何种操作被 key 授权，以及 key 被子进程泄露或滥用后 sidecar HMAC 的信任语义。能持有完整 HMAC key 的子进程可以生成合法 MAC，故“受限 SID”本身不能维持 key 对该进程的保密性或约束其签名用途。

**修订建议：** 明确受信任组件边界和进程身份；优先让 key 留在 supervisor，由子进程请求窄化的 MAC/验证操作。若确需交付 key，明确承认持钥子进程属于 trusted computing base，限定为固定签名操作、句柄继承白名单/一次性管道、退出后句柄关闭和密钥清零的可验证边界，并调整“子进程消息不可信”的定义及 sidecar 可信度声明。另明确日志 case-hash 使用独立域分离密钥，不可复用 case HMAC key。

## 重要问题（P2）

### SEC-SUP-05 — P2：无锁快照与 catalog 读取必须持锁的规则冲突

**条款：** §4（§38）、§8（§67）。

§38 将 catalog 链读取列为 global lock 覆盖范围；§67 的顺序却先“读取快照”，再取得 case lock 和 catalog lock 复核。若快照解析遇到 append/Sync 中间态，可能把合法并发写入的短暂尾部误判为损坏并 poison 全局服务。即便快照只是提示，也需明文禁止由它触发完整性失败或授权决定。

**修订建议：** 明确所有用于校验/决定状态的读取都在 catalog lock 下完成。若保留无锁预读，仅允许得到不可信提示，禁止以 partial frame、CRC 或状态异常 poison；随后必须在锁内完整重读并作唯一裁决。为 append 写入与 opener 竞争增加确定性 schedule。

### SEC-SUP-06 — P2：Windows 逐级 reparse 验证与 cleanup 删除的 handle 语义不足

**条款：** §2.3、§3.6、§8（§78）、§9（§87、§89）。

`reparse-safe flags`、检查 owner/DACL/identity 和“不共享 DELETE”的要求尚未规定每个路径分量如何相对已验证父目录句柄打开、是否对每级使用 `FILE_FLAG_OPEN_REPARSE_POINT` 并验证 reparse tag、如何在删除时将已验证 file ID 绑定到删除对象。cleanup 又必须关闭阻止删除的句柄后删除条目；若随后按字符串路径重开/删除，会重新引入替换竞态。`RemoveFile`/`RemoveDir` 抽象没有写明必须句柄相对或核对待删身份。

**修订建议：** 明确 parent-handle-relative 打开和 handle-based disposition 删除协议；对待删对象重新核对 file ID、reparse tag、volume ID，并说明关闭防替换句柄到删除操作间如何确保目录项不可被非授权主体替换。Windows ACL 对控制文件、目录、SQLite sidecar 的继承与受限子进程实际 SID 权限也需逐项约束。

### SEC-SUP-07 — P2：清理文件 allowlist 与 SQLite sidecar 清单不一致

**条款：** §3.6、§8（§78）、§9（§87）。

cleanup 要成组移除 `db.sqlite` 的 WAL、SHM、journal，但 §87 allowlist 只列 `db.sqlite`，没有这些 sidecar。若临时扩为任意前缀删除会扩大路径攻击面；若只按现有 allowlist 则无法完成规定清理。

**修订建议：** 将可删除成员定义成固定精确清单（如 `db.sqlite-wal`、`db.sqlite-shm`、`db.sqlite-journal`），说明只允许在固定 case 目录内、通过验证过的 parent handle 与身份检查删除；未知文件一律 quarantine，不用 glob/前缀或递归通配。

### SEC-SUP-08 — P2：日志 keyed case hash 的密钥与诊断文件保护未定义

**条款：** §2.7、§4（§40）、§11（§122）。

要求记录 keyed short hash，却没有定义密钥归属、生命周期、用途分离或日志文件 ACL；若复用 case HMAC key，会将跨 case 标识与子进程密钥耦合。日志保留路径和权限也未被安全不变量覆盖。

**修订建议：** 使用独立、仅监督器可读、与 case key 分离的日志 pseudonym key；规定轮换/生命周期和日志所在目录 ACL/mode。限制 hash 长度与日志访问范围，并继续对原始日志做路径、ID、key、DSN、文本的负向扫描。

## 通过的设计控制（限文档审查）

- 明确排除同一可信账号/管理员/内核被攻陷，并要求不支持所需 OS 语义时 fail closed（§2.1）。
- 规定 POSIX `O_NOFOLLOW`、owner-only mode、单链接和稳定身份检查，以及 Windows DACL/reparse/file-ID 检查方向（§2.2–§2.3）；仍需按上述条款补足实现细节。
- 锁/Sync 不确定时 poison、不降级、不把 context cancellation 当回滚证明（§2.5、§3.4、§9.4）。
- cleanup 先 durable quarantine、删除固定成员后 durable tombstone，失败不重新开放（§3.6、§8、§10）。
- 错误码与日志禁止暴露路径、ID、key、token、DSN 和原始输入（§3.4、§4）。

## 复审门

关闭 SEC-SUP-01–04 后再评估能否解除实现阻断；SEC-SUP-05–08 也应在定稿前写入 SDD 并由对应测试/平台证据闭环。此报告仅审查设计文本；没有测试结论，也不为实现、生产接线或真实课堂试点提供安全批准。

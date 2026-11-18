Pilot 验证根与成员句柄切片 SDD

**状态：设计评审通过，2026-10-20；代码静态复核阶段，平台验收未完成。** docs/42 §2、§9 的首个可实现增量定义。架构、后端、安全、测试四域设计评审均 PASS、无设计阻断。范围仅供 `pilot_e2e` 隔离测试内核使用；不接产品路由、课堂、模型、Gateway 或 Reaper，也不宣称生产就绪。设计评审不代表代码或 Pilot 验收通过。

## 1. 目标与边界

为 catalog bootstrap 提供最小、可验证的操作系统文件句柄入口，证明受信任目录树中的既有根目录和既有成员可按其真实 OS identity 使用。只允许枚举run-root直接成员并验证打开句柄；不得读取文件内容、同步、创建、删除、改名、写入、打开 SQLite、执行 Start 或根据请求路径选择成员。control ledger/VFS、case生命周期和授权另按docs/42、docs/43设计。

## 2. API 与所有权

API 留在 `server/internal/pilotsupervisor`，并受 `pilot_e2e` build tag 隔离。构造只接受由调用者已经打开并持有的anchor directory handle和opaque `RunEpoch`，不接受任意绝对路径、请求字符串、环境变量、CLI参数或 `filepath.Join` 结果。此切片的 `AnchorHandle` 只能在 `_test.go` 的 `newVerifiedTestAnchor(t)` 中构造：它从随机临时目录打开目录handle，并按本SDD的Linux/Windows owner、权限、type、identity规则完整验证；验证前不生成 `AnchorHandle`。anchor由调用方借用，root不得Close它。未来运行harness的anchor只能来自固定受保护配置/服务常量，产品进程尚无此配置入口。生产anchor opener、配置来源和launcher身份审验均不在本切片内，完成前不得称为完整root bootstrap。

本切片的唯一目标是已存在的 `pilot-runs/<RunEpoch>/`。`RunEpoch` 是经验证的 `[0-9a-f]{32}` 不透明类型，路径深度固定为一个组件；anchor 是外层已验证的 `pilot-runs/` directory handle。根层精确允许集合为 `catalog.bootstrap.lock`、`catalog.log`、`case-locks/`、`cases/`、`logs/`，每项均为可选既有项；缺失合法项表示尚未初始化，额外项拒绝。`OpenVerifiedRoot`必须在发布root前执行一次完整严格inventory验证；每次公开`OpenDirectory`/`OpenMember`也必须先通过同一个非递归内部`validateRootInventory`，再打开目标项，不能要求调用者先调用`ReadNames`，也不能将其结果当作授权凭据。该前置检查在Linux/Windows一致。仅可打开根目录、这三个既有一级目录和两个根级普通文件；不枚举其下动态case内容，不进入`<caseID>`动态目录、不读取DB/sidecar/key/event日志或catalog日志内容。封闭enum值仅有 `DirectoryName={CaseLocksDir,CasesDir,LogsDir}`、`MemberName={CatalogBootstrapLock,CatalogLog}`、`RootEntry`为上述五个固定enum之一。完整Go合同为 `OpenVerifiedRoot(anchor *AnchorHandle, epoch RunEpoch) (*VerifiedRoot,error)`、`(*VerifiedRoot).OpenDirectory(DirectoryName) (*VerifiedDirectory,error)`、`(*VerifiedRoot).OpenMember(MemberName) (*VerifiedFile,error)`、`(*VerifiedRoot).ReadNames() ([]RootEntry,error)`、各句柄 `VerifyIdentity() error` 和 `Close() error`。`ReadNames`按§3.2执行双枚举及逐项短暂打开验证；只在全流程成功后原子返回完整allowlist，不授权任何后续业务操作。`Close`成功返回nil；排空timeout返回`verified_timeout`（Supervisor映射`close_pending`），未调用任何OS close；root保持poison以拒绝新操作，但同一owner可在在途操作完成后重试Close。OS close状态不确定返回`verified_close_unknown`并映射`restart_required`。Root.Close先拒绝新操作、等待in-flight最多500ms，再按逆序关闭未关子句柄并最后关闭root；第k个close unknown时停止，记住已确认关闭的句柄并保留该项及尚未关闭项的引用，poison root并要求worker退出。unknown后的Close重调只返回缓存错误，不调用OS；成功后的重调仅返回nil，不调用OS。打开失败时也按逆序关闭本次已打开句柄，遇close unknown立即停止并将其作为主错误。所有句柄均为不导出字段的指针类型并实现 `noCopy`；anchor 是借用句柄，调用者在 root Close 前不得关闭/改用；root注册并独占管理返回的子句柄，调用者可先Close子句柄，root随后跳过已关闭句柄。

IdentitySnapshot 分离不可变对象身份与权限证据：Linux identity=`(dev,ino,mount_id,type)`、policy=`(uid,gid,mode,regular-file nlink=1、access/default POSIX ACL均不存在)`；目录不以 nlink 作为拒绝条件。Windows identity=`(volume serial,file ID,type)`、policy=`(owner SID, protected DACL规范化ACE及flags/mask,reparse tag, regular-file link count=1)`；目录不以 link count 作为拒绝条件。所有字段必须来自同一已打开 fd/HANDLE 的 `fstat/statx(AT_EMPTY_PATH)` 或 `GetFileInformationByHandleEx/GetSecurityInfo`；字段缺失、查询拒绝或unsupported表示验证失败，不得使用路径查询补值。每个对象的身份pin线性化点是其相对父句柄打开成功后，首次从返回fd/HANDLE采集完整IdentitySnapshot的时刻；紧接着须由父句柄按同名重新核对绑定关系，只有identity相等才发布句柄。此规则同时适用于anchor→run-root及root→一级成员。Linux每次`VerifyIdentity`/`ReadNames`复核时，通过父directory fd重新打开同名对象、对短暂句柄采集identity并与pin比较，再关闭短暂句柄；Windows句柄不共享`FILE_SHARE_DELETE`，并在父枚举核验精确名称仍绑定目标handle identity。身份重复（同一inventory内两个不同名字得到相同稳定identity）固定映射`verified_identity_changed`。拒绝 symlink/reparse、hardlink、非普通文件/目录、错误owner或权限、ACL额外授权、跨mount/volume及未知目录项；枚举完整验证成功后才返回排序后的名字，不泄漏部分清单。

`AnchorHandle`、`VerifiedRoot`、`VerifiedDirectory`、`VerifiedFile` 的原始 fd/HANDLE、路径和身份均不可导出；一个owner唯一负责关闭。首片run-root DACL覆盖并收窄docs/42 §2.3中对run-root的模糊子进程授权：DACL受保护、owner为SYSTEM；恰有两条无继承flag的allow ACE：由可信launcher固定并传入的 Pilot Worker service SID 仅获本段定义的只读验证mask，SYSTEM获`FILE_ALL_ACCESS (0x001F01FF)`；不得有其它allow/deny ACE。实现必须把这个固定SID与worker实际token中的唯一service SID精确比较，不能从token动态挑一个SID来充当期望值；固定SID的生产配置/launcher接线属于后续阶段，本切片默认空SID并fail closed。此只读策略适用于 `pilot-runs/<epoch>` 及三个既有一级目录和两个根级成员；后续任何写权限都要在独立SDD阶段定义。`pilot-control/`仍按docs/43规定仅由Reaper访问。Windows fixture需由可将owner设为SYSTEM并验证实际token SID的受限native runner执行；不能满足时该平台未验收，不可skip。POSIX期望UID为测试worker euid、GID为fixture固定service GID；目录 `0700`，普通文件 `0600`，access/default ACL无额外项。Linux普通文件使用 `O_RDONLY|O_CLOEXEC|O_NOFOLLOW|O_NONBLOCK`，目录使用 `O_RDONLY|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW`；Windows按对象仅请求目录 `FILE_LIST_DIRECTORY|FILE_TRAVERSE|FILE_READ_ATTRIBUTES|READ_CONTROL|SYNCHRONIZE (0x001200A1)` 或文件 `FILE_READ_ATTRIBUTES|READ_CONTROL|SYNCHRONIZE (0x00120080)`。不授Worker写、追加、读取文件内容、删除、改DACL或改owner权限。POSIX同可信worker UID作为owner仍可另行open/chmod目录，本切片不声称能限制owner或同账号；只承诺API无写/删/ACL变更方法。只读 opener 不提供 Sync、创建或删除方法。

内部以私有typed error包装底层cause，并通过固定sentinel支持 `errors.Is`；cause不得格式化进外部响应或日志。映射如下：

| 条件 | 稳定错误 | Supervisor映射 | 资源处理 |
|---|---|---|---|
| nil/伪造anchor或anchor句柄已关闭 | `verified_anchor_invalid` | `case_integrity_error` | 不打开run root |
| epoch/name格式错误、未知enum、请求打开缺失项 | `verified_path_invalid` / `verified_member_invalid` | 输入错误；不转业务状态 | 不打开任何目标 |
| symlink/reparse/magic-link或跨mount/volume | `verified_path_invalid` | `case_integrity_error` | poison当前root，拒绝后续open |
| 硬链接、未知项、类型/owner/mode/ACL/DACL策略不符 | `verified_policy_mismatch` | `case_integrity_error` | poison当前root，拒绝后续open |
| 文件系统类型查询成功且可识别，但非支持矩阵 | `verified_policy_mismatch` | `case_integrity_error` | poison当前root；不尝试降级 |
| `fstatfs/statfs`或Windows volume-info类型查询失败/无法判定 | `verified_io_failed` | `case_integrity_error` | poison当前root；不猜测文件系统类型 |
| Windows未由可信launcher pin Worker service SID | `verified_configuration_missing` | `feature_disabled` | 在任何对象安全描述符查询前fail closed；不得把未接线误报为ACL损坏 |
| 打开句柄与pin identity不符或目录项换绑 | `verified_identity_changed` | `case_integrity_error` | poison当前root，零后续访问 |
| 同一inventory内不同名字映射到重复稳定identity | `verified_identity_changed` | `case_integrity_error` | poison当前root；不返回部分清单 |
| statx/ACL/DACL/ReadNames/identity系统调用失败，或支持文件系统上必需能力明确unsupported | `verified_io_failed` | `case_integrity_error` | poison当前root；本只读切片无持久写，不映射`io_uncertain` |
| Close的in-flight drain超过500ms且底层Close尚未调用 | `verified_timeout` | `close_pending` | poison并拒绝新操作；保留句柄，同一owner在操作排空后可重试Close |
| open/enumeration/identity调用超过2秒外层硬时限 | worker被harness终止，无Go返回错误 | `restart_required`（父进程判定） | 归档timeout/exit artifact；不声称worker返回稳定错误 |
| 任何部分或最终Close结果unknown | `verified_close_unknown` | `restart_required` | 保留未确认关闭对象及后续对象引用，不再调用OS Close；要求worker退出 |

错误不得携带绝对路径、原始成员内容或身份值；内部日志只记trace_id、固定event/error类和耗时。Open失败时逆序关闭本次取得的子句柄；若关闭全部成功，返回原open错误，任何close不确定则以`verified_close_unknown`为主错误并要求worker退出。Close成功后重调仅返回缓存的成功结果，不重复系统调用；并发Close等待在途读取排空。

## 3.1 有界操作与平台参数

每次open/identity/enumeration操作预算2秒；Close排空in-flight最多等待500ms。Go context不能保证中断卡在内核的同步文件调用，因此专属测试worker由其父测试harness实施硬watchdog：超过2秒即终止worker，最多再用5秒确认进程退出/reap；不允许在同进程启动替代root。该deadline是测试harness必须实际断言的接口，不把Go超时包装误当作syscall已取消。worker被杀后由OS回收所有句柄。Close drain timeout返回`verified_timeout`并映射`close_pending`：底层Close未调用，允许同一owner等待在途操作自然结束后重试；外层硬watchdog若杀死worker，父harness记录`restart_required`/exit，不声称worker返回该状态。底层Close调用后任何结果unknown则映射`verified_close_unknown`/`restart_required`，不得重试。

Windows只用`NtCreateFile`的`FILE_OPEN`，按目录/文件类型指定`FILE_DIRECTORY_FILE`/`FILE_NON_DIRECTORY_FILE`并设置`FILE_OPEN_REPARSE_POINT`；`ObjectName`只能是单一ASCII enum组件转成UTF-16，不接受分隔符、冒号、NUL或多组件名。DesiredAccess、ShareAccess见§2/§3冻结mask。Linux缺失目标仅`ENOENT`映射`verified_member_invalid`；拒绝访问和身份查询失败映射`verified_io_failed`；symlink/mount越界映射`verified_path_invalid`。

实现测试注入仅通过 `pilot_e2e` 内部 `fileOps` seam；默认构造器永远绑定真实OS实现，只有`_test.go`可替换。Linux可注入点：`openat2/openat/fstat/fstatfs/statx/fgetxattr/getdents/close`；Windows可注入点：`NtCreateFile/GetFileInformationByHandleEx/GetSecurityInfo/GetVolumeInformationByHandle/NtQueryDirectoryFile/CloseHandle`。seam只替换单个syscall返回或暂停屏障，不得伪造成功identity/ACL；阳性和基本负例仍走真实系统调用。延迟注入通过子进程真实timeout/watchdog验证，不能令同进程测试永久挂起。

## 3. 句柄验证合同

每个组件必须相对上一级已验证目录句柄打开，并在打开后对同一个返回句柄读取身份及权限；路径检查结果不得替代 handle 检查。任一层拒绝 symlink/reparse、硬链接、未知/多余目录项、权限/owner不匹配、文件类型错误、跨 mount/volume、身份重复或支持能力缺失。验证完成后，所有后续读取均通过返回对象持有的句柄，不可按名称二次打开。

Linux：anchor 和目录使用 directory fd；优先 `openat2` 的 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV`。fallback 逐组件 `openat`，传入上述按对象类型冻结的 flags，并在父/子已打开 fd 上以 `statx(AT_EMPTY_PATH|AT_SYMLINK_NOFOLLOW)`核对 mount ID；不支持 mount ID时拒绝。对同一fd核验type、owner、配置service GID、目录`0700`/文件`0600`、regular-file `nlink=1`、POSIX access/default ACL xattr不存在、device/inode/mount ID。ACL xattr存在且含额外授权映射`verified_policy_mismatch`；ACL查询被拒或unsupported映射`verified_io_failed`；目录枚举须仅返回该层allowlist。

Windows：使用相对已验证父目录 HANDLE 的 `NtCreateFile`：`OBJECT_ATTRIBUTES.RootDirectory=parent`、`Attributes=0`（不得含`OBJ_CASE_INSENSITIVE`）、`ObjectName`仅为单一ASCII enum组件转成UTF-16、`CreateDisposition=FILE_OPEN`、`CreateOptions=FILE_OPEN_REPARSE_POINT|FILE_DIRECTORY_FILE`（目录）或`FILE_OPEN_REPARSE_POINT|FILE_NON_DIRECTORY_FILE`（文件）、`ShareAccess=FILE_SHARE_READ|FILE_SHARE_WRITE`（不含DELETE）、`DesiredAccess`为上文最小mask；不得接受分隔符、冒号、NUL或多组件名。打开前在父HANDLE枚举并要求精确大小写enum匹配；同一目录内若有ASCII大小写折叠后重复的名字，拒绝整个inventory；打开后再核对父项仍绑定实际HANDLE identity。这样固定enum名不会因默认大小写查找落到不同名字。读取返回HANDLE的reparse tag，拒绝所有reparse point。对同一HANDLE读取volume serial与128-bit file ID、owner SID、完整DACL与继承保护标志；逐项核验上文冻结的owner、ACE、flags、mask及regular-file link count。NTSTATUS `STATUS_OBJECT_NAME_NOT_FOUND`仅映射缺失项；拒绝访问/身份查询失败映射`verified_io_failed`；reparse/跨卷映射`verified_path_invalid`。DACL/identity不匹配映射`verified_policy_mismatch`。只支持NTFS。禁止以 `CreateFileW` 的路径打开结果冒充相对句柄打开证明。`OBJECT_ATTRIBUTES.Attributes`语义依据[Microsoft OBJECT_ATTRIBUTES](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wudfwdm/ns-wudfwdm-_object_attributes)；名称仍必须以目录枚举到的精确大小写和handle identity绑定。

### 3.2 ReadNames 一致性与资源合同

`validateRootInventory`是open内部使用的完整严格校验，不调用公开Open方法、不递归调用自身；它枚举根层allowlist并逐项校验现有直接成员。`OpenVerifiedRoot`发布root前调用它；每次公开`OpenDirectory`/`OpenMember`先调用它，成功后才打开并发布目标句柄。因此额外根项或成员策略异常无法通过直接Open绕过。

`ReadNames`是只读诊断枚举，不是授权快照；后续业务绝不能依据其结果打开case或决定Start。它复用同一校验算法并返回enum名称。顺序固定为：①进入root操作门并核验root pin；②从root handle完整枚举第一份直接子项名称集合，拒绝非allowlist或重复大小写名；③按ASCII名称排序，对每个存在的allowlist项相对root打开短暂handle、采集同handle身份/策略、核对父名仍绑定该身份，并在验证下一项前关闭当前handle；同一inventory中重复稳定identity返回`verified_identity_changed`；④任何一步失败均停止，不返回部分清单；⑤再次完整枚举，要求名称集合与第一份完全相同，再核验root pin；⑥只有全部成功才返回按`RootEntry`稳定顺序排序的完整集合。允许项缺失可接受并不出现在返回集合中。ReadNames只保证两次观察的名称集合相同，并保证每个对象在各自单次验证点符合策略；不保证整个扫描期间同名对象identity恒定，也不声称对抗短暂替换后恢复的同账号行为。返回集合不具授权语义。
短暂handle始终由当前inventory操作拥有，逐个验证后立即关闭，不注册到root的长期子句柄表；关闭成功后才继续下一项。出错路径按打开顺序的逆序关闭所有仍持有的短暂handle；若任一底层Close状态unknown，立即停止关闭并以`verified_close_unknown`为主错误，保留其余handle引用并要求worker退出。最终枚举名称集合变化映射`verified_identity_changed`；目录枚举/身份查询系统错误映射`verified_io_failed`。成功结果只在in-flight计数释放前构造并整体返回，错误分支的结果必须为nil slice。

## 4. 可观测性与失败行为

每次open/enumerate/identity/close发出结构化事件：`trace_id, module="pilot.fs", event, object_kind, result, duration_ms, error_class`。不得记录绝对路径、成员原始输入、SID、uid/gid、file ID、key或内容。Open构造/策略验证失败必须在返回新handle前发生；运行期ReadNames/VerifyIdentity失败返回typed error并poison root。测试计数器必须证明失败时catalog mutation、SQLite、Coordinator、Start均为0。身份复核不一致立即poison root，不允许继续用同一对象。

## 5. 测试与验收

OS 专用测试在原生 Linux amd64/arm64 与 Windows amd64 执行；交叉编译只证明可编译，不计平台验收。测试必须使用真实临时目录/句柄和受控子进程/barrier，不使用依赖 sleep 的竞态断言。每项断言稳定错误类别、无句柄泄漏、后续副作用计数为 0、日志无敏感值。

| ID | 用例 | 验收断言 |
|---|---|---|
| ROOT-01 | 合法 anchor 与 allowlisted root/成员 | 返回句柄identity与同句柄系统查询一致；ReadNames完整验证成功；Close一次成功 |
| ROOT-02 | symlink/reparse/magic-link、路径穿越、未知成员、根层多余目录项、Windows大小写变体 | symlink/reparse/magic-link/穿越=`verified_path_invalid`；未知enum=`verified_member_invalid`；额外项/大小写重复=`verified_policy_mismatch`；额外项存在时，OpenVerifiedRoot及直接OpenDirectory/OpenMember均拒绝且不返回句柄；不得打开逃逸目标 |
| ROOT-03 | 普通文件硬链接/错误类型/owner/GID/mode/DACL/继承不匹配 | `verified_policy_mismatch`；目录link count不作为拒绝依据；不得创建、写入或SQLite open |
| ROOT-04 | 跨 mount/bind mount/不同 volume 与不支持能力 | mount/volume越界=`verified_path_invalid`；文件系统类型查询失败=`verified_io_failed`；可识别但不支持类型=`verified_policy_mismatch`；支持fs上的mount ID/statx能力查询失败=`verified_io_failed`；openat2和强制statx fallback分支分别覆盖 |
| ROOT-05a | root与一级成员两层的open→首次snapshot→发布线性化竞态 | 子用例分别在root/member返回handle后、首次snapshot前和snapshot后/父绑定复核前经barrier换绑；断言不发布错误identity、正确拒绝逃逸，且调用事件顺序证明barrier落点 |
| ROOT-05b | 返回后的目录/文件同名项替换 | 分别锁定POSIX directory/file 父名复核及Windows directory/file 不共享DELETE；拒绝替换或VerifyIdentity报identity_changed。首次open前同账号以同策略文件替换只能pin实际对象 |
| ROOT-06 | identity drift/目录项换绑/同一inventory重复file ID | `verified_identity_changed`并poison root；不继续枚举或打开兄弟项；下游计数为0 |
| ROOT-07 | enumerate/identity/close故障、部分open回滚、并发操作/close、重复关闭 | 枚举/身份错误稳定分类；drain timeout映射`close_pending`且无OS close，同一owner可重试；close unknown映射`restart_required`、不再次关闭且保留句柄；close成功重复调用不二次syscall |
| ROOT-08 | build-tag隔离与日志隐私 | `go list ./...`不含pilotsupervisor包；本切片源码均带`pilot_e2e` tag；敏感哨兵不出现在日志，事件字段完整 |
| ROOT-09 | 文件与目录link count、POSIX access/default ACL及Windows ACE边界 | regular file必须nlink/linkcount=1；目录不以link count拒绝；已发现额外ACL=`verified_policy_mismatch`，查询unsupported/denied=`verified_io_failed`；Windows ACE主体/flags/mask/继承按精确allowlist核对 |
| ROOT-10 | anchor借用所有权、部分打开失败回滚 | 测试fixture独占创建并验证anchor；Open返回后anchor仍有效；失败按逆序关闭新handle；close unknown要求worker退出 |
| ROOT-11 | 按句柄最小访问权 | Linux检查fd `F_GETFL`含O_RDONLY且API无文件内容读取/写/删/ACL变更方法；Windows以Worker token尝试read-data/write/delete/WRITE_DAC均被OS拒绝；只验证ReadNames枚举，不提供文件内容读取方法。同可信POSIX owner另行open/chmod不在本切片威胁模型内 |
| ROOT-12 | 文件系统/能力不支持 | 支持矩阵严格为Linux ext4/XFS、Windows NTFS；Linux在专用runner真实挂载NFS/SMB/FUSE/tmpfs/btrfs/zfs及unknown fs并断言拒绝，Windows在预置ReFS卷断言拒绝；mount ID/statx或DACL查询能力缺失均fail closed。runner或目标文件系统缺失则BLOCKED，不得skip计PASS |
| ROOT-13 | ReadNames名称集合一致性及全量返回 | 首枚举→逐项handle验证/关闭→末枚举；断言恰两次枚举、缺失项可选、RootEntry固定顺序。新增/删除和复核前替换返回错误且nil结果；关闭unknown要求restart_required。短暂handle关闭后同名替换不保证发现；返回集合不授权 |
| ROOT-14 | 错误边界与读取能力边界 | nil/closed anchor、坏epoch/enum、缺失项、无文件内容读取方法、closed handle；逐项断言errors.Is |
| ROOT-15 | poisoned对象与超时退出 | 每种poison后所有业务API拒绝；仅owner Close可执行清理/重试；close syscall最多一次；延迟enumeration/identity触发固定预算，父harness终止并在5秒内reap，无替代root |

测试manifest的叶子ID固定为：`ROOT-02/{symlink,reparse,magic_link,path_traversal,unknown_enum,extra_root_entry,case_variant,extra_entry_open_root,extra_entry_open_directory,extra_entry_open_member}`；`ROOT-03/{hardlink,wrong_type,wrong_owner,wrong_mode,dacl_extra_or_inherited}`；`ROOT-04/{bind_mount,mount_id_unavailable,unsupported_fs,required_capability_unavailable,fs_type_query_error}`；`ROOT-05a/{preopen_escape,root_open_before_snapshot_swap,root_open_before_publish_swap,member_open_before_snapshot_swap,member_open_before_publish_swap}`；`ROOT-05b/{directory_postreturn_swap,file_postreturn_swap}`；`ROOT-07/{enumerate_error,identity_error,close_unknown,binding_close_unknown_stops_root_close,close_drain_observes_unknown_close,child_close_drain_observes_unknown_close,partial_open_close_unknown,concurrent_enumerate_close,repeated_close,close_drain_pending_retry}`;`ROOT-08/{structured_log_fields}`；`ROOT-09/{file_link_count,directory_link_count,acl_absent,acl_extra,acl_query_error,dacl_exact}`；`ROOT-10/{borrowed_anchor,reverse_rollback}`；`ROOT-11/{posix_readonly_fd,windows_read_mask}`；`ROOT-12/{nfs,smb,fuse,tmpfs,btrfs,zfs,unknown_fs,refs,statx_unavailable,dacl_query_unavailable}`；`ROOT-13/{add_during_scan,remove_during_scan,replace_before_parent_recheck,short_handle_close_unknown,stable_nameset}`；`ROOT-14/{nil_anchor,zero_value_anchor,closed_anchor,bad_epoch,unknown_enum,missing_member,no_content_read_method,closed_member,nil_root_open_directory,nil_root_open_member}`；`ROOT-15/{enumeration_watchdog,identity_watchdog,close_drain_watchdog,poison_rejects_all,queued_operation_stops_after_poison}`。各OS只运行其GOOS适用项；平台manifest明确标注Linux/Windows，不得以不适用或缺环境跳过必需runner项。

Go父测试及逐项测试ID固定为 `TestVerifiedRoot/ROOT-01`…`ROOT-15`，ROOT-02/03/04/05a/05b/13/14必须再按攻击变体创建有名字的子用例；review manifest逐项列全测试名及适用GOOS。已实现的 Linux 用例包含 ROOT-01、ROOT-02 部分变体、ROOT-03 类型/权限模式（Linux当前仅测mode）、ROOT-07 关闭未知/并发排空、ROOT-08 结构化日志字段与隐私、ROOT-11 只读fd、ROOT-13 稳定集合、ROOT-14 错误边界及 ROOT-15 poison 后排队操作；它们不是完整验收矩阵。命令为 `go test -tags pilot_e2e -json ./internal/pilotsupervisor -run '^TestVerifiedRoot$' -count=1`；发现器必须确认JSON parent/subtest终态PASS、无skip、exit code=0和每个manifest项恰执行一次，并归档命令、stdout、stderr、OS/Go版本及结果。Linux只支持ext4/XFS，合法fixture根通过`PILOT_FS_TEST_ROOT`指定并预检`statfs`；不得用`testing.T.TempDir()`或`/tmp`作为正测根，除非核实落在ext4/XFS。Windows只支持NTFS，fixture预检卷类型并从`PILOT_FS_TEST_ROOT`创建；所有必需fixture未就绪即BLOCKED。Linux native覆盖openat2及强制statx fallback；bind mount场景须在专用具备CAP_SYS_ADMIN的Linux runner运行，不可skip计PASS。该runner需先证明mount namespace能力可用、创建隔离bind mount、卸载并清理；任何preflight失败都令阶段验收BLOCKED。Linux负测需在专用runner真实提供NFS/SMB/FUSE/tmpfs/btrfs/zfs和unknown fs；Windows负测提供ReFS。Windows native覆盖NTFS DACL/reparse/handle-relative API；fixture在受限管理员bootstrap中把测试树owner设为SYSTEM、DACL设为本SDD mask，然后以实际Pilot Worker token运行测试，不具备该runner时BLOCKED。POSIX fixture以worker euid和固定service gid创建随机树并显式核验owner/mode/ACL；不需要sudo。当前无产品路由，因此本切片不伪造Playwright覆盖；接入Gateway/页面前，必须另设计并评审真实浏览器E2E。Linux/Windows native runner未齐备或未实际运行时，本切片状态保持未验收。

本轮代码静态复核已确认：`OpenVerifiedRoot` 显式错误出口均记录 `root_open` 事件；close-unknown 后不会再次调用 OS close；Linux binding 临时 handle 的 close 故障可由测试 seam 注入；并发用例在失败路径释放屏障并有界回收。2026-10-20验证：Windows tagged 全包测试PASS（含Windows错误分类/空SID单测）；Linux/amd64 tagged交叉编译PASS；Linux原生ROOT矩阵因当前主机为Windows且无ext4/XFS fixture而BLOCKED。**这些结果不等于平台验收。**仍需在真实ext4/XFS Linux worker运行Linux适用矩阵；配置SYSTEM-owned NTFS fixture及由可信launcher提供固定Worker SID后运行Windows native矩阵。当前无生产launcher/SID注入入口，Windows默认空SID且fail-closed是本切片的明确未启用状态；不得用测试专用注入伪装生产接线。

## 6. 后续依赖与阶段出口

该切片通过后只提供可信的只读目录句柄原语。下一阶段另行设计并评审 bootstrap CREATE_NEW/Sync、catalog lock/journal 接线；再后续才做专用 control-ledger VFS、持久 request registry、Reaper/Gateway、产品课堂入口和多用户 E2E。任何阶段均不得绕开 docs/43 的真实 SQLite OS-handle 身份验证要求，也不得把 test-only 切片当作真实课堂试点授权。

## 7. 2026-10-20 测试清单补充与命令门

下列稳定叶子补充到§5 manifest，避免测试名暗示未覆盖的能力：ROOT-03加入`wrong_gid`；ROOT-04拆分`cross_volume`、`openat2_native`和`forced_statx_fallback`，分别断言跨卷拒绝、原生openat2路径被命中、fallback路径确被强制命中；ROOT-05b拆为`posix_directory_swap`、`posix_file_swap`、`windows_directory_no_share_delete`、`windows_file_no_share_delete`。ROOT-12每个不支持文件系统fixture均须断言准确稳定错误：跨mount/volume为`verified_path_invalid`，能力探测失败为`verified_io_failed`，已识别但不支持类型为`verified_policy_mismatch`；fixture或mount权限缺失属于job `BLOCKED`，不得转成Go test skip或PASS。

固定清单保存在`review/pilot-root-platform-manifest.json`，逐叶绑定本文件SHA、父测试名、subtest名、GOOS/runner、fixture、预期错误、发布/逃逸断言、下游零调用计数及artifact目录。发现器必须校验父测试确实存在并运行，缺父测试、零匹配、skip、重复、缺叶或意外额外叶均失败。Windows当前可运行子集只能使用`review/pilot-root-windows-contract-manifest.json`及其明确列出的两个合同父测试；不得对Windows运行`TestVerifiedRoot`并把零匹配解释为成功。Linux完整矩阵只在原生ext4/XFS runner执行`TestVerifiedRoot`清单；交叉编译、普通TempDir和unsupported fixture的预检失败都不计原生验收。








## 16. Run-root bootstrap authority update (docs/45)

This section supersedes earlier fixture wording in this document wherever it applies worker ownership to the outer `pilot-runs/` anchor. The outer allocation anchor is owned by the Reaper service UID/GID, mode `0710`, with no POSIX access/default ACL. The worker's fixed service group may traverse it but cannot list, create, rename, or delete sibling roots. Each `<RunEpoch>/` root and its allowed members are owned by worker euid/fixed service GID; directories are `0700`, regular files `0600`, and have no extra ACL. `AnchorHandle` pins and verifies the outer anchor. `OpenVerifiedRoot` only opens a root whose allocation is durably completed by Reaper; it never creates, repairs, or removes a root. Native fixtures must separately validate both ownership domains. Add stable ROOT-16 children for anchor owner/mode/ACL, worker traverse-only behavior, sibling mutation denial, and epoch-root ownership/mode.

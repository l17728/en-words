# Catalog journal 架构评审

2026-10-19；只读对照 docs41 §109 与第42/56段、§108。版本 SHA256：`20da1d695556bf451a2533cd6325343c55fee59d99f201db66ba7a4b874c21c4`。

**结论：BLOCKED，2项P2。** 未运行测试/构建/服务，未改规格或代码。Catalog/supervisor仍是设计，不以已有sidecar测试代替验收。

## 通过项

- Catalog是全局授权状态源；内存degraded不能代替持久隔离。固定身份append-only文件、CRC仅检测破损而非认证，符合可信监督器/ACL威胁模型。
- 帧的长度、重复长度、magic/trailer、CRC-32C、canonical四字段、全局连续generation与容量界限足以形成可实现的严格codec。完整已落盘帧在重启后权威，不试图猜测前次响应是否到达，语义合理。
- 双阶段授权先短暂读catalog并释放，再拿case锁，最后重新拿catalog锁复核；所有嵌套顺序case→catalog，catalog持有者不得等case。按此约束不存在明显ABBA；catalog不能覆盖DB、Planner或删除。
- quarantine先Sync再删、deleted在全部文件删除及父目录Sync后写入、永久tombstone不复用ID，保留了崩溃隔离事实。

## CAT-01：case终态粒度与既有多课/重试合同冲突（P2）

§109只允许ready→open_recovery_required一次；任一成功或已知失败即转committed/terminal_failed，且禁止任何新command。第56段还要求排他case生命周期锁从open持续到所有DB/sidecar/key句柄关闭。因此同一case第一课Start将封住另一课，或另一课只能等待整个持久连接生命周期结束。

这与docs41既有“同case不同lesson并行”、A/B混合active/created、同case不同课交错sidecar事件，以及确定性失败后显式新command合同不一致。并行测试若用两个独立case/DB，也不能替代同库混合生命周期验证。

需明确选择而不是增加隐式回边：

1. Catalog记录整个case运行会话。监督器一次取得case lease，建立当前进程可用的已授权会话；课级操作共用该授权而不递归取得OS case锁，仍各自lesson锁/SQLite事务。所有在途操作结束、句柄关闭并核完整case处置后才记case终态；重启open仍仅恢复，不能把旧会话视为新鲜授权。或者
2. 明确收窄一case一次Start，更新受影响的A/B并发、失败后新command及混合库验收边界，并说明原组件混合库测试独立保留。

首选第一种的短小case-session合同，不建议为每个command再造第二套catalog状态机。必须加第一课完成而B仍在途的确定性验收，防过早封板或覆盖隔离状态。

## CAT-02：cleanup_quarantined中断后的恢复入口未闭合（P2）

§109说cleanup失败留cleanup_quarantined且“denies all opens”；第42/56段要求每次case/文件open先catalog授权，且删除过程需安全校验和句柄。监督器在quarantine已Sync、删除未完成时崩溃后，重启处于该状态，既不能普通打开，也没有明确可重新安全打开剩余目录/文件并继续cleanup的例外。允许的cleanup_quarantined→deleted迁移因此缺可执行恢复路径。

应只禁止业务/receipt/Planner打开，另定义监督器cleanup-only授权：同一case锁+catalog二次复核，仅OpenVerified剩余待删对象，不打开SQLite/不查receipt/不启动child，缺失对象按已删除处理，链接/身份异常隔离；完成并Sync父目录后仅追加deleted，不重复写cleanup_quarantined。覆盖崩溃发生于删除前、删一部分、目录已删但deleted尚未Sync三个位置。若选择永久人工处置，必须明确该状态不可自动恢复及工具边界，不能同时暗示普通cleanup可重试。

## 非阻断同步与复杂度

- 第42段“Sync不确定则启动全局fail closed”需让位于§109的可观察规则：重启完整有效帧按链接受；部分/坏帧拒绝。不能要求新进程识别未持久记录的旧Sync异常。
- 同catalog并发首次CREATE_NEW需指定获胜者初始化首帧，其他进程等锁后完整读取；禁止因看到初始化中的空文件抢先破坏正常启动。首次真实崩溃留下空文件仍可按当前规则失败关闭。
- 句柄级内部open helper必须接收已持有的case授权证明，不重新获取同一非重入OS锁；否则“每次open都两阶段”会造成自锁。
- 16MiB/65,536上限且无压缩对有限测试catalog可以接受，但应在追加前检查拟写帧，拒绝容量不足不能把原合法文件写成超限。全链读取/验链为O(N)，无需现在增加共享缓存或第二状态库；记录容量边界不代表生产无限期运行可用。
- case锁载体需在待删case目录外或明确删除后的稳定获取机制，不能删除锁文件后让新监督器锁到不同对象；属于实现前平台装配检查。

以上不要求改变任何教学策略或默认产品路径。闭合两个粒度/恢复接口后再进行实现与平台验收。

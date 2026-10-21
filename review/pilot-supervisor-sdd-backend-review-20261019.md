# Pilot Supervisor 后端域设计复审

- 评审对象：`docs/42-Pilot隔离运行监督器SDD.md`
- SHA-256：`8FE8F24EDC16D3B7272368EFCDAB25795AA25845A8A01C12FDEEAA4CA6CF7B42`
- 结论：**FAIL（2 项阻断）**。仅静态设计评审，未修改被审文档或源码，未运行测试。
- 旧请求 SHA `79436BF0CF92CD7ED04B6D0C0EE2ECD9AE44369733655287C1F73E1E468B0876` 已被新候选替代；本结论仅针对上列新 SHA。

## B42-01：首次 catalog 的构造器与已授权 CreateCase 责任冲突

§3 的 `OpenPilotSupervisor(ctx, runEpoch, mode)` 不接收 create grant，却要求按 §7 初始化 catalog。§7 明确首次启动生成 case ID、写 provisioning 首帧并继续初始化。另一方面，`CreateCase(ctx, grant)` 又负责首次 catalog 路径，且 §3/FI-AUTH-01 要求无有效 grant 不进入 CreateCase、拒绝时不创建 case。

缺少一个可执行的唯一合同：构造器创建的首个 reservation 是否只是未绑定占位？由哪一个并发 create request 领取？何时绑定 request_id/digest/principal？首次请求失败或 worker 在领取前重启如何区分内部 bootstrap 与结果未知的 create？现有 worker 内存去重和 Gateway selector 映射不能补足这个首帧归属边界。

建议采用惰性首建：构造器仅建立安全 root、日志和永久 bootstrap lock；不存在 catalog 时保持明确的 uninitialized 状态，不创建空 catalog 或 case。第一个验签成功的 CreateCase 在 bootstrap 锁内执行 §7 首帧协议；后续请求在锁内复核并走已有 catalog 路径。首次 CreateCase 结果未知仍废弃整个 run。同步明确无 catalog 时构造器句柄集合/Close 行为与并发首请求测试。若采用另一方案，也须给出同等明确的授权、绑定及崩溃状态机。

## B42-02：重启恢复 SQL 的查询键和冻结参照缺乏来源

§9.1 R-LESSON 按 `id=?`，R-TRANSITION 按 lesson_id 加“sidecar绑定 command_id”，并要求对照原创建计划、nonce、fingerprint、计划时间。末段又限定参数只能来自已认证 case 绑定和 HMAC 有效 sidecar。

然而 §3 Gateway 持久于 worker 重启的内存映射仅描述 case selector/principal、request_id/digest/scope；docs41 §3 的严格 sidecar 事件字段只保存 command_key_hmac/command_hmac 等摘要，不含可直接查询的 command_id，也不能反推 HMAC。同样未规定 worker 重启后取得完整冻结创建参照及 lesson_id 的恢复协议。仅有“一个 lesson”的 fixture 基数，不能授权实施者自行添加 SELECT 或信任 DB 自报身份。

须明确一个受信恢复描述符或固定有界查询发现协议。例如 Gateway 在命令派发前原子保留 case 对应不可变 typed 恢复上下文，并将其纳入新签名 grant 的 request_digest；重启 worker 从该授权上下文取得查询键和参照，再逐项比对 DB/sidecar。也可新增严格只读发现 SQL，取得候选 command_id 后重算 command_key_hmac 并逐项验证；但必须更新白名单、查询顺序、无 receipt profile、结果上限和精确计数。不得从 HMAC 推回 ID、接受浏览器未认证字段或越过现有 SQL 白名单。

## 已收敛边界与实施注意

创建结果未知时废弃整个 run，而已绑定 case 的 worker 重启保留 Gateway owner/session，边界已明确。恢复不重放 Planner/Registry、committed 完整性失败不擅改 catalog、pending_unknown 不假称回滚、cleanup tombstone/stable lock 保留及 Close unknown 要求旧 PID 退出，方向通过。

实现时须把 §9.1 的领域字段投影转换为实际 SQLite 列名（例如现有 AuditLog 的 ActorType/TraceID/DurationMs 对应 GORM 命名映射），并提交固定 SQL golden；不能将领域字段列表直接当已验证 SQL。PRAGMA/schema 查询完整顺序也须以第一条 database_list、第二条 query_only 为准，不能机械按 §9 的展示列表先执行 integrity_check。

本评审不证明 Windows/Linux 文件系统适配已可用，不放行默认 App/Room、真实教学、模型或音频。两项阻断修订后需对新文档 SHA 复审。

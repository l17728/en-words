# Pilot Supervisor 后端域最终候选复审 R3

对象：`docs/42-Pilot隔离运行监督器SDD.md`

SHA-256：`12B20F4DDF08129E482DD372320F98BCCC0D90DB243F4847EF44CDDE51D4ACB0`

**PASS。B42-01、B42-02 均关闭，新增后端设计阻断 0。**

在 R2 完整复核基础上再次读取最终候选的 §3、§7、§8、§9.1：

- OpenPilotSupervisor 仅准备 root/日志/控制锁及 replay 已有 catalog；首个有效 CreateCase 才分配 case ID、创建 catalog 首帧。不存在无授权首个 case 的构造器路径。
- 固定 `POST /__pilot/v1/cases` 只签发 create scope；客户端不能指定 scope。Idempotency-Key 必填、严格格式且只有一份；同会话同 key/digest 合并，不同 payload 冲突，跨会话隔离。
- 首建期间未知结果废弃整个 run；仅已绑定 case 的 worker 重启可保留 Gateway 授权关系。absent catalog 的 resume 需 Gateway 证明从未派发 create；inventory 恢复例外不允许未知 create 重新暴露 selector。
- R-LESSON 有界发现唯一 lesson，R-TRANSITION 按该 lesson 读取至多两行，成功结果恰一行并对 command_id 重算 sidecar HMAC。冻结事实、receipt、audit 和 outbox 的逐向验证保留；无 receipt 仅返回 pending_unknown，不能重放或声称回滚。

非阻断编辑建议沿 R2 保留：`§28` 和 `docs41 §43` 应改为实际章节/公式定位；§10 的首个 case-lock 概括行应区分首次已存在 provisioning 首帧与后续未 reservation 的 orphan。实施应严格使用 §7 明确优先的详细合同。

仅静态设计评审，未运行测试、修改被审文档或产品。PASS 不代表 Windows/Linux adapter 已验证、监督器代码已实现，也不放行默认 App/Room 或真实模型、音频教学；各平台故障注入、固定 SQL golden 和后续 E2E 仍须完成。

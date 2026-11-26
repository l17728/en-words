# Pilot Supervisor 后端域最终复审 R4

对象：`docs/42-Pilot隔离运行监督器SDD.md`

SHA-256：`BFCD1B6BF85A171563DC2FC2D42A890356145BFC5905332EAECF0C416EC958F4`

**PASS：B42-01/02 继续关闭；新增阻断 0，前轮两项非阻断编辑意见已关闭。**

本轮在 R3 完整设计评审基础上，实际核查最终候选的接口、首次与后续 case-lock 故障表、恢复 HMAC 公式引用和 HTTP 测试分层：

1. CreateCase 未绑定结果按当前 §3 Gateway 协议处理，未知结果仍废弃 run/root；恢复 command_key_hmac 公式准确指向 docs41 §3，不改变有界 SQL 发现及 HMAC 反核合同。
2. §10 明确首次 stable lock 在 catalog reservation 之后，后续 stable lock 在 reservation 之前；首次恢复只处理同一 ID，后续 orphan 永不删除/复用。Gateway 已派发且结果未知时仍不启动恢复 worker，不暴露 selector。
3. Playwright 覆盖浏览器能够构造的非法 Idempotency-Key；两条真实同名 HTTP header 由原始 HTTP 集成夹具覆盖。分层不削弱拒绝合同，两者均在 request_id/IPC/CreateCase 之前拒绝。

未运行测试、修改被审文档或产品。结论仅为后端域设计 PASS；真实平台、故障注入、SQL golden、代码和后续 E2E 验收门保持。

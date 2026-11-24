# Catalog 架构复审 r2

结论：**PASS，0 阻断**。只读核查 docs41 当前 SHA-256 `c2e2454f81013eccb451ef527ed6733f6211865a7f0030d7d3d66d363971754d`；未运行测试或修改设计/产品。

- **CAT-01 已闭合**：§32、§96、§109.3 将一个 case 限为一个目标 Start；同 case 串行，不同 lesson 在不同 case 并发。确定失败后的新 fixture 重试与未知提交恢复分开。
- **CAT-02 已闭合**：生命周期锁位于独立受保护根，删除 case 目录不丢锁身份。专用 cleanup-resume 不打开 SQLite、sidecar 或 key；允许已经删除的成员，在父目录同步后追加永久 tombstone。失败继续隔离。

实现仍须遵守 §56：取得稳定 case 锁后重读 catalog，不能使用锁前授权；目录已不存在也须先同步父目录。锁对象不得在竞争者可能存在时删除重建。删除后的 catalog 写失败不授权重建或重跑 Start。独立 case 并发不替代同数据库多 lesson 的仓储测试。

非阻断文字建议：§109.4 第175行要求“合法 canonical payload 恰256字节”和“合法 catalog 恰16 MiB”，在固定四字段、固定 case ID、有限状态及最多65536条连续 generation 下不可达。边界测试应区分 framing 层长度上限与语义合法 journal，不应填充 JSON 或放宽 canonical 合同来凑长度。

此结论仅关闭架构生命周期问题；不代表监督器、平台锁/ACL或崩溃恢复已实现及验证。

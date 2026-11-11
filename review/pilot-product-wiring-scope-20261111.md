# Pilot v2 接线边界与最快业务验收路径（2026-11-11）

## 源码事实

`main.go` 仅把 `ENWORDS_DUPLEX_PILOT=1` 映射为 `DuplexPreflight`。`api/router.go` 注册的 Pilot 相关路由只有 `duplex-capabilities`；响应明确 `mode=preflight`、`native_duplex=false`、`automatic_teaching=false`（`duplex_preflight.go`）。`store.PilotStore` 已有隔离创建与 `LoadCreatedState`，但注释明确“无应用或 HTTP 入口”“读取不授权进入 Room”。当前产品的教学、跟读和答题仍由 legacy Hub/Room 处理。不能把隔离持久层测试解释为 Pilot 学生旅程通过。

## 交付顺序建议

最快可用的学生闭环已经是已验收的 legacy Mock/文字路径，见 `business-closure-20261111.md`。先完成本机 ASR 恢复后的合成样本、真人语音与课堂试点验收；这是学生实际使用的直接缺口。Pilot v2 属下一阶段接线，不应为它延迟该闭环或修改已验收的 legacy 入口。

Pilot 的下一开发切片先完成**显式隔离试点课**的持久模式、受信身份和全部旧入口分流，并在真实双连接条件下证明旧 Hub 无法进入试点课。完成这道门后，才接 `created → active` Start：冻结计划、同一 SQLite writer 事务写 checkpoint/receipt/outbox、提交后才发布。现有课仍走 legacy；分流未证明前保持 Pilot 路由关闭。只有 Start 事务不能宣称浏览器无双读；派发、ACK、恢复和确认输入的分歧处理须另行验收，不能与旧 Hub 混写。

## 最小验收门

1. Go 集成：先证实两学生一 legacy 一 Pilot 并发的持久分流与绕行入口拒绝；随后验证同课重复 Start 得同一 receipt、COMMIT 失败零发布、重启后只重放既有 outbox。
2. 浏览器：隔离 SQLite、真实后端进程、两个用户；legacy 全旅程不变。Pilot 的无双读须等派发、ACK、恢复接线后验证；确认输入则另覆盖两次答题机会与迟到/乱序/超时结果。
3. 记录 trace、lesson、checkpoint version、decision、effect index 和耗时；不得记录原始音频或密钥。架构、后端、安全、测试审阅逐入口写门后再开发。

本记录为现状与实施顺序分析，不解除 `docs/36` 对完整产品接线的未完成状态。

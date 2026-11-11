# Pilot v2 接线边界与最快业务验收路径（2026-11-11）

## 源码事实

`main.go` 仅把 `ENWORDS_DUPLEX_PILOT=1` 映射为 `DuplexPreflight`。`api/router.go` 注册的 Pilot 相关路由只有 `duplex-capabilities`；响应明确 `mode=preflight`、`native_duplex=false`、`automatic_teaching=false`（`duplex_preflight.go`）。`store.PilotStore` 已有隔离创建与 `LoadCreatedState`，但注释明确“无应用或 HTTP 入口”“读取不授权进入 Room”。当前产品的教学、跟读和答题仍由 legacy Hub/Room 处理。不能把隔离持久层测试解释为 Pilot 学生旅程通过。

## 交付顺序建议

最快可用的学生闭环已经是已验收的 legacy Mock/文字路径，见 `business-closure-20261111.md`。先完成本机 ASR 恢复后的合成样本、真人语音与课堂试点验收；这是学生实际使用的直接缺口。Pilot v2 属下一阶段接线，不应为它延迟该闭环或修改已验收的 legacy 入口。

Pilot 下一可交付切片只能是**显式隔离试点课**的 `created → active` Start：受信身份绑定、冻结计划、同一 SQLite writer 事务写 checkpoint/receipt/outbox、提交后才发布。现有课仍走 legacy；若不能证明 scope 分流与所有旧入口拒绝，保持 Pilot 路由关闭。只接一个 Start 按钮不能宣称课堂可用；确认输入、结算和恢复随后须共用同一持久协调层，不能与旧 Hub 混写。

## 最小验收门

1. Go 集成：两学生一 legacy 一 Pilot 并发；同课重复 Start 得同一 receipt；COMMIT 失败零发布；重启后仅重放既有 outbox，不重选话术。
2. 浏览器：隔离 SQLite、真实后端进程、两个用户；legacy 全旅程不变；Pilot Start 后刷新/断线重进无双读、无双判。接入确认输入后再增加两次答题机会与迟到/乱序/超时结果用例。
3. 记录 trace、lesson、checkpoint version、decision、effect index 和耗时；不得记录原始音频或密钥。架构、后端、安全、测试审阅逐入口写门后再开发。

本记录为现状与实施顺序分析，不解除 `docs/36` 对完整产品接线的未完成状态。

# 正式组件全量 Go r16：覆盖门未通过

本轮为正式整合 16 文件后的新树，不沿用隔离定向或 r15 结论。测试及 vet 通过，但 store 覆盖不足，阶段验收未通过。完整逐模块、逐组件文件与逐函数数值及原始摘要 SHA 见同名 JSON。

- Go 源码（含测试）SHA256：`8b1810dc95671aa77eff40589e740203219a96b646ad3976159bf3e71e53c30a`，前后不变。
- 23 包通过；顶层 575 项执行，566 通过、9 显式跳过；含子测试 1450 项执行，1441 通过、9 跳过。新增组件 31 个顶层测试全部纳入并通过。
- `go vet -p=1 ./...` 返回 0；完整 coverage/vet 阶段 703.14 秒。没有执行本树 race。
- store：1349/1742，**77.44%**；新增 10 个组件生产文件：835/1170，**71.37%**。两者均未达到 80%。其他已有核心模块覆盖门通过。

原命令为 `go test -p=1 -count=1 -json -coverpkg=./internal/... -coverprofile=<独占 r16 路径> ./...`，随后 vet；`GOMAXPROCS=2`、本地工具链、离线依赖。覆盖聚合沿既有跨测试包相同块取最大命中口径。原始文件保留在 `server/.test-artifacts/final-runtime-coverage-r16*`，没有覆盖旧轮结果。

主要遗漏是 legacy runtime 的实际有效负载与损坏负载审计（`auditPilotLegacyPayload` / `pilotRequired` 均 0%，整个文件 43.68%），以及私有已迁移 scope 预留 helper（0%，所在文件 3.57%）。其余低覆盖文件为 schema 76.74%、verifier 73.77%、creation plan 73.13%、identity 76.34%。这说明通过现有 31 项不足以证明这些边界，不是忽略测试前缀。

后续优先补真实四类历史 runtime 的合法迁移、所有权和类型损坏拒绝、迁移失败无残留，以及 scope 新建、幂等读取、拒绝 pilot、事务回滚和失效事务边界。只补测试，不通过减少分母或放宽合同达标；新增测试尚未执行。

9 项跳过分别为 `TestOptInProductionFB001Diagnostic`、`TestStartupCleanupHarness`、`TestBrowserOverdueHarness`、`TestOptInIndependentASRThroughOwnedProductRoute`、`TestChatReal`、`TestChatJSONReal`、`TestChatRefuseLongInput`、`TestOptInLocalSpeechQuality`、`TestStartupFailureHarness`。它们为显式 opt-in/真实服务或专用 harness，本轮没有因此调用模型或共享服务。

组件仍不可通过 HTTP/Room 使用，默认 Open 不创建 v2。历史 21.678 秒与 22.594 秒课堂提交长尾仍未关闭；本轮测试通过不替代该性能问题的验收。

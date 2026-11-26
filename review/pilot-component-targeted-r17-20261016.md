# r16 覆盖缺口补测：完整 store 包专项

仅新增 `pilot_runtime_compatibility_test.go` 与 `pilot_scope_contract_test.go`，4 个顶层行为测试。架构实际复核通过后执行；初整合历史 manifest 不修改，10 个生产组件文件逐个 SHA 仍与初整合一致。

专项 `go test -p=1 -count=1 -json -coverprofile=... ./internal/store` 返回 0，61.12 秒。62 顶层通过，含子测试 296 通过，1 个专用 StartupFailureHarness 显式跳过。当前 Go 树 SHA 为 `b2bac383acace0b1c957cf607f0a785d6e8196f85a78740a145d6eb06a6c5ba2`，前后不变。

新增组件覆盖 **945/1170 = 80.77%**。仅 store 包直接测试的整个 store 覆盖 **1340/1742 = 76.92%**：该值没有 API 等包对 store 的交叉覆盖，不能与历史全量口径混用。与 r16 相比新增测试覆盖 110 条此前未覆盖语句；两份 profile 的诊断并集为 83.75%，它不是新树全量验收结果。正式 r17 全量 coverage/vet 另跑，原 r16 门失败永久保留。

测试锁定真实 Machine Snapshot、Result、历史 settled 两形态和 agent_result 的合法迁移及 26 种损坏数据拒绝；每个变异先通过同一合法负载的数据库审计，失败后原库不变且无 v2。scope 用例先取得同事务 writer/schema 校验，再直接调用 helper 首次创建或拒绝 pilot；涵盖跨池幂等、不更新时间、回滚、失效事务及真实 SQL 插入失败。它们验证私有组件，不代表旧应用已接入 scope 保护。

证据：`server/.test-artifacts/pilot-store-coverage-r17-targeted*`；完整统计和逐文件覆盖见同名 JSON。没有修改生产行为、调用模型或重跑浏览器。

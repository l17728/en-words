# Pilot 授权幂等 SDD 评审结论

## 当前评审快照

- `docs/42-Pilot隔离运行监督器SDD.md` — SHA-256 `FF2C4B2936D148EB48F17787054B5197DDB3AACB3AA458892C6B59F65619642C`
- `docs/43-Pilot授权幂等与父进程身份绑定SDD.md` — SHA-256 `79705707714E6E41A55134C95618FE384538629625D7CC68CE44BAB74B7875E6`
- 评审日期：2026-10-19

架构与后端对最终快照复核 PASS，阻断项为 0。测试设计复核了前一微版本 `6E859C5730AD91F4703F1CACFEF124A03C5B99282BBE126C11AB1AB5D0EB44B8` 的父测试/subtest 发现门；最终差异只增加必须检查 JSON 终态 PASS 和进程退出码为 0，架构、后端对该差异复核 PASS。安全域复核了前一基线 `C50FB49ADBC05B6500FCC27070E0AA2FEDB8B8FB884AEBC7B6F0332092C4C63C`；此后修改仅涉及测试发现/终态判定，没有变更授权、身份、数据或执行边界。

设计收敛包括：`CreatePinnedCaseRoot`/`ExecutePinnedCleanup` typed allowlist 与 Reaper 执行权；receipt durable ACK、Gateway terminal CAS durable ACK 后方可报告成功；Cleanup expected identity 锚定已提交创建 receipt、同一 principal 和不可变 intent；加锁后核验、initial-present witness durable 后方可 unlink；首次 absent/unknown 转 indeterminate，仅已有 durable witness 的恢复分支可据同对象 absent 判完成；anchor 缺失、来源错配、跨主体请求在 prepared 前拒绝并保持零副作用。测试发现门分别校验目标平台及相同 tags 下的父测试清单、逐项执行的 subtest ID、JSON 终态 PASS、无 skip 和进程退出码，避免父测试缺子用例及空匹配假绿。

## 原型与验证范围

当前仅实现 `pilot_e2e` build tag 下的纯状态/授权/恢复合同原型：

- `server/internal/pilotsupervisor/cleanup_contract.go` — SHA-256 `AD5FA3EF274B89092A65EB9D1EBC5D68538F738BBE19FA3F467B94289B73825D`
- `server/internal/pilotsupervisor/cleanup_contract_test.go` — SHA-256 `D34C4412499F07FC4961E21DFC3BEBBFCCD86720CF9D04D6965F85F9A9F31859`
- 定向命令曾通过：`go test -tags pilot_e2e ./internal/pilotsupervisor -run 'TestPilot(CleanupAnchor|CleanupOwnerClaim|CleanupRecovery|LedgerTransition)' -count=1 -v`；4 个顶层测试、58 个子场景。该结果仅覆盖纯模型合同，不验证 durable I/O。
- 文档最后修改仅调整测试发现门文字；`git diff --check` 通过，尾随空白 0。

## 尚未完成的阶段门

本 PASS 仅批准进入下一实现阶段，不代表 docs/42 本轮重新评审，也不代表 pilot 就绪。Durable ledger/VFS/recovery journal、真实 IPC/Gateway/Supervisor 接线、Linux/Windows 原生故障注入 runner、Playwright 多用户课堂 E2E 和隔离真实课堂验收仍未完成。原生 runner 在完整测试实现及发现门落地前保持 **BLOCKED**；不得开放课堂入口或上传真人音频。真实试点还需 HTTPS 安全传输、负载/长尾测量、降级恢复及教师验收。

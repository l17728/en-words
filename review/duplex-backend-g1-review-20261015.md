# 全双工 G1 后端组件验收（2026-10-15）

本轮只实现独立协议状态组件、显式迁移的 SQLite 提交组件、严格远程文本适配器与学生能力预检。没有将这些组件接入既有 Room/Teacher Agent/结算/音频传输链；`native_duplex`、`automatic_teaching` 始终关闭。组件通过不代表真实全双工课堂或物理声学门通过。

## 独立审查与修复

主审及架构实际审查已闭合：撤销输出后旧 lease 不可复活；最终文本 UTF-8/长度校验；JSON 精确小写字段、重复/别名/null 拒绝；ID 内部 Unicode 控制/空白拒绝；规范化只移除单个末尾中英文句号，问号/感叹号/省略号及连续混合句号保留；逻辑机会唯一键为 lesson/epoch/input_revision，换 utterance 不可再次提交。

确认计算共用 8 秒截止。ASR 一致只进入 verified；coordinator 完成判分后须调用 BeginCommit 检查原截止。进入 committing 后取消只撤输出，不能谎称数据库未提交；未知提交结果保留 committing 并查 receipt，已确认回滚才 CommitFailed。MarkCommitted 反映实际数据库结果，不能绕过 BeginCommit。

持久化在同一固定物理连接事务内写 checkpoint、receipt、outbox 及可信回调；owner 验证后先查重复 receipt，再做新提交 CAS。故障用例覆盖 callback、checkpoint、receipt、outbox、延迟外键 COMMIT、取消、panic 的全回滚，以及双连接并发重复提交。回调是未来可信工具接线边界，不是模型任意 SQL 接口。普通启动未自动迁移这三张试点表。

## 定向证据

`.test-artifacts/duplex-components-final.txt`：11 顶层测试、54 条含子用例通过；duplex 97.6%。选择性 store/api 包覆盖率不作为全包质量门，最终采用全量跨包合并统计。

新增 `store/duplex_repository.go` 定向函数覆盖：NewDuplexRepo、Migrate、CreateCheckpoint、LoadCheckpoint、validDuplexString、LookupReceipt、ListOutbox 均 100%；Commit 90.6%。这是行为故障测试结果，不代表整个 store 已由本次定向验收。

红证据保留于 `.test-artifacts/duplex-review-boundaries-red.txt`、`duplex-case-alias-red.txt`、`duplex-commit-gate-red.txt`。协议没有可用产品入口，故组件缺陷用单元/SQLite 集成锁定；能力预检用户路径另由 Playwright 多用户用例验证，不伪造已接入课堂的 E2E。

预检真实路由 14 子例覆盖 flag 开/关、本人/他人/管理员/未登录、非法/不存在课次；开 flag 也只允许设备预检，不能开启自动教学。远程适配器 required 字段由业务调用者负责，严格 JSON 解码不等同完整 JSON Schema 执行。

## 构建与最终门

浏览器专项产物：`.test-artifacts/duplex-preflight-server-20261015.exe` SHA256 `b73c9a80719f6a7cec401d39d93e7a2c9f44ea0f552114c0f81d76195c50465c`；harness SHA256 `5eb847daa82382f372226de88f287c1e7828a02e400c3823a3d63b492af5801d`。

r12 全量 coverage/vet 已退出 0，耗时 129.42 秒：511 顶层、1121 含子用例通过，7 项显式 opt-in 跳过，22 个测试包通过；另 1 个包无测试。源码 SHA256 `557da0fadbb0236110381ed77be4571d42231e0e4c75e19d94972d7bbe5e3a45`，执行前后不变。证据 `server/.test-artifacts/final-runtime-coverage-r12-summary.json`、对应 JSONL、profile 与 vet 文件。

沿历史一致口径使用 `-coverpkg=./internal/...`，将多测试二进制的同源码块命中合并；没有将单个测试二进制打印的跨包百分比误当包覆盖率。

| 包 | 语句覆盖率 |
|---|---:|
| agent | 97.42% |
| api | 80.96% |
| auth | 89.94% |
| classroom | 91.30% |
| config | 87.59% |
| duplex | 97.60% |
| feedback | 94.77% |
| judge | 98.36% |
| llmzhipu | 100.00% |
| localmodel | 90.48% |
| memory | 93.10% |
| obs | 92.59% |
| remoteprovider | 96.67% |
| scheduler | 90.47% |
| scripts | 93.91% |
| store | 87.12% |
| tools | 93.75% |
| voiceclone | 94.64% |
| voicecmd | 91.40% |
| wordbook | 91.50% |

全部核心 80% 门及状态机/调度/判分/反馈/duplex 的 90% 门达到。7 个跳过分别为 ProductionFB001Diagnostic、BrowserOverdueHarness、IndependentASRThroughOwnedProductRoute、ChatReal、ChatJSONReal、ChatRefuseLongInput、LocalSpeechQuality 对应 opt-in 测试；6 项须真实模型环境，harness 由 Playwright 独立启动。此处未冒称真实模型测试已执行。

r12 race 同树退出 0，258.53 秒；511 顶层/1121 含子用例通过，7 skip，22 测试包通过，无 race 报告。前后 source SHA 与 coverage 完全一致。证据 `server/.test-artifacts/final-runtime-race-r12-summary.json` 及对应 JSONL。执行使用便携 clang、CGO_ENABLED=1、GOMAXPROCS=2、`-p=1 -count=1 -race ./...`；与浏览器串行，未调用真实模型。完成后已释放资源给最终浏览器全族，未以本报告代替其结果。

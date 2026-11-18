# 简单跟读与精确控制：后端验收（2026-10-15）

本报告对应 docs38 第一片。跟读独立进行目标词匹配；quiz 的英文短答、释义、同义词及 L2 合同保持。尚未接入 docs36 统一课堂提交链，也不代表原生全双工或真实模型质量验收完成。

## 实现与审查

- `judge/repeat.go` 按 NFC、Unicode Latin、完整词组/重复/冠词进行匹配；仅词内直/左右弯撇号与连字符可用。目标侧省略号转为字面骨架，不猜插槽填词；输入侧引用、否定附加文本、省略号及超长输入不能借清洗通过。
- `api/ingress.go` 保留完整原文供控制与跟读使用，quiz 单独沿原 sanitize 链。避免 `cat<不要判对>` 或截断后的前缀被当成正确跟读。
- `api/hub.go` 跟读不再调用语义 Judge，不生成 pendingJudge 或判分前快照；真实成功仍走原正常推进与工具事务，不能宣称所有数据库写为零。取消及陈旧 envelope 仍受保护。
- `voicecmd` 所有控制按完整规范化句匹配，撤销前缀、编辑距离及语气词猜删。单个末尾句号/感叹号允许；问号、连续混合标点、引号和附加文本保留。反馈正文捕获独立保留。quiz 中未匹配命令的输入仍按原普通作答处理，并非一律免耗机会。
- `repeat_evaluated` 记录 lesson trace、word_id、purpose、source、result、duration_ms；不记录学生原文或目标词面。结果区分 matched/no_match/canceled/unsupported_target。unsupported 保留原跳过及超时出口，不伪称学生读错或已通过。
- 主审与架构实际代码审查已闭合，包括左弯撇号真实词库兼容、完整 raw 入口和控制规范化边界。未修改词库、共享模型、数据库配置或 AGENTS。

## 红绿证据

目录前缀为仓库根 `.test-artifacts/`；原失败文件均保留。

| 证据 | 结论 |
|---|---|
| simple-confirmation-red-local-only.txt | 旧行为真实红：猫释义误通过跟读、错误英文调用 L2、否定/问句误命中控制 |
| simple-confirmation-boundaries-red.txt | raw sanitize 丢失正文、Unicode Latin 与超长规范化边界真实红 |
| simple-confirmation-left-apostrophe-red.txt | 两条真实种子左弯撇号目标兼容红 |
| simple-confirmation-green-r3.txt | 上述三包最新定向绿及二进制构建前验证 |
| simple-confirmation-room-final.txt | 实际 Room 6 子例绿；含 unsupported 固定日志、不推进、LLM 0、pending nil、预判分快照不变 |

首次空输出 Go 启动尝试属于基础设施中止，不当作产品红。最新 Room 定向包测试体 2.198 秒，整体启动/编译等待另计，不冒充产品请求延迟。旧模糊控制用例按明确新合同修订；保留“下一个！”合法兼容。

浏览器独立报告为 `review/repeat-contract-browser-20261015.md`：原两冻结用例旧服务真实红/新服务绿；后增 Unicode 与 unsupported 旅程另冻结。不能以 Go 回归替代用户路径证据。

## r13 完整覆盖率与 vet

证据：`server/.test-artifacts/final-runtime-coverage-r13-summary.json`、同前缀 `.jsonl`、`-vet.txt`、`-source-before.json`。执行 `-coverpkg=./internal/...`，同源码块跨测试二进制以最高命中计数合并，保持历史口径。

- exit 0、vet exit 0，437.03 秒。
- 516 顶层 PASS；含子用例 1184 PASS；7 显式 opt-in SKIP；22 有测试包 PASS，另 1 无测试包。
- Go 源码树 SHA256：`16e40264e2f551c517b29479262245baaa45d548c6257e26350b2e421d53636c`，前后不变。口径是 server 下所有 `.go`、go.mod、go.sum（包含测试），与浏览器组合资源哈希不是同一口径。

| 包 | 覆盖率 % | 包 | 覆盖率 % |
|---|---:|---|---:|
| agent | 97.42 | api | 80.90 |
| auth | 89.94 | classroom | 91.30 |
| config | 87.59 | duplex | 97.60 |
| feedback | 94.77 | judge | 98.76 |
| llmzhipu | 100.00 | localmodel | 90.48 |
| memory | 93.10 | obs | 92.59 |
| remoteprovider | 96.67 | scheduler | 90.47 |
| scripts | 93.91 | store | 87.12 |
| tools | 93.75 | voiceclone | 94.64 |
| voicecmd | 96.15 | wordbook | 91.50 |

7 项跳过均需显式环境授权/外部本机服务或专用浏览器启动，未把未执行算 PASS：TestOptInProductionFB001Diagnostic、TestBrowserOverdueHarness、TestOptInIndependentASRThroughOwnedProductRoute、TestChatReal、TestChatJSONReal、TestChatRefuseLongInput、TestOptInLocalSpeechQuality。

race 已完成：`server/.test-artifacts/final-runtime-race-r13-summary.json`，exit 0、564.36 秒，516 顶层 / 1184 含子用例 PASS、同 7 SKIP、22 有测试包 PASS，无 race 报告；源码树 SHA 与 coverage 完全相同且前后不变。

当前服务 `.test-artifacts/simple-confirmation-server-20261015.exe` SHA256 `282bd570550cf3e29d3b6c68f9987f4a89e02dd3dd75a6ce3cb81d122c27472f`；其后仅增加测试，没有生产改动。race 后已重建完整测试源码的 `.test-artifacts/simple-confirmation-harness-final-20261015.exe`，SHA256 `135ffb90cb8d3ca7b34b3acf0a0650a48a8407cae3e1f85f2ec3a7c6f944f213`。测试及构建链均退出 0，已交回浏览器独占窗口；完整浏览器最终结果由其报告独立记录，不用专项 3/3 替代全族。

## 仍需区分的限制

本片消除了跟读无谓 L2 网络等待，确定性证据是请求次数 0 和无判分前快照，不是整体课堂低延迟承诺。既有 D 盘冷迁移和热提交约 22 秒长尾仍为独立问题，不能说本片已修复，也不能将编译慢直接归因到该产品路径。复杂词库目标按 unsupported 显式保留出口；真实声音是否为真人、发音准确性及 ASR 质量均不由文本匹配证明。

只读数据分类证据：`review/repeat-seed-target-audit-20261015.json`。以实际 repeat.go 目标规则及本机 Go Unicode Latin/White_Space 范围重现分类，5568 条中 97 unsupported，分布 g10 35 / g11 29 / g12 26 / g7 6 / g9 1，其余两库 0，与架构原独立扫描一致。记录了实现文件及各库 SHA；没有编译 Go、调用模型或修改种子。此分类审计不能冒称新增执行了 Go 测试。

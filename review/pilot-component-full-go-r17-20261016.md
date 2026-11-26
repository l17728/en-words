# 正式组件 r17 后端总回归

r16 覆盖门失败后新增 4 个顶层行为测试，没有修改生产文件或覆盖率门。完整新树重新执行 coverage、vet 和 race，均通过；浏览器新树整体验收另行进行，不在本报告提前记绿。

Go 全树（含测试及 go.mod/go.sum）SHA256：`b2bac383acace0b1c957cf607f0a785d6e8196f85a78740a145d6eb06a6c5ba2`。coverage 与 race 的前后哈希均一致，10 个生产组件文件仍逐个匹配初次整合历史 manifest。

| 阶段 | 结果 | 时间 |
|---|---|---:|
| 全量 coverage + vet | exit 0，23 包通过 | 338.17 秒 |
| 全量 race | exit 0，无数据竞争报告 | 1519.11 秒 |

两轮均为 **570 个顶层通过、1473 项含子测试通过、9 项显式跳过、零失败**；共运行 579 个顶层 / 1482 项含子测试。最初 31 个组件顶层和新增 4 个顶层均纳入正式全量，没有用 profile 拼合替代运行。

| 模块 | 覆盖率 |
|---|---:|
| agent | 97.42% |
| api | 80.93% |
| auth | 89.94% |
| classroom | 92.29% |
| config | 89.47% |
| duplex | 97.60% |
| feedback | 94.77% |
| judge | 98.76% |
| llmzhipu | 100.00% |
| localmodel | 90.48% |
| memory | 93.10% |
| obs | 92.59% |
| remoteprovider | 96.67% |
| scheduler | 90.47% |
| scripts | 93.91% |
| store | **83.75%（1459/1742）** |
| tools | 93.75% |
| voiceclone | 94.64% |
| voicecmd | 96.15% |
| wordbook | 91.50% |
| 新增 10 文件组件独立统计 | **80.77%（945/1170）** |

全部既定门通过。coverage 使用 `-coverpkg=./internal/...` 并沿既有同源码块跨测试包合并口径；race 使用既定便携 clang、CGO、`GOMAXPROCS=2`、`-p=1 -count=1`。工具链与依赖离线，未切服务或调用真实模型。原始证据位于 `server/.test-artifacts/final-runtime-coverage-r17*` 和 `final-runtime-race-r17*`，完整摘要及其 SHA 见同名 JSON。

9 项跳过：`TestOptInProductionFB001Diagnostic`、`TestStartupCleanupHarness`、`TestBrowserOverdueHarness`、`TestOptInIndependentASRThroughOwnedProductRoute`、`TestChatReal`、`TestChatJSONReal`、`TestChatRefuseLongInput`、`TestOptInLocalSpeechQuality`、`TestStartupFailureHarness`。真实模型探针与专用进程 harness 需显式启用，未执行不计成功；浏览器会独立启用其使用的 harness。

新补测试经架构实际复核，验证历史 runtime 合法形状及损坏拒绝、原库保留、私有 scope 创建/重放/拒绝/回滚等真实持久合同。它们没有把私有组件声明成已接入 Room、Agent、HTTP 或旧仓储写门。默认 Open 仍无 v2 自动迁移。

r16 失败报告和专项结果均保留。历史课堂 COMMIT 21.678 秒及 22.594 秒长尾没有因本轮回归通过而关闭；固定 50 次工具负载也不是课堂延迟验收。

## 当前源码二进制交接

全量通过后串行构建 server、API classroom test harness、store startup process harness、cmd/server cleanup harness，四次均退出 0，构建前后 Go 源码哈希相同。四个新文件使用 `.test-artifacts/pilot-r17-20261016-*` 唯一路径，没有覆盖历史产物。

可供浏览器 runner 校验的完整交接为 `review/pilot-r17-browser-handoff-20261016.json`，SHA256 `a9050f5392407c5a151c555de83f60e0113489048f9e44d0df421f9830b95a9a`。其中包括当前四角色的用途/path/hash/源码清单与第五个历史 startup-old 的明确来源。历史二进制基于 `a2d7ad32cb3a6701e2f59eefd686e89e979ad54c` 且 `vcs.modified=true`，不能用该提交号独自声称可重建，另关联 prior acceptance 的完整生产文件清单。

构建结束后已明确释放浏览器重测试窗口；本报告不提前宣称其完整用户旅程通过。

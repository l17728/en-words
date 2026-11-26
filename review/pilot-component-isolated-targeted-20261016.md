# Pilot 最小 store 组件隔离验收记录

2026-10-16。本次只在 `.test-tools/pilot-component-work/server` 副本整合组件；正式 `server/` 前后源码未变。组件没有接入 App、HTTP、Room、Teacher 或 Agent，不能据此启用生产迁移或真实 pilot 教学。

## 执行结果

| 轮次 | 结果 | 边界 |
|---|---|---|
| r1 | 编译失败，0 用例；184.188 秒 | 临时整合器误取注释中的 `package store`，不是产品行为红例 |
| 整合器回归 | 3/3 通过，0.045 秒 | 注释中的 package、拒绝覆盖、缺少真实声明三种边界 |
| r2 | 31 顶层、含子项 216 通过，0 跳过、0 失败 | 总耗时 322.906 秒；Go 包测试体 46.176 秒 |

r2 命令：在隔离副本执行 `.test-tools/go/bin/go.exe test -p=1 -count=1 -timeout=180s -json ./internal/store -run TestPilot`，工具路径按仓库根解析。runner 显式传入 `GOTOOLCHAIN=local`、`GOPROXY=off`、`GOSUMDB=off`、`GOMAXPROCS=2` 和既有本地缓存路径；模块没有本地 replace。没有通过读取子进程内存再独立证明环境变量。等待编译的时间不等于产品请求耗时，也未据低 CPU 推断网络或产品故障。

整合器从不受锚定的字符串查找修为行首完整 package 声明匹配。历史 3 条自测执行位置是 `review/test_integrate_pilot_component.py`；随后按主审要求原样移到 `.test-tools/test_integrate_pilot_component.py`，避免提交一个依赖被忽略工具的不可复现 review 测试。移动后没有重复跑测。该工具与自测仍是临时整合辅助，不声称 clean checkout 已包含它们。

## 精确源码与证据

完整清单见 [机器记录](pilot-component-isolated-targeted-20261016.json)：逐一记录 16 份草稿的初始/当前 SHA256、隔离目标文件 SHA256，以及精确变换相等性。16 份草稿均未改变，全部隔离文件等于记录的变换；不是只凭文件名判断一致。

变换仅截取真实 package 声明之后的源码、加入隔离边界注释，并统一三个入口为包私有名称：`NewPilotStore → newPilotStore`、`InspectPilotSchema → inspectPilotSchema`、`MigratePilotSchema → migratePilotSchema`。完整 facade 的旧仓储绑定工厂没有纳入，采用最小 component-store。

隔离 Go 源码树 SHA256：`be4bffeaec39fadb6b31288773d38eabf0c464b91f80f7615739f6a0796bb782`。

原始证据目录：`.test-artifacts/pilot-component-targeted-20261016/`，保留 `r1.jsonl`、`r1-summary.json`、`r2.jsonl`、`r2-summary.json` 和 `draft-manifest.json`；上述 JSON 报告另记录这些文件及整合器/自测的哈希。命令 runner 为 `.test-tools/run_pilot_component_targeted.py`。

## 已验证的合同与未验证范围

六份新增测试文件共声明 31 个顶层测试，全部以 `TestPilot` 开头；声明集合与 r2 实际通过集合严格一致。没有因筛选前缀遗漏其中测试。默认 Open 仍为原 22 表；私有显式迁移另测完整 v2 schema、并发双 pool、真实延迟外键 COMMIT 失败及重试、永久 scope 三触发器、大小写对象、启动兼容三门。

其他真实 SQLite 用例覆盖 strict bridge/created codec、SAVEPOINT 回滚后重新预留、完成事务不能授权、未提交 schema cookie 不发布、pool 来源绑定、双 pool 创建/nonce、当前授权、配置 ABA、失败 COMMIT 不发布 receipt、冻结数据损坏与安全 observer。created 审计验证历史冻结而不以当前停用账号或词库状态推翻历史事实；操作重放仍要求当前授权。

五类未来事实 fixture 均先成功插入并确认 `foreign_key_check` 为零，再断言首片审计拒绝。部分表依赖 receipt，因此只证明非空未来事实被拒绝，不宣称每个拒绝分支都独立命中。

本次没有执行整个 store 包、全 Go、coverage、vet、race 或新的浏览器测试，不提供组件覆盖率达标结论。已有生产 r15 结果仍属于原冻结树，不能用来替代组件正式集成后的全量门。下一片 card/lesson replacement 草稿及其测试未纳入本次 16 文件，也未被宣称通过。

## 下一片既有草稿的最小顺序

1. **仓储来源绑定。** 先将现有 full facade 中 `SchedulerRepo(tx)`、`ConfigPersister(tx)` 的私有 verifier 字段与来源检查接到真实仓储；逐一核 agenttransaction、feedbackstorage、lessonexecution、overdue 的事务工厂。不得把 tx-bound 对象转回未绑定仓储。此片可单独测试、交付，但仍无生产 pilot 创建或迁移入口。
2. **同 writer 的仓储写门。** 整合既有 scope-gate、card-write、lesson-write 草稿：实际写前查询当前 scope/bridge，不能缓存成功许可；新课使用数据库 ID，非零 Save 只更新存在且 owner/book 不变的课，外层失败不得发布 ID。接入七个既有路径之外，还须核 `CreateCardsIfAbsent`、`MarkPlacementAtomic` 等批量路径，不能以七个方法替换代表全部写门完成。对应 lesson/card 草稿测试须在真实替换后编译执行。
3. **副作用前 service 门。** 接入现有 legacy-entry 草稿及已定位调用链：AlreadyPlanned 后续 ReserveCards、Settle 的内存缓存、Placement/Reset、配置影响的全 scope 预检。门必须早于缓存、任务或配置副作用；finished 只读 Enter/Join/ticket 和 Timer no-op 不应为读取永久预留。全局配置/梯度有效范围事务尚有独立缺口，不能靠仓储拒写假称完成。
4. **取消选库独立闭环。** 当前 DeleteStudentBook 草稿不足以交付完整删除。仍需评审 Room 退休、任务/跨进程旧回调边界、失败重试，以及同事务按所属 LessonID 精确清理四类 canonical runtime、草稿、reservation、卡和课；scope/bridge 墓碑保留，未知 key 不自动删除。见现有删除兼容设计报告，本轮不扩大实现。
5. **最后才允许启动/迁移接线评审。** 上述写入保护及支持版本协调完整后，再评审正式启动审计、显式迁移与可信调用入口。独立组件通过不授权在旧 writer 仍可运行的生产库迁移。Room/统一提交/Teacher/outbox 整链仍属于后续切片。

上述顺序仅整理已有草稿和已知依赖，没有新增生产实现。能独立交付的是不可经 HTTP 到达的组件及仓储保护增量；在入口覆盖、删除生命周期与迁移协调闭合前，不得启用 pilot 数据生产使用。

## 正式落地前只读核验与执行交接

在 browser 基线独占期间，仅执行 `gofmt -l`，没有 `-w`：16 文件中 15 个需要格式化，只有 `pilot_scope.go` 已符合 gofmt。r2 是这些未格式化字节的真实结果；正式格式化后必须重新记录 SHA，不能沿用 r2 字节哈希作为最终源码。正式 `startup.go:88` 的 `startupModels` 仍列原 22 模型；新旧 Open 路径均未注册 v2。隔离副本 `internal/` 与 `cmd/` 非 `_test.go` 扫描 `newPilotStore`、`inspectPilotSchema`、`migratePilotSchema`、`CreateIsolatedPilotLesson`，只找到各自定义，没有生产调用点。

基线 141 验收结束并由主进程完成基线提交后，正式整合步骤为：

1. 再核本 JSON 的 16 份草稿 hash 和目标路径不存在，确认基线后没有新的接口变动；只按已有映射新增 16 文件，不复制整个隔离 server，不覆盖旧仓储文件。
2. 使用已修正且拒绝覆盖的整合器执行同一 private 变换；对新增 16 文件 gofmt，记录格式化前后逐文件 hash 与完整 Go 树 hash，并核差异仅为预定新增文件。
3. 重查默认 Open 22 模型和无生产调用边界，先正式树 `go test -p=1 -count=1 -timeout=180s -json ./internal/store -run TestPilot`。此命令不能替代后续全量。
4. 子代理按串行窗口执行全量覆盖、vet、race。沿既有 runner 使用新的唯一轮次名，先确认输出不存在：`python .test-tools/run_final_acceptance.py coverage pilot-component-final`，完成后 `python .test-tools/run_final_acceptance.py race pilot-component-final`。runner 必须在项目根启动，内部 cwd 为 `server/`；race 使用已校验 LLVM MinGW 的 CGO 配置。
5. 归档前后源码 hash、全部失败/skip、覆盖门和新增函数覆盖，并交还浏览器完整回归窗口。只有同树最终门通过后才能把组件纳入正式基线，仍不启用迁移或创建路由。

上述 full 命令实际展开为 `go test -p=1 -count=1 -json -coverpkg=./internal/... -coverprofile=<唯一输出> ./...`、`go vet -p=1 ./...`、`go test -p=1 -count=1 -json -race ./...`。本次没有执行这些命令。临时 runner 位于被忽略目录；命令与参数在此明示，不能宣称 clean checkout 自带该 runner。所有实际回归仍交后台测试子代理执行，且与浏览器串行。

legacy 写门可以在保持 pilot 不启用的前提下独立开发、验证；其最多五项前置依赖就是上节的来源绑定、仓储全写路径、service 副作用前门、删除生命周期、最后迁移接线。前三项可先作为保护组件落地；取消选库不可只凭当前删除草稿交付。未具备全部启用条件时，仍只允许隔离测试显式创建 v2 数据，不对正常应用暴露 facade、迁移或 pilot 创建入口。

## 2026-10-16 正式源码整合更新（验收未通过）

原基线浏览器门未全部通过：SUB02 保留真实 21.678 秒事务长尾，不能把检查点提交当作验收提交。主进程保存 `e1da932` 后结束旧源码冻结，将本组件与 docs39 作为新的待验收候选。上述“基线 141 验收结束后”是原计划，实际并未满足。

已核对 16 份草稿和整合器 SHA，确认目标不存在后新增文件。格式化前与 r2 隔离副本的 UTF-8 文本全部一致；`pilot_legacy_bridge_test.go` 原始换行字节不同，未误报字节一致。随后完成 gofmt。逐文件前后 SHA 见 [整合清单](pilot-component-integration-20261016.json)。字节相等检查曾因此中止一次，随后明确按 UTF-8 和换行归一验证，没有覆盖既有文件或重复生成。

组件仍无应用、路由、工具入口或默认迁移接线。新树需要重新执行全量 Go 覆盖率、vet、race 及浏览器验收，历史 r15 和 r2 专项结果不替代该门。原事务长尾与完整试点接线仍未关闭。

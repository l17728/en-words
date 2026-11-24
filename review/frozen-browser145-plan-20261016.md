# 新候选145例一次构建验收计划

状态：2026-10-16 仅静态方案，待主审/架构复读及 backend r17 race、四产物交接；未启动构建或测试。当前 WAIT39 四绿及旧 startup14 均为历史专项，不拼接为新候选通过。

## 产物与输入冻结

使用 backend 新构建普通产品 server、课堂 harness、启动故障 harness、cleanup harness，逐一核交接 SHA。普通课堂绝不替换为诊断服务。ST04 原有旧版迁移前置仍需显式 `E2E_STARTUP_OLD_BINARY`；该第五个历史二进制另冻 SHA，仅用于创建旧库，恢复阶段使用新普通 server，不把历史库创建冒称新候选代码。

独立一次 `npm run build`，180秒预算与真实Popen退出码、完整stdout/stderr；超时终止自有树并保留失败，不补跑。源/输入前后冻结：生产Go与web/src、web/public、index.html、package/lock、tsconfig/Vite/Playwright配置、完整e2e/tests、Node/Python执行器及其导入helper、存在的四类Vite env文件仅bytes/SHA。记录Node/npm/TypeScript/Vite/Playwright版本，构建环境仅摘要哈希，不打印内容。server/go.mod/go.sum及backend产物清单也关联。

构建成功才检查index.html、至少一个JS bundle以及每个本地src/href引用文件；对dist全部文件路径/长度/SHA冻结。缺失、额外/变化文件均拒绝。每一发现/执行阶段前后核源、输入、四新binary、历史迁移binary、dist与发现报告不变。

## 最小可复现装配

拟新增小入口 `review/run_frozen_browser_acceptance.py`，复用现有 `sources/cases/validate_shards/wait_owned/stop_owned_tree`，不另造测试框架。一个位于web根的 `playwright.frozen.config.ts` 根据受限suite枚举导入原配置，只把精确 `npm run e2e:serve` 的前端command替换为本地Vite preview；其余webServer（尤其真实后端）、cwd、env、端口、proxy、testDir、timeout、workers、retry与headless原样保留。根目录位置避免改变相对cwd含义；feedback配置原导入取独立DB副作用保留。

入口必须提供本次新构建manifest并验证完整dist及输入，不能裸 `skip-build` 复用旧产物。配置不增加真实平台访问；每个JSON reporter绑定该进程专属绝对输出路径。输出目录exclusive创建，不覆盖旧报告。构建/preview/浏览器各阶段耗时和总墙钟分别记录；拆分不声称旧组合180秒失败已修或SLO通过。

## 顺序与精确分母

1. 轻量Node完整族、lint、Python工具回归，记录本轮真实数量，不固定猜测。
2. 本次正式180秒build、资源检查及冻结。
3. SUB03完整两参数预检，沿原单例/500ms/Node15秒屏障，不修改产品时钟；这两次只是夹具预检，不计145，也不替代后面完整recovery中的同ID。
4. 用serve-only配置对全部13个族执行 `--list --reporter=json`，冻结specID+projectID、完整标题与所属族。预期145唯一ID，重复/缺失立即拒绝。
5. 完整族按 continuation4、interaction30、feedback26、closure6、playback1、legacy9、journey1、firstuse1、overdue1、duplex7、repeat3、startup14、recovery42 执行。12个普通族共131，加startup14=145。
6. recovery42预先固定6×7 test级shard（`--fully-parallel --workers=1 --shard=i/6`）；每批先list验证准确7ID、不交且并集等于原42。各批900秒执行预算，原所有单例/HTTP/业务/启动/退出门不变。其他完整族各900秒，不拆失败后挑绿。startup14包含ST09新候选真实cleanup+Router seam，仍不声称完整run故障注入。

预检失败先停定位，不进入全量赌绿。正式执行失败保留退出码、精确case/阶段、trace/log；清理自有进程后是否继续独立族依主审分类，绝不自动重试失败case或补最快样本。任一未通过/未执行都使总验收失败；不得拼接旧candidate或旧fixture结果。

## 验证与报告

每个结果要求实际ID等于预声明集合，恰一次passed、retry0、skip/flaky/unexpected0。所有13族最终并集精确145；预检两例另账，不混分母。Manifest逐阶段落盘，失败/timeout也留当前步骤与已完成/未执行清单；finally即使退出清理异常也写证据，记录无法证实的残留，不能谎称停净。

新增工具单测锁：完整145计数/ID唯一；6×7精确并集；重复/遗漏/skip/retry拒绝；缺index/缺bundle/额外dist/输入变更拒绝；只替换前端command保留后端与门；build超时不启动preview、原错误保留。工具测试不是产品E2E替代。

结束交专家复核并同步docs03/35/37/39当前候选状态。明确真实语音、pilot全链、原SUB02 ACK后21.678秒COMMIT长尾未因功能通过自动关闭。不能把受控WS屏障/模拟媒体视为无屏障并发声学验收。

## 工具实际保存点与第二轮修订

执行器、root-web wrapper及helper已实现；`review/frozen-browser145-tool-review-20261016.json`记录逐文件SHA及轻量Python12/Node2结果，实际145发现/构建/浏览器仍未执行。已按审查补固定批准handoff SHA及角色/逐Go源文件校验，Popen/等待中断清理与原子失败证据，最终binary缺失也不覆盖原错误。新产物必须匹配`review/pilot-r17-browser-handoff-20261016.json`，旧ST04 binary也由该清单明确绑定。

依赖采用主审批准的两端完整字节校验：web/node_modules和实际global npm文件在开始/finally全SHA，阶段间完整路径/size/mtime检查，Node/npm执行入口逐阶段SHA；只排除顶层明确缓存`.tmp`/`.vite`。此保证为两端字节一致及阶段元数据，不冒称持续字节监控。一次实际同size/mtime但内容更改的临时依赖测试证明最终SHA会拒绝。

启动命令须提供`--handoff review/pilot-r17-browser-handoff-20261016.json`及五个明确角色路径；清单SHA硬绑定在经过review的执行器内。所有新增文件留未提交，等待第二轮实际审查和主审放行。

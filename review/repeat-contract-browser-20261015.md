# 跟读与严格口令浏览器验收

## 当前保存点

docs38 第一片，现已获得旧二进制真实红与新二进制完整三例绿；全127后继功能验收已完成，见文末最终节。两个聚合用例使用真实产品 HTTP voice 入口、WS、课堂 UI；转写及模型协议为受控夹具，不代表声学识别质量。

- RP01：cat 跟读中中文释义、错误词、含原文指令不能推进/调用 L2；正确 cat 推进；测验中文意义继续按既有 L1 判分；另一学生不受影响。
- RP02：否定、疑问、含指令样式文本不触发跳过；精确跳过仅推进本人。此负例合同仅 repeating，不承诺 quiz 错答不耗机会。
- 多输入场景通过既有配置设置学生 timeoutRepeatS=60（允许范围内）；不改产品默认及 HTTP/媒体/启动期限。B 提交前再次确认仍是 cat/repeating，避免自然超时造成假证明。

## 红证据

` .test-artifacts/repeat-contract-red-r2-20261015/ ` 下 baseline.json、frontend-inputs.json、dist.json、results.json、classification.json、build.log、run.log。

旧 server SHA：`b73c9a80719f6a7cec401d39d93e7a2c9f44ea0f552114c0f81d76195c50465c`。
测试 SHA：`5e163724e2d25f8eeba7106b77dc573ad006fb9374682a9e7b100c32da878e93`。
正式前端 build 成功 45.77 秒，冻结源码/依赖/配置及 dist 逐文件 hash，index 引用资源均存在。红绿专项复用该完整 dist，仅更换 server。

旧二进制：2 failed / 0 skipped / 0 flaky，122.112 秒。

1. RP01 首个“猫”被 repeat_verified 接受，repeating 转 wait_answer，违反整词英文跟读合同。
2. RP02 首个“不跳过”返回 event_processed.command=skip，repeating 转 show_word。

失败立即停止该例，所以 banana、cat<不要判对>、后续疑问/含括号控制并未在此旧红中执行到，不能据此声称这些子项各自已有浏览器红；后端专属逻辑红单列。

早一轮 `.test-artifacts/repeat-contract-red-20261015` 为前端构建/启动 180 秒超时，未进入测试，且 dist 缺 JS，属于基础设施失败，未计产品红。已检查并保留，无调高门槛或将不完整 dist 当成功产物。

## 回归与性能边界

正式 runner 新增 repeat 完整 3 例，原 124 例保留，总计划 127。新树尚未宣称全量通过。
此前启动 30 秒失败、入课 COMMIT 22.598 秒长尾仍是独立未闭合性能问题；本片减少跟读 LLM 不等于解决落盘/冷启动。受控媒体分阶段等待只修正验收取点，不据此宣称整体交互速度达标。

## 新版专项完成（2026-10-15 21:39）

可提交机器摘要：`review/repeat-contract-browser-targeted-20261015.json`；完整结果 `.test-artifacts/repeat-contract-green3-20261015/results.json`。

- 原2例保持同测试SHA、同完整dist，换新版server后2/2绿（115.932秒）。
- 随后获批追加RP03，旧server在首个带引号café负例错误推进，1例真实红（93.309秒）。尚未运行到NFD正例，不称Unicode所有子项均有浏览器红。
- 最终同新增测试SHA、同dist，新server完整3/3绿（232.499秒），0 skipped、0 flaky、0 unexpected，不拼零散成功。
- 新server SHA `282bd570550cf3e29d3b6c68f9987f4a89e02dd3dd75a6ce3cb81d122c27472f`。
- 三例测试SHA `2ba1d5e3fc152fe30d41088f77e9c5cc2a828f504b5df8a13f9bf03bd6c94b26`，运行前后相同。
- 前端源码/依赖/配置38源文件及额外构建输入、dist逐文件前后校验不变。运行后生产组合SHA `3e763da3d99fd5d301dff1b4bc4eadb57d58815af0097f8833ea8c345ea40306`；这是本轮后验源码标识，不以它冒称本脚本曾采集运行前Go源码hash。二进制生成来源由backend构建证据单列。
- RP03真实导入回读四目标、实际NFD→café与ASCII→左弯词内撇号通过；复杂目标明确unsupported_target不假通过，精确skip才继续。另一学生课表保持，3个用例的judgeCalls均0。标准quiz中文意义L1评分仍正常。

### 耗时取点

最终三例实际repeat_evaluated共17条（含RP01另一学生），固定匹配计算16条记录0ms、1条30ms。0为毫秒粒度截断，不能称绝对零时延。voice真实HTTP共19次，0–4267ms，包含上下文/持久化/推进；不把规则计算耗时当完整课堂响应。

这些是有限受控样本，未声明p95或声学质量达标。旧入课22.598秒COMMIT长尾、30秒冷启动失败仍保留为独立性能问题。

## 后续完整族计划（等待全Go窗口释放）

依主审顺序，使用正式runner独立续跑完整族并链接同树manifest：interaction30、feedback26、closure6、playback1、legacy9、journey1、firstuse1、overdue1、duplex7、repeat3，最后recovery42（固定21+21，逐ID并集校验）。共11族127例。

每族仍原预算900秒；recovery两组各900秒，最多12个900秒窗口=180分钟上界，另加既有Node/lint/build步骤。预计实际30–60分钟受本机IO影响，不承诺该估计为SLO。每个完整族记录通过/失败/未执行及source/binary SHA，不以失败后的零散绿替代族结果；首次冷启超时按原门记录并继续其他独立族收集，不反复赌绿。


## 本轮最终浏览器验收（2026-10-15，取代前述待验保存点）

**功能回归127/127通过，0失败、0跳过、0flaky。** 最终采用首段完整10族85例，加补正日志合同后的完整recovery42例；后者重新discovery并验证21+21精确不交并集42。不是首轮单次全绿，不把旧recovery40成功样本拼入最终42。

首轮125通过/2失败原件保留：JD28旧断言把跟读当judge L1事件；本片已明确跟读仅repeat_evaluated。根与架构实审后仅补正该测试断言，严格保留两次quiz L2 wrong/correct、0%终局及B隔离。旧失败后B隔离断言未执行；新完整42已执行通过。未改产品/时限，测试输入逐文件核唯一变化为该recovery文件。

证据：`../review/simple-confirmation-browser-final127-20261015.json`；原件位于`.test-artifacts/simple-confirmation-full127-20261015/segment1`与`segment2-recovery`。Node74/74、runner Python6/6、lint退出0。最终选用配置共12次正式build&&preview启动（recovery两个分组各一次），配置禁止复用服务；旧被替换recovery另2次留历史。Playwright默认不转发webServer stdout，不虚构Vite成功marker。

| 完整族 | 通过 | 含构建报告耗时（秒） |
|---|---:|---:|
| interaction | 30 | 578.287 |
| feedback | 26 | 293.205 |
| closure | 6 | 107.405 |
| playback | 1 | 181.874 |
| legacy | 9 | 128.798 |
| journey | 1 | 100.728 |
| firstuse | 1 | 94.173 |
| overdue | 1 | 37.815 |
| duplex | 7 | 65.147 |
| repeat | 3 | 93.108 |
| recovery | 42 | 595.082 |

生产组合SHA `3e763da3d99fd5d301dff1b4bc4eadb57d58815af0097f8833ea8c345ea40306` 两段前后不变；server `282bd570550cf3e29d3b6c68f9987f4a89e02dd3dd75a6ce3cb81d122c27472f`，最终harness `135ffb90cb8d3ca7b34b3acf0a0650a48a8407cae3e1f85f2ec3a7c6f944f213`。测试输入最终SHA `26cf33f9ee8c76b230fb671565da8f43c11bdebf9e09034a9dc315a4108b9dd9`。

**性能/声学边界仍未清零：** 本轮功能绿不消除历史冷启动>30秒、入课22.598秒/COMMIT收尾22.594秒长尾。最终所选81个owned用例日志子集（不是全部127请求）中，enter n126/中位16.5ms/最大543ms，voice n33/中位64ms/最大403ms；最慢分别为慢判分重启、慢判分心跳故障夹具，且enter含测试状态读取。只作混合故障HTTP诊断，不据此宣称正常课堂或真人端到端SLO。RP跟读专属voice另列n19/中位2ms/最大149ms；固定匹配计算与HTTP分账，非真人发音质量。完整分母/最慢case及原失败批独立保存在`../review/simple-confirmation-browser-http-diagnostic-20261015.json`与原件目录。

本轮没有新增真实模型/真人声学验收；G1仅本地麦克风预检，native/自动教学门仍关闭，docs36统一提交接线尚未实施。

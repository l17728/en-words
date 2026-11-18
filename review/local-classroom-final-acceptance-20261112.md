# 本机课堂业务闭环与最终回归记录（2026-11-12）

## 业务结果

同一版生产源码（验收脚本 SHA-256：`3c6b0bdaf5897a49f757ba372cb5d9781624fe778e33765674a8ded35d46d3cd`）完成隔离 SQLite、独立浏览器及本机 8090 模型服务验收。两名学生各完成三词教学、跟读、最终测试、复教、课报和下一课；每人五次真实 ASR 均匹配冻结音源的标注，24 段播放分别对应 24 条字幕和 24 次唯一 ACK。课堂中两次“我不会”消耗同一题两次机会，跳过只推进一次，旧 turn 重放被拒绝。

真实媒体 Playwright 结果为 **7/7**，零跳过、零失败、零重试；覆盖 ASR 503 后文字恢复、迟到转写与旧 REST 响应、带标签确认拒绝、疑问/引号/内部标点不误判。原始结果：`.test-artifacts/local-journey-7-r2-20261112.json`、同名 `.log` 与 `local-journey-7-r2-validation.json`。

## 全量回归与评审

12 套无头浏览器配置共 **148 次场景执行**，**147 条不同测试定义**；仅首用旅程在普通和播放设置两种配置下各执行一次。每套用例发现清单与结果 ID 完全相等，每例恰好一次通过，零重试、零跳过；生产源码及 server/harness 二进制在运行前后未变。证据：`.test-artifacts/browser-feedbackfix-full-148-r2-20261112/manifest.json` 和 `global-scenario-validation.json`。验收 runner 单测 9/9。

Go 全包测试、`-race`（API/课堂/判分/口令）、`go vet` 与前端构建通过。核心覆盖率：API 82.3%、store 81.3%、voicecmd 84.0%、wordbook 82.0%；classroom 90.2%、scheduler 91.6%、judge 97.2%、feedback 94.8%，均达本轮门槛。原始日志在 `.test-artifacts/stage-acceptance/`。后端/安全、前端/测试复审所发现的长反馈误拒、旧测试对标签答案的过时预期和跨套重复计数已闭环；本轮功能 P0/P1 为 0。设计和测试用例见 `docs/78-local-classroom-journey-sdd.md` 与 `docs/03-测试用例.md`。

临时分支 `acceptance/local-classroom-20261112` 上，GitHub Actions 针对提交 `93bb314aaad5e4c9661843126e7c31e90f0748e2` 的 [Linux 包/清单 workflow](https://github.com/l17728/en-words/actions/runs/43226213294) 三个 job 与 [首音控制协议 workflow](https://github.com/l17728/en-words/actions/runs/43226213310) 的 Linux/Windows 两个 job 均为 `completed/success`。这些 CI 只证明其定义的包、控制协议与边界测试；不代替本机实时模型容量或真人声学试点。

## 尚未验收的边界

本轮自动化证明的是**本机合成音源与隔离浏览器业务闭环**，不等同真人课堂声学验收。生产中文 TTS 仍每请求冷加载模型；隔离常驻 4 worker 的 N=4/8 HTTP p95 为 2,887/3,092 ms，未达到冻结的 2 秒门；一次并行编译期间还观察到 6 秒 worker 超时。文本模型扩展首轮 75/83 中曾连续出现八次 8 秒超时，之后的完整 100/100 复测不能抹去该故障。中文合成音源独立 ASR 为 9/12；“我想跳过”等错识别需真人试点或改用文字降级进一步确认。详见 `review/zh-tts-resident-isolated-20261112.md`、`review/local-text-offline-acceptance-20261112.md` 和 `review/asr-product-gpu-zh-20261112.json.jsonl`。

按用户安排，真人隔离课堂试点在开发与自动验证结束后启动；外放回声、键盘和背景人声、真人插话、并发首音/打断时延、转写错误率及网络断开恢复尚需现场验收。上述性能与现场门未通过前，不宣称正式课堂首音矩阵或真人试点验收完成。

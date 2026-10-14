# 显式数量授权边界：后端验收记录

日期：2026-10-14。设计依据：docs/26。当前状态：定向通过，架构对实际代码最终复核通过；已与 docs/27 合并冻结，完整 Go/覆盖率/vet/race r6 已通过；浏览器与真实模型质量单独验收。

## 发现与修复

候选模型对“每日安排0节课”产生 `B_increase/target_words=0`，规划器把零当作相对增量，形成禁止计划动作。此次真实模型评测仅规划，未执行用户配置写入；原评分与冻结标签保留。

规则、模型结果和携带 Raw 的直接 PlanAt 现在共用数量约束。完整频率单位、显式零词量、候选数值与原文明示值不一致、跨数量维度分类均不能授权写入。明确独立历史分句按设计排除，合法当前目标及独立相对减量仍可应用。无 Raw 的程序调用保留原数值边界。

独立审查进一步发现共享尾单位范围（50或60个词、50至60个词、50/60个词、50-60个词、50、60个词、50或者60个词、逗号后或者）可能只取尾部数字，已加入先红后绿回归。合法中文五十、六十、一百补正向回归。规则对小数/半节产生澄清时也记录固定拒绝原因，保留非写意图，不泄露原文。

## 测试证据

- `server/.test-artifacts/quantity-authorization-red.txt`：初始问题重现。
- `quantity-authorization-review-red.txt`、`quantity-authorization-log-red.txt`、`quantity-alternative-red.txt、quantity-legacy-red.txt`（同目录）：独审补充问题重现。
- `quantity-authorization-review-green.txt`：第一轮独审修正后 feedback 全包通过。
- `quantity-directed-final.txt`：最新反馈定向 3.456 秒、API 定向 5.328 秒通过。

`feedback_quantity_regression_test.go` 使用真实 SQLite、两个 App 独立句柄，验证澄清任务持久化、重复提交相同任务、跨学生拒绝、配置/记忆/写工具/管理员待办无副作用，并验证后续合法中文五十词仍可应用。浏览器代理独立执行真实 UI 正负旅程，Go 不替代 E2E。追加真实课程列表与 cat 记忆卡的跨实例前后深比较通过（quantity-persistence-final.txt，3.909 秒）。

## 限制

有限数量语法不是通用自然语言证明器；模型分类和完整语义准确率仍按原真实评测记录。禁止将此产品保护补丁回填成模型质量通过。全量证据见 runtime-backend-acceptance.md r6：445 顶层通过、7 opt-in 跳过，896 含子用例通过；反馈覆盖94.77%，全部核心覆盖门及race通过。浏览器最终结果单列，不沿用旧树结果。

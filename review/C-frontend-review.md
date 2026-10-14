# C 反馈闭环前端复核

日期：2026-10-14。复核者：browser_acceptance（本轮独立阅读前一阶段实现；发现问题后参与修复，不将自己的修复冒称独立终审）。范围：docs/12、反馈 DTO/状态组件、Dashboard、Classroom、Admin、Settings、会话与 API 客户端及专项浏览器用例。AGENTS.md 未修改。

## 发现与闭环

| 编号 | 发现 | 修复与锁定 |
|---|---|---|
| FE-C1 | 相同角色跨标签换账号时 Dashboard 保留旧选课、草稿和通知；Admin/Settings 仅按局部依赖读取，存在同类状态残留 | Routes 按 userId+token 重建。Dashboard/Settings/管理员详情三条浏览器回归覆盖清理、重读；静态确认独立 destroyAll，迟到 onOk 校验 generation 和 token |
| FE-C2 | 旧请求迟到的401无条件清理 localStorage，能把新登录踢出 | API 响应解析后比较请求 token 与当前 token，先于401逻辑；旧结果不应用。真实登录取得新 token，协议夹具分别验证旧401保留新会话及当前401仍退出 |
| FE-C3 | 后端持久计划依赖的前置项终止失败时，后项仍 retryable，用户可永久点击无效恢复 | architecture_review 修执行器；浏览器显式持久计划夹具验证 stale_target/dependency_failed 均终止、恢复按钮消失、配置不回退 |
| FE-C4 | 新真实用户路径暴露规则未识别“每天一次”；数字超范围频率还可能误降为每天一次 | 主代理已修完整数量解析和模型原文数量防护，后端代理补红绿回归并经架构独立复核；浏览器保留原文并新增4种超范围请求，不改数字夹具掩盖产品缺陷 |

FE-C1/2 集中方案已经 architecture_review 复核批准，规格先补充到 docs/12 §8.5。token 只作为 React 内部 key，未写入 DOM 属性或日志。已发送的写请求可能已按旧身份提交，本修复只拒绝旧会话 UI 结果，不声称撤销服务端事实。正常登录在收到 API 结果后才写入新 token，保持可用。

## 其他复核结论

- submission_id 与 version 合并不依赖课堂 seq；旧版本反馈不能倒退 UI，processing 只 GET 查询，显式恢复才 POST retry。
- 待确认缓存只存当前学生的提交 ID，不持久保存反馈原文。课堂识别/WS 回调绑定连接时身份和实时 token，换会话清理旧媒体与草稿。
- 逐项结果独立显示，总状态 partial 不遮盖管理员待办；学生和管理员授权查看原文，页面以 React 文本节点渲染，未发现原文插入 HTML。
- API 错误字段保留稳定分类；空失败回复和超预算有中文解释，400 清理无效 pending，409查询原任务并保留冲突说明。
- 真语音和真实模型质量不在浏览器 mock 媒体证明范围；SQLite trigger 仅操作本轮隔离数据库，产品无测试注入端点。

## 验收证据

会话修复前红验证：`web/.test-artifacts/closure-1791942868502-3116/results.json`。临时恢复旧会话行为后，跨标签换学生仍保留“移除词库”弹窗，断言期望0、实际1；测试后finally恢复两个生产文件并核对diff。

首轮最终回归：`web/.test-artifacts/closure-1791942988123-12976/results.json`，16通过、1失败、1因serial跳过。新增Dashboard/Settings/迟到401三条均通过；失败揭示FE-C4，不能作为完整验收证据。修复后重新运行全部专项及closure/firstuse/playback。覆盖率、Go服务身份隔离及其他领域复核由主代理汇总。

第二次回归：`web/.test-artifacts/closure-1791943947200-5964/results.json`，16通过、1失败、1跳过。中文一次及超范围输入已通过；依赖夹具调用了错误路由 `/config`，修正为真实 `/config/set`，只改测试、不改产品。此失败归类为夹具错误。

最终冻结代码顺序验收：

| 配置 | 通过 | JSON报告 |
|---|---:|---|
| feedback | 18/18 | `web/.test-artifacts/closure-1791944115433-12232/results.json` |
| closure | 6/6 | `web/.test-artifacts/closure-1791944253523-12724/results.json` |
| firstuse | 1/1 | `web/.test-artifacts/closure-1791944326885-6156/results.json` |
| playback | 1/1 | `web/.test-artifacts/closure-1791944440607-16232/results.json` |

合计26/26，无失败、跳过或flaky。每组正式配置均执行TypeScript/Vite构建后preview，后端由本轮冻结源码重建。oxlint退出0、19条警告；Vite体积提示保留。汇总、lint及源码不变凭证分别在 `.test-artifacts/2026-10-14/browser-summary.json`、`browser-lint.log`、`browser-source-manifest.json`。源码聚合SHA256前后均为 `e6a850a7b30337c73e28fb3fec4407a19ab504359e5ce1029885765cdb54e882`（server非测试Go及web/src）。

会话补丁实际代码已获 `review/C-architecture-review.md` 独立终审；本复核发现的FE-C1～C4均已实现且浏览器锁定，无本范围遗留阻断。未运行硬编码开发实例的旧fulljourney/bugs组，不宣称整个仓库全部E2E已绿；真实媒体、模型质量、Go覆盖率及项目剩余GAP仍按总台账验收。

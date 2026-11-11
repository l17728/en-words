# Pilot v2 产品接线独立评审（2026-11-11）

结论：`review/pilot-product-wiring-scope-20261111.md` 的**业务优先级判断通过**；所写的“下一可交付切片只能是 Start”须修正为**下一开发切片的后续步骤**，不得直接开放产品入口。评审范围：架构、后端、前端与测试的接线边界；未执行代码或声学验收。

## 核对事实

- `main.go` 只设置 `DuplexPreflight`；`router.go` 只注册 `duplex-capabilities`，响应显式声明 `mode=preflight`、`native_duplex=false`、`automatic_teaching=false`。`pilot_created_state_read.go` 明示持久层读取既不开始 Room 也不授予入课权。
- `docs/36` §1 的既定顺序先要求**持久模式、逻辑身份和全部入口分流**，之后才是冻结计划、事务提交、outbox 发布和完整旅程。Start 必须服从这条先决条件。§2 列出了 REST/WS、旧 Hub 便捷入口、Timer、Join、退出、结算等绕行路径。
- 已验收的 legacy Mock/文字路径是目前最短的学生业务闭环；恢复 ASR 后的真实语音、性能矩阵及真人课堂仍须独立验收。Pilot 底座的绿色测试不能替代这些结果。

## 阻断项与修订建议

1. **P1：Start 不能成为首个产品切片。** 先实现并验证显式试点课的持久模式选择、受信身份及所有旧入口拒绝/分流；未证明前保持 Pilot HTTP/WS 路由关闭。然后才实施 `created → active` 的冻结计划与单事务 receipt/outbox。
2. **P1：Start 单独通过不等于浏览器“无双读”。** 该断言依赖提交后派发、ACK、重连恢复及旧 lease 撤销。应将 Start 事务测试与后续 delivery/restore 浏览器验收分阶段列明；不得给只有 Start 的切片附上完整课堂通过结论。
3. **P1：文字输入与模拟快慢确认的分歧规则仍属核心合同。** `docs/36` §8 允许 Mock 协调层完整开发；在确认结果可改变教学决策前，需持久逻辑输入 claim、迟到/乱序/超时处理与跨重启去重。不能让 Pilot 新入口调用 legacy Room 的判分写路径。

建议保持当前产品入口与 legacy 行为，按 `docs/36` §1 顺序开发并逐片 Go/Playwright 验收。此评审没有批准 Pilot 上线或真人语音试点。

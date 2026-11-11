# 已迁移 legacy 词库取消选书：最小业务设计草案

状态：待架构、后端、安全、测试复核；未实施。范围只包括学生主动取消一门已选 legacy 词库，并保留课报和结算历史。当前 `DeleteStudentBook` 对已迁移 scope 的拒绝行为应维持，直至以下合同通过验收。

## 产品规则

学生只能取消**无进行中课堂、无待提交答案、无待结算任务**的词库。若课程正在进行或结算结果不明，返回明确提示“请先结束当前课堂，稍后再试”，不修改任何数据。未开始的排课可在取消事务中标记取消；已完成课程、课报、记忆卡、scope 与结算事实保留，只从“已选词库”列表隐藏。重复取消幂等。重新选库开启新一代选课；旧课报仍可查，旧课程、票据和 worker 不得复活。

## 持久模型与事务

新增 `student_book_enrollments(student_id, book_id, generation, status, updated_at)`，主键为学生/词库，`generation>=1`、状态只允许 active/retired。迁移时只为能够从卡、课与 scope **逐项核实同一 owner** 的记录建立 generation=1；归属不明则拒绝迁移并输出定位日志。保留不可变的 `classroom_scope_reservations`。新建课程需持久记录所绑定的 enrollment generation；历史课按迁移核实结果显式回填，不在读取时猜测。

取消与重选都先取得 SQLite writer，在同一事务中重新读 enrollment、scope、课次状态、待处理结算与输出任务。取消仅在无可执行中的课堂/任务时写 `retired`、递增 generation，并取消未开始的排课；重选仅从 retired 写 active，使用当前 generation 播种或恢复卡。事务提交失败或结果不明时，客户端只按 `(student_id,book_id)` 查询权威状态，不自动重放取消或重选。任何状态异常或 owner 冲突在首个业务写入前拒绝。

## 必须逐入口接线

`StudentBooks` 以 active enrollment 为准，不再以卡存在判定；`EnableBook`、摸底保存/提交、`PlanNext`、老师排课工具和重教计划必须在写事务内确认 active generation。`Enter`、`IssueTicket`、REST/WS 提交、已有 Room 的动作、定时器、过期扫描与 `commitLessonWithAgents` 在其实际副作用事务内复核 lesson generation 与 active enrollment；历史课报读取不需要 active。票据携带 generation，历史无 generation 票据在第一次退休后失效。输出前需检查同一代有效性；由于本方案禁止取消进行中课堂，若仍有未完成输出/派发租约则取消返回冲突，不能“先取消再撤声”。以上检查必须在各入口现有 writer 边界中落实，单纯 HTTP 或内存检查不构成验收。

## 验收矩阵与日志

- Go：取消与 Enter/PlanNext/结算/定时器双连接交错，各只允许一个序列化赢家；COMMIT 故障零部分变化；重复取消、重选新代、旧票据/旧 Room/迟到 worker 拒绝；另一学生同书不受影响；重启后 active 列表与旧课报正确。
- Playwright：学生选书→排课未开始→取消→刷新列表隐藏→重新选书→只进入新课；进行中取消提示冲突且课堂不中断；旧完成课报仍可查看。两类测试分别锁定存储语义与真实操作。
- 日志：`book_retire_requested/committed/rejected` 和 `book_reenabled` 记录 trace_id、student_id、book_id、旧/新 generation、拒绝原因、事务时长；不记录答案、密钥或原始语音。

## 评审关注点

架构：用“仅空闲可取消”收窄输出竞态，但必须证明所有 worker/派发租约可被同一事务枚举。后端：现有卡即选课模型和 `DeleteStudentBook` 破坏式删除都需切换；检查 SQLite 真实 COMMIT 与跨连接顺序。安全：验证 owner、旧票据及旧 generation 不能跨重选复活。测试：故障注入与双进程 E2E 不可用单连接 Mock 替代。任何入口无法完成 writer 内 fencing 时继续维持当前安全拒绝，不把此草案标成通过。

# 已迁移 legacy 取消选书：独立设计评审

结论：**BLOCK，不能据此开始产品接线。** 评审范围为 `review/legacy-book-retirement-minimal-design-20261111.md` 与当前 `store/api` 实现；未修改业务代码。当前已迁移 scope 的安全拒绝应继续保留。

## P0：空闲条件无法原子证明

草案要求取消事务同时证明没有课堂、答案、结算和输出租约，但现有 legacy `Hub` 的 Room、浏览器播放与部分运行中动作不在同一 SQLite 表或 writer 事务中。只查询 lesson 状态可能在检查后与旧 Room 的下一次输出/提交交错。须先给出可枚举的持久 lease 或等价 fence：取消取得 writer 后阻止新 lease；既有 lease 耗尽才提交退休；每次发声、判分和结算在实际副作用前验证同一 generation。若浏览器已有音频不能撤销，业务规则应明确取消只在服务端确认完成且无待播段后开放。

## P1：入口与历史迁移不完整

- `SchedulerRepo.StudentBooks` 当前以 `MemoryCard` 存在判断已选；`DeleteStudentBook` 删除卡和课。新增 enrollment 后，统计、反馈任务和课程表等所有 `StudentBooks` 消费者都要统一使用 active 状态，同时保留旧课报读取，防止取消后历史消失。
- `SavePlacementDraft`、`EnableBookContext → SeedCards`、`PlacementSubmitContext → MarkPlacementContext/ResetForReplacement/PlanNext` 是分段写入。仅在外层 HTTP 检查 generation 无法防取消竞态；须逐个落到真实 writer 事务并证明重选新代不会继承旧草稿或错误重排。
- `IssueTicket` 的当前 `generationToken` 是课次内存租约；不能代替持久选课 generation。旧票据、已开 WS、定时器、过期扫描和 `commitLessonWithAgents` 均需以课绑定的持久 generation 复核。
- 历史 owner 回填不能从卡/课“尽可能推断”；应列出缺卡、有课无 scope、重复课和孤儿 runtime 的真实旧库处理规则、版本化迁移及失败恢复路径。迁移失败不得阻断现有用户正常只读课报。

## 可缩小的第一片

先实现**只读 enrollment 投影和迁移审计**：对一个隔离旧库验证 owner、课报与当前选课列表一致，不改取消行为。其后实现持久 generation 与入口 fence；最后才开放“仅空闲可取消”。测试须覆盖双连接交错、COMMIT 不明、重启、旧票据、两个学生，以及 Playwright 的取消/冲突/重选旅程。评审门重新核验全部 P0/P1 后再解除 BLOCK。

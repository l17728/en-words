# 后端评审：跟读失败诊断与人工授权修复

## 结论

设计方向可行，但当前 SDD 还不能直接进入实现。必须先补齐任务持久化、并发幂等和权限边界，否则“异步分析”和“一键实施”会出现重复写入、任务丢失或越权。

## 阻塞项

1. **任务没有持久化模型**：只定义了分析结果表，没有队列表、重试次数、租约、下次重试时间、错误原因和 worker 状态。进程重启会丢失待处理任务。建议增加 `teacher_analysis_jobs`，以 `analysis_id` 唯一，采用 pending/running/retry/succeeded/failed 状态和租约超时恢复。
2. **幂等键不足**：`lesson_id+entry_id+transcript` 无法区分同一词的不同失败阶段，也无法防止并发请求。建议使用规范化 transcript、失败类型、课堂 epoch 和 attempt 序号生成稳定 `analysis_key`，并建立数据库唯一索引。
3. **实施竞态未定义**：两个管理员同时点击或 Agent 重试可能重复添加、删除或覆盖变体。`(entry_id, variant)` 唯一约束只能防重复行，不能防止“拒绝后重新实施”的状态竞争。实施必须在事务中校验 pending 版本，并使用 compare-and-set。
4. **API 权限不完整**：学生查询接口必须校验 lesson 所属学生；管理员列表和评价接口必须经过 admin middleware；教师 Agent 不能直接调用管理员评价 API，只能调用受限工具。需要明确每个 API 的角色和响应码。
5. **批量扫描接口缺少任务契约**：`pronunciation-scan` 没有返回 job id、进度、取消、失败明细和重复请求行为。建议改为 `202 Accepted + job_id`，增加查询和重试接口。

## 必须补充

- SQLite 迁移顺序、索引和事务边界；
- Agent 超时、非法 JSON、模型不可用时的确定性降级；
- transcript 的隐私保留期和管理员可见范围；
- 变体删除后的课堂快照语义；
- 审计记录中的 operator、analysis、tool、old/new 值和 trace_id；
- worker 单实例锁或并发上限，避免本机模型被任务洪峰压垮。

## 评审通过条件

补齐队列与恢复协议后，再实现 API 和工具；至少增加“重复提交、进程重启、并发实施、Agent 超时、学生越权、扫描重试”集成用例，以及管理员从评价到变体生效的 Playwright 旅程。

# 后端最终复核：跟读失败诊断与人工授权修复

## 结论

85、86、87 已覆盖此前评审提出的大部分方向：持久化任务状态、租约回收、重试上限、CAS 实施、异步 job、权限、隐私、审计和 E2E 发布门。设计可以进入实现准备阶段，但还不能称为后端闭环定稿。

## 仍需在实现前补齐的两个阻塞项

1. **任务表的物理字段未落地**：SDD 只描述状态和租约，没有明确 `teacher_analysis_jobs` 的字段、唯一索引、状态转换和 SQLite 迁移编号。实现前必须写出 schema matrix，至少包含 `job_id/analysis_id/status/attempts/lease_owner/lease_until/next_retry_at/last_error/created_at/updated_at`，并定义 `queued→running→succeeded|failed|cancelled|expired` 的合法迁移。
2. **一次性授权收据未建模**：工具要求 authorization receipt，但数据模型没有 receipt 表或字段，也未定义防重放格式。必须增加不可复用的 `authorization_id`、管理员、评价类型、参数摘要、过期时间和使用状态，并在同一事务内 CAS 消费。

## 非阻塞补充

- `analysis_key` 必须明确由规范化 transcript、failure_type、lesson epoch 和 attempt 组成，不能只写组合名称。
- `teacher_analysis_evaluated` 应记录人类评价与 Agent 工具结果的关联，方便审计回放。
- 0 天清理策略应改为明确的保留期配置，避免误删正在处理的任务。

## 放行条件

补齐上述两个数据模型后，方可开始后端开发；开发完成后必须验证并发评价、进程重启恢复、租约超时、授权收据重放、学生越权、Agent 超时和批量扫描幂等。当前评审结果为：**有条件通过设计，阻塞实现放行**。

# 跟读失败诊断与人工授权修复 SDD

## 数据模型

新增 `teacher_failure_analyses`：`id`、`lesson_id`、`entry_id`、`student_id`、`failure_type`、`target_text`、`transcript`、`diagnosis`、`suggestion`、`confidence`、`status`、`analysis_key`、`created_at`、`resolved_at`。新增 `word_pronunciation_variants`：`entry_id`、`variant`、`kind`、`source`、`confidence`、`enabled`，并以 `(entry_id, variant)` 唯一约束。

## 流程

判分失败后写入幂等事件，快路径立即返回。后台任务按 `lesson_id+entry_id+transcript` 去重，先执行确定性分类，再异步调用老师 Agent。Agent 输出严格 JSON；解析失败使用系统诊断。成功重试将相关分析标记为 `superseded`。

## API

`GET /api/v1/student/lessons/:id/teacher-analysis` 查询本人报告；管理员使用 `GET /api/v1/admin/teacher-analysis` 查询待处理项。`POST /api/v1/admin/teacher-analysis/:id/evaluate` 接收人类评价；评价后由老师 Agent 选择受控工具，服务端再次校验作用域和参数。`POST /api/v1/admin/wordbooks/:id/pronunciation-scan` 扫描已有词库，任务可重试且幂等。

## 工具

仅注册 `wordbook.variant.add`、`wordbook.variant.remove`、`speech.failure.mark`、`teacher_analysis.resolve`。工具禁止修改原词、发布词库或跨词条批量写入。

## UI

课堂只显示“正在分析”，不改变布局和课堂状态；课程报告显示诊断、ASR 文本、建议和评价按钮。管理员页面显示原词、转写、变体预览、置信度和审计结果。

## 可观测性与测试

记录 `failure_analysis_queued`、`teacher_analysis_completed`、`teacher_analysis_evaluated`、`variant_applied`、`scan_completed`，均带 `trace_id`。单测覆盖幂等和权限，集成测试覆盖工具调用，Playwright 覆盖管理员评价到变体生效、多用户和迟到结果。

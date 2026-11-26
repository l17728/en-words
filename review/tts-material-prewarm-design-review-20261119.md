# TTS 素材预生成设计评审记录（2026-11-19）

## 评审范围

评审对象：docs/90、docs/91、docs/92，以及现有 SpeechAsset、ModelProfile、synthesizeCached 和课堂 enter 路径。

## 分领域结论

- 架构/安全：要求补充有效 TTS 身份、内容 revision、持久化 claim/lease、manifest canonicalization、课堂门禁位置和驻留程序最小安全协议，已在 90/91 补齐。
- 后端：现有进程内锁不足以保证跨进程幂等；需要新增 speech_material_jobs 和条件状态迁移，补启动恢复、唯一索引及迁移测试，已写入 91，属于开发前置约束。
- 测试/E2E：原 12 例方向完整但不可直接执行；已补充 isolated harness、stub 计数、具体 UI/API 断言、重启/lease 注入、cancel、乱序轮询和日志证据矩阵。

## 定稿决议

设计文档在上述修改后通过“进入开发”门。首期仅实现本地服务端 coordinator、持久化任务、课堂门禁、进度轮询和文字降级；驻留程序、外部云模型和 8 人容量矩阵不在本期实现，分别记录为后续阶段或暂停验收项。开发必须先补 Go 单测/集成测试，再补 Playwright 用例，最后执行全量回归和阶段验收。

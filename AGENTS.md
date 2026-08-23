# en-words 项目记忆（每个会话自动继承）

## 项目定位
单词学习 Web 软件 = **平台 + 开放能力（工具注册表） + 双智能体（老师Agent/系统Agent） + 用户** 四层架构。
设计文档在 `docs/`，需求决策记录在 `docs/01-需求分析.md` §7 ADR。

## 质量铁律（用户明确要求，优先级最高，不可协商）

1. **专家评审门**：所有文档和代码完成后，必须邀请多个领域专家（架构/前端/后端/测试/安全/AI-Agent 等，按产物类型选择）进行 review，问题闭环后才算完成。
2. **测试充分性**：测试用例必须充分，覆盖率达标（后端核心模块语句覆盖率 ≥ 80%，状态机/调度器/判分器 ≥ 90%），不达标不算完成。
3. **定位日志完备**：全链路可观测——每步关键操作有 trace_id、module、event、payload 摘要；出问题时能凭日志直接定位，不需要猜。
4. **举一反三**：发现一个问题 → 搜全库同类问题 → 消灭一类问题 → 重测 → 递归迭代，直至问题数收敛为 0。禁止只修当前这一例。
5. **多用户 E2E**：测试时必须模拟多个用户（学生×N + 管理员 + 老师 Agent + 系统 Agent 并发在场）以实际使用和交互的拟真场景撰写 E2E 用例测试，不是单接口冒烟。
6. **问题清零才停**：在所有问题消除之前不停止，不交付"基本可以"的产物。停止条件 = 专家 review 通过 + 覆盖率达标 + 全部测试绿 + 问题清单收敛为 0。

## 技术栈（已锁定，见 docs/01 §7）
React18+AntD5+Vite+TS+Zustand / Go(Gin)+GORM+SQLite / Gorilla WS / 阿里 DashScope（先 Mock）

## 词库种子数据（data/）
- 7 个种子词库：`data/seed/wordbook-{g7,g8,g9,g10,g11,g12,gaokao}.json`，全库 5568 词条、跨库唯一（0 冲突）、0 缺释义。
- g7~g9：人教版初中（qlsyy.cc 抓取，raw/junior-qlsyy.txt）；g10~g12：人教版高中新教材（github lfcypo 7 册，raw/senior-pep.json）；跨年级递减去重。
- gaokao=历年英语高考曾用词词库（1346 词=1241 单词+105 词组）：17 份高考真题 docx（2011-2025，`试卷/` 目录）提取，三重去重（内部/词形归一化比对六库/写盘前），释义由 qwen-flash 真实生成。
- 高中(g10~g12)与 gaokao 音标全缺：待后续用 LLM 导入流程补全（docs/01 FR-B3）。
- 工具链（重跑顺序）：`data/tools/parse_seed.py → build_gaokao_book.py → clean_gaokao.py → verify_seed.py`。
- 质量过滤规则已固化在工具里：答案串/粘连词/人名/书名/自由搭配过滤；新增试卷沿用同链路。

## 质量门状态（2026-08-23）
- 设计文档已过三轮评审（自查34+外部qwen-plus两轮28/19）+全生命周期推演，收敛为0新结构问题，冻结为实施基线。详见 docs/06 §5~§7。
- 评审工具：`review/run_expert_review.py --round roundN`（DashScope qwen-plus，独立视角）；子代理环境持续故障勿再尝试（模型配置缺失）。
- 实施期跟踪：R2/R3 回归用例族、05 §6 V-4~V-8 真实API验收、高中+高考库音标补全。

## 语言与协作
- 中文回复；完成用证据说话（测试数/覆盖率/日志）。
- 大模型 API 先 mock；真实 API 样例后续用户提供。

## 密钥（严禁写入任何文档/代码/日志/提交）
- 阿里百炼 DashScope API Key 已存本地：`D:\en-words\secrets\dashscope.local.yaml`（.gitignore 已排除 secrets/ 与 *.local.yaml；**严禁 push**）。
- key 前缀 sk-ws-，REST 亦可用：2026-08-23 实测 OpenAI 兼容模式 qwen-flash 返回 200。TTS/ASR/Omni 待接入时再验。
- WorkspaceId `ws-rcouyh3kd6v0c7g8` 已存 secrets 文件；Omni-Realtime 连通已实测 PASS（session.created+session.updated，qwen3.5-omni-plus-realtime，见 docs/05 §3.1 实测记录）。

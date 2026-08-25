# 02 软件设计文档（SDD）

> 项目代号：en-words ｜ 版本：v0.3 ｜ 状态：待评审
> 上游：[01-需求分析.md](01-需求分析.md) ｜ 配套：[04-Agent与话术库设计.md](04-Agent与话术库设计.md) [05-Mock接口与真实API替换方案.md](05-Mock接口与真实API替换方案.md)

---

## 目录

1. [总体架构](#1-总体架构)
2. [模块划分与代码布局](#2-模块划分与代码布局)
3. [开放能力层：Tool Registry 设计](#3-开放能力层tool-registry-设计)
4. [双 Agent 运行时设计](#4-双-agent-运行时设计)
5. [课堂核心：状态机设计](#5-课堂核心状态机设计)
6. [调度器设计（等间隔复习）](#6-调度器设计等间隔复习)
7. [反馈闭环设计](#7-反馈闭环设计)
8. [配置中心设计](#8-配置中心设计)
9. [数据模型设计](#9-数据模型设计)
10. [REST API 设计](#10-rest-api-设计)
11. [WebSocket 协同协议设计](#11-websocket-协同协议设计)
12. [语音能力设计（TTS/ASR/听读判读）](#12-语音能力设计)
13. [前端设计](#13-前端设计)
14. [可观测性设计（定位日志）](#14-可观测性设计定位日志)
15. [安全设计](#15-安全设计)
16. [质量保障设计（评审/覆盖率/收敛流程）](#16-质量保障设计)
17. [部署设计](#17-部署设计)

---

## 1. 总体架构

### 1.1 架构图

```
┌─────────────────────────────── 用户层 ────────────────────────────────┐
│  学生浏览器 (React18+AntD5+Zustand)          管理员浏览器（管理后台）    │
│   学习UI / 协同课堂UI / 反馈 / TTS播放/语音识别/麦克风 │ 词库管理/配置/审计    │
└──────────────┬──────────────────────────────┬────────────────────────┘
               │ HTTPS(WSS)                   │ HTTPS
┌──────────────▼──────────────────────────────▼────────────────────────┐
│                            Go 单体服务 (Gin)                          │
│  ┌── 接入层 ──────────────────────────────────────────────────────┐   │
│  │ REST /api/v1/*  ｜  WS /ws/classroom  ｜  静态资源(前端dist)     │   │
│  └────────────────────────────────────────────────────────────────┘   │
│  ┌── 智能体层 ────────────────────────────────────────────────────┐   │
│  │ TeacherAgentRuntime(课堂事件循环)   SystemAgentRuntime(事务循环)  │   │
│  │   LLM决策(LLMProvider接口→Mock/DashScope)                        │   │
│  └───────────┬────────────────────────────────────────────────────┘   │
│  ┌── 开放能力层 ──────────────────────────────────────────────────┐   │
│  │ ToolRegistry: Schema校验→作用域检查→风险控制→执行→审计           │   │
│  └───────────┬────────────────────────────────────────────────────┘   │
│  ┌── 平台服务层 ──────────────────────────────────────────────────┐   │
│  │ ClassroomService(课堂会话/状态机)  SchedulerService(排课)        │   │
│  │ ProgressService(学习记录)  WordBookService(词库)                 │   │
│  │ FeedbackService(反馈)  ConfigService(配置中心)  ScriptService    │   │
│  │ SpeechService(TTS/ASR Provider)  AuthService  AuditService      │   │
│  └───────────┬────────────────────────────────────────────────────┘   │
│  ┌── 基础层 ──────────────────────────────────────────────────────┐   │
│  │ GORM → SQLite(enwords.db)  ｜ 结构化日志(zap) ｜ 定时器(cron)     │   │
│  └────────────────────────────────────────────────────────────────┘   │
└───────────────────────────────────────────────────────────────────────┘
```

### 1.2 关键架构决策

| # | 决策 | 理由 |
|---|---|---|
| A1 | **单体分层架构**（非微服务） | 家用规模，单二进制+SQLite 零运维；层次清晰即可测试 |
| A2 | **Agent 不直连业务模块**，只经 ToolRegistry 调工具 | 决策与执行分离；统一授权、校验、审计；Agent 可整体替换/Mock |
| A3 | **课堂状态机放在平台侧**（不在 Agent 侧） | Agent 是决策者，平台是事实源；断线/重启可恢复；Agent 失误不破坏课堂状态 |
| A4 | **Mock Provider 编译期同接口注入** | LLM/TTS/ASR 全接口化，配置切换 `provider: mock\|dashscope`，无 mock 分支散落 |
| A5 | **WS 指令带 seq 幂等** | 断线重连/重放安全 |
| A6 | 前端**不用轮询**，课堂内一切实时性走 WS | 简化心智；课程表页用 REST |
| A7 | **时间只存 UTC**，展示层转 Asia/Shanghai | 排期计算无歧义 |

---

## 2. 模块划分与代码布局

```
en-words/
├── docs/                        # 本设计文档集
├── server/                      # Go 后端
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── api/                 # REST handler + WS handler + 中间件(JWT/审计)
│   │   ├── agent/               # Agent 运行时
│   │   │   ├── teacher/         #   老师 Agent：课堂事件循环
│   │   │   ├── system/          #   系统 Agent：排课/反馈/逾期处理
│   │   │   ├── loop.go          #   通用 感知→决策→执行→观察 循环
│   │   │   └── guard.go         #   行为上限（调用次数/时长）
│   │   ├── tools/               # 开放能力层
│   │   │   ├── registry.go      #   ToolRegistry
│   │   │   ├── schema.go        #   JSON Schema 校验
│   │   │   └── defs/            #   工具定义（按域分文件）
│   │   ├── classroom/           # 课堂状态机 + 会话管理
│   │   ├── scheduler/           # 等间隔调度器
│   │   ├── feedback/            # 反馈意图处理
│   │   ├── config/              # 配置中心（边界+层级+审计钩子）
│   │   ├── wordbook/            # 词库服务
│   │   ├── progress/            # 学习记录
│   │   ├── speech/              # TTS/ASR Provider 抽象 + mock + dashscope
│   │   ├── llm/                 # LLM Provider 抽象 + mock + dashscope
│   │   ├── scripts/             # 话术库服务
│   │   ├── auth/                # 认证（JWT/APIKey）
│   │   ├── audit/               # 审计
│   │   └── obs/                 # 日志/trace_id（质量铁律3）
│   ├── migrations/              # SQL 迁移
│   └── tests/                   # 集成测试 + 多用户E2E驱动
├── web/                         # React 前端
│   ├── src/
│   │   ├── api/                 # REST client
│   │   ├── ws/                  # WS client（重连/快照/seq）
│   │   ├── stores/              # Zustand
│   │   ├── pages/               # 登录/词库广场/摸底/课程表/课堂/统计/管理后台
│   │   ├── classroom/           # 协同课堂组件（渲染 action 指令）
│   │   ├── audio/               # TTS播放/语音识别（原 VAD/录音上传已废弃）
│   │   └── scripts/             # 话术口语化展示辅助
│   └── tests/                   # 前端单测 + Playwright E2E
└── scripts/                     # 构建/种子数据/覆盖率脚本
```

---

## 3. 开放能力层：Tool Registry 设计

> 对应 FR-I1~I3、I7。这是"平台能力暴露为工具"的核心机制。

### 3.1 工具定义结构

```go
type Tool struct {
    Name        string          // 如 "classroom.show_word"
    Domain      string          // classroom / scheduler / feedback / config / wordbook / progress / scripts
    Description string          // 给 LLM 看的自然语言描述（含使用时机）
    Scope       []Role          // 允许调用者角色
    RiskLevel   Risk            // low / mid / high
    InputSchema json.RawMessage // JSON Schema（入参）
    Handler     ToolHandler     // func(ctx, caller, args) (result, error)
    Timeout     time.Duration
}
```

### 3.2 工具清单（v1 全量注册表）

**课堂域 classroom（作用域：teacher_agent）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `classroom.show_word` | lessonId, wordId, phase | low | UI 显示单词卡（仅英文或含中文） |
| `classroom.play_tts` | lessonId, text, lang, repeat, intervalMs | low | 触发学生端 TTS 朗读 |
| `classroom.start_repeat` | lessonId, wordId, times | low | 开始学生跟读环节（老师听读判读，2026-08-25 定案） |
| `classroom.start_quiz` | lessonId, quizId, items[] | low | 出测试题（仅英文） |
| `classroom.judge_answer` | lessonId, itemId, rawText, judgment | mid | 提交判分结果（对/错/近似） |
| `classroom.show_feedback_ui` | lessonId, kind(check/cross) , wordId | low | 打勾/打叉动画 |
| `classroom.start_review` | lessonId, wordIds[] | low | 进入回顾环节 |
| `classroom.say` | lessonId, text, tone | low | 老师气泡说话（话术已口语化） |
| `classroom.say` | lessonId, text, tone | low | 老师气泡说话（话术已口语化）；REPEATING 期唯一话术通道 |
| ~~`classroom.advance_phase`~~ | — | — | **R3 移除**：快路径下阶段推进由状态机自完成，保留该工具徒增 LLM 幻觉面（v1 删除；调试需求走内部 API） |
| `classroom.get_state` | lessonId | low | 读课堂当前快照 |

**教务域 scheduler（作用域：system_agent）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `scheduler.plan_next_lesson` | studentId, bookId, proposeAt | mid | 生成下一课（时间+批次） |
| `scheduler.postpone_lesson` | lessonId, toDate, reason | mid | 推迟课程 |
| `scheduler.get_due_words` | studentId, bookId, date | low | 查到期复习词 |
| `scheduler.get_progress_summary` | studentId, bookId | low | 学习效率数据（正确率/耗时/逾期） |

**反馈域 feedback（作用域：system_agent）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `feedback.reply` | feedbackId, text | low | 回复学生 |
| `feedback.apply_adjustment` | feedbackId, patch{configKey,value} | **high** | 应用配置调整（走配置中心边界校验） |
| `feedback.escalate` | feedbackId, note | low | 转人工待办 |

**配置域 config（作用域：system_agent, admin）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `config.get` | scope(global/student), studentId?, key? | low | 读配置 |
| `config.set` | scope, studentId?, key, value, reason | **high** | 写配置（强制 reason；边界校验） |

**词库/记录域（作用域：admin；部分只读给 system_agent）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `wordbook.import_parse` | bookId, rawText | mid | LLM 解析批量导入文本 → 结构化预览 |
| `wordbook.import_commit` | bookId, entries[] | mid | 提交入库 |
| `wordbook.crud_*` | ... | mid | 词库/词条增删改查 |
| `progress.query` | studentId, bookId, filters | low | 学习记录查询 |

**话术域 scripts（作用域：teacher_agent, system_agent）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `scripts.pick` | category, context{studentName,word,...} | low | 抽话术并执行口语化微调 |
| `scripts.list` | category | low | 话术库列表 |

**记忆域 memory（D-16：Agent 自维护记忆，平台提供完整 CRUD 能力）**

| 工具 | 入参摘要 | 风险 | 说明 |
|---|---|---|---|
| `memory.append` | scope(student_profile\|task_memory), studentId, bookId?, kind, content, importance | mid | Agent 追加记忆（学生档案或教学任务笔记） |
| `memory.recall` | studentId, bookId?, kind?, query?, limit? | low | 检索记忆（近因+重要度加权排序；课堂开场/排课审视时调用） |
| `memory.revise` | memoryId, content, importance? | mid | 修订自己早先的记忆（归纳更新） |
| `memory.delete` | memoryId | mid | 删除失效记忆（如学生诉求已过时） |
| `memory.list` | studentId, bookId?, kind? | low | 全量列出（管理端/Agent 大盘点用） |

> 记忆 CRUD 全部经工具注册表（scope 校验：teacher_agent/system_agent 只能读写**自己 owner_role** 的记忆，互不可见；admin 只读全量用于审计）。写入带 source_lesson_id 关联课次，方便"查阅改进"时回溯。

### 3.3 调用管线

```
Agent(LLM function call) 
  → POST /api/v1/agent/tools/call {name, args}
  → ① AuthN（Agent 服务账号） ② Scope 检查 ③ JSON Schema 校验
  → ④ Risk=high：参数边界二次校验 + 强制 reason ⑤ 限流（单事件调用上限，I7）
  → ⑥ Handler 执行（带 trace_id, timeout）
  → ⑦ 审计落库（who/what/args/result/duration/trace_id）
  → 返回结构化结果给 Agent → 进入下一轮决策
```

---

## 4. 双 Agent 运行时设计

### 4.1 通用循环（agent/loop.go）

```
感知(事件/触发) → 组装上下文(课堂快照/学习档案/话术候选)
  → LLMProvider.Decide(prompt, tools)  // 输出：话术 + 工具调用序列
  → Guard 检查（本轮调用数 ≤ 8，累计时长 ≤ 60s）
  → 逐个执行工具（经 Registry）
  → 观察结果 → 若未达阶段完成条件，继续循环（上限内）
```

**设计原则**：
- LLM 只做**决策**（下一步做什么、说什么话），所有**状态变更**由工具 Handler 在平台侧事务执行——LLM 永不直接写库。
- Mock 模式下 `LLMProvider.Decide` 用确定性策略脚本（见 05 文档），TeacherAgent 与 SystemAgent 的接口完全一致。
- LLM 决策输出不包含任何状态推进指令（v1 无 advance_phase 工具，R3 移除）；阶段推进仅由平台状态机按决策表执行。

### 4.2 老师 Agent（TeacherAgentRuntime）— 确定性快路径 + LLM 异步话术注入

> 设计修订（评审发现 AI-01，P0）：真实 LLM 每事件 1~3s 决策时延会拖垮课堂节奏。
> 采用**双层模型**：课堂流程由"平台状态机 + 决策表"**确定性驱动（快路径）**；
> LLM 只负责**话术选择与口语化**，且在 TTS/跟读/作答等等待窗口内**异步预取**，永不阻塞流程。

**快路径（平台侧，确定性）**：
- 状态机收到学生事件/平台超时 → 按 04 文档决策表 T-01..T-20 直接计算下一动作（show_word/play_tts/start_quiz/show_check/...）并执行；
- 计时器归平台所有（§5.3）——Agent/LLM 存活与否不影响超时兜底推进（Agent 崩溃课堂不卡死）；
- LLM 话术层只输出话术，无任何状态推进能力（advance_phase 已于 R3 移除）；快路径下阶段推进由状态机自身完成。

**话术层（LLM，异步预取）**：
- 快路径执行动作的同时，向话术层请求该槽位话术（template_id + 变量）；
- Mock 期=本地即时渲染；真实模式=LLM 选模板（在等待窗口内返回）；
- **预取超时未返回 → 直接用服务端确定性抽取的模板原文**（跳过口语化），流程零等待；
- LLM 输出校验失败/自由发挥 → 同上降级，并记审计。

**生命周期**：学生进入课堂 → Runtime（快路径+话术层）启动，订阅课堂事件总线 → 课堂 end → 退出。

**事件清单**（快路径感知输入）：

| 事件 | 触发 |
|---|---|
| `lesson_started` | 学生进入 |
| `word_shown` / `tts_finished` | 展示/朗读完成回执 |
| `repeat_done` | 学生跟读完成（老师听读判读通过：ASR 转写匹配当前词，或超时兜底计为完成——2026-08-25 定案，单遍制） |
| `repeat_timeout` | 跟读超时（nudge 提醒后自动推进） |
| `student_answered` | 测试作答（文本或 ASR 转写结果） |
| `teaching_completed` | 本课全部词教完 → 触发 final 完整测试 |
| `quiz_completed` | final 测试判分完成 |
| `phase_timeout` | 任意等待超时（**平台计时器触发**） |

**上下文预算（话术层）**：仅含 当前槽位 + 当前词 + 最近 5 条事件 + 本课统计摘要（连对/错题/疲劳度）——token 有界，不随课程长度增长（评审发现 AI-05）。

**输出行为**：快路径直接调用 3.2 课堂域工具；话术经 `classroom.say` 下发。节奏参数（等待超时、鼓励频率）读配置中心。

### 4.3 系统 Agent（SystemAgentRuntime）

**触发源**（三种）：

1. **结课事件** `lesson_finished` → 评估本课 → `scheduler.plan_next_lesson`
2. **反馈事件** `feedback_submitted` → 意图解析 → 调整或转人工（见 §7）
3. **定时巡检**（cron 每 30min）→ 逾期检测 → `scheduler.postpone_lesson` + 通知

**评估逻辑输入**：正确率、平均作答时长、逾期次数、反馈情绪倾向 → 输出参数调整建议（在 §8 边界内）。

---

## 5. 课堂核心：状态机设计

> 对应 FR-D。课堂状态机是平台侧**唯一事实源**：阶段推进只由状态机自身（决策表+计时器）执行，Agent/LLM 无推进接口（R3）。

### 5.1 顶层阶段（Phase）

> **用户定案（2026-08-25）教学测试分离**：教学中不测试——全部词顺序教完才开始一次性完整测试（final）。
> 组内小测（GROUP_QUIZ）与综合测试（CUMULATIVE_TEST）已随之移除；组测/综测期间的全部错题重测语义并入 final 测试。

```
LESSON（可跨多个 SESSION 续课，见 §5.5）
  ├─ TEACHING (w=1..W, W=wordsPerLesson)  # 教学阶段，逐词顺序推进（无分组、无插入测试）
  │   ├─ SHOW_WORD                        #   中英对照展示（点击或 0.8s 自动开读）
  │   ├─ TTS_SEG (i=1..N+1)               #   朗读段：en×ttsEnRepeat 后 zh×1（R2-FE-02 拆分）
  │   ├─ REPEATING                        #   学生跟读一遍；老师听读判读（ASR 转写匹配当前词，
  │   │                                   #   correct/near → 判定读完；超时 nudge 后计为完成兜底）
  │   └─ WORD_DONE → 下一个词（直至全部教完）
  ├─ FINAL_TEST (教完全部词后一次性开始)     # 完整测试（全部词 Fisher-Yates 打乱，seed=lessonId*31+bookId）
  │   └─ QUIZ_ITEM (仅英文题面)            #   串行逐题（教学式问答=老师逐题问，非整卷并发考试）
  │       ├─ WAIT_ANSWER                  #   学生语音/文本作答
  │       ├─ JUDGING                      #   ASR→LLM 判分
  │       └─ 第1次错 → RETEACH 完整闭环（show_word(phase=with_zh) → zh→en→跟读 → 重测 round+1）
  │           第2次仍错/说"不会" → 记未掌握（ReteachN+FinalWrong）跳下一题（用户定案：每词最多 2 次机会）
  │   └─ QUIZ_DONE
  ├─ REVIEW_WRONG                         # final 错词轻量回顾（显中文+TTS 朗读，无跟读）
  │       # 实现注记：复用 tts_seg 相位 + InReview 上下文标志（非独立相位常量），
  │       # 快照含 InReview/ReviewIdx/ReviewList
  └─ LESSON_END                           # 总结话术 → 持久化 → 触发系统Agent
```

### 5.2 状态持久化（N2 可靠性，R2-BE-02 改列存）

```
lessons 表状态字段独立成列（非单一 JSON）：
  phase TEXT, group_idx INT, word_idx INT, repeat_count INT, tts_seg_idx INT,
  quiz_state JSON(仅题目项列表), cumulative_excluded_ids JSON, updated_at
每次状态迁移 = **单一 GORM 事务**：UPDATE lessons 状态列 + INSERT classroom_events。
崩溃恢复：以 lessons 各列为当前态；classroom_events 仅作审计回放（两者同事务写入，必然一致）。
进程重启 → Runtime 从 lessons 行恢复，Agent 收到 state_restored 事件继续。
```

### 5.3 超时兜底矩阵（D12，全部可配置）

| 阶段 | 默认超时 | 超时动作 |
|---|---|---|
| SHOW_WORD 等点击 | 0.8s | 自动开始朗读（用户定案二合一节奏：展示即开读，不等长思考；点击仍可立即开始） |
| **TTS_SEG 单段**（en 或 zh 各一段） | 词长+2s | 跳过本段记 `tts_skipped` 事件 → 下一段；段全完/全跳过 → 进跟读（R2 拆段粒度） |
| 单遍跟读 | 10s | 温和提醒 → 计为完成（不阻塞） |
| 测试作答 | 20s | Agent 提示 → 再 20s → 判为"不会"（错） |
| 判分（ASR/LLM） | 5s | mock 即时；真实超时→文本通道兜底询问 |
| **整堂 session** | 60min | 自动保存进度 → lesson 转 `in_progress_paused` → 温和下课话术 → 学生可随时再进入续课（R2-TE-01） |

**计时器归属**：全部超时计时器由**平台状态机**持有并触发（`phase_timeout` 是平台事件，不是 Agent 事件）——Agent/LLM 不可用时兜底推进照常；Agent 只消费该事件决定伴随话术（评审发现 AR-03）。

### 5.4 毕业/测试池规则（D9/F4）

> 2026-08-25 教学测试分离定案后，测试只剩 final 一种；小测/综合测试的连对计数场景随之消失，consec 仅在 final（含 RETEACH 重测）中累积。

- 每词维护 `consecutiveCorrect`（**课内计数**，结课清零）；final 测试（含重测）答对 +1，答错清 0。
- `consecutiveCorrect ≥ graduateThreshold(默认2)` → **课内掌握**：从本课期末重测池移除（打✓）。记忆卡 status 不变（长期毕业见 §6.2）。
- final 测试单独统计本课数据（打✓=课内掌握，打✗=进轻量回顾 REVIEW_WRONG，不再重测）。
- **打乱算法**（R2-TE-03）：final 测试池 Fisher-Yates 洗牌，`seed = lessonId*31 + bookId`，确定性可复现（E2E 断言稳定）。

### 5.5 长课与续课（R2-TE-01）

- wordsPerLesson 最大 100 词，单次 session 可能超长 → 引入 `sessionTimeoutMin`(默认 60)。
- 超时/学生主动退出：进度落库，lesson.status=`in_progress_paused`，下次进入**从断点续课**（batch 不变，已完成词不重学；已判分题不重判）。
- 续课超过 open_at+48h 未完成 → cron 判定课程过期，按 §6.4 逾期重排（已学部分记忆卡正常推进，未学部分 LadderStep 不变）。

### 5.6 课后学习报告（D-13/D16）

结课时 Machine.Result() 产出**结构化课报数据**，供老师 Agent 渲染与系统 Agent 审视：

```go
type LessonReport struct {
    // 教学评价数据
    TotalWords, CorrectCount, WrongFirstTry int
    CorrectRate          float64            // 首答正确率
    ReteachCount         int                // RETEACH 闭环触发次数（薄弱信号）
    AvgAnswerMs          int                // 平均作答时长（偏慢→S-04 慢学生判定）
    StreakBest           int                // 本课最长连对
    SlowWords            []int64            // 作答 >15s 的词
    // 建议数据
    WrongWords           []int64            // 首答曾错的词（下节关注）
    WeakMeaningWords     []int64            // RETEACH≥2 次的词（S-14 建议手动复习）
    // 进展总结数据（系统 Agent 注入，跨课聚合）
    CumLearned, CumGraduated, CumMastered int64
    CorrectRateTrend     []float64          // 最近 5 课正确率
    // 记忆曲线对照（S-13 审视输入）
    DueReviewPlanned, DueReviewDone int     // 到期复习词 计划/实际完成
    OverdueWords         int                // 本课开始时已逾期词数
    LadderHealth         float64            // 阶梯健康度=未逾期 learning 词占比
}
```

- **渲染分工**：老师 Agent 用 04 §5.9 课报话术模板（RB 系列）+ 口语化管线渲染三段式总结（下课时 `lesson_summary` UI + 通知中心存档）；系统 Agent 消费同数据走 S-13~S-15 决策。
- **量化+定性双段结构**（用户要求 2026-08-23）：
  - **量化统计表**（数字，学生端课程详情直显）：本课词数/首答正确率/RETEACH 次数/最长连对/平均作答时长/到期复习完成率 done÷plan/与上一课对比Δ（正确率、用时）；
  - **定性总结**（文字评语）：`定性标签`由规则从量化数据映射——`节奏快/适中/偏慢`（AvgAnswerMs 阈值 8s/15s）、`扎实/波动/需巩固`（CorrectRate ≥90/70~90/<70）、`记忆健康/阶梯粘滞`（LadderHealth ≥0.7/<0.6）——标签注入 RB 模板渲染成老师口吻评语；标签规则常量表 `reportQualitativeRules` 可配置。
- **持久化**：lessons.result JSON 存全量课报；/student/lessons/:id 详情返回。

---

## 6. 调度器设计（等间隔复习）

> 对应 FR-F；策略依据《别再被艾宾浩斯曲线"骗了"》：等间隔 + 间隔效应，不用前密后疏。

### 6.1 数据结构

```go
// 每生每词的记忆卡
type MemoryCard struct {
    StudentID, WordID string
    Status            string  // new|learning|mastered|graduated
    // LadderStep 语义（二轮评审 R2-BE-01 统一定义，排课/毕业/逾期三处共用）：
    //   已成功完成的复习次数 = 下次复习应取的间隔索引。
    //   初始 0（新学完，首次复习用 ladder[0]）；第 k 次复习成功 → k。
    //   读取间隔一律 interval = ladder[min(LadderStep, len(ladder)-1)]（防配置缩短越界）。
    LadderStep        int
    ConsecutiveCorrect int
    WrongCount        int
    LastReviewAt      *time.Time     // 最近一次实际学习/复习完成时间（非计划时间）
    NextDueAt         *time.Time     // = LastReviewAt + interval(LadderStep)
    BoostFlag         bool           // 反馈"难记"→ 优先+加密测试
}
```

### 6.2 间隔阶梯（可配置，等间隔思想）

```json
ladderDefault = [1,1,2,2,4,7,14,30]  // 天；ladder[k]=第k+1次复习距上次复习的间隔
// 目标模式 exam_sprint：[1,1,1,1,2,2,3,3]（整体压缩）
```

- **等间隔含义**：阶梯相邻同级内间隔相等（1,1 与 2,2），整体缓慢拉长，避免密集轰炸。
- **两个"毕业"严格区分**（R3 推演发现，禁止混用）：
  - **课内掌握**（lesson-scoped，§5.4）：`consecutiveCorrect ≥ graduateThreshold(2)` → 本课后续测试池移除该词（打✓）。**不改记忆卡 status**——下节课照常到期复习。若课内连对即退出排课，间隔复习将被彻底架空。
  - **长期毕业**（card-scoped）：`LadderStep ≥ len(ladder)` 且最近一次到期测试答对 → `status=graduated`，退出排课，进 30 天维护抽查（答错重置 `LadderStep = min(5, len-1)` 回 learning）。
- **step 推进规则**（结课时结算；R3 澄清：**新学不算复习**——新词首课=step 0 起点，只有后续课的复习成功才 +1）：本课该词任一测试（小测/综合/期末）答对 ≥1 次 → 视为本次复习成功，`LadderStep+1`；全错 → step 不变（省力再学），`LastReviewAt` 仍更新（间隔从本课重起）；**未上课（未进课堂）不触发任何结算**，由 §6.4 逾期重排处理。
- **逾期缺课**："省力再学" = 该词 `LadderStep` **不变**（错过不清零、也不前进，重走本级间隔）。
- **ladder 配置缩短**（R2-AR-02）：`config.set(ladder)` 生效时，同事务内将全部 `memory_cards.ladder_step` 截断为 `min(step, 新len-1)`，杜绝越界；毕业门槛随新 len 同步降低（截断到 len-1 的词再成功一次复习即 ≥ 新 len 毕业），自洽（R3-BE）。

### 6.3 排课算法（plan_next_lesson）

```
输入: studentId, bookId, config(wordsPerLesson W∈[40,100])
1. dueWords  = {status=learning, NextDueAt ≤ now+24h} 按 NextDueAt 升序   ← 全部保留
2. boosted   = {BoostFlag=true 且未在 due} 按标记时间倒序
3. newWords  = {status=new, 未被摸底标记 mastered} 按词库序
4. 批次构造（顺序与保留性，SC-04 语义）：顺序 = **boosted 头部 → due（按到期升序）→ new（按词库序）补足**；
   保留性 = due 全保留；new 数量 ≤ max(0.6×W, W−已填复习词数)——首课可全为新词补满 W，new 不足不强凑
5. 主线step（R2 定义）= 该生该库未毕业词(learning/mastered) LadderStep 的**中位数**（无词时取 0）
   lessonTime = 最近一次 status=finished 课程的 finished_at + ladder[min(主线step, len-1)]
   ★ 基线取 finished_at 而非 scheduled openAt（R2-BE-04：逾期补课后排期不漂移）
   （首个排课：now + 1d 当天学生偏好时段）
6. 事务内：写 lessons 行（status=scheduled, openAt=lessonTime, batch=词表快照）
   + 更新批次各 MemoryCard：NextDueAt=lessonTime（约课到课）
```

> 只排下一课（F2）：计划视图 = 历史课 + 已排课 + **虚线预测**（按阶梯推算展示，不落库）。

### 6.4 逾期重排（cron 每 30min）

```
lesson.status=scheduled && now > openAt + overdueHours(默认24h)
  → lesson.status=expired, log 逾期
  → SystemAgent 重排：新 lesson = 次日同时段；涉及词 LadderStep 不变（省力再学）
  → 词的 LastReviewAt 保持"最近实际学习时间"不变（未上课≠学习了）
  → 通知学生（通知中心 + 下次登录提示）
```

---

## 7. 反馈闭环设计

> 对应 FR-G。

### 7.1 数据流

```
学生提交(text, channel) → feedbacks 行(status=submitted)
  → SystemAgent 触发 feedback_submitted
  → LLM 意图解析（mock：规则+关键词）：
     {intent: A减量|B加量|C频率|D推迟|E难词|F投诉|G其他,
      params: {deltaWords?, lessonsPerDay?, date?, word?, note?},
      confidence}
  → 分派：
     intent∈{A,B,C} → feedback.apply_adjustment → ConfigService.Set（边界校验）
        边界内：applied + reply
        超边界：need_confirm → 回复学生确认（学生确认后二次提交直接 applied）
     intent=D → scheduler.postpone_lesson（目标日校验：≤7天，不与已排课冲突）→ applied
     intent=E → ProgressService 标记 BoostFlag → applied
     intent=F → feedback.escalate → 管理员待办 + 安抚回复
     confidence<0.6 或 intent=G → 追问一次；再不明 → escalate
```

### 7.2 状态机

```
submitted → processing → applied
                      → need_confirm → (确认)applied / (拒绝)closed
                      → escalated → (管理员处理)resolved
                      → rejected（如：与平台规则冲突）
```

### 7.3 边界规则示例（与 §8 联动）

| 调整 | 边界 | 超界处理 |
|---|---|---|
| wordsPerLesson | [40,100] | 学生确认 or escalate |
| lessonsPerDay | [1,2] | 同上 |
| 推迟天数 | ≤7 天/次，月累计 ≤3 次 | 同上 |
| 单词 Boost | 每词库同时 ≤20 词 | FIFO 淘汰最旧 |

---

## 8. 配置中心设计

> 对应 FR-H。**一切可调参数皆配置，Agent 与管理员共用同一 API，变更必带边界校验与审计。**

### 8.1 配置 Schema（全局默认 + 学生覆盖两层）

| key | 类型 | 默认 | 边界 | 说明 |
|---|---|---|---|---|
| wordsPerLesson | int | 60 | [50,100] | 每课词数（用户定案 2026-08-25：一次课 50~100 词） |
| ttsEnRepeat | int | 3 | [1,5] | 英文朗读遍数（3 遍英文+1 遍中文） |
| ttsIntervalMs | int | 1500 | [500,4000] | 朗读间隔 |
| repeatTimes | int | 1 | [1,5] | 跟读遍数（用户定案 2026-08-25：跟读一遍，老师听读判读通过即下一词） |
| timeoutShowWordS | int | 8 | [3,30] | 展示等点击（实际生效 0.8s 自动开读，见 §5.3 定案注记） |
| timeoutRepeatS | int | 10 | [5,60] | 单遍跟读（超时 nudge 后计为完成兜底） |
| timeoutAnswerS | int | 20 | [10,120] | 测试作答 |
| ladder | []int | [1,1,2,2,4,7,14,30] | 每项[1,60]，长度[3,10] | 间隔阶梯 |
| goalMode | enum | longterm | longterm\|exam_sprint | 目标模式 |
| lessonsPerDay | int | 1 | [1,2] | 每日课数 |
| overdueHours | int | 24 | [6,72] | 逾期判定 |
| graduateThreshold | int | 2 | [2,4] | 连对毕业 |
| encourageEveryN | int | 8 | [3,20] | 每 N 词鼓励一次 |

> 已废弃键（保留 schema 兼容、无行为语义）：`groupSize`（分组随 2026-08-25 教学测试分离定案移除——教学不再分组）、`quizOldWords`（小测混入旧词随组测移除）。

### 8.2 读取合并

```
effective(student, key) = studentOverride(student,key) ?? globalDefault(key)
启动时载入内存（原子 map），写入时同步刷新；所有 Handler 只读 effective 值。
```

### 8.3 写入管线

> **审计归属**（评审发现 AI-03，P1）：审计在 **ConfigService.Set 内部**落笔（而非仅 ToolRegistry 层）——Agent 经 `feedback.apply_adjustment` 间接触发的配置变更同样被审计，外层工具调用与内层配置变更两条记录以同一 trace_id 关联。

```
config.set(scope, studentId?, key, value, reason)
  → 边界校验（8.1 表）→ 失败返回违反的边界
  → 事务写 config_overrides / config_globals（含 old/new/reason/operator）
  → 审计事件 config_changed
  → 前端/Agent 下次读取生效；课堂进行中参数变更不打断当前课（下课生效）
```

---

## 9. 数据模型设计

> SQLite；全部软删除（deleted_at）；时间 UTC。核心表 16 张。

```sql
users(id, username UNIQUE, password_hash, role: admin|student, nickname, status, created_at, updated_at, deleted_at)
agent_accounts(id, name: teacher|system, api_key_hash, status)          -- Agent 服务账号

word_books(id, name, grade: g7..g12, description, status: draft|published|archived, word_count, created_by, ...)
word_entries(id, book_id, word, phonetic, pos, meanings JSON(≤2条), example_sentence, example_translation, freq_rank, UNIQUE(book_id,word))

student_books(id, student_id, book_id, status: active|done|dropped, placement_done BOOL, started_at)
placements(id, student_id, book_id, known_word_ids JSON, finished_at)   -- 摸底记录

lessons(id, student_id, book_id, seq, status: scheduled|open|in_progress|in_progress_paused|finished|expired,
        open_at, started_at, finished_at, batch_word_ids JSON,
        phase TEXT, group_idx INT, word_idx INT, repeat_count INT, tts_seg_idx INT,
        quiz_state JSON, cumulative_excluded_ids JSON, state_updated_at,   -- R2-BE-02: 状态拆列, 与 events 同事务
        result JSON)
classroom_events(id, lesson_id, ts, event, payload JSON, trace_id)      -- 课堂事件流（回放/审计）
ws_tickets(id, lesson_id, student_id, jwt_id, expires_at, created_at)   -- R2-AR-03: ticket 持久化(重启不失效)

memory_cards(id, student_id, word_id, status: new|learning|mastered|graduated,
             ladder_step, consecutive_correct, wrong_count, boost_flag,
             first_learned_at, last_review_at, next_due_at, UNIQUE(student_id,word_id))
quiz_items(id, lesson_id, kind: group_quiz|cumulative|final, word_id, question_type,
           answer_raw TEXT, asr_text, judgment: correct|wrong|near, judged_reason TEXT, judged_by, judged_at)

feedbacks(id, student_id, channel: in_lesson|page, text, status: submitted|processing|applied|need_confirm|escalated|rejected|closed,
          parsed_intent, parsed_params JSON, adjust_result JSON, admin_id, created_at, updated_at)
notifications(id, user_id, type, title, body, ref_id, read_at, created_at)

-- D-16: Agent 自维护记忆（平台只存取，内容由 Agent 决定）
agent_memories(id, owner_role: teacher_agent|system_agent, student_id, book_id NULL, -- NULL=学生档案, 否则=任务记忆
               kind, content TEXT, importance INT(1..5), source_lesson_id NULL,
               revised_from NULL, created_at, updated_at)  -- 索引(owner_role,student_id,book_id,updated_at)

config_globals(key, value JSON, updated_by, reason, updated_at)
config_overrides(id, student_id, key, value JSON, updated_by, reason, updated_at)
audit_logs(id, ts, actor_type: user|agent|system, actor_id, action, tool_name,
           args JSON, result JSON, risk, trace_id, duration_ms)

script_bank(id, category: open|transition|encourage|correct|closing|feedback_reply,
            template TEXT, tone, weight, variables JSON, enabled)
admin_todos(id, source: feedback|audit, ref_id, note, status, handled_by, created_at)
```

**索引**：
- memory_cards 部分索引 `idx_mc_due ON memory_cards(status, next_due_at)` WHERE status IN ('new','learning')（R2-BE-05，排课主查询）
- memory_cards(student_id, status, next_due_at)、lessons(student_id, status, open_at)、classroom_events(lesson_id, ts)、audit_logs(ts, actor_id)
- quiz_items 唯一约束 `(lesson_id, word_id, kind, round)`（判分幂等）
- ws_tickets(lesson_id, student_id, expires_at)

**SQLite 配置**（评审发现 BE-01）：连接参数固定 `journal_mode=WAL, busy_timeout=5000, foreign_keys=ON`；**写连接数=1**（所有写经单一写连接串行化），读连接数=4——家用规模（≤3 并发课堂）下消除 `SQLITE_BUSY` 锁竞争。

---

## 10. REST API 设计

> 统一前缀 `/api/v1`；JWT（用户）/ API-Key（Agent）；响应包 `{code, msg, data}`；全链路 trace_id（§14）。

### 10.1 认证与引导
```
POST /auth/login                  {username,password} → {token, role}
POST /auth/change_password
GET  /me                          → 个人信息+昵称
POST /me/nickname                 {nickname} 改昵称
GET  /bootstrap/status            → {needed} 首启引导探测
POST /bootstrap                   无 admin 时创建首个管理员（随后封死）；可选导入种子词库
```

### 10.2 词库（admin；students 只读已发布）
```
GET/POST/PUT/DELETE /wordbooks[/:id]
POST /wordbooks/:id/import/parse    {rawText} → {entries[], llmMeta}     # LLM解析
POST /wordbooks/:id/import/commit   {entries[]} → {inserted, skipped}
POST /wordbooks/:id/publish         # 发布（draft→published）
POST/PUT/DELETE /wordbooks/:id/entries[/:entryId]   # 词条增改删
GET  /wordbooks/:id/words?page&size                                     # 词条列表（复述，含摸底用）
GET  /wordbooks/:id/export          → csv
```

### 10.3 学生学习
```
GET  /student/books                                 # 我的已启用词库（Dashboard"我的词库"）
POST /student/books/:bookId/enable                  # 选库 → 触发摸底
GET  /student/books/:bookId/placement               # 摸底词条（含草稿恢复）
POST /student/books/:bookId/placement               {knownWordIds, finish?}（可多次提交；finish=true 完成→排课）
GET  /student/lessons?bookId                        # 计划视图（含虚线预测）
GET  /student/lessons/:id/report                    # 课报详情
GET  /student/notifications                        # 通知列表
POST /student/notifications/read                    # 全部标记已读
```

### 10.4 课堂（学生）
```
POST /classroom/lessons/:id/enter     # 校验开放时间 → 课堂快照 + WS ticket
POST /classroom/lessons/:id/event     {event, text}   # 学生事件（白名单校验；REST 兜底通道）
POST /classroom/lessons/:id/voice     {text}          # 语音转写文本（指令解析→跟读判读→反馈→作答）
POST /classroom/lessons/:id/resume    # 断点续课
# 注：作答无独立 /answer、跳过无独立 /skip——统一走 /event(answer_submitted|repeat_skipped) 或 /voice（"跳过"指令）
```

### 10.5 反馈与通知
```
POST /feedbacks                        {text, channel}
GET  /feedbacks/mine                   # 历史+处理结果
```

### 10.6 配置与管理（admin / system_agent）
```
GET  /config?keys=…                    # 配置读取（全局+学生覆盖）
POST /config/set                       {scope, studentId?, key, value, reason}
DELETE /config/override                {studentId, key}   # 删除学生覆盖→回落全局
GET  /admin/audit?actor&tool&from&to   # 审计查询（admin）

GET/POST  /admin/users                             # 学生账号管理（FR-A1/A4）
POST /admin/users/:username/reset_password         # 重置密码（按用户名）
POST /admin/users/:username/disable                # 停用（踢下线）
POST /admin/users/:username/enable
GET  /admin/todos                                  # 人工待办（投诉/越权告警）
```

### 10.7 Agent 专用
```
POST /agent/tools/call                 {name, args} → 工具结果（§3.3 管线）
GET  /agent/tools                      # 该 Agent 可见工具清单（给 LLM 的 function 定义）
```

---

## 11. WebSocket 协同协议设计

> 对应 FR-J。端点 `GET /ws/classroom?lessonId&token`；JSON 帧。

### 11.1 帧结构

```jsonc
// 通用帧
{ "seq": 123,            // 连接内单调递增，接收方去重
  "type": "...",         // 见下表
  "ts": 1724321234567,
  "traceId": "tr-xxxx",
  "payload": { } }
```

### 11.2 消息类型

**平台→学生（状态机动作驱动）**

| type | payload | 说明 |
|---|---|---|
| `state_snapshot` | 课堂全量状态（phase/word/quiz_word/zhMeaning/paused） | 连接/重连首帧 |
| `ui_action` | {action, …params} | UI 指令（=模拟点击）。action ∈ show_word / play_tts / tts_skipped / start_repeat / nudge_repeat / nudge_answer / start_quiz / show_quiz_item / show_check / show_cross / say_hint / start_review / say / lesson_summary / lesson_paused |
| `phase_changed` | {from, phase} | 阶段切换 |
| `judge_result` | {quiz_item, judgment, asr_text} | 判分反馈 |
| `lesson_summary` | {report} | 下课总结（D-13 课报） |

**学生→平台（经 Room 管线进状态机）**

| type | payload | 说明 |
|---|---|---|
| `student_event` | {event, text?} | 学生活动；event 白名单（R4-sec#6）：word_clicked / tts_finished / repeat_done / repeat_skipped / answer_submitted |
| `voice_text` | {text} | 语音转写文本（浏览器 Web Speech / Omni 中继）：指令解析→**跟读判读**→反馈捕获→作答 |
| `heartbeat` | — | 30s 心跳（回 heartbeat_ack） |
| `resume` | — | 断点续课 |

### 11.3 可靠性

- **双通道事件入口**（实现定案）：学生事件既可走 WS `student_event` 帧，也可走 REST `POST /classroom/lessons/:id/event`（文本兜底/弱网降级/测试通道）——两者进入同一 Room 管线，产生相同的 ui_action 广播流；REST `POST /classroom/lessons/:id/voice` 为语音转写文本入口（指令解析→反馈捕获→作答）。
- seq 去重；断线指数退避重连（1s/2s/4s…60s）；重连成功即收 `state_snapshot`。
- **重连鉴权**（R2-AR-03/FE-03 细化）：enter 签发 **签名 JWT ticket**，payload `{lesson_id, student_id, iat, exp}`，`exp = open_at + overdueHours + 1h`（覆盖续课窗口）；WS 连接校验签名+exp+绑定关系。ticket **持久化** `ws_tickets` 表（服务重启不失效）。防重放：连接数上限=2（双标签页），超出拒最旧连接；lesson finished/expired 后 ticket 立即失效。
- **多连接策略**（AR-02）：同一学生允许多标签页连接，全部收广播；学生事件按 `(connId, seq)` 去重；**作答按 quizItemId 幂等**（R2-BE-06：quiz_items 有 `(lesson_id, word_id, kind, round)` 唯一约束，判分前 `INSERT OR IGNORE` 抢占——并发重复提交只有一行胜出，败者读既有判分返回，无双判分）。
- **判分/超时竞态**（BE-05）：作答窗口超时与 ASR 结果同时到达时**先到事件生效**，后到者丢弃并记 warn 日志。
- 语音作答（实现定案）：浏览器 Web Speech / Omni 中继产出转写文本 → WS `voice_text` 帧或 REST `POST /classroom/lessons/:id/voice`（同一 HandleVoiceText 管线：指令解析→跟读判读→反馈捕获→作答判分）——音频不过 WS，避免大帧。

---

## 12. 语音能力设计

### 12.1 语音指令解析（D-14，全语音操作）

```
输入源: 课堂内 = Omni InputTranscript 流（每个转写文本先过指令解析器再进判分/反馈）
       课堂外 = 浏览器 Web Speech 识别结果(WS voice_text / REST /voice 上报)
解析器: internal/voicecmd —— 确定性词表匹配, 不经 LLM
  指令集: 下一个/继续 | 跳过 | 重听一遍 | 暂停 | 退出课堂 |
          我想反馈*(捕获至句尾→feedbacks 表, 走 §7 G2 管线) |
          首页/课程表/统计/开始学习(L3 导航)
          （"我读完了"口令已随 2026-08-25"老师听学生读完"定案移除——
            跟读推进只认听读判读，不认口令/按钮）
  匹配: 归一化(去标点/全半角) + 编辑距离≤1 容错("下一下"→下一个) + 前缀触发(反馈类)
  优先级: 反馈触发 > 教学指令 > 跟读判读 > 测试作答
          (quiz 阶段指令仅"跳过"生效; repeating 阶段指令仅"跳过"生效, 其余转写交听读判读)
产出: VoiceCommand{Type, Payload} → 注入课堂事件总线(等价对应 UI 按钮点击) → UI 按钮高亮回执(L4)
```

### 12.2 TTS（SpeechService）

```
接口: TTSProvider.Synthesize(ctx, text, lang, voice) → audio bytes/URL
实现: MockTTS(返回标记文本，前端转 Web Speech API 本地朗读)
      DashScopeTTS(Omni 非实时 qwen3.5-omni-plus 单段合成+播放队列编排, D-17 定案; cosyvoice 已砍, 见05)
播放: 前端 audio/ 模块统一排队播放（en×n 间隔 ttsIntervalMs + zh×1）
兜底: Web Speech API 的 voices 异步加载/onend 不可靠 → 前端按字数估时+1s 看门狗
      强制视为完成；Web Speech 完全不可用 → 静音计时+视觉动画，tts_finished 照常
      上报，流程不阻塞（评审发现 FE-03，P0）
```

### 12.3 跟读判定：老师听读判读（2026-08-25 定案，替代前端 VAD）

```
0. 回声抑制（评审发现 FE-02，P0）：REPEATING 阶段老师通道只用文字气泡、
   不发 TTS（nudge 提醒同样仅文字）——杜绝老师语音被学生麦克风拾音
1. 学生跟读开口 → 浏览器 Web Speech（en-US）/ Omni InputTranscript 持续识别
2. 转写文本 → HandleVoiceText 跟读判读分支：
   Judge(当前词, meanings, 转写) ∈ {correct, near} → 老师确认"听到了" → repeat_done → 下一词
   不匹配 → 继续听（不推进、不报错）
3. 总超时（timeoutRepeatS）兜底：nudge_repeat 温和提醒（"再试一次，大声读出来～"）
   → 仍超时 → 计为完成自动推进（不阻塞课堂）
4. 学生也可说"跳过"（跟读相位唯一放行指令）
输出: repeat_done 事件（不录音、不上传——只上传判定结果）
注: 前端 VAD 能量门限方案（RMS 基线+动态门限+TRAILING 判定）已废弃——
    能量只能证明"发了声"不能证明"读的是这个词"；判读走 ASR 转写+判分器 L1。
```

### 12.4 ASR + LLM 判分（测试作答）

```
学生语音作答（wait_answer 相位浏览器自动开麦，zh-CN）→ Web Speech / Omni 转写文本
  → WS voice_text 帧 / REST /voice（同一 HandleVoiceText 管线）
  → 非指令文本 → answer_submitted 进判分管线
  → Judge 两级判分: L1 归一化关键词匹配（中文同义/英文不区分大小写与冠词）
                   L2 智谱 GLM 语义判分（近似/同义表述）
文本兜底: 测试界面常驻输入框（K3 双通道；E2E/无麦克风环境走 REST 提交）
```

---

## 13. 前端设计

### 13.1 页面与路由

```
/login                 登录（学生/管理员同入口）
/books                 词库广场（学生）
/placement/:bookId     摸底（100词/屏 勾选）
/dashboard             课程表 + 通知中心 + 反馈入口（学生首页）
/classroom/:lessonId   协同课堂（核心页面，全屏模式）
/stats                 学习统计
/admin/*               管理后台：词库/导入/用户/配置/审计/待办
```

### 13.2 协同课堂页（classroom/）

```
状态源: ws/client ← state_snapshot + ui_action 流 → zustand classroomStore
渲染器: ActionRenderer —— ui_action.action → 对应组件：
  show_word   → WordCard（中英对照大字，点击开读）
  play_tts    → 播放队列（§12.2）+ 喇叭动画
  start_repeat→ RepeatPanel（"轮到你读了" + 🎙 监听指示——自动开麦 en-US，
                老师听读判读通过自动下一词；无"我读完了"按钮，2026-08-25 定案）
  start_quiz  → QuizPanel（仅英文题面 + 自动开麦 zh-CN + 文本输入兜底 + 倒计时环）
  show_check/cross → 判定动画（✓ 绿 / ✗ 红 抖动）
  start_review→ ReviewPanel（中文大字 + 自动播放英文）
  say         → TeacherBubble（老师头像气泡 + 可选 TTS 旁白）
断线: 顶部黄条"重连中…"；恢复自动 snapshot 续课
```

### 13.3 状态管理（Zustand）

```
authStore / booksStore / lessonsStore / classroomStore(WS驱动) /
feedbackStore / adminStore —— classroomStore 只由 WS 与本地回执写入，REST 不碰。
```

---

## 14. 可观测性设计（定位日志）

> 质量铁律 3：凭日志可直接定位，不需要猜。

### 14.1 结构化日志（zap → stdout + 文件滚动）

每条日志字段：

```jsonc
{ "ts":"...","level":"info","trace_id":"tr-xxx","module":"classroom",
  "event":"phase_changed","lesson_id":12,"actor":"teacher_agent",
  "payload":{"from":"TTS_PLAYING","to":"REPEATING","word":"apple"},
  "duration_ms":3 }
```

- **trace_id 贯穿**：REST 入口/WS 帧/Agent 决策轮/工具执行/判分/排课——同一请求链路同 ID。
- **必记事件清单**：登录、工具调用(全量)、状态机每次迁移、TTS/ASR 调用、判分输入输出、配置变更、排课计算（含选词依据）、反馈处理、逾期重排、WS 连接/断开/重连、错误（含堆栈）。
- classroom_events 表存课堂事件流（与日志互补：面向回放与 E2E 断言）。

### 14.2 排障路径示例

```
"学生说词被误判" → trace_id →
  audit_logs(判分工具调用: asr_text="阿剖" judgment=wrong reason=...)
  → classroom_events(answer_submitted) → 定位是 ASR 转写错 or 判分错，1 分钟内定位。
```

---

## 15. 安全设计

- 密码 bcrypt(cost=10)；JWT HS256 7 天；Agent API-Key 服务端持有、bcrypt 存哈希。
- Agent 越权调用（scope 外工具）→ 拒绝 + 审计 risk 标记 + 管理员待办。
- WS ticket 一次性（enter 课堂签发，防 URL 泄露盗连）。
- 上传音频限 2MB/条、限类型；批量导入文本 ≤ 500KB。
- CORS 仅同源（单服务部署无跨域）；SQL 全参数化（GORM）。

---

## 16 质量保障设计

> 质量铁律 1/2/4/5/6 的工程化落点。

### 16.1 专家评审门（gate）

| 产物 | 评审人（并行 review） |
|---|---|
| 每份设计文档 | 架构师 + 前端 + 后端 + AI-Agent 专家（+测试专家 for 03） |
| 后端模块代码 | 后端 + 测试 + 安全（Agent 域加 AI-Agent 专家） |
| 前端代码 | 前端 + 设计/交互 + 测试 |

流程：产出 → 并行专家 review（问题入清单，按 P0/P1/P2 定级）→ 逐条闭环 → 举一反三（同类扫描）→ 复审至清单清零 → 才算完成。

### 16.2 覆盖率标准

| 模块 | 语句覆盖率 |
|---|---|
| classroom（状态机） | ≥ 90% |
| scheduler | ≥ 90% |
| feedback（意图/边界） | ≥ 90% |
| tools/registry（校验/授权） | ≥ 90% |
| 其余后端模块 | ≥ 80% |
| 前端 stores/ws/audio | ≥ 70%（组件用 E2E 覆盖） |

工具：Go `go test -coverprofile` + gate 脚本；前端 vitest coverage。CI 不达标即失败。

### 16.3 多用户 E2E（铁律 5）

场景库见 [03-测试用例.md](03-测试用例.md) §4：学生×3 并行上课 + 管理员同时导入词库 + 双 Agent 并发处理反馈——拟真时序交互，非单接口冒烟。驱动：Go 集成测试（服务内起 httptest + WS 客户端池）+ Playwright（浏览器端）。

### 16.4 问题收敛流程（铁律 4/6）

```
发现问题(测试/review/日志) → 入 problems 表(级别/根因/类标签)
 → 修当前例 → 按类标签全库扫描同类 → 全部修复 → 回归全量测试
 → 复审 → 问题数>0 ? 继续循环 : 收敛达标
停止条件: review 清单清零 ∧ 覆盖率达标 ∧ 全测试绿 ∧ problems 收敛为 0
```

---

## 17. 部署设计

```
构建: make build → dist/enwords-server.exe(内嵌 web/dist 静态资源) + enwords.db
运行: 双击/命令行启动 → 监听 :8080 → 家庭局域网访问 http://<host>:8080
      开发: go run ./cmd/server [-persist] [-testmode] [-providers real]
      参数: -persist 启用 SQLite 持久化（默认 data/enwords.db；-db <路径> 自定义）
            -providers mock|real（real=智谱 GLM 判分L2+反馈解析，密钥读 secrets）
            -testmode 跳过课程开放时间校验（E2E 专用，产品禁用）
      持久化范围: 用户/词库/词条/记忆卡/课程/摸底草稿（store 包 14 表 GORM 自动迁移）
首次: 引导创建管理员 → 导入词库 → 建学生账号
备份: SQLite 单文件，内置每日自动 backup 到 ./backups（保留 14 份）
升级: 替换单文件，migrations 自动执行
```

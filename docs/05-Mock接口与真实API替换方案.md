# 05 Mock 接口与真实 API 替换方案

> 项目代号：en-words ｜ 版本：v0.4 ｜ 状态：待评审
> 原则（D-9）：LLM/TTS/ASR 全部走 **Provider 接口**；开发/测试期用确定性 Mock；用户提供阿里 DashScope 真实 API（地址/Key/样例）后，**改配置即切换，零代码改动**。
> v0.4 变更（D-11）：课堂语音双向交互采用百炼 **Qwen-Omni-Realtime** WebSocket 实时会话 API（服务端 VAD + ASR + LLM + 流式 TTS 一体，支持语音打断），新增 §4 Omni 通道设计；独立 TTS（CosyVoice）保留用于单词朗读（精确控制遍数/间隔/缓存）。

---

## 目录

1. [Provider 接口定义](#1-provider-接口定义)
2. [Mock 实现规格](#2-mock-实现规格)
3. [阿里 DashScope 接入预留](#3-阿里-dashscope-接入预留)
4. [Omni-Realtime 双向语音会话通道（D-11）](#4-omni-realtime-双向语音会话通道d-11)
5. [配置与切换](#5-配置与切换)
6. [验收：Mock→真实的切换清单](#6-验收mock真实的切换清单)

---

## 1. Provider 接口定义

### 1.1 LLM Provider（两个 Agent 共用）

```go
// internal/llm/provider.go
type LLMProvider interface {
    // 老师 Agent 决策：课堂事件 → {话术+工具调用}
    // 注: 快路径模型(02 §4.2/D-10)下，TeacherDecide 仅用于"话术层"选择
    //     (template_id+变量)；流程决策由平台决策表确定性执行，
    //     LLM 时延与故障不阻塞课堂（预取超时→服务端模板原文降级）。
    TeacherDecide(ctx context.Context, in TeacherInput) (TeacherOutput, error)
    // 系统 Agent 决策：结课/反馈/逾期 → {工具调用}
    SystemDecide(ctx context.Context, in SystemInput) (SystemOutput, error)
    // 判分：ASR 文本 vs 词条 → 对/近似/错 + 理由
    Judge(ctx context.Context, in JudgeInput) (JudgeOutput, error)
    // 批量导入解析：自由文本 → 结构化词条（含学段适配释义）
    ParseImport(ctx context.Context, in ParseInput) (ParseOutput, error)
}

type TeacherInput struct {
    Event       string          // §4.2 事件清单
    Classroom   ClassroomSnap   // 课堂快照（phase/组/词/计数）
    Student     StudentProfile  // 昵称/连对/错题/疲劳度
    Config      EffectiveConfig // 生效配置（超时/鼓励密度）
    ToolsAvail  []ToolDef       // 可用工具（function 定义）
}
type TeacherOutput struct {
    Say       string           // 已口语化的话术（可空）
    ToolCalls []ToolCall       // {name, args}
}
// JudgeInput{Word, Meanings, Stage, AsrText} → JudgeOutput{Judgment: correct|near|wrong, Reason}
// ParseInput{RawText, Grade} → ParseOutput{Entries []WordEntry, Warnings []string}
```

### 1.2 Speech Provider

```go
// internal/speech/provider.go
type TTSProvider interface {
    // 合成音频；返回可直接播放的资源（URL 或 bytes+mime）
    Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error)
    // TTSRequest{Text, Lang: en|zh, Voice, Speed}  TTSResult{Audio []byte, Mime, DurationMs}
}
type ASRProvider interface {
    // 音频 → 文本
    Recognize(ctx context.Context, audio []byte, mime string) (ASRResult, error)
    // ASRResult{Text, Confidence}
}
```

### 1.3 设计约束

- 接口即边界：业务代码（agent/classroom/wordbook）**只 import 接口包**，实现包由 wire 注入。
- 每个 Provider 实现自带：超时、重试(仅幂等操作)、trace_id 打点、错误分类（可重试/不可重试）。
- 判分与决策在 Mock 下**确定性**（同输入同输出），保证 E2E 可复现（03 文档 §6）。

---

## 2. Mock 实现规格

### 2.1 MockLLM

| 方法 | Mock 行为 |
|---|---|
| TeacherDecide | 04 文档 §7.1 决策表条件机；脚本变体 A/B/C 由 `mock.teacherScript` 配置选择 |
| SystemDecide | 04 文档 §7.2 决策表 + 意图规则表（03 §2.4） |
| Judge | **两级判分**（评审发现 AI-04）：第 1 级=确定性关键词归一化+同义表（20 组，可扩展 `testdata/synonyms.csv`；精确命中→correct，同义命中→near，空/噪音/"不知道"→wrong）；第 2 级=第 1 级未命中且非空 → LLM 细判（仅真实模式）；LLM 超时 3s → 回退第 1 级结果 |
| ParseImport | 行解析 `word 释义` / `word|词性|释义` / 自由文本按首词提取；释义取第一中文短语；缺失字段生成 `[phonetic?]` 占位告警 |

### 2.2 MockTTS

```
Synthesize(text, lang) → {Audio: nil, Mime: "text/mock-tts", Meta: {text, lang}}
前端 speech/ 模块约定: Mime=text/mock-tts → 使用浏览器 Web Speech API
  (speechSynthesis) 本地朗读，voice 显式选择·严格本地优先
  （精确 lang 本地 → 同语言族本地（zh voice 兜底读 en）→ 任意本地；
  2026-08-26 无声 BUG：默认选声可能命中 Google 在线语音，国内不可达
  → speak 静默挂起），u.lang 与选中 voice 对齐，
  DurationMs=按字数估算，朗读完成回执 tts_finished。
效果: 开发期零外部依赖即可全流程走通，且真实语音效果可提前体验。
```

### 2.3 MockASR

```
Recognize(audio, mime):
  E2E/开发模式: 若请求头 X-Mock-ASR-Script 携带预置文本 → 直接返回
  （前端测试面板可输入"我说的答案"模拟语音识别结果）
  否则: 返回 {Text:"", Confidence:0} → 前端引导走文本兜底通道
真实音频: mock 不做真实识别（明确提示），避免误导。
```

### 2.4 Mock 的价值边界

| Mock 能 | Mock 不能（真实 API 才有） |
|---|---|
| 全流程功能验证、E2E 确定性、边界/异常注入 | 真实语音自然度、口音鲁棒 ASR、LLM 开放域话术 |
| 判分规则正确性 | 同义/近义的真实覆盖度 |

→ 因此 §5 的切换验收必须用真实 API 重跑关键 E2E。

---

## 3. 阿里 DashScope 接入预留

### 3.1 预留配置（config.yaml，用户提供后填入）

```yaml
providers:
  llm:                             # D-12: 纯文本任务走智谱 GLM API 直连
    provider: mock                 # mock | zhipu | dashscope(备选)
    zhipu:
      api_key: ""                  # 已有：secrets/dashscope.local.yaml zhipu 段（coding plan key 复用，实测 200）
      base_url: "https://open.bigmodel.cn/api/coding/paas/v4"  # coding plan 端点(OpenAI 兼容)，实测 glm-4-flash/4.6/4.5-air 全通
      model: "glm-4-flash"         # 小任务足够；可升 glm-4.6
      timeout_s: 20
      max_retries: 2
    dashscope:                     # 备选（保留切换能力）
      api_key: ""
      base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1"
      model: "qwen-plus"
      timeout_s: 20
      max_retries: 2
  tts:                             # D-17: 单词朗读=Omni 非实时（砍掉 CosyVoice）
    provider: mock                 # mock | dashscope
    dashscope:
      api_key: ""                  # 已有（secrets/dashscope.local.yaml）
      model: "qwen3.5-omni-plus"   # 非实时；单段合成+后端播放队列编排遍数/间隔+预生成缓存
      voice: "Tina"                # 或克隆音色名（§4.6）
      format: "wav"
  asr:                             # D-17: P2 可选（降级链下一级=文本框零成本；默认不启用）
    provider: mock                 # mock | dashscope
    dashscope:
      api_key: ""
      model: "paraformer-v2"
      format: "wav"
  omni:                            # D-11: 课堂双向语音会话通道（DashScope 独占）
    provider: mock                 # mock | dashscope
    dashscope:
      api_key: ""                  # 已有（secrets/dashscope.local.yaml）
      model: "qwen3.5-omni-plus-realtime"        # 备选 qwen3-omni-flash-realtime / qwen-omni-turbo-realtime
      url: "wss://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/api-ws/v1/realtime"  # {WorkspaceId} 已实测可用
      voice: "Tina"                # 老师音色（qwen3.5 默认；音色列表以文档为准）
      turn_detection: "server_vad"               # server_vad | semantic_vad
      input_sample_rate: 16000                    # PCM 16k mono 16bit
      output_sample_rate: 24000
      session_idle_s: 300                         # 课堂语音会话空闲超时回收
```

> **D-12 模型分工定案（2026-08-23，用户确认）**：产品运行时仅两个真实供应商——
> ① **智谱 GLM（API 直连）**：全部纯文本任务（系统 Agent 反馈意图解析、判分第 2 级兜底、词库批量导入解析、音标补全）；
> ② **阿里 DashScope**：语音全部（Omni-Realtime 课堂双工会话 + 单词朗读 + 语音克隆）。
> 开发/测试期全部 Mock，零外部依赖。

> **D-17 模型收敛定案（2026-08-23，用户确认）**：语音通道收敛为 Omni 双模式——
> ① 单词朗读（en×3+zh×1）改用 **Omni 非实时**（qwen3.5-omni-plus）单段合成+后端播放队列精确编排遍数/间隔+预生成缓存，**砍掉 CosyVoice**；
> ② paraformer ASR 兜底**降为 P2 可选**（降级链下一级已是零成本文本输入框，配置位保留不实现）；
> ③ qwen-voice-enrollment 克隆音色保留（用户功能）。
> 最终运行时=2 供应商 4 模型（3 必选 1 可选）：omni-realtime / omni 非实时 / glm-4-flash / enrollment(+paraformer P2)。
> 风险预案：若实测 Omni 非实时朗读单词质量不达标（多音节/专有名词），恢复 CosyVoice 配置位即可（Provider 抽象下为配置变更）。

> **占位说明**：模型名/音色/BaseURL 均按 DashScope 公开文档预填，**最终以用户提供的真实 API 请求样例为准**——Provider 实现层把样例请求原样映射为代码参数（见 3.2）。
>
> **实测记录（2026-08-23）**：用户已提供 API Key 与 WorkspaceId（均存 `secrets/dashscope.local.yaml`，已 git-ignore）。
> ① LLM：OpenAI 兼容模式 `POST /compatible-mode/v1/chat/completions` + `qwen-flash` 实测 **200**；
> ② Omni-Realtime：`wss://ws-rc***.cn-beijing.maas.aliyuncs.com/api-ws/v1/realtime?model=qwen3.5-omni-plus-realtime` 实测 **PASS**（WS OPEN 鉴权通过，`session.created` + `session.update→session.updated` 全通）。
> TTS/ASR 独立通道待代码接入后按 §6 V-6/V-8 验证。

### 3.2 DashscopeLLM 实现要点（待真实样例后编写）

```
TeacherDecide/SystemDecide:
  messages = [system(角色人设+决策表摘要+话术规则)] + [user(事件+快照 JSON)]
  tools = ToolRegistry 导出的 function 定义
  → chat.completions(OpenAI 兼容) → 解析 tool_calls → TeacherOutput
  约束: 话术必须返回 template_id+variables（由服务端 render，§6 管线），或
        temperature=0.7 时允许受限自由话术（≤60字，过安全检查）
Judge: 仅确定性第 1 级未命中时调用；prompt 注入词条+学段释义+同义表 → 严格 JSON {judgment, reason}；超时 3s 回退第 1 级结果（两级判分，见 §2.1；课堂 5s 判分预算 = ASR 上传余量 + 3s LLM 上限）
ParseImport: 分批(50行/批) prompt 要求严格 JSON 数组输出 + 校验
```

### 3.3 DashscopeTTS / DashscopeASR 要点

```
TTS: 同步合成接口 → mp3 bytes → 前端 <audio> 播放；音频缓存 (text+voice) LRU 500 条
     （单词朗读重复率高，缓存显著省配额）
ASR: 【P2 预留，未实现】历史方案：前端 wav 上传 → REST POST /speech/asr →
      Provider.Recognize(paraformer 文件转写；2MB/20s 上限)——D-17 降级为 P2 可选；
      现行作答/跟读转写一律走 Web Speech / Omni InputTranscript 文本通道（02 §11.3、§12.3/§12.4）
```

---

## 4. Omni-Realtime 双向语音会话通道（D-11）

> 依据用户提供的百炼 Realtime API（wss://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/api-ws/v1/realtime?model=...）。
> 一个 WebSocket 会话同时提供：服务端 VAD（server_vad/semantic_vad）、输入 ASR 转写（qwen3-asr-flash-realtime）、LLM、流式 TTS 输出（24kHz PCM）、语音打断。

### 4.1 定位与通道分工

| 语音场景 | 通道 | 理由 |
|---|---|---|
| 单词朗读（en×3 + zh×1） | 独立 TTS（CosyVoice / Mock 浏览器） | 需精确控制遍数/间隔/字幕同步；可缓存省配额；不依赖会话状态 |
| 学生跟读判定 | **Omni server_vad**（主）+ 前端 VAD（兜底） | 服务端 VAD 天然免疫"老师 TTS 被麦克风拾音"回声问题（回声抑制由服务端声学处理+客户端静默策略保证） |
| 测试作答 ASR | **Omni 转写**（主）+ REST 上传 paraformer（兜底）+ 文本输入（最终兜底） | 流式转写延迟低于文件识别；三通道冗余 |
| 老师对话话术语音 | **Omni 流式输出**（instructions=老师人设） | 自然语音+可打断；与 ASR/LLM 同会话，上下文连贯 |
| 系统 Agent（课后事务） | 纯文本 LLM（qwen-plus） | 无实时语音需求 |

### 4.2 接口抽象

```go
// internal/speech/omni.go
type OmniSession interface {
    // 会话生命周期
    Open(ctx, SessionConfig) error            // 连接 + session.update（VAD/音色/instructions/音频格式）
    Close() error
    // 上行：学生麦克风流（前端 WS 二进制帧中继，16kHz PCM base64 分片 ~100ms）
    AppendAudio(ctx, pcm16k []byte) error
    // 下行事件流（服务端事件 → 平台事件总线）
    Events() <-chan OmniEvent
    // 主动让 Omni 说话（老师话术文本→流式语音；不经 LLM 自由发挥时用受控模式）
    Speak(ctx, text string) error
    CancelSpeaking() error                    // 学生开口时打断（配合 speech_started）
}
// OmniEvent 归一化（屏蔽协议细节）:
//   SpeechStarted{}                          → 触发打断/跟读起判
//   SpeechStopped{}                          → 学生停止说话（仅相位等待窗参考，不作跟读判定）
//   InputTranscript{text}                    → repeating 相位=跟读判读文本(Judge 当前词)；作答相位=作答文本
//   AudioDelta{pcm24k} / AudioDone{}         → 老师语音流（转发给学生端 WS）
//   Error{code,msg}                          → 降级触发
```

### 4.3 课堂接线（与 02-SDD 状态机对接）

```
学生浏览器 ──WS(课堂频道, 二进制音频帧)──▶ Go 后端 Omni 中继
                                          ├─ AppendAudio → DashScope
                                          ├─ ◀── OmniEvent 归一化后注入课堂事件总线:
                                          │    InputTranscript → HandleVoiceText 管线(指令解析:
                                          │      repeating=Judge(当前词,text)通过→repeat_done /
                                          │      作答=student_answered(text)→判分管线(两级))
                                          │    AudioDelta → 转发学生端(teacher_voice 帧)
                                          └─ 快路径话术槽: 话术层给文本 → Speak(text) 流式说出
约束:
  - REPEATING 阶段**双保险防回声**（R2-TE-05）：① 后端硬拦——REPEATING 期间对 Omni
    Session 的 Speak() 调用一律拒绝（服务端拦截，非仅靠 instructions 约束）；
    ② 前端静默——课堂 UI 检测到 REPEATING 阶段即 mute 全部老师音频输出通道（无论
    Omni/独立 TTS 是否误发；原 vad.ts 能量门限方案已废弃，见 02-SDD §12.3）。
    Omni 健康自检含"REPEATING 拦截验证"用例。
 - 非跟读阶段话术输出：优先 Omni Speak() 流式语音（phase-aware），失败降级独立 TTS，
   再降级文字气泡；REPEATING 期强制文字气泡（classroom.say）。
 - 测试作答阶段 SpeechStarted 触发 CancelSpeaking（学生抢答打断老师，拟真）
 - 每课堂一个 Omni 会话，lesson_end 即 Close；空闲 idle_s 自动回收
 - Omni 断连/连败 ≥5 → 降级链: 跟读判定回退前端 VAD；作答回退 REST ASR+文本；
   老师话术回退独立 TTS（课堂流程零中断，管理员告警）
```

### 4.4 MockOmniSession 规格

```
- Open/Close: 内存态
- AppendAudio: 丢弃（不解析）
- 事件注入: 由测试脚本按时间线推送（E2E 声明脚本号）:
    SpeakStopped×3 + 800ms 模拟跟读；InputTranscript="苹果"（预置答案）；
    AudioDelta 生成占位标记（前端显示"老师语音"动画）
- Speak(text): 立即回 AudioDone（前端 Mock TTS 朗读文本）
- 确定性: 同脚本号同事件序列，可 -count=10 重跑
```

### 4.5 安全与配额

- API Key 仅存服务端；学生浏览器**不直连** DashScope（WS 经后端中继，key 不下发）。
  （WebRTC 直连模式内置回声消除/降噪，但需临时凭证下发，列为 P2 优化项。）
- 会话配额保护：单课堂 Omni 会话数=1；单课 AudioDelta 总时长上限（默认 10min）告警。
- 全链路 trace：Omni 事件注入课堂总线时携带 trace_id=课堂 trace。

---

### 4.6 语音克隆通道（叮当老师音色定制，2026-08-23 新增）

> 依据官方文档实测结论：`qwen3.5-omni-plus-realtime / qwen3.5-omni-flash-realtime` 支持声音复刻
> （模型 `qwen-voice-enrollment`，端点 `https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/api/v1/services/audio/tts/customization`，
> 与 Omni 同 workspace 同 Key）。10~20 秒音频免训练克隆；创建的音色名直接填入 Omni 会话 `voice` 参数。

```go
// internal/voiceclone
type Provider interface {
    // Create 上传音频创建克隆音色；targetModel 必须与 Omni 会话模型一致（否则合成失败）
    Create(ctx, in CreateInput) (voice string, err error)
    // List 已创建音色（管理页下拉）
    List(ctx) ([]VoiceInfo, error)
}
// CreateInput{AudioWAV []byte(16bit 单声道 ≥24kHz 10~20s), PreferredName(≤16字符字母数字下划线), Text?, Language?}
// 校验(本地预检+服务端): 时长 10~60s / <10MB / ≥3s 连续清晰人声 / 无背景乐噪音
// VoiceInfo{Voice string(如 qwen-omni-vc-dingdang-voice-...), Name, TargetModel, CreatedAt}
```

- **使用方式**：管理端"老师音色"配置项 = 预置音色（Tina/Cherry…）∪ 克隆音色列表；选定值写入 config `voice.teacherVoice`，课堂 Omni 会话 session.update 的 voice 参数读取该配置。
- **Mock**：Create 返回 `mock-vc-<name>`；List 返回已创建集合——全流程可测。
- **计费**：创建音色 0.01 元/次（近免费）；对话计费不变。配额：每账号 1000 音色。

```
切换路径（零代码）:
  1. 用户填 config.yaml providers.*.provider=dashscope + api_key（omni.url 填真实 WorkspaceId）
     （或环境变量 ENWORDS_LLM/ENWORDS_OMNI=mock|dashscope 快速覆盖）
  2. 重启服务 → wire 按配置装配实现 → 启动日志打印各 Provider 实现+连通性自检
  3. 自检: 启动时对已启用的真实 Provider 发 1 次最小调用（llm: "ping" 补全;
     tts: 合成 "hello"; asr: 1s 静音; omni: 建会话+append 1s 静音+close）→ 失败则拒绝启动并明确报错（fail-fast）
降级: 任意真实 Provider 连续失败 ≥5 次 → 自动降级 mock（浏览器TTS/前端VAD/文本通道兜底）+ 管理员告警；
      课中降级不打断流程（快路径继续推进，话术自动改走模板原文，学生无感切换）
安全: API Key 加载后即脱敏持有，永不出现在日志/审计/错误消息；yaml 文件建议收紧
      文件权限（chmod 600）；env 变量优先级高于 yaml
```

---

## 6. 验收：Mock→真实的切换清单

| # | 检查 | 标准 |
|---|---|---|
| V-1 | 接口契约 | 业务代码 0 处 import 具体实现；切 dashscope 仅改配置 |
| V-2 | Mock 确定性 | 03 §6 脚本下全部 E2E 可重复跑 10 次全绿 |
| V-3 | 连通自检 | 真实配置错误 Key → 启动 fail-fast 报错明确 |
| V-4 | 真实决策一致率 | 04 §7.3：同输入 10 次，与决策表语义一致 ≥90% |
| V-5 | 真实判分抽测 | 100 题人工标注集：准确率 ≥95%（错例入同义表迭代） |
| V-6 | 真实 TTS/ASR | 20 词朗读可懂度人工评分；Omni 转写作答抽检 ≥90% 可用 |
| V-7 | 缓存与配额 | TTS 缓存命中率日志；单课外部调用数上限告警配置 |
| V-8 | Omni 通道（新增） | 真实会话：服务端 VAD 跟读判定准确率抽检 ≥90%；打断响应 <500ms；降级链演练（人为断 Omni）课堂零中断 |

**V-8 细化：全双工语音分层测试法（2026-08-23 定案，用户提供思路+扩展）**

| 层 | 方法 | 通过标准 |
|---|---|---|
| L1 内容回环 | 测试文本（正确/错误/近义/噪音/空）→ **异构 TTS**（Edge-TTS/本地 Piper，避免同源虚高；叠加白噪声/变速/换音色模拟不同学生）→ 喂 Omni `append_audio` → 收 `InputTranscript` → **拼音级模糊比对**（pypinyin 归一+编辑距离，同音"平果"≈"苹果"不算错）→ 接两级判分器断言 judgment | 100 用例转写可用率 ≥90%；判分准确率 ≥95% |
| L2 时序行为 | 预生成 PCM 片段（纯语音段/纯静音段/语音+800ms 尾静默）直接注入：① Speak 播放中注入学生语音 → `speech_started` 触发且 AudioDelta ≤500ms 停止（打断）；② 尾静默 → `speech_stopped`/`committed` 时机正确（VAD）；③ 空闲 idle 超时回收 | 打断 <500ms；VAD 判定与预期一致 |
| L3 课堂端到端 | Go 集成测试：真服务+真 Omni+Mock 学生行为（点击/发音节奏脚本）走完整课堂 | 全流程无卡死，事件序列完整 |
| L4 人工抽测 | 真人 20 词朗读/作答 | 可懂度人工评分（V-6） |

工具位置：`server/tests/omni_roundtrip/`（L1/L2 脚本，Edge-TTS 生成测试音频，可重复执行）；Mock 期用 MockOmniSession 跑同套用例（确定性），真实期切换 Provider 复跑。

> V-4~V-6、V-8 在用户提供真实 API 后执行；当前阶段以 V-1/V-2/V-3 验收。


### 28 当前生产判分验收补充

上文关键词匹配为历史口径，当前 L1 收紧为整体短答及显式同义表，不采用任意token或泛编辑距离。V-5 的100题、≥95%保留；旧评测脚本80输出token仅属历史诊断，当前本机生产合同为8秒、temperature=0、max_tokens=512、json_object。新40留出、旧100已见回归与已知B分别计分；强制L2诊断和同次响应生产回放分账，不二次推理、不因降级wrong给负例语义加分。候选提示待实测，须≥95%、安全误接收0且旧100不退化，不能按输出改标签或加入特例词表；本段不宣布质量通过。

### 2026-10-15 远端网关探索（不替换本机生产配置）

隔离`remoteprovider`组件合同与权限见33，远端取证及用户仅授权公开合成样本HTTP例外见35。真实探针确认`qwen-omni`和`minicpm-o`文本可达；1024 token、`response_format=json_object`的各一次公开任务分别返回Markdown包裹和思考标签，不符合严格裸JSON合同。这是兼容性样本，不是质量评测集，不能据此宣布模型整体通过或不合格。

普通非流式Chat Completions的input_audio（base64完整WAV）已实际验证接受；流式事件、取消确认和原生双工仍未知。公开合成探索分批保留：首批4例×2alias=8请求；扩集14例×2=28请求（中文12条+英文1+静音1，和首批重叠，不合并为22个独立样本），均200/stop，扩集qwen中文9/12、英文1/1、静音0/1，minicpm完整转写0/14（含think/空输出等格式问题，不等于听力全部错误）。另新边界12标签×2=24计划，其中新猫合成超时导致2未请求，实际22请求全200/stop；qwen严格6/12、minicpm1/12，两alias静音及非语音脉冲均0/2。新边界音源未人工试听，混说偏差不能全归ASR；旧12中文有人审签收。原gold和SHA先于识别固定，不剥think/fence改分母。详见review/remote-synthetic-audio-review-20261015.md和remote-synthetic-audio-boundary-review-20261015.md。此为synthetic exploratory轨，不替代真人300样本、声学、完整课堂或双工准入。未来仍须分开验证适配协议、识别质量、教学判分和物理声学；HTTP200不等于内容正确。组件的JSON解码不执行完整业务schema：必填字段、枚举及数值范围须由调用它的业务适配层显式检查，未验证前不得进入教学决策。现有8090服务与模型配置保持不变。

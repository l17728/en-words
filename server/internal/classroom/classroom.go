// Package classroom 实现课堂状态机（SDD §5）：平台侧唯一事实源。
// 段级 TTS、RETEACH 完整闭环、判分幂等、超时兜底矩阵、课内掌握出池、
// final 测试确定性打乱、session 暂停恢复、结课量化+定性课报（D-13）。
package classroom

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"enwords/internal/config"
)

// ---- Phase ----

const (
	PhaseShowWord    = "show_word"
	PhaseTTSSeg      = "tts_seg"
	PhaseRepeating   = "repeating"
	PhaseWaitAnswer  = "wait_answer"
	PhaseJudging     = "judging"
	PhaseReteachShow = "reteach_show"
	PhaseLessonEnd   = "lesson_end"
)

// ---- Action 类型 ----

const (
	ActShowWord      = "show_word"
	ActPlayTTS       = "play_tts"
	ActTTSSkipped    = "tts_skipped"
	ActStartRepeat   = "start_repeat"
	ActNudgeRepeat   = "nudge_repeat"
	ActStartQuiz     = "start_quiz"
	ActShowQuizItem  = "show_quiz_item"
	ActShowCheck     = "show_check"
	ActShowCross     = "show_cross"
	ActNudgeAnswer   = "nudge_answer"
	ActJudgeRequest  = "judge_request"
	ActJudgeFallback = "judge_fallback"
	ActStartReview   = "start_review"
	ActSayHint       = "say_hint" // 老师提示帧（如两次机会用尽的安抚）
	ActLessonSummary = "lesson_summary"
	ActLessonPaused  = "lesson_paused"
)

// ---- 事件 ----

const (
	EvWordClicked     = "word_clicked"
	EvTTSFinished     = "tts_finished"
	EvRepeatDone      = "repeat_done"
	EvRepeatSkipped   = "repeat_skipped"
	EvAnswerSubmitted = "answer_submitted"
	EvQuizSkipped     = "quiz_skipped"
)

type QuizItemID struct {
	WordID int64  `json:"word_id"`
	Kind   string `json:"kind"` // final（历史值 group_quiz/cumulative 已随 2026-08-25 定案废弃）
	Round  int    `json:"round"`
	Seq    int    `json:"seq"` // 测验实例序号（每轮 quiz 递增；同词跨 quiz 不碰撞）
}

// Text encoding makes composite IDs safe as JSON map keys in durable snapshots.
func (id QuizItemID) MarshalText() ([]byte, error) {
	return json.Marshal([]any{id.WordID, id.Kind, id.Round, id.Seq})
}
func (id *QuizItemID) UnmarshalText(b []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(b, &parts); err != nil {
		return err
	}
	if len(parts) != 4 {
		return fmt.Errorf("invalid quiz item key")
	}
	for i, v := range []any{&id.WordID, &id.Kind, &id.Round, &id.Seq} {
		if err := json.Unmarshal(parts[i], v); err != nil {
			return err
		}
	}
	return nil
}
func (id QuizItemID) MarshalJSON() ([]byte, error) {
	type plain QuizItemID
	return json.Marshal(plain(id))
}
func (id *QuizItemID) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var key string
		if err := json.Unmarshal(b, &key); err != nil {
			return err
		}
		return id.UnmarshalText([]byte(key))
	}
	type plain QuizItemID
	return json.Unmarshal(b, (*plain)(id))
}

type Event struct {
	Type string
	Text string
	Item *QuizItemID // WS 层幂等：携带 item ID（R2-08/BE-06）
}

// ---- 判分 ----

type Judgment string

const (
	JudgmentCorrect Judgment = "correct"
	JudgmentNear    Judgment = "near"
	JudgmentWrong   Judgment = "wrong"
)

func (j Judgment) IsCorrect() bool { return j == JudgmentCorrect || j == JudgmentNear }

// ---- 领域输入/输出 ----

type Word struct {
	ID       int64    `json:"id"`
	Word     string   `json:"word"`
	Meanings []string `json:"meanings"`
}

// LessonReport D-13 课报：量化 + 定性（跨课聚合与记忆曲线对照由系统 Agent 注入）。
type LessonReport struct {
	TotalWords    int     `json:"total_words"`
	CorrectCount  int     `json:"correct_count"`
	WrongFirstTry int     `json:"wrong_first_try"`
	FirstTryRate  float64 `json:"first_try_rate"`
	ReteachCount  int     `json:"reteach_count"`
	StreakBest    int     `json:"streak_best"`
	AvgAnswerMs   int     `json:"avg_answer_ms"`
	WrongWords    []int64 `json:"wrong_words"`
	QualPace      string  `json:"qual_pace"`  // 快/适中/偏慢
	QualSolid     string  `json:"qual_solid"` // 扎实/波动/需巩固
	// 记忆曲线对照（S-13 审视输入，结课后注入）
	DueReviewPlanned int     `json:"due_review_planned"`
	DueReviewDone    int     `json:"due_review_done"`
	OverdueWords     int     `json:"overdue_words"`
	LadderHealth     float64 `json:"ladder_health"`
}

type Result struct {
	TotalWords int
	Correct    map[int64]bool // wordID → 任一次答对
	Report     LessonReport
}

type Action struct {
	DelayBeforeMs int      `json:"delay_before_ms"`
	Type          string   `json:"type"`
	Lang          string   `json:"lang,omitempty"`
	SegIdx        int      `json:"seg_idx,omitempty"`
	WordID        int64    `json:"word_id,omitempty"`
	Word          string   `json:"word,omitempty"`
	WithZh        bool     `json:"with_zh,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	Total         int      `json:"total,omitempty"`
	Round         int      `json:"round,omitempty"`
	Meanings      []string `json:"meanings,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

// ---- 状态 ----

type State struct {
	Config                      *config.EffectiveSnapshot
	LessonID, BookID, StudentID int64
	Phase                       string
	GroupIdx, WordIdx           int // GroupIdx 废弃（D-21），保留持久化兼容
	TtsSegIdx                   int // Active teaching segment, 1..N(en)+M(zh).
	RepeatCount                 int
	RepeatNudged                bool
	// quiz
	QuizQueue   []QuizItemID
	QuizIdx     int
	QuizSeq     int // 测验实例计数器
	AnswerStage int
	PendingText string
	InReteach   bool // RETEACH 上下文（跟读完成后重测）
	ReteachSeg  int  // Version 1: active 1..M(zh)+N(en); legacy: 0=zh, 1=en.
	// 轻量回顾
	InReview   bool
	ReviewList []int64
	ReviewIdx  int
	ReviewSeg  int
	// 统计
	Consec     map[int64]int
	Judged     map[QuizItemID]Judgment
	Mastered   map[int64]bool
	ReteachN   map[int64]int
	FirstWrong map[int64]bool
	FinalWrong []int64
	Streak     int
	StreakBest int
	AnswerMs   []int
	StartedAt  time.Time
	Paused     bool
	Finished   bool
}

// Machine 课堂状态机（单课堂单 goroutine 驱动）。
type Machine struct {
	lessonID, bookID, studentID int64
	words                       []Word
	wordByID                    map[int64]Word
	cfg                         config.EffectiveSnapshot
	clock                       interface{ Now() time.Time }
	state                       *State
	acts                        []Action
	// 测试钩子
	lastActions []Action
	usedWrong   map[int64]int
}

func NewMachine(lessonID, bookID, studentID int64, words []Word,
	cfg config.EffectiveSnapshot, clock interface{ Now() time.Time }) *Machine {
	if cfg.PlaybackVersion == 0 {
		cfg = playbackDefaults(cfg)
		cfg.TtsZhIntervalMs, cfg.TtsSwitchIntervalMs = 1500, 1500
		cfg.PlaybackVersion = 1
	}
	m := &Machine{
		lessonID: lessonID, bookID: bookID, studentID: studentID,
		words: words, cfg: cfg, clock: clock,
		wordByID: map[int64]Word{}, usedWrong: map[int64]int{},
	}
	for _, w := range words {
		m.wordByID[w.ID] = w
	}
	m.state = &State{
		Config:   &cfg,
		LessonID: lessonID, BookID: bookID, StudentID: studentID,
		Phase:      PhaseShowWord,
		Consec:     map[int64]int{},
		Judged:     map[QuizItemID]Judgment{},
		Mastered:   map[int64]bool{},
		ReteachN:   map[int64]int{},
		FirstWrong: map[int64]bool{},
		StartedAt:  clock.Now(),
	}
	return m
}

func (m *Machine) Start() error {
	if len(m.words) == 0 {
		return fmt.Errorf("classroom: empty batch")
	}
	m.acts = append(m.acts, m.showWordAction())
	return nil
}

// ---- 只读 ----

func (m *Machine) Phase() string { return m.state.Phase }
func (m *Machine) Paused() bool  { return m.state.Paused }

func (m *Machine) CurrentWord() Word {
	if m.state.WordIdx < 0 || m.state.WordIdx >= len(m.words) {
		return m.words[len(m.words)-1]
	}
	return m.words[m.state.WordIdx]
}

type QuizItemView struct {
	QuizItemID
	Word string
}

// ID 返回题目标识。
func (v QuizItemView) ID() QuizItemID { return v.QuizItemID }

func (m *Machine) CurrentItem() QuizItemView {
	it := m.currentQuizItem()
	return QuizItemView{QuizItemID: it, Word: m.wordByID[it.WordID].Word}
}

func (m *Machine) currentQuizItem() QuizItemID {
	st := m.state
	if st.QuizIdx < len(st.QuizQueue) {
		return st.QuizQueue[st.QuizIdx]
	}
	return QuizItemID{}
}

func (m *Machine) DrainActions() []Action {
	as := m.acts
	m.acts = nil
	m.lastActions = as
	return as
}

func (m *Machine) Snapshot() State {
	// Deep copy maps/slices: saved state must not be mutated by the live machine.
	b, err := json.Marshal(m.state)
	if err != nil {
		panic(err)
	}
	var out State
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func (m *Machine) Restore(s State) error {
	if err := m.validateRuntimePosition(s); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("classroom: bad snapshot")
	}
	var cp State
	if err := json.Unmarshal(raw, &cp); err != nil {
		return fmt.Errorf("classroom: bad snapshot")
	}
	var restoredCfg config.EffectiveSnapshot
	if cp.Config == nil || cp.Config.PlaybackVersion == 0 {
		restoredCfg = legacySnapshotConfig(cp.Config)
	} else {
		restoredCfg = *cp.Config
	}
	if err := validatePlaybackSnapshot(restoredCfg); err != nil {
		return err
	}
	if err := validatePlaybackPosition(cp, restoredCfg); err != nil {
		return err
	}
	m.cfg = restoredCfg
	frozenCfg := m.cfg
	cp.Config = &frozenCfg
	if cp.InReview && cp.ReviewSeg == 0 {
		cp.ReviewSeg = 1
	}
	if cp.Judged == nil {
		cp.Judged = map[QuizItemID]Judgment{}
	}
	if cp.Consec == nil {
		cp.Consec = map[int64]int{}
	}
	if cp.QuizQueue == nil {
		cp.QuizQueue = []QuizItemID{}
	}
	m.state = &cp
	return nil
}

func (m *Machine) Result() *Result {
	if !m.state.Finished {
		return nil
	}
	correct := map[int64]bool{}
	for _, w := range m.words {
		correct[w.ID] = false
	}
	for it, j := range m.state.Judged {
		if j.IsCorrect() {
			correct[it.WordID] = true
		}
	}
	return &Result{TotalWords: len(m.words), Correct: correct, Report: m.buildReport(correct)}
}

func (m *Machine) buildReport(correct map[int64]bool) LessonReport {
	st := m.state
	correctCount := 0
	for _, ok := range correct {
		if ok {
			correctCount++
		}
	}
	rep := LessonReport{TotalWords: len(m.words), CorrectCount: correctCount}
	rep.WrongFirstTry = len(st.FirstWrong)
	rep.FirstTryRate = float64(rep.TotalWords-rep.WrongFirstTry) / float64(rep.TotalWords)
	rep.StreakBest = st.StreakBest
	for _, n := range st.ReteachN {
		rep.ReteachCount += n
	}
	for id := range st.FirstWrong {
		rep.WrongWords = append(rep.WrongWords, id)
	}
	sort.Slice(rep.WrongWords, func(i, j int) bool { return rep.WrongWords[i] < rep.WrongWords[j] })
	if len(st.AnswerMs) > 0 {
		sum := 0
		for _, v := range st.AnswerMs {
			sum += v
		}
		rep.AvgAnswerMs = sum / len(st.AnswerMs)
	}
	// 定性标签（SDD §5.6 规则）
	switch {
	case rep.AvgAnswerMs < 8000:
		rep.QualPace = "快"
	case rep.AvgAnswerMs <= 15000:
		rep.QualPace = "适中"
	default:
		rep.QualPace = "偏慢"
	}
	switch {
	case rep.FirstTryRate >= 0.9:
		rep.QualSolid = "扎实"
	case rep.FirstTryRate >= 0.7:
		rep.QualSolid = "波动"
	default:
		rep.QualSolid = "需巩固"
	}
	return rep
}

// ---- 超时 ----

func (m *Machine) TimeoutFor() (time.Duration, bool) {
	switch m.state.Phase {
	case PhaseShowWord:
		// 用户定案（二合一节奏）：show_word 仅 0.8s 自动开读（不等长思考；点击仍可立即开始）
		return 800 * time.Millisecond, true
	case PhaseRepeating:
		return time.Duration(m.cfg.TimeoutRepeatS) * time.Second, true
	case PhaseWaitAnswer:
		return time.Duration(m.cfg.TimeoutAnswerS) * time.Second, true
	case PhaseJudging:
		return 5 * time.Second, true
	case PhaseTTSSeg, PhaseReteachShow:
		// R4-arch#7：zh 段（释义更长）超时按释义长度估算；RETEACH 视同 zh 段
		action := m.currentPlaybackAction()
		w := m.wordByID[action.WordID]
		base := len(w.Word) + 2
		if action.Lang == "zh" {
			// zh 段：按首释义字符数估算（中文朗读约 2 字符/秒）
			n := 3
			if len(w.Meanings) > 0 {
				n = len([]rune(w.Meanings[0]))/2 + 2
			}
			if n > base {
				base = n
			}
		}
		return time.Duration(base)*time.Second + time.Duration(action.DelayBeforeMs)*time.Millisecond, true
	}
	return 0, false
}

func (m *Machine) OnTimeout(phase string) error {
	if m.state.Paused || m.state.Phase != phase {
		return nil
	}
	switch phase {
	case PhaseShowWord:
		return m.enterTTS()
	case PhaseTTSSeg:
		if m.state.InReview {
			return m.reviewAdvance()
		}
		m.acts = append(m.acts, Action{Type: ActTTSSkipped, SegIdx: m.state.TtsSegIdx})
		return m.ttsAdvance()
	case PhaseReteachShow:
		return m.reteachAdvance()
	case PhaseRepeating:
		if !m.state.RepeatNudged {
			m.acts = append(m.acts, Action{Type: ActNudgeRepeat})
			m.state.RepeatNudged = true
		}
		return m.repeatAdvance()
	case PhaseWaitAnswer:
		m.state.AnswerStage++
		if m.state.AnswerStage == 1 {
			m.acts = append(m.acts, Action{Type: ActNudgeAnswer})
			return nil
		}
		return m.applyJudge(m.currentQuizItem(), JudgmentWrong, true)
	case PhaseJudging:
		m.acts = append(m.acts, Action{Type: ActJudgeFallback})
		return nil
	}
	return nil
}

// ---- 事件 ----

func (m *Machine) OnEvent(e Event) error {
	if m.state.Paused {
		return nil
	}
	switch e.Type {
	case EvWordClicked:
		if m.state.Phase == PhaseShowWord && !m.state.InReview {
			return m.enterTTS()
		}
	case EvTTSFinished:
		switch m.state.Phase {
		case PhaseTTSSeg:
			if m.state.InReview {
				return m.reviewAdvance()
			}
			return m.ttsAdvance()
		case PhaseReteachShow:
			return m.reteachAdvance()
		}
	case EvRepeatDone, EvRepeatSkipped:
		if m.state.Phase == PhaseRepeating {
			return m.repeatAdvance()
		}
	case EvAnswerSubmitted:
		switch m.state.Phase {
		case PhaseWaitAnswer:
			if e.Item != nil {
				if _, done := m.state.Judged[*e.Item]; done {
					return nil // 已判题重复提交忽略
				}
			}
			m.state.PendingText = e.Text
			m.state.AnswerMs = append(m.state.AnswerMs, 0)
			m.state.Phase = PhaseJudging
			m.acts = append(m.acts, Action{Type: ActJudgeRequest, WordID: m.currentQuizItem().WordID})
		case PhaseJudging:
			m.state.PendingText = e.Text // 文本兜底重答
			m.acts = append(m.acts, Action{Type: ActJudgeRequest, WordID: m.currentQuizItem().WordID})
		}
	case EvQuizSkipped:
		if m.state.Phase != PhaseWaitAnswer {
			return nil
		}
		item := m.currentQuizItem()
		if e.Item != nil && *e.Item != item {
			return nil
		}
		if _, done := m.state.Judged[item]; done {
			return nil
		}
		m.state.Judged[item] = JudgmentWrong
		m.state.AnswerMs = append(m.state.AnswerMs, 0)
		delete(m.state.Mastered, item.WordID)
		m.state.FirstWrong[item.WordID] = true
		m.state.Consec[item.WordID] = 0
		m.state.Streak = 0
		if item.Kind == "final" {
			m.state.FinalWrong = appendUnique(m.state.FinalWrong, item.WordID)
		}
		m.acts = append(m.acts,
			Action{Type: ActShowCross, WordID: item.WordID, Reason: "explicit_skip"},
			Action{Type: ActSayHint, WordID: item.WordID, Reason: "give_up_next"},
		)
		return m.quizNext()
	}
	return nil
}

// ---- 教学推进 ----

func (m *Machine) showWordAction() Action {
	w := m.CurrentWord()
	return Action{Type: ActShowWord, WordID: w.ID, Word: w.Word}
}

func (m *Machine) enterTTS() error {
	st := m.state
	st.Phase = PhaseTTSSeg
	st.TtsSegIdx = 1
	w := m.CurrentWord()
	// Meanings：点击即显示中英对照（原始需求：点击后发音并显示汉语意思——不等到 zh 段）
	m.acts = append(m.acts, m.playbackAction(w, 1, false, false))
	return nil
}

// firstMeaning 首条释义（zh 段朗读文本）。
func firstMeaning(w Word) string {
	if len(w.Meanings) > 0 {
		return stripPOS(w.Meanings[0])
	}
	return w.Word
}

// stripPOS 去掉词性前缀（如 "n. 猫" → "猫"），纯中文送给语音引擎。
func stripPOS(s string) string {
	// 常见词性前缀：n./v./adj./adv./prep./conj./pron./art./num./int.
	for _, pre := range []string{"n.", "v.", "adj.", "adv.", "prep.", "conj.", "pron.", "art.", "num.", "int.", "vt.", "vi.", "abbr.", "aux."} {
		if len(s) > len(pre)+1 && s[:len(pre)] == pre && s[len(pre)] == ' ' {
			return s[len(pre)+1:]
		}
	}
	return s
}

func (m *Machine) ttsAdvance() error {
	st := m.state
	w := m.CurrentWord()
	en, zh := m.playbackCounts(false, false)
	if st.TtsSegIdx < en+zh {
		st.TtsSegIdx++
		m.acts = append(m.acts, m.playbackAction(w, st.TtsSegIdx, false, false))
	} else {
		st.Phase = PhaseRepeating
		st.RepeatCount = 0
		st.RepeatNudged = false
		m.acts = append(m.acts, Action{Type: ActStartRepeat, WordID: w.ID})
	}
	return nil
}

func (m *Machine) repeatAdvance() error {
	st := m.state
	st.RepeatCount++
	if st.RepeatCount < m.cfg.RepeatTimes {
		return nil
	}
	if st.InReteach {
		return m.retestAfterReteach()
	}
	return m.wordDone()
}

// wordDone 单词教学完成 → 下一词。
// 用户定案（2026-08-25）：教学中不测试——全部词顺序教完才开始完整测试（startFinal）。
func (m *Machine) wordDone() error {
	st := m.state
	st.WordIdx++
	if st.WordIdx >= len(m.words) {
		return m.startFinal() // 教学完成 → 完整测试
	}
	st.Phase = PhaseShowWord
	m.acts = append(m.acts, m.showWordAction())
	return nil
}

// ---- quiz 构建 ----
// （组测/综测已随 2026-08-25"教学测试分离"定案移除：测试=教完全部词后一次完整 final）

func (m *Machine) quizStartAction(kind string, total int) Action {
	it := m.currentQuizItem()
	w := m.wordByID[it.WordID]
	return Action{Type: ActStartQuiz, Kind: kind, Total: total,
		WordID: it.WordID, Word: w.Word, Round: it.Round}
}

func (m *Machine) setQuiz(queue []QuizItemID) {
	st := m.state
	st.QuizSeq++
	for i := range queue {
		queue[i].Seq = st.QuizSeq
	}
	st.QuizQueue = queue
	st.QuizIdx = 0
	st.AnswerStage = 0
	st.Phase = PhaseWaitAnswer
	m.acts = append(m.acts, Action{Type: ActShowQuizItem, WordID: m.currentQuizItem().WordID})
}

// shuffleIDs Fisher-Yates，seed=lessonID*31+bookID（R2-10 确定性可复现）。
func (m *Machine) shuffleIDs(pool *[]int64) {
	r := rand.New(rand.NewSource(m.lessonID*31 + m.bookID))
	p := *pool
	r.Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
}

// startFinal 完整测试（用户定案 2026-08-25：教完全部词后一次测试；
// 教学期无判分 → 全部词入题；确定性打乱）。
func (m *Machine) startFinal() error {
	var pool []int64
	for _, w := range m.words {
		if !m.state.Mastered[w.ID] {
			pool = append(pool, w.ID)
		}
	}
	m.shuffleIDs(&pool)
	queue := make([]QuizItemID, 0, len(pool))
	for _, id := range pool {
		queue = append(queue, QuizItemID{WordID: id, Kind: "final"})
	}
	if len(queue) == 0 {
		return m.enterEnd()
	}
	m.setQuiz(queue)
	m.acts = append(m.acts, m.quizStartAction("final", len(queue)))
	return nil
}

// ---- 判分 ----

func (m *Machine) OnJudged(item QuizItemID, j Judgment) Judgment {
	if prev, done := m.state.Judged[item]; done {
		return prev // 幂等（R2-08）
	}
	if item != m.currentQuizItem() {
		return j // 非当前题：忽略不落状态
	}
	_ = m.applyJudge(item, j, false)
	return j
}

// applyJudge 执行判分迁移；auto=超时自动判错。
// 用户定案（2026-08-23）：每词最多 2 次机会——
//
//	第 1 次错（Round==0）→ RETEACH 完整闭环后重测；
//	第 2 次错（Round>=1 重测仍错）或说"不会"→ 记未掌握，直接下一题。
func (m *Machine) applyJudge(item QuizItemID, j Judgment, auto bool) error {
	st := m.state
	if _, done := st.Judged[item]; done {
		return nil
	}
	st.Judged[item] = j
	w := m.wordByID[item.WordID]
	if j.IsCorrect() {
		m.acts = append(m.acts, Action{Type: ActShowCheck, WordID: item.WordID})
		m.consecUpdate(item, true)
		return m.quizNext()
	}
	reason := ""
	if auto {
		reason = "timeout"
	}
	m.acts = append(m.acts, Action{Type: ActShowCross, WordID: item.WordID, Reason: reason})
	m.consecUpdate(item, false)
	st.ReteachN[item.WordID]++
	if item.Kind == "final" {
		st.FinalWrong = appendUnique(st.FinalWrong, item.WordID)
	}
	if item.Round >= 1 {
		// 第 2 次机会用尽：记未掌握，跳下一题（用户定案；不再无限重试）
		m.acts = append(m.acts, Action{Type: ActSayHint, WordID: item.WordID,
			Reason: "give_up_next"}) // 前端渲染"没关系，记住它，我们继续"
		return m.quizNext()
	}
	// 第 1 次错：RETEACH 完整闭环（显中文→zh→en→跟读→重测）
	st.Phase = PhaseReteachShow
	st.ReteachSeg = 0
	if m.cfg.PlaybackVersion > 0 {
		st.ReteachSeg = 1
	}
	st.InReteach = true
	m.acts = append(m.acts, Action{Type: ActShowWord, WordID: item.WordID, Word: w.Word, WithZh: true})
	m.acts = append(m.acts, m.playbackAction(w, 1, true, false))
	return nil
}

func appendUnique(xs []int64, v int64) []int64 {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

// consecUpdate 课内连对（SDD §5.4）：final 测试答对计数。
func (m *Machine) consecUpdate(item QuizItemID, correct bool) {
	st := m.state
	if correct {
		st.Consec[item.WordID]++
		st.Streak++
		if st.Streak > st.StreakBest {
			st.StreakBest = st.Streak
		}
		if st.Consec[item.WordID] >= m.cfg.GraduateThreshold {
			st.Mastered[item.WordID] = true // 课内掌握（≠长期毕业，R3）
		}
	} else {
		st.Consec[item.WordID] = 0
		st.Streak = 0
		st.FirstWrong[item.WordID] = true
	}
}

// quizNext 答对后推进（测试只剩 final 一种——组测/综测已随"教学测试分离"定案移除）。
func (m *Machine) quizNext() error {
	st := m.state
	st.QuizIdx++
	st.AnswerStage = 0
	if st.QuizIdx < len(st.QuizQueue) {
		st.Phase = PhaseWaitAnswer
		m.acts = append(m.acts, Action{Type: ActShowQuizItem, WordID: m.currentQuizItem().WordID})
		return nil
	}
	return m.afterFinal()
}

// reteachAdvance RETEACH 段推进：zh → en → 跟读。
func (m *Machine) reteachAdvance() error {
	st := m.state
	item := m.currentQuizItem()
	st.ReteachSeg++
	seg := st.ReteachSeg
	if m.cfg.PlaybackVersion == 0 {
		seg++
	}
	en, zh := m.playbackCounts(true, false)
	if seg <= en+zh {
		m.acts = append(m.acts, m.playbackAction(m.wordByID[item.WordID], seg, true, false))
	} else {
		st.Phase = PhaseRepeating
		st.RepeatCount = 0
		st.RepeatNudged = false
		m.acts = append(m.acts, Action{Type: ActStartRepeat, WordID: item.WordID})
	}
	return nil
}

// retestAfterReteach RETEACH 跟读完成 → 原位替换为重测项（round+1）。
func (m *Machine) retestAfterReteach() error {
	st := m.state
	item := m.currentQuizItem()
	st.QuizQueue[st.QuizIdx] = QuizItemID{WordID: item.WordID, Kind: item.Kind,
		Round: item.Round + 1, Seq: item.Seq}
	st.Phase = PhaseWaitAnswer
	st.AnswerStage = 0
	st.InReteach = false
	w := m.wordByID[item.WordID]
	m.acts = append(m.acts, Action{Type: ActStartQuiz, Kind: item.Kind, WordID: item.WordID,
		Word: w.Word, Round: item.Round + 1, Total: 1})
	m.acts = append(m.acts, Action{Type: ActShowQuizItem, WordID: item.WordID})
	return nil
}

// afterFinal 期末完成 → 错词轻量回顾 → 下课。
func (m *Machine) afterFinal() error {
	st := m.state
	if len(st.FinalWrong) == 0 {
		return m.enterEnd()
	}
	st.InReview = true
	st.ReviewIdx = 0
	st.ReviewSeg = 1
	st.ReviewList = st.FinalWrong
	st.Phase = PhaseTTSSeg
	m.acts = append(m.acts, Action{Type: ActStartReview, Total: len(st.ReviewList)})
	m.emitReviewItem()
	return nil
}

func (m *Machine) emitReviewItem() {
	st := m.state
	w := m.wordByID[st.ReviewList[st.ReviewIdx]]
	m.acts = append(m.acts, Action{Type: ActShowWord, WordID: w.ID, Word: w.Word, WithZh: true})
	if st.ReviewSeg < 1 {
		st.ReviewSeg = 1
	}
	m.acts = append(m.acts, m.playbackAction(w, st.ReviewSeg, false, true))
}

func (m *Machine) reviewAdvance() error {
	st := m.state
	en, zh := m.playbackCounts(false, true)
	if st.ReviewSeg < en+zh {
		st.ReviewSeg++
		m.acts = append(m.acts, m.playbackAction(m.wordByID[st.ReviewList[st.ReviewIdx]], st.ReviewSeg, false, true))
		return nil
	}
	st.ReviewIdx++
	st.ReviewSeg = 1
	if st.ReviewIdx >= len(st.ReviewList) {
		return m.enterEnd()
	}
	st.Phase = PhaseTTSSeg
	m.emitReviewItem()
	return nil
}

func (m *Machine) enterEnd() error {
	m.state.Phase = PhaseLessonEnd
	m.state.Finished = true
	m.acts = append(m.acts, Action{Type: ActLessonSummary})
	return nil
}

// ---- Session 暂停/恢复（R2-09）----

func (m *Machine) OnSessionTimeout() error {
	st := m.state
	if st.Paused || st.Finished {
		return nil
	}
	if m.clock.Now().Sub(st.StartedAt) < time.Duration(m.cfg.SessionTimeoutMin)*time.Minute {
		return nil
	}
	st.Paused = true
	m.acts = append(m.acts, Action{Type: ActLessonPaused})
	return nil
}

func (m *Machine) Resume() {
	st := m.state
	if !st.Paused {
		return
	}
	st.Paused = false
	st.StartedAt = m.clock.Now()
	switch st.Phase {
	case PhaseWaitAnswer, PhaseJudging:
		m.acts = append(m.acts, Action{Type: ActShowQuizItem, WordID: m.currentQuizItem().WordID})
	case PhaseReteachShow:
		w := m.wordByID[m.currentQuizItem().WordID]
		m.acts = append(m.acts, Action{Type: ActShowWord, WordID: w.ID, Word: w.Word, WithZh: true})
		m.acts = append(m.acts, m.currentPlaybackAction())
	case PhaseTTSSeg:
		if st.InReview {
			m.emitReviewItem()
		} else {
			m.acts = append(m.acts, m.showWordAction())
			m.acts = append(m.acts, m.currentPlaybackAction())
		}
	case PhaseRepeating:
		w := m.CurrentWord()
		if st.InReteach {
			w = m.wordByID[m.currentQuizItem().WordID]
		}
		m.acts = append(m.acts, Action{Type: ActStartRepeat, WordID: w.ID})
	default:
		m.acts = append(m.acts, m.showWordAction())
	}
}

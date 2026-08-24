package classroom

import (
	"testing"
	"time"

	"enwords/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 测试基建 ----

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func newCfg() config.EffectiveSnapshot {
	cfg, _ := config.NewStore(nil, nil)
	return cfg.Snapshot(1)
}

func batchN(n int) []Word {
	ws := make([]Word, n)
	for i := range ws {
		ws[i] = Word{ID: int64(i + 1), Word: string(rune('a' + i)), Meanings: []string{"n. 测试"}}
	}
	return ws
}

func newMachine(t *testing.T, words []Word) (*Machine, config.EffectiveSnapshot) {
	t.Helper()
	cfg := newCfg()
	m := NewMachine(12, 3, 7, words, cfg, &fakeClock{t: time.Date(2026, 8, 23, 19, 0, 0, 0, time.UTC)})
	require.NoError(t, m.Start())
	m.DrainActions()
	return m, cfg
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func teachWord(m *Machine, cfg config.EffectiveSnapshot) {
	must(m.OnEvent(Event{Type: EvWordClicked}))
	for i := 0; i < cfg.TtsEnRepeat+1; i++ {
		must(m.OnEvent(Event{Type: EvTTSFinished}))
	}
	for i := 0; i < cfg.RepeatTimes; i++ {
		must(m.OnEvent(Event{Type: EvRepeatDone}))
	}
}

func teachGroup(m *Machine, cfg config.EffectiveSnapshot) {
	for i := 0; i < cfg.GroupSize; i++ {
		teachWord(m, cfg)
	}
}

// answerN 依次答 n 题；onceWrong: 指定词先错 onceWrong 次再对（走 RETEACH 闭环）
func answerN(m *Machine, cfg config.EffectiveSnapshot, n int, onceWrong map[int64]int) {
	for i := 0; i < n; i++ {
		for m.Phase() != PhaseWaitAnswer {
			if m.Phase() == PhaseLessonEnd {
				panic("answerN: lesson ended before " + itoa(n) + " answers (at " + itoa(i) + ")")
			}
			driveOneStep(m, cfg)
		}
		it := m.CurrentItem()
		j := JudgmentCorrect
		if m.usedWrong[it.WordID] < onceWrong[it.WordID] {
			j = JudgmentWrong
			m.usedWrong[it.WordID]++
		}
		must(m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "x"}))
		m.DrainActions()
		got := m.OnJudged(it.ID(), j)
		if got != j {
			panic("answerN: judgment mismatch")
		}
		m.lastActions = m.DrainActions()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func driveOneStep(m *Machine, cfg config.EffectiveSnapshot) {
	switch m.Phase() {
	case PhaseShowWord:
		_ = m.OnEvent(Event{Type: EvWordClicked})
	case PhaseTTSSeg:
		_ = m.OnEvent(Event{Type: EvTTSFinished})
	case PhaseRepeating:
		_ = m.OnEvent(Event{Type: EvRepeatDone})
	case PhaseReteachShow:
		_ = m.OnEvent(Event{Type: EvTTSFinished})
	default:
		return
	}
	m.lastActions = m.DrainActions()
}

func findAction(as []Action, typ string) *Action {
	for i := range as {
		if as[i].Type == typ {
			return &as[i]
		}
	}
	return nil
}

func teachToRepeating(m *Machine, cfg config.EffectiveSnapshot) {
	_ = m.OnEvent(Event{Type: EvWordClicked})
	for i := 0; i < cfg.TtsEnRepeat+1; i++ {
		_ = m.OnEvent(Event{Type: EvTTSFinished})
	}
	m.DrainActions()
}// ---- SM-01 教学主线 ----

func TestHappyPathTeachOneWord(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	require.Equal(t, PhaseShowWord, m.Phase())
	require.Equal(t, int64(1), m.CurrentWord().ID)

	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	assert.Equal(t, PhaseTTSSeg, m.Phase())
	a := findAction(m.DrainActions(), ActPlayTTS)
	require.NotNil(t, a)
	assert.Equal(t, "en", a.Lang)
	assert.Equal(t, 1, a.SegIdx)

	for i := 0; i < cfg.TtsEnRepeat; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	}
	as := m.DrainActions()
	assert.Equal(t, PhaseTTSSeg, m.Phase())
	var zh *Action
	for i := range as { // 找最后一个 play_tts = zh 段
		if as[i].Type == ActPlayTTS {
			zh = &as[i]
		}
	}
	require.NotNil(t, zh)
	assert.Equal(t, "zh", zh.Lang, "最后一段是中文释义")
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	assert.Equal(t, PhaseRepeating, m.Phase())
	assert.NotNil(t, findAction(m.DrainActions(), ActStartRepeat))

	for i := 0; i < cfg.RepeatTimes; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
	}
	m.DrainActions()
	assert.Equal(t, PhaseShowWord, m.Phase())
	assert.Equal(t, int64(2), m.CurrentWord().ID)
}

// ---- 用户定案：每词最多 2 次机会（第 2 次错/说不会 → 记未掌握跳下一题）----

func TestTwoChancesThenSkip(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	m.DrainActions()

	// 第 1 题连错 2 次（Round 0 错 → RETEACH → 重测 Round 1 再错 → 跳过）
	it1 := m.CurrentItem().ID()
	require.Equal(t, JudgmentWrong, m.OnJudged(it1, JudgmentWrong))
	m.DrainActions()
	require.Equal(t, PhaseReteachShow, m.Phase())
	// 走完 RETEACH 闭环到重测
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished})) // zh
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished})) // en
	m.DrainActions()
	for i := 0; i < cfg.RepeatTimes; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
	}
	as := m.DrainActions()
	require.NotNil(t, findAction(as, ActStartQuiz), "重测")
	// 重测（Round 1）再错 → 不再 RETEACH，跳下一题
	require.Equal(t, PhaseWaitAnswer, m.Phase())
	it1Retry := m.CurrentItem().ID()
	require.EqualValues(t, it1.WordID, it1Retry.WordID, "重测同词")
	require.Equal(t, JudgmentWrong, m.OnJudged(it1Retry, JudgmentWrong))
	as = m.DrainActions()
	require.NotNil(t, findAction(as, ActSayHint), "两次用尽发提示帧")
	assert.Equal(t, PhaseWaitAnswer, m.Phase(), "直接进入下一题")
	next := m.CurrentItem()
	assert.Equal(t, int64(2), next.WordID, "跳到下一词")
	assert.Equal(t, 2, m.state.ReteachN[it1.WordID], "未掌握已记录（两轮各+1）")
}

// 说"不会"（判 wrong）同理：重测轮放弃 → 跳过
func TestGiveUpSkipsOnRetest(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	m.DrainActions()
	it := m.CurrentItem().ID()
	require.Equal(t, JudgmentWrong, m.OnJudged(it, JudgmentWrong))
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	for i := 0; i < cfg.RepeatTimes; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
	}
	m.DrainActions()
	retry := m.CurrentItem().ID()
	// 学生说"不知道"（L1 判 wrong=放弃）
	require.Equal(t, JudgmentWrong, m.OnJudged(retry, JudgmentWrong))
	as := m.DrainActions()
	require.NotNil(t, findAction(as, ActSayHint))
	assert.Equal(t, PhaseWaitAnswer, m.Phase())
	assert.Equal(t, int64(2), m.CurrentItem().WordID)
}

// ---- R4-arch#7：zh 段超时按释义长度（区分 en/zh）----

func TestTTSSegTimeoutZhLonger(t *testing.T) {
	m, cfg := newMachine(t, []Word{
		{ID: 1, Word: "ab", Meanings: []string{"n. 一个非常非常长的中文释义用来测试超时时长估算"}},
	})
	// en 段：len("ab")+2 = 4s
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	dEn, ok := m.TimeoutFor()
	require.True(t, ok)
	assert.Equal(t, 4*time.Second, dEn)

	// 推进到 zh 段：释义 25 字符（含 "n. " 前缀）→ 25/2+2 = 14s > 4s
	for i := 0; i < cfg.TtsEnRepeat; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	}
	m.DrainActions()
	require.Equal(t, PhaseTTSSeg, m.Phase())
	dZh, ok := m.TimeoutFor()
	require.True(t, ok)
	assert.Greater(t, dZh, dEn, "zh 段（长释义）超时应大于 en 段")
	assert.Equal(t, 14*time.Second, dZh, "25 字符释义 → 25/2+2=14s")

	// RETEACH 上下文同样适用（视同 zh 段）
	teachGroup(m, cfg)
	m.DrainActions()
	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "不知道"}))
	m.DrainActions()
	it := m.CurrentItem()
	require.Equal(t, JudgmentWrong, m.OnJudged(it.ID(), JudgmentWrong))
	m.DrainActions()
	require.Equal(t, PhaseReteachShow, m.Phase())
	dRt, ok := m.TimeoutFor()
	require.True(t, ok)
	assert.GreaterOrEqual(t, dRt, 14*time.Second, "RETEACH zh 段超时按释义长度")
}

func TestIllegalEventIgnored(t *testing.T) {
	m, _ := newMachine(t, batchN(5))
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
	assert.Equal(t, PhaseShowWord, m.Phase())
}

// ---- SM-12 展示超时 ----

func TestShowWordTimeoutAutoPlay(t *testing.T) {
	m, _ := newMachine(t, batchN(5))
	d, ok := m.TimeoutFor()
	require.True(t, ok)
	assert.Equal(t, 800*time.Millisecond, d) // 用户定案：show_word 0.8s 自动开读（二合一节奏）
	require.NoError(t, m.OnTimeout(PhaseShowWord))
	assert.Equal(t, PhaseTTSSeg, m.Phase())
	assert.NotNil(t, findAction(m.DrainActions(), ActPlayTTS))
}

// ---- R2-05 TTS 段级超时跳段 ----

func TestTTSSegTimeoutSkipsSegment(t *testing.T) {
	m, _ := newMachine(t, batchN(5))
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	m.DrainActions()
	require.NoError(t, m.OnTimeout(PhaseTTSSeg))
	as := m.DrainActions()
	assert.NotNil(t, findAction(as, ActTTSSkipped))
	assert.Equal(t, PhaseTTSSeg, m.Phase())
	p := findAction(as, ActPlayTTS)
	require.NotNil(t, p)
	assert.Equal(t, 2, p.SegIdx)
}

// ---- SM-04 跟读超时计为完成 ----

func TestRepeatTimeoutFallback(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachToRepeating(m, cfg)
	require.NoError(t, m.OnTimeout(PhaseRepeating))
	assert.NotNil(t, findAction(m.DrainActions(), ActNudgeRepeat))
	for i := 1; i < cfg.RepeatTimes; i++ {
		require.NoError(t, m.OnTimeout(PhaseRepeating))
		m.DrainActions()
	}
	assert.Equal(t, PhaseShowWord, m.Phase())
}

// ---- SM-03 跳过跟读 ----

func TestRepeatSkipped(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachToRepeating(m, cfg)
	for i := 0; i < cfg.RepeatTimes; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatSkipped}))
	}
	m.DrainActions()
	assert.Equal(t, PhaseShowWord, m.Phase())
}

// ---- SM-06 组完成 → 小测（含旧词混入）----

func TestGroupQuizStart(t *testing.T) {
	m, cfg := newMachine(t, batchN(10))
	teachGroup(m, cfg)
	as := m.DrainActions()
	q := findAction(as, ActStartQuiz)
	require.NotNil(t, q)
	assert.Equal(t, "group_quiz", q.Kind)
	assert.Equal(t, 5, q.Total, "第一组无旧词 5 题")
	assert.Equal(t, PhaseWaitAnswer, m.Phase())
	assert.Empty(t, q.Meanings, "题面仅英文")
	// 答完组1小测 → 教组2 → 组2小测=5+3旧词
	answerN(m, cfg, 5, nil)
	teachGroup(m, cfg)
	as2 := m.DrainActions()
	q2 := findAction(as2, ActStartQuiz)
	require.NotNil(t, q2)
	assert.Equal(t, 8, q2.Total, "组2小测=5本组+quizOldWords(3)旧词(FR-D6)")
}

// ---- SM-05/R2-06 答错 → RETEACH 完整闭环 ----

func TestWrongAnswerReteachLoop(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	m.DrainActions()

	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "不知道"}))
	assert.Equal(t, PhaseJudging, m.Phase())
	assert.NotNil(t, findAction(m.DrainActions(), ActJudgeRequest))

	item := m.CurrentItem().ID()
	require.Equal(t, JudgmentWrong, m.OnJudged(item, JudgmentWrong))
	as := m.DrainActions()
	assert.NotNil(t, findAction(as, ActShowCross))
	sw := findAction(as, ActShowWord)
	require.NotNil(t, sw)
	assert.True(t, sw.WithZh, "RETEACH 显示中文释义")
	require.Equal(t, PhaseReteachShow, m.Phase())

	// R4-test#1：RETEACH 闭环逐相位断言（zh→en→repeating）
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished})) // zh
	enA := m.DrainActions()
	require.Equal(t, PhaseReteachShow, m.Phase(), "zh 段完成仍在 reteach（待 en）")
	en := findAction(enA, ActPlayTTS)
	require.NotNil(t, en)
	assert.Equal(t, "en", en.Lang)
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished})) // en
	assert.Equal(t, PhaseRepeating, m.Phase(), "en 完成进跟读（R4-test#1）")
	m.DrainActions()
	for i := 0; i < cfg.RepeatTimes; i++ {
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
	}
	as = m.DrainActions()
	q := findAction(as, ActStartQuiz)
	require.NotNil(t, q, "RETEACH 后重测同词")
	assert.Equal(t, int64(1), q.WordID)
	assert.Equal(t, PhaseWaitAnswer, m.Phase())

	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "测试"}))
	m.DrainActions()
	retest := QuizItemID{WordID: item.WordID, Kind: item.Kind, Round: item.Round + 1, Seq: item.Seq}
	require.Equal(t, JudgmentCorrect, m.OnJudged(retest, JudgmentCorrect))
	as = m.DrainActions()
	assert.NotNil(t, findAction(as, ActShowCheck))
	assert.Equal(t, PhaseWaitAnswer, m.Phase())
}

// ---- R2-08 判分幂等 ----

func TestJudgedIdempotent(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	m.DrainActions()
	item := m.CurrentItem().ID()
	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "x"}))
	m.DrainActions()
	first := m.OnJudged(item, JudgmentCorrect)
	m.DrainActions()
	assert.Equal(t, first, m.OnJudged(item, JudgmentCorrect), "幂等返回既有判分")
	assert.Equal(t, PhaseWaitAnswer, m.Phase())
	// WS 层同 item 重复提交（携带已判 item ID）→ 忽略
	dup := item
	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "again", Item: &dup}))
	assert.Equal(t, PhaseWaitAnswer, m.Phase(), "已判题重复提交忽略")
}

// ---- SM-07/R2-10 综合测试确定性打乱 ----

func TestCumulativePoolDeterministic(t *testing.T) {
	run := func(lessonID, bookID int64) []int64 { // R4-test#3：显式 bookID 参与 seed
		m := NewMachine(lessonID, bookID, 7, batchN(15), newCfg(),
			&fakeClock{t: time.Date(2026, 8, 23, 19, 0, 0, 0, time.UTC)})
		require.NoError(t, m.Start())
		m.DrainActions()
		cfg := newCfg()
		teachGroup(m, cfg)
		answerN(m, cfg, 5, nil)
		teachGroup(m, cfg)
		answerN(m, cfg, 8, nil) // 组2小测(5+3旧)
		cum := findAction(m.lastActions, ActStartQuiz)
		require.NotNil(t, cum, "组2小测完成后触发综合测试")
		require.Equal(t, "cumulative", cum.Kind)
		require.Equal(t, 10, cum.Total, "threshold=2: 组1/2 各对1次仍在池")
		var order []int64
		for i := 0; i < cum.Total; i++ {
			for m.Phase() != PhaseWaitAnswer {
				driveOneStep(m, cfg)
			}
			it := m.CurrentItem()
			order = append(order, it.WordID)
			require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "x"}))
			m.DrainActions()
			require.Equal(t, JudgmentCorrect, m.OnJudged(it.ID(), JudgmentCorrect))
			m.lastActions = m.DrainActions()
		}
		return order
	}
	o1 := run(12, 3)
	o2 := run(12, 3)
	o3 := run(12, 9)
	assert.Equal(t, o1, o2, "同 seed 重放一致")
	require.Len(t, o1, 10)
	assert.NotEqual(t, o1, o3, "不同 bookID 打乱序列应不同（seed=lessonID*31+bookID 的证据）")
}

// ---- SM-08 课内掌握出池 ----

func TestLessonScopedMastery(t *testing.T) {
	m, cfg := newMachine(t, batchN(15))
	teachGroup(m, cfg)          // 组1
	answerN(m, cfg, 5, nil)     // 小测 consec=1
	teachGroup(m, cfg)          // 组2
	answerN(m, cfg, 8, nil)     // 组2小测(含旧词3→组1部分词 consec=2)
	answerN(m, cfg, 10, nil)    // 综合1 全对 → 全部 consec=2 课内掌握
	teachGroup(m, cfg)          // 组3
	answerN(m, cfg, 5, nil)     // 组3小测(无旧词可混? 有——组1/2已掌握全部…quizOldWords 取未掌握旧词,无则少题)
	cum2 := findAction(m.lastActions, ActStartQuiz)
	require.NotNil(t, cum2)
	assert.Equal(t, "cumulative", cum2.Kind)
	assert.Equal(t, 5, cum2.Total, "组1/2 全部课内掌握出池, 综合2 仅组3")
}

// ---- SM-09 期末 → 轻量回顾 → 下课 ----

func TestFinalTestAndLessonEnd(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	answerN(m, cfg, 5, nil) // 小测 consec=1 未掌握 → 期末池=5
	fin := findAction(m.lastActions, ActStartQuiz)
	require.NotNil(t, fin)
	assert.Equal(t, "final", fin.Kind)
	// 期末: 词2 先错(闭环)后对 → 5 题 + 1 次重测 = 6 次作答
	answerN(m, cfg, 6, map[int64]int{2: 1})
	for m.Phase() != PhaseLessonEnd {
		driveOneStep(m, cfg)
	}
	assert.NotNil(t, findAction(m.lastActions, ActLessonSummary))
}

// 期末错词轻量回顾：显中文+朗读（无跟读）
func TestFinalWrongLightReview(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	answerN(m, cfg, 5, nil)                 // 小测全对
	answerN(m, cfg, 6, map[int64]int{2: 1}) // 期末词2错→闭环→对 (5题+1重测)
	found := false
	scan := func(as []Action) {
		for _, a := range as {
			if a.Type == ActStartReview {
				found = true
			}
		}
	}
	scan(m.lastActions) // afterFinal 的动作可能在 answerN 末次 drain 中
	for m.Phase() != PhaseLessonEnd {
		scan(m.DrainActions())
		driveOneStep(m, cfg)
	}
	// 词2曾在期末错 → 轻量回顾段(即使经闭环已答对, 仍进回顾列表逐词过)
	assert.True(t, found, "期末错词进入轻量回顾 start_review")
}

// ---- SM-10 快照恢复 ----

func TestSnapshotRestore(t *testing.T) {
	m, cfg := newMachine(t, batchN(10))
	teachWord(m, cfg)
	teachWord(m, cfg)
	m.DrainActions()

	snap := m.Snapshot()
	clk := &fakeClock{t: time.Date(2026, 8, 23, 19, 0, 0, 0, time.UTC)}
	m2 := NewMachine(12, 3, 7, batchN(10), cfg, clk)
	require.NoError(t, m2.Restore(snap))
	assert.Equal(t, m.Phase(), m2.Phase())
	assert.Equal(t, m.CurrentWord().ID, m2.CurrentWord().ID)
	teachWord(m2, cfg) // 教完第3词 → 当前第4词（断点续学不重学）
	assert.Equal(t, PhaseShowWord, m2.Phase())
	assert.Equal(t, int64(4), m2.CurrentWord().ID)
}

// ---- SM-11 超时矩阵 ----

func TestTimeoutMatrix(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	d, ok := m.TimeoutFor()
	require.True(t, ok)
	assert.Equal(t, 800*time.Millisecond, d) // 用户定案：show_word 0.8s 自动开读（二合一节奏）

	teachToRepeating(m, cfg)
	d, ok = m.TimeoutFor()
	require.True(t, ok)
	assert.Equal(t, 10*time.Second, d)

	teachGroup(m, cfg)
	m.DrainActions()
	d, ok = m.TimeoutFor()
	require.True(t, ok)
	assert.Equal(t, 20*time.Second, d)

	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "x"}))
	m.DrainActions()
	d, ok = m.TimeoutFor()
	require.True(t, ok)
	assert.Equal(t, 5*time.Second, d)
}

// ---- 作答超时两段式 ----

func TestAnswerTimeoutTwoStage(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	m.DrainActions()
	require.NoError(t, m.OnTimeout(PhaseWaitAnswer))
	assert.NotNil(t, findAction(m.DrainActions(), ActNudgeAnswer))
	assert.Equal(t, PhaseWaitAnswer, m.Phase())
	require.NoError(t, m.OnTimeout(PhaseWaitAnswer))
	as := m.DrainActions()
	assert.NotNil(t, findAction(as, ActShowCross))
	assert.Equal(t, PhaseReteachShow, m.Phase())
}

// ---- JUDGING 超时文本兜底 ----

func TestJudgingTimeoutFallback(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "听不清"}))
	m.DrainActions()
	require.NoError(t, m.OnTimeout(PhaseJudging))
	assert.NotNil(t, findAction(m.DrainActions(), ActJudgeFallback))
	assert.Equal(t, PhaseJudging, m.Phase())
}

// ---- R2-09 session 暂停/恢复 ----

func TestSessionTimeoutPauseResume(t *testing.T) {
	m, cfg := newMachine(t, batchN(10))
	teachWord(m, cfg)
	m.DrainActions()
	require.NoError(t, m.OnSessionTimeout())
	assert.False(t, m.Paused())
	m.state.StartedAt = m.state.StartedAt.Add(-61 * time.Minute)
	require.NoError(t, m.OnSessionTimeout())
	assert.True(t, m.Paused())
	assert.NotNil(t, findAction(m.DrainActions(), ActLessonPaused))
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	assert.True(t, m.Paused())
	m.Resume()
	as := m.DrainActions()
	assert.False(t, m.Paused())
	assert.NotNil(t, findAction(as, ActShowWord))
	assert.Equal(t, PhaseShowWord, m.Phase())
	assert.Equal(t, int64(2), m.CurrentWord().ID)
}

// ---- 结算+课报（D-13 量化+定性）----

func TestResultAndReport(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	answerN(m, cfg, 6, map[int64]int{2: 1}) // 小测: 词2 先错后对(5题+1重测)
	answerN(m, cfg, 5, nil)                 // 期末全对
	for m.Phase() != PhaseLessonEnd {
		driveOneStep(m, cfg)
	}
	res := m.Result()
	require.NotNil(t, res)
	for id := int64(1); id <= 5; id++ {
		assert.True(t, res.Correct[id])
	}
	rep := res.Report
	assert.Equal(t, 5, rep.TotalWords)
	assert.Equal(t, 5, rep.CorrectCount)
	assert.Equal(t, 1, rep.WrongFirstTry)
	assert.Equal(t, 1, rep.ReteachCount)
	assert.Equal(t, []int64{2}, rep.WrongWords)
	// 定性标签(默认快照下全部即时作答→节奏快; 首答率80%→波动)
	assert.NotEmpty(t, rep.QualPace)
	assert.NotEmpty(t, rep.QualSolid)
	assert.Contains(t, []string{"快", "适中", "偏慢"}, rep.QualPace)
	assert.Contains(t, []string{"扎实", "波动", "需巩固"}, rep.QualSolid)
}

// ---- 边界分支补充（覆盖率）----

func TestEdgeBranches(t *testing.T) {
	m, cfg := newMachine(t, batchN(5))

	// 未结课 Result = nil
	assert.Nil(t, m.Result())

	// 坏快照 Restore 报错
	bad := State{}
	assert.Error(t, m.Restore(bad))

	// OnTimeout 相位不匹配 → 忽略（防串相位）
	require.NoError(t, m.OnTimeout(PhaseJudging))
	assert.Equal(t, PhaseShowWord, m.Phase())

	// 非当前题 OnJudged：不落状态、返回原值
	teachGroup(m, cfg)
	m.DrainActions()
	other := QuizItemID{WordID: 999, Kind: "group_quiz", Round: 0, Seq: 99}
	got := m.OnJudged(other, JudgmentCorrect)
	assert.Equal(t, JudgmentCorrect, got, "非当前题返回原值")
	assert.Equal(t, PhaseWaitAnswer, m.Phase(), "状态不变")

	// 点击/朗读事件在错误相位被忽略
	require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	assert.Equal(t, PhaseWaitAnswer, m.Phase())
}

// Resume 在各相位的重呈现分支
func TestResumeVariants(t *testing.T) {
	// 空批次 Start 报错
	mEmpty := NewMachine(1, 1, 1, nil, newCfg(), &fakeClock{t: time.Now()})
	assert.Error(t, mEmpty.Start())

	// wait_answer 相位
	m, cfg := newMachine(t, batchN(5))
	teachGroup(m, cfg)
	m.DrainActions()
	m.state.Paused = true
	m.state.Phase = PhaseWaitAnswer
	m.Resume()
	assert.NotNil(t, findAction(m.DrainActions(), ActShowQuizItem))

	// reteach_show 相位
	m2, _ := newMachine(t, batchN(5))
	teachGroup(m2, cfg)
	m2.DrainActions()
	m2.state.Phase = PhaseReteachShow
	m2.state.Paused = true
	m2.Resume()
	assert.NotNil(t, findAction(m2.DrainActions(), ActShowWord))

	// repeating(正常教学)相位
	m3, _ := newMachine(t, batchN(5))
	m3.state.Phase = PhaseRepeating
	m3.state.Paused = true
	m3.Resume()
	assert.NotNil(t, findAction(m3.DrainActions(), ActStartRepeat))

	// repeating(reteach)相位 → 用当前 quiz 词
	m4, _ := newMachine(t, batchN(5))
	teachGroup(m4, cfg)
	m4.DrainActions()
	m4.state.Phase = PhaseRepeating
	m4.state.InReteach = true
	m4.state.Paused = true
	m4.Resume()
	a := findAction(m4.DrainActions(), ActStartRepeat)
	require.NotNil(t, a)
	assert.Equal(t, int64(1), a.WordID, "reteach 跟读恢复用 quiz 词")

	// review 相位
	m5, _ := newMachine(t, batchN(5))
	m5.state.Phase = PhaseTTSSeg
	m5.state.InReview = true
	m5.state.ReviewList = []int64{2}
	m5.state.ReviewIdx = 0
	m5.state.Paused = true
	m5.Resume()
	assert.NotNil(t, findAction(m5.DrainActions(), ActPlayTTS))
}

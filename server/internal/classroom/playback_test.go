package classroom

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPlaybackRestoreRejectsCorruptActivePosition(t *testing.T) {
	for _, mode := range []string{"teach", "reteach", "review"} {
		for _, seg := range []int{-1, 0, 1, 4, 5} {
			t.Run(fmt.Sprintf("%s/%d", mode, seg), func(t *testing.T) {
				m, _ := newMachine(t, batchN(1))
				before := m.Snapshot()
				st := m.Snapshot()
				st.Config.TtsEnRepeat, st.Config.TtsZhRepeat = 2, 2
				st.Phase, st.TtsSegIdx = PhaseTTSSeg, seg
				switch mode {
				case "reteach":
					st.Phase, st.InReteach, st.ReteachSeg = PhaseReteachShow, true, seg
				case "review":
					st.InReview, st.ReviewSeg = true, seg
				}
				err := m.Restore(st)
				if seg < 1 || seg > 4 {
					require.Error(t, err)
					require.Equal(t, before, m.Snapshot())
					require.Equal(t, *before.Config, m.cfg)
				} else {
					require.NoError(t, err)
				}
				// Stale counters outside a playback phase do not invalidate a snapshot.
				st.Phase = PhaseRepeating
				require.NoError(t, m.Restore(st))
			})
		}
	}
}

func collectPlayback(t *testing.T, m *Machine, phase string) []Action {
	t.Helper()
	var out []Action
	for i := 0; i < 12 && m.Phase() == phase; i++ {
		for _, a := range m.DrainActions() {
			if a.Type == ActPlayTTS {
				out = append(out, a)
				d, ok := m.TimeoutFor()
				require.True(t, ok)
				require.Greater(t, d, time.Duration(a.DelayBeforeMs)*time.Millisecond)
			}
		}
		require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	}
	return out
}

func assertPlayback(t *testing.T, as []Action, n int, reverse bool) {
	t.Helper()
	require.Len(t, as, n*2)
	for i, a := range as {
		lang := "en"
		if i >= n {
			lang = "zh"
		}
		if reverse {
			if lang == "en" {
				lang = "zh"
			} else {
				lang = "en"
			}
		}
		require.Equal(t, lang, a.Lang)
		require.Equal(t, i+1, a.SegIdx)
		delay := 100
		if lang == "zh" {
			delay = 200
		}
		if i == n {
			delay = 300
		}
		if i == 0 {
			delay = 0
		}
		require.Equal(t, delay, a.DelayBeforeMs)
		if lang == "zh" {
			require.Equal(t, "猫", a.Word)
		} else {
			require.Equal(t, "cat", a.Word)
		}
	}
}

func TestPlaybackCountsAndIntervalsAcrossTeachReteachReview(t *testing.T) {
	for _, n := range []int{1, 2, 5} {
		cfg := newCfg()
		cfg.TtsEnRepeat = n
		cfg.TtsZhRepeat = n
		cfg.TtsIntervalMs = 100
		cfg.TtsZhIntervalMs = 200
		cfg.TtsSwitchIntervalMs = 300
		m := NewMachine(1, 1, 7, []Word{{ID: 1, Word: "cat", Meanings: []string{"n. 猫"}}}, cfg, &fakeClock{t: time.Now()})
		require.NoError(t, m.Start())
		m.DrainActions()
		require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
		assertPlayback(t, collectPlayback(t, m, PhaseTTSSeg), n, false)
		require.Equal(t, PhaseRepeating, m.Phase())
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
		m.DrainActions()
		item := m.CurrentItem().ID()
		require.Equal(t, JudgmentWrong, m.OnJudged(item, JudgmentWrong))
		assertPlayback(t, collectPlayback(t, m, PhaseReteachShow), n, true)
		require.Equal(t, PhaseRepeating, m.Phase())
		require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
		m.DrainActions()
		require.Equal(t, JudgmentWrong, m.OnJudged(m.CurrentItem().ID(), JudgmentWrong))
		require.True(t, m.state.InReview)
		assertPlayback(t, collectPlayback(t, m, PhaseTTSSeg), n, false)
		require.Equal(t, PhaseLessonEnd, m.Phase())
	}
}

func TestPlaybackSnapshotPreservesZeroAndCurrentSegment(t *testing.T) {
	cfg := newCfg()
	cfg.TtsEnRepeat = 2
	cfg.TtsZhRepeat = 2
	cfg.TtsIntervalMs = 0
	cfg.TtsZhIntervalMs = 0
	cfg.TtsSwitchIntervalMs = 0
	m := NewMachine(1, 1, 7, batchN(1), cfg, &fakeClock{t: time.Now()})
	require.NoError(t, m.Start())
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	st := m.Snapshot()
	st.Paused = true
	current := newCfg()
	current.TtsEnRepeat = 5
	current.TtsZhRepeat = 5
	restored := NewMachine(1, 1, 7, batchN(1), current, &fakeClock{t: time.Now()})
	require.NoError(t, restored.Restore(st))
	restored.Resume()
	a := findAction(restored.DrainActions(), ActPlayTTS)
	require.NotNil(t, a)
	require.Equal(t, 2, a.SegIdx)
	require.Equal(t, "en", a.Lang)
	require.Zero(t, a.DelayBeforeMs)
	require.NoError(t, restored.OnEvent(Event{Type: EvTTSFinished}))
	a = findAction(restored.DrainActions(), ActPlayTTS)
	require.Equal(t, "zh", a.Lang)
	require.Equal(t, 3, a.SegIdx)
	require.Zero(t, a.DelayBeforeMs)
	require.Equal(t, 2, restored.Snapshot().Config.TtsEnRepeat)
}

func TestPlaybackLegacySnapshotKeepsShortReteachAndReview(t *testing.T) {
	m, cfg := newMachine(t, batchN(1))
	legacy := cfg
	legacy.PlaybackVersion = 0
	legacy.TtsEnRepeat = 5
	legacy.TtsZhRepeat = 5
	st := m.Snapshot()
	st.Config = &legacy
	st.Phase = PhaseReteachShow
	st.InReteach = true
	st.ReteachSeg = 0
	st.QuizQueue = []QuizItemID{{WordID: 1, Kind: "final"}}
	require.NoError(t, m.Restore(st))
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	a := findAction(m.DrainActions(), ActPlayTTS)
	require.NotNil(t, a)
	require.Equal(t, "en", a.Lang)
	require.Equal(t, 2, a.SegIdx)
	require.Zero(t, a.DelayBeforeMs)
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	require.Equal(t, PhaseRepeating, m.Phase())
	st = m.Snapshot()
	st.InReteach = false
	st.InReview = true
	st.Phase = PhaseTTSSeg
	st.ReviewList = []int64{1}
	st.ReviewIdx = 0
	st.ReviewSeg = 0
	require.NoError(t, m.Restore(st))
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	require.Equal(t, PhaseLessonEnd, m.Phase())
	// Entirely missing Config uses fixed legacy defaults, not constructor overrides.
	st.Config = nil
	st.InReview = false
	st.Phase = PhaseTTSSeg
	st.TtsSegIdx = 3
	require.NoError(t, m.Restore(st))
	require.Equal(t, 3, m.Snapshot().Config.TtsEnRepeat)
	require.Zero(t, m.Snapshot().Config.PlaybackVersion)
	m.DrainActions()
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	a = findAction(m.DrainActions(), ActPlayTTS)
	require.NotNil(t, a)
	require.Equal(t, "zh", a.Lang)
	require.Zero(t, a.DelayBeforeMs)
}

func TestPlaybackMaximumGapAndReviewResumeAreBounded(t *testing.T) {
	m, _ := newMachine(t, batchN(1))
	st := m.Snapshot()
	st.Config.TtsEnRepeat = 2
	st.Config.TtsZhRepeat = 2
	st.Config.TtsZhIntervalMs = 10000
	st.Config.TtsSwitchIntervalMs = 10000
	st.InReview = true
	st.ReviewList = []int64{1}
	st.ReviewSeg = 3
	st.Phase = PhaseTTSSeg
	st.Paused = true
	require.NoError(t, m.Restore(st))
	m.Resume()
	a := findAction(m.DrainActions(), ActPlayTTS)
	require.NotNil(t, a)
	require.Equal(t, 3, a.SegIdx)
	require.Equal(t, "zh", a.Lang)
	require.Equal(t, 10000, a.DelayBeforeMs)
	d, _ := m.TimeoutFor()
	require.Greater(t, d, 10*time.Second)
	require.NoError(t, m.OnEvent(Event{Type: EvTTSFinished}))
	a = findAction(m.DrainActions(), ActPlayTTS)
	require.Equal(t, 4, a.SegIdx)
	require.Equal(t, 10000, a.DelayBeforeMs)
	bad := m.Snapshot()
	before := m.Snapshot()
	bad.Config.PlaybackVersion = 2
	require.Error(t, m.Restore(bad))
	bad.Config.PlaybackVersion = 1
	bad.Config.TtsZhRepeat = 0
	require.Error(t, m.Restore(bad))
	bad.Config.TtsZhRepeat = 1
	bad.Config.TtsZhIntervalMs = -1
	require.Error(t, m.Restore(bad))
	require.Equal(t, before, m.Snapshot(), "invalid restore must preserve existing state")
	require.Equal(t, *before.Config, m.cfg, "invalid restore must preserve effective playback configuration")
}

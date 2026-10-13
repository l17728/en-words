package classroom

import (
	"enwords/internal/config"
	"fmt"
)

func playbackDefaults(cfg config.EffectiveSnapshot) config.EffectiveSnapshot {
	if cfg.TtsEnRepeat == 0 {
		cfg.TtsEnRepeat = 3
	}
	if cfg.TtsZhRepeat == 0 {
		cfg.TtsZhRepeat = 1
	}
	return cfg
}

func legacySnapshotConfig(saved *config.EffectiveSnapshot) config.EffectiveSnapshot {
	var cfg config.EffectiveSnapshot
	if saved == nil {
		store, _ := config.NewStore(nil, nil)
		cfg = store.Snapshot(0)
	} else {
		cfg = *saved
	}
	cfg = playbackDefaults(cfg)
	cfg.PlaybackVersion = 0
	cfg.TtsZhRepeat = 1
	// Legacy players never applied the stored interval. Keep the actual cadence.
	cfg.TtsIntervalMs, cfg.TtsZhIntervalMs, cfg.TtsSwitchIntervalMs = 0, 0, 0
	return cfg
}

func validatePlaybackSnapshot(cfg config.EffectiveSnapshot) error {
	if cfg.PlaybackVersion < 0 || cfg.PlaybackVersion > 1 {
		return fmt.Errorf("classroom: unsupported playback snapshot")
	}
	if cfg.TtsEnRepeat < 1 || cfg.TtsEnRepeat > 5 || cfg.TtsZhRepeat < 1 || cfg.TtsZhRepeat > 5 {
		return fmt.Errorf("classroom: invalid playback repeat count")
	}
	for _, gap := range []int{cfg.TtsIntervalMs, cfg.TtsZhIntervalMs, cfg.TtsSwitchIntervalMs} {
		if gap < 0 || gap > 10000 {
			return fmt.Errorf("classroom: invalid playback gap")
		}
	}
	return nil
}

// Only an active playback segment has a meaningful position. Transitional
// phases may retain the completed segment or have not initialized it yet.
func validatePlaybackPosition(s State, cfg config.EffectiveSnapshot) error {
	if cfg.PlaybackVersion == 0 || (s.Phase != PhaseTTSSeg && s.Phase != PhaseReteachShow) {
		return nil
	}
	seg := s.TtsSegIdx
	if s.Phase == PhaseReteachShow || s.InReteach {
		seg = s.ReteachSeg
	} else if s.InReview {
		seg = s.ReviewSeg
	}
	if seg < 1 || seg > cfg.TtsEnRepeat+cfg.TtsZhRepeat {
		return fmt.Errorf("classroom: invalid playback segment")
	}
	return nil
}

func (m *Machine) playbackCounts(reverse, review bool) (int, int) {
	en, zh := m.cfg.TtsEnRepeat, m.cfg.TtsZhRepeat
	if m.cfg.PlaybackVersion == 0 {
		zh = 1
		if reverse {
			en = 1
		}
		if review {
			en, zh = 1, 0
		}
	}
	return en, zh
}

func (m *Machine) playbackAction(w Word, seg int, reverse, review bool) Action {
	en, zh := m.playbackCounts(reverse, review)
	firstCount := en
	firstLang, secondLang := "en", "zh"
	if reverse {
		firstCount = zh
		firstLang, secondLang = "zh", "en"
	}
	lang := firstLang
	if seg > firstCount {
		lang = secondLang
	}
	delay := 0
	if seg > 1 && m.cfg.PlaybackVersion > 0 {
		if seg == firstCount+1 {
			delay = m.cfg.TtsSwitchIntervalMs
		} else if lang == "en" {
			delay = m.cfg.TtsIntervalMs
		} else {
			delay = m.cfg.TtsZhIntervalMs
		}
	}
	text := w.Word
	if lang == "zh" {
		text = firstMeaning(w)
	}
	return Action{Type: ActPlayTTS, Lang: lang, SegIdx: seg, WordID: w.ID, Word: text, Meanings: w.Meanings, DelayBeforeMs: delay}
}

func (m *Machine) currentPlaybackAction() Action {
	s := m.state
	if s.InReview && s.ReviewIdx >= 0 && s.ReviewIdx < len(s.ReviewList) {
		seg := s.ReviewSeg
		if seg < 1 {
			seg = 1
		}
		return m.playbackAction(m.wordByID[s.ReviewList[s.ReviewIdx]], seg, false, true)
	}
	if s.InReteach {
		seg := s.ReteachSeg
		if m.cfg.PlaybackVersion == 0 {
			seg++
		}
		if seg < 1 {
			seg = 1
		}
		return m.playbackAction(m.wordByID[m.currentQuizItem().WordID], seg, true, false)
	}
	seg := s.TtsSegIdx
	if seg < 1 {
		seg = 1
	}
	return m.playbackAction(m.CurrentWord(), seg, false, false)
}

package config

import "fmt"

type PlaybackSettings struct {
	TtsEnRepeat         int `json:"ttsEnRepeat"`
	TtsZhRepeat         int `json:"ttsZhRepeat"`
	TtsIntervalMs       int `json:"ttsIntervalMs"`
	TtsZhIntervalMs     int `json:"ttsZhIntervalMs"`
	TtsSwitchIntervalMs int `json:"ttsSwitchIntervalMs"`
}

func (s *Store) Playback(studentID int64) PlaybackSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return PlaybackSettings{TtsEnRepeat: s.effInt(studentID, "ttsEnRepeat"), TtsZhRepeat: s.effInt(studentID, "ttsZhRepeat"), TtsIntervalMs: s.effInt(studentID, "ttsIntervalMs"), TtsZhIntervalMs: s.effInt(studentID, "ttsZhIntervalMs"), TtsSwitchIntervalMs: s.effInt(studentID, "ttsSwitchIntervalMs")}
}

// SetPlayback publishes all five values together. A persistent caller must bind
// the Store's Persister to its outer transaction; this method does not open one.
func (s *Store) SetPlayback(actor string, studentID int64, global bool, values PlaybackSettings, reason string) error {
	if isBlank(reason) {
		return fmt.Errorf("playback settings require reason")
	}
	if (!global && studentID <= 0) || (global && studentID != 0) {
		return fmt.Errorf("invalid playback scope")
	}
	keys := []string{"ttsEnRepeat", "ttsZhRepeat", "ttsIntervalMs", "ttsZhIntervalMs", "ttsSwitchIntervalMs"}
	vals := []int{values.TtsEnRepeat, values.TtsZhRepeat, values.TtsIntervalMs, values.TtsZhIntervalMs, values.TtsSwitchIntervalMs}
	for i, key := range keys {
		if _, err := canon(key, vals[i]); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := make([]any, len(keys))
	for i, key := range keys {
		old[i] = s.effective(studentID, key)
	}
	if s.persist != nil {
		for i, key := range keys {
			var err error
			if global {
				err = s.persist.SaveGlobal(key, vals[i])
			} else {
				err = s.persist.SaveOverride(studentID, key, vals[i])
			}
			if err != nil {
				return fmt.Errorf("playback persistence failed: %w", err)
			}
		}
	}
	scope := ScopeStudent
	if global {
		scope = ScopeGlobal
	} else if s.overrides[studentID] == nil {
		s.overrides[studentID] = map[string]any{}
	}
	for i, key := range keys {
		if global {
			s.globals[key] = vals[i]
		} else {
			s.overrides[studentID][key] = vals[i]
		}
	}
	if s.audit != nil {
		for i, key := range keys {
			s.audit.ConfigChanged(AuditEntry{Actor: actor, Scope: scope, StudentID: studentID, Key: key, Old: old[i], New: vals[i], Reason: reason})
		}
	}
	return nil
}

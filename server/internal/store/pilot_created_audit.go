// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/config"
	"gorm.io/gorm"
)

func decodePilotPersistedPlan(raw []byte) (*PilotCreationPlan, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, errPilotSchemaInvalid
	}
	if err := pilotAuditJSON(raw); err != nil {
		return nil, err
	}
	var data pilotCreationPlanData
	if err := pilotDecodeLegacy(raw, &data); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(data)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, errPilotSchemaInvalid
	}
	if err = validatePilotPlanData(data); err != nil {
		return nil, err
	}
	fingerprint, err := pilotCanonicalHash(data)
	if err != nil {
		return nil, err
	}
	return &PilotCreationPlan{data: data, fingerprint: fingerprint}, nil
}

func validatePilotPlanData(data pilotCreationPlanData) error {
	if data.PlanVersion != 1 || data.CreationVersion != 1 || data.ConfigSnapshotVersion != 1 || data.StudentID <= 0 || data.BookID <= 0 || data.OpenAtMS < 0 || len(data.WordIDs) == 0 || len(data.WordIDs) > 100 {
		return errPilotSchemaInvalid
	}
	nonce, err := hex.DecodeString(data.Nonce)
	if err != nil || len(nonce) != 16 || hex.EncodeToString(nonce) != data.Nonce {
		return errPilotSchemaInvalid
	}
	seen := map[int64]bool{}
	for _, id := range data.WordIDs {
		if id <= 0 || seen[id] {
			return errPilotSchemaInvalid
		}
		seen[id] = true
	}
	keys := config.SortedKeys()
	if len(data.ConfigRevisions) != len(keys) {
		return errPilotSchemaInvalid
	}
	for i, key := range keys {
		r := data.ConfigRevisions[i]
		if r.Key != key || r.Revision.Global < 0 || r.Revision.Student < 0 {
			return errPilotSchemaInvalid
		}
	}
	if err = validatePilotFrozenConfig(data.Config); err != nil {
		return err
	}
	fingerprint, err := pilotCanonicalHash(data.Config)
	if err != nil || fingerprint != data.ConfigFingerprint {
		return errPilotSchemaInvalid
	}
	return nil
}

// Reuse the existing config schema on an isolated, nonpersistent Store. Never
// reread live settings, invoke application hooks, or invent duplicate ranges.
func validatePilotFrozenConfig(snapshot config.EffectiveSnapshot) error {
	values := map[string]any{
		"wordsPerLesson": snapshot.WordsPerLesson, "groupSize": snapshot.GroupSize,
		"ttsEnRepeat": snapshot.TtsEnRepeat, "ttsIntervalMs": snapshot.TtsIntervalMs,
		"ttsZhRepeat": snapshot.TtsZhRepeat, "ttsZhIntervalMs": snapshot.TtsZhIntervalMs,
		"ttsSwitchIntervalMs": snapshot.TtsSwitchIntervalMs, "repeatTimes": snapshot.RepeatTimes,
		"quizOldWords": snapshot.QuizOldWords, "timeoutShowWordS": snapshot.TimeoutShowWordS,
		"timeoutRepeatS": snapshot.TimeoutRepeatS, "timeoutAnswerS": snapshot.TimeoutAnswerS,
		"sessionTimeoutMin": snapshot.SessionTimeoutMin, "lessonsPerDay": snapshot.LessonsPerDay,
		"overdueHours": snapshot.OverdueHours, "graduateThreshold": snapshot.GraduateThreshold,
		"encourageEveryN": snapshot.EncourageEveryN, "goalMode": snapshot.GoalMode, "ladder": snapshot.Ladder,
	}
	cfg, err := config.NewStore(nil, nil)
	if err != nil {
		return err
	}
	for _, key := range config.SortedKeys() {
		value, ok := values[key]
		if !ok {
			return errPilotSchemaInvalid
		}
		if err = cfg.SetGlobal("pilot_codec", key, value, "validate frozen configuration"); err != nil {
			return errPilotSchemaInvalid
		}
	}
	got, err := pilotCanonicalHash(cfg.Snapshot(0))
	if err != nil {
		return err
	}
	want, err := pilotCanonicalHash(snapshot)
	if err != nil || got != want {
		return errPilotSchemaInvalid
	}
	return nil
}

func auditPilotCreatedDataTx(tx *gorm.DB) error {
	// This slice implements no transitions/effects/jobs. Nonempty later-lifecycle
	// facts require their future codecs; FK-valid alone cannot authorize them.
	for _, table := range []string{"classroom_transition_receipts", "classroom_answer_claims", "classroom_active_commands", "classroom_effect_outbox", "classroom_completion_jobs"} {
		var present bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM " + pilotQuote(table) + " LIMIT 1)").Scan(&present).Error; err != nil {
			return err
		}
		if present {
			return errPilotSchemaInvalid
		}
	}
	var reservations []pilotModeReservation
	if err := tx.Table("classroom_mode_reservations").Where("mode=?", "pilot").Order("lesson_id").Find(&reservations).Error; err != nil {
		return err
	}
	seenScopes := map[pilotScopeKey]bool{}
	for _, reservation := range reservations {
		scope := pilotScopeKey{StudentID: reservation.StudentID, BookID: reservation.BookID}
		if seenScopes[scope] {
			return errPilotSchemaInvalid
		}
		seenScopes[scope] = true
		var checkpoint struct{ CreationPlanJSON string }
		if err := tx.Table("classroom_pilot_states").Select("creation_plan_json").Where("lesson_id=?", reservation.LessonID).Take(&checkpoint).Error; err != nil {
			return err
		}
		plan, err := decodePilotPersistedPlan([]byte(checkpoint.CreationPlanJSON))
		if err != nil {
			return err
		}
		if reservation.StudentID != plan.data.StudentID || reservation.BookID != plan.data.BookID || reservation.SchemaVersion != 2 || reservation.CreationNonce == nil || *reservation.CreationNonce != plan.data.Nonce || reservation.CreationVersion == nil || *reservation.CreationVersion != 1 || reservation.CreationFingerprint == nil || *reservation.CreationFingerprint != plan.fingerprint {
			return errPilotSchemaInvalid
		}
		if err = verifyPilotCreatedReplayTx(tx, plan, reservation); err != nil {
			return err
		}
	}
	// Every state must belong to a pilot reservation, not merely to any FK-valid
	// legacy reservation. Equality also rejects extra unsupported legacy states.
	var count int64
	if err := tx.Table("classroom_pilot_states").Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(reservations)) {
		return errPilotSchemaInvalid
	}
	if err := tx.Table("classroom_scope_reservations").Where("mode=?", "pilot").Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(seenScopes)) {
		return errPilotSchemaInvalid
	}
	return nil
}

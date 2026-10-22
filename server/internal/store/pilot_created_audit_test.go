// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"encoding/json"
	"enwords/internal/config"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestPilotPersistedPlanRejectsAmbiguityAndInvalidDataDraft(t *testing.T) {
	_, _, _, plan := pilotCreationFixtureDraft(t)
	original, e := json.Marshal(plan.data)
	require.NoError(t, e)
	decoded, e := decodePilotPersistedPlan(original)
	require.NoError(t, e)
	require.Equal(t, plan.fingerprint, decoded.fingerprint)
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(original, &object))
	for key := range object {
		t.Run("missing_"+key, func(t *testing.T) {
			clone := map[string]json.RawMessage{}
			for k, v := range object {
				if k != key {
					clone[k] = v
				}
			}
			raw, e := json.Marshal(clone)
			require.NoError(t, e)
			_, e = decodePilotPersistedPlan(raw)
			require.Error(t, e)
		})
	}
	for name, raw := range map[string][]byte{
		"null": []byte(`null`), "unknown": []byte(strings.Replace(string(original), `"plan_version":`, `"unknown":0,"plan_version":`, 1)),
		"duplicate":     []byte(strings.Replace(string(original), `"plan_version":`, `"plan_version":1,"plan_version":`, 1)),
		"null_required": []byte(strings.Replace(string(original), `"plan_version":1`, `"plan_version":null`, 1)),
		"type":          []byte(strings.Replace(string(original), `"plan_version":1`, `"plan_version":"1"`, 1)),
		"invalid_utf8":  append(append([]byte(nil), original...), 0xff),
		"oversize":      []byte(strings.Repeat(" ", 65537)),
	} {
		t.Run(name, func(t *testing.T) { _, e := decodePilotPersistedPlan(raw); require.Error(t, e) })
	}
	for _, name := range []string{"nonce_upper", "nonce_short", "config_hash", "revisions_order", "negative_revision", "duplicate_ids", "invalid_config_range", "invalid_ladder", "invalid_goal"} {
		t.Run(name, func(t *testing.T) {
			var data pilotCreationPlanData
			require.NoError(t, json.Unmarshal(original, &data))
			switch name {
			case "nonce_upper":
				data.Nonce = "ABCDEFABCDEFABCDEFABCDEFABCDEFABCD"
			case "nonce_short":
				data.Nonce = "ab"
			case "config_hash":
				data.ConfigFingerprint = strings.Repeat("0", 64)
			case "revisions_order":
				data.ConfigRevisions[0], data.ConfigRevisions[1] = data.ConfigRevisions[1], data.ConfigRevisions[0]
			case "negative_revision":
				data.ConfigRevisions[0].Revision.Global = -1
			case "duplicate_ids":
				data.WordIDs = []int64{91, 91}
			case "invalid_config_range":
				data.Config.WordsPerLesson = 101
			case "invalid_ladder":
				data.Config.Ladder = []int{1}
			case "invalid_goal":
				data.Config.GoalMode = "unrecognized"
			}
			if strings.HasPrefix(name, "invalid_") {
				var e error
				data.ConfigFingerprint, e = pilotCanonicalHash(data.Config)
				require.NoError(t, e)
			}
			raw, e := json.Marshal(data)
			require.NoError(t, e)
			_, e = decodePilotPersistedPlan(raw)
			require.Error(t, e)
		})
	}
}

func TestPilotCreatedAuditUsesHistoricalFreezeNotCurrentAvailabilityDraft(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	_, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, e)
	require.NoError(t, db.Model(&User{}).Where("id=7").Update("status", "disabled").Error)
	require.NoError(t, db.Model(&WordBook{}).Where("id=9").Update("status", "draft").Error)
	cfg, e := config.NewStore(NewConfigPersister(db), nil)
	require.NoError(t, e)
	require.NoError(t, cfg.SetGlobal("test", "wordsPerLesson", 70, "current configuration changed"))
	require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { return auditPilotCreatedDataTx(tx) }))
	// Historical startup audit passing never grants current execution authority.
	receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.Error(t, e)
	require.Equal(t, PilotCreationReceipt{}, receipt)
}

func TestPilotCreatedAuditRejectsFingerprintAndNonceDamageDraft(t *testing.T) {
	for _, field := range []string{"creation_nonce", "creation_fingerprint"} {
		t.Run(field, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			replacement := strings.Repeat("a", 32)
			if field == "creation_fingerprint" {
				replacement = strings.Repeat("a", 64)
			}
			require.NoError(t, db.Table("classroom_mode_reservations").Where("lesson_id=?", receipt.LessonID).Update(field, replacement).Error)
			require.Error(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { return auditPilotCreatedDataTx(tx) }))
		})
	}
}

func TestPilotCreatedAuditRejectsActiveAndLegacyAttachedStatesDraft(t *testing.T) {
	for _, scenario := range []string{"active", "legacy_attached"} {
		t.Run(scenario, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			if scenario == "active" {
				require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", receipt.LessonID).Updates(map[string]any{
					"lifecycle": "active", "phase": "wait_answer", "logical_input_revision": 1, "question_key": "q1", "checkpoint_version": 2,
					"state_json": `{"schema_version":2,"lifecycle":"active","machine":{}}`,
				}).Error)
			} else {
				require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
					require.NoError(t, tx.Exec("INSERT INTO classroom_scope_reservations(student_id,book_id,mode,schema_version,reserved_at) VALUES(8,9,'legacy',2,0)").Error)
					var lesson Lesson
					require.NoError(t, tx.First(&lesson, receipt.LessonID).Error)
					lesson.ID = 0
					lesson.StudentID = 8
					require.NoError(t, tx.Create(&lesson).Error)
					require.NoError(t, tx.Exec("INSERT INTO classroom_mode_reservations(lesson_id,student_id,book_id,mode,schema_version,reserved_at) VALUES(?,8,9,'legacy',2,0)", lesson.ID).Error)
					var clone map[string]any
					require.NoError(t, tx.Table("classroom_pilot_states").Where("lesson_id=?", receipt.LessonID).Take(&clone).Error)
					clone["lesson_id"] = lesson.ID
					clone["student_id"] = int64(8)
					clone["mode"] = "legacy"
					require.NoError(t, tx.Table("classroom_pilot_states").Create(clone).Error)
					return nil
				}))
			}
			var fkRows []map[string]any
			require.NoError(t, db.Raw("PRAGMA foreign_key_check").Scan(&fkRows).Error)
			require.Empty(t, fkRows, "fixture must be FK-valid before application audit")
			require.Error(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { return auditPilotCreatedDataTx(tx) }))
		})
	}
}

func TestPilotCreatedAuditScopeBijectionDraft(t *testing.T) {
	for _, scenario := range []string{"orphan_pilot_scope", "two_pilot_lessons_one_scope", "scope_timestamp_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			first, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			switch scenario {
			case "orphan_pilot_scope":
				require.NoError(t, db.Exec("INSERT INTO classroom_scope_reservations(student_id,book_id,mode,schema_version,reserved_at) VALUES(8,9,'pilot',2,0)").Error)
			case "scope_timestamp_mismatch":
				require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
					require.NoError(t, tx.Exec("UPDATE classroom_mode_reservations SET reserved_at=reserved_at+1 WHERE lesson_id=?", first.LessonID).Error)
					require.NoError(t, tx.Exec("UPDATE classroom_pilot_states SET created_at=created_at+1,updated_at=updated_at+1 WHERE lesson_id=?", first.LessonID).Error)
					return nil
				}))
			case "two_pilot_lessons_one_scope":
				// Build another fully self-consistent creation record by direct fixture SQL;
				// the public creation method would correctly reject this occupied scope.
				other := plan.data
				other.Nonce = "abcdefabcdefabcdefabcdefabcdefab"
				if other.Nonce == plan.data.Nonce {
					other.Nonce = "12345678901234567890123456789012"
				}
				fingerprint, e := pilotCanonicalHash(other)
				require.NoError(t, e)
				encoded, e := json.Marshal(other)
				require.NoError(t, e)
				require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
					var lesson Lesson
					require.NoError(t, tx.First(&lesson, first.LessonID).Error)
					lesson.ID = 0
					require.NoError(t, tx.Create(&lesson).Error)
					var prior pilotModeReservation
					require.NoError(t, tx.Table("classroom_mode_reservations").Where("lesson_id=?", first.LessonID).Take(&prior).Error)
					prior.LessonID = lesson.ID
					prior.CreationNonce = &other.Nonce
					prior.CreationFingerprint = &fingerprint
					require.NoError(t, tx.Table("classroom_mode_reservations").Create(&prior).Error)
					var clone map[string]any
					require.NoError(t, tx.Table("classroom_pilot_states").Where("lesson_id=?", first.LessonID).Take(&clone).Error)
					clone["lesson_id"] = lesson.ID
					clone["creation_plan_json"] = string(encoded)
					require.NoError(t, tx.Table("classroom_pilot_states").Create(clone).Error)
					return nil
				}))
			}
			var fkRows []map[string]any
			require.NoError(t, db.Raw("PRAGMA foreign_key_check").Scan(&fkRows).Error)
			require.Empty(t, fkRows)
			require.Error(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { return auditPilotCreatedDataTx(tx) }))
		})
	}
}

func TestPilotCreatedAuditRejectsFKValidFutureFactsDraft(t *testing.T) {
	for _, table := range []string{"classroom_transition_receipts", "classroom_answer_claims", "classroom_active_commands", "classroom_effect_outbox", "classroom_completion_jobs"} {
		t.Run(table, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			hash := strings.Repeat("a", 64)
			require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
				// Claims and outbox depend on a transition receipt; insert that parent
				// first. Those cases prove rejection of future facts, not independent
				// branch coverage after the earlier receipt-table check.
				if table == "classroom_transition_receipts" || table == "classroom_answer_claims" || table == "classroom_effect_outbox" {
					require.NoError(t, tx.Table("classroom_transition_receipts").Create(map[string]any{
						"lesson_id": receipt.LessonID, "command_id": "cmd1", "student_id": 7,
						"request_fingerprint": hash, "kind": "answer.submit", "from_version": 1, "to_version": 2,
						"logical_input_revision": 1, "decision_id": "decision1", "result_json": "{}", "committed_at": 0,
					}).Error)
				}
				switch table {
				case "classroom_answer_claims":
					require.NoError(t, tx.Table(table).Create(map[string]any{
						"lesson_id": receipt.LessonID, "logical_input_revision": 1, "student_id": 7, "question_key": "q1",
						"command_id": "cmd1", "decision_id": "decision1", "answer_kind": "quiz", "result_fingerprint": hash,
					}).Error)
				case "classroom_active_commands":
					require.NoError(t, tx.Table(table).Create(map[string]any{
						"lesson_id": receipt.LessonID, "command_id": "cmd1", "student_id": 7, "request_fingerprint": hash,
						"kind": "answer.submit", "logical_input_revision": 1, "question_key": "q1", "auth_generation": 1,
						"capture_attempt_generation": 0, "status": "computing", "claim_token": "claim1", "worker_generation": 1,
						"compute_deadline": 8000, "claim_until": 8000, "decision_id": nil, "failure_code": nil, "created_at": 0, "updated_at": 0,
					}).Error)
				case "classroom_effect_outbox":
					require.NoError(t, tx.Table(table).Create(map[string]any{
						"decision_id": "decision1", "effect_index": 0, "lesson_id": receipt.LessonID, "student_id": 7,
						"schema_version": 2, "checkpoint_version": 2, "kind": "play_tts", "payload_json": "{}",
						"delivery_status": "pending", "attempt_count": 0, "next_attempt_at": 0, "claim_token": nil, "claim_until": nil,
						"output_auth_generation": nil, "segment_id": nil, "lease_id": nil, "created_at": 0,
					}).Error)
				case "classroom_completion_jobs":
					require.NoError(t, tx.Table(table).Create(map[string]any{
						"lesson_id": receipt.LessonID, "student_id": 7, "book_id": 9, "result_state_json": "{}", "frozen_plan_json": "{}",
						"plan_version": 1, "fingerprint": hash, "status": "pending", "claim_token": nil, "claim_until": nil,
						"attempt_count": 0, "next_attempt_at": 0, "last_error_code": nil, "completion_decision_id": nil, "created_at": 0, "updated_at": 0,
					}).Error)
				}
				return nil
			}), "fixture transaction must COMMIT; deferred FK failure is not audit evidence")
			var count int64
			require.NoError(t, db.Table(table).Count(&count).Error)
			require.EqualValues(t, 1, count)
			var fkRows []map[string]any
			require.NoError(t, db.Raw("PRAGMA foreign_key_check").Scan(&fkRows).Error)
			require.Empty(t, fkRows)
			require.Error(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { return auditPilotCreatedDataTx(tx) }))
		})
	}
}

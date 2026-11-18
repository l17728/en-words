package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type PilotEffect struct {
	Kind    string
	Payload json.RawMessage
}

type PilotTransition struct {
	Command    PilotCommand
	StateJSON  json.RawMessage
	FactsJSON  json.RawMessage
	ResultJSON json.RawMessage
	Effects    []PilotEffect
}

type PilotTransitionReceipt struct {
	DecisionID  string `json:"decision_id"`
	FromVersion int64  `json:"from_version"`
	ToVersion   int64  `json:"to_version"`
	Replayed    bool   `json:"replayed"`
}

func (p *PilotStore) CommitPilotTransition(ctx context.Context, tr PilotTransition) (PilotTransitionReceipt, error) {
	var out PilotTransitionReceipt
	if p == nil || p.db == nil || p.verifier == nil || len(tr.StateJSON) == 0 || len(tr.FactsJSON) == 0 || len(tr.ResultJSON) == 0 {
		return out, ErrPilotInvalidCommand
	}
	if json.Valid(tr.StateJSON) != true || json.Valid(tr.FactsJSON) != true || json.Valid(tr.ResultJSON) != true {
		return out, ErrPilotInvalidCommand
	}
	fingerprint, err := tr.Command.Fingerprint()
	if err != nil {
		return out, err
	}
	decisionHash := sha256.Sum256([]byte(fingerprint + ":decision"))
	decisionID := hex.EncodeToString(decisionHash[:])
	now := time.Now().UnixMilli()
	err = atomicConnection(ctx, p.db, func(tx *gorm.DB) error {
		var state struct {
			StudentID            int64
			LogicalInputRevision int64
			CheckpointVersion    int64
			QuestionKey          string
			Mode                 string
			Lifecycle            string
		}
		if err := tx.Table("classroom_pilot_states").Where("lesson_id = ?", tr.Command.LessonID).Take(&state).Error; err != nil {
			return err
		}
		if state.StudentID != tr.Command.StudentID || state.Mode != "pilot" || state.Lifecycle == "retired" {
			return ErrPilotInvalidCommand
		}
		var existing struct {
			DecisionID         string
			RequestFingerprint string
			FromVersion        int64
			ToVersion          int64
		}
		if e := tx.Table("classroom_transition_receipts").Where("lesson_id=? AND command_id=?", tr.Command.LessonID, tr.Command.CommandID).Take(&existing).Error; e == nil {
			if existing.RequestFingerprint != fingerprint {
				return ErrPilotInvalidCommand
			}
			out = PilotTransitionReceipt{DecisionID: existing.DecisionID, FromVersion: existing.FromVersion, ToVersion: existing.ToVersion, Replayed: true}
			return nil
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		expectedQuestion := state.QuestionKey
		if expectedQuestion == "" {
			expectedQuestion = "pending"
		}
		if err := tr.Command.Validate(state.LogicalInputRevision+1, expectedQuestion); err != nil {
			return err
		}
		from, to := state.CheckpointVersion, state.CheckpointVersion+1
		if err := tx.Exec(`INSERT INTO classroom_transition_receipts(lesson_id,command_id,student_id,request_fingerprint,kind,from_version,to_version,logical_input_revision,decision_id,result_json,committed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, tr.Command.LessonID, tr.Command.CommandID, tr.Command.StudentID, fingerprint, tr.Command.Kind, from, to, tr.Command.LogicalInputRevision, decisionID, string(tr.ResultJSON), now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE classroom_pilot_states SET lifecycle='active', phase=?, question_key=?, checkpoint_version=?, logical_input_revision=?, state_json=?, room_facts_json=?, updated_at=? WHERE lesson_id=? AND checkpoint_version=?`, "show_word", tr.Command.QuestionKey, to, tr.Command.LogicalInputRevision, string(tr.StateJSON), string(tr.FactsJSON), now, tr.Command.LessonID, from).Error; err != nil {
			return err
		}
		for i, effect := range tr.Effects {
			if strings.TrimSpace(effect.Kind) == "" || !json.Valid(effect.Payload) {
				return fmt.Errorf("%w: invalid effect", ErrPilotInvalidCommand)
			}
			if err := tx.Exec(`INSERT INTO classroom_effect_outbox(decision_id,effect_index,lesson_id,student_id,schema_version,checkpoint_version,kind,payload_json,delivery_status,attempt_count,next_attempt_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, decisionID, i, tr.Command.LessonID, tr.Command.StudentID, 2, to, effect.Kind, string(effect.Payload), "pending", 0, now, now).Error; err != nil {
				return err
			}
		}
		out = PilotTransitionReceipt{DecisionID: decisionID, FromVersion: from, ToVersion: to}
		return nil
	})
	return out, err
}

// Experimental isolated persistence component; no application or HTTP entry point.
package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// PilotCreatedState contains only the metadata needed to identify a durable
// created checkpoint. It is not a decision or permission to enter a Room.
type PilotCreatedState struct {
	LessonID          int64  `json:"lesson_id"`
	StudentID         int64  `json:"student_id"`
	BookID            int64  `json:"book_id"`
	SchemaVersion     int64  `json:"schema_version"`
	Lifecycle         string `json:"lifecycle"`
	Phase             string `json:"phase"`
	CheckpointVersion int64  `json:"checkpoint_version"`
}

// LoadCreatedState is for a trusted, already-authorized system adapter only.
// BindPilotSystemPrincipal does not itself authenticate a caller. All reads
// share one SQLite snapshot; this method never reserves a scope or starts Room.
func (r *PilotStore) LoadCreatedState(ctx context.Context, principal PilotSystemPrincipal, lessonID int64) (PilotCreatedState, error) {
	var result PilotCreatedState
	if r == nil || r.db == nil || r.verifier == nil || principal.studentID <= 0 || principal.taskID == "" || lessonID <= 0 {
		return result, errPilotCreationDenied
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		shape, err := r.verifier.inspectCurrentTx(tx)
		if err != nil {
			return err
		}
		if shape != PilotSchemaReady {
			return errPilotNotReady
		}
		var student User
		err = tx.Select("id,role,status").First(&student, principal.studentID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errPilotCreationDenied
		}
		if err != nil {
			return err
		}
		if student.Role != "student" || student.Status != "active" {
			return errPilotCreationDenied
		}
		var reservation pilotModeReservation
		err = tx.Table("classroom_mode_reservations").Where("lesson_id=? AND student_id=?", lessonID, principal.studentID).Take(&reservation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errPilotCreationDenied
		}
		if err != nil {
			return err
		}
		if reservation.Mode != "pilot" || reservation.SchemaVersion != 2 || reservation.CreationVersion == nil || *reservation.CreationVersion != 1 || reservation.CreationNonce == nil || reservation.CreationFingerprint == nil {
			return errPilotSchemaInvalid
		}
		var row pilotCreatedCheckpointRow
		if err = tx.Table("classroom_pilot_states").Where("lesson_id=? AND student_id=?", lessonID, principal.studentID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errPilotSchemaInvalid
			}
			return err
		}
		plan, err := decodePilotPersistedPlan([]byte(row.CreationPlanJSON))
		if err != nil {
			return err
		}
		if plan.data.StudentID != principal.studentID || plan.data.BookID != reservation.BookID || plan.data.Nonce != *reservation.CreationNonce || plan.fingerprint != *reservation.CreationFingerprint {
			return errPilotSchemaInvalid
		}
		if err = verifyPilotCreatedReplayTx(tx, plan, reservation); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errPilotSchemaInvalid
			}
			return err
		}
		for _, table := range []string{"classroom_transition_receipts", "classroom_answer_claims", "classroom_active_commands", "classroom_effect_outbox", "classroom_completion_jobs"} {
			var present bool
			if err = tx.Raw("SELECT EXISTS(SELECT 1 FROM "+pilotQuote(table)+" WHERE lesson_id=? LIMIT 1)", lessonID).Scan(&present).Error; err != nil {
				return err
			}
			if present {
				return errPilotSchemaInvalid
			}
		}
		result = PilotCreatedState{LessonID: row.LessonID, StudentID: row.StudentID, BookID: row.BookID,
			SchemaVersion: row.SchemaVersion, Lifecycle: row.Lifecycle, Phase: row.Phase, CheckpointVersion: row.CheckpointVersion}
		return nil
	})
	if err != nil {
		return PilotCreatedState{}, err
	}
	return result, nil
}

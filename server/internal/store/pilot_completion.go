package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// PilotCompletionJob is the durable hand-off from the classroom decision path
// to the existing lesson settlement worker.
type PilotCompletionJob struct {
	LessonID             int64
	StudentID            int64
	BookID               int64
	Status               string
	ClaimToken           string
	AttemptCount         int64
	NextAttemptAt        int64
	CompletionDecisionID string
}

func (p *PilotStore) EnqueuePilotCompletion(ctx context.Context, lessonID, studentID, bookID int64, resultState, frozenPlan json.RawMessage, planVersion int64, fingerprint string, nowMS int64) error {
	if p == nil || p.db == nil || lessonID <= 0 || studentID <= 0 || bookID <= 0 || planVersion <= 0 || nowMS < 0 || len(fingerprint) != 64 || !json.Valid(resultState) || !json.Valid(frozenPlan) {
		return ErrPilotInvalidCommand
	}
	return atomicConnection(ctx, p.db, func(tx *gorm.DB) error {
		var existing struct{ Fingerprint string }
		err := tx.Table("classroom_completion_jobs").Where("lesson_id=?", lessonID).Take(&existing).Error
		if err == nil {
			if existing.Fingerprint != fingerprint {
				return ErrPilotInvalidCommand
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Exec(`INSERT INTO classroom_completion_jobs(lesson_id,student_id,book_id,result_state_json,frozen_plan_json,plan_version,fingerprint,status,attempt_count,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, lessonID, studentID, bookID, string(resultState), string(frozenPlan), planVersion, strings.ToLower(fingerprint), "pending", 0, nowMS, nowMS, nowMS).Error
	})
}

func (p *PilotStore) ClaimPilotCompletion(ctx context.Context, lessonID int64, claimToken string, nowMS, leaseMS int64) (PilotCompletionJob, bool, error) {
	var job PilotCompletionJob
	if p == nil || p.db == nil || lessonID <= 0 || strings.TrimSpace(claimToken) == "" || nowMS < 0 || leaseMS <= 0 {
		return job, false, ErrPilotInvalidCommand
	}
	err := atomicConnection(ctx, p.db, func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE classroom_completion_jobs SET status='running',claim_token=?,claim_until=?,attempt_count=attempt_count+1,updated_at=? WHERE lesson_id=? AND status IN ('pending','running') AND (status='pending' OR claim_until IS NULL OR claim_until<=?)`, claimToken, nowMS+leaseMS, nowMS, lessonID, nowMS)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Table("classroom_completion_jobs").Where("lesson_id=?", lessonID).Take(&job).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return job, false, nil
	}
	if err != nil {
		return job, false, err
	}
	return job, true, nil
}

func (p *PilotStore) CompletePilotCompletion(ctx context.Context, lessonID int64, claimToken, decisionID string, nowMS int64) error {
	if p == nil || p.db == nil || lessonID <= 0 || strings.TrimSpace(claimToken) == "" || strings.TrimSpace(decisionID) == "" || nowMS < 0 {
		return ErrPilotInvalidCommand
	}
	return atomicConnection(ctx, p.db, func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE classroom_completion_jobs SET status='done',claim_token=NULL,claim_until=NULL,completion_decision_id=?,updated_at=? WHERE lesson_id=? AND status='running' AND claim_token=?`, decisionID, nowMS, lessonID, claimToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrPilotInvalidCommand
		}
		return nil
	})
}

func pilotCompletionNow() int64 { return time.Now().UnixMilli() }

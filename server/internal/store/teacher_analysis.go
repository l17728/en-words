package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

type TeacherFailureAnalysis struct {
	ID                   int64      `gorm:"primaryKey" json:"id"`
	LessonID             int64      `gorm:"index" json:"lesson_id"`
	EntryID              int64      `gorm:"index" json:"entry_id"`
	StudentID            int64      `gorm:"index" json:"student_id"`
	FailureType          string     `gorm:"index" json:"failure_type"`
	TargetText           string     `json:"target_text"`
	Transcript           string     `json:"transcript,omitempty"`
	Diagnosis            string     `json:"diagnosis,omitempty"`
	Suggestion           string     `json:"suggestion,omitempty"`
	Confidence           float64    `json:"confidence,omitempty"`
	AnalysisStatus       string     `gorm:"index" json:"status"`
	ImplementationStatus string     `gorm:"index" json:"implementation_status"`
	AnalysisKey          string     `gorm:"uniqueIndex" json:"analysis_key"`
	CreatedAt            time.Time  `json:"created_at"`
	ResolvedAt           *time.Time `json:"resolved_at,omitempty"`
}

type TeacherAnalysisJob struct {
	ID          string `gorm:"primaryKey"`
	AnalysisID  int64  `gorm:"index"`
	AnalysisKey string `gorm:"uniqueIndex"`
	Status      string `gorm:"index:idx_teacher_job_status_next,priority:1"`
	LeaseOwner  string
	LeaseUntil  *time.Time `gorm:"index"`
	Attempts    int
	NextRunAt   time.Time `gorm:"index:idx_teacher_job_status_next,priority:2"`
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TeacherAuthorizationReceipt struct {
	ID         string `gorm:"primaryKey"`
	AnalysisID int64  `gorm:"index:idx_teacher_receipt_action,priority:1"`
	OperatorID int64  `gorm:"index"`
	ToolName   string `gorm:"index:idx_teacher_receipt_action,priority:2"`
	ArgsSHA256 string `gorm:"index:idx_teacher_receipt_action,priority:3"`
	Nonce      string `gorm:"uniqueIndex"`
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

type WordPronunciationVariant struct {
	ID         int64  `gorm:"primaryKey"`
	EntryID    int64  `gorm:"index:idx_entry_variant,unique"`
	Variant    string `gorm:"index:idx_entry_variant,unique"`
	Kind       string
	Source     string
	Confidence float64
	Enabled    bool
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

var ErrTeacherJobUnavailable = errors.New("teacher analysis job unavailable")

func TeacherAnalysisModels() []any {
	return []any{&TeacherFailureAnalysis{}, &TeacherAnalysisJob{}, &TeacherAuthorizationReceipt{}, &WordPronunciationVariant{}}
}

type TeacherAnalysisRepo struct{ db *gorm.DB }

func NewTeacherAnalysisRepo(db *gorm.DB) *TeacherAnalysisRepo { return &TeacherAnalysisRepo{db: db} }

func (r *TeacherAnalysisRepo) CreateAnalysis(a *TeacherFailureAnalysis) error {
	return r.db.Create(a).Error
}

func (r *TeacherAnalysisRepo) GetAnalysis(id int64) (*TeacherFailureAnalysis, error) {
	var a TeacherFailureAnalysis
	if err := r.db.First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *TeacherAnalysisRepo) CreateJob(j *TeacherAnalysisJob) error { return r.db.Create(j).Error }

func (r *TeacherAnalysisRepo) ListAnalyses(studentID, lessonID int64) ([]TeacherFailureAnalysis, error) {
	q := r.db.Order("created_at DESC")
	if studentID != 0 {
		q = q.Where("student_id = ?", studentID)
	}
	if lessonID != 0 {
		q = q.Where("lesson_id = ?", lessonID)
	}
	var rows []TeacherFailureAnalysis
	return rows, q.Find(&rows).Error
}

func (r *TeacherAnalysisRepo) AddVariant(v *WordPronunciationVariant) error {
	if v.Version == 0 {
		v.Version = 1
	}
	return r.db.Where("entry_id = ? AND variant = ?", v.EntryID, v.Variant).FirstOrCreate(v).Error
}

func (r *TeacherAnalysisRepo) ListVariants(entryID int64) ([]WordPronunciationVariant, error) {
	var rows []WordPronunciationVariant
	return rows, r.db.Where("entry_id = ? AND enabled = ?", entryID, true).Order("version DESC, id ASC").Find(&rows).Error
}

func (r *TeacherAnalysisRepo) ClaimJob(now time.Time, owner string) (*TeacherAnalysisJob, error) {
	var j TeacherAnalysisJob
	err := r.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Where("status = ? AND next_run_at <= ?", "queued", now).
			Order("next_run_at ASC").First(&j)
		if errors.Is(q.Error, gorm.ErrRecordNotFound) {
			return ErrTeacherJobUnavailable
		}
		if q.Error != nil {
			return q.Error
		}
		until := now.Add(2 * time.Minute)
		res := tx.Model(&TeacherAnalysisJob{}).Where("id = ? AND status = ?", j.ID, "queued").Updates(map[string]any{"status": "running", "lease_owner": owner, "lease_until": until, "attempts": gorm.Expr("attempts + 1"), "updated_at": now})
		if res.RowsAffected != 1 {
			return ErrTeacherJobUnavailable
		}
		j.Status, j.LeaseOwner, j.LeaseUntil = "running", owner, &until
		j.Attempts++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *TeacherAnalysisRepo) ConsumeReceipt(tx *gorm.DB, id string, now time.Time) error {
	res := tx.Model(&TeacherAuthorizationReceipt{}).Where("id = ? AND consumed_at IS NULL AND expires_at > ?", id, now).Updates(map[string]any{"consumed_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrTeacherJobUnavailable
	}
	return nil
}

func (r *TeacherAnalysisRepo) CompleteJob(id, owner string, now time.Time) error {
	res := r.db.Model(&TeacherAnalysisJob{}).Where("id = ? AND status = ? AND lease_owner = ?", id, "running", owner).Updates(map[string]any{"status": "succeeded", "lease_owner": "", "lease_until": nil, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrTeacherJobUnavailable
	}
	return nil
}

func (r *TeacherAnalysisRepo) FailJob(id, owner, reason string, now, next time.Time, terminal bool) error {
	status := "queued"
	if terminal {
		status = "failed"
	}
	res := r.db.Model(&TeacherAnalysisJob{}).Where("id = ? AND status = ? AND lease_owner = ?", id, "running", owner).Updates(map[string]any{"status": status, "lease_owner": "", "lease_until": nil, "last_error": reason, "next_run_at": next, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrTeacherJobUnavailable
	}
	return nil
}

func (r *TeacherAnalysisRepo) RequeueExpired(now time.Time) (int64, error) {
	res := r.db.Model(&TeacherAnalysisJob{}).Where("status = ? AND lease_until IS NOT NULL AND lease_until <= ?", "running", now).Updates(map[string]any{"status": "queued", "lease_owner": "", "lease_until": nil, "next_run_at": now, "updated_at": now})
	return res.RowsAffected, res.Error
}

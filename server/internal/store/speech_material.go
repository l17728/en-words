package store

import "time"

// SpeechMaterialJob tracks durable pre-generation work. A unique key makes
// concurrent requests idempotent across processes; lease fields make stale
// workers recoverable after a restart.
type SpeechMaterialJob struct {
	ID               int64  `gorm:"primaryKey"`
	CacheKey         string `gorm:"uniqueIndex:idx_speech_job_key_revision"`
	ManifestRevision string `gorm:"uniqueIndex:idx_speech_job_key_revision"`
	LessonID         int64  `gorm:"index"`
	Language         string `gorm:"index:idx_speech_job_capability"`
	SpeechText       string
	ServiceID        string `gorm:"index:idx_speech_job_capability"`
	Model            string `gorm:"index:idx_speech_job_capability"`
	Voice            string
	AgentID          string `gorm:"index"`
	Status           string `gorm:"index"`
	Attempt          int
	AttemptToken     string
	LeaseUntil       *time.Time
	ErrorCode        string
	ErrorSummary     string
	TraceID          string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

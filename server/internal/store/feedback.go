package store

import (
	"enwords/internal/config"
	"fmt"
	"gorm.io/gorm"
	"time"
)

type FeedbackTask struct {
	ID                                                           int64  `gorm:"primaryKey"`
	StudentID                                                    int64  `gorm:"uniqueIndex:idx_feedback_submission"`
	SubmissionID                                                 string `gorm:"uniqueIndex:idx_feedback_submission"`
	TaskID                                                       string `gorm:"uniqueIndex"`
	SchemaVersion                                                int
	InputText, InputHash, Channel, Idempotency                   string
	PlanJSON                                                     string
	LegacyJSON                                                   string
	ErrorCode                                                    string
	Retryable                                                    bool
	ParentSubmissionID, ConversationID, ClarificationChildTaskID string
	Version                                                      int64
	CreatedAt, UpdatedAt                                         time.Time
}
type FeedbackIntent struct {
	TaskID                               string `gorm:"primaryKey"`
	IntentIndex                          int    `gorm:"primaryKey;autoIncrement:false"`
	Kind, FrozenJSON, ResultJSON, Status string
	AttemptCount                         int
	CompletedAt                          *time.Time
}
type FeedbackTodo struct {
	ID          int64  `gorm:"primaryKey"`
	TaskID      string `gorm:"uniqueIndex:idx_feedback_todo"`
	IntentIndex int    `gorm:"uniqueIndex:idx_feedback_todo"`
	StudentID   int64
	Status      string
	Legacy      bool
	CreatedAt   time.Time
}
type FeedbackNotice struct {
	ID                   int64  `gorm:"primaryKey"`
	TaskID               string `gorm:"uniqueIndex:idx_feedback_notice"`
	IntentIndex          int    `gorm:"uniqueIndex:idx_feedback_notice"`
	Recipient            int64  `gorm:"uniqueIndex:idx_feedback_notice"`
	Kind                 string `gorm:"uniqueIndex:idx_feedback_notice"`
	Title, Body          string
	Read                 bool
	CreatedAt, UpdatedAt time.Time
}

// Separate tombstone revisions survive override deletion and detect ABA writes.
type ConfigRevision struct {
	StudentID int64  `gorm:"primaryKey;autoIncrement:false"`
	Key       string `gorm:"primaryKey"`
	Revision  int64
}

func installConfigRevisionTriggers(db *gorm.DB) error {
	for _, table := range []string{"config_globals", "config_overrides"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			row := "NEW"
			if op == "DELETE" {
				row = "OLD"
			}
			sid := "0"
			if table == "config_overrides" {
				sid = row + ".student_id"
			}
			sql := fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS rev_%s_%s AFTER %s ON %s BEGIN INSERT INTO config_revisions(student_id,key,revision) VALUES(%s,%s.key,1) ON CONFLICT(student_id,key) DO UPDATE SET revision=revision+1; END", table, op, op, table, sid, row)
			if err := db.Exec(sql).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
func (p *ConfigPersister) Revisions(sid int64, key string) (config.Revision, error) {
	var rows []ConfigRevision
	if err := p.db.Where("key=? AND student_id IN ?", key, []int64{0, sid}).Find(&rows).Error; err != nil {
		return config.Revision{}, err
	}
	var out config.Revision
	for _, r := range rows {
		if r.StudentID == 0 {
			out.Global = r.Revision
		} else {
			out.Student = r.Revision
		}
	}
	return out, nil
}

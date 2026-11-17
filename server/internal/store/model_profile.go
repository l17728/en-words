package store

import "time"

// ModelProfile is the persisted assignment of model-console services to
// product roles. Keeping this in SQLite makes the active selection survive a
// restart and gives administrators an auditable, named configuration.
type ModelProfile struct {
	ID           int64     `gorm:"primaryKey" json:"id"`
	Name         string    `gorm:"uniqueIndex" json:"name"`
	TextService  string    `json:"text_service"`
	ASRService   string    `json:"asr_service"`
	TTSZhService string    `json:"tts_zh_service"`
	TTSEnService string    `json:"tts_en_service"`
	Active       bool      `gorm:"index" json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

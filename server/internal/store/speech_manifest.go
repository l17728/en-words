package store

import "time"

// SpeechMaterialManifest fences all in-flight work to the currently selected
// lesson/profile material set.
type SpeechMaterialManifest struct {
	LessonID   int64     `gorm:"primaryKey"`
	Revision   string    `gorm:"index"`
	UpdatedAt  time.Time
}

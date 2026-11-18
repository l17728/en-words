package store

import "time"

type SpeechAsset struct {
	ID          int64  `gorm:"primaryKey"`
	CacheKey    string `gorm:"uniqueIndex"`
	Language    string `gorm:"index"`
	SpeechText  string
	Provider    string
	Model       string
	Voice       string
	RuleVersion string
	FilePath    string
	SHA256      string
	SizeBytes   int64
	Status      string `gorm:"index"`
	CreatedAt   time.Time
	LastUsedAt  time.Time
}

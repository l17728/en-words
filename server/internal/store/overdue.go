package store

import "time"

type OverdueTask struct {
	TaskID          string `gorm:"primaryKey"`
	LessonID        int64
	StudentID       int64
	BookID          int64
	OriginalOpenAt  time.Time
	PlanJSON        string
	PlanHistoryJSON string
	Version         int
	Status          string
	NewLessonID     int64
	CreatedAt       time.Time
	PlannedAt       time.Time
	CompletedAt     *time.Time
}

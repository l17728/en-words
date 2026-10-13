package api

import (
	"encoding/json"
	"enwords/internal/store"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Caller serializes writes with a.mu. Failure is observable and never logged as saved.
func (a *App) saveRuntimeLocked(key string, value any) bool {
	if a.DB == nil {
		return true
	}
	raw, err := json.Marshal(value)
	if err == nil {
		err = a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&store.RuntimeRecord{Key: key, Value: string(raw)}).Error
	}
	if err != nil {
		a.Log.Error("store", "snapshot_failed", map[string]any{"key": key, "error": err.Error()})
		return false
	}
	a.Log.Info("store", "snapshot_saved", map[string]any{"key": key})
	return true
}
func (a *App) readRuntime(key string, out any) bool {
	err := a.readRuntimeError(key, out)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	if err != nil {
		a.Log.Error("store", "snapshot_failed", map[string]any{"key": key, "error": err.Error()})
		return false
	}
	return true
}
func (a *App) readRuntimeError(key string, out any) error {
	if a.DB == nil {
		return gorm.ErrRecordNotFound
	}
	var row store.RuntimeRecord
	err := a.DB.Where("key=?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		err = json.Unmarshal([]byte(row.Value), out)
	}
	return err
}
func (a *App) restoreRuntime() {
	if a.DB == nil {
		return
	}
	for key, out := range map[string]any{"notifications": &a.notifications, "feedbacks": &a.feedbackRecs} {
		if err := a.readRuntimeError(key, out); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			a.StartupError = err
			return
		}
	}
	for _, n := range a.notifications {
		if n.ID > a.nextNotifID {
			a.nextNotifID = n.ID
		}
	}
	for _, f := range a.feedbackRecs {
		if f.ID > a.nextFeedbackID {
			a.nextFeedbackID = f.ID
		}
	}
}

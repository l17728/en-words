package api

import (
	"context"
	"encoding/json"
	"enwords/internal/obs"
	"enwords/internal/store"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
)

// Recover the classroom correlation only for canonical, trusted resource keys.
func runtimeTraceContext(ctx context.Context, key string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if obs.TraceID(ctx) != "" {
		return ctx
	}
	prefix, suffix, ok := strings.Cut(key, "/")
	if ok {
		switch prefix {
		case "snapshot", "report", "settled", "agent_result":
			id, err := strconv.ParseInt(suffix, 10, 64)
			if err == nil && id > 0 && strconv.FormatInt(id, 10) == suffix {
				return obs.WithTrace(ctx, "lesson-"+suffix)
			}
		}
	}
	return obs.WithChild(ctx, "runtime")
}

// Caller serializes writes with a.mu. Failure is observable and never logged as saved.
func (a *App) saveRuntimeLocked(key string, value any) bool {
	return a.saveRuntimeContextLocked(context.Background(), key, value)
}
func (a *App) saveRuntimeContextLocked(ctx context.Context, key string, value any) bool {
	if a.DB == nil {
		return true
	}
	ctx = runtimeTraceContext(ctx, key)
	raw, err := json.Marshal(value)
	if err == nil {
		err = a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&store.RuntimeRecord{Key: key, Value: string(raw)}).Error
	}
	if err != nil {
		a.Log.ErrorCtx(ctx, "store", "snapshot_failed", map[string]any{"key": key, "reason": "persistence_failed"})
		return false
	}
	a.Log.InfoCtx(ctx, "store", "snapshot_saved", map[string]any{"key": key})
	return true
}
func (a *App) readRuntime(key string, out any) bool {
	return a.readRuntimeError(key, out) == nil
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
	if err != nil {
		a.Log.ErrorCtx(runtimeTraceContext(nil, key), "store", "snapshot_failed", map[string]any{"key": key, "reason": "read_failed"})
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

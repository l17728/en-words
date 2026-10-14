package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/store"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
)

func (a *App) migrateLegacyFeedback() error {
	for _, old := range a.feedbackRecs {
		b, _ := json.Marshal(old)
		hash := sha256.Sum256([]byte(old.Text))
		t := store.FeedbackTask{ID: old.ID, StudentID: old.StudentID, SubmissionID: fmt.Sprintf("legacy_%d", old.ID), TaskID: fmt.Sprintf("feedback_legacy_%d_%d", old.StudentID, old.ID), SchemaVersion: 1, InputText: old.Text, InputHash: hex.EncodeToString(hash[:]), Idempotency: "legacy", PlanJSON: "[]", LegacyJSON: string(b), Version: 1, CreatedAt: old.CreatedAt, UpdatedAt: old.CreatedAt}
		todo := store.FeedbackTodo{TaskID: t.TaskID, IntentIndex: 0, StudentID: old.StudentID, Status: "open", Legacy: true, CreatedAt: old.CreatedAt}
		if a.DB != nil {
			t.ID = 0
			if err := a.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&t).Error; err != nil {
					return err
				}
				if err := tx.Where("task_id=?", t.TaskID).First(&t).Error; err != nil {
					return err
				}
				if old.Status == "escalated" {
					return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&todo).Error
				}
				return nil
			}); err != nil {
				return err
			}
		} else {
			m := a.feedbackMemoryLocked()
			m.Tasks[t.TaskID] = t
			if old.Status == "escalated" {
				m.NextTodo++
				todo.ID = m.NextTodo
				m.Todos = append(m.Todos, todo)
			}
		}
	}
	return nil
}
func (a *App) FeedbacksContext(ctx context.Context, sid int64) ([]FeedbackRecord, error) {
	if err := a.feedbackActive(ctx, sid, a.DB); err != nil {
		return nil, err
	}
	out := []FeedbackRecord{}
	if a.DB != nil {
		err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var ts []store.FeedbackTask
			if err := tx.Where("student_id=?", sid).Order("id").Find(&ts).Error; err != nil {
				return err
			}
			for _, t := range ts {
				var its []store.FeedbackIntent
				if err := tx.Where("task_id=?", t.TaskID).Order("intent_index").Find(&its).Error; err != nil {
					return err
				}
				out = append(out, *feedbackRecord(t, its))
			}
			return nil
		})
		return out, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.feedbackMemoryLocked()
	for _, t := range m.Tasks {
		if t.StudentID == sid {
			out = append(out, *feedbackRecord(t, m.Intents[t.TaskID]))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (a *App) FeedbackAdminTodosContext(ctx context.Context) ([]FeedbackAdminTodo, error) {
	out := []FeedbackAdminTodo{}
	project := func(t store.FeedbackTodo, task store.FeedbackTask, its []store.FeedbackIntent) {
		r := feedbackRecord(task, its)
		out = append(out, FeedbackAdminTodo{ID: t.ID, TaskID: t.TaskID, IntentIndex: t.IntentIndex, StudentID: t.StudentID, Text: task.InputText, Status: t.Status, Reply: r.Reply, CreatedAt: t.CreatedAt, Legacy: t.Legacy})
	}
	if a.DB != nil {
		err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var todos []store.FeedbackTodo
			if err := tx.Order("id").Find(&todos).Error; err != nil {
				return err
			}
			for _, t := range todos {
				var task store.FeedbackTask
				if err := tx.Where("task_id=?", t.TaskID).First(&task).Error; err != nil {
					return err
				}
				var its []store.FeedbackIntent
				if err := tx.Where("task_id=?", t.TaskID).Order("intent_index").Find(&its).Error; err != nil {
					return err
				}
				project(t, task, its)
			}
			return nil
		})
		return out, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.feedbackMemoryLocked()
	for _, t := range m.Todos {
		project(t, m.Tasks[t.TaskID], m.Intents[t.TaskID])
	}
	return out, nil
}
func (a *App) FeedbackAdminTodoContext(ctx context.Context, id int64) (*FeedbackAdminTodo, error) {
	out, err := a.FeedbackAdminTodosContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range out {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, feedbackErr(404, "todo_not_found", false)
}
func feedbackNotification(n store.FeedbackNotice) Notification {
	return Notification{ID: -n.ID, UserID: n.Recipient, Type: n.Kind, Title: n.Title, Body: n.Body, Read: n.Read, CreatedAt: n.CreatedAt}
}
func (a *App) NotificationsContext(ctx context.Context, sid int64) ([]Notification, error) {
	out := []Notification{}
	if a.DB != nil {
		var old []Notification
		var row store.RuntimeRecord
		err := a.DB.WithContext(ctx).Where("key=?", "notifications").First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err == nil {
			if err = json.Unmarshal([]byte(row.Value), &old); err != nil {
				return nil, err
			}
		}
		for _, n := range old {
			if n.UserID == sid {
				out = append(out, n)
			}
		}
		var notices []store.FeedbackNotice
		if err := a.DB.WithContext(ctx).Where("recipient=?", sid).Order("id").Find(&notices).Error; err != nil {
			return nil, err
		}
		for _, n := range notices {
			out = append(out, feedbackNotification(n))
		}
		return out, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range a.notifications {
		if n.UserID == sid {
			out = append(out, n)
		}
	}
	for _, n := range a.feedbackMemoryLocked().Notices {
		if n.Recipient == sid {
			out = append(out, feedbackNotification(n))
		}
	}
	return out, nil
}
func (a *App) MarkNotificationReadContext(ctx context.Context, sid, id int64) error {
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.DB != nil {
		var old []Notification
		err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&store.FeedbackNotice{}).Where("recipient=?", sid)
			if id < 0 {
				q = q.Where("id=?", -id)
			}
			if id <= 0 {
				if err := q.Update("read", true).Error; err != nil {
					return err
				}
			}
			var row store.RuntimeRecord
			err := tx.Where("key=?", "notifications").First(&row).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				if err = json.Unmarshal([]byte(row.Value), &old); err != nil {
					return err
				}
				for i := range old {
					if old[i].UserID == sid && (id == 0 || old[i].ID == id) {
						old[i].Read = true
					}
				}
				b, _ := json.Marshal(old)
				row.Value = string(b)
				return tx.Save(&row).Error
			}
			return nil
		})
		if err == nil && old != nil {
			a.notifications = old
		}
		return err
	}
	for i := range a.notifications {
		if a.notifications[i].UserID == sid && (id == 0 || a.notifications[i].ID == id) {
			a.notifications[i].Read = true
		}
	}
	m := a.feedbackMemoryLocked()
	for i := range m.Notices {
		if m.Notices[i].Recipient == sid && (id == 0 || -m.Notices[i].ID == id) {
			m.Notices[i].Read = true
		}
	}
	return nil
}

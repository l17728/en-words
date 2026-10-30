package api

import (
	"context"
	"encoding/json"
	"enwords/internal/auth"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/wordbook"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type feedbackMemoryState struct {
	Tasks                        map[string]store.FeedbackTask
	Intents                      map[string][]store.FeedbackIntent
	Todos                        []store.FeedbackTodo
	Notices                      []store.FeedbackNotice
	NextID, NextTodo, NextNotice int64
}

func newFeedbackMemory() *feedbackMemoryState {
	return &feedbackMemoryState{Tasks: map[string]store.FeedbackTask{}, Intents: map[string][]store.FeedbackIntent{}}
}
func (a *App) feedbackMemoryLocked() *feedbackMemoryState {
	if a.feedbackState == nil {
		a.feedbackState = newFeedbackMemory()
	}
	return a.feedbackState
}

type feedbackWork struct {
	app     *App
	db      *gorm.DB
	mem     *feedbackMemoryState
	cfg     *config.Store
	sched   *scheduler.Service
	memory  *memory.Service
	task    store.FeedbackTask
	intents []store.FeedbackIntent
}

func feedbackErr(code int, name string, retry bool) error {
	return &FeedbackSubmissionError{Code: code, ErrorCode: name, Retryable: retry}
}
func feedbackErrorCode(err error) string {
	var e *FeedbackSubmissionError
	if errors.As(err, &e) {
		return e.ErrorCode
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if feedbackResourceMissing(err) {
		return "resource_unavailable"
	}
	return "storage_unavailable"
}
func feedbackResourceMissing(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, scheduler.ErrCardNotFound) || errors.Is(err, scheduler.ErrLessonNotFound) || errors.Is(err, wordbook.ErrBookNotFound) || errors.Is(err, wordbook.ErrEntryNotFound)
}
func feedbackRetryable(err error) bool {
	var e *FeedbackSubmissionError
	if errors.As(err, &e) {
		return e.Retryable
	}
	return !feedbackResourceMissing(err)
}
func (a *App) feedbackActive(ctx context.Context, sid int64, db *gorm.DB) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u, err := a.Auth.GetUserByID(sid)
	if err != nil || u.Role != auth.RoleStudent || u.Status != "active" {
		return feedbackErr(403, "student_unavailable", false)
	}
	if db != nil {
		var row store.User
		err := db.First(&row, sid).Error
		if err == nil {
			if row.Role != string(auth.RoleStudent) || row.Status != "active" {
				return feedbackErr(403, "student_unavailable", false)
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			return feedbackErr(403, "student_unavailable", false)
		} else {
			return err
		}
	}
	return nil
}
func (a *App) withFeedbackWork(ctx context.Context, taskID string, fn func(*feedbackWork) error) error {
	_, err := a.withFeedbackWorkOutcome(ctx, taskID, fn)
	return err
}

func (a *App) withFeedbackWorkOutcome(ctx context.Context, taskID string, fn func(*feedbackWork) error) (string, error) {
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	if a.DB != nil {
		outcome := "outcome_uncertain"
		err := a.withAtomicWriteObserved(ctx, a.DB, func(tx *gorm.DB) error {
			// The first statement obtains the SQLite write lock before any resource read.
			q := tx.Model(&store.FeedbackTask{}).Where("task_id=?", taskID).UpdateColumn("version", gorm.Expr("version+1"))
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return feedbackErr(404, "submission_not_found", false)
			}
			w := &feedbackWork{app: a, db: tx}
			if err := tx.Where("task_id=?", taskID).First(&w.task).Error; err != nil {
				return err
			}
			if err := tx.Where("task_id=?", taskID).Order("intent_index").Find(&w.intents).Error; err != nil {
				return err
			}
			if err := a.feedbackActive(ctx, w.task.StudentID, tx); err != nil {
				return err
			}
			var err error
			w.cfg, err = config.NewStore(store.NewConfigPersister(tx), nil)
			if err != nil {
				return err
			}
			w.sched = scheduler.NewService(store.NewSchedulerRepo(tx), w.cfg, a.Clock)
			w.memory = memory.NewService(store.NewMemoryRepo(tx))
			if err := fn(w); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			w.task.UpdatedAt = a.Clock.Now()
			if err := tx.Save(&w.task).Error; err != nil {
				return err
			}
			for i := range w.intents {
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "task_id"}, {Name: "intent_index"}}, DoUpdates: clause.AssignmentColumns([]string{"kind", "frozen_json", "result_json", "status", "attempt_count", "completed_at"})}).Create(&w.intents[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}, func(timing store.AtomicConnectionTiming) { outcome = timing.Outcome })
		return outcome, preserveAtomicOutcome(err, outcome)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	base := a.feedbackMemoryLocked()
	raw, _ := json.Marshal(base)
	cp := newFeedbackMemory()
	if err := json.Unmarshal(raw, cp); err != nil {
		return "failed_cleaned", err
	}
	task, ok := cp.Tasks[taskID]
	if !ok {
		return "failed_cleaned", feedbackErr(404, "submission_not_found", false)
	}
	if err := a.feedbackActive(ctx, task.StudentID, nil); err != nil {
		return "failed_cleaned", err
	}
	err := a.Cfg.WithMemoryTransaction(func(cfg *config.Store) error {
		return a.Sched.WithMemoryTransaction(cfg, func(sched *scheduler.Service) error {
			return a.Mem.WithMemoryTransaction(func(mem *memory.Service) error {
				w := &feedbackWork{app: a, mem: cp, cfg: cfg, sched: sched, memory: mem, task: task, intents: cp.Intents[taskID]}
				w.task.Version++
				if err := fn(w); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				w.task.UpdatedAt = a.Clock.Now()
				cp.Tasks[taskID] = w.task
				cp.Intents[taskID] = w.intents
				a.feedbackState = cp
				return nil
			})
		})
	})
	if err != nil {
		return "failed_cleaned", err
	}
	return "committed", nil
}
func (w *feedbackWork) notice(index int, recipient int64, kind, title, body string) error {
	now := w.app.Clock.Now()
	n := store.FeedbackNotice{TaskID: w.task.TaskID, IntentIndex: index, Recipient: recipient, Kind: kind, Title: title, Body: body, CreatedAt: now, UpdatedAt: now}
	if w.db != nil {
		return w.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "task_id"}, {Name: "intent_index"}, {Name: "recipient"}, {Name: "kind"}}, DoUpdates: clause.AssignmentColumns([]string{"title", "body", "updated_at"})}).Create(&n).Error
	}
	for i, v := range w.mem.Notices {
		if v.TaskID == n.TaskID && v.IntentIndex == index && v.Recipient == recipient && v.Kind == kind {
			w.mem.Notices[i].Body = body
			w.mem.Notices[i].Title = title
			w.mem.Notices[i].UpdatedAt = now
			return nil
		}
	}
	w.mem.NextNotice++
	n.ID = w.mem.NextNotice
	w.mem.Notices = append(w.mem.Notices, n)
	return nil
}
func (w *feedbackWork) todo(index int) error {
	t := store.FeedbackTodo{TaskID: w.task.TaskID, IntentIndex: index, StudentID: w.task.StudentID, Status: "open", CreatedAt: w.app.Clock.Now()}
	if w.db != nil {
		return w.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&t).Error
	}
	for _, v := range w.mem.Todos {
		if v.TaskID == t.TaskID && v.IntentIndex == index {
			return nil
		}
	}
	w.mem.NextTodo++
	t.ID = w.mem.NextTodo
	w.mem.Todos = append(w.mem.Todos, t)
	return nil
}
func feedbackRecord(task store.FeedbackTask, intents []store.FeedbackIntent) *FeedbackRecord {
	if task.LegacyJSON != "" {
		var r FeedbackRecord
		_ = json.Unmarshal([]byte(task.LegacyJSON), &r)
		r.SubmissionID = task.SubmissionID
		r.ID = task.ID
		r.TaskID = task.TaskID
		r.Version = task.Version
		r.Idempotency = "legacy"
		r.Results = []FeedbackIntentResult{}
		r.UpdatedAt = task.UpdatedAt
		return &r
	}
	r := &FeedbackRecord{ID: task.ID, StudentID: task.StudentID, Text: task.InputText, SubmissionID: task.SubmissionID, TaskID: task.TaskID, Version: task.Version, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, Idempotency: task.Idempotency, ErrorCode: task.ErrorCode, Results: []FeedbackIntentResult{}, Intents: []string{}}
	r.ParentSubmissionID = task.ParentSubmissionID
	r.ConversationID = task.ConversationID
	r.ClarificationAnswered = task.ClarificationChildTaskID != ""
	pending, failed, confirm, clarify, escalated := 0, 0, 0, 0, 0
	for _, it := range intents {
		v := FeedbackIntentResult{Index: it.IntentIndex, Kind: it.Kind, Status: it.Status}
		if it.ResultJSON != "" {
			_ = json.Unmarshal([]byte(it.ResultJSON), &v)
		}
		r.Results = append(r.Results, v)
		r.Intents = append(r.Intents, it.Kind)
		r.Retryable = r.Retryable || v.Retryable
		r.RefreshPending = r.RefreshPending || v.RefreshPending
		if v.Reply != "" {
			if r.Reply != "" {
				r.Reply += " "
			}
			r.Reply += v.Reply
		}
		switch v.Status {
		case "pending":
			pending++
		case "failed":
			failed++
		case "need_confirm":
			confirm++
		case "clarify":
			clarify++
		case "escalated":
			escalated++
		}
	}
	r.Status = "applied"
	switch {
	case task.PlanJSON == "":
		r.Status = "processing"
		r.Retryable = true
		if task.ErrorCode != "" {
			r.Status = "failed"
			r.Retryable = task.Retryable
			r.Reply = feedbackFailureReply(task.ErrorCode, task.Retryable)
		}
	case pending > 0:
		r.Status = "processing"
		r.Retryable = true
	case failed == len(intents) && failed > 0:
		r.Status = "failed"
	case failed > 0:
		r.Status = "partial"
	case confirm > 0:
		r.Status = "need_confirm"
	case clarify > 0:
		r.Status = "clarify"
	case escalated > 0:
		r.Status = "escalated"
	case r.RefreshPending:
		r.Status = "applied_refresh_pending"
	}
	if r.Status == "processing" {
		r.RetryAfterMs = 1000
	}
	return r
}
func (a *App) feedbackRead(ctx context.Context, sid int64, id string) (store.FeedbackTask, []store.FeedbackIntent, error) {
	var task store.FeedbackTask
	var intents []store.FeedbackIntent
	if a.DB != nil {
		err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("student_id=? AND submission_id=?", sid, id).First(&task).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return feedbackErr(404, "submission_not_found", false)
				}
				return err
			}
			return tx.Where("task_id=?", task.TaskID).Order("intent_index").Find(&intents).Error
		})
		return task, intents, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.feedbackMemoryLocked().Tasks {
		if t.StudentID == sid && t.SubmissionID == id {
			return t, append([]store.FeedbackIntent(nil), a.feedbackState.Intents[t.TaskID]...), nil
		}
	}
	return task, nil, feedbackErr(404, "submission_not_found", false)
}
func feedbackNowPtr(t time.Time) *time.Time { return &t }

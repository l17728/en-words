package api

import (
	"context"
	"encoding/json"
	"enwords/internal/store"
	"errors"
	"gorm.io/gorm"
)

func clarificationReady(parent store.FeedbackTask, items []store.FeedbackIntent) bool {
	if parent.LegacyJSON != "" || len(items) == 0 {
		return false
	}
	found := false
	for _, it := range items {
		if it.CompletedAt == nil {
			return false
		}
		var result FeedbackIntentResult
		if json.Unmarshal([]byte(it.ResultJSON), &result) != nil {
			return false
		}
		if it.Status == "clarify" && result.Status == "clarify" {
			found = true
		}
	}
	return found
}

func (a *App) validateClarificationParent(ctx context.Context, sid int64, id string) (store.FeedbackTask, error) {
	parent, items, err := a.feedbackRead(ctx, sid, id)
	if err != nil {
		var e *FeedbackSubmissionError
		if errors.As(err, &e) && e.Code == 404 {
			return store.FeedbackTask{}, feedbackErr(409, "clarification_parent_unavailable", false)
		}
		return store.FeedbackTask{}, err
	}
	if !clarificationReady(parent, items) {
		return store.FeedbackTask{}, feedbackErr(409, "clarification_parent_unavailable", false)
	}
	return parent, nil
}

func (w *feedbackWork) claimClarificationParent() error {
	if w.task.ParentSubmissionID == "" {
		return nil
	}
	var parent store.FeedbackTask
	var items []store.FeedbackIntent
	if w.db != nil {
		if err := w.db.Where("student_id=? AND submission_id=?", w.task.StudentID, w.task.ParentSubmissionID).First(&parent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return feedbackErr(409, "clarification_parent_unavailable", false)
			}
			return err
		}
		if err := w.db.Where("task_id=?", parent.TaskID).Order("intent_index").Find(&items).Error; err != nil {
			return err
		}
	} else {
		for _, t := range w.mem.Tasks {
			if t.StudentID == w.task.StudentID && t.SubmissionID == w.task.ParentSubmissionID {
				parent = t
				items = w.mem.Intents[t.TaskID]
				break
			}
		}
	}
	if parent.TaskID == w.task.TaskID || !clarificationReady(parent, items) {
		return feedbackErr(409, "clarification_parent_unavailable", false)
	}
	if parent.ClarificationChildTaskID != "" && parent.ClarificationChildTaskID != w.task.TaskID {
		return feedbackErr(409, "clarification_already_answered", false)
	}
	parent.ClarificationChildTaskID = w.task.TaskID
	parent.Version++
	parent.UpdatedAt = w.app.Clock.Now()
	w.task.ConversationID = parent.ConversationID
	if w.task.ConversationID == "" {
		w.task.ConversationID = parent.TaskID
	}
	if w.db != nil {
		return w.db.Save(&parent).Error
	}
	w.mem.Tasks[parent.TaskID] = parent
	return nil
}

// A rejected parent claim never leaves an unplanned, unrecoverable child task.
func (a *App) discardUnplannedFeedback(ctx context.Context, task store.FeedbackTask) error {
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	if a.DB != nil {
		return a.DB.WithContext(ctx).Where("task_id=? AND student_id=? AND plan_json=?", task.TaskID, task.StudentID, "").Delete(&store.FeedbackTask{}).Error
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.feedbackMemoryLocked()
	if current, ok := m.Tasks[task.TaskID]; ok && current.PlanJSON == "" {
		delete(m.Tasks, task.TaskID)
		delete(m.Intents, task.TaskID)
	}
	return nil
}

package api

import (
	"context"
	"enwords/internal/agent"
	"time"
)

func feedbackMonth(at time.Time) string {
	return at.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01")
}

func (w *feedbackWork) postponementsThisMonth() (int, error) {
	month := feedbackMonth(w.task.CreatedAt)
	count := 0
	if w.db != nil {
		var rows []struct{ CreatedAt time.Time }
		err := w.db.Table("feedback_intents AS i").Select("t.created_at").Joins("JOIN feedback_tasks AS t ON t.task_id=i.task_id").Where("t.student_id=? AND i.kind=? AND i.status=? AND i.completed_at IS NOT NULL", w.task.StudentID, "D_postpone", "applied").Scan(&rows).Error
		if err != nil {
			return 0, err
		}
		for _, row := range rows {
			if feedbackMonth(row.CreatedAt) == month {
				count++
			}
		}
		return count, nil
	}
	for id, task := range w.mem.Tasks {
		if task.StudentID != w.task.StudentID || feedbackMonth(task.CreatedAt) != month {
			continue
		}
		for _, it := range w.mem.Intents[id] {
			if it.Kind == "D_postpone" && it.Status == "applied" && it.CompletedAt != nil {
				count++
			}
		}
	}
	return count, nil
}

func (w *feedbackWork) enforceFeedbackLimits(ctx context.Context, f *feedbackFrozenIntent) (any, error) {
	if f.Mode != "postpone" {
		return nil, nil
	}
	used, err := w.postponementsThisMonth()
	if err != nil {
		return nil, err
	}
	w.app.Log.InfoCtx(ctx, "feedback", "feedback_quota_checked", map[string]any{"task_id": w.task.TaskID, "intent_index": f.Index, "month": feedbackMonth(w.task.CreatedAt), "used": used})
	if agent.FeedbackPostponeAllowed(used) {
		return nil, nil
	}
	f.Mode = "need_confirm"
	f.Key = "postpone_monthly_limit"
	f.Calls = []string{"feedback.reply"}
	return map[string]any{"month": feedbackMonth(w.task.CreatedAt), "used": used, "limit": agent.FeedbackPostponeLimit()}, nil
}

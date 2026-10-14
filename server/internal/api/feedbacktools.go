package api

import (
	"context"
	"enwords/internal/agent"
	"enwords/internal/auth"
	"enwords/internal/store"
	"enwords/internal/tools"
	"enwords/internal/wordbook"
	"fmt"
	"strings"
)

type feedbackToolSession struct {
	work   *feedbackWork
	frozen feedbackFrozenIntent
	result *FeedbackIntentResult
}

func (a *App) registerFeedbackTools(reg *tools.Registry, svc agentServices) error {
	for _, name := range []string{"scheduler.postpone_lesson", "scheduler.boost_word", "feedback.escalate", "feedback.reply"} {
		toolName := name
		t := tools.Tool{Name: name, Domain: strings.Split(name, ".")[0], Risk: tools.RiskMid, Scope: []tools.Role{tools.RoleSystemAgent}, InputSchema: mustSchema(`{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`)}
		t.Handler = func(ctx context.Context, caller tools.Caller, args tools.Args) (any, error) {
			task, err := taskFromContext(ctx, caller)
			if err != nil {
				return nil, err
			}
			s := svc.feedback
			if task.app != a || task.trigger != "feedback_submitted" || s == nil || s.work.task.TaskID != task.taskID || s.work.task.StudentID != task.studentID {
				return nil, fmt.Errorf("bound feedback task required")
			}
			if a.DB != nil && !svc.transactional {
				return nil, fmt.Errorf("transaction required")
			}
			var in struct {
				Reason string `json:"reason"`
			}
			if err := strictToolArgs(args, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Reason) == "" {
				return nil, fmt.Errorf("reason required")
			}
			w, f := s.work, s.frozen
			switch toolName {
			case "scheduler.postpone_lesson":
				if f.Mode != "postpone" {
					return nil, fmt.Errorf("task action mismatch")
				}
				lesson, err := w.sched.Lesson(f.LessonID)
				if err != nil {
					return nil, err
				}
				if lesson.StudentID != task.studentID || lesson.BookID != f.BookID || lesson.Status != "scheduled" || !lesson.OpenAt.Equal(f.OriginalAt) {
					return nil, feedbackErr(409, "stale_target", false)
				}
				if !f.TargetAt.After(w.app.Clock.Now()) || !f.TargetAt.After(f.OriginalAt) || f.TargetAt.After(w.task.CreatedAt.In(f.OriginalAt.Location()).AddDate(0, 0, 7)) {
					return nil, feedbackErr(400, "invalid_date", false)
				}
				if err := w.validateFeedbackBook(f.BookID); err != nil {
					return nil, err
				}
				ls, err := w.sched.StudentLessons(task.studentID)
				if err != nil {
					return nil, err
				}
				for _, l := range ls {
					if l.ID != lesson.ID && (l.Status == "scheduled" || l.Status == "active") && l.OpenAt.Equal(f.TargetAt) {
						return nil, feedbackErr(409, "schedule_conflict", false)
					}
				}
				if err := w.sched.PostponeLesson(lesson.ID, f.TargetAt); err != nil {
					return nil, err
				}
				s.result.Actual = map[string]any{"lesson_id": lesson.ID, "book_id": lesson.BookID, "open_at": f.TargetAt}
				return s.result.Actual, nil
			case "scheduler.boost_word":
				if f.Mode != "boost" || len(f.Words) == 0 {
					return nil, fmt.Errorf("task action mismatch")
				}
				evictedWords := make([]map[string]any, 0)
				for _, target := range f.Words {
					if err := w.validateFeedbackBook(target.BookID); err != nil {
						return nil, err
					}
					entry, err := w.books().EntryByKey(target.BookID, normWord(f.Word))
					if err != nil {
						return nil, err
					}
					if entry.ID != target.WordID {
						return nil, feedbackErr(409, "stale_target", false)
					}
					if _, err := w.sched.GetCard(task.studentID, target.BookID, target.WordID); err != nil {
						return nil, err
					}
					evicted, err := w.sched.BoostWordWithCapacity(task.studentID, target.BookID, target.WordID, agent.FeedbackBoostCapacity())
					if err != nil {
						return nil, err
					}
					for _, id := range evicted {
						old, err := w.books().GetEntry(id)
						if err != nil {
							return nil, err
						}
						if old.BookID != target.BookID {
							return nil, fmt.Errorf("boost entry book mismatch")
						}
						evictedWords = append(evictedWords, map[string]any{"book_id": target.BookID, "word_id": id, "word": old.Word})
					}
					a.Log.InfoCtx(ctx, "scheduler", "boost_pool_updated", map[string]any{"task_id": task.taskID, "book_id": target.BookID, "word_id": target.WordID, "evicted": evicted, "transaction_outcome": "pending"})
				}
				s.result.Actual = map[string]any{"resources": f.Words, "count": len(f.Words), "boosted_word": f.Word, "evicted_words": evictedWords}
				return s.result.Actual, nil
			case "feedback.escalate":
				if f.Mode != "escalate" {
					return nil, fmt.Errorf("task action mismatch")
				}
				if f.Kind != "F_complaint" && w.task.ParentSubmissionID == "" {
					return nil, fmt.Errorf("clarification parent required")
				}
				var admins []int64
				if w.db != nil {
					var rows []store.User
					if err := w.db.Where("role=? AND status=?", string(auth.RoleAdmin), "active").Find(&rows).Error; err != nil {
						return nil, err
					}
					for _, u := range rows {
						admins = append(admins, u.ID)
					}
				} else {
					for _, u := range a.Auth.ListUsers() {
						if u.Role == auth.RoleAdmin && u.Status == "active" {
							admins = append(admins, u.ID)
						}
					}
				}
				if len(admins) == 0 {
					return nil, feedbackErr(409, "admin_unavailable", false)
				}
				if err := w.todo(f.Index); err != nil {
					return nil, err
				}
				for _, sid := range admins {
					if err := w.notice(f.Index, sid, "admin_feedback", "学生反馈待处理", "有新的学生反馈，请在待办详情中查看。"); err != nil {
						return nil, err
					}
				}
				s.result.Status = "escalated"
				s.result.Actual = map[string]any{"recipients": len(admins)}
				return s.result.Actual, nil
			case "feedback.reply":
				if f.Mode == "need_confirm" || f.Mode == "clarify" {
					s.result.Status = f.Mode
				}
				s.result.Reply = agent.RenderFeedbackFacts(a.Bank, task.studentID, agent.FeedbackReplyFacts{Mode: f.Mode, Key: f.Key, Target: f.Target, Date: f.TargetAt.Format("2006-01-02 15:04"), Count: len(f.Words)})
				return s.result.Reply, nil
			}
			return nil, fmt.Errorf("unsupported feedback tool")
		}
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}
func (w *feedbackWork) validateFeedbackBook(bid int64) error {
	ids, err := w.sched.StudentBooks(w.task.StudentID)
	if err != nil {
		return err
	}
	found := false
	for _, id := range ids {
		if id == bid {
			found = true
		}
	}
	if !found {
		return feedbackErr(403, "book_not_enrolled", false)
	}
	b, err := w.books().GetBook(bid)
	if err != nil {
		return err
	}
	if b.Status != wordbook.StatusPublished {
		return feedbackErr(409, "book_unavailable", false)
	}
	return nil
}

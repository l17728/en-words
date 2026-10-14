package api

import (
	"context"
	"enwords/internal/agent"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/tools"
	"fmt"
)

type overdueWorkKey struct{}
type overdueWork struct {
	task   store.OverdueTask
	plan   agent.OverduePlan
	sched  *scheduler.Service
	next   *scheduler.Lesson
	notice func(string) error
}

func (a *App) registerOverdueTools(reg *tools.Registry) error {
	for _, name := range []string{"scheduler.replan_overdue", "notification.publish_overdue"} {
		name := name
		if err := reg.Register(tools.Tool{Name: name, Domain: "scheduler", Risk: tools.RiskHigh, Scope: []tools.Role{tools.RoleSystemAgent}, InputSchema: mustSchema(`{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`), Handler: func(ctx context.Context, c tools.Caller, args tools.Args) (any, error) {
			t, err := taskFromContext(ctx, c)
			if err != nil {
				return nil, err
			}
			w, ok := ctx.Value(overdueWorkKey{}).(*overdueWork)
			if !ok || t.app != a || t.trigger != "cron_overdue" || t.studentID != w.task.StudentID || t.bookID != w.task.BookID || t.lessonID != w.task.LessonID {
				return nil, fmt.Errorf("frozen overdue task required")
			}
			var in struct {
				Reason string `json:"reason"`
			}
			if err := strictToolArgs(args, &in); err != nil {
				return nil, err
			}
			if in.Reason != "scheduled overdue recovery" {
				return nil, fmt.Errorf("invalid overdue reason")
			}
			if name == "scheduler.replan_overdue" {
				if w.next != nil {
					return nil, fmt.Errorf("overdue already applied")
				}
				w.next, err = w.sched.ReplanExpired(w.task.LessonID, w.task.OriginalOpenAt, w.plan.Target)
				return w.next, err
			}
			if w.next == nil {
				return nil, fmt.Errorf("overdue replan required before notice")
			}
			if err := w.notice(a.System.OverdueNotice(w.plan)); err != nil {
				return nil, err
			}
			return true, nil
		}}); err != nil {
			return err
		}
	}
	return nil
}

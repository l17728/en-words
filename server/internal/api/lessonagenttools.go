package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"enwords/internal/scheduler"
	"enwords/internal/scripts"
	"enwords/internal/tools"
)

type lessonScheduleResult struct {
	Status               string            `json:"status"`
	Lesson               *scheduler.Lesson `json:"lesson,omitempty"`
	RequestedRatio       float64           `json:"requested_ratio"`
	SelectedNewCount     int               `json:"selected_new_count"`
	FirstLessonExemption bool              `json:"first_lesson_exemption"`
}

// The publisher holds trusted facts and a transaction-bound writer. Tool JSON
// cannot choose another recipient or supply a fabricated lesson report.
type lessonPublisher struct {
	task    *lessonCompletion
	publish func() (any, error)
}

func (a *App) registerLessonAgentTools(reg *tools.Registry, svc agentServices) error {
	check := func(ctx context.Context, caller tools.Caller) (*agentTask, error) {
		task, err := taskFromContext(ctx, caller)
		if err != nil {
			return nil, err
		}
		if task.app != a || task.trigger != "lesson_finished" || task.lessonID <= 0 || svc.publisher == nil || svc.publisher.task == nil {
			return nil, fmt.Errorf("trusted lesson completion required")
		}
		p := svc.publisher.task
		if task.lessonID != p.LessonID || task.studentID != p.StudentID || task.bookID != p.BookID {
			return nil, fmt.Errorf("lesson publication owner mismatch")
		}
		if a.DB != nil && !svc.transactional {
			return nil, fmt.Errorf("persistent tool requires audited transaction")
		}
		return task, nil
	}
	for _, name := range []string{"scheduler.plan_next_lesson", "report.compose_reply", "report.publish"} {
		toolName := name
		err := reg.Register(tools.Tool{Name: toolName, Domain: strings.Split(toolName, ".")[0], Risk: tools.RiskMid, Scope: []tools.Role{tools.RoleSystemAgent},
			InputSchema: mustSchema(`{"type":"object","properties":{}}`),
			Handler: func(ctx context.Context, caller tools.Caller, args tools.Args) (any, error) {
				task, err := check(ctx, caller)
				if err != nil {
					return nil, err
				}
				var empty struct{}
				if err := strictToolArgs(args, &empty); err != nil {
					return nil, err
				}
				p := svc.publisher.task
				switch toolName {
				case "scheduler.plan_next_lesson":
					if svc.scheduler == nil {
						return nil, fmt.Errorf("scheduler service unavailable")
					}
					result, err := a.planLessonFromAgent(svc.scheduler, p)
					if err == nil {
						p.Next = result
					}
					return result, err
				case "report.compose_reply":
					solid := p.Result.Report.QualSolid
					if solid == "" {
						solid = "需巩固"
					}
					text := a.Bank.RenderID(scripts.NewSeededRNG(task.lessonID), "RB-7", map[string]any{"solid": solid, "solid_comment": "别着急，学过的词会继续安排复习。"})
					if strings.TrimSpace(text) == "" {
						text = "别着急，学过的词会继续安排复习。"
					}
					if p.Target != nil {
						text += fmt.Sprintf(" 后续每课学习量已设为%d词。", *p.Target)
					}
					p.Replies = append(p.Replies, text)
					return text, nil
				case "report.publish":
					if svc.publisher.publish == nil {
						return nil, fmt.Errorf("report publisher unavailable")
					}
					return svc.publisher.publish()
				}
				return nil, fmt.Errorf("unsupported lesson tool")
			}})
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *App) planLessonFromAgent(sched *scheduler.Service, task *lessonCompletion) (*lessonScheduleResult, error) {
	result := &lessonScheduleResult{RequestedRatio: task.Plan.Policy.MaxNewRatio}
	next, err := sched.PlanNextWithPolicy(task.StudentID, task.BookID, task.Plan.Policy)
	switch {
	case err == nil:
		result.Status = "planned"
	case errors.Is(err, scheduler.ErrNoWords):
		result.Status = "no_words"
		return result, nil
	case errors.Is(err, scheduler.ErrAlreadyPlanned):
		result.Status = "already_planned"
		lessons, e := sched.Lessons(task.StudentID, task.BookID)
		if e != nil {
			return nil, e
		}
		for _, lesson := range lessons {
			if lesson.Status == scheduler.LessonScheduledStr {
				if next != nil {
					return nil, fmt.Errorf("multiple scheduled lessons")
				}
				next = lesson
			}
		}
		if next == nil {
			return nil, fmt.Errorf("scheduled lesson missing")
		}
	default:
		return nil, err
	}
	if a.TestMode && result.Status == "planned" {
		next.OpenAt = a.Clock.Now()
		if err := sched.SaveLesson(next); err != nil {
			return nil, err
		}
	}
	// Also repairs reservations after CreateLesson succeeded but ReserveCards failed.
	if err := sched.ReserveCards(task.StudentID, task.BookID, next.BatchWordIDs, next.OpenAt); err != nil {
		return nil, err
	}
	for _, wid := range next.BatchWordIDs {
		card, e := sched.GetCard(task.StudentID, task.BookID, wid)
		if e != nil {
			return nil, e
		}
		if card.Status == scheduler.StatusNew {
			result.SelectedNewCount++
		}
	}
	result.Lesson = next
	// Completion tasks necessarily have teaching history; no first-lesson waiver.
	result.FirstLessonExemption = false
	return result, nil
}

func lessonToolArgs(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

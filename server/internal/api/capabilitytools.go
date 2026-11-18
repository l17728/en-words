package api

import (
	"context"
	"enwords/internal/memory"
	"enwords/internal/scripts"
	"enwords/internal/tools"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Definitions are reused by live and transaction-bound registries. JSON never
// supplies a student, role or book identity: those come from the trusted task.
func (a *App) registerCapabilityTools(reg *tools.Registry, svc agentServices) error {
	check := func(ctx context.Context, caller tools.Caller) (*agentTask, error) {
		t, err := taskFromContext(ctx, caller)
		if err != nil {
			return nil, err
		}
		if t.app != a {
			return nil, fmt.Errorf("task application mismatch")
		}
		return t, nil
	}
	for _, name := range []string{"scheduler.get_due_words", "scheduler.get_progress_summary", "progress.query"} {
		name := name
		if err := reg.Register(tools.Tool{Name: name, Domain: "scheduler", Risk: tools.RiskLow, Scope: []tools.Role{tools.RoleTeacherAgent, tools.RoleSystemAgent}, InputSchema: mustSchema(`{"type":"object","properties":{},"additionalProperties":false}`), Handler: func(ctx context.Context, c tools.Caller, args tools.Args) (any, error) {
			t, err := check(ctx, c)
			if err != nil {
				return nil, err
			}
			var in struct{}
			if err := strictToolArgs(args, &in); err != nil {
				return nil, err
			}
			if t.bookID <= 0 || svc.scheduler == nil {
				return nil, fmt.Errorf("bound book and scheduler required")
			}
			if name == "scheduler.get_due_words" {
				return svc.scheduler.DueWords(t.studentID, t.bookID)
			}
			if name == "scheduler.get_progress_summary" {
				lessons, err := svc.scheduler.Lessons(t.studentID, t.bookID)
				if err != nil {
					return nil, err
				}
				finished, expired := 0, 0
				reports := []map[string]any{}
				for _, l := range lessons {
					if l.FinishedAt != nil {
						finished++
						if svc.report == nil {
							return nil, fmt.Errorf("report reader unavailable")
						}
						r, err := svc.report(l.ID)
						if err != nil {
							return nil, err
						}
						if r == nil || math.IsNaN(r.Report.FirstTryRate) || math.IsInf(r.Report.FirstTryRate, 0) || r.Report.FirstTryRate < 0 || r.Report.FirstTryRate > 1 || r.Report.AvgAnswerMs < 0 {
							return nil, fmt.Errorf("invalid report metrics")
						}
						reports = append(reports, map[string]any{"lesson_id": l.ID, "first_try_rate": r.Report.FirstTryRate, "avg_answer_ms": r.Report.AvgAnswerMs})
					}
					if l.Status == "expired" {
						expired++
					}
				}
				return map[string]any{"lessons": len(lessons), "finished": finished, "expired": expired, "reports": reports}, nil
			}
			return svc.scheduler.Progress(t.studentID, t.bookID)
		}}); err != nil {
			return err
		}
	}
	for _, name := range []string{"scripts.list", "scripts.pick"} {
		name := name
		if err := reg.Register(tools.Tool{Name: name, Domain: "scripts", Risk: tools.RiskLow, Scope: []tools.Role{tools.RoleTeacherAgent, tools.RoleSystemAgent}, InputSchema: mustSchema(`{"type":"object","properties":{"category":{"type":"string"}},"required":["category"],"additionalProperties":false}`), Handler: func(ctx context.Context, c tools.Caller, args tools.Args) (any, error) {
			t, err := check(ctx, c)
			if err != nil {
				return nil, err
			}
			var in struct {
				Category string `json:"category"`
			}
			if err := strictToolArgs(args, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Category) == "" || a.Bank == nil {
				return nil, fmt.Errorf("category and bank required")
			}
			items := a.Bank.List(in.Category)
			if name == "scripts.list" {
				if items == nil {
					items = []scripts.Template{}
				}
				return items, nil
			}
			item, ok := a.Bank.Pick(in.Category, scripts.NewSeededRNG(t.lessonID*31+t.studentID), nil)
			if !ok {
				return nil, fmt.Errorf("script category unavailable")
			}
			return item, nil
		}}); err != nil {
			return err
		}
	}
	for _, name := range []string{"memory.list", "memory.revise", "memory.delete"} {
		name := name
		risk := tools.RiskMid
		if name == "memory.list" {
			risk = tools.RiskLow
		}
		if err := reg.Register(tools.Tool{Name: name, Domain: "memory", Risk: risk, Scope: []tools.Role{tools.RoleTeacherAgent, tools.RoleSystemAgent}, InputSchema: mustSchema(`{"type":"object","properties":{"scope":{"type":"string"},"id":{"type":"integer"},"content":{"type":"string"},"importance":{"type":"integer"},"kind":{"type":"string"}},"required":["scope"],"additionalProperties":false}`), Handler: func(ctx context.Context, c tools.Caller, args tools.Args) (any, error) {
			t, err := check(ctx, c)
			if err != nil {
				return nil, err
			}
			var in struct {
				Scope      memory.Scope `json:"scope"`
				ID         int64        `json:"id"`
				Content    string       `json:"content"`
				Importance int          `json:"importance"`
				Kind       string       `json:"kind"`
			}
			if err := strictToolArgs(args, &in); err != nil {
				return nil, err
			}
			bid := int64(0)
			switch in.Scope {
			case memory.ScopeStudentProfile:
			case memory.ScopeTaskMemory:
				bid = t.bookID
				if bid <= 0 {
					return nil, fmt.Errorf("bound book required")
				}
			default:
				return nil, fmt.Errorf("invalid memory scope")
			}
			if svc.memory == nil {
				return nil, fmt.Errorf("memory unavailable")
			}
			owner := memory.OwnerRole(c.Role)
			items, err := svc.memory.List(owner, t.studentID, bid, in.Kind)
			if err != nil {
				return nil, err
			}
			if name == "memory.list" {
				if items == nil {
					items = []*memory.Memory{}
				}
				sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
				return items, nil
			}
			found := false
			for _, m := range items {
				if m.ID == in.ID {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("memory resource mismatch")
			}
			if a.DB != nil && !svc.transactional {
				return nil, fmt.Errorf("persistent tool requires audited transaction")
			}
			if name == "memory.delete" {
				if err := svc.memory.Delete(owner, in.ID); err != nil {
					return nil, err
				}
				return true, nil
			}
			if strings.TrimSpace(in.Content) == "" || in.Importance < 1 || in.Importance > 5 {
				return nil, fmt.Errorf("invalid memory content or importance")
			}
			return svc.memory.Revise(owner, in.ID, in.Content, in.Importance)
		}}); err != nil {
			return err
		}
	}
	return a.registerOverdueTools(reg)
}

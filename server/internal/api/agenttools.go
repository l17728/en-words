package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/tools"
	"fmt"
	"gorm.io/gorm"
	"io"
	"strings"
)

type agentTaskKey struct{}

// Tasks are created by application code, never decoded from client JSON.
type agentTask struct {
	app                         *App
	studentID, bookID, lessonID int64
	taskID, trigger             string
	role                        tools.Role
}

func (a *App) newAgentTaskContext(ctx context.Context, sid, bid, lid int64, trigger string, role tools.Role) (context.Context, error) {
	if sid <= 0 || bid < 0 || lid < 0 || (role != tools.RoleSystemAgent && role != tools.RoleTeacherAgent) {
		return nil, fmt.Errorf("invalid trusted task identity")
	}
	if role == tools.RoleTeacherAgent && (lid == 0 || trigger != "classroom_event") {
		return nil, fmt.Errorf("teacher task requires classroom event")
	}
	if role == tools.RoleSystemAgent && trigger != "feedback_submitted" && trigger != "lesson_finished" && trigger != "cron_overdue" {
		return nil, fmt.Errorf("unsupported system task trigger")
	}
	if trigger == "lesson_finished" && lid == 0 {
		return nil, fmt.Errorf("settlement task requires lesson")
	}
	if bid > 0 {
		books, err := a.Sched.StudentBooks(sid)
		if err != nil {
			return nil, err
		}
		enrolled := false
		for _, id := range books {
			if id == bid {
				enrolled = true
				break
			}
		}
		if !enrolled {
			return nil, fmt.Errorf("task book not enrolled")
		}
	}
	if lid > 0 {
		lesson, err := a.Sched.Lesson(lid)
		if err != nil {
			return nil, err
		}
		if lesson.StudentID != sid || lesson.BookID != bid {
			return nil, fmt.Errorf("task lesson ownership mismatch")
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	task := &agentTask{app: a, studentID: sid, bookID: bid, lessonID: lid, trigger: trigger, role: role, taskID: hex.EncodeToString(nonce[:])}
	ctx = context.WithValue(ctx, agentTaskKey{}, task)
	if obs.TraceID(ctx) == "" {
		ctx = obs.WithTrace(ctx, task.taskID)
	}
	return tools.WithEventBudget(ctx, 8), nil
}

func taskFromContext(ctx context.Context, caller tools.Caller) (*agentTask, error) {
	task, ok := ctx.Value(agentTaskKey{}).(*agentTask)
	if !ok || task == nil || task.studentID <= 0 || task.role != caller.Role {
		return nil, fmt.Errorf("trusted agent task required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return task, nil
}

// Handlers must use these services, including when bound to a SQLite transaction.
type agentServices struct {
	configDB       *gorm.DB
	configMutation config.MutationHandler
	report         func(int64) (*classroom.Result, error)
	scheduler      *scheduler.Service
	publisher      *lessonPublisher
	feedback       *feedbackToolSession
	config         *config.Store
	memory         *memory.Service
	transactional  bool
}

func strictToolArgs(raw json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid tool arguments")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid tool arguments")
	}
	return nil
}

func (a *App) setStudentFromAgent(sid int64, key string, value any, reason string) error {
	ctx, err := a.newAgentTaskContext(context.Background(), sid, 0, 0, "feedback_submitted", tools.RoleSystemAgent)
	if err != nil {
		return err
	}
	_, err = a.callAgentTool(ctx, "config.set_student", map[string]any{"key": key, "value": value, "reason": reason})
	return err
}

func (a *App) registerStudentTools() {
	svc := agentServices{config: a.Cfg, memory: a.Mem, scheduler: a.Sched, report: a.capabilityReport}
	if err := a.registerAgentTools(a.Tools, svc); err != nil {
		panic(err)
	}
	if err := a.registerLessonAgentTools(a.Tools, svc); err != nil {
		panic(err)
	}
	if err := a.registerFeedbackTools(a.Tools, svc); err != nil {
		panic(err)
	}
}

func (a *App) registerAgentTools(reg *tools.Registry, svc agentServices) error {
	if svc.configMutation == nil && svc.config != nil && svc.scheduler != nil && svc.config != a.Cfg {
		svc.configMutation = func(ctx context.Context, m config.Mutation) (config.MutationResult, error) {
			return applyConfigMutationInWork(ctx, &configMutationWork{cfg: svc.config, sched: svc.scheduler, db: svc.configDB}, m)
		}
	}
	check := func(ctx context.Context, caller tools.Caller) (*agentTask, error) {
		task, err := taskFromContext(ctx, caller)
		if err != nil {
			return nil, err
		}
		if task.app != a {
			return nil, fmt.Errorf("task application mismatch")
		}
		return task, nil
	}
	defs := []tools.Tool{
		{Name: "config.set_student", Domain: "config", Risk: tools.RiskHigh, Scope: []tools.Role{tools.RoleSystemAgent},
			InputSchema: mustSchema(`{"type":"object","properties":{"key":{"type":"string"},"value":{},"reason":{"type":"string"}},"required":["key","value","reason"]}`),
			Handler: func(ctx context.Context, caller tools.Caller, args tools.Args) (any, error) {
				task, err := check(ctx, caller)
				if err != nil {
					return nil, err
				}
				var in struct {
					Key    string `json:"key"`
					Value  any    `json:"value"`
					Reason string `json:"reason"`
				}
				if err := strictToolArgs(args, &in); err != nil {
					return nil, err
				}
				if in.Key != "wordsPerLesson" && in.Key != "lessonsPerDay" {
					return nil, fmt.Errorf("unsupported student adjustment")
				}
				if svc.config == nil {
					return nil, fmt.Errorf("config service unavailable")
				}
				if a.DB != nil && !svc.transactional {
					return nil, fmt.Errorf("persistent tool requires audited transaction")
				}
				if err := svc.config.SetStudent(string(caller.Role), task.studentID, in.Key, in.Value, in.Reason); err != nil {
					return nil, err
				}
				return svc.config.Int(task.studentID, in.Key), nil
			}},
		{Name: "config.get", Domain: "config", Risk: tools.RiskLow, Scope: []tools.Role{tools.RoleSystemAgent},
			InputSchema: mustSchema(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
			Handler: func(ctx context.Context, caller tools.Caller, args tools.Args) (any, error) {
				task, err := check(ctx, caller)
				if err != nil {
					return nil, err
				}
				var in struct {
					Key string `json:"key"`
				}
				if err := strictToolArgs(args, &in); err != nil {
					return nil, err
				}
				if in.Key != "wordsPerLesson" && in.Key != "lessonsPerDay" {
					return nil, fmt.Errorf("unsupported student config")
				}
				if svc.config == nil {
					return nil, fmt.Errorf("config service unavailable")
				}
				return svc.config.Int(task.studentID, in.Key), nil
			}},
	}
	for _, name := range []string{"memory.append", "memory.recall"} {
		toolName := name
		risk := tools.RiskMid
		if name == "memory.recall" {
			risk = tools.RiskLow
		}
		defs = append(defs, tools.Tool{Name: toolName, Domain: "memory", Risk: risk, Scope: []tools.Role{tools.RoleSystemAgent, tools.RoleTeacherAgent},
			InputSchema: mustSchema(`{"type":"object","properties":{"scope":{"type":"string"}},"required":["scope"]}`),
			Handler: func(ctx context.Context, caller tools.Caller, args tools.Args) (any, error) {
				task, err := check(ctx, caller)
				if err != nil {
					return nil, err
				}
				var in struct {
					Scope          memory.Scope `json:"scope"`
					StudentID      *int64       `json:"student_id"`
					BookID         *int64       `json:"book_id"`
					SourceLessonID *int64       `json:"source_lesson_id"`
					Kind           string       `json:"kind"`
					Content        string       `json:"content"`
					Importance     int          `json:"importance"`
					Query          string       `json:"query"`
					Limit          int          `json:"limit"`
				}
				if err := strictToolArgs(args, &in); err != nil {
					return nil, err
				}
				bid := int64(0)
				switch in.Scope {
				case memory.ScopeStudentProfile:
				case memory.ScopeTaskMemory:
					bid = task.bookID
					if bid <= 0 {
						return nil, fmt.Errorf("task memory requires bound book")
					}
				default:
					return nil, fmt.Errorf("invalid memory scope")
				}
				if (in.StudentID != nil && *in.StudentID != task.studentID) || (in.BookID != nil && *in.BookID != bid) || (in.SourceLessonID != nil && *in.SourceLessonID != task.lessonID) {
					return nil, fmt.Errorf("memory resource mismatch")
				}
				if svc.memory == nil {
					return nil, fmt.Errorf("memory service unavailable")
				}
				owner := memory.OwnerRole(caller.Role)
				if toolName == "memory.recall" {
					if in.Limit < 0 || in.Limit > 100 {
						return nil, fmt.Errorf("invalid recall limit")
					}
					return svc.memory.Recall(owner, task.studentID, bid, in.Kind, in.Query, in.Limit)
				}
				if strings.TrimSpace(in.Kind) == "" || strings.TrimSpace(in.Content) == "" || in.Importance < 1 || in.Importance > 5 {
					return nil, fmt.Errorf("invalid memory content or importance")
				}
				if a.DB != nil && !svc.transactional {
					return nil, fmt.Errorf("persistent tool requires audited transaction")
				}
				return svc.memory.Append(owner, in.Scope, task.studentID, bid, in.Kind, in.Content, in.Importance, task.lessonID)
			}})
	}
	for _, def := range defs {
		if err := reg.Register(def); err != nil {
			return err
		}
	}
	return a.registerCapabilityTools(reg, svc)
}

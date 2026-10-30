package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"enwords/internal/agent"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/tools"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type lessonCommitRecord struct {
	Version     int    `json:"version"`
	StudentID   int64  `json:"student_id"`
	BookID      int64  `json:"book_id"`
	Fingerprint string `json:"fingerprint"`
}

func (a *App) commitLessonWithAgents(ctx context.Context, token generationToken, id, sid, bid int64, res *classroom.Result, st classroom.State) error {
	if token.owner == nil || token.lesson != id {
		return errGenerationRetired
	}
	a.completionMu.Lock()
	defer a.completionMu.Unlock()
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	// A completed durable task remains replayable after its book was removed.
	if a.DB != nil {
		var record lessonCommitRecord
		err := a.readRuntimeError(fmt.Sprintf("settled/%d", id), &record)
		if err == nil {
			if record.StudentID == 0 { // Legacy completion marker: consult persisted owner.
				lesson, e := a.Sched.Lesson(id)
				if e != nil {
					return e
				}
				record.StudentID, record.BookID = lesson.StudentID, lesson.BookID
			}
			if record.StudentID != sid || record.BookID != bid {
				return fmt.Errorf("completed lesson owner mismatch")
			}
			if record.Fingerprint != "" {
				_, _, fingerprint, e := freezeLessonInput(res, st)
				if e != nil {
					return e
				}
				if fingerprint != record.Fingerprint {
					return fmt.Errorf("completion retry facts changed")
				}
			}
			a.reloadCommittedConfig(id)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	frozen, state, fingerprint, err := freezeLessonInput(res, st)
	if err != nil {
		return err
	}
	ctx, err = a.newAgentTaskContext(obs.WithTrace(ctx, fmt.Sprintf("lesson-%d", id)), sid, bid, id, "lesson_finished", tools.RoleSystemAgent)
	if err != nil {
		return err
	}
	ctx.Value(agentTaskKey{}).(*agentTask).taskID = fmt.Sprintf("lesson/%d/settlement-v1", id)
	var task *lessonCompletion
	if a.DB == nil && a.completionTasks != nil {
		task = a.completionTasks[id]
	}
	if task != nil {
		if task.StudentID != sid || task.BookID != bid || task.Fingerprint != fingerprint {
			return fmt.Errorf("completion retry facts changed")
		}
	} else {
		lesson, e := a.Sched.Lesson(id)
		if e != nil {
			return e
		}
		if e = validateLessonResult(lesson, sid, bid, frozen); e != nil {
			return e
		}
		actions := a.System.Decide(agent.SystemInput{Trigger: "lesson_finished", StudentID: sid, BookID: bid, CorrectRate: frozen.Report.FirstTryRate, AvgAnswerMs: frozen.Report.AvgAnswerMs})
		plan, e := compileLessonPlan(actions)
		if e != nil {
			return e
		}
		task = &lessonCompletion{LessonID: id, StudentID: sid, BookID: bid, Fingerprint: fingerprint, Result: frozen, State: state, Plan: plan, Done: map[int]any{}, At: a.Clock.Now()}
		if a.DB == nil {
			if a.completionTasks == nil {
				a.completionTasks = map[int64]*lessonCompletion{}
			}
			a.completionTasks[id] = task
		}
	}
	a.Log.InfoCtx(ctx, "agent", "agent_decision", map[string]any{"lesson_id": id, "task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "trigger": "lesson_finished", "actions": len(task.Plan.Steps), "tool_calls": task.Plan.Calls})
	a.mu.Lock()
	defer a.mu.Unlock()
	buffer := &tools.MemAuditor{}
	var committedNotifications []Notification
	work := func(svc agentServices, write func(string, any) error) error {
		publisher := &lessonPublisher{task: task}
		publisher.publish = func() (any, error) {
			if err := write(fmt.Sprintf("report/%d", id), task.Result); err != nil {
				return nil, err
			}
			if err := write(fmt.Sprintf("snapshot/%d", id), task.State); err != nil {
				return nil, err
			}
			body := fmt.Sprintf("首次正确率 %.0f%%", task.Result.Report.FirstTryRate*100)
			if len(task.Replies) > 0 {
				body += "。" + strings.Join(task.Replies, " ")
			}
			if task.Next != nil && task.Next.RequestedRatio > 0 {
				switch task.Next.Status {
				case "planned":
					body += fmt.Sprintf(" 下一课安排新词%d个，优先巩固已学词。", task.Next.SelectedNewCount)
				case "already_planned":
					body += " 已有下一课计划保持原批次，本次建议未改动它。"
				case "no_words":
					body += " 当前没有可安排的学习词。"
				}
			}
			committedNotifications = append([]Notification(nil), a.notifications...)
			committedNotifications = append(committedNotifications, Notification{ID: a.nextNotifID + 1, UserID: sid, Type: "info", Title: "课报已生成", Body: body, CreatedAt: task.At})
			if err := write("notifications", committedNotifications); err != nil {
				return nil, err
			}
			return map[string]any{"lesson_id": id, "notification_id": a.nextNotifID + 1}, nil
		}
		svc.publisher = publisher
		reg := tools.New(buffer)
		if err := a.registerAgentTools(reg, svc); err != nil {
			return err
		}
		if err := a.registerLessonAgentTools(reg, svc); err != nil {
			return err
		}
		if !task.Settled {
			lesson, err := svc.scheduler.Lesson(id)
			if err != nil {
				return err
			}
			if err = validateLessonResult(lesson, sid, bid, task.Result); err != nil {
				return err
			}
			if err = svc.scheduler.SettleLesson(id, task.Result.Correct, task.At); err != nil {
				return err
			}
			task.Settled = true
		}
		if err := a.executeLessonPlan(ctx, reg, task); err != nil {
			return err
		}
		if err := write(fmt.Sprintf("agent_result/%d", id), map[string]any{"actions": task.Plan.Steps, "results": task.Done, "next": task.Next, "replies": task.Replies}); err != nil {
			return err
		}
		return write(fmt.Sprintf("settled/%d", id), lessonCommitRecord{Version: 1, StudentID: sid, BookID: bid, Fingerprint: task.Fingerprint})
	}
	if a.DB != nil {
		atomicOutcome := "outcome_uncertain"
		err = a.withAtomicWriteObserved(ctx, a.DB, func(tx *gorm.DB) error {
			var existing store.RuntimeRecord
			if e := tx.Where("key=?", fmt.Sprintf("settled/%d", id)).First(&existing).Error; e == nil {
				return fmt.Errorf("completion concurrently committed; retry read")
			} else if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			cfg, e := config.NewStore(store.NewConfigPersister(tx), nil)
			if e != nil {
				return e
			}
			svc := agentServices{configDB: tx, config: cfg, memory: memory.NewService(store.NewMemoryRepo(tx)), scheduler: scheduler.NewService(store.NewSchedulerRepo(tx), cfg, a.Clock), transactional: true}
			if e = work(svc, func(key string, value any) error { return writeLessonRuntime(tx, key, value) }); e != nil {
				return e
			}
			return persistToolAudits(tx, buffer.Snapshot(), false)
		}, func(timing store.AtomicConnectionTiming) { atomicOutcome = timing.Outcome })
		err = preserveAtomicOutcome(err, atomicOutcome)
	} else {
		err = work(agentServices{config: a.Cfg, memory: a.Mem, scheduler: a.Sched.ConfigWorkView(a.Cfg)}, func(string, any) error { return nil })
	}
	entries := buffer.Snapshot()
	recordAudit := func(entry tools.AuditEntry, outcome string) {
		a.ToolAudit.ToolCalled(entry)
		a.Log.InfoCtx(ctx, "tools", "tool_called", map[string]any{"task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "student_id": sid, "lesson_id": id, "tool": entry.Tool, "role": entry.ActorRole, "duration_ms": entry.DurationMs, "transaction_outcome": outcome, "ok": entry.OK})
	}
	if err != nil {
		if errors.Is(err, errAtomicOutcomeUncertain) {
			a.Log.ErrorCtx(ctx, "agent", "lesson_settlement_outcome_uncertain", map[string]any{"lesson_id": id, "student_id": sid, "task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "transaction_outcome": "outcome_uncertain"})
			return err
		}
		if a.DB != nil {
			for i := range entries {
				entries[i].OK = false
				entries[i].Result = "rolled_back_" + entries[i].Result
			}
			if e := persistToolAudits(a.DB, entries, false); e != nil {
				a.Log.ErrorCtx(ctx, "tools", "audit_persist_failed", map[string]any{"lesson_id": id})
			}
		}
		for _, entry := range entries {
			outcome := "partial"
			if a.DB != nil {
				outcome = "rolled_back"
			}
			recordAudit(entry, outcome)
		}
		a.Log.ErrorCtx(ctx, "agent", "lesson_settlement_failed", map[string]any{"lesson_id": id, "student_id": sid, "task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "storage_mode": map[bool]string{true: "sqlite", false: "memory"}[a.DB != nil], "error_code": "completion_write_failed"})
		return err
	}
	for _, entry := range entries {
		recordAudit(entry, "committed")
	}
	if committedNotifications != nil {
		a.notifications = committedNotifications
		a.nextNotifID++
	}
	if a.DB == nil {
		if a.reports == nil {
			a.reports = map[int64]*classroom.Result{}
		}
		if a.lessonStates == nil {
			a.lessonStates = map[int64]*classroom.State{}
		}
		a.reports[id] = task.Result
		copyState := task.State
		a.lessonStates[id] = &copyState
	} else {
		a.reloadCommittedConfig(id)
	}
	a.Log.InfoCtx(ctx, "scheduler", "lesson_settled", map[string]any{"lesson_id": id, "student_id": sid, "task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "transaction_outcome": "committed"})
	return nil
}

func writeLessonRuntime(db *gorm.DB, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&store.RuntimeRecord{Key: key, Value: string(raw)}).Error
}

func (a *App) reloadCommittedConfig(id int64) {
	if err := a.Cfg.Reload(); err != nil {
		a.Log.Error("config", "config_reload_failed", map[string]any{"lesson_id": id, "committed": true, "refresh_pending": true})
	}
}

func (a *App) executeLessonPlan(ctx context.Context, reg *tools.Registry, task *lessonCompletion) error {
	caller := tools.Caller{Role: tools.RoleSystemAgent, ID: "system_agent"}
	call := func(name string, args any) (any, error) { return reg.Call(ctx, caller, name, lessonToolArgs(args)) }
	for _, step := range task.Plan.Steps {
		if _, done := task.Done[step.Index]; done {
			continue
		}
		var result any
		var err error
		toolName := ""
		switch step.Kind {
		case "adjust_words_down":
			if task.Target == nil {
				current, e := call("config.get", map[string]any{"key": "wordsPerLesson"})
				if e != nil {
					return e
				}
				value, ok := current.(int)
				if !ok {
					return fmt.Errorf("invalid config result")
				}
				target := max50(value + step.Delta)
				if target > 100 {
					target = 100
				}
				task.Target = &target
			}
			toolName = "config.set_student"
			result, err = call(toolName, map[string]any{"key": "wordsPerLesson", "value": *task.Target, "reason": "课报评估：首答率低"})
		case "memory_append":
			toolName = "memory.append"
			content := step.Content
			if task.Target != nil {
				content = strings.ReplaceAll(content, "已降量", fmt.Sprintf("后续每课词量%d", *task.Target))
			}
			result, err = call(toolName, map[string]any{"scope": "task_memory", "kind": step.MemoryKind, "content": content, "importance": step.Importance})
		case "adjust_new_ratio":
			result = map[string]any{"status": "compiled_into_plan", "max_new_ratio": task.Plan.Policy.MaxNewRatio}
		case "plan_next":
			toolName = "scheduler.plan_next_lesson"
			result, err = call(toolName, map[string]any{})
		case "reply_comfort":
			toolName = "report.compose_reply"
			result, err = call(toolName, map[string]any{})
		case "report_push":
			toolName = "report.publish"
			result, err = call(toolName, map[string]any{})
		default:
			return fmt.Errorf("unsupported_action: %s", step.Kind)
		}
		if err != nil {
			a.Log.ErrorCtx(ctx, "agent", "agent_action_failed", map[string]any{"lesson_id": task.LessonID, "student_id": task.StudentID, "task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "index": step.Index, "kind": step.Kind, "tool": toolName, "error_code": "tool_call_failed"})
			return err
		}
		task.Done[step.Index] = result
		a.Log.InfoCtx(ctx, "agent", "agent_action_completed", map[string]any{"lesson_id": task.LessonID, "student_id": task.StudentID, "task_id": ctx.Value(agentTaskKey{}).(*agentTask).taskID, "index": step.Index, "kind": step.Kind, "tool": toolName, "transaction_outcome": "pending"})
	}
	return nil
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"enwords/internal/agent"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/tools"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"time"
)

// RunOverdue scans persisted lessons; the production timer supplies its
// cadence. No network or model call is made while holding classroom locks.
func (a *App) RunOverdue(ctx context.Context) error {
	lessons, err := a.Sched.AllLessons()
	if err != nil {
		return err
	}
	var failures []error
	for _, l := range lessons {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.Status != "scheduled" || l.FinishedAt != nil || !a.Clock.Now().After(l.OpenAt.Add(time.Duration(a.Cfg.Int(l.StudentID, "overdueHours"))*time.Hour)) {
			continue
		}
		if err := a.runOverdueLesson(ctx, l); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (a *App) runOverdueLesson(ctx context.Context, l *scheduler.Lesson) error {
	lease, err := a.generations.acquire(ctx, l.ID)
	if err != nil {
		return err
	}
	defer lease.Release()
	effect, err := lease.Token().Permit()
	if err != nil {
		return err
	}
	defer effect.Release()
	ctx = withGenerationLease(lease.Context(), lease)
	// Hub -> Room -> agentWriteMu: never reverse the Room/tool lock order.
	a.Rooms.mu.Lock()
	defer a.Rooms.mu.Unlock()
	r := a.Rooms.rooms[l.ID]
	if r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.retired {
			return nil
		}
	}
	ctx, err = a.newAgentTaskContext(ctx, l.StudentID, l.BookID, l.ID, "cron_overdue", tools.RoleSystemAgent)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("overdue_%x", sha256.Sum256([]byte(fmt.Sprintf("%d/%s/v1", l.ID, l.OpenAt.UTC().Format(time.RFC3339Nano)))))
	ctx = obs.WithTrace(ctx, key)
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	now := a.Clock.Now()
	// A live or persisted classroom receives the full continuation window.
	started := r != nil
	if !started && a.DB != nil {
		var state classroom.State
		err := a.readRuntimeError(fmt.Sprintf("snapshot/%d", l.ID), &state)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		started = err == nil
	} else if !started {
		a.mu.Lock()
		started = a.lessonStates[l.ID] != nil
		a.mu.Unlock()
	}
	if started && !now.After(l.OpenAt.Add(48*time.Hour)) {
		return nil
	}
	a.Log.InfoCtx(ctx, "scheduler", "overdue_detected", map[string]any{"task_id": key, "lesson_id": l.ID})
	task := store.OverdueTask{TaskID: key, LessonID: l.ID, StudentID: l.StudentID, BookID: l.BookID, OriginalOpenAt: l.OpenAt, Version: 1, Status: "prepared", CreatedAt: now}
	prepare := func(s *scheduler.Service) error {
		current, err := s.Lesson(l.ID)
		if err != nil {
			return err
		}
		if current.Status != "scheduled" || current.FinishedAt != nil || !current.OpenAt.Equal(l.OpenAt) {
			task.Status = "stale"
			return nil
		}
		rows, err := s.Lessons(l.StudentID, l.BookID)
		if err != nil {
			return err
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].OpenAt.Equal(rows[j].OpenAt) {
				return rows[i].ID > rows[j].ID
			}
			return rows[i].OpenAt.After(rows[j].OpenAt)
		})
		count := 1
		for _, past := range rows {
			if past.ID == l.ID || past.OpenAt.After(l.OpenAt) {
				continue
			}
			if past.FinishedAt != nil {
				break
			}
			if past.Status == "expired" {
				count++
			}
		}
		plan := a.System.PlanOverdue(l.OpenAt, now, count)
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		task.PlanJSON = string(raw)
		task.PlannedAt = now
		return nil
	}
	refresh := func(s *scheduler.Service) error {
		if task.Status != "prepared" {
			return nil
		}
		if task.PlanJSON == "" {
			return prepare(s)
		}
		prior, err := a.decodeOverduePlan(task)
		if err != nil {
			return err
		}
		if prior.Target.After(now) {
			return nil
		}
		var history []map[string]any
		if task.PlanHistoryJSON != "" {
			if err := json.Unmarshal([]byte(task.PlanHistoryJSON), &history); err != nil {
				return err
			}
		}
		history = append(history, map[string]any{"version": task.Version, "plan": prior, "reason": "target_elapsed", "superseded_at": now})
		raw, err := json.Marshal(history)
		if err != nil {
			return err
		}
		task.PlanHistoryJSON = string(raw)
		task.Version++
		return prepare(s)
	}
	if a.DB != nil {
		err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&task).Error; err != nil {
				return err
			}
			if err := tx.Where("task_id=?", key).First(&task).Error; err != nil {
				return err
			}
			cfg, err := config.NewStore(store.NewConfigPersister(tx), nil)
			if err != nil {
				return err
			}
			if err := a.feedbackActive(ctx, l.StudentID, tx); err != nil {
				return err
			}
			if err := refresh(scheduler.NewService(store.NewSchedulerRepo(tx), cfg, a.Clock)); err != nil {
				return err
			}
			return tx.Save(&task).Error
		})
		if err != nil {
			return err
		}
	} else {
		a.mu.Lock()
		if a.overdueTasks == nil {
			a.overdueTasks = map[string]store.OverdueTask{}
		}
		existing, ok := a.overdueTasks[key]
		a.mu.Unlock()
		if ok {
			task = existing
		}
		if err := refresh(a.Sched); err != nil {
			return err
		}
		a.mu.Lock()
		a.overdueTasks[key] = task
		a.mu.Unlock()
	}
	if task.Status != "prepared" {
		return nil
	}
	plan, err := a.decodeOverduePlan(task)
	if err != nil {
		return err
	}
	a.Log.InfoCtx(ctx, "agent", "agent_decision", map[string]any{"task_id": key, "trigger": "cron_overdue", "lesson_id": l.ID, "consecutive": plan.Consecutive})
	buffer := &tools.MemAuditor{}
	var newID int64
	execute := func(s *scheduler.Service, notice func(string) error) error {
		books, err := s.StudentBooks(l.StudentID)
		if err != nil {
			return err
		}
		enrolled := false
		for _, bid := range books {
			if bid == l.BookID {
				enrolled = true
			}
		}
		if !enrolled {
			return fmt.Errorf("overdue book not enrolled")
		}
		w := &overdueWork{task: task, plan: plan, sched: s, notice: notice}
		bound := context.WithValue(ctx, overdueWorkKey{}, w)
		reg := tools.New(buffer)
		if err := a.registerOverdueTools(reg); err != nil {
			return err
		}
		for _, name := range []string{"scheduler.replan_overdue", "notification.publish_overdue"} {
			if _, err := reg.Call(bound, tools.Caller{Role: tools.RoleSystemAgent, ID: "system_agent"}, name, json.RawMessage(`{"reason":"scheduled overdue recovery"}`)); err != nil {
				return err
			}
		}
		newID = w.next.ID
		return ctx.Err()
	}
	if a.DB != nil {
		err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&store.OverdueTask{}).Where("task_id=? AND status=? AND version=?", key, "prepared", task.Version).UpdateColumn("version", gorm.Expr("version"))
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return nil
			}
			if err := a.feedbackActive(ctx, l.StudentID, tx); err != nil {
				return err
			}
			cfg, err := config.NewStore(store.NewConfigPersister(tx), nil)
			if err != nil {
				return err
			}
			err = execute(scheduler.NewService(store.NewSchedulerRepo(tx), cfg, a.Clock), func(body string) error {
				return tx.Create(&store.FeedbackNotice{TaskID: key, Recipient: l.StudentID, Kind: "overdue", Title: "叮当words 课程重排", Body: body, CreatedAt: now, UpdatedAt: now}).Error
			})
			if err != nil {
				return err
			}
			if err := persistToolAudits(tx, buffer.Snapshot(), false); err != nil {
				return err
			}
			return tx.Model(&store.OverdueTask{}).Where("task_id=?", key).Updates(map[string]any{"status": "completed", "new_lesson_id": newID, "completed_at": now}).Error
		})
	} else {
		var body string
		err = a.Sched.WithMemoryTransaction(a.Cfg, func(s *scheduler.Service) error {
			return execute(s, func(text string) error { body = text; return nil })
		})
		if err == nil {
			a.mu.Lock()
			m := a.feedbackMemoryLocked()
			m.NextNotice++
			m.Notices = append(m.Notices, store.FeedbackNotice{ID: m.NextNotice, TaskID: key, Recipient: l.StudentID, Kind: "overdue", Title: "叮当words 课程重排", Body: body, CreatedAt: now, UpdatedAt: now})
			task.Status = "completed"
			task.NewLessonID = newID
			task.CompletedAt = &now
			a.overdueTasks[key] = task
			a.mu.Unlock()
		}
	}
	if err != nil {
		entries := buffer.Snapshot()
		for i := range entries {
			entries[i].OK = false
			entries[i].Result = "rolled_back_" + entries[i].Result
			a.ToolAudit.ToolCalled(entries[i])
		}
		if a.DB != nil {
			if auditErr := persistToolAudits(a.DB, entries, false); auditErr != nil {
				a.Log.ErrorCtx(ctx, "tools", "audit_persist_failed", map[string]any{"task_id": key})
			}
		}
		event := "overdue_replan_failed"
		if errors.Is(err, scheduler.ErrOverdueConflict) {
			event = "overdue_conflict"
		}
		a.Log.ErrorCtx(ctx, "scheduler", event, map[string]any{"task_id": key, "lesson_id": l.ID})
		return err
	}
	for _, entry := range buffer.Snapshot() {
		a.ToolAudit.ToolCalled(entry)
	}
	if newID == 0 {
		return nil
	}
	if r != nil {
		r.retired = true
		r.cancelJudgmentLocked()
		r.deliveryReady = false
		if client, ok := r.currentCli.(interface{ Close() error }); ok {
			_ = client.Close()
		}
		delete(a.Rooms.rooms, l.ID)
	}
	a.Log.InfoCtx(ctx, "scheduler", "overdue_replanned", map[string]any{"task_id": key, "lesson_id": l.ID, "new_lesson_id": newID, "consecutive": plan.Consecutive})
	return nil
}

func (a *App) decodeOverduePlan(task store.OverdueTask) (agent.OverduePlan, error) {
	var plan agent.OverduePlan
	bad := func() (agent.OverduePlan, error) { return plan, fmt.Errorf("invalid overdue plan") }
	if task.Version < 1 || task.PlannedAt.IsZero() || strictToolArgs(json.RawMessage(task.PlanJSON), &plan) != nil {
		return bad()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(task.PlanJSON), &fields) != nil || len(fields) != 4 {
		return bad()
	}
	for _, key := range []string{"care", "suggest_pause"} {
		if string(fields[key]) != "true" && string(fields[key]) != "false" {
			return bad()
		}
	}
	if plan.Consecutive < 1 || plan.Target.IsZero() {
		return bad()
	}
	want := a.System.PlanOverdue(task.OriginalOpenAt, task.PlannedAt, plan.Consecutive)
	if !plan.Target.Equal(want.Target) || plan.Care != want.Care || plan.SuggestPause != want.SuggestPause {
		return bad()
	}
	return plan, nil
}

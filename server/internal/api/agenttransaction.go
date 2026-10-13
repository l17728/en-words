package api

import (
	"context"
	"encoding/json"
	"fmt"

	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/store"
	"enwords/internal/tools"
	"gorm.io/gorm"
)

// The write committed; callers must not describe this as a failed save or
// automatically retry a relative adjustment.
type committedToolError struct{ cause error }

func (e *committedToolError) Error() string { return "tool committed; configuration refresh pending" }
func (e *committedToolError) Unwrap() error { return e.cause }

// callAgentTool gives each persistent mutation and its audit one unit of work.
// A future multi-action executor must bind the same definitions to its outer tx.
func (a *App) callAgentTool(ctx context.Context, name string, args any) (any, error) {
	task, ok := ctx.Value(agentTaskKey{}).(*agentTask)
	if !ok || task == nil || task.app != a {
		return nil, fmt.Errorf("trusted agent task required")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	caller := tools.Caller{Role: task.role, ID: string(task.role)}
	if a.DB == nil {
		result, err := a.Tools.Call(ctx, caller, name, raw)
		a.Log.InfoCtx(ctx, "tools", "tool_called", map[string]any{"task_id": task.taskID, "student_id": task.studentID, "lesson_id": task.lessonID, "tool": name, "transaction_outcome": "memory", "ok": err == nil})
		return result, err
	}
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	buffer := &tools.MemAuditor{}
	var result any
	err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cfg, e := config.NewStore(store.NewConfigPersister(tx), nil)
		if e != nil {
			return e
		}
		reg := tools.New(buffer)
		if e := a.registerAgentTools(reg, agentServices{config: cfg, memory: memory.NewService(store.NewMemoryRepo(tx)), transactional: true}); e != nil {
			return e
		}
		if e := a.registerTeacherTool(reg); e != nil {
			return e
		}
		result, e = reg.Call(ctx, caller, name, raw)
		if e != nil {
			return e
		}
		return persistToolAudits(tx, buffer.Snapshot(), false)
	})
	entries := buffer.Snapshot()
	outcome := "committed"
	if err != nil {
		outcome = "rolled_back"
		for i := range entries {
			entries[i].OK = false
			entries[i].Result = "rolled_back_" + entries[i].Result
		}
		// The failed transaction cannot retain its own audit. Record attempts
		// separately; if storage is still unavailable emit a bounded log event.
		if e := persistToolAudits(a.DB, entries, false); e != nil {
			a.Log.ErrorCtx(ctx, "tools", "audit_persist_failed", map[string]any{"task_id": task.taskID, "tool": name})
		}
	}
	for _, entry := range entries {
		a.ToolAudit.ToolCalled(entry)
	}
	a.Log.InfoCtx(ctx, "tools", "tool_called", map[string]any{"task_id": task.taskID, "student_id": task.studentID, "lesson_id": task.lessonID, "tool": name, "transaction_outcome": outcome, "ok": err == nil})
	if err != nil {
		return nil, err
	}
	if name == "config.set_student" {
		if err := a.Cfg.Reload(); err != nil {
			a.Log.ErrorCtx(ctx, "config", "config_reload_failed", map[string]any{"task_id": task.taskID, "committed": true})
			return result, &committedToolError{cause: err}
		}
	}
	return result, nil
}

func persistToolAudits(db *gorm.DB, entries []tools.AuditEntry, rolledBack bool) error {
	for _, entry := range entries {
		if rolledBack {
			entry.OK = false
			entry.Result = "rolled_back_" + entry.Result
		}
		row := store.AuditLog{TS: entry.TS, ActorID: entry.ActorID, ActorRole: string(entry.ActorRole), Tool: entry.Tool, Risk: string(entry.Risk), OK: entry.OK, Result: entry.Result, Args: entry.Args, DurationMs: entry.DurationMs, TraceID: entry.TraceID}
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

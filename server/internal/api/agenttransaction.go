package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/scheduler"
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
	timings := map[string]any{"lock_wait_ms": int64(0), "transaction_ms": int64(0), "begin_wait_ms": int64(0), "config_load_ms": int64(0), "audit_write_ms": int64(0), "finalize_ms": int64(0), "rollback_audit_write_ms": int64(0), "storage_callback_ms": int64(0), "storage_commit_ms": int64(0), "storage_rollback_ms": int64(0), "storage_raw_cleanup_ms": int64(0), "storage_connection_release_ms": int64(0), "storage_total_ms": int64(0), "storage_outcome": "not_started"}
	var storageTiming store.AtomicConnectionTiming
	storageTimingObserved := false
	defer func() {
		if !storageTimingObserved {
			return
		}
		// Emit after WithAtomicWrite has returned (or unwound) and the write lock
		// has been released; diagnostics do not extend the storage timing spans.
		safeAtomicTimingLog(func() {
			a.Log.InfoCtx(ctx, "store", "atomic_write_timing", map[string]any{
				"task_id": task.taskID, "student_id": task.studentID, "lesson_id": task.lessonID, "tool": name,
				"transaction_outcome": storageTiming.Outcome,
				"operation_ms":        storageTiming.Callback.DurationMS, "operation_state": storageTiming.Callback.State,
				"commit_ms": storageTiming.Commit.DurationMS, "commit_state": storageTiming.Commit.State,
				"rollback_ms": storageTiming.Rollback.DurationMS, "rollback_state": storageTiming.Rollback.State,
				"raw_cleanup_ms": storageTiming.RawCleanup.DurationMS, "raw_cleanup_state": storageTiming.RawCleanup.State,
				"connection_release_ms": storageTiming.ConnectionRelease.DurationMS, "connection_release_state": storageTiming.ConnectionRelease.State,
				"total_ms": storageTiming.Total.DurationMS,
			})
		})
	}()
	logCall := func(outcome string, successful bool) {
		payload := map[string]any{"task_id": task.taskID, "student_id": task.studentID, "lesson_id": task.lessonID, "tool": name, "transaction_outcome": outcome, "ok": successful}
		for key, value := range timings {
			payload[key] = value
		}
		a.Log.InfoCtx(ctx, "tools", "tool_called", payload)
	}
	if a.DB == nil {
		result, err := a.Tools.Call(ctx, caller, name, raw)
		logCall("memory", err == nil)
		return result, err
	}
	lockStarted := time.Now()
	a.agentWriteMu.Lock()
	timings["lock_wait_ms"] = time.Since(lockStarted).Milliseconds()
	defer a.agentWriteMu.Unlock()
	buffer := &tools.MemAuditor{}
	var result any
	transactionStarted := time.Now()
	var callbackFinished time.Time
	err = store.WithAtomicWriteObserved(ctx, a.DB, func(tx *gorm.DB) error {
		timings["begin_wait_ms"] = time.Since(transactionStarted).Milliseconds()
		defer func() { callbackFinished = time.Now() }()
		configStarted := time.Now()
		cfg, e := config.NewStore(store.NewConfigPersister(tx), nil)
		timings["config_load_ms"] = time.Since(configStarted).Milliseconds()
		if e != nil {
			return e
		}
		reg := tools.New(buffer)
		if e := a.registerAgentTools(reg, agentServices{configDB: tx, config: cfg, memory: memory.NewService(store.NewMemoryRepo(tx)), scheduler: scheduler.NewService(store.NewSchedulerRepo(tx), cfg, a.Clock), report: func(id int64) (*classroom.Result, error) { return capabilityReportTx(tx, id) }, transactional: true}); e != nil {
			return e
		}
		if e := a.registerTeacherTool(reg); e != nil {
			return e
		}
		result, e = reg.Call(ctx, caller, name, raw)
		if e != nil {
			return e
		}
		auditStarted := time.Now()
		e = persistToolAudits(tx, buffer.Snapshot(), false)
		timings["audit_write_ms"] = time.Since(auditStarted).Milliseconds()
		return e
	}, func(storage store.AtomicConnectionTiming) {
		storageTiming = storage
		storageTimingObserved = true
		timings["storage_outcome"] = storage.Outcome
		timings["storage_callback_ms"] = storage.Callback.DurationMS
		timings["storage_commit_ms"] = storage.Commit.DurationMS
		timings["storage_rollback_ms"] = storage.Rollback.DurationMS
		timings["storage_raw_cleanup_ms"] = storage.RawCleanup.DurationMS
		timings["storage_connection_release_ms"] = storage.ConnectionRelease.DurationMS
		timings["storage_total_ms"] = storage.Total.DurationMS
		timings["storage_callback_state"] = storage.Callback.State
		timings["storage_commit_state"] = storage.Commit.State
		timings["storage_rollback_state"] = storage.Rollback.State
		timings["storage_raw_cleanup_state"] = storage.RawCleanup.State
		timings["storage_connection_release_state"] = storage.ConnectionRelease.State
	})
	transactionFinished := time.Now()
	timings["transaction_ms"] = transactionFinished.Sub(transactionStarted).Milliseconds()
	if callbackFinished.IsZero() {
		timings["begin_wait_ms"] = timings["transaction_ms"]
	} else {
		timings["finalize_ms"] = transactionFinished.Sub(callbackFinished).Milliseconds()
	}
	entries := buffer.Snapshot()
	outcome := "committed"
	if err != nil {
		outcome = "failed_cleaned"
		if storageOutcome, ok := timings["storage_outcome"].(string); ok && storageOutcome != "" {
			outcome = storageOutcome
		}
		resultPrefix := "failed_cleaned_"
		if outcome == "outcome_uncertain" {
			resultPrefix = "outcome_uncertain_"
		}
		for i := range entries {
			entries[i].OK = false
			entries[i].Result = resultPrefix + entries[i].Result
		}
		// A known-clean transaction can safely persist failed-attempt audits. If the
		// COMMIT outcome is uncertain, retrying the same audit could duplicate a
		// record that was committed with the operation, so keep it in the bounded log.
		if outcome == "failed_cleaned" {
			rollbackAuditStarted := time.Now()
			if e := persistToolAudits(a.DB, entries, false); e != nil {
				a.Log.ErrorCtx(ctx, "tools", "audit_persist_failed", map[string]any{"task_id": task.taskID, "tool": name})
			}
			timings["rollback_audit_write_ms"] = time.Since(rollbackAuditStarted).Milliseconds()
		} else if outcome == "outcome_uncertain" {
			a.Log.ErrorCtx(ctx, "tools", "audit_persist_skipped_outcome_uncertain", map[string]any{"task_id": task.taskID, "tool": name})
		}
	}
	for _, entry := range entries {
		a.ToolAudit.ToolCalled(entry)
	}
	logCall(outcome, err == nil)
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

func safeAtomicTimingLog(write func()) {
	defer func() { _ = recover() }()
	write()
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

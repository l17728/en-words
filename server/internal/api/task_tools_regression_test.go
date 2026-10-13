package api

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/obs"
	"enwords/internal/store"
	"enwords/internal/tools"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func taskFixtureContext(t *testing.T, a *App, sid int64) context.Context {
	t.Helper()
	ctx, err := a.newAgentTaskContext(context.Background(), sid, 0, 0, "feedback_submitted", tools.RoleSystemAgent)
	require.NoError(t, err)
	return ctx
}

func TestAgentTaskIdentityAndResourceBoundaries(t *testing.T) {
	app, _ := newTestApp(t)
	other, _ := newTestApp(t)
	args := json.RawMessage(`{"scope":"student_profile","kind":"obs","content":"private fixture","importance":3}`)
	caller := tools.Caller{Role: tools.RoleSystemAgent, ID: "fixture"}
	_, err := app.Tools.Call(context.Background(), caller, "memory.append", args)
	require.Error(t, err)
	ctx := taskFixtureContext(t, app, 7)
	_, err = other.Tools.Call(ctx, caller, "memory.append", args)
	require.Error(t, err)
	_, err = app.Tools.Call(ctx, tools.Caller{Role: tools.RoleTeacherAgent}, "memory.append", args)
	require.Error(t, err)
	for _, extra := range []string{`"student_id":8`, `"book_id":9`, `"source_lesson_id":42`, `"owner_role":"teacher_agent"`, `"unexpected":"value"`} {
		raw := json.RawMessage(`{"scope":"student_profile","kind":"obs","content":"private fixture","importance":3,` + extra + `}`)
		_, err = app.Tools.Call(taskFixtureContext(t, app, 7), caller, "memory.append", raw)
		require.Error(t, err)
	}
	got, err := app.Mem.Recall(memory.OwnerSystem, 7, 0, "", "", 10)
	require.NoError(t, err)
	require.Empty(t, got)
	entries := app.ToolAudit.Snapshot()
	require.Len(t, entries, 7)
	for _, entry := range entries {
		require.False(t, entry.OK)
		require.NotContains(t, entry.Args, "private fixture")
	}
}

func TestAgentTaskLessonOwnershipAndMemoryOwnerIsolation(t *testing.T) {
	app, _, lid, room := setupRoom(t)
	sid, bid := room.studentID, room.bookID
	for _, tc := range []struct {
		sid, bid, lid int64
		trigger       string
		role          tools.Role
	}{
		{sid + 1, bid, lid, "classroom_event", tools.RoleTeacherAgent},
		{sid, bid + 1, lid, "classroom_event", tools.RoleTeacherAgent},
		{sid, bid, 0, "classroom_event", tools.RoleTeacherAgent},
		{sid, bid, lid, "feedback_submitted", tools.RoleTeacherAgent},
		{sid, bid, 0, "lesson_finished", tools.RoleSystemAgent},
		{sid, bid + 1, 0, "feedback_submitted", tools.RoleSystemAgent},
	} {
		_, err := app.newAgentTaskContext(context.Background(), tc.sid, tc.bid, tc.lid, tc.trigger, tc.role)
		require.Error(t, err)
	}
	teacherCtx, err := app.newAgentTaskContext(context.Background(), sid, bid, lid, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	systemCtx := taskFixtureContext(t, app, sid)
	for _, tc := range []struct {
		ctx     context.Context
		content string
	}{{teacherCtx, "teacher private"}, {systemCtx, "system private"}} {
		_, err := app.callAgentTool(tc.ctx, "memory.append", map[string]any{"scope": "student_profile", "kind": "obs", "content": tc.content, "importance": 3})
		require.NoError(t, err)
		result, err := app.callAgentTool(tc.ctx, "memory.recall", map[string]any{"scope": "student_profile", "limit": 10})
		require.NoError(t, err)
		records := result.([]*memory.Memory)
		require.Len(t, records, 1)
		require.Equal(t, tc.content, records[0].Content)
		if tc.content == "teacher private" {
			require.Equal(t, lid, records[0].SourceLessonID)
		}
	}
}

type refreshFailurePersister struct {
	config.Persister
	fail bool
}

func (p *refreshFailurePersister) LoadGlobals() (map[string]json.RawMessage, error) {
	if p.fail {
		return nil, errors.New("fixture refresh unavailable")
	}
	return p.Persister.LoadGlobals()
}

func TestFeedbackCommittedConfigRefreshFailureDoesNotInviteRetry(t *testing.T) {
	app, sid, _, _, _ := durableSettlementFixture(t)
	persister := &refreshFailurePersister{Persister: store.NewConfigPersister(app.DB)}
	cfg, err := config.NewStore(persister, nil)
	require.NoError(t, err)
	app.Cfg = cfg
	before := cfg.Int(sid, "wordsPerLesson")
	require.NotEqual(t, 70, before)
	persister.fail = true
	rec := app.HandleFeedbackContext(obs.WithTrace(context.Background(), "commit-refresh-trace"), sid, "每课70个单词")
	require.Equal(t, "applied_refresh_pending", rec.Status)
	require.Contains(t, rec.Reply, "已保存")
	require.NotContains(t, rec.Reply, "未能保存")
	restored, err := config.NewStore(store.NewConfigPersister(app.DB), nil)
	require.NoError(t, err)
	require.Equal(t, 70, restored.Int(sid, "wordsPerLesson"))
	require.Equal(t, before, cfg.Int(sid, "wordsPerLesson"))
	var entries []store.AuditLog
	require.NoError(t, app.DB.Where("tool = ?", "config.set_student").Find(&entries).Error)
	require.Len(t, entries, 1)
	require.True(t, entries[0].OK)
	require.Equal(t, "commit-refresh-trace", entries[0].TraceID)
}

func TestAgentTaskBudgetSharedAcrossDifferentTools(t *testing.T) {
	app, _ := newTestApp(t)
	ctx := taskFixtureContext(t, app, 7)
	for i := 0; i < 8; i++ {
		var err error
		if i%2 == 0 {
			_, err = app.callAgentTool(ctx, "config.get", map[string]any{"key": "wordsPerLesson"})
		} else {
			_, err = app.callAgentTool(ctx, "memory.recall", map[string]any{"scope": "student_profile"})
		}
		require.NoError(t, err)
	}
	_, err := app.callAgentTool(ctx, "memory.append", map[string]any{"scope": "student_profile", "kind": "obs", "content": "must not commit", "importance": 3})
	require.ErrorIs(t, err, tools.ErrBudgetExceeded)
	records, err := app.Mem.Recall(memory.OwnerSystem, 7, 0, "", "", 10)
	require.NoError(t, err)
	require.Empty(t, records)
	require.Equal(t, "budget_denied", app.ToolAudit.Snapshot()[8].Result)
}

func TestPersistentAgentToolAuditFailureRollsBackMutation(t *testing.T) {
	for _, name := range []string{"config.set_student", "memory.append"} {
		t.Run(name, func(t *testing.T) {
			app, sid, _, _, _ := durableSettlementFixture(t)
			before := app.Cfg.Int(sid, "wordsPerLesson")
			var log bytes.Buffer
			app.SetLogger(obs.NewWithSink(&log))
			require.NoError(t, app.DB.Exec(`CREATE TRIGGER fixture_audit_fail BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END`).Error)
			require.NotEqual(t, 70, before, "rollback fixture must attempt a real change")
			args := map[string]any{"key": "wordsPerLesson", "value": 70, "reason": "private task reason"}
			if name == "memory.append" {
				args = map[string]any{"scope": "student_profile", "kind": "obs", "content": "private task content", "importance": 3}
			}
			_, err := app.callAgentTool(taskFixtureContext(t, app, sid), name, args)
			require.Error(t, err)
			restored, err := config.NewStore(store.NewConfigPersister(app.DB), nil)
			require.NoError(t, err)
			require.Equal(t, before, restored.Int(sid, "wordsPerLesson"))
			require.Equal(t, before, app.Cfg.Int(sid, "wordsPerLesson"))
			rows, err := store.NewMemoryRepo(app.DB).List(memory.OwnerSystem, sid, 0)
			require.NoError(t, err)
			require.Empty(t, rows)
			entries := app.ToolAudit.Snapshot()
			require.NotEmpty(t, entries)
			require.False(t, entries[len(entries)-1].OK)
			raw, err := json.Marshal(entries)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private task")
			require.NotContains(t, log.String(), "private task")
			require.Contains(t, log.String(), "audit_persist_failed")
			require.NoError(t, app.DB.Exec(`DROP TRIGGER fixture_audit_fail`).Error)
			_, err = app.callAgentTool(taskFixtureContext(t, app, sid), name, args)
			require.NoError(t, err)
			if name == "config.set_student" {
				require.Equal(t, 70, app.Cfg.Int(sid, "wordsPerLesson"))
			} else {
				committed, err := store.NewMemoryRepo(app.DB).List(memory.OwnerSystem, sid, 0)
				require.NoError(t, err)
				require.Len(t, committed, 1)
			}
			var auditCount int64
			require.NoError(t, app.DB.Model(&store.AuditLog{}).Where("tool=? AND ok=?", name, true).Count(&auditCount).Error)
			require.EqualValues(t, 1, auditCount)
		})
	}
}

func TestFeedbackParentTraceAndCancellation(t *testing.T) {
	app, _ := newTestApp(t)
	parent := obs.WithTrace(context.Background(), "feedback-parent-trace")
	rec := app.HandleFeedbackContext(parent, 7, "每课70个单词")
	require.Equal(t, "applied", rec.Status)
	require.Equal(t, 70, app.Cfg.Int(7, "wordsPerLesson"))
	entries := app.ToolAudit.Snapshot()
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		require.Equal(t, "feedback-parent-trace", entry.TraceID)
	}
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[persistent], func(t *testing.T) {
			a, _ := newTestApp(t)
			sid := int64(7)
			if persistent {
				a, sid, _, _, _ = durableSettlementFixture(t)
			}
			before := a.Cfg.Int(sid, "wordsPerLesson")
			require.NotEqual(t, 70, before, "cancellation fixture must attempt a real change")
			canceled, cancel := context.WithCancel(parent)
			cancel()
			result := a.HandleFeedbackContext(canceled, sid, "每课70个单词")
			require.Equal(t, "failed", result.Status)
			require.Equal(t, before, a.Cfg.Int(sid, "wordsPerLesson"))
			rows, err := a.Mem.Recall(memory.OwnerSystem, sid, 0, "", "", 10)
			require.NoError(t, err)
			require.Empty(t, rows)
			if persistent {
				cfg, err := config.NewStore(store.NewConfigPersister(a.DB), nil)
				require.NoError(t, err)
				require.Equal(t, before, cfg.Int(sid, "wordsPerLesson"))
			}
		})
	}
}

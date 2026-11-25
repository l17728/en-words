package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"enwords/internal/store"
	"enwords/internal/tools"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTrustedToolExecutorUsesCallerTransactionForAudit(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "trusted-tool.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	app.DB = db

	ctx, err := app.newAgentTaskContext(context.Background(), room.studentID, room.bookID, lessonID, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	caller := tools.Caller{Role: tools.RoleTeacherAgent, ID: "forged-actor"}
	args := map[string]any{"text": "Try again.", "category": "nudge"}

	// A trusted tool call and its audit are visible inside, but disappear with,
	// the caller's transaction. The executor must never commit independently.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	executor, err := app.newTrustedToolExecutor(tx)
	require.NoError(t, err)
	result, err := executor.Call(ctx, caller, "classroom.say", args)
	require.NoError(t, err)
	require.Equal(t, 1, teacherSayCount([]Msg{result.(Msg)}, "nudge"))
	require.Len(t, executor.Audits(), 1)
	require.Equal(t, string(tools.RoleTeacherAgent), executor.Audits()[0].ActorID, "audit actor must be derived from trusted task identity")
	var inside int64
	require.NoError(t, tx.Model(&store.AuditLog{}).Count(&inside).Error)
	require.EqualValues(t, 1, inside)
	require.NoError(t, tx.Rollback().Error)
	var afterRollback int64
	require.NoError(t, db.Model(&store.AuditLog{}).Count(&afterRollback).Error)
	require.Zero(t, afterRollback)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	executor, err = app.newTrustedToolExecutor(tx)
	require.NoError(t, err)
	_, err = executor.Call(ctx, caller, "classroom.say", args)
	require.NoError(t, err)
	require.NoError(t, tx.Commit().Error)
	var committed int64
	require.NoError(t, db.Model(&store.AuditLog{}).Count(&committed).Error)
	require.EqualValues(t, 1, committed)
}

func TestTrustedToolExecutorRejectsUntrustedScopeAndMissingTransaction(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	_, err := app.newTrustedToolExecutor(nil)
	require.Error(t, err)

	db, err := store.Open(filepath.Join(t.TempDir(), "trusted-tool-scope.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	executor, err := app.newTrustedToolExecutor(tx)
	require.NoError(t, err)
	ctx, err := app.newAgentTaskContext(context.Background(), room.studentID, room.bookID, lessonID, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	_, err = executor.Call(ctx, tools.Caller{Role: tools.RoleSystemAgent, ID: "system_agent"}, "classroom.say", map[string]any{"text": "No", "category": "nudge"})
	require.Error(t, err)
	require.Empty(t, executor.Audits(), "a system task must be rejected before registry execution")
}

func TestDuplexCommitCanAtomicallyPersistTrustedToolAudit(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "trusted-tool-outbox.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	app.DB = db
	repo := store.NewDuplexRepo(db)
	require.NoError(t, repo.Migrate())
	initial, err := json.Marshal(room.durableSnapshot())
	require.NoError(t, err)
	require.NoError(t, repo.CreateCheckpoint(context.Background(), room.studentID, lessonID, initial))
	ctx, err := app.newAgentTaskContext(context.Background(), room.studentID, room.bookID, lessonID, "classroom_event", tools.RoleTeacherAgent)
	require.NoError(t, err)
	input := sha256.Sum256([]byte("repeat_read"))
	candidate := store.DuplexCandidate{
		DecisionID: "decision-trusted-tool", Epoch: "epoch-trusted-tool", UtteranceID: "utterance-trusted-tool",
		InputRevision: 1, InputFingerprint: hex.EncodeToString(input[:]), TextHash: hex.EncodeToString(input[:]),
		State: json.RawMessage(`{"phase":"wait_answer"}`), Effects: []json.RawMessage{json.RawMessage(`{"type":"teacher_say","text":"Try again."}`)},
	}
	applyCalls := 0
	apply := func(tx *gorm.DB) error {
		applyCalls++
		executor, e := app.newTrustedToolExecutor(tx)
		if e != nil {
			return e
		}
		_, e = executor.Call(ctx, tools.Caller{Role: tools.RoleTeacherAgent, ID: "teacher_agent"}, "classroom.say", map[string]any{"text": "Try again.", "category": "nudge"})
		if e != nil {
			return e
		}
		return nil
	}
	first, err := repo.Commit(context.Background(), room.studentID, lessonID, 0, candidate, apply)
	require.NoError(t, err)
	require.Equal(t, "committed", first.Status)
	second, err := repo.Commit(context.Background(), room.studentID, lessonID, 0, candidate, apply)
	require.NoError(t, err)
	require.Equal(t, first.DecisionID, second.DecisionID)
	require.Equal(t, 1, applyCalls, "committed replay must not execute teacher tools or audits again")
	var auditCount, outboxCount int64
	require.NoError(t, db.Model(&store.AuditLog{}).Count(&auditCount).Error)
	require.NoError(t, db.Model(&store.DuplexOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 1, auditCount)
	require.EqualValues(t, 1, outboxCount)
}

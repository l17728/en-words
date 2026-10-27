package api

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/store"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUncertainToolTransactionRetiresRoomWithoutFailureSnapshotOrAudit(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	beforeState := app.lessonState(lessonID)
	beforeAudits := len(app.ToolAudit.Snapshot())
	judgeCtx, cancelJudge := context.WithCancel(context.Background())
	defer cancelJudge()
	room.pendingJudge = &roomJudgment{cancel: cancelJudge}
	room.deliveryReady = true

	frames := room.uncertainToolTransactionLocked("classroom.play_tts")

	require.True(t, room.retired)
	require.False(t, room.deliveryReady)
	require.ErrorIs(t, judgeCtx.Err(), context.Canceled)
	require.Nil(t, room.pendingJudge)
	require.Len(t, frames, 1)
	require.Equal(t, "classroom_error", frames[0]["type"])
	require.Equal(t, "transaction_outcome_uncertain", frames[0]["code"])
	require.Equal(t, false, frames[0]["retryable"])
	require.Equal(t, beforeState, app.lessonState(lessonID), "uncertain commit must not write a failure snapshot")
	require.Len(t, app.ToolAudit.Snapshot(), beforeAudits, "uncertain commit must not append a failure audit")
}

func TestRetiredRoomRejectsPublicProgressionEntrypoints(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	before := room.machine.Snapshot()
	beforeState := app.lessonState(lessonID)
	beforeAudits := len(app.ToolAudit.Snapshot())
	room.mu.Lock()
	room.retired = true
	room.deliveryReady = false
	room.mu.Unlock()

	require.Empty(t, room.Resume())
	require.Empty(t, room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked}))
	require.Empty(t, room.FireTimeout())
	room.mu.Lock()
	frames := room.broadcastEffectsLocked(context.Background(), true)
	room.mu.Unlock()
	require.Empty(t, frames)

	require.Equal(t, before, room.machine.Snapshot(), "retired room must not advance its state machine")
	require.Equal(t, beforeState, app.lessonState(lessonID), "retired room must not persist a later snapshot")
	require.Len(t, app.ToolAudit.Snapshot(), beforeAudits, "retired room must not call or audit tools")
}

func TestDeferredCommitFailureStopsTeacherContinuationAndSnapshot(t *testing.T) {
	app, _, lessonID, room := setupRoom(t)
	db, err := store.Open(t.TempDir() + "/uncertain-commit.db")
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	app.DB = db
	require.NoError(t, db.Exec("CREATE TABLE tx_commit_parent (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE tx_commit_child (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES tx_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
	require.NoError(t, db.Exec("CREATE TRIGGER tx_force_deferred_commit_failure AFTER INSERT ON audit_logs BEGIN INSERT INTO tx_commit_child(parent_id) VALUES (999999); END").Error)
	var logOutput bytes.Buffer
	app.SetLogger(obs.NewWithSink(&logOutput))

	auditInsertReached := false
	outcome, commitErr := runObservedToolTransaction(db, context.Background(), func(tx *gorm.DB) error {
		row := store.AuditLog{Tool: "synthetic-commit-probe", Result: "test"}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		auditInsertReached = true
		return nil
	})
	require.True(t, auditInsertReached, "the callback and audit INSERT must succeed before COMMIT is attempted")
	require.Equal(t, toolTransactionOutcomeUncertain, outcome)
	require.Error(t, commitErr)
	require.True(t, strings.Contains(strings.ToLower(commitErr.Error()), "foreign key"), "expected the deferred foreign-key COMMIT error: %v", commitErr)

	beforeState := app.lessonState(lessonID)
	beforeMemoryAudits := len(app.ToolAudit.Snapshot())
	var snapshotsBefore []store.RuntimeRecord
	require.NoError(t, db.Where("key = ?", fmt.Sprintf("snapshot/%d", lessonID)).Find(&snapshotsBefore).Error)
	room.mu.Lock()
	room.teacherPending = "lesson_started"
	room.mu.Unlock()
	_, _, enterErr := app.Rooms.Enter(lessonID, room.studentID)

	require.True(t, room.retired)
	require.ErrorIs(t, enterErr, errToolOutcomeUncertain, "initial lifecycle uncertainty must reach the Enter caller")
	require.Equal(t, beforeState, app.lessonState(lessonID), "uncertain commit must not write a later classroom snapshot")
	require.Len(t, app.ToolAudit.Snapshot(), beforeMemoryAudits, "uncertain commit must not append a failure audit")
	var auditCount, childCount int64
	require.NoError(t, db.Model(&store.AuditLog{}).Count(&auditCount).Error)
	require.NoError(t, db.Table("tx_commit_child").Count(&childCount).Error)
	var snapshotsAfter []store.RuntimeRecord
	require.NoError(t, db.Where("key = ?", fmt.Sprintf("snapshot/%d", lessonID)).Find(&snapshotsAfter).Error)
	require.Zero(t, auditCount, "commit failure rollback should leave no tool audit row")
	require.Zero(t, childCount, "deferred violation must not commit")
	require.Equal(t, snapshotsBefore, snapshotsAfter, "uncertain commit must not write or replace the persisted lesson snapshot")
	require.Contains(t, logOutput.String(), `"transaction_outcome":"outcome_uncertain"`, "callAgentTool must log the uncertain outcome")
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPilotCreatedStateReadAndRestart(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	receipt, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, err)
	beforeRows := pilotCreatedReadRows(t, db, receipt.LessonID)
	var before struct {
		FirstEnteredAt *int64
	}
	require.NoError(t, db.Table("classroom_mode_reservations").Where("lesson_id=?", receipt.LessonID).Take(&before).Error)
	require.Nil(t, before.FirstEnteredAt)

	state, err := facade.LoadCreatedState(context.Background(), principal, receipt.LessonID)
	require.NoError(t, err)
	require.Equal(t, PilotCreatedState{LessonID: receipt.LessonID, StudentID: 7, BookID: 9, SchemaVersion: 2,
		Lifecycle: "created", Phase: "pending", CheckpointVersion: 1}, state)
	var after struct {
		FirstEnteredAt *int64
	}
	require.NoError(t, db.Table("classroom_mode_reservations").Where("lesson_id=?", receipt.LessonID).Take(&after).Error)
	require.Nil(t, after.FirstEnteredAt)
	require.Equal(t, beforeRows, pilotCreatedReadRows(t, db, receipt.LessonID))

	var files []struct{ Name, File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	var dbPath string
	for _, file := range files {
		if file.Name == "main" {
			dbPath = file.File
		}
	}
	require.NotEmpty(t, dbPath)
	other, err := Open(filepath.Clean(dbPath))
	require.NoError(t, err)
	pool, err := other.DB()
	require.NoError(t, err)
	defer pool.Close()
	restarted, err := newPilotStore(context.Background(), other)
	require.NoError(t, err)
	got, err := restarted.LoadCreatedState(context.Background(), principal, receipt.LessonID)
	require.NoError(t, err)
	require.Equal(t, state, got)
	require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", receipt.LessonID).Update("checkpoint_version", 2).Error)
	got, err = restarted.LoadCreatedState(context.Background(), principal, receipt.LessonID)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.CheckpointVersion)
}

func pilotCreatedReadRows(t *testing.T, db *gorm.DB, lessonID int64) map[string][]map[string]any {
	t.Helper()
	rows := map[string][]map[string]any{}
	for _, table := range []string{"classroom_scope_reservations", "classroom_mode_reservations", "classroom_pilot_states", "lessons", "classroom_transition_receipts", "classroom_answer_claims", "classroom_active_commands", "classroom_effect_outbox", "classroom_completion_jobs"} {
		var found []map[string]any
		query := db.Table(table)
		if table == "classroom_scope_reservations" {
			query = query.Where("student_id=7 AND book_id=9")
		} else if table == "lessons" {
			query = query.Where("id=?", lessonID)
		} else {
			query = query.Where("lesson_id=?", lessonID)
		}
		require.NoError(t, query.Find(&found).Error)
		rows[table] = found
	}
	return rows
}

func TestPilotCreatedStateReadRejectsWrongOwnerAndDisabledStudent(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	receipt, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, err)
	other, err := BindPilotSystemPrincipal(8, "other-task")
	require.NoError(t, err)
	_, err = facade.LoadCreatedState(context.Background(), other, receipt.LessonID)
	require.ErrorIs(t, err, errPilotCreationDenied)
	_, err = facade.LoadCreatedState(context.Background(), principal, receipt.LessonID+100)
	require.ErrorIs(t, err, errPilotCreationDenied)
	require.NoError(t, db.Model(&User{}).Where("id=7").Update("status", "disabled").Error)
	_, err = facade.LoadCreatedState(context.Background(), principal, receipt.LessonID)
	require.ErrorIs(t, err, errPilotCreationDenied)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = facade.LoadCreatedState(ctx, principal, receipt.LessonID)
	require.Error(t, err)
}

func TestPilotCreatedStateReadRejectsTamperingAndUnreadySchema(t *testing.T) {
	for _, tc := range []struct{ name, table, column, value string }{
		{"tampered checkpoint", "classroom_pilot_states", "state_json", "{}"},
		{"unsupported lifecycle", "classroom_pilot_states", "lifecycle", "active"},
		{"tampered plan", "classroom_pilot_states", "creation_plan_json", "{}"},
		{"tampered fingerprint", "classroom_pilot_states", "config_fingerprint", strings.Repeat("a", 64)},
		{"tampered reservation", "classroom_mode_reservations", "creation_fingerprint", strings.Repeat("a", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			receipt, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, err)
			require.NoError(t, db.Table(tc.table).Where("lesson_id=?", receipt.LessonID).Update(tc.column, tc.value).Error)
			_, err = facade.LoadCreatedState(context.Background(), principal, receipt.LessonID)
			require.ErrorIs(t, err, errPilotSchemaInvalid)
		})
	}
	t.Run("unexpected receipt", func(t *testing.T) {
		db, facade, principal, plan := pilotCreationFixtureDraft(t)
		receipt, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
		require.NoError(t, err)
		require.NoError(t, db.Exec("INSERT INTO classroom_transition_receipts(lesson_id,command_id,student_id,request_fingerprint,kind,from_version,to_version,logical_input_revision,decision_id,result_json,committed_at) VALUES(?, 'unexpected', 7, ?, 'lesson.start', 1, 2, 0, 'unexpected-decision', '{}', 0)", receipt.LessonID, strings.Repeat("b", 64)).Error)
		_, err = facade.LoadCreatedState(context.Background(), principal, receipt.LessonID)
		require.ErrorIs(t, err, errPilotSchemaInvalid)
	})
	t.Run("missing checkpoint", func(t *testing.T) {
		db, facade, principal, plan := pilotCreationFixtureDraft(t)
		receipt, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
		require.NoError(t, err)
		require.NoError(t, db.Exec("DELETE FROM classroom_pilot_states WHERE lesson_id=?", receipt.LessonID).Error)
		_, err = facade.LoadCreatedState(context.Background(), principal, receipt.LessonID)
		require.ErrorIs(t, err, errPilotSchemaInvalid)
	})
	t.Run("legacy lesson", func(t *testing.T) {
		db, facade, principal, _ := pilotCreationFixtureDraft(t)
		lesson := Lesson{StudentID: 7, BookID: 9, Status: "scheduled", BatchWordIDs: "[]"}
		require.NoError(t, db.Create(&lesson).Error)
		require.NoError(t, db.Exec("INSERT INTO classroom_scope_reservations(student_id,book_id,mode,schema_version,reserved_at) VALUES(7,9,'legacy',2,0)").Error)
		require.NoError(t, db.Exec("INSERT INTO classroom_mode_reservations(lesson_id,student_id,book_id,mode,schema_version,reserved_at) VALUES(?,7,9,'legacy',2,0)", lesson.ID).Error)
		_, err := facade.LoadCreatedState(context.Background(), principal, lesson.ID)
		require.ErrorIs(t, err, errPilotSchemaInvalid)
	})
	t.Run("unmigrated", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
		require.NoError(t, err)
		pool, err := db.DB()
		require.NoError(t, err)
		defer pool.Close()
		facade, err := newPilotStore(context.Background(), db)
		require.NoError(t, err)
		principal, err := BindPilotSystemPrincipal(7, "read-task")
		require.NoError(t, err)
		_, err = facade.LoadCreatedState(context.Background(), principal, 1)
		require.True(t, errors.Is(err, errPilotNotReady) || errors.Is(err, errPilotV2SchemaInvalid))
	})
}

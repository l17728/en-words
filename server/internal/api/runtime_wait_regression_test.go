package api

import (
	"bytes"
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSnapshotCanceledContextDoesNotSaveOrUpdateMemory(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cancel.db"))
	require.NoError(t, err)
	pool, _ := db.DB()
	defer pool.Close()
	app := NewApp(nil, db)
	var log bytes.Buffer
	app.SetLogger(obs.New(&log))
	lease, leaseCtx := testGenerationContext(t, app, 987)
	defer lease.Release()
	ctx, cancel := context.WithCancel(leaseCtx)
	cancel()
	require.False(t, app.saveLessonStateContext(ctx, 987, classroom.State{LessonID: 987}))
	var count int64
	require.NoError(t, db.Model(&store.RuntimeRecord{}).Where("key=?", "snapshot/987").Count(&count).Error)
	require.Zero(t, count)
	require.Nil(t, app.lessonStates[987])
	require.Contains(t, log.String(), "snapshot_failed")
	require.NotContains(t, log.String(), "snapshot_saved")
}

func TestSnapshotCancellationReleasesPoolWaitBeforePoolOwner(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wait.db"))
	require.NoError(t, err)
	pool, _ := db.DB()
	defer pool.Close()
	app := NewApp(nil, db)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	lease, leaseCtx := testGenerationContext(t, app, 987)
	defer lease.Release()
	ctx, cancel := context.WithCancel(leaseCtx)
	done := make(chan struct{})
	go func() { _ = app.saveLessonStateContext(ctx, 987, classroom.State{LessonID: 987}); close(done) }()
	deadline := time.Now().Add(time.Second)
	for pool.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	exited := false
	select {
	case <-done:
		exited = true
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Rollback().Error)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot goroutine failed cleanup")
	}
	require.True(t, exited, "canceled snapshot must finish before the unrelated pool owner releases")
	require.Nil(t, app.lessonStates[987])
}

func TestSessionTimeoutDoesNotRewriteFinishedOrRetiredRoom(t *testing.T) {
	for _, kind := range []string{"finished", "retired", "active"} {
		t.Run(kind, func(t *testing.T) {
			app, clk, _, room := setupRoom(t)
			db, err := store.Open(filepath.Join(t.TempDir(), "timer.db"))
			require.NoError(t, err)
			pool, _ := db.DB()
			defer pool.Close()
			app.DB = db
			var writes atomic.Int64
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("count_timer", func(*gorm.DB) { writes.Add(1) }))
			if kind == "finished" {
				s := room.machine.Snapshot()
				s.Phase = classroom.PhaseLessonEnd
				s.Finished = true
				require.NoError(t, room.machine.Restore(s))
				room.finished = true
			}
			if kind == "retired" {
				room.retired = true
			}
			clk.Advance(61 * time.Minute)
			frames := room.SessionTimeout()
			if kind == "active" {
				require.True(t, room.machine.Paused())
				require.Positive(t, writes.Load())
				require.NotEmpty(t, frames)
			} else {
				require.Zero(t, writes.Load())
				require.Empty(t, frames)
			}
		})
	}
}

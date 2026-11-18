package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAtomicConnectionObservedReportsCommitRollbackAndPreservesResults(t *testing.T) {
	db, _ := openTest(t)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Exec("CREATE TABLE timing_parent (id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE timing_child (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES timing_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)

	var committed AtomicConnectionTiming
	require.NoError(t, atomicConnectionObserved(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO timing_parent(id) VALUES (1)").Error
	}, func(got AtomicConnectionTiming) { committed = got }))
	require.Equal(t, "committed", committed.Outcome)
	require.Equal(t, "completed", committed.Callback.State)
	require.Equal(t, "completed", committed.Commit.State)
	require.Equal(t, "not_started", committed.Rollback.State)
	require.Equal(t, "completed", committed.ConnectionRelease.State)
	require.Equal(t, "completed", committed.Total.State)

	callbackErr := errors.New("callback failed")
	var rolledBack AtomicConnectionTiming
	require.ErrorIs(t, atomicConnectionObserved(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO timing_parent(id) VALUES (2)").Error; err != nil {
			return err
		}
		return callbackErr
	}, func(got AtomicConnectionTiming) {
		rolledBack = got
		panic("observer panic must not replace callback error")
	}), callbackErr)
	require.Equal(t, "failed_cleaned", rolledBack.Outcome)
	require.Equal(t, "failed", rolledBack.Callback.State)
	require.Equal(t, "not_started", rolledBack.Commit.State)
	require.Equal(t, "completed", rolledBack.Rollback.State)

	var commitFailed AtomicConnectionTiming
	err = atomicConnectionObserved(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO timing_child(parent_id) VALUES (999)").Error
	}, func(got AtomicConnectionTiming) { commitFailed = got })
	require.Error(t, err)
	require.Equal(t, "failed", commitFailed.Commit.State)
	require.Equal(t, "failed", commitFailed.Rollback.State)
	require.Equal(t, "completed", commitFailed.RawCleanup.State)
	require.Equal(t, "failed_cleaned", commitFailed.Outcome)

	var canceled AtomicConnectionTiming
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, atomicConnectionObserved(ctx, db, func(*gorm.DB) error {
		t.Fatal("canceled operation must not run")
		return nil
	}, func(got AtomicConnectionTiming) { canceled = got }))
	require.Equal(t, "failed_cleaned", canceled.Outcome)
	require.Equal(t, "not_started", canceled.Callback.State)
	require.Equal(t, "not_started", canceled.Commit.State)

	require.NoError(t, atomicConnectionObserved(context.Background(), db, func(*gorm.DB) error { return nil }, func(AtomicConnectionTiming) { panic("observer panic must be isolated") }))
	var afterObserver int64
	require.NoError(t, db.Table("timing_parent").Count(&afterObserver).Error)
	require.EqualValues(t, 1, afterObserver)
}

func TestAtomicConnectionObservedPreservesOperationPanicAfterCleanup(t *testing.T) {
	db, _ := openTest(t)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Exec("CREATE TABLE timing_panic (value INTEGER)").Error)
	var timing AtomicConnectionTiming
	require.PanicsWithValue(t, "operation panic", func() {
		_ = atomicConnectionObserved(context.Background(), db, func(tx *gorm.DB) error {
			require.NoError(t, tx.Exec("INSERT INTO timing_panic VALUES (1)").Error)
			panic("operation panic")
		}, func(got AtomicConnectionTiming) { timing = got })
	})
	require.Equal(t, "failed_cleaned", timing.Outcome)
	require.Equal(t, "unknown", timing.Callback.State)
	require.Equal(t, "completed", timing.Rollback.State)
	var count int64
	require.NoError(t, db.Table("timing_panic").Count(&count).Error)
	require.Zero(t, count)
}

func TestAtomicConnectionErrorPanicAndNestedKeepPoolUsable(t *testing.T) {
	db, _ := openTest(t)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Exec("CREATE TABLE atomic_probe (value INTEGER)").Error)
	for _, mode := range []string{"error", "panic", "nested"} {
		t.Run(mode, func(t *testing.T) {
			operation := func() error {
				return atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
					require.NoError(t, tx.Exec("INSERT INTO atomic_probe VALUES (1)").Error)
					switch mode {
					case "panic":
						panic("test atomic panic")
					case "nested":
						return atomicConnection(context.Background(), tx, func(*gorm.DB) error { t.Error("nested callback must not execute"); return nil })
					default:
						return errors.New("test atomic failure")
					}
				})
			}
			if mode == "panic" {
				require.PanicsWithValue(t, "test atomic panic", func() { _ = operation() })
			} else {
				require.Error(t, operation())
			}
			var count int64
			require.NoError(t, db.Table("atomic_probe").Count(&count).Error)
			require.Zero(t, count)
		})
	}
	require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { return tx.Exec("INSERT INTO atomic_probe VALUES (2)").Error }))
	var count int64
	require.NoError(t, db.Table("atomic_probe").Count(&count).Error)
	require.EqualValues(t, 1, count)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, atomicConnection(ctx, db, func(*gorm.DB) error { t.Error("canceled callback must not execute"); return nil }))
}

func TestReplacementSQLiteConnectionRetainsSafetyPragmas(t *testing.T) {
	db, _ := openTest(t)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	conn, err := pool.Conn(context.Background())
	require.NoError(t, err)
	require.ErrorIs(t, conn.Raw(func(any) error { return driver.ErrBadConn }), driver.ErrBadConn)
	_ = conn.Close()
	var foreignKeys, busy int
	require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error)
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busy).Error)
	require.Equal(t, 1, foreignKeys)
	require.Equal(t, 5000, busy)
}

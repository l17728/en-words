package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPilotReadyLegacyScopeDurabilityAndRollback(t *testing.T) {
	db, path := pilotMatrixDB(t)
	require.NoError(t, migratePilotSchema(context.Background(), db))
	reserve := func(tx *gorm.DB, sid, bid, at int64) (pilotScopeRow, error) {
		_, err := newPilotLegacyTxGuard(tx, nil)
		if err != nil {
			return pilotScopeRow{}, err
		}
		// The guard establishes writer/schema validation only; the helper must
		// perform the first insertion itself, not replay a guard-created row.
		return reserveLegacyScopeAfterSchemaTx(tx, sid, bid, at)
	}
	var first pilotScopeRow
	require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { var err error; first, err = reserve(tx, 7, 9, 100); return err }))
	require.Equal(t, pilotScopeRow{StudentID: 7, BookID: 9, Mode: "legacy", SchemaVersion: 2, ReservedAt: 100}, first)
	other, err := connectSQLite(path)
	require.NoError(t, err)
	pool, err := other.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, atomicConnection(context.Background(), other, func(tx *gorm.DB) error { row, err := reserve(tx, 7, 9, 999); require.Equal(t, first, row); return err }))
	sentinel := errors.New("rollback test")
	require.ErrorIs(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error { _, err := reserve(tx, 8, 9, 200); require.NoError(t, err); return sentinel }), sentinel)
	var rows []pilotScopeRow
	require.NoError(t, other.Find(&rows).Error)
	require.Equal(t, []pilotScopeRow{first}, rows)
	// A previously established schema proof is not permission after transaction end.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	guard, err := newPilotLegacyTxGuard(tx, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback().Error)
	require.Error(t, guard.Reserve(tx, 7, 9, 300))
	row, err := reserveLegacyScopeAfterSchemaTx(tx, 7, 9, 300)
	require.Error(t, err)
	require.Equal(t, pilotScopeRow{}, row)
	row, err = reserveLegacyScopeAfterSchemaTx(db, 7, 9, 300)
	require.Error(t, err)
	require.Equal(t, pilotScopeRow{}, row)
}

func TestPilotReadyLegacyScopeRejectsPilotAndFailedWrites(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	_, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, err)
	var before []pilotScopeRow
	require.NoError(t, db.Find(&before).Error)
	require.ErrorIs(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		_, err := newPilotLegacyTxGuard(tx, nil)
		if err != nil {
			return err
		}
		row, err := reserveLegacyScopeAfterSchemaTx(tx, 7, 9, 123)
		require.Equal(t, pilotScopeRow{}, row)
		return err
	}), errPilotScopeProtected)
	// Inject a real insert failure after the validated schema gate. DDL and
	// scope insertion share the transaction; rollback removes the fault too.
	require.Error(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		_, err := newPilotLegacyTxGuard(tx, nil)
		if err != nil {
			return err
		}
		if err = tx.Exec(`CREATE TEMP TRIGGER reject_test_scope BEFORE INSERT ON classroom_scope_reservations WHEN NEW.student_id=8 BEGIN SELECT RAISE(ABORT,'test write failure'); END`).Error; err != nil {
			return err
		}
		row, err := reserveLegacyScopeAfterSchemaTx(tx, 8, 9, 123)
		require.Equal(t, pilotScopeRow{}, row)
		require.ErrorContains(t, err, "test write failure")
		return err
	}))
	var after []pilotScopeRow
	require.NoError(t, db.Find(&after).Error)
	require.Equal(t, before, after)
	for _, args := range [][3]int64{{0, 9, 1}, {7, 0, 1}, {7, 9, -1}} {
		row, err := reserveLegacyScopeAfterSchemaTx(db, args[0], args[1], args[2])
		require.Error(t, err)
		require.Equal(t, pilotScopeRow{}, row)
	}
}

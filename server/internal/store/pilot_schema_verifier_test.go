// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestPilotGuardSavepointMustReestablishReservationDraft(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "scope.db"))
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	v, err := newPilotSchemaVerifier(db)
	require.NoError(t, err)
	require.NoError(t, v.AuditCommitted(context.Background()))
	key, err := pilotLegacyBridgeKey(7, 9)
	require.NoError(t, err)
	require.NoError(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		guard, e := newPilotLegacyTxGuard(tx, v)
		require.NoError(t, e)
		require.NoError(t, tx.Exec("SAVEPOINT scope_before_insert").Error)
		require.NoError(t, guard.Reserve(tx, 7, 9, 1))
		require.NoError(t, tx.Exec("ROLLBACK TO scope_before_insert").Error)
		var count int64
		require.NoError(t, tx.Table("runtime_records").Where("key=?", key).Count(&count).Error)
		require.Zero(t, count)
		require.NoError(t, guard.Reserve(tx, 7, 9, 2))
		require.NoError(t, tx.Table("runtime_records").Where("key=?", key).Count(&count).Error)
		require.EqualValues(t, 1, count)
		require.NoError(t, tx.Exec("RELEASE scope_before_insert").Error)
		return nil
	}))
	var count int64
	require.NoError(t, db.Table("runtime_records").Where("key=?", key).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestPilotGuardFinishedTransactionNeverAuthorizesDraft(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "done.db"))
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			defer pool.Close()
			var captured *gorm.DB
			var guard *pilotLegacyTxGuard
			intentional := errors.New("intentional rollback")
			err = atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
				captured = tx
				var e error
				guard, e = newPilotLegacyTxGuard(tx, nil)
				require.NoError(t, e)
				require.NoError(t, guard.Reserve(tx, 7, 9, 1))
				if rollback {
					return intentional
				}
				return nil
			})
			if rollback {
				require.ErrorIs(t, err, intentional)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, captured.Statement.Context.Err(), "valid context does not prove a live transaction")
			require.ErrorIs(t, guard.Reserve(captured, 7, 9, 2), sql.ErrTxDone)
			require.ErrorIs(t, guard.Reserve(captured, 8, 9, 2), sql.ErrTxDone)
		})
	}
}

func TestPilotVerifierUncommittedCookieProofIsNotPublishedDraft(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cookie.db"))
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	v, err := newPilotSchemaVerifier(db)
	require.NoError(t, err)
	require.NoError(t, v.AuditCommitted(context.Background()))
	baselineCookie := v.cookie
	intentional := errors.New("rollback reviewed schema")
	var transientCookie int64
	err = atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE runtime_records SET value=value WHERE 1=0").Error)
		require.NoError(t, tx.Exec("CREATE TABLE unrelated_cookie_probe(id INTEGER)").Error)
		var e error
		transientCookie, e = pilotSchemaCookieTx(tx)
		require.NoError(t, e)
		require.NotEqual(t, baselineCookie, transientCookie)
		shape, e := v.inspectWriterTx(tx)
		require.NoError(t, e)
		require.Equal(t, PilotLegacyUnmigrated, shape)
		return intentional
	})
	require.ErrorIs(t, err, intentional)
	require.Equal(t, baselineCookie, v.cookie, "writer-local validation must not update shared proof")
	// SQLite automatically reuses the rolled-back cookie for different DDL.
	// Do not force PRAGMA schema_version: the ABA must arise from real rollback.
	err = atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE runtime_records SET value=value WHERE 1=0").Error)
		require.NoError(t, tx.Exec("CREATE TABLE classroom_mode_metadata(id INTEGER)").Error)
		cookie, e := pilotSchemaCookieTx(tx)
		require.NoError(t, e)
		require.Equal(t, transientCookie, cookie)
		_, e = v.inspectWriterTx(tx)
		require.Error(t, e, "same transient cookie cannot authorize incomplete pilot schema")
		return intentional
	})
	require.ErrorIs(t, err, intentional)
	require.Equal(t, baselineCookie, v.cookie)
}

func TestPilotVerifierRejectsAnotherPoolEvenForSameDatabaseDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pools.db")
	a, err := Open(path)
	require.NoError(t, err)
	ap, _ := a.DB()
	defer ap.Close()
	b, err := Open(path)
	require.NoError(t, err)
	bp, _ := b.DB()
	defer bp.Close()
	v, err := newPilotSchemaVerifier(a)
	require.NoError(t, err)
	require.NoError(t, v.AuditCommitted(context.Background()))
	require.NoError(t, atomicConnection(context.Background(), b, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("UPDATE runtime_records SET value=value WHERE 1=0").Error)
		_, e := v.inspectWriterTx(tx)
		require.Error(t, e)
		return nil
	}))
}

// Further migrated-path cases require the real approved migration fixture and
// owner/lesson plan; do not fabricate marker-only success: repeat savepoint
// test against classroom_scope_reservations, corrupt marker/metadata on cached
// schema, and verify actual scope=pilot is rejected on every Reserve. Also run
// migration-vs-old-App barriers using real full audit and no shared-pool reuse.

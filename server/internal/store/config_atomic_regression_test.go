package store

import (
	"context"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestConfigScopeTempObjectAndReadFailureFailClosed(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "scope.db"))
	require.NoError(t, err)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	require.NoError(t, WithAtomicWrite(context.Background(), db, func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("CREATE TEMP TABLE classroom_scope_reservations(student_id INTEGER, mode TEXT, schema_version INTEGER, reserved_at INTEGER)").Error)
		defer tx.Exec("DROP TABLE temp.classroom_scope_reservations")
		_, err := ConfigReservedStudents(tx)
		require.ErrorContains(t, err, "atomic_configuration_unavailable")
		return nil
	}))
	require.NoError(t, handle.Close())
	_, err = ConfigReservedStudents(db)
	require.Error(t, err)
}

func TestConfigScopeAbsentAndCanonicalLegacyRemainAvailable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	handle, err := db.DB()
	require.NoError(t, err)
	defer handle.Close()
	scopes, err := ConfigReservedStudents(db)
	require.NoError(t, err)
	require.Empty(t, scopes)
	require.NoError(t, db.Exec("CREATE TABLE classroom_scope_reservations(student_id INTEGER, mode TEXT, schema_version INTEGER, reserved_at INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO classroom_scope_reservations VALUES(1,'legacy',2,0)").Error)
	scopes, err = ConfigReservedStudents(db)
	require.NoError(t, err)
	require.Empty(t, scopes)
}

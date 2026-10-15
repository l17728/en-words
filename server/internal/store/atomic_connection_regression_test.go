package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func schemaObjects(t *testing.T, path string) []string {
	t.Helper()
	d, e := connectSQLite(path)
	require.NoError(t, e)
	pool, e := d.DB()
	require.NoError(t, e)
	defer pool.Close()
	var names []string
	require.NoError(t, d.Raw("SELECT name FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND substr(name,1,7) <> 'sqlite_' ORDER BY name").Scan(&names).Error)
	return names
}

func TestFreshStartupFailureRollsBackAndClosesPool(t *testing.T) {
	for _, failure := range []string{"schema", "trigger", "index"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh.db")
			var pool *sql.DB
			inject := func(dsn string) (*gorm.DB, error) {
				db, err := connectSQLite(dsn)
				if err != nil {
					return nil, err
				}
				pool, err = db.DB()
				if err != nil {
					return nil, err
				}
				t.Cleanup(func() { _ = pool.Close() })
				err = db.Callback().Raw().Before("gorm:raw").Register("startup_fault", func(tx *gorm.DB) {
					q := tx.Statement.SQL.String()
					match := failure == "schema" && strings.HasPrefix(q, "CREATE TABLE `users`") || failure == "trigger" && strings.HasPrefix(q, "CREATE TRIGGER") || failure == "index" && strings.HasPrefix(q, "DROP INDEX IF EXISTS idx_book_norm")
					if match {
						tx.AddError(errors.New("private SQL/path must not escape observer"))
					}
				})
				return db, err
			}
			var events []StageEvent
			got, err := openContext(context.Background(), path, func(e StageEvent) { events = append(events, e) }, inject)
			require.Error(t, err)
			require.Nil(t, got)
			require.Empty(t, schemaObjects(t, path), "fresh DDL must be one atomic unit")
			require.Error(t, pool.Ping(), "failed Open must close its owned pool")
			require.NotEmpty(t, events)
		})
	}
}

func TestFreshStartupPreCanceledDoesNotCreateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canceled.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := OpenContext(ctx, path, nil)
	if got != nil {
		pool, _ := got.DB()
		defer pool.Close()
	}
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, got)
	require.Empty(t, schemaObjects(t, path))
}

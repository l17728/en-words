package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Used by unit tests and the opt-in external-process harness only.
func startupFaultConnector(mode string, cancel context.CancelFunc, capture func(*sql.DB)) func(string) (*gorm.DB, error) {
	return func(dsn string) (*gorm.DB, error) {
		db, err := connectSQLite(dsn)
		if err != nil {
			return nil, err
		}
		pool, err := db.DB()
		if err != nil {
			return nil, err
		}
		if capture != nil {
			capture(pool)
		}
		probes := 0
		err = db.Callback().Row().Before("gorm:row").Register("startup_probe_fault", func(tx *gorm.DB) {
			if strings.Contains(tx.Statement.SQL.String(), "SELECT COUNT(*) FROM sqlite_master") {
				probes++
				if mode == "probe_outer" && probes == 1 || mode == "probe_inner" && probes == 2 {
					tx.AddError(errors.New("injected private probe failure"))
				}
			}
		})
		if err != nil {
			return nil, err
		}
		err = db.Callback().Raw().Before("gorm:raw").Register("startup_raw_fault", func(tx *gorm.DB) {
			q := tx.Statement.SQL.String()
			if mode == "pragma" && strings.HasPrefix(q, "PRAGMA journal_mode") {
				tx.AddError(errors.New("private pragma error"))
				return
			}
			if strings.HasPrefix(q, "CREATE TABLE `users`") {
				switch mode {
				case "schema":
					tx.AddError(errors.New("private schema error"))
				case "cancel":
					cancel()
				case "panic":
					panic("startup injected panic")
				}
			}
			if mode == "trigger" && strings.HasPrefix(q, "CREATE TRIGGER") || mode == "index" && strings.HasPrefix(q, "DROP INDEX IF EXISTS idx_book_norm") {
				tx.AddError(errors.New("private DDL error"))
			}
		})
		if err != nil {
			return nil, err
		}
		err = db.Callback().Raw().After("gorm:raw").Register("startup_deferred_commit_fault", func(tx *gorm.DB) {
			if mode != "commit" || tx.Error != nil || !strings.HasPrefix(tx.Statement.SQL.String(), "CREATE UNIQUE INDEX IF NOT EXISTS idx_book_norm") {
				return
			}
			for _, q := range []string{"CREATE TABLE startup_parent(id INTEGER PRIMARY KEY)", "CREATE TABLE startup_child(id INTEGER REFERENCES startup_parent(id) DEFERRABLE INITIALLY DEFERRED)", "INSERT INTO startup_child VALUES(999)"} {
				if _, e := tx.Statement.ConnPool.ExecContext(tx.Statement.Context, q); e != nil {
					tx.AddError(e)
					return
				}
			}
		})
		return db, err
	}
}

type startupSchemaRow struct{ Type, Name, TblName, SQL string }

func TestFreshStartupUsesOneSQLTransactionAndReleasesStartupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transactions := map[*sql.Tx]bool{}
	ddl := 0
	outside := 0
	connect := func(dsn string) (*gorm.DB, error) {
		db, err := connectSQLite(dsn)
		if err != nil {
			return nil, err
		}
		err = db.Callback().Raw().Before("gorm:raw").Register("observe_startup_transaction", func(tx *gorm.DB) {
			q := tx.Statement.SQL.String()
			if !strings.HasPrefix(q, "CREATE ") && !strings.HasPrefix(q, "DROP ") {
				return
			}
			ddl++
			if p, ok := tx.Statement.ConnPool.(*sql.Tx); ok {
				transactions[p] = true
			} else {
				outside++
			}
		})
		return db, err
	}
	var events []StageEvent
	db, err := openContext(ctx, filepath.Join(t.TempDir(), "one-tx.db"), func(e StageEvent) { events = append(events, e) }, connect)
	require.NoError(t, err)
	pool, _ := db.DB()
	defer pool.Close()
	require.Greater(t, ddl, 22)
	require.Len(t, transactions, 1)
	require.Zero(t, outside)
	cancel()
	require.NoError(t, db.Create(&User{Username: "after_startup_context"}).Error)
	probe := false
	committed := false
	for _, e := range events {
		require.GreaterOrEqual(t, e.DurationMS, int64(0))
		if e.Stage == "schema_probe" && e.Event == "startup_stage_completed" {
			require.NotNil(t, e.Fresh)
			require.True(t, *e.Fresh)
			require.NotNil(t, e.SchemaObjectCount)
			require.Zero(t, *e.SchemaObjectCount)
			probe = true
		}
		if e.Stage == "finalize" && e.Event == "startup_stage_completed" {
			require.Equal(t, "committed", e.TransactionOutcome)
			committed = true
		}
		require.NotEqual(t, "startup_stage_failed", e.Event)
	}
	require.True(t, probe)
	require.True(t, committed)
}

func schemaSnapshot(t *testing.T, db *gorm.DB) []startupSchemaRow {
	t.Helper()
	var rows []startupSchemaRow
	require.NoError(t, db.Raw("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE substr(name,1,7) <> 'sqlite_' ORDER BY type,name").Scan(&rows).Error)
	for i := range rows {
		rows[i].SQL = strings.Join(strings.Fields(rows[i].SQL), " ")
	}
	return rows
}

func TestStartupOpenErrorClosesPartiallyOpenedPool(t *testing.T) {
	var pool *sql.DB
	var events []StageEvent
	db, err := openContext(nil, filepath.Join(t.TempDir(), "partial-open.db"), func(e StageEvent) { events = append(events, e) }, func(dsn string) (*gorm.DB, error) {
		d, e := connectSQLite(dsn)
		require.NoError(t, e)
		pool, e = d.DB()
		require.NoError(t, e)
		return d, errors.New("private path open error")
	})
	require.Error(t, err)
	require.Nil(t, db)
	require.Error(t, pool.Ping())
	require.Len(t, events, 2)
	require.Equal(t, "open_failed", events[1].Reason)
	require.Equal(t, "startup_stage_failed", events[1].Event)
}

func TestFreshStartupSchemaEqualsPriorMigration(t *testing.T) {
	fresh, e := Open(filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, e)
	pool, _ := fresh.DB()
	defer pool.Close()
	legacy, e := connectSQLite(filepath.Join(t.TempDir(), "reference.db"))
	require.NoError(t, e)
	oldPool, _ := legacy.DB()
	defer oldPool.Close()
	require.NoError(t, legacy.AutoMigrate(startupModels()...))
	require.NoError(t, installConfigRevisionTriggers(legacy))
	require.NoError(t, legacy.Exec("DROP INDEX IF EXISTS idx_book_norm").Error)
	require.NoError(t, legacy.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_book_norm ON word_entries(book_id, norm_key)").Error)
	want := schemaSnapshot(t, legacy)
	require.Equal(t, want, schemaSnapshot(t, fresh))
	tables, triggers := 0, 0
	for _, row := range want {
		if row.Type == "table" {
			tables++
		}
		if row.Type == "trigger" {
			triggers++
		}
	}
	require.Equal(t, 22, tables)
	require.Equal(t, 6, triggers)
	for _, q := range []struct {
		sql  string
		want int
	}{{"PRAGMA foreign_keys", 1}, {"PRAGMA busy_timeout", 5000}} {
		var got int
		require.NoError(t, fresh.Raw(q.sql).Scan(&got).Error)
		require.Equal(t, q.want, got)
	}
	var journal string
	require.NoError(t, fresh.Raw("PRAGMA journal_mode").Scan(&journal).Error)
	require.Equal(t, "wal", journal)
	var freshSync, oldSync int
	require.NoError(t, fresh.Raw("PRAGMA synchronous").Scan(&freshSync).Error)
	require.NoError(t, legacy.Raw("PRAGMA synchronous").Scan(&oldSync).Error)
	require.Equal(t, oldSync, freshSync)
}

func TestFreshStartupFaultMatrixAndRecovery(t *testing.T) {
	for _, mode := range []string{"probe_outer", "probe_inner", "commit", "cancel", "panic", "pragma"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.db")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var pool *sql.DB
			var events []StageEvent
			connect := startupFaultConnector(mode, cancel, func(p *sql.DB) { pool = p; t.Cleanup(func() { _ = p.Close() }) })
			call := func() {
				db, err := openContext(ctx, path, func(e StageEvent) { events = append(events, e) }, connect)
				require.Error(t, err)
				require.Nil(t, db)
			}
			if mode == "panic" {
				require.PanicsWithValue(t, "startup injected panic", call)
			} else {
				call()
			}
			require.Error(t, pool.Ping())
			require.Empty(t, schemaObjects(t, path))
			failed := false
			commitFailed := false
			for _, event := range events {
				require.GreaterOrEqual(t, event.DurationMS, int64(0))
				if event.Event == "startup_stage_failed" {
					failed = true
				}
				if event.Stage == "finalize" {
					require.NotEqual(t, "committed", event.TransactionOutcome)
				}
				if event.Reason == "commit_failed" {
					commitFailed = true
				}
				require.NotContains(t, event.Reason, "private")
			}
			require.True(t, failed)
			if mode == "commit" {
				require.True(t, commitFailed)
			}
			recovered, err := Open(path)
			require.NoError(t, err)
			p, _ := recovered.DB()
			require.NoError(t, p.Close())
		})
	}
}

func TestStartupLegacyPreservesDataViewAndRepairsOldIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, db.Create(&User{ID: 7, Username: "keep", Hash: "opaque"}).Error)
	require.NoError(t, db.Exec("CREATE VIEW sqliteX_user_view AS SELECT 7 AS marker").Error)
	require.NoError(t, db.Exec("DROP INDEX idx_book_norm").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_book_norm ON word_entries(norm_key)").Error)
	pool, _ := db.DB()
	require.NoError(t, pool.Close())
	var events []StageEvent
	db, err = OpenContext(context.Background(), path, func(e StageEvent) { events = append(events, e) })
	require.NoError(t, err)
	pool, _ = db.DB()
	defer pool.Close()
	var user User
	require.NoError(t, db.First(&user, 7).Error)
	require.Equal(t, "opaque", user.Hash)
	var marker int
	require.NoError(t, db.Raw("SELECT marker FROM sqliteX_user_view").Scan(&marker).Error)
	require.Equal(t, 7, marker)
	var indexSQL string
	require.NoError(t, db.Raw("SELECT sql FROM sqlite_master WHERE name='idx_book_norm'").Scan(&indexSQL).Error)
	require.Contains(t, indexSQL, "book_id, norm_key")
	legacy := false
	for _, e := range events {
		require.NotEqual(t, "fresh_schema", e.Stage)
		if e.Stage == "legacy_schema" {
			legacy = true
		}
		if e.Fresh != nil {
			require.False(t, *e.Fresh)
		}
	}
	require.True(t, legacy)
}

func TestStartupStrictProbeAndChangedInnerProbe(t *testing.T) {
	for _, object := range []string{"CREATE VIEW sqliteX_custom AS SELECT 1", "CREATE TABLE partial_old(id INTEGER)"} {
		t.Run(object, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "partial.db")
			raw, e := connectSQLite(path)
			require.NoError(t, e)
			pool, _ := raw.DB()
			require.NoError(t, raw.Exec(object).Error)
			require.NoError(t, pool.Close())
			var stages []string
			db, e := OpenContext(context.Background(), path, func(e StageEvent) { stages = append(stages, e.Stage) })
			require.NoError(t, e)
			p, _ := db.DB()
			defer p.Close()
			require.Contains(t, stages, "legacy_schema")
			require.NotContains(t, stages, "fresh_schema")
		})
	}
	path := filepath.Join(t.TempDir(), "race.db")
	mutated := false
	db, e := OpenContext(context.Background(), path, func(e StageEvent) {
		if !mutated && e.Stage == "schema_probe" && e.Event == "startup_stage_completed" {
			mutated = true
			other, err := connectSQLite(path)
			require.NoError(t, err)
			p, _ := other.DB()
			defer p.Close()
			require.NoError(t, other.Exec("CREATE TABLE concurrent_owner(id INTEGER)").Error)
		}
	})
	require.Nil(t, db)
	require.ErrorIs(t, e, ErrInitializationConflict)
	require.Equal(t, []string{"concurrent_owner"}, schemaObjects(t, path))
}

func TestStartupTwoIndependentPoolsNeverPublishPartialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compete.db")
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	type result struct {
		db  *gorm.DB
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			once := sync.Once{}
			db, err := OpenContext(context.Background(), path, func(e StageEvent) {
				if e.Stage == "schema_probe" && e.Event == "startup_stage_completed" {
					once.Do(func() { ready <- struct{}{}; <-release })
				}
			})
			results <- result{db, err}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(20 * time.Second):
			close(release)
			t.Fatal("independent pools did not reach outer probe")
		}
	}
	close(release)
	success := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil {
			success++
			require.Len(t, schemaSnapshot(t, r.db), len(schemaObjects(t, path)))
			p, _ := r.db.DB()
			require.NoError(t, p.Close())
		} else {
			require.Nil(t, r.db)
		}
	}
	require.Equal(t, 1, success)
	recovered, err := Open(path)
	require.NoError(t, err)
	p, _ := recovered.DB()
	require.NoError(t, p.Close())
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gosqlite "github.com/glebarez/go-sqlite"
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

func startupSQLiteBusyPrimaryCode(code int) bool {
	return code > 0 && code&0xff == 5
}

func startupSQLiteBusyError(err error) bool {
	var sqliteErr *gosqlite.Error
	return errors.As(err, &sqliteErr) && startupSQLiteBusyPrimaryCode(sqliteErr.Code())
}

type startupCodeLookalike struct{}

func (startupCodeLookalike) Error() string { return "lookalike" }
func (startupCodeLookalike) Code() int     { return 5 }

func TestStartupSQLiteBusyPrimaryCodeClassification(t *testing.T) {
	for _, code := range []int{5, 261, 517, 773} {
		require.True(t, startupSQLiteBusyPrimaryCode(code), "SQLite BUSY-family code %d", code)
	}
	for _, code := range []int{0, -5, 6, 19, 262} {
		require.False(t, startupSQLiteBusyPrimaryCode(code), "non-BUSY SQLite code %d", code)
	}
	require.False(t, startupSQLiteBusyError(startupCodeLookalike{}), "arbitrary Code interfaces are not SQLite driver errors")
	require.False(t, startupSQLiteBusyError(errors.New("SQLITE_BUSY")), "error strings do not define SQLite error codes")
}

func TestStartupSQLiteBusyErrorRecognizesWrappedDriverError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	blocker, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d0")
	require.NoError(t, err)
	defer blocker.Close()
	contender, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d0")
	require.NoError(t, err)
	defer contender.Close()
	blocker.SetMaxOpenConns(1)
	contender.SetMaxOpenConns(1)
	_, err = blocker.Exec("CREATE TABLE startup_busy_probe (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	tx, err := blocker.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO startup_busy_probe(id) VALUES (1)")
	require.NoError(t, err)
	_, err = contender.Exec("INSERT INTO startup_busy_probe(id) VALUES (2)")
	require.Error(t, err)
	require.True(t, startupSQLiteBusyError(err), "actual go-sqlite BUSY error must be recognized")
	require.True(t, startupSQLiteBusyError(fmt.Errorf("wrapped startup error: %w", err)), "errors.As must recognize a wrapped go-sqlite BUSY error")
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
	rows, err := readSchemaSnapshot(db)
	require.NoError(t, err)
	return rows
}

func readSchemaSnapshot(db *gorm.DB) ([]startupSchemaRow, error) {
	return readSchemaSnapshotContext(context.Background(), db)
}

func readSchemaSnapshotContext(ctx context.Context, db *gorm.DB) ([]startupSchemaRow, error) {
	var rows []startupSchemaRow
	if err := db.WithContext(ctx).Raw("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE substr(name,1,7) <> 'sqlite_' ORDER BY type,name").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].SQL = strings.Join(strings.Fields(rows[i].SQL), " ")
	}
	return rows, nil
}

func expectedStartupSchema(ctx context.Context, path string) (rows []startupSchemaRow, err error) {
	db, openErr := connectSQLite(path)
	if db == nil {
		return nil, openErr
	}
	pool, poolErr := db.DB()
	if poolErr != nil {
		return nil, poolErr
	}
	defer func() {
		if closeErr := pool.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if openErr != nil {
		return nil, openErr
	}
	ctxDB := db.WithContext(ctx)
	if err = ctxDB.AutoMigrate(startupModels()...); err != nil {
		return nil, err
	}
	if err = installConfigRevisionTriggers(ctxDB); err != nil {
		return nil, err
	}
	if err = ctxDB.Exec("DROP INDEX IF EXISTS idx_book_norm").Error; err != nil {
		return nil, err
	}
	if err = ctxDB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_book_norm ON word_entries(book_id, norm_key)").Error; err != nil {
		return nil, err
	}
	return readSchemaSnapshotContext(ctx, db)
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
	require.Len(t, events, 4)
	require.Equal(t, "open_failed", events[1].Reason)
	require.Equal(t, "startup_stage_failed", events[1].Event)
	require.Equal(t, "pool_cleanup", events[2].Stage)
	require.Equal(t, "startup_stage_started", events[2].Event)
	require.Equal(t, "pool_cleanup", events[3].Stage)
	require.Equal(t, "startup_stage_completed", events[3].Event)
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
	type workerResult struct {
		id                     int
		opened                 bool
		returnedDBOnError      bool
		poolCloseVerified      bool
		initializationConflict bool
		sqliteBusy             bool
		schema                 []startupSchemaRow
		errorCode              string
		closeCode              string
		stages                 []string
	}
	type workerEvent struct {
		id     int
		result workerResult
	}

	dir, err := os.MkdirTemp("", "enwords-startup-two-pools-")
	require.NoError(t, err)
	path := filepath.Join(dir, "compete.db")
	allWorkersJoined := true
	allPoolsClosed := true
	cleanupDir := false
	defer func() {
		if cleanupDir && allWorkersJoined && allPoolsClosed {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("startup race cleanup_remove_failed")
			}
		} else {
			t.Logf("startup race diagnostic directory retained; workers_joined=%t pools_closed=%t", allWorkersJoined, allPoolsClosed)
		}
	}()
	// Establish WAL mode before introducing concurrent startup. The test below
	// targets schema initialization contention; racing the initial journal-mode
	// transition would fail in the PRAGMA stage before either worker can observe
	// the empty schema barrier.
	preflightCode := ""
	preflightCloseFailed := false
	preflight, preflightOpenErr := sql.Open("sqlite", path)
	if preflightOpenErr != nil {
		preflightCode = "open_failed"
		cleanupDir = true
	} else {
		preflight.SetMaxOpenConns(1)
		preflightCtx, preflightCancel := context.WithTimeout(context.Background(), 20*time.Second)
		var mode string
		if queryErr := preflight.QueryRowContext(preflightCtx, "PRAGMA journal_mode=WAL").Scan(&mode); queryErr != nil {
			preflightCode = "journal_mode_failed"
		} else if !strings.EqualFold(mode, "wal") {
			preflightCode = "journal_mode_unexpected"
		} else {
			var objects int
			if schemaErr := preflight.QueryRowContext(preflightCtx, "SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND substr(name,1,7) <> 'sqlite_'").Scan(&objects); schemaErr != nil {
				preflightCode = "schema_probe_failed"
			} else if objects != 0 {
				preflightCode = "schema_not_empty"
			}
		}
		closeErr := preflight.Close()
		preflightCancel()
		if closeErr != nil {
			preflightCloseFailed = true
			allPoolsClosed = false
			if preflightCode == "" {
				preflightCode = "close_failed"
			} else {
				preflightCode += "+close_failed"
			}
		} else {
			cleanupDir = true
		}
	}
	require.Equal(t, "", preflightCode, "startup race preflight failed (close_failed=%t)", preflightCloseFailed)
	cleanupDir = false
	ready := make(chan int, 2)
	release := make(chan struct{})
	results := make(chan workerEvent, 2)
	contexts := [2]context.CancelFunc{}
	seenReady := [2]bool{}
	seenResult := [2]bool{}
	var resultByID [2]workerResult
	var resultCount int
	var stages [2][]string
	var stageOverflow [2]bool
	var stageMu sync.Mutex
	appendStage := func(id int, e StageEvent) {
		stageMu.Lock()
		if len(stages[id]) < 64 {
			stages[id] = append(stages[id], e.Stage+":"+e.Event+":"+e.Reason+":"+strconv.FormatInt(e.DurationMS, 10)+"ms")
		} else {
			stageOverflow[id] = true
		}
		stageMu.Unlock()
	}
	worker := func(id int, ctx context.Context) {
		var once sync.Once
		r := workerResult{id: id}
		var db *gorm.DB
		defer func() {
			if db != nil {
				pool, poolErr := db.DB()
				if poolErr != nil {
					r.closeCode = "pool_access_failed"
				} else if closeErr := pool.Close(); closeErr != nil {
					r.closeCode = "pool_close_failed"
				} else {
					r.poolCloseVerified = true
				}
			}
			stageMu.Lock()
			r.stages = append([]string(nil), stages[id]...)
			stageMu.Unlock()
			results <- workerEvent{id: id, result: r}
		}()
		var openErr error
		db, openErr = OpenContext(ctx, path, func(e StageEvent) {
			appendStage(id, e)
			if e.Stage == "pool_cleanup" && e.Event == "startup_stage_completed" {
				r.poolCloseVerified = true
			}
			if e.Stage == "schema_probe" && e.Event == "startup_stage_completed" {
				once.Do(func() {
					ready <- id
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
			}
		})
		if openErr != nil {
			r.returnedDBOnError = db != nil
			if errors.Is(openErr, ErrInitializationConflict) {
				r.initializationConflict = true
				r.errorCode = "initialization_conflict"
			} else if errors.Is(openErr, context.Canceled) || errors.Is(openErr, context.DeadlineExceeded) {
				r.errorCode = "canceled"
			} else if startupSQLiteBusyError(openErr) {
				r.sqliteBusy = true
				r.errorCode = "sqlite_busy"
			} else {
				r.errorCode = "open_failed"
			}
		} else if db == nil {
			r.errorCode = "open_returned_nil"
		} else {
			r.opened = true
			var queryErr error
			r.schema, queryErr = readSchemaSnapshotContext(ctx, db)
			if queryErr != nil {
				r.errorCode = "schema_read_failed"
			}
		}
	}

	for id := 0; id < 2; id++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		contexts[id] = cancel
		if id == 0 {
			allWorkersJoined = false
			allPoolsClosed = false
		}
		go worker(id, ctx)
	}

	readyTimer := time.NewTimer(20 * time.Second)
	readyFailure := ""
	for readyCount := 0; readyCount < 2; {
		select {
		case id := <-ready:
			if id < 0 || id >= len(seenReady) || seenReady[id] {
				readyFailure = "duplicate_or_invalid_ready"
				break
			}
			seenReady[id] = true
			readyCount++
		case event := <-results:
			if event.id >= 0 && event.id < len(seenResult) && !seenResult[event.id] {
				seenResult[event.id] = true
				resultByID[event.id] = event.result
				resultCount++
			}
			readyFailure = "worker_returned_before_ready"
		case <-readyTimer.C:
			readyFailure = "ready_timeout"
		}
		if readyFailure != "" {
			break
		}
	}
	if !readyTimer.Stop() {
		select {
		case <-readyTimer.C:
		default:
		}
	}

	if readyFailure == "" {
		close(release)
	} else {
		close(release)
		for _, cancel := range contexts {
			cancel()
		}
	}
	resultFailure := ""
	if readyFailure == "" {
		resultTimer := time.NewTimer(20 * time.Second)
		for resultCount < 2 {
			select {
			case event := <-results:
				if event.id < 0 || event.id >= len(seenResult) || seenResult[event.id] {
					resultFailure = "duplicate_or_invalid_result"
					break
				}
				seenResult[event.id] = true
				resultByID[event.id] = event.result
				resultCount++
			case <-resultTimer.C:
				resultFailure = "result_timeout"
			}
			if resultFailure != "" {
				break
			}
		}
		if !resultTimer.Stop() {
			select {
			case <-resultTimer.C:
			default:
			}
		}
		if resultFailure != "" {
			for _, cancel := range contexts {
				cancel()
			}
		}
	}

	if resultCount < 2 {
		joinTimer := time.NewTimer(10 * time.Second)
		for resultCount < 2 {
			select {
			case event := <-results:
				if event.id >= 0 && event.id < len(seenResult) && !seenResult[event.id] {
					seenResult[event.id] = true
					resultByID[event.id] = event.result
					resultCount++
				}
			case <-joinTimer.C:
				resultFailure = "cleanup_incomplete"
			}
			if resultFailure == "cleanup_incomplete" {
				break
			}
		}
		joinTimer.Stop()
	}
	allWorkersJoined = resultCount == 2
	allPoolsClosed = allWorkersJoined && resultByID[0].closeCode == "" && resultByID[1].closeCode == "" && resultByID[0].poolCloseVerified && resultByID[1].poolCloseVerified
	cleanupDir = allWorkersJoined && allPoolsClosed
	for _, cancel := range contexts {
		cancel()
	}
	if !allWorkersJoined {
		stageMu.Lock()
		lastStages := [2][]string{append([]string(nil), stages[0]...), append([]string(nil), stages[1]...)}
		lastStageOverflow := stageOverflow
		stageMu.Unlock()
		t.Errorf("startup race worker cleanup incomplete (%s); worker0=%s stages=%s overflow=%t worker1=%s stages=%s overflow=%t; diagnostic directory retained", resultFailure, resultByID[0].errorCode, strings.Join(lastStages[0], ","), lastStageOverflow[0], resultByID[1].errorCode, strings.Join(lastStages[1], ","), lastStageOverflow[1])
		return
	}
	if !allPoolsClosed {
		t.Errorf("startup race worker pool cleanup failed (%s)", "pool_close_failed")
		return
	}

	stageMu.Lock()
	finalStages := stages
	finalStageOverflow := stageOverflow
	stageMu.Unlock()
	if readyFailure != "" || resultFailure != "" {
		t.Errorf("startup race did not complete: ready=%s result=%s worker0=%s stages=%s overflow=%t worker1=%s stages=%s overflow=%t", readyFailure, resultFailure, resultByID[0].errorCode, strings.Join(finalStages[0], ","), finalStageOverflow[0], resultByID[1].errorCode, strings.Join(finalStages[1], ","), finalStageOverflow[1])
		return
	}
	for id := range resultByID {
		require.NotEmpty(t, finalStages[id], "worker %d produced no startup stage diagnostics", id)
		require.Empty(t, resultByID[id].closeCode, "worker %d pool close", id)
		require.True(t, resultByID[id].poolCloseVerified, "worker %d pool close must be observed", id)
		require.False(t, resultByID[id].returnedDBOnError, "worker %d must not publish a DB with startup error", id)
	}
	success := 0
	var winningSchema []startupSchemaRow
	for _, r := range resultByID {
		if r.opened {
			success++
			require.Empty(t, r.errorCode)
			require.NotEmpty(t, r.schema)
			winningSchema = r.schema
		} else {
			require.False(t, r.returnedDBOnError, "worker %d returned a DB with startup error", r.id)
			require.True(t, r.initializationConflict || r.sqliteBusy,
				"worker %d loser outcome=%s stages=%s overflow=%t; expected initialization_conflict or sqlite_busy",
				r.id, r.errorCode, strings.Join(finalStages[r.id], ","), finalStageOverflow[r.id])
		}
	}
	require.Equal(t, 1, success)
	expectedCtx, expectedCancel := context.WithTimeout(context.Background(), 20*time.Second)
	expectedSchema, expectedErr := expectedStartupSchema(expectedCtx, filepath.Join(dir, "expected.db"))
	expectedCancel()
	if expectedErr != nil {
		allPoolsClosed = false
		cleanupDir = false
	}
	require.NoError(t, expectedErr)
	require.Equal(t, expectedSchema, winningSchema)
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 20*time.Second)
	recovered, err := OpenContext(recoveryCtx, path, nil)
	var recoveredSchema []startupSchemaRow
	var recoveryErr error
	if recovered != nil {
		var pool *sql.DB
		pool, recoveryErr = recovered.DB()
		if recoveryErr != nil {
			allPoolsClosed = false
			cleanupDir = false
		} else {
			recoveredSchema, recoveryErr = readSchemaSnapshotContext(recoveryCtx, recovered)
			if closeErr := pool.Close(); closeErr != nil {
				allPoolsClosed = false
				cleanupDir = false
				if recoveryErr == nil {
					recoveryErr = closeErr
				}
			}
		}
	}
	recoveryCancel()
	if err != nil || recoveryErr != nil {
		allPoolsClosed = false
		cleanupDir = false
	}
	require.NoError(t, err)
	require.NoError(t, recoveryErr)
	require.Equal(t, expectedSchema, recoveredSchema)
}

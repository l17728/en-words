package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// StageEvent contains bounded startup observations, never SQL, paths or errors.
// Nested fresh_schema stages overlap: their durations must not be summed.
type StageEvent struct {
	Event, Stage, StorageMode, Reason, TransactionOutcome string
	DurationMS                                            int64
	Fresh                                                 *bool
	SchemaObjectCount                                     *int64
}

var ErrInitializationConflict = errors.New("store: initialization conflict")

func Open(path string) (*gorm.DB, error) { return OpenContext(context.Background(), path, nil) }

// OpenContext publishes a usable pool only after initialization succeeds.
// The returned DB does not retain the startup context for later requests.
func OpenContext(ctx context.Context, path string, observer func(StageEvent)) (*gorm.DB, error) {
	return openContext(ctx, path, observer, connectSQLite)
}

func connectSQLite(dsn string) (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
}

type startupObservation struct {
	observer func(StageEvent)
	fresh    *bool
	count    *int64
}

func (s *startupObservation) emit(event, stage, reason, outcome string, started time.Time) {
	if s.observer == nil {
		return
	}
	e := StageEvent{Event: event, Stage: stage, StorageMode: "sqlite", Reason: reason, TransactionOutcome: outcome, DurationMS: time.Since(started).Milliseconds()}
	if s.fresh != nil {
		v := *s.fresh
		e.Fresh = &v
	}
	if s.count != nil {
		v := *s.count
		e.SchemaObjectCount = &v
	}
	s.observer(e)
}

func startupReason(err error, fallback string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if errors.Is(err, ErrInitializationConflict) {
		return "initialization_conflict"
	}
	return fallback
}

func (s *startupObservation) stage(name, reason string, fn func() error) (err error) {
	start := time.Now()
	s.emit("startup_stage_started", name, "", "", start)
	returned := false
	defer func() {
		if !returned || err != nil {
			s.emit("startup_stage_failed", name, startupReason(err, reason), "", start)
		} else {
			s.emit("startup_stage_completed", name, "", "", start)
		}
	}()
	err = fn()
	returned = true
	return err
}

func startupModels() []any {
	return []any{&OverdueTask{}, &FeedbackTask{}, &FeedbackIntent{}, &FeedbackTodo{}, &FeedbackNotice{}, &ConfigRevision{}, &RuntimeRecord{}, &DurableMemory{}, &User{}, &WordBook{}, &WordEntry{}, &MemoryCard{}, &Lesson{}, &ClassroomEvent{}, &WsTicket{}, &ConfigGlobal{}, &ConfigOverride{}, &Feedback{}, &Notification{}, &AgentMemory{}, &AuditLog{}, &PlacementDraft{}}
}

func userSchemaCount(db *gorm.DB) (int64, error) {
	var n int64
	err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','index','trigger','view') AND substr(name,1,7) <> 'sqlite_'").Scan(&n).Error
	return n, err
}

func initializeSchema(ctx context.Context, db *gorm.DB, s *startupObservation) error {
	var count int64
	if err := s.stage("schema_probe", "probe_failed", func() error {
		var e error
		count, e = userSchemaCount(db.WithContext(ctx))
		if e == nil {
			fresh := count == 0
			s.fresh = &fresh
			s.count = &count
		}
		return e
	}); err != nil {
		return err
	}
	fresh := count == 0
	s.fresh = &fresh
	s.count = &count
	build := func(tx *gorm.DB, create bool) error {
		if create {
			if err := tx.Migrator().CreateTable(startupModels()...); err != nil {
				return err
			}
		} else {
			if err := tx.AutoMigrate(startupModels()...); err != nil {
				return err
			}
		}
		if err := s.stage("triggers", "trigger_failed", func() error { return installConfigRevisionTriggers(tx) }); err != nil {
			return err
		}
		return s.stage("index", "index_failed", func() error {
			if err := tx.Exec("DROP INDEX IF EXISTS idx_book_norm").Error; err != nil {
				return err
			}
			return tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_book_norm ON word_entries(book_id, norm_key)").Error
		})
	}
	if !fresh {
		return s.stage("legacy_schema", "schema_failed", func() error { return build(db.WithContext(ctx), false) })
	}
	return s.stage("fresh_schema", "schema_failed", func() (err error) {
		var finalStarted time.Time
		callbackSucceeded := false
		returned := false
		defer func() {
			if finalStarted.IsZero() {
				return
			}
			event, outcome, reason := "startup_stage_completed", "committed", ""
			if !returned {
				event, outcome, reason = "startup_stage_failed", "panicked", "schema_failed"
			} else if err != nil {
				event, outcome = "startup_stage_failed", "failed"
				reason = "schema_failed"
				if callbackSucceeded {
					reason = "commit_failed"
				}
				reason = startupReason(err, reason)
			}
			s.emit(event, "finalize", reason, outcome, finalStarted)
		}()
		err = atomicConnection(ctx, db, func(tx *gorm.DB) (operationErr error) {
			defer func() { finalStarted = time.Now(); s.emit("startup_stage_started", "finalize", "", "", finalStarted) }()
			if e := s.stage("schema_probe", "probe_failed", func() error {
				n, e := userSchemaCount(tx)
				if e != nil {
					return e
				}
				if n != 0 {
					return ErrInitializationConflict
				}
				return nil
			}); e != nil {
				return e
			}
			operationErr = build(tx, true)
			callbackSucceeded = operationErr == nil
			return operationErr
		})
		returned = true
		return err
	})
}

// connect is a private dependency seam for deterministic SQLite fault tests.
func openContext(ctx context.Context, path string, observer func(StageEvent), connect func(string) (*gorm.DB, error)) (result *gorm.DB, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if path == "" {
		path = filepath.Join("data", "enwords.db")
	}
	s := &startupObservation{observer: observer}
	var d *gorm.DB
	var pool *sql.DB
	published := false
	defer func() {
		if !published && pool != nil {
			_ = pool.Close()
		}
	}()
	err = s.stage("open", "open_failed", func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		var e error
		d, e = connect(path + separator + "_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29")
		// Some drivers return a partially opened DB together with an error.
		// Capture its pool before returning so failed initialization still owns cleanup.
		if d != nil {
			pool, _ = d.DB()
		}
		if e != nil {
			return e
		}
		if d == nil {
			return errors.New("store: connector returned no database")
		}
		pool, e = d.DB()
		if e != nil {
			return e
		}
		pool.SetMaxOpenConns(1)
		return pool.PingContext(ctx)
	})
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err = s.stage("pragmas", "pragma_failed", func() error {
		for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
			if e := d.WithContext(ctx).Exec(q).Error; e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: pragmas: %w", err)
	}
	if err = initializeSchema(ctx, d, s); err != nil {
		return nil, fmt.Errorf("store: initialize: %w", err)
	}
	published = true
	return d, nil
}

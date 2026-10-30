package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type AtomicPhaseTiming struct {
	State      string
	DurationMS int64
}

type AtomicConnectionTiming struct {
	Outcome           string
	Callback          AtomicPhaseTiming
	Commit            AtomicPhaseTiming
	Rollback          AtomicPhaseTiming
	RawCleanup        AtomicPhaseTiming
	ConnectionRelease AtomicPhaseTiming
	Total             AtomicPhaseTiming
}

// atomicConnection pins the physical connection until failed COMMIT cleanup is
// complete. database/sql marks a Tx done even when SQLite's COMMIT leaves it open.
// Only outer repositories may call this helper; nested transactions are rejected.
func atomicConnection(ctx context.Context, db *gorm.DB, operation func(*gorm.DB) error) error {
	return atomicConnectionObserved(ctx, db, operation, nil)
}

// atomicConnectionObserved adds best-effort diagnostics without changing the
// transaction result, cleanup path, or panic propagation.
func atomicConnectionObserved(ctx context.Context, db *gorm.DB, operation func(*gorm.DB) error, observe func(AtomicConnectionTiming)) (err error) {
	started := time.Now()
	timing := AtomicConnectionTiming{
		Outcome:           "outcome_uncertain",
		Callback:          AtomicPhaseTiming{State: "not_started"},
		Commit:            AtomicPhaseTiming{State: "not_started"},
		Rollback:          AtomicPhaseTiming{State: "not_started"},
		RawCleanup:        AtomicPhaseTiming{State: "not_started"},
		ConnectionRelease: AtomicPhaseTiming{State: "not_started"},
		Total:             AtomicPhaseTiming{State: "running"},
	}
	finish := func() {
		timing.Total = AtomicPhaseTiming{State: "completed", DurationMS: time.Since(started).Milliseconds()}
		if observe != nil {
			func() {
				defer func() { _ = recover() }()
				observe(timing)
			}()
		}
	}
	pool, ok := db.Statement.ConnPool.(*sql.DB)
	if !ok {
		err = fmt.Errorf("store: atomic operation requires an outer database")
		timing.Outcome = "failed_cleaned"
		finish()
		return err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		timing.Outcome = "failed_cleaned"
		finish()
		return err
	}
	var tx *gorm.DB
	committed := false
	commitAttempted := false

	// Register these independently so cleanup panics cannot skip connection return
	// or the observer. LIFO order is cleanup, release, then observer.
	defer finish()
	defer func() {
		releaseStarted := time.Now()
		timing.ConnectionRelease.State = "running"
		defer func() {
			if panicValue := recover(); panicValue != nil {
				timing.ConnectionRelease = AtomicPhaseTiming{State: "unknown", DurationMS: time.Since(releaseStarted).Milliseconds()}
				panic(panicValue)
			}
		}()
		closeErr := conn.Close()
		state := "completed"
		if closeErr != nil {
			state = "failed"
		}
		timing.ConnectionRelease = AtomicPhaseTiming{State: state, DurationMS: time.Since(releaseStarted).Milliseconds()}
	}()
	defer func() {
		panicked := recover()
		if !committed {
			if tx == nil {
				timing.Outcome = "failed_cleaned"
			} else {
				rollbackStarted := time.Now()
				timing.Rollback.State = "running"
				var rollbackErr error
				func() {
					defer func() {
						if panicValue := recover(); panicValue != nil {
							timing.Rollback = AtomicPhaseTiming{State: "unknown", DurationMS: time.Since(rollbackStarted).Milliseconds()}
							panic(panicValue)
						}
					}()
					rollbackErr = tx.Rollback().Error
				}()
				rollbackState := "completed"
				if rollbackErr != nil {
					rollbackState = "failed"
				}
				timing.Rollback = AtomicPhaseTiming{State: rollbackState, DurationMS: time.Since(rollbackStarted).Milliseconds()}
				if rollbackErr == nil {
					timing.Outcome = "failed_cleaned"
				} else {
					// Cancellation may already have rolled back the sql.Tx. A failed COMMIT,
					// however, needs raw ROLLBACK on this exact still-pinned connection.
					cleanupStarted := time.Now()
					timing.RawCleanup.State = "running"
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					var cleanupErr error
					func() {
						defer cancel()
						defer func() {
							if panicValue := recover(); panicValue != nil {
								timing.RawCleanup = AtomicPhaseTiming{State: "unknown", DurationMS: time.Since(cleanupStarted).Milliseconds()}
								panic(panicValue)
							}
						}()
						_, cleanupErr = conn.ExecContext(cleanupCtx, "ROLLBACK")
						if cleanupErr != nil && !errors.Is(cleanupErr, sql.ErrConnDone) && !strings.Contains(cleanupErr.Error(), "cannot rollback - no transaction is active") {
							// Never return a potentially dirty connection to the pool.
							_ = conn.Raw(func(any) error { return driver.ErrBadConn })
						}
					}()
					if cleanupErr == nil {
						timing.RawCleanup = AtomicPhaseTiming{State: "completed", DurationMS: time.Since(cleanupStarted).Milliseconds()}
						timing.Outcome = "failed_cleaned"
					} else if errors.Is(cleanupErr, sql.ErrConnDone) || strings.Contains(cleanupErr.Error(), "cannot rollback - no transaction is active") {
						state := "completed"
						if commitAttempted {
							state = "unknown"
							timing.Outcome = "outcome_uncertain"
						} else {
							timing.Outcome = "failed_cleaned"
						}
						timing.RawCleanup = AtomicPhaseTiming{State: state, DurationMS: time.Since(cleanupStarted).Milliseconds()}
					} else {
						timing.RawCleanup = AtomicPhaseTiming{State: "failed", DurationMS: time.Since(cleanupStarted).Milliseconds()}
						timing.Outcome = "outcome_uncertain"
						if err == nil {
							err = fmt.Errorf("store: transaction cleanup failed")
						}
					}
				}
			}
		}
		if panicked != nil {
			panic(panicked)
		}
	}()

	pinned := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	pinned.Statement.ConnPool = conn
	begin := pinned.Begin()
	if begin.Error != nil {
		timing.Outcome = "failed_cleaned"
		return begin.Error
	}
	tx = begin
	callbackStarted := time.Now()
	timing.Callback.State = "running"
	func() {
		defer func() {
			if panicValue := recover(); panicValue != nil {
				timing.Callback = AtomicPhaseTiming{State: "unknown", DurationMS: time.Since(callbackStarted).Milliseconds()}
				panic(panicValue)
			}
		}()
		err = operation(tx)
	}()
	callbackState := "completed"
	if err != nil {
		callbackState = "failed"
	}
	timing.Callback = AtomicPhaseTiming{State: callbackState, DurationMS: time.Since(callbackStarted).Milliseconds()}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	commitStarted := time.Now()
	commitAttempted = true
	timing.Commit.State = "running"
	func() {
		defer func() {
			if panicValue := recover(); panicValue != nil {
				timing.Commit = AtomicPhaseTiming{State: "unknown", DurationMS: time.Since(commitStarted).Milliseconds()}
				panic(panicValue)
			}
		}()
		err = tx.Commit().Error
	}()
	commitState := "completed"
	if err != nil {
		commitState = "failed"
	}
	timing.Commit = AtomicPhaseTiming{State: commitState, DurationMS: time.Since(commitStarted).Milliseconds()}
	committed = err == nil
	if committed {
		timing.Outcome = "committed"
	}
	return err
}

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

// atomicConnection pins the physical connection until failed COMMIT cleanup is
// complete. database/sql marks a Tx done even when SQLite's COMMIT leaves it open.
// Only outer repositories may call this helper; nested transactions are rejected.
func atomicConnection(ctx context.Context, db *gorm.DB, operation func(*gorm.DB) error) (err error) {
	pool, ok := db.Statement.ConnPool.(*sql.DB)
	if !ok {
		return fmt.Errorf("store: atomic operation requires an outer database")
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	pinned := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	pinned.Statement.ConnPool = conn
	tx := pinned.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback().Error; rollbackErr == nil {
			return
		}
		// Cancellation may already have rolled back the sql.Tx. A failed COMMIT,
		// however, needs raw ROLLBACK on this exact still-pinned connection.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, cleanupErr := conn.ExecContext(cleanupCtx, "ROLLBACK")
		if cleanupErr == nil || errors.Is(cleanupErr, sql.ErrConnDone) || strings.Contains(cleanupErr.Error(), "cannot rollback - no transaction is active") {
			return
		}
		// Never return a potentially dirty connection to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		if err == nil {
			err = fmt.Errorf("store: transaction cleanup failed")
		}
	}()
	if err = operation(tx); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	err = tx.Commit().Error
	committed = err == nil
	return err
}

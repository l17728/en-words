//go:build cgo

package sqlite3

import (
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openBusyTestConn(t *testing.T, dsn string) *SQLiteConn {
	t.Helper()
	conn, err := (&SQLiteDriver{}).Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlConn, ok := conn.(*SQLiteConn)
	if !ok {
		t.Fatalf("connection type = %T", conn)
	}
	t.Cleanup(func() { _ = sqlConn.Close() })
	return sqlConn
}

func busyTestLock(t *testing.T) (*SQLiteConn, *SQLiteConn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.db")
	owner := openBusyTestConn(t, path)
	contender := openBusyTestConn(t, path)
	if _, err := owner.Exec("CREATE TABLE IF NOT EXISTS t(v)", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec("BEGIN IMMEDIATE", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec("ROLLBACK", nil) })
	return owner, contender
}

func handleCountFor(db *SQLiteConn) int {
	handleLock.Lock()
	defer handleLock.Unlock()
	count := 0
	for _, value := range handleVals {
		if value.db == db {
			count++
		}
	}
	return count
}

func TestRegisterBusyHandlerCountReplacementAndClear(t *testing.T) {
	_, contender := busyTestLock(t)
	oldCalls, newCalls := 0, 0
	if err := contender.RegisterBusyHandler(func(retry int) bool {
		oldCalls++
		if retry != 0 {
			t.Errorf("first retry count = %d, want 0", retry)
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.Exec("BEGIN IMMEDIATE", nil); err == nil || oldCalls != 1 {
		t.Fatalf("first handler result: calls=%d err=%v", oldCalls, err)
	}
	if err := contender.RegisterBusyHandler(func(int) bool { newCalls++; return false }); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.Exec("BEGIN IMMEDIATE", nil); err == nil || newCalls != 1 || oldCalls != 1 {
		t.Fatalf("replacement result: old=%d new=%d err=%v", oldCalls, newCalls, err)
	}
	if err := contender.RegisterBusyHandler(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.Exec("BEGIN IMMEDIATE", nil); err == nil || newCalls != 1 {
		t.Fatalf("cleared handler result: calls=%d err=%v", newCalls, err)
	}
	if _, err := contender.Exec("PRAGMA busy_timeout=35", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := contender.Query("PRAGMA busy_timeout", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		t.Fatal(err)
	}
	if got, ok := values[0].(int64); !ok || got != 35 {
		t.Fatalf("busy_timeout after reapplication = %v, want 35", values[0])
	}
}

func TestRegisterBusyHandlerPanicFailsClosedAndConnectionRemainsUsable(t *testing.T) {
	_, contender := busyTestLock(t)
	if err := contender.RegisterBusyHandler(func(int) bool { panic("test panic") }); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.Exec("BEGIN IMMEDIATE", nil); err == nil {
		t.Fatal("panicking busy handler unexpectedly retried")
	}
	if _, err := contender.Exec("SELECT 1", nil); err != nil {
		t.Fatalf("connection unusable after callback panic: %v", err)
	}
}

func TestRegisterBusyHandlerRejectsClosedConnectionAndReleasesHandle(t *testing.T) {
	conn := openBusyTestConn(t, ":memory:")
	before := handleCountFor(conn)
	if err := conn.RegisterBusyHandler(func(int) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if got := handleCountFor(conn); got != before+1 {
		t.Fatalf("registered handles=%d want %d", got, before+1)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := handleCountFor(conn); got != 0 {
		t.Fatalf("handles retained after close: %d", got)
	}
	if err := conn.RegisterBusyHandler(func(int) bool { return false }); err == nil {
		t.Fatal("registration succeeded on closed connection")
	}
}

func TestRegisterBusyHandlerSerializesWithClose(t *testing.T) {
	for i := 0; i < 100; i++ {
		conn, err := (&SQLiteDriver{}).Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		sqlConn := conn.(*SQLiteConn)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var registerErr, closeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			registerErr = sqlConn.RegisterBusyHandler(func(int) bool { return false })
		}()
		go func() {
			defer wg.Done()
			<-start
			closeErr = sqlConn.Close()
		}()
		close(start)
		wg.Wait()
		if closeErr != nil {
			t.Fatalf("iteration %d close: %v", i, closeErr)
		}
		if registerErr != nil && !errors.Is(registerErr, errSQLiteConnClosed) {
			t.Fatalf("iteration %d register: %v", i, registerErr)
		}
		if got := handleCountFor(sqlConn); got != 0 {
			t.Fatalf("iteration %d leaked %d cgo handles", i, got)
		}
	}
}

var _ driver.Conn = (*SQLiteConn)(nil)

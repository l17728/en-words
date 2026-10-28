//go:build cgo

package api

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"enwords/internal/store"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type firstAudioCapturedAuthorizerTuple struct {
	action       int
	arg1         string
	arg2         string
	databaseName string
	triggerName  string
}

type firstAudioAuthorizerCapture struct {
	mu     sync.Mutex
	tuples []firstAudioCapturedAuthorizerTuple
}

func (c *firstAudioAuthorizerCapture) install(conn *sqlite3.SQLiteConn) error {
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
		c.mu.Lock()
		c.tuples = append(c.tuples, firstAudioCapturedAuthorizerTuple{
			action: action, arg1: arg1, arg2: arg2, databaseName: databaseName, triggerName: triggerName,
		})
		c.mu.Unlock()
		// This recorder always allows callbacks and is never a security policy.
		// It is restricted to isolated temporary fixtures: the original test
		// uses EXPLAIN only, while the real-SQL tuple-discovery test executes a
		// synthetic lesson transaction in a disposable database. Never install
		// it on an app or acceptance candidate.
		return sqlite3.SQLITE_OK
	})
	return nil
}

func (c *firstAudioAuthorizerCapture) snapshot() []firstAudioCapturedAuthorizerTuple {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]firstAudioCapturedAuthorizerTuple(nil), c.tuples...)
}

func TestFirstAudioMatrixAuditTriggerUsesExactSQLiteAuthorizerTuples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-authorizer-tuples.db")
	setup, err := store.Open(path)
	if err != nil {
		t.Fatal("open setup database:", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := installFirstAudioStartupGuards(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install fixed audit trigger:", err)
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("close setup pool before authorizer capture:", err)
	}

	capture := &firstAudioAuthorizerCapture{}
	name := fmt.Sprintf("first_audio_matrix_authorizer_capture_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioProbeDriver(name, capture.install); err != nil {
		t.Fatal("register capture-only mattn driver:", err)
	}
	db, err := gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("open capture-only mattn pool:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatal("open capture connection:", err)
	}

	// EXPLAIN compiles the INSERT and its fixed BEFORE trigger but never runs
	// the bytecode, so the capture authorizer cannot change fixture data.
	rows, err := db.Raw(`EXPLAIN INSERT INTO audit_logs (id, ts, actor_type, result, trace_id) VALUES (?, ?, ?, ?, ?)`,
		7001, "2026-10-27T00:00:00Z", "matrix", "probe", "lesson-7001").Rows()
	if err != nil {
		t.Fatal("EXPLAIN audit insert:", err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal("iterate EXPLAIN rows:", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal("close EXPLAIN rows:", err)
	}

	policy := newFirstAudioStartupPolicy()
	policy.phase.Store(uint32(firstAudioStartupPreflight))
	seenSelect, seenIDRead, seenTableRead := false, false, false
	for _, tuple := range capture.snapshot() {
		if tuple.triggerName != "" && tuple.triggerName != firstAudioAuditNoReplaceTrigger {
			t.Errorf("EXPLAIN requested an unexpected nonempty trigger source: %#v", tuple)
			continue
		}
		if tuple.triggerName == "" {
			continue
		}
		if !policy.authorize(tuple.action, tuple.arg1, tuple.arg2, tuple.databaseName, tuple.triggerName) {
			t.Errorf("actual SQLite trigger tuple is not authorized by the exact policy: %#v", tuple)
		}
		switch {
		case tuple.action == sqlite3.SQLITE_SELECT:
			seenSelect = true
		case tuple.action == sqlite3.SQLITE_READ && tuple.databaseName == "main" && tuple.arg1 == "audit_logs" && tuple.arg2 == "id":
			seenIDRead = true
		case tuple.action == sqlite3.SQLITE_READ && tuple.databaseName == "" && tuple.arg1 == "audit_logs" && tuple.arg2 == "":
			seenTableRead = true
		}
	}
	if !seenSelect || !seenIDRead || !seenTableRead {
		t.Fatalf("actual authorizer callbacks omitted expected audit trigger tuple: select=%v id_read=%v table_read=%v callbacks=%#v", seenSelect, seenIDRead, seenTableRead, capture.snapshot())
	}
	if policy.authorize(sqlite3.SQLITE_INSERT, "audit_logs", "", "main", "") {
		t.Fatal("phase/source exception authorized a top-level audit INSERT")
	}
	var auditRows int64
	if err := db.Raw("SELECT count(*) FROM audit_logs").Scan(&auditRows).Error; err != nil || auditRows != 0 {
		t.Fatalf("EXPLAIN changed the audit fixture: rows=%d err=%v", auditRows, err)
	}
}

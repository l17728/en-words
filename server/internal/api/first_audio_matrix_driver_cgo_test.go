//go:build cgo

package api

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type firstAudioProbePhase uint32

const (
	firstAudioProbeDriver firstAudioProbePhase = iota + 1
	firstAudioProbeStartup
)

type firstAudioProbePolicy struct {
	phase   atomic.Uint32
	hooks   atomic.Uint64
	denials atomic.Uint64
}

var firstAudioProbeDriverSequence atomic.Uint64

func (p *firstAudioProbePolicy) install(conn *sqlite3.SQLiteConn) error {
	p.hooks.Add(1)
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
		if p.authorize(action, arg1, arg2, databaseName, triggerName) {
			return sqlite3.SQLITE_OK
		}
		p.denials.Add(1)
		return sqlite3.SQLITE_DENY
	})
	return nil
}

func (p *firstAudioProbePolicy) authorize(action int, arg1, arg2, databaseName, triggerName string) bool {
	phase := firstAudioProbePhase(p.phase.Load())
	switch action {
	case sqlite3.SQLITE_SELECT:
		return triggerName == ""
	case sqlite3.SQLITE_READ:
		if databaseName == "main" && triggerName == "" &&
			((arg1 == "fixture_probe" && arg2 == "value") ||
				(arg1 == "sqlite_master" && (arg2 == "type" || arg2 == "name" || arg2 == "tbl_name" || arg2 == "rootpage" || arg2 == "sql"))) {
			return true
		}
	case sqlite3.SQLITE_TRANSACTION:
		return triggerName == "" && (arg1 == "BEGIN" || arg1 == "COMMIT" || arg1 == "ROLLBACK")
	case sqlite3.SQLITE_FUNCTION:
		return phase == firstAudioProbeDriver && triggerName == "" && arg2 == "sqlite_version"
	}
	return false
}

func TestFirstAudioMatrixDriverInstallsPolicyOnEveryPhysicalConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-probe.db")
	setup, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`CREATE TABLE fixture_probe (value TEXT); INSERT INTO fixture_probe(value) VALUES ('synthetic')`); err != nil {
		_ = setup.Close()
		t.Fatal("unprotected test setup failed", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal("unprotected test setup close failed", err)
	}

	name := fmt.Sprintf("first_audio_matrix_probe_%d", firstAudioProbeDriverSequence.Add(1))
	policy := &firstAudioProbePolicy{}
	policy.phase.Store(uint32(firstAudioProbeDriver))
	if err := registerFirstAudioProbeDriver(name, policy.install); err != nil {
		t.Fatal("guarded driver registration failed", err)
	}
	db, err := gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("guarded GORM pool open failed", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(3)
	pool.SetMaxIdleConns(3)

	connections := make([]*sql.Conn, 0, 3)
	for range 3 {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatalf("open guarded physical connection: %v", err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	if got := policy.hooks.Load(); got != 3 {
		t.Fatalf("ConnectHook calls = %d, want one hook per physical connection (3)", got)
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatal("guarded physical connection close failed", err)
		}
	}
	connections = nil

	var value string
	if err := db.Raw("SELECT value FROM fixture_probe").Scan(&value).Error; err != nil || value != "synthetic" {
		t.Fatalf("approved synthetic read failed: value=%q err=%v", value, err)
	}
	if err := db.Exec("CREATE TABLE forbidden_probe (id INTEGER)").Error; err == nil {
		t.Fatal("guarded connection permitted schema DDL")
	}
	if err := db.Exec("UPDATE fixture_probe SET value = 'changed'").Error; err == nil {
		t.Fatal("guarded connection permitted unapproved DML")
	}
	if got := policy.denials.Load(); got < 2 {
		t.Fatalf("denials = %d, want DDL and DML denials", got)
	}
	policy.phase.Store(uint32(firstAudioProbeStartup))
	if err := db.Raw("SELECT sqlite_version()").Scan(&value).Error; err == nil {
		t.Fatal("driver-probe-only sqlite_version function remained available after phase tightening")
	}
}

func registerFirstAudioProbeDriver(name string, hook func(*sqlite3.SQLiteConn) error) error {
	if name == "" || hook == nil {
		return fmt.Errorf("driver name and policy are required")
	}
	sql.Register(name, &sqlite3.SQLiteDriver{ConnectHook: hook})
	return nil
}

//go:build cgo

package api

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/scheduler"
	"enwords/internal/store"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type firstAudioStartupPhase uint32

const (
	firstAudioStartupDriverProbe firstAudioStartupPhase = iota + 1
	firstAudioStartupApp
	firstAudioStartupPreflight
)

type firstAudioStartupPolicy struct {
	phase   atomic.Uint32
	hooks   atomic.Uint64
	denials atomic.Uint64
	rowGuardCalls atomic.Uint64
	reads   map[string]map[string]struct{}
	lastDenial atomic.Value
}

func newFirstAudioStartupPolicy() *firstAudioStartupPolicy {
	p := &firstAudioStartupPolicy{reads: firstAudioStartupReadManifest()}
	p.phase.Store(uint32(firstAudioStartupDriverProbe))
	return p
}

func firstAudioStartupReadManifest() map[string]map[string]struct{} {
	models := []any{
		&store.OverdueTask{}, &store.FeedbackTask{}, &store.FeedbackIntent{}, &store.FeedbackTodo{},
		&store.FeedbackNotice{}, &store.ConfigRevision{}, &store.RuntimeRecord{}, &store.DurableMemory{},
		&store.User{}, &store.WordBook{}, &store.WordEntry{}, &store.MemoryCard{}, &store.Lesson{},
		&store.ClassroomEvent{}, &store.WsTicket{}, &store.ConfigGlobal{}, &store.ConfigOverride{},
		&store.Feedback{}, &store.Notification{}, &store.AgentMemory{}, &store.AuditLog{}, &store.PlacementDraft{},
	}
	cache := &sync.Map{}
	manifest := make(map[string]map[string]struct{}, len(models)+2)
	for _, model := range models {
		parsed, err := schema.Parse(model, cache, schema.NamingStrategy{})
		if err != nil {
			panic("first-audio model manifest parse failed")
		}
		// SQLite reports an empty column name for SQLITE_READ callbacks that
		// implement COUNT(*) over a table. This sentinel grants row counting,
		// not access to an undeclared physical column.
		columns := map[string]struct{}{"": {}}
		for _, field := range parsed.Fields {
			if field.DBName != "" {
				columns[field.DBName] = struct{}{}
			}
		}
		manifest[parsed.Table] = columns
	}
	manifest["sqlite_master"] = map[string]struct{}{"": {}, "type": {}, "name": {}, "tbl_name": {}, "rootpage": {}, "sql": {}}
	manifest["sqlite_sequence"] = map[string]struct{}{"": {}, "name": {}, "seq": {}}
	return manifest
}

func (p *firstAudioStartupPolicy) install(conn *sqlite3.SQLiteConn) error {
	p.hooks.Add(1)
	if err := conn.RegisterFunc("first_audio_row_guard", func() bool {
		p.rowGuardCalls.Add(1)
		return false
	}, true); err != nil {
		return err
	}
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
		if p.authorize(action, arg1, arg2, databaseName, triggerName) {
			return sqlite3.SQLITE_OK
		}
		p.denials.Add(1)
		p.lastDenial.Store(fmt.Sprintf("action=%d arg1=%q arg2=%q database=%q trigger=%q phase=%d", action, arg1, arg2, databaseName, triggerName, p.phase.Load()))
		return sqlite3.SQLITE_DENY
	})
	return nil
}

var firstAudioApprovedGuardTriggers = map[string]struct{}{
	"fa_users_no_insert": {}, "fa_users_no_update": {}, "fa_users_no_delete": {},
	"fa_config_globals_no_insert": {}, "fa_config_globals_no_update": {}, "fa_config_globals_no_delete": {},
}

func (p *firstAudioStartupPolicy) authorize(action int, arg1, arg2, databaseName, triggerName string) bool {
	phase := firstAudioStartupPhase(p.phase.Load())
	switch action {
	case sqlite3.SQLITE_SELECT:
		return true
	case sqlite3.SQLITE_READ:
		_, ok := p.reads[arg1][arg2]
		// SQLite omits the database name for COUNT(*)'s table-existence
		// authorization callback. Accept that tuple only for the empty-column
		// sentinel of an explicitly manifested main-schema table.
		return ok && (databaseName == "main" || (databaseName == "" && arg2 == ""))
	case sqlite3.SQLITE_TRANSACTION:
		return triggerName == "" && (arg1 == "BEGIN" || arg1 == "COMMIT" || arg1 == "ROLLBACK")
	case sqlite3.SQLITE_FUNCTION:
		fn := strings.ToLower(arg2)
		if fn == "sqlite_version" {
			return phase == firstAudioStartupDriverProbe && triggerName == ""
		}
		if fn == "count" || fn == "substr" || fn == "coalesce" || fn == "max" {
			return true
		}
		if fn == "first_audio_row_guard" {
			_, ok := firstAudioApprovedGuardTriggers[triggerName]
			return ok
		}
	case sqlite3.SQLITE_UPDATE:
		if databaseName == "main" && triggerName == "rev_config_globals_UPDATE" && arg1 == "config_revisions" {
			return true
		}
		if databaseName != "main" {
			return false
		}
		if phase == firstAudioStartupApp && triggerName == "" && arg1 == "users" && arg2 == "id" {
			return true // NewApp's exact UPDATE users SET id=id WHERE 1=0 reservation.
		}
		if triggerName == "" && arg1 == "config_globals" && arg2 == "key" {
			return true // WithAtomicWriteObserved's zero-row reservation; row guard rejects effects.
		}
	case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_DELETE:
		if databaseName == "main" && triggerName == "rev_config_globals_UPDATE" && arg1 == "config_revisions" {
			return true
		}
	}
	return false
}

func installFirstAudioStartupGuards(db *gorm.DB) error {
	statements := []string{
		`CREATE TRIGGER fa_users_no_insert BEFORE INSERT ON users BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_users_no_update BEFORE UPDATE ON users BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_users_no_delete BEFORE DELETE ON users BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_config_globals_no_insert BEFORE INSERT ON config_globals BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_config_globals_no_update BEFORE UPDATE ON config_globals BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_config_globals_no_delete BEFORE DELETE ON config_globals BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func TestFirstAudioStartupManifestAllowsOnlySQLiteCountWithoutDatabaseName(t *testing.T) {
	policy := newFirstAudioStartupPolicy()
	policy.phase.Store(uint32(firstAudioStartupApp))
	if !policy.authorize(sqlite3.SQLITE_READ, "config_revisions", "", "", "") {
		t.Fatal("SQLite COUNT(*) callback on a manifested main table was denied")
	}
	for _, tuple := range []struct {
		table, column, database string
	}{
		{table: "unlisted_table", database: ""},
		{table: "config_revisions", column: "unknown_column", database: ""},
		{table: "config_revisions", database: "temp"},
	} {
		if policy.authorize(sqlite3.SQLITE_READ, tuple.table, tuple.column, tuple.database, "") {
			t.Fatalf("unmanifested read tuple was allowed: %#v", tuple)
		}
	}
}

func TestFirstAudioMatrixNewAppStartsOnGuardedSQLitePool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.db")
	setup, err := store.Open(path)
	if err != nil {
		t.Fatal("setup pool open failed", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		t.Fatal(err)
	}
	capability := firstAudioTestCapability()
	identity, _, err := seedFirstAudioSetupFixture(setup, capability, "startup_20261027", time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("synthetic setup failed", err)
	}
	if err := setup.Create(&store.ConfigGlobal{Key: "wordsPerLesson", Value: `60`}).Error; err != nil {
		_ = setupPool.Close()
		t.Fatal("config guard fixture seed failed", err)
	}
	var setupRevisionCount int64
	if err := setup.Model(&store.ConfigRevision{}).Count(&setupRevisionCount).Error; err != nil {
		_ = setupPool.Close()
		t.Fatal("config revision baseline read failed", err)
	}
	if err := installFirstAudioStartupGuards(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("fixed row guards installation failed", err)
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("setup pool close failed", err)
	}

	name := fmt.Sprintf("first_audio_matrix_startup_%d", firstAudioProbeDriverSequence.Add(1))
	policy := newFirstAudioStartupPolicy()
	if err := registerFirstAudioProbeDriver(name, policy.install); err != nil {
		t.Fatal("guarded app driver registration failed", err)
	}
	appDB, err := gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("guarded app pool open failed", err)
	}
	pool, err := appDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	policy.phase.Store(uint32(firstAudioStartupApp))
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatal("first guarded connection could not be opened", err)
	}
	if policy.hooks.Load() != 1 {
		t.Fatalf("hook count = %d, want 1 before app startup", policy.hooks.Load())
	}

	app := NewApp(scheduler.RealClock{}, appDB)
	if app == nil || app.DB != appDB || app.StartupError != nil {
		t.Fatalf("NewApp did not start on guarded pool: app=%v startup=%v", app != nil, app.StartupError)
	}
	user, err := app.Auth.GetUser(identity.Username)
	if err != nil || user.Role != auth.RoleStudent || user.ID != identity.StudentID {
		t.Fatalf("guarded startup did not restore synthetic student: user=%#v err=%v", user, err)
	}
	if policy.hooks.Load() != 1 {
		t.Fatalf("NewApp caused an unexpected physical connection: hooks=%d", policy.hooks.Load())
	}

	// This write is allowed to prepare by the startup reservation exception;
	// SQLite's fixed BEFORE trigger must reject it when it matches a real row.
	if err := appDB.Exec("UPDATE users SET id=id WHERE id=?", identity.StudentID).Error; err == nil {
		t.Fatal("row guard allowed a matching user update")
	}
	if err := appDB.Exec("UPDATE config_globals SET key=key WHERE key=?", "wordsPerLesson").Error; err == nil {
		t.Fatal("row guard allowed a matching config reservation update")
	}
	var revisionCount int64
	if err := appDB.Model(&store.ConfigRevision{}).Count(&revisionCount).Error; err != nil || revisionCount != setupRevisionCount {
		t.Fatalf("blocked config row change emitted revision side effect: count=%d baseline=%d err=%v last_denial=%v", revisionCount, setupRevisionCount, err, policy.lastDenial.Load())
	}
	if policy.rowGuardCalls.Load() < 2 {
		t.Fatalf("row guard calls = %d, want at least the blocked users and config writes", policy.rowGuardCalls.Load())
	}
	policy.phase.Store(uint32(firstAudioStartupPreflight))
	if err := appDB.Exec("UPDATE users SET id=id WHERE id=?", identity.StudentID).Error; err == nil {
		t.Fatal("startup-only user update reservation remained authorized after phase tightening")
	}
}


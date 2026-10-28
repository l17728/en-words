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
	phase         atomic.Uint32
	hooks         atomic.Uint64
	denials       atomic.Uint64
	rowGuardCalls atomic.Uint64
	reads         map[string]map[string]struct{}
	lastDenial    atomic.Value
}

// This policy obtains its phase exclusively from firstAudioGuardController.
// It is used by the strict NewApp integration test; the older atomic policy
// remains only for isolated allowlist unit tests and historical startup cases.
type firstAudioControllerStartupPolicy struct {
	controller    *firstAudioGuardController
	reads         map[string]map[string]struct{}
	hooks         atomic.Uint64
	denials       atomic.Uint64
	rowGuardCalls atomic.Uint64
	lastDenial    atomic.Value
}

func newFirstAudioControllerStartupPolicy(controller *firstAudioGuardController) *firstAudioControllerStartupPolicy {
	return &firstAudioControllerStartupPolicy{controller: controller, reads: firstAudioStartupReadManifest()}
}

func (p *firstAudioControllerStartupPolicy) install(conn *sqlite3.SQLiteConn) error {
	if p == nil || p.controller == nil || conn == nil {
		return fmt.Errorf("controller startup authorizer is incomplete")
	}
	p.hooks.Add(1)
	if err := conn.RegisterFunc("first_audio_row_guard", func() bool {
		p.rowGuardCalls.Add(1)
		if err := p.controller.TerminalizeFromSQLiteCallback(); err != nil {
			p.lastDenial.Store(fmt.Sprintf("callback terminal CAS failed: %v", err))
		}
		return false
	}, true); err != nil {
		return err
	}
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
		active := p.controller.publisher.Load()
		allowed := false
		switch {
		case p.controller.IsActivePhase(firstAudioGuardDriverProbe):
			allowed = p.controller.ObserveDriverProbe(conn, action, arg1, arg2, databaseName, triggerName) == nil
		case p.controller.IsActivePhase(firstAudioGuardStartupProbe):
			allowed = firstAudioStartupAuthorizeAt(firstAudioStartupApp, p.reads, action, arg1, arg2, databaseName, triggerName)
		}
		if allowed {
			return sqlite3.SQLITE_OK
		}
		p.controller.Terminalize()
		p.denials.Add(1)
		phase := firstAudioGuardTerminal
		if active != nil {
			phase = active.phase
		}
		p.lastDenial.Store(fmt.Sprintf("action=%d arg1=%q arg2=%q database=%q trigger=%q phase=%s", action, arg1, arg2, databaseName, triggerName, phase))
		return sqlite3.SQLITE_DENY
	})
	return nil
}

func newFirstAudioStartupPolicy() *firstAudioStartupPolicy {
	p := &firstAudioStartupPolicy{reads: firstAudioStartupReadManifest()}
	p.phase.Store(uint32(firstAudioStartupDriverProbe))
	return p
}

func firstAudioStartupReadManifest() map[string]map[string]struct{} {
	models := firstAudioStartupModels()
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

func firstAudioStartupModels() []any {
	return []any{
		&store.OverdueTask{}, &store.FeedbackTask{}, &store.FeedbackIntent{}, &store.FeedbackTodo{},
		&store.FeedbackNotice{}, &store.ConfigRevision{}, &store.RuntimeRecord{}, &store.DurableMemory{},
		&store.User{}, &store.WordBook{}, &store.WordEntry{}, &store.MemoryCard{}, &store.Lesson{},
		&store.ClassroomEvent{}, &store.WsTicket{}, &store.ConfigGlobal{}, &store.ConfigOverride{},
		&store.Feedback{}, &store.Notification{}, &store.AgentMemory{}, &store.AuditLog{}, &store.PlacementDraft{},
	}
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

const firstAudioAuditNoReplaceTrigger = "fa_audit_no_replace"

func (p *firstAudioStartupPolicy) authorize(action int, arg1, arg2, databaseName, triggerName string) bool {
	return firstAudioStartupAuthorizeAt(firstAudioStartupPhase(p.phase.Load()), p.reads, action, arg1, arg2, databaseName, triggerName)
}

func firstAudioStartupAuthorizeAt(phase firstAudioStartupPhase, reads map[string]map[string]struct{}, action int, arg1, arg2, databaseName, triggerName string) bool {
	switch action {
	case sqlite3.SQLITE_SELECT:
		if triggerName == "" {
			return true
		}
		if phase != firstAudioStartupApp && phase != firstAudioStartupPreflight {
			return false
		}
		_, ok := firstAudioApprovedGuardTriggers[triggerName]
		return ok || triggerName == firstAudioAuditNoReplaceTrigger
	case sqlite3.SQLITE_READ:
		if triggerName != "" {
			if phase != firstAudioStartupApp && phase != firstAudioStartupPreflight {
				return false
			}
			if databaseName != "main" {
				// SQLite reports table-existence reads from EXISTS(SELECT 1 ...)
				// with an empty database and column name, even for a fixed main DB trigger.
				// Keep this exception limited to the audit collision trigger and table.
				if triggerName == firstAudioAuditNoReplaceTrigger {
					return arg1 == "audit_logs" && arg2 == "" && databaseName == ""
				}
				return false
			}
			if triggerName == firstAudioAuditNoReplaceTrigger {
				return arg1 == "audit_logs" && arg2 == "id"
			}
			return false
		}
		_, ok := reads[arg1][arg2]
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
		if fn == "count" || fn == "coalesce" || fn == "max" {
			return (phase == firstAudioStartupApp || phase == firstAudioStartupPreflight) && arg1 == "" && triggerName == ""
		}
		if fn == "first_audio_row_guard" {
			_, ok := firstAudioApprovedGuardTriggers[triggerName]
			return ok
		}
	case sqlite3.SQLITE_UPDATE:
		if databaseName != "main" {
			return false
		}
		if phase == firstAudioStartupApp && triggerName == "" && arg1 == "users" && arg2 == "id" {
			return true // NewApp's exact UPDATE users SET id=id WHERE 1=0 reservation.
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
		`CREATE TRIGGER fa_audit_no_replace BEFORE INSERT ON audit_logs WHEN EXISTS (SELECT 1 FROM audit_logs WHERE id=NEW.id) BEGIN SELECT RAISE(ABORT,'first_audio_audit_id_collision'); END`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func TestFirstAudioMatrixAuditNoReplaceAuthorizerExceptionIsExact(t *testing.T) {
	policy := newFirstAudioStartupPolicy()
	if policy.authorize(sqlite3.SQLITE_SELECT, "", "", "", firstAudioAuditNoReplaceTrigger) ||
		policy.authorize(sqlite3.SQLITE_READ, "audit_logs", "id", "main", firstAudioAuditNoReplaceTrigger) ||
		policy.authorize(sqlite3.SQLITE_READ, "audit_logs", "", "", firstAudioAuditNoReplaceTrigger) {
		t.Fatal("audit collision trigger exceptions were allowed during driver probing")
	}
	policy.phase.Store(uint32(firstAudioStartupPreflight))
	if !policy.authorize(sqlite3.SQLITE_SELECT, "", "", "", firstAudioAuditNoReplaceTrigger) {
		t.Fatal("audit collision trigger SELECT was denied")
	}
	if !policy.authorize(sqlite3.SQLITE_READ, "audit_logs", "id", "main", firstAudioAuditNoReplaceTrigger) {
		t.Fatal("audit collision trigger ID read was denied")
	}
	if !policy.authorize(sqlite3.SQLITE_READ, "audit_logs", "", "", firstAudioAuditNoReplaceTrigger) {
		t.Fatal("audit collision trigger table-existence read was denied")
	}
	if policy.authorize(sqlite3.SQLITE_FUNCTION, "", "raise", "", firstAudioAuditNoReplaceTrigger) {
		t.Fatal("unobserved RAISE function callback must remain denied")
	}
	for _, tuple := range []struct {
		action                           int
		table, column, database, trigger string
	}{
		{sqlite3.SQLITE_SELECT, "", "", "", "unapproved_trigger"},
		{sqlite3.SQLITE_READ, "audit_logs", "actor_id", "main", firstAudioAuditNoReplaceTrigger},
		{sqlite3.SQLITE_READ, "audit_logs", "", "temp", firstAudioAuditNoReplaceTrigger},
		{sqlite3.SQLITE_READ, "audit_logs", "", "main", firstAudioAuditNoReplaceTrigger},
		{sqlite3.SQLITE_READ, "users", "", "", firstAudioAuditNoReplaceTrigger},
		{sqlite3.SQLITE_READ, "users", "id", "main", firstAudioAuditNoReplaceTrigger},
		{sqlite3.SQLITE_READ, "audit_logs", "id", "temp", firstAudioAuditNoReplaceTrigger},
		{sqlite3.SQLITE_READ, "audit_logs", "id", "main", "unapproved_trigger"},
		{sqlite3.SQLITE_FUNCTION, "", "raise", "", "unapproved_trigger"},
	} {
		if policy.authorize(tuple.action, tuple.table, tuple.column, tuple.database, tuple.trigger) {
			t.Fatalf("unapproved audit authorizer tuple was allowed: %#v", tuple)
		}
	}
}

func TestFirstAudioMatrixConfigRevisionTriggerReadsAreDenied(t *testing.T) {
	policy := newFirstAudioStartupPolicy()
	for _, phase := range []firstAudioStartupPhase{firstAudioStartupDriverProbe, firstAudioStartupApp, firstAudioStartupPreflight} {
		policy.phase.Store(uint32(phase))
		for _, tuple := range []struct {
			table, column, database, trigger string
		}{
			{"config_globals", "key", "main", "rev_config_globals_UPDATE"},
			{"config_revisions", "student_id", "main", "rev_config_globals_UPDATE"},
			{"config_revisions", "key", "main", "rev_config_globals_UPDATE"},
			{"config_revisions", "revision", "main", "rev_config_globals_UPDATE"},
			{"config_revisions", "created_at", "main", "rev_config_globals_UPDATE"},
			{"config_globals", "value", "main", "rev_config_globals_UPDATE"},
			{"audit_logs", "id", "main", "rev_config_globals_UPDATE"},
			{"config_revisions", "revision", "temp", "rev_config_globals_UPDATE"},
			{"config_revisions", "revision", "main", "rev_config_globals_INSERT"},
		} {
			if policy.authorize(sqlite3.SQLITE_READ, tuple.table, tuple.column, tuple.database, tuple.trigger) {
				t.Fatalf("config revision trigger read tuple was allowed in phase %d: %#v", phase, tuple)
			}
		}
	}
}

func TestFirstAudioMatrixAuditNoReplaceTriggerRejectsReplace(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "audit-no-replace.db"))
	if err != nil {
		t.Fatal("open audit trigger fixture:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := installFirstAudioStartupGuards(db); err != nil {
		t.Fatal("install fixed audit trigger:", err)
	}
	original := store.AuditLog{ID: 7001, TS: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), ActorType: "matrix", Result: "original", TraceID: "lesson-7001"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal("insert unused audit ID:", err)
	}
	for _, query := range []string{
		`INSERT INTO audit_logs (id, ts, actor_type, result, trace_id) VALUES (?, ?, ?, ?, ?)`,
		`INSERT OR REPLACE INTO audit_logs (id, ts, actor_type, result, trace_id) VALUES (?, ?, ?, ?, ?)`,
	} {
		if err := db.Exec(query, original.ID, original.TS, "matrix", "replacement", original.TraceID).Error; err == nil || !strings.Contains(err.Error(), "first_audio_audit_id_collision") {
			t.Fatalf("duplicate audit ID was not rejected by BEFORE trigger: query=%q err=%v", query, err)
		}
		var current store.AuditLog
		if err := db.First(&current, original.ID).Error; err != nil || current.Result != "original" {
			t.Fatalf("duplicate/replace attempt changed original audit row: row=%#v err=%v", current, err)
		}
		var count int64
		if err := db.Model(&store.AuditLog{}).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("duplicate/replace attempt changed audit row count: count=%d err=%v", count, err)
		}
	}
}

func TestFirstAudioMatrixStartupManifestAllowsOnlySQLiteCountWithoutDatabaseName(t *testing.T) {
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

func TestFirstAudioMatrixStartupPolicyDeniesConfigWrites(t *testing.T) {
	policy := newFirstAudioStartupPolicy()
	if policy.authorize(sqlite3.SQLITE_FUNCTION, "", "count", "", "") {
		t.Fatal("application count function was allowed during driver probing")
	}
	if policy.authorize(sqlite3.SQLITE_FUNCTION, "", "count", "", "unapproved_trigger") {
		t.Fatal("trigger-sourced count function was allowed")
	}
	for _, phase := range []firstAudioStartupPhase{firstAudioStartupApp, firstAudioStartupPreflight} {
		policy.phase.Store(uint32(phase))
		if !policy.authorize(sqlite3.SQLITE_FUNCTION, "", "count", "", "") {
			t.Fatalf("top-level count function denied in phase %d", phase)
		}
		for _, tuple := range []struct {
			action                           int
			table, column, database, trigger string
		}{
			{sqlite3.SQLITE_INSERT, "config_globals", "", "main", ""},
			{sqlite3.SQLITE_UPDATE, "config_globals", "key", "main", ""},
			{sqlite3.SQLITE_DELETE, "config_globals", "", "main", ""},
			{sqlite3.SQLITE_INSERT, "config_revisions", "", "main", ""},
			{sqlite3.SQLITE_UPDATE, "config_revisions", "revision", "main", ""},
			{sqlite3.SQLITE_DELETE, "config_revisions", "", "main", ""},
			{sqlite3.SQLITE_INSERT, "config_revisions", "", "main", "rev_config_globals_UPDATE"},
			{sqlite3.SQLITE_UPDATE, "config_revisions", "revision", "main", "rev_config_globals_UPDATE"},
			{sqlite3.SQLITE_DELETE, "config_revisions", "", "main", "rev_config_globals_UPDATE"},
		} {
			if policy.authorize(tuple.action, tuple.table, tuple.column, tuple.database, tuple.trigger) {
				t.Fatalf("config write tuple was allowed in phase %d: %#v", phase, tuple)
			}
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
	if err := installFirstAudioStartupGuards(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("fixed row guards installation failed", err)
	}
	if err := firstAudioValidateSchemaManifest(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("schema baseline must pass before guarded app pool opens:", err)
	}
	if err := firstAudioValidateStartupReadManifest(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("startup read manifest must match pinned schema before guarded app pool opens:", err)
	}
	baselineSnapshot, err := firstAudioCaptureDatabaseSnapshot(setup)
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("capture complete fixture snapshot:", err)
	}
	if len(baselineSnapshot.Tables) != len(firstAudioStartupModels())+1 || baselineSnapshot.SHA256 == "" {
		_ = setupPool.Close()
		t.Fatalf("fixture snapshot incomplete: tables=%d want=%d digest=%q", len(baselineSnapshot.Tables), len(firstAudioStartupModels())+1, baselineSnapshot.SHA256)
	}
	fixtureRows := make(map[string]int, len(baselineSnapshot.Tables))
	for _, table := range baselineSnapshot.Tables {
		fixtureRows[table.Name] = len(table.Rows)
	}
	for table, minimum := range map[string]int{"users": 3, "word_books": 1, "word_entries": 2, "lessons": 1} {
		if fixtureRows[table] < minimum {
			_ = setupPool.Close()
			t.Fatalf("fixture snapshot omitted synthetic %s rows: got=%d want-at-least=%d", table, fixtureRows[table], minimum)
		}
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("setup pool close failed", err)
	}

	controller := newFirstAudioGuardController()
	driverContext, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "startup_20261027", currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil {
		t.Fatal("driver-probe context construction failed:", err)
	}
	if err := controller.PublishInitial(driverContext); err != nil {
		t.Fatal("controller rejected driver-probe context:", err)
	}
	policy := newFirstAudioControllerStartupPolicy(controller)
	name := fmt.Sprintf("first_audio_matrix_controller_startup_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(conn *sqlite3.SQLiteConn) error {
		if _, err := controller.connections.Register(conn); err != nil {
			return err
		}
		return policy.install(conn)
	}, controller.connections.Retire); err != nil {
		t.Fatal("guarded controller app driver registration failed", err)
	}
	appDB, err := gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("guarded app pool open failed", err)
	}
	pool, err := appDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxOpenConns(1)
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatal("first guarded connection could not be opened", err)
	}
	if policy.hooks.Load() != 1 {
		t.Fatalf("hook count = %d, want 1 before app startup", policy.hooks.Load())
	}
	if controller.driverProbe == nil || !controller.connections.IsLive(controller.driverProbe.connectionID, controller.driverProbe.rawConnection) {
		t.Fatal("initial dialector receipt is not bound to a live tracked SQLite connection")
	}
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: driverContext.runID, currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		t.Fatal("startup context construction failed:", err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal("close driver-probe admission:", err)
	}
	if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("publish STARTUP_PROBE from the live physical dialector observation:", err)
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

	pinned, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin the sole guarded application connection for readback:", err)
	}
	startupSnapshot, snapshotErr := firstAudioCaptureDatabaseSnapshotOnPinnedConn(appDB, pinned)
	if closeErr := pinned.Close(); closeErr != nil && snapshotErr == nil {
		snapshotErr = closeErr
	}
	if snapshotErr != nil {
		t.Fatal("capture complete post-startup snapshot on the pinned connection:", snapshotErr)
	}
	if baselineSnapshot.SHA256 != startupSnapshot.SHA256 || string(baselineSnapshot.Canonical) != string(startupSnapshot.Canonical) {
		t.Fatalf("guarded NewApp startup changed the synthetic database\nbefore:\n%s\nafter:\n%s", baselineSnapshot.Canonical, startupSnapshot.Canonical)
	}
	var restoredUsers int64
	if err := appDB.Model(&store.User{}).Count(&restoredUsers).Error; err != nil || restoredUsers != int64(fixtureRows["users"]) {
		t.Fatalf("base GORM pool unusable after pinned snapshot helper: users=%d err=%v", restoredUsers, err)
	}

	// The startup-only zero-row users reservation is allowed to prepare, but the
	// fixed BEFORE trigger must reject it when it matches a real row. The first
	// unexpected SQL terminalizes this candidate, so this assertion is last.
	if err := appDB.Exec("UPDATE users SET id=id WHERE id=?", identity.StudentID).Error; err == nil {
		t.Fatal("row guard allowed a matching user update")
	}
	// The SQLite UDF only sets its lock-free terminal latch. The coordinator
	// observes the failed SQL result here and closes lifecycle admission.
	controller.Terminalize()
	if policy.rowGuardCalls.Load() != 1 {
		t.Fatalf("row guard calls = %d, want exactly one for the blocked user write", policy.rowGuardCalls.Load())
	}
	if !controller.IsTerminal() {
		t.Fatal("unexpected guarded SQL denial did not terminalize the controller")
	}
	if err := appDB.Model(&store.User{}).Count(&restoredUsers).Error; err == nil {
		t.Fatal("terminalized guarded app candidate continued accepting SQL")
	}
}

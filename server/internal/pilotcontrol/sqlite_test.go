//go:build cgo

package pilotcontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

func TestAuthorizerAllowsSQLiteRecursiveActionButDeniesUnscopedDDL(t *testing.T) {
	state := &connectionState{}
	if got := state.authorize(sqliteRecursiveAction, "", "", "", ""); got != sqlite3.SQLITE_OK {
		t.Fatalf("recursive authorizer action = %d, want SQLITE_OK", got)
	}
	if got := state.authorize(sqlite3.SQLITE_CREATE_TABLE, "unexpected", "", "", ""); got != sqlite3.SQLITE_DENY {
		t.Fatalf("unscoped CREATE TABLE action = %d, want SQLITE_DENY", got)
	}
}

func TestTrustedDerivedTableAuthorizationRequiresExactTriggerCapability(t *testing.T) {
	trusted := &connectionState{trustedTrigger: true}
	trusted.capability = capPrepareRunAllocation
	if got := trusted.authorize(sqlite3.SQLITE_UPDATE, "pilot_business_capacity_counts", "count", "main", "pilot_allocation_capacity_after_insert"); got != sqlite3.SQLITE_OK {
		t.Fatalf("validated derived-table write authorization = %d, want SQLITE_OK", got)
	}
	untrusted := &connectionState{}
	if got := untrusted.authorize(sqlite3.SQLITE_UPDATE, "pilot_business_capacity_counts", "count", "main", "pilot_allocation_capacity_after_insert"); got != sqlite3.SQLITE_DENY {
		t.Fatalf("unvalidated derived-table write authorization = %d, want SQLITE_DENY", got)
	}
	wrongCapability := &connectionState{trustedTrigger: true, capability: capResolvePrincipal}
	if got := wrongCapability.authorize(sqlite3.SQLITE_UPDATE, "pilot_business_capacity_counts", "count", "main", "pilot_allocation_capacity_after_insert"); got != sqlite3.SQLITE_DENY {
		t.Fatalf("wrong-capability derived-table write authorization = %d, want SQLITE_DENY", got)
	}
	if got := trusted.authorize(sqlite3.SQLITE_UPDATE, "pilot_business_capacity_counts", "count", "main", "unreviewed_trigger"); got != sqlite3.SQLITE_DENY {
		t.Fatalf("wrong-trigger derived-table write authorization = %d, want SQLITE_DENY", got)
	}
	if got := trusted.authorize(sqlite3.SQLITE_DELETE, "pilot_business_capacity_counts", "", "main", "pilot_allocation_capacity_after_insert"); got != sqlite3.SQLITE_DENY {
		t.Fatalf("derived-table delete authorization = %d, want SQLITE_DENY", got)
	}
	if got := trusted.authorize(sqlite3.SQLITE_UPDATE, "pilot_business_capacity_counts", "count", "main", ""); got != sqlite3.SQLITE_DENY {
		t.Fatalf("direct derived-table write authorization = %d, want SQLITE_DENY", got)
	}
}

func TestReadOnlyPragmaAllowlist(t *testing.T) {
	for _, name := range []string{"foreign_keys", "journal_mode", "integrity_check", "table_list"} {
		if !readOnlyPragma(name) {
			t.Errorf("readOnlyPragma(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"writable_schema", "user_version=3", "journal_mode=DELETE", "unknown_pragma"} {
		if readOnlyPragma(name) {
			t.Errorf("readOnlyPragma(%q) = true, want false", name)
		}
	}
}

func TestTriggerWriteAllowlist(t *testing.T) {
	allowed := []struct {
		action int
		table  string
	}{
		{sqlite3.SQLITE_INSERT, "pilot_business_capacity_counts"},
		{sqlite3.SQLITE_UPDATE, "pilot_business_capacity_counts"},
		{sqlite3.SQLITE_INSERT, "pilot_capacity_alert_events"},
	}
	for _, item := range allowed {
		if !triggerWriteAllowed(item.action, item.table) {
			t.Errorf("triggerWriteAllowed(%d, %q) = false", item.action, item.table)
		}
	}
	for _, item := range []struct {
		action int
		table  string
	}{
		{sqlite3.SQLITE_DELETE, "pilot_business_capacity_counts"},
		{sqlite3.SQLITE_UPDATE, "pilot_capacity_alert_events"},
		{sqlite3.SQLITE_INSERT, "pilot_principal_aliases"},
	} {
		if triggerWriteAllowed(item.action, item.table) {
			t.Errorf("triggerWriteAllowed(%d, %q) = true", item.action, item.table)
		}
	}
}

func TestHashNormalizedSQL(t *testing.T) {
	first := hashNormalizedSQL("CREATE TABLE item (id INTEGER);\n-- note\n")
	second := hashNormalizedSQL(" CREATE   TABLE item (id INTEGER) ; /* note */ ")
	if first != second {
		t.Fatalf("equivalent DDL hashes differ: %s != %s", first, second)
	}
	if first == hashNormalizedSQL("CREATE TABLE item (id TEXT)") {
		t.Fatal("different DDL produced the same normalized hash")
	}
}

func TestHashNormalizedSQLPreservesQuotedTokenContents(t *testing.T) {
	for _, pair := range [][2]string{
		{"CREATE TABLE t(x TEXT DEFAULT 'journal anchor cannot be deleted')", "CREATE TABLE t(x TEXT DEFAULT 'journal  anchor cannot be deleted')"},
		{"CREATE TABLE t(x TEXT DEFAULT 'student')", "CREATE TABLE t(x TEXT DEFAULT 'stu/*x*/dent')"},
		{"CREATE TABLE t(x TEXT DEFAULT 'it''s ready')", "CREATE TABLE t(x TEXT DEFAULT 'it''s  ready')"},
		{"CREATE TABLE t(`student name` TEXT)", "CREATE TABLE t(`student  name` TEXT)"},
		{"CREATE TABLE t([student name] TEXT)", "CREATE TABLE t([student  name] TEXT)"},
	} {
		if hashNormalizedSQL(pair[0]) == hashNormalizedSQL(pair[1]) {
			t.Errorf("semantically distinct quoted SQL normalized to the same hash:\n%q\n%q", pair[0], pair[1])
		}
	}
	if got, want := normalizeSchemaSQL("CREATE/* between */TABLE t(x TEXT)"), "CREATE TABLE t(x TEXT)"; got != want {
		t.Fatalf("comment between tokens normalized to %q, want %q", got, want)
	}
}

func TestReadOnlySQLiteURIEncodesWindowsDriveAsPath(t *testing.T) {
	got := readOnlySQLiteURI(`C:\pilot data\ledger.sqlite`)
	want := "file:///C:/pilot%20data/ledger.sqlite?mode=ro"
	if got != want {
		t.Fatalf("readOnlySQLiteURI() = %q, want %q", got, want)
	}
}

func TestQuerySingleValueEnforcesOneColumnAndOneRow(t *testing.T) {
	raw, err := (&sqlite3.SQLiteDriver{}).Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn, ok := raw.(*sqlite3.SQLiteConn)
	if !ok {
		t.Fatalf("driver connection type = %T", raw)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if got, err := querySingleValue(conn, "SELECT 'value'"); err != nil || got != "value" {
		t.Fatalf("single-value query = %q, %v", got, err)
	}
	for _, query := range []string{"SELECT 1, 2", "SELECT 1 UNION ALL SELECT 2", "SELECT FROM"} {
		if _, err := querySingleValue(conn, query); err == nil {
			t.Errorf("querySingleValue(%q) unexpectedly succeeded", query)
		}
	}
}

func TestOpenRejectsUnrecognizedSchemaWithoutMutatingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := sql.OpenDB(&sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: path})
	_, err := db.Exec(`CREATE TABLE run_root_ledger_meta(singleton INTEGER,schema_version INTEGER);
INSERT INTO run_root_ledger_meta VALUES(1,3);
CREATE TABLE pilot_business_capacity_counts(value INTEGER);
CREATE TABLE pilot_capacity_alert_events(value INTEGER);
CREATE TRIGGER pilot_allocation_capacity_after_insert AFTER INSERT ON run_root_ledger_meta BEGIN
  UPDATE pilot_business_capacity_counts SET value=value+1;
END;
PRAGMA user_version=3;`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, 2); err == nil {
		t.Fatal("Open accepted an unrecognized schema")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("read-only preflight mutated the rejected database")
	}
}

func TestEmbeddedSchemaInventoryMatchesPinnedContentHash(t *testing.T) {
	if _, err := loadApprovedSchemaInventory(); err != nil {
		t.Fatalf("embedded schema inventory failed pin validation: %v", err)
	}
	original := schemaInventoryJSON[0]
	schemaInventoryJSON[0] = ' '
	defer func() { schemaInventoryJSON[0] = original }()
	if _, err := loadApprovedSchemaInventory(); err == nil {
		t.Fatal("embedded schema inventory accepted modified bytes with unchanged metadata")
	}
}

func TestOpenRejectsUnexpectedPersistentView(t *testing.T) {
	t.Skip(unapprovedDraftV3SchemaReason)
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createDraftV3SchemaFixture(t, path)
	db := sql.OpenDB(&sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: path})
	if _, err := db.Exec(`CREATE VIEW unexpected_pilot_view AS SELECT schema_version FROM run_root_ledger_meta`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, 2); err == nil {
		t.Fatal("Open accepted an unexpected persistent view")
	}
}

func TestOpenRejectsUnexpectedSQLiteUserVersionWithoutMutation(t *testing.T) {
	t.Skip(unapprovedDraftV3SchemaReason)
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createDraftV3SchemaFixture(t, path)
	db := sql.OpenDB(&sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: path})
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, 2); err == nil {
		t.Fatal("Open accepted user_version 4; expected the pinned v3 schema")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("read-only schema rejection mutated the database")
	}
}

func TestConfigureConnectionRejectsDatabaseNotAlreadyInWALMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-wal.sqlite")
	raw, err := (&sqlite3.SQLiteDriver{}).Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = openWithOptions(context.Background(), path, 1, storeOptions{skipPreflight: true})
	if err == nil || !strings.Contains(err.Error(), "must already use WAL mode") {
		t.Fatalf("opening a non-WAL database error = %v, want fail-closed WAL mode error", err)
	}
}

func TestSQLiteConnectorHonorsCanceledContextBeforeOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-be-created.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connector := &sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: path}
	if _, err := connector.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Connect created/opened database path: stat error = %v", err)
	}
}

func TestSQLiteConnectorRejectsMissingAuthorizerState(t *testing.T) {
	store := &Store{states: make(map[*sqlite3.SQLiteConn]*connectionState)}
	connector := &sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: ":memory:", store: store}
	if _, err := connector.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "connection hook state missing") {
		t.Fatalf("Connect error = %v, want missing authorizer state error", err)
	}
}

func TestSQLiteConnectorRejectsConnectionAfterStoreClose(t *testing.T) {
	store := &Store{states: make(map[*sqlite3.SQLiteConn]*connectionState), closed: true}
	driver := &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		store.mu.Lock()
		store.states[conn] = &connectionState{}
		store.mu.Unlock()
		return nil
	}}
	connector := &sqliteConnector{driver: driver, dsn: ":memory:", store: store}
	if _, err := connector.Connect(context.Background()); !errors.Is(err, errStoreClosed) {
		t.Fatalf("Connect error = %v, want errStoreClosed", err)
	}
}

func TestStoreCloseIsIdempotentAndFencesLaterCapabilities(t *testing.T) {
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil Store.Close() = %v, want nil", err)
	}
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createAuthorizerFixture(t, path)
	s, err := openWithOptions(context.Background(), path, 1, storeOptions{skipPreflight: true, trustedTriggerForTests: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() = %v, want idempotent nil", err)
	}
	if err := s.withCapability(context.Background(), capResolvePrincipal, func(*sql.Tx) error {
		t.Fatal("closed store invoked the capability callback")
		return nil
	}); !errors.Is(err, errStoreClosed) {
		t.Fatalf("capability after Close() error = %v, want errStoreClosed", err)
	}
}

func TestOpenAcceptsExactPinnedSchemaFixture(t *testing.T) {
	t.Skip(unapprovedDraftV3SchemaReason)
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createDraftV3SchemaFixture(t, path)
	s, err := Open(context.Background(), path, 2)
	if err != nil {
		t.Fatalf("Open rejected exact pinned schema fixture: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close validated pool: %v", err)
	}
}

func TestProtectedConnectionRevalidatesSchemaAfterReadOnlyPreflight(t *testing.T) {
	t.Skip(unapprovedDraftV3SchemaReason)
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createDraftV3SchemaFixture(t, path)
	_, err := openWithOptions(context.Background(), path, 1, storeOptions{
		trustedTrigger: true,
		beforeValidate: func(conn *sqlite3.SQLiteConn) error {
			_, err := conn.Exec("CREATE VIEW unexpected_pilot_view AS SELECT schema_version FROM run_root_ledger_meta", nil)
			return err
		},
	})
	if err == nil {
		t.Fatal("protected pool accepted a schema changed after read-only preflight")
	}
}

const unapprovedDraftV3SchemaReason = "draft schema fixture NOT RUN: 103-object candidate is not reconciled with the approved 95-object inventory; pending docs/65 canonical input approval"

func createDraftV3SchemaFixture(t *testing.T, path string) {
	t.Helper()
	// This is only a disposable candidate fixture. Tests must not pass it to the
	// production Open validator until the 103-object schema is approved.
	dllPath := filepath.Join("..", "..", "..", "review", "pilot-control-ledger-v3-literal-ddl-draft-20261021.sql")
	ddl, err := os.ReadFile(dllPath)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: path})
	defer db.Close()
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO run_root_ledger_meta(singleton,schema_version) VALUES(1,3); PRAGMA user_version=3; PRAGMA journal_mode=WAL;"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsClosedWhenConnectHookFailsDuringPoolWarmup(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	wantErr := errors.New("injected authorizer install failure")
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createAuthorizerFixture(t, path)
	_, err := openWithOptions(context.Background(), path, 4, storeOptions{skipPreflight: true, trustedTrigger: true,
		connectHook: func(*sqlite3.SQLiteConn) error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 3 {
				return wantErr
			}
			return nil
		},
	})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Open error = %v, want wrapped hook failure", err)
	}
	if calls < 3 {
		t.Fatalf("ConnectHook calls = %d, want pool warmup to exercise later connection", calls)
	}
}

func TestLaterConnectHookFailureFencesExistingStore(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	wantErr := errors.New("injected replacement connection failure")
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createAuthorizerFixture(t, path)
	s, err := openWithOptions(context.Background(), path, 1, storeOptions{skipPreflight: true, trustedTrigger: true,
		connectHook: func(*sqlite3.SQLiteConn) error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 2 {
				return wantErr
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.SetMaxIdleConns(0) // Force replacement of the validated startup connection.
	if _, err := s.db.Conn(context.Background()); err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("replacement connection error = %v, want hook error", err)
	}
	if err := s.withCapability(context.Background(), capResolvePrincipal, func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("store admitted an operation after a later physical connection hook failed")
	}
}

func TestAuthorizerInstalledOnEveryPhysicalPoolConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createAuthorizerFixture(t, path)
	s, err := openWithOptions(context.Background(), path, 8, storeOptions{skipPreflight: true, trustedTriggerForTests: true, connectHook: func(conn *sqlite3.SQLiteConn) error {
		_, err := conn.Exec("ATTACH DATABASE ':memory:' AS attached", nil)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	connections := make([]*sql.Conn, 0, 8)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	physical := make(map[*protectedConn]struct{})
	for range 8 {
		conn, err := s.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		if err := conn.Raw(func(raw any) error {
			protected, ok := raw.(*protectedConn)
			if !ok {
				return fmt.Errorf("unexpected driver conn %T", raw)
			}
			physical[protected] = struct{}{}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(physical) != 8 {
		t.Fatalf("distinct physical connections = %d, want 8", len(physical))
	}

	for i, conn := range connections {
		expectAuthorizerDenial(t, conn, fmt.Sprintf("INSERT INTO pilot_principal_aliases(value) VALUES (%d)", i))
		for _, statement := range []string{
			"UPDATE pilot_principal_aliases SET value=2",
			"DELETE FROM pilot_principal_aliases",
			"INSERT INTO pilot_business_capacity_counts(value) VALUES(9)",
			"UPDATE pilot_business_capacity_counts SET value=9",
			"DELETE FROM pilot_business_capacity_counts",
			"INSERT INTO pilot_capacity_alert_events(value) VALUES(9)",
			"UPDATE pilot_capacity_alert_events SET value=9",
			"DELETE FROM pilot_capacity_alert_events",
			"CREATE TABLE forbidden(value INTEGER)",
			"CREATE TEMP TABLE forbidden_temp(value INTEGER)",
			"ATTACH DATABASE ':memory:' AS forbidden_db",
			"DETACH DATABASE attached",
			"PRAGMA user_version=7",
		} {
			expectAuthorizerDenial(t, conn, statement)
		}
		if _, err := conn.ExecContext(context.Background(), "SELECT count(*) FROM pilot_principal_aliases"); err != nil {
			t.Fatalf("physical connection %d rejected read: %v", i, err)
		}
	}
	// Release every physical connection before withCapability acquires its
	// transaction connection. The pool is intentionally capped at eight, so
	// retaining all eight sql.Conn handles here would block forever.
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("release verified physical connection: %v", err)
		}
	}
	if err := s.withCapability(context.Background(), capPrepareRunAllocation, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO run_epoch_allocations(value) VALUES (99)")
		if err != nil {
			return err
		}
		for _, statement := range []string{
			"INSERT INTO pilot_business_capacity_counts(value) VALUES (99)",
			"UPDATE pilot_business_capacity_counts SET value=99",
			"INSERT INTO pilot_capacity_alert_events(value) VALUES (99)",
			"DELETE FROM pilot_business_capacity_counts",
			"DELETE FROM pilot_capacity_alert_events",
		} {
			if _, err := tx.ExecContext(context.Background(), statement); !isAuthorizerDenial(err) {
				return fmt.Errorf("direct derived-table statement %q error = %v, want SQLITE_AUTH", statement, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("typed capability did not permit exact insert: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT value FROM pilot_business_capacity_counts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("approved trigger did not update capacity count: value=%d err=%v", count, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM pilot_capacity_alert_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("approved trigger did not insert alert event: count=%d err=%v", count, err)
	}
}

func TestCapabilityIsClearedAfterSuccessErrorAndPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	createAuthorizerFixture(t, path)
	s, err := openWithOptions(context.Background(), path, 1, storeOptions{skipPreflight: true, trustedTriggerForTests: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.withCapability(context.Background(), capResolvePrincipal, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO pilot_principal_aliases(value) VALUES (1)")
		return err
	}); err != nil {
		t.Fatalf("success path failed: %v", err)
	}
	if err := assertRawInsertDenied(s); err != nil {
		t.Fatalf("success leaked capability: %v", err)
	}

	injected := errors.New("injected method error")
	if err := s.withCapability(context.Background(), capResolvePrincipal, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO pilot_principal_aliases(value) VALUES (2)")
		if err != nil {
			return err
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("error path = %v, want injected error", err)
	}
	if err := assertRawInsertDenied(s); err != nil {
		t.Fatalf("error path leaked capability: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM pilot_principal_aliases").Scan(&count); err != nil || count != 1 {
		t.Fatalf("returned-error path failed to roll back: count=%d err=%v", count, err)
	}

	func() {
		defer func() {
			if got := recover(); got != "injected panic" {
				t.Errorf("recovered panic = %v", got)
			}
		}()
		_ = s.withCapability(context.Background(), capResolvePrincipal, func(tx *sql.Tx) error {
			_, _ = tx.ExecContext(context.Background(), "INSERT INTO pilot_principal_aliases(value) VALUES (3)")
			panic("injected panic")
		})
	}()
	if err := assertRawInsertDenied(s); err != nil {
		t.Fatalf("panic path leaked capability: %v", err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM pilot_principal_aliases").Scan(&count); err != nil || count != 1 {
		t.Fatalf("panic path failed to roll back: count=%d err=%v", count, err)
	}
}

func assertRawInsertDenied(s *Store) error {
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), "INSERT INTO pilot_principal_aliases(value) VALUES (77)")
	if !isAuthorizerDenial(err) {
		return fmt.Errorf("unscoped insert error = %v, want SQLITE_AUTH", err)
	}
	return nil
}

func expectAuthorizerDenial(t *testing.T, conn *sql.Conn, statement string) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), statement)
	if !isAuthorizerDenial(err) {
		t.Fatalf("statement %q error = %v, want SQLITE_AUTH", statement, err)
	}
}

func isAuthorizerDenial(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrAuth
}

func createAuthorizerFixture(t *testing.T, path string) {
	t.Helper()
	db := sql.OpenDB(&sqliteConnector{driver: &sqlite3.SQLiteDriver{}, dsn: path})
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pilot_principal_aliases(value INTEGER NOT NULL);
CREATE TABLE pilot_business_capacity_counts(value INTEGER NOT NULL);
CREATE TABLE pilot_capacity_alert_events(value INTEGER NOT NULL);
CREATE TABLE run_epoch_allocations(value INTEGER NOT NULL);
INSERT INTO pilot_business_capacity_counts(value) VALUES(0);
CREATE TRIGGER pilot_allocation_capacity_after_insert AFTER INSERT ON run_epoch_allocations BEGIN
  UPDATE pilot_business_capacity_counts SET value=value+1;
  INSERT INTO pilot_capacity_alert_events(value) VALUES(new.value);
END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
}

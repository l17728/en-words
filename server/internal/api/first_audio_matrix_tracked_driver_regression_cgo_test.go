//go:build cgo

package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

func TestFirstAudioTrackedSQLiteDriverPreservesIdentityInterfacesAndRetiresOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracked-driver.db")
	setup, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal("open setup database:", err)
	}
	if _, err := setup.Exec(`CREATE TABLE probe_values (value TEXT NOT NULL)`); err != nil {
		_ = setup.Close()
		t.Fatal("create synthetic setup table:", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal("close setup database:", err)
	}

	var mu sync.Mutex
	var hooked *sqlite3.SQLiteConn
	var hookCount, retireCount atomic.Uint64
	retiredWrongIdentity := atomic.Bool{}
	name := fmt.Sprintf("first_audio_matrix_tracked_driver_regression_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(conn *sqlite3.SQLiteConn) error {
		mu.Lock()
		hooked = conn
		mu.Unlock()
		hookCount.Add(1)
		return nil
	}, func(conn *sqlite3.SQLiteConn) {
		mu.Lock()
		defer mu.Unlock()
		if conn != hooked {
			retiredWrongIdentity.Store(true)
		}
		retireCount.Add(1)
	}); err != nil {
		t.Fatal("register tracked SQLite driver:", err)
	}

	db, err := sql.Open(name, path)
	if err != nil {
		t.Fatal("open tracked SQLite pool:", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal("acquire tracked SQL connection:", err)
	}

	var tracked *firstAudioTrackedSQLiteConn
	var rawIdentity *sqlite3.SQLiteConn
	if err := conn.Raw(func(dc any) error {
		var ok bool
		tracked, ok = dc.(*firstAudioTrackedSQLiteConn)
		if !ok || tracked == nil {
			t.Fatalf("Conn.Raw driver connection type = %T, want tracked wrapper", dc)
		}
		rawIdentity = tracked.raw
		if rawIdentity == nil {
			t.Fatal("tracked wrapper did not retain the concrete SQLite connection identity")
		}
		mu.Lock()
		defer mu.Unlock()
		if rawIdentity != hooked {
			t.Fatalf("Conn.Raw SQLite identity %p differs from ConnectHook identity %p", rawIdentity, hooked)
		}
		return nil
	}); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatal("inspect tracked raw connection:", err)
	}
	if got := hookCount.Load(); got != 1 {
		t.Fatalf("ConnectHook calls = %d, want one physical connection", got)
	}

	assertDriverOptionalInterfaces(t, tracked)
	if _, ok := tracked.Conn.(driver.ConnPrepareContext); !ok {
		t.Fatal("underlying SQLite connection lacks ConnPrepareContext required by wrapper")
	}
	if _, ok := tracked.Conn.(driver.ConnBeginTx); !ok {
		t.Fatal("underlying SQLite connection lacks ConnBeginTx required by wrapper")
	}
	if _, ok := tracked.Conn.(driver.ExecerContext); !ok {
		t.Fatal("underlying SQLite connection lacks ExecerContext required by wrapper")
	}
	if _, ok := tracked.Conn.(driver.QueryerContext); !ok {
		t.Fatal("underlying SQLite connection lacks QueryerContext required by wrapper")
	}
	if _, ok := tracked.Conn.(driver.Pinger); !ok {
		t.Fatal("underlying SQLite connection lacks Pinger required by wrapper")
	}
	// mattn/go-sqlite3 does not implement these optional interfaces. The
	// wrapper must preserve database/sql fallback semantics instead of claiming
	// a successful reset/check that the underlying driver never performed.
	if err := tracked.ResetSession(ctx); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("unsupported inner SessionResetter should return ErrSkip, got %v", err)
	}
	if !tracked.IsValid() {
		t.Fatal("unsupported inner Validator should leave the connection valid")
	}
	if err := tracked.CheckNamedValue(&driver.NamedValue{}); !errors.Is(err, driver.ErrSkip) {
		t.Fatalf("unsupported inner NamedValueChecker should return ErrSkip, got %v", err)
	}

	if _, err := conn.ExecContext(ctx, `INSERT INTO probe_values(value) VALUES (?)`, "forwarded"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatal("ExecContext did not forward through wrapper:", err)
	}
	var got string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM probe_values`).Scan(&got); err != nil || got != "forwarded" {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("QueryContext forwarding returned value=%q err=%v", got, err)
	}

	if err := conn.Close(); err != nil {
		_ = db.Close()
		t.Fatal("return SQL connection to pool:", err)
	}
	if got := retireCount.Load(); got != 0 {
		t.Fatalf("retirement callbacks after *sql.Conn.Close() = %d, want 0 (connection should return to pool)", got)
	}

	reused, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal("reacquire pooled SQL connection:", err)
	}
	if err := reused.Raw(func(dc any) error {
		if dc != tracked {
			t.Errorf("pooled connection wrapper changed: got %p want %p", dc, tracked)
		}
		if wrapped, ok := dc.(*firstAudioTrackedSQLiteConn); !ok || wrapped.raw != rawIdentity {
			t.Errorf("reused raw SQLite identity changed: %#v", dc)
		}
		return nil
	}); err != nil {
		_ = reused.Close()
		_ = db.Close()
		t.Fatal("inspect reused pooled connection:", err)
	}
	if err := reused.Close(); err != nil {
		_ = db.Close()
		t.Fatal("return reused SQL connection:", err)
	}
	if got := retireCount.Load(); got != 0 {
		t.Fatalf("retirement callbacks before pool close = %d, want 0", got)
	}

	if err := db.Close(); err != nil {
		t.Fatal("close tracked pool:", err)
	}
	if got := retireCount.Load(); got != 1 {
		t.Fatalf("retirement callbacks after physical pool close = %d, want exactly one", got)
	}
	if retiredWrongIdentity.Load() {
		t.Fatal("retirement callback received a different physical SQLite identity")
	}
	if err := tracked.Close(); err != nil {
		t.Fatal("repeat wrapper Close after pool retirement:", err)
	}
	if got := retireCount.Load(); got != 1 {
		t.Fatalf("retirement callbacks after repeated wrapper Close = %d, want exactly one", got)
	}
}

func TestFirstAudioTrackedSQLiteDriverRetiresRegistrationWhenConnectHookFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-failure.db")
	registry := newFirstAudioSQLiteConnectionRegistry()
	var raw *sqlite3.SQLiteConn
	name := fmt.Sprintf("first_audio_matrix_tracked_hook_failure_%d", firstAudioProbeDriverSequence.Add(1))
	hookFailure := errors.New("synthetic post-registration hook failure")
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(conn *sqlite3.SQLiteConn) error {
		raw = conn
		if _, err := registry.Register(conn); err != nil {
			return err
		}
		return hookFailure
	}, registry.Retire); err != nil {
		t.Fatal("register tracked SQLite driver:", err)
	}
	db, err := sql.Open(name, path)
	if err != nil {
		t.Fatal("open tracked SQLite database/sql pool:", err)
	}
	defer db.Close()
	if _, err := db.Conn(context.Background()); !errors.Is(err, hookFailure) {
		t.Fatalf("physical open after hook failure = %v, want synthetic hook error", err)
	}
	if raw == nil {
		t.Fatal("ConnectHook did not expose the registered raw SQLite identity")
	}
	if _, live := registry.Lookup(raw); live {
		t.Fatal("ConnectHook failure left a registered-but-closed SQLite connection")
	}
}

func TestFirstAudioSQLiteConnectionRegistryRetirementWaitsForLiveOperation(t *testing.T) {
	driverConn, err := (&sqlite3.SQLiteDriver{}).Open(":memory:")
	if err != nil {
		t.Fatal("open isolated SQLite connection:", err)
	}
	defer driverConn.Close()
	raw, ok := driverConn.(*sqlite3.SQLiteConn)
	if !ok {
		t.Fatalf("SQLite driver connection type=%T", driverConn)
	}
	registry := newFirstAudioSQLiteConnectionRegistry()
	registration, err := registry.Register(raw)
	if err != nil {
		t.Fatal("register isolated SQLite connection:", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- registry.WithLiveRegistration(registration.ID, raw, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("live operation did not acquire the registry lock")
	}
	retired := make(chan struct{})
	retireAttempted := make(chan struct{})
	go func() {
		close(retireAttempted)
		registry.Retire(raw)
		close(retired)
	}()
	select {
	case <-retireAttempted:
	case <-time.After(time.Second):
		t.Fatal("retirement goroutine did not start")
	}
	select {
	case <-retired:
		t.Fatal("physical retirement crossed a live operation before it released the registry lock")
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-operationDone:
		if err != nil {
			t.Fatal("live operation failed:", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live operation did not finish after release")
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("physical retirement did not proceed after live operation completed")
	}
	if registry.IsLive(registration.ID, raw) {
		t.Fatal("retirement did not remove the physical identity")
	}
}

func assertDriverOptionalInterfaces(t *testing.T, conn *firstAudioTrackedSQLiteConn) {
	t.Helper()
	if conn == nil {
		t.Fatal("tracked connection is nil")
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"ConnPrepareContext", implementsConnPrepareContext(conn)},
		{"ConnBeginTx", implementsConnBeginTx(conn)},
		{"ExecerContext", implementsExecerContext(conn)},
		{"QueryerContext", implementsQueryerContext(conn)},
		{"Pinger", implementsPinger(conn)},
		{"SessionResetter", implementsSessionResetter(conn)},
		{"Validator", implementsValidator(conn)},
		{"NamedValueChecker", implementsNamedValueChecker(conn)},
	}
	for _, check := range checks {
		if !check.ok {
			t.Errorf("tracked SQLite wrapper does not implement %s", check.name)
		}
	}
}

func implementsConnPrepareContext(value any) bool {
	_, ok := value.(driver.ConnPrepareContext)
	return ok
}

func implementsConnBeginTx(value any) bool {
	_, ok := value.(driver.ConnBeginTx)
	return ok
}

func implementsExecerContext(value any) bool {
	_, ok := value.(driver.ExecerContext)
	return ok
}

func implementsQueryerContext(value any) bool {
	_, ok := value.(driver.QueryerContext)
	return ok
}

func implementsPinger(value any) bool {
	_, ok := value.(driver.Pinger)
	return ok
}

func implementsSessionResetter(value any) bool {
	_, ok := value.(driver.SessionResetter)
	return ok
}

func implementsValidator(value any) bool {
	_, ok := value.(driver.Validator)
	return ok
}

func implementsNamedValueChecker(value any) bool {
	_, ok := value.(driver.NamedValueChecker)
	return ok
}

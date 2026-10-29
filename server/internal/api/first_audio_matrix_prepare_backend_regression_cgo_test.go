//go:build cgo

package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

func TestFirstAudioPreflightPrepareRejectedConnectHookRetiresRegistration(t *testing.T) {
	registry := newFirstAudioSQLiteConnectionRegistry()
	var rejectedRaw atomic.Pointer[sqlite3.SQLiteConn]
	var retireCalls atomic.Uint64
	name := fmt.Sprintf("first_audio_prepare_rejected_hook_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(raw *sqlite3.SQLiteConn) error {
		registration, err := registry.Register(raw)
		if err != nil {
			return err
		}
		rejectedRaw.Store(registration.Raw)
		return errors.New("reject_connect_hook_phase")
	}, func(raw *sqlite3.SQLiteConn) {
		retireCalls.Add(1)
		registry.Retire(raw)
	}); err != nil {
		t.Fatal("register tracked driver:", err)
	}
	db, err := sql.Open(name, ":memory:")
	if err != nil {
		t.Fatal("open database handle:", err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err == nil || !strings.Contains(err.Error(), "reject_connect_hook_phase") {
		t.Fatalf("rejected ConnectHook ping error=%v", err)
	}
	raw := rejectedRaw.Load()
	if raw == nil || retireCalls.Load() != 1 {
		t.Fatalf("rejected raw=%p retire calls=%d, want one retired raw", raw, retireCalls.Load())
	}
	if _, ok := registry.Lookup(raw); ok {
		t.Fatal("rejected ConnectHook left a live registry identity")
	}
}

func firstAudioPrepareTestEnvironment(t *testing.T, runID string) (string, string, []byte) {
	t.Helper()
	capability := firstAudioTestCapability()
	artifactRoot := filepath.Join(t.TempDir(), ".test-artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal("create test artifact root")
	}
	runRoot := filepath.Join(artifactRoot, "first-audio-matrix-"+runID)
	if runtime.GOOS == "windows" {
		if err := createFirstAudioPrivateTestDirectory(runRoot); err != nil {
			t.Fatal("create private test run root")
		}
	} else if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal("create private test run root")
	}
	dbPath := filepath.Join(runRoot, "matrix.db")
	t.Setenv(firstAudioHarnessGateEnv, firstAudioHarnessGate)
	t.Setenv(firstAudioHarnessCapEnv, base64.RawURLEncoding.EncodeToString(capability))
	t.Setenv(firstAudioHarnessRunEnv, runID)
	t.Setenv(firstAudioHarnessDBEnv, dbPath)
	return runRoot, dbPath, capability
}

func TestFirstAudioGuardedAppPrepareRealSQL(t *testing.T) {
	const runID = "prepare_20261029_1"
	runRoot, dbPath, capability := firstAudioPrepareTestEnvironment(t, runID)
	deps := defaultFirstAudioPrepareDependencies()
	owner, err := prepareFirstAudioGuardedOwner(t, context.Background(), runRoot, dbPath, runID, capability, deps)
	if err != nil {
		t.Fatalf("guarded preparation failed: category=%v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if owner == nil || owner.app == nil || owner.pool == nil || owner.db == nil {
		t.Fatal("successful preparation did not retain the guarded App and pool")
	}
	if !owner.controller.IsActivePhase(firstAudioGuardPreflight) || owner.controller.IsTerminal() {
		t.Fatal("successful preparation did not stop in active PREFLIGHT")
	}
	if len(owner.opened) != firstAudioGuardedPreparePoolCapacity || len(owner.connections) != firstAudioGuardedPreparePoolCapacity {
		t.Fatalf("physical connection count mismatch: opened=%d registered=%d", len(owner.opened), len(owner.connections))
	}
	if len(owner.receipts) != firstAudioGuardedPreparePoolCapacity {
		t.Fatalf("probe receipt count mismatch: got=%d want=%d", len(owner.receipts), firstAudioGuardedPreparePoolCapacity)
	}
	for _, receipt := range owner.receipts {
		if receipt == nil || firstAudioGuardProbeReceiptState(receipt.state.Load()) != firstAudioGuardProbeReadbackVerified {
			t.Fatal("a physical connection lacks a completed PREFLIGHT readback receipt")
		}
	}
	for id, connection := range owner.connections {
		if id == "" || connection.tracked == nil || !owner.registry.IsLive(id, connection.tracked.raw) {
			t.Fatal("prepared connection identity is not live in the registry")
		}
		if id == owner.dialectorConnectionID {
			if connection.registrationPhase != firstAudioGuardDriverProbe || connection.registrationVersion != 0 {
				t.Fatal("dialector connection has incorrect registration provenance")
			}
		} else if connection.registrationPhase != firstAudioGuardPreflightPrepare || connection.registrationVersion != 2 {
			t.Fatal("expanded connection was not first registered in PREFLIGHT_PREPARE")
		}
		if connection.authorizer.denials.Load() != 0 {
			t.Fatal("prepared connection has an unexpected authorizer denial")
		}
	}
	if owner.listenerCalls.Load() != 0 {
		t.Fatal("preparation created or attempted a listener")
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close prepared owner: category=%v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("idempotent close returned error: category=%v", err)
	}
	if owner.registryCount() != 0 {
		t.Fatal("close left physical identities registered")
	}
}

func TestFirstAudioGuardedAppPrepareRejectsInvalidCapabilityBeforeOpen(t *testing.T) {
	const runID = "prepare_20261029_2"
	runRoot, dbPath, capability := firstAudioPrepareTestEnvironment(t, runID)
	badCapability := append([]byte(nil), capability...)
	badCapability[0] ^= 0xff
	deps := defaultFirstAudioPrepareDependencies()
	var setupOpenCalls, guardedOpenCalls, reserveCalls atomic.Uint64
	deps.reserveDB = func(path string) error { reserveCalls.Add(1); return errors.New("unexpected") }
	deps.setupOpen = func(string) (*gorm.DB, error) { setupOpenCalls.Add(1); return nil, errors.New("unexpected") }
	deps.guardedOpen = func(string, string) (*gorm.DB, error) { guardedOpenCalls.Add(1); return nil, errors.New("unexpected") }
	if _, err := prepareFirstAudioGuardedOwner(t, context.Background(), runRoot, dbPath, runID, badCapability, deps); err == nil {
		t.Fatal("mismatched capability was accepted")
	}
	for _, b := range badCapability {
		if b != 0 {
			t.Fatal("rejected capability buffer was not zeroized")
		}
	}
	if setupOpenCalls.Load() != 0 || guardedOpenCalls.Load() != 0 || reserveCalls.Load() != 0 {
		t.Fatalf("invalid capability reached filesystem/SQLite: reserve=%d setup_open=%d guarded_open=%d", reserveCalls.Load(), setupOpenCalls.Load(), guardedOpenCalls.Load())
	}
}

func TestFirstAudioGuardedAppPrepareNewAppPanicRollsBackAndClosesWithoutListener(t *testing.T) {
	const runID = "prepare_20261029_4"
	runRoot, dbPath, capability := firstAudioPrepareTestEnvironment(t, runID)
	deps := defaultFirstAudioPrepareDependencies()
	var guarded *gorm.DB
	var listenerCalls atomic.Uint64
	baseGuardedOpen := deps.guardedOpen
	deps.guardedOpen = func(name, path string) (*gorm.DB, error) {
		db, err := baseGuardedOpen(name, path)
		guarded = db
		return db, err
	}
	deps.newApp = func(db *gorm.DB) *App {
		_ = db.Transaction(func(*gorm.DB) error {
			panic("injected startup panic")
		})
		return nil
	}
	deps.listen = func(string, string) (net.Listener, error) {
		listenerCalls.Add(1)
		return nil, errors.New("listener must remain unused")
	}
	if _, err := prepareFirstAudioGuardedOwner(t, context.Background(), runRoot, dbPath, runID, capability, deps); err == nil || err.Error() != "prepare_new_app_panic" {
		t.Fatalf("expected sanitized panic category, got %v", err)
	}
	if listenerCalls.Load() != 0 {
		t.Fatal("listener factory was called after NewApp panic")
	}
	if guarded == nil {
		t.Fatal("guarded pool was not opened before injected panic")
	}
	pool, err := guarded.DB()
	if err != nil {
		t.Fatal("resolve guarded pool after panic")
	}
	if err := pool.PingContext(context.Background()); err == nil {
		t.Fatal("guarded pool remained open after panic cleanup")
	}
}

func TestFirstAudioGuardedAppPrepareNewAppStartupErrorClosesWithoutListener(t *testing.T) {
	const runID = "prepare_20261029_3"
	runRoot, dbPath, capability := firstAudioPrepareTestEnvironment(t, runID)
	deps := defaultFirstAudioPrepareDependencies()
	var guardedPool *gorm.DB
	var listenerCalls atomic.Uint64
	baseGuardedOpen := deps.guardedOpen
	deps.guardedOpen = func(name, path string) (*gorm.DB, error) {
		db, err := baseGuardedOpen(name, path)
		guardedPool = db
		return db, err
	}
	deps.newApp = func(db *gorm.DB) *App { return &App{DB: db, StartupError: errors.New("injected startup failure")} }
	deps.listen = func(string, string) (net.Listener, error) {
		listenerCalls.Add(1)
		return nil, errors.New("listener must remain unused")
	}
	if _, err := prepareFirstAudioGuardedOwner(t, context.Background(), runRoot, dbPath, runID, capability, deps); err == nil {
		t.Fatal("App with StartupError was accepted")
	}
	if listenerCalls.Load() != 0 {
		t.Fatal("listener factory was called on NewApp failure")
	}
	if guardedPool == nil {
		t.Fatal("guarded pool was not opened before injected startup failure")
	}
	pool, err := guardedPool.DB()
	if err != nil {
		t.Fatal("resolve guarded pool after failure")
	}
	if err := pool.PingContext(context.Background()); err == nil {
		t.Fatal("guarded pool remained available after startup failure")
	}
}

func (o *firstAudioPreparedOwner) registryCount() int {
	if o == nil || o.registry == nil {
		return 0
	}
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	return len(o.registry.byRaw)
}

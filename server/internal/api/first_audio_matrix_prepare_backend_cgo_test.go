//go:build cgo

package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/scheduler"
	"enwords/internal/store"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const firstAudioGuardedPreparePoolCapacity = 3

type firstAudioPrepareDependencies struct {
	reserveDB   func(string) error
	setupOpen   func(string) (*gorm.DB, error)
	guardedOpen func(string, string) (*gorm.DB, error)
	newApp      func(*gorm.DB) *App
	checkpoint  func(string) error
	listen      func(string, string) (net.Listener, error)
}

type firstAudioPrepareConnection struct {
	tracked             *firstAudioTrackedSQLiteConn
	authorizer          *firstAudioRealSQLAuthorizer
	sessionRef          *firstAudioRealSQLProbeSessionRef
	registrationPhase   firstAudioGuardContextPhase
	registrationVersion uint64
}

type firstAudioPreparedOwner struct {
	mu                    sync.Mutex
	closeOnce             sync.Once
	runID                 string
	identity              firstAudioFixtureIdentity
	dialectorConnectionID string
	app                   *App
	db                    *gorm.DB
	pool                  *sql.DB
	controller            *firstAudioGuardController
	registry              *firstAudioSQLiteConnectionRegistry
	baseline              firstAudioDatabaseSnapshot
	connections           map[string]firstAudioPrepareConnection
	receipts              []*firstAudioGuardProbeReceipt
	opened                []*firstAudioTrackedSQLiteConn
	listen                func(string, string) (net.Listener, error)
	listenerCalls         atomic.Uint64
	rowGuardCalls         atomic.Uint64
	closeErr              error
}

func defaultFirstAudioPrepareDependencies() firstAudioPrepareDependencies {
	return firstAudioPrepareDependencies{
		reserveDB: func(path string) error {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			return file.Close()
		},
		setupOpen: store.Open,
		guardedOpen: func(driverName, path string) (*gorm.DB, error) {
			return gorm.Open(gormsqlite.Dialector{DriverName: driverName, DSN: path}, &gorm.Config{})
		},
		newApp: func(db *gorm.DB) *App { return NewApp(scheduler.RealClock{}, db) },
		listen: net.Listen,
	}
}

func firstAudioPrepareRegistrationAllowed(phase firstAudioGuardContextPhase, version uint64, registrationID string, nextID uint64) bool {
	switch phase {
	case firstAudioGuardDriverProbe:
		return version == 0 && registrationID == "conn-1" && nextID == 1
	case firstAudioGuardPreflightPrepare:
		return version == 2 && nextID >= 2 && nextID <= firstAudioGuardedPreparePoolCapacity && registrationID == fmt.Sprintf("conn-%d", nextID)
	default:
		return false
	}
}

// prepareFirstAudioGuardedOwner composes the reviewed fixture, real NewApp,
// and physical-connection PREFLIGHT probes. It never creates a listener or
// publishes JOURNEY; it is deliberately not the protocol bind implementation.
func prepareFirstAudioGuardedOwner(t *testing.T, ctx context.Context, runRoot, dbPath, runID string, capability []byte, deps firstAudioPrepareDependencies) (_ *firstAudioPreparedOwner, resultErr error) {
	t.Helper()
	defer zeroFirstAudioSecret(capability)
	started := time.Now()
	logSafe := false
	cleanupOutcome := "not_started"
	var owner *firstAudioPreparedOwner
	defer func() {
		if logSafe && resultErr != nil {
			phase := firstAudioGuardTerminal
			if owner != nil && owner.controller != nil {
				if active := owner.controller.publisher.Load(); active != nil {
					phase = active.phase
				}
			}
			t.Logf("trace_id=%s module=first_audio_prepare event=prepare_failed phase=%s error_category=%s cleanup=%s elapsed_ms=%d", runID, phase, resultErr.Error(), cleanupOutcome, time.Since(started).Milliseconds())
		}
	}()
	if deps.reserveDB == nil || deps.setupOpen == nil || deps.guardedOpen == nil || deps.newApp == nil || deps.listen == nil {
		return nil, errors.New("prepare_dependencies_invalid")
	}
	if ctx == nil {
		return nil, errors.New("prepare_context_invalid")
	}
	if deps.checkpoint == nil {
		deps.checkpoint = func(string) error { return nil }
	}
	failAt := func(stage string) error {
		if err := deps.checkpoint(stage); err != nil {
			return fmt.Errorf("injected_%s", stage)
		}
		return nil
	}
	gateCapability, err := firstAudioCapabilityFromEnvironment()
	defer zeroFirstAudioSecret(gateCapability)
	if err != nil || subtle.ConstantTimeCompare(gateCapability, capability) != 1 || runID != os.Getenv(firstAudioHarnessRunEnv) || filepath.Clean(runRoot) != filepath.Clean(filepath.Dir(dbPath)) || filepath.Clean(dbPath) != filepath.Clean(os.Getenv(firstAudioHarnessDBEnv)) {
		return nil, errors.New("prepare_environment_invalid")
	}
	if !firstAudioRunIDPattern.MatchString(runID) || filepath.Base(runRoot) != "first-audio-matrix-"+runID {
		return nil, errors.New("prepare_run_root_invalid")
	}
	canonicalRoot, err := filepath.EvalSymlinks(runRoot)
	if err != nil || !firstAudioSamePath(canonicalRoot, runRoot) || filepath.Clean(filepath.Dir(dbPath)) != filepath.Clean(runRoot) {
		return nil, errors.New("prepare_path_invalid")
	}
	if err := verifyFirstAudioPrivateDirectory(runRoot); err != nil {
		return nil, errors.New("prepare_root_not_private")
	}
	logSafe = true
	if err := failAt("before_database_reservation"); err != nil {
		return nil, err
	}
	if err := deps.reserveDB(dbPath); err != nil {
		return nil, errors.New("prepare_database_reservation_failed")
	}
	info, err := os.Lstat(dbPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("prepare_database_file_invalid")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, statErr := os.Lstat(dbPath + suffix); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return nil, errors.New("prepare_database_sidecar_exists")
		}
	}
	if err := failAt("before_setup_open"); err != nil {
		return nil, err
	}
	setup, err := deps.setupOpen(dbPath)
	if err != nil || setup == nil {
		return nil, errors.New("prepare_setup_open_failed")
	}
	setupPool, err := setup.DB()
	if err != nil {
		return nil, errors.New("prepare_setup_pool_failed")
	}
	setupOpen := true
	defer func() {
		if setupOpen {
			if closeErr := setupPool.Close(); closeErr != nil {
				cleanupOutcome = "uncertain"
				if resultErr == nil {
					resultErr = errors.New("prepare_setup_close_uncertain")
				}
			} else {
				cleanupOutcome = "setup_closed"
			}
		}
	}()
	if err := failAt("setup_opened"); err != nil {
		return nil, err
	}
	identity, _, err := seedFirstAudioSetupFixture(setup, capability, runID, time.Now().UTC())
	if err != nil {
		return nil, errors.New("prepare_fixture_failed")
	}
	if err := setup.Create(&store.ConfigGlobal{Key: "wordsPerLesson", Value: `60`}).Error; err != nil {
		return nil, errors.New("prepare_config_fixture_failed")
	}
	if err := installFirstAudioStartupGuards(setup); err != nil {
		return nil, errors.New("prepare_startup_guards_failed")
	}
	if err := installFirstAudioLessonProbeTriggers(setup); err != nil {
		return nil, errors.New("prepare_lesson_guards_failed")
	}
	if err := firstAudioValidateSchemaManifest(setup); err != nil {
		return nil, errors.New("prepare_schema_manifest_failed")
	}
	if err := firstAudioValidateStartupReadManifest(setup); err != nil {
		return nil, errors.New("prepare_startup_manifest_failed")
	}
	baseline, err := firstAudioCaptureDatabaseSnapshot(setup)
	if err != nil {
		return nil, errors.New("prepare_baseline_failed")
	}
	if err := failAt("fixture_ready"); err != nil {
		return nil, err
	}
	if err := setupPool.Close(); err != nil {
		return nil, errors.New("prepare_setup_close_failed")
	}
	setupOpen = false
	cleanupOutcome = "setup_closed"
	if err := setupPool.PingContext(ctx); err == nil {
		return nil, errors.New("prepare_setup_pool_still_open")
	}
	if err := failAt("setup_closed"); err != nil {
		return nil, err
	}

	controller := newFirstAudioGuardControllerWithPolicy(true, firstAudioGuardedPreparePoolCapacity)
	controller.startupBaselineHash = baseline.SHA256
	registry := controller.connections
	driverContext, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: runID, currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil || controller.PublishInitial(driverContext) != nil {
		return nil, errors.New("prepare_driver_context_failed")
	}
	owner = &firstAudioPreparedOwner{
		runID: runID, identity: identity, controller: controller, registry: registry,
		baseline: baseline, connections: make(map[string]firstAudioPrepareConnection),
		listen: func(network, address string) (net.Listener, error) {
			owner.listenerCalls.Add(1)
			return deps.listen(network, address)
		},
	}
	ownerCleanup := true
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = errors.New("prepare_internal_panic")
		}
		if ownerCleanup {
			if closeErr := owner.Close(); closeErr != nil {
				cleanupOutcome = "guarded_cleanup_uncertain"
				resultErr = errors.Join(resultErr, errors.New("prepare_cleanup_uncertain"))
			} else {
				cleanupOutcome = "guarded_cleanup_confirmed"
			}
		}
	}()
	var tokenCalls atomic.Uint64
	var callbackErr atomic.Value
	name := fmt.Sprintf("first_audio_guarded_prepare_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(raw *sqlite3.SQLiteConn) error {
		registration, registerErr := registry.Register(raw)
		if registerErr != nil {
			return registerErr
		}
		registeredDuring := controller.publisher.Load()
		if registeredDuring == nil || !firstAudioPrepareRegistrationAllowed(registeredDuring.phase, registeredDuring.version, registration.ID, registry.nextID.Load()) {
			registry.Retire(raw)
			return errors.New("prepare_registration_phase_rejected")
		}
		sessionRef := &firstAudioRealSQLProbeSessionRef{}
		authorizer := newFirstAudioRealSQLAuthorizer(controller, sessionRef, baseline)
		if registeredDuring.phase == firstAudioGuardStartupProbe {
			authorizer.startupPermit.Store(true)
		}
		owner.mu.Lock()
		owner.connections[registration.ID] = firstAudioPrepareConnection{authorizer: authorizer, sessionRef: sessionRef, registrationPhase: registeredDuring.phase, registrationVersion: registeredDuring.version}
		owner.mu.Unlock()
		if registerErr := raw.RegisterFunc("first_audio_probe", func(token []byte) bool {
			tokenCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() || session.challenge == nil || session.binding.connection != registration.ID {
				return false
			}
			if consumeErr := session.challenge.ConsumeInTransaction(token, session.binding, session.txEpoch.Load()); consumeErr != nil {
				callbackErr.Store("probe_token_rejected")
				return false
			}
			return true
		}, true); registerErr != nil {
			return registerErr
		}
		if registerErr := raw.RegisterFunc("first_audio_row_guard", func(args ...any) bool {
			owner.rowGuardCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() || session.challenge == nil || session.binding.connection != registration.ID || len(args) != 72 {
				return false
			}
			expected := session.expectedProjection.Load()
			actualOld := normalizeFirstAudioTypedSQLitePairs(args[:36])
			if expected == nil || !reflect.DeepEqual(expected.values, actualOld) {
				callbackErr.Store("probe_old_projection_mismatch")
				return false
			}
			if actualNew := normalizeFirstAudioTypedSQLitePairs(args[36:]); !reflect.DeepEqual(actualOld, actualNew) {
				callbackErr.Store("probe_new_projection_mismatch")
				return false
			}
			session.callbackArgs.Store(&firstAudioCapturedTypedCallbackArgs{values: cloneFirstAudioSQLiteCallbackArgs(args)})
			if observeErr := session.challenge.ObserveRowGuardInTransaction(session.binding, session.txEpoch.Load()); observeErr != nil {
				callbackErr.Store("probe_receipt_rejected")
				return false
			}
			return true
		}, true); registerErr != nil {
			return registerErr
		}
		return authorizer.install(raw)
	}, registry.Retire, func(tracked *firstAudioTrackedSQLiteConn) {
		owner.mu.Lock()
		owner.opened = append(owner.opened, tracked)
		if registration, ok := registry.Lookup(tracked.raw); ok {
			connection := owner.connections[registration.ID]
			connection.tracked = tracked
			owner.connections[registration.ID] = connection
		}
		owner.mu.Unlock()
	}); err != nil {
		return nil, errors.New("prepare_driver_register_failed")
	}
	if err := failAt("driver_registered"); err != nil {
		return nil, err
	}
	guarded, err := deps.guardedOpen(name, dbPath)
	if err != nil || guarded == nil {
		return nil, errors.New("prepare_guarded_open_failed")
	}
	if controller.driverProbe == nil || controller.driverProbe.connectionID == "" {
		return nil, errors.New("prepare_dialector_receipt_missing")
	}
	owner.dialectorConnectionID = controller.driverProbe.connectionID
	// gorm.Open is the only DRIVER_PROBE operation. Tighten the controller
	// before resolving the pool or doing any further driver interaction.
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: runID, currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		return nil, errors.New("prepare_startup_context_failed")
	}
	startupTransition, err := controller.CloseAndDrain(ctx)
	if err != nil || startupTransition.Publish(ctx, startup, firstAudioGuardTransitionEvidence{}) != nil {
		return nil, errors.New("prepare_startup_transition_failed")
	}
	pool, err := guarded.DB()
	if err != nil {
		return nil, errors.New("prepare_guarded_pool_failed")
	}
	owner.db, owner.pool, owner.app = guarded, pool, nil
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(0)
	owner.mu.Lock()
	for _, connection := range owner.connections {
		connection.authorizer.startupPermit.Store(true)
	}
	owner.mu.Unlock()
	if err := failAt("before_new_app"); err != nil {
		return nil, err
	}
	var app *App
	func() {
		defer func() {
			if recover() != nil {
				app = nil
				resultErr = errors.New("prepare_new_app_panic")
			}
		}()
		app = deps.newApp(guarded)
	}()
	owner.mu.Lock()
	var startupBegins, startupEnds uint32
	for _, connection := range owner.connections {
		connection.authorizer.startupPermit.Store(false)
		startupBegins += connection.authorizer.startupBegins.Load()
		startupEnds += connection.authorizer.startupEnds.Load()
	}
	owner.mu.Unlock()
	if resultErr != nil {
		if startupErr := owner.verifyStartupFailureState(); startupErr != nil {
			return nil, errors.New("prepare_new_app_panic_cleanup_uncertain")
		}
		return nil, resultErr
	}
	if err := owner.verifyStartupAutocommit(); err != nil {
		return nil, errors.New("prepare_new_app_transaction_state_uncertain")
	}
	if app == nil || app.DB != guarded || app.StartupError != nil || startupBegins != 1 || startupEnds != 1 || owner.rowGuardCalls.Load() != 0 {
		if err := owner.verifyStartupFailureState(); err != nil {
			return nil, errors.New("prepare_new_app_failure_cleanup_uncertain")
		}
		return nil, errors.New("prepare_new_app_startup_failed")
	}
	owner.app = app
	if err := failAt("new_app_ready"); err != nil {
		return nil, err
	}
	startupSnapshot, err := owner.verifyStartupAndRecord(startupTransition)
	if err != nil {
		return nil, errors.New("prepare_startup_snapshot_mismatch")
	}
	t.Logf("trace_id=%s module=first_audio_prepare event=startup_verified phase=%s version=1 connection_count=%d startup_begin_count=1 startup_end_count=1 baseline_sha256=%s verified_sha256=%s", runID, firstAudioGuardStartupProbe, len(owner.opened), baseline.SHA256, startupSnapshot.SHA256)
	prepareContext, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: runID, currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardPreflightPrepare, version: 2,
	})
	if err != nil {
		return nil, errors.New("prepare_preflight_prepare_context_failed")
	}
	if err := startupTransition.Publish(ctx, prepareContext, firstAudioGuardTransitionEvidence{}); err != nil || !controller.IsActivePhase(firstAudioGuardPreflightPrepare) {
		return nil, errors.New("prepare_preflight_prepare_transition_failed")
	}
	pool.SetMaxOpenConns(firstAudioGuardedPreparePoolCapacity)
	pool.SetMaxIdleConns(firstAudioGuardedPreparePoolCapacity)
	if err := failAt("preflight_prepare_published"); err != nil {
		return nil, err
	}
	pins, identities, err := owner.pinAll(ctx)
	if err != nil {
		return nil, errors.New("prepare_preflight_connections_incomplete")
	}
	defer func() { _ = owner.releasePins(pins) }()
	if got := len(identities); got != firstAudioGuardedPreparePoolCapacity || registry.nextID.Load() != firstAudioGuardedPreparePoolCapacity {
		return nil, errors.New("prepare_preflight_connection_count_mismatch")
	}
	owner.mu.Lock()
	provenanceValid := len(owner.connections) == firstAudioGuardedPreparePoolCapacity
	for _, pinned := range identities {
		connection := owner.connections[pinned.registration.ID]
		if pinned.registration.ID == owner.dialectorConnectionID {
			provenanceValid = provenanceValid && connection.registrationPhase == firstAudioGuardDriverProbe && connection.registrationVersion == 0
		} else {
			provenanceValid = provenanceValid && connection.registrationPhase == firstAudioGuardPreflightPrepare && connection.registrationVersion == 2
		}
	}
	owner.mu.Unlock()
	if !provenanceValid {
		return nil, errors.New("prepare_connection_provenance_mismatch")
	}
	if err := owner.verifyPrepareNoAuthorizerEvents(identities); err != nil {
		return nil, errors.New("prepare_connection_initialization_authorizer_event")
	}
	t.Logf("trace_id=%s module=first_audio_prepare event=preflight_prepare_ready phase=%s version=2 connection_count=%d driver_registration_count=1 prepare_registration_count=%d authorizer_event_count=0", runID, firstAudioGuardPreflightPrepare, len(identities), len(identities)-1)
	if err := failAt("preflight_prepare_connections_ready"); err != nil {
		return nil, err
	}
	bindings := make([]firstAudioGuardProbeBinding, 0, len(identities))
	challenges := make([]*firstAudioGuardProbeChallenge, 0, len(identities))
	tokens := make([][]byte, 0, len(identities))
	probeContracts := make([]firstAudioGuardProbeContract, 0, len(identities))
	rowContracts := make([]firstAudioGuardRowContract, 0, len(identities))
	connectionIDs := make([]string, 0, len(identities))
	expectedConnections := make(map[string]*sqlite3.SQLiteConn, len(identities))
	for _, pinned := range identities {
		binding := firstAudioGuardProbeBinding{runID: runID, lessonID: identity.LessonID, generation: 1, version: 3, connection: pinned.registration.ID, probeID: "probe-" + strings.TrimPrefix(pinned.registration.ID, "conn-")}
		token, challenge, issueErr := newFirstAudioPhysicalProbeChallenge(binding)
		if issueErr != nil {
			for _, secret := range tokens {
				zeroFirstAudioSecret(secret)
			}
			return nil, errors.New("prepare_probe_challenge_failed")
		}
		bindings = append(bindings, binding)
		challenges = append(challenges, challenge)
		tokens = append(tokens, token)
		probeContracts = append(probeContracts, challenge.Contract())
		rowContracts = append(rowContracts, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", identity.LessonID, pinned.registration.ID), oldRow: []byte("typed-projection"), newRow: []byte("typed-projection")})
		connectionIDs = append(connectionIDs, pinned.registration.ID)
		expectedConnections[pinned.registration.ID] = pinned.raw
	}
	defer func() {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
	}()
	preflight, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: runID, currentLessonID: identity.LessonID, generation: 1, phase: firstAudioGuardPreflight, version: 3, requirePhysicalProbes: true, connectionIDs: connectionIDs, probeContracts: probeContracts, rowContracts: rowContracts})
	if err != nil {
		return nil, errors.New("prepare_preflight_context_failed")
	}
	receipts := make([]*firstAudioGuardProbeReceipt, 0, len(challenges))
	for _, challenge := range challenges {
		receipts = append(receipts, challenge.receipt)
	}
	owner.receipts = append([]*firstAudioGuardProbeReceipt(nil), receipts...)
	if err := startupTransition.Publish(ctx, preflight, firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, expectedConnections: expectedConnections}); err != nil {
		return nil, errors.New("prepare_preflight_transition_failed")
	}
	if !controller.IsActivePhase(firstAudioGuardPreflight) {
		return nil, errors.New("prepare_preflight_phase_invalid")
	}
	for index, pinned := range identities {
		connection := owner.connections[pinned.registration.ID]
		connection.authorizer.tracked, connection.authorizer.pinned, connection.authorizer.db = pinned.tracked, pinned.conn, guarded
		session := &firstAudioRealSQLProbeSession{challenge: challenges[index], binding: bindings[index], tracked: pinned.tracked}
		connection.sessionRef.session.Store(session)
		runFirstAudioRealSQLConnectionProbe(t, connection.authorizer, pinned, challenges[index], bindings[index], tokens[index], session, connection.sessionRef, &tokenCalls, &owner.rowGuardCalls, &callbackErr)
	}
	for _, challenge := range challenges {
		if !challenge.receipt.completeFor(challenge.binding, challenge.digest) {
			return nil, errors.New("prepare_probe_receipt_incomplete")
		}
	}
	if callbackErr.Load() != nil {
		return nil, errors.New("prepare_probe_callback_failed")
	}
	for _, connection := range owner.connections {
		if connection.authorizer.denials.Load() != 0 {
			return nil, errors.New("prepare_authorizer_denied")
		}
	}
	if err := owner.releasePins(pins); err != nil {
		return nil, errors.New("prepare_pins_release_failed")
	}
	pins = nil
	if registry.nextID.Load() != firstAudioGuardedPreparePoolCapacity || owner.listenerCalls.Load() != 0 || controller.IsTerminal() || !controller.IsActivePhase(firstAudioGuardPreflight) {
		return nil, errors.New("prepare_terminal_invariant_failed")
	}
	ownerCleanup = false
	t.Logf("trace_id=%s module=first_audio_prepare event=preflight_ready phase=%s version=3 pool_capacity=%d hook_count=%d receipt_count=%d denial_count=%d baseline_sha256=%s elapsed_ms=%d listener_calls=%d", runID, firstAudioGuardPreflight, firstAudioGuardedPreparePoolCapacity, len(owner.opened), len(challenges), 0, baseline.SHA256, time.Since(started).Milliseconds(), owner.listenerCalls.Load())
	return owner, nil
}

func (o *firstAudioPreparedOwner) pinAll(ctx context.Context) ([]*sql.Conn, []firstAudioPinnedRealSQLConnection, error) {
	if o == nil || o.controller == nil || !o.controller.IsActivePhase(firstAudioGuardPreflightPrepare) {
		return nil, nil, errors.New("pin_connections_outside_preflight_prepare")
	}
	pins := make([]*sql.Conn, 0, firstAudioGuardedPreparePoolCapacity)
	identities := make([]firstAudioPinnedRealSQLConnection, 0, firstAudioGuardedPreparePoolCapacity)
	seen := make(map[string]struct{}, firstAudioGuardedPreparePoolCapacity)
	for range firstAudioGuardedPreparePoolCapacity {
		pinned, err := o.pool.Conn(ctx)
		if err != nil {
			_ = o.releasePins(pins)
			return nil, nil, err
		}
		pins = append(pins, pinned)
		var tracked *firstAudioTrackedSQLiteConn
		if err := pinned.Raw(func(raw any) error {
			var ok bool
			tracked, ok = raw.(*firstAudioTrackedSQLiteConn)
			if !ok || tracked == nil || tracked.raw == nil {
				return errors.New("tracked_conn_missing")
			}
			return nil
		}); err != nil {
			_ = o.releasePins(pins)
			return nil, nil, err
		}
		registration, ok := o.registry.Lookup(tracked.raw)
		if !ok || !o.registry.IsLive(registration.ID, tracked.raw) {
			_ = o.releasePins(pins)
			return nil, nil, errors.New("connection_registry_missing")
		}
		if _, exists := seen[registration.ID]; exists {
			_ = o.releasePins(pins)
			return nil, nil, errors.New("connection_reused")
		}
		seen[registration.ID] = struct{}{}
		identities = append(identities, firstAudioPinnedRealSQLConnection{conn: pinned, raw: tracked.raw, tracked: tracked, registration: registration})
		o.mu.Lock()
		connection := o.connections[registration.ID]
		connection.tracked = tracked
		o.connections[registration.ID] = connection
		o.mu.Unlock()
	}
	if len(seen) != firstAudioGuardedPreparePoolCapacity {
		_ = o.releasePins(pins)
		return nil, nil, errors.New("connection_count_mismatch")
	}
	return pins, identities, nil
}

func (o *firstAudioPreparedOwner) verifyStartupAutocommit() error {
	if o == nil || o.controller == nil || !o.controller.IsActivePhase(firstAudioGuardStartupProbe) {
		return errors.New("startup_phase_uncertain")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.opened) == 0 || len(o.connections) != len(o.opened) {
		return errors.New("startup_connection_count_mismatch")
	}
	var totalBegins, totalEnds uint32
	for _, connection := range o.connections {
		if connection.tracked == nil || connection.tracked.raw == nil || !connection.tracked.raw.AutoCommit() {
			return errors.New("startup_connection_not_autocommit")
		}
		if connection.authorizer.startupBegins.Load() != connection.authorizer.startupEnds.Load() {
			return errors.New("startup_transaction_unbalanced")
		}
		totalBegins += connection.authorizer.startupBegins.Load()
		totalEnds += connection.authorizer.startupEnds.Load()
	}
	if totalBegins != 1 || totalEnds != 1 {
		return errors.New("startup_transaction_count_mismatch")
	}
	return nil
}

// verifyStartupAndRecord runs the startup proof itself, then stores a
// controller-bound receipt. Callers cannot assert readiness/count/digest flags.
func (o *firstAudioPreparedOwner) verifyStartupAndRecord(lease *firstAudioGuardTransitionLease) (firstAudioDatabaseSnapshot, error) {
	if o == nil || lease == nil || o.controller == nil || o.app == nil || o.app.DB != o.db || o.app.StartupError != nil || o.db == nil {
		return firstAudioDatabaseSnapshot{}, errors.New("startup_app_not_ready")
	}
	if err := o.verifyStartupAutocommit(); err != nil {
		return firstAudioDatabaseSnapshot{}, err
	}
	if o.rowGuardCalls.Load() != 0 {
		return firstAudioDatabaseSnapshot{}, errors.New("startup_row_guard_invoked")
	}
	startupSnapshot, err := firstAudioCaptureDatabaseSnapshot(o.db)
	if err != nil || startupSnapshot.SHA256 != o.baseline.SHA256 || !reflect.DeepEqual(startupSnapshot.Canonical, o.baseline.Canonical) {
		return firstAudioDatabaseSnapshot{}, errors.New("startup_snapshot_mismatch")
	}
	c := o.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.publisher.Load()
	if !c.requirePreflightPrepare || lease.owner != c || lease.released || lease.busy || !c.exclusive || c.activeTransition != lease || c.isTerminalLocked() || active == nil || active.phase != firstAudioGuardStartupProbe || active.version != 1 || c.startupBaselineHash != o.baseline.SHA256 || c.startupVerificationReceipt != nil {
		return firstAudioDatabaseSnapshot{}, errFirstAudioGuardContextTransition
	}
	c.startupVerificationReceipt = &firstAudioStartupVerificationReceipt{
		runID: active.runID, lessonID: active.currentLessonID, generation: active.generation, version: active.version,
		baselineHash: o.baseline.SHA256, verifiedHash: startupSnapshot.SHA256,
	}
	return startupSnapshot, nil
}

func (o *firstAudioPreparedOwner) verifyPrepareNoAuthorizerEvents(identities []firstAudioPinnedRealSQLConnection) error {
	if o == nil || o.controller == nil || !o.controller.IsActivePhase(firstAudioGuardPreflightPrepare) || len(identities) != firstAudioGuardedPreparePoolCapacity {
		return errors.New("prepare_phase_or_identity_count_invalid")
	}
	for _, identity := range identities {
		if identity.registration.ID == o.dialectorConnectionID {
			continue
		}
		o.mu.Lock()
		connection := o.connections[identity.registration.ID]
		o.mu.Unlock()
		if connection.registrationPhase != firstAudioGuardPreflightPrepare || connection.registrationVersion != 2 || connection.authorizer.authorizerEvents.Load() != 0 || connection.authorizer.denials.Load() != 0 {
			return errors.New("prepare_connection_authorizer_observed_sql")
		}
	}
	return nil
}

func (o *firstAudioPreparedOwner) releasePins(pins []*sql.Conn) error {
	var first error
	for _, pin := range pins {
		if err := pin.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (o *firstAudioPreparedOwner) releaseAndRepin(ctx context.Context, identities []firstAudioPinnedRealSQLConnection, pins *[]*sql.Conn) ([]firstAudioPinnedRealSQLConnection, error) {
	if err := o.releasePins(*pins); err != nil {
		return nil, err
	}
	*pins = nil
	newPins, newIdentities, err := o.pinAll(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[string]*sqlite3.SQLiteConn, len(identities))
	for _, identity := range identities {
		want[identity.registration.ID] = identity.raw
	}
	for _, identity := range newIdentities {
		if want[identity.registration.ID] != identity.raw {
			_ = o.releasePins(newPins)
			return nil, errors.New("physical_identity_changed")
		}
	}
	*pins = newPins
	return newIdentities, nil
}

// verifyStartupFailureState performs no SQL until every tracked raw connection
// reports autocommit. STARTUP_PROBE never borrows the PREFLIGHT rollback permit.
func (o *firstAudioPreparedOwner) verifyStartupFailureState() error {
	if o == nil || o.controller == nil || o.pool == nil || o.controller.IsTerminal() || !o.controller.IsActivePhase(firstAudioGuardStartupProbe) {
		return errors.New("startup_failure_phase_uncertain")
	}
	o.mu.Lock()
	connections := make([]*firstAudioTrackedSQLiteConn, 0, len(o.connections))
	for _, connection := range o.connections {
		if connection.tracked == nil || connection.tracked.raw == nil || connection.tracked.closeOK.Load() {
			o.mu.Unlock()
			return errors.New("startup_failure_connection_uncertain")
		}
		connections = append(connections, connection.tracked)
	}
	o.mu.Unlock()
	if len(connections) == 0 {
		return errors.New("startup_failure_connection_count_mismatch")
	}
	for _, connection := range connections {
		if !connection.raw.AutoCommit() {
			return errors.New("startup_failure_transaction_active")
		}
	}
	snapshot, err := firstAudioCaptureDatabaseSnapshot(o.db)
	if err != nil || snapshot.SHA256 != o.baseline.SHA256 || !reflect.DeepEqual(snapshot.Canonical, o.baseline.Canonical) {
		return errors.New("startup_failure_snapshot_mismatch")
	}
	return nil
}

func (o *firstAudioPreparedOwner) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		if o.controller != nil {
			o.controller.Terminalize()
		}
		if o.pool != nil {
			if err := o.pool.Close(); err != nil {
				o.closeErr = errors.New("guarded_pool_close_failed")
				return
			}
		}
		for _, tracked := range o.opened {
			if !tracked.closeOK.Load() || o.registry.IsLive(o.connectionID(tracked.raw), tracked.raw) {
				o.closeErr = errors.New("physical_connection_retirement_unconfirmed")
				return
			}
		}
		o.registry.mu.Lock()
		remaining := len(o.registry.byRaw)
		o.registry.mu.Unlock()
		if remaining != 0 {
			o.closeErr = errors.New("connection_registry_not_empty")
			return
		}
	})
	return o.closeErr
}

func (o *firstAudioPreparedOwner) connectionID(raw *sqlite3.SQLiteConn) string {
	for _, connection := range o.opened {
		if connection.raw == raw {
			for id, current := range o.connections {
				if current.tracked == connection {
					return id
				}
			}
		}
	}
	return ""
}

//go:build cgo

package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
)

type firstAudioRealSQLProbeSession struct {
	challenge          *firstAudioGuardProbeChallenge
	binding            firstAudioGuardProbeBinding
	tracked            *firstAudioTrackedSQLiteConn
	txEpoch            atomic.Uint64
	callbackArgs       atomic.Pointer[firstAudioCapturedTypedCallbackArgs]
	expectedProjection atomic.Pointer[firstAudioExpectedTypedProjection]
}

type firstAudioCapturedTypedCallbackArgs struct{ values []any }

type firstAudioRealSQLProbeSessionRef struct {
	session atomic.Pointer[firstAudioRealSQLProbeSession]
}

type firstAudioPinnedRealSQLConnection struct {
	conn         *sql.Conn
	raw          *sqlite3.SQLiteConn
	tracked      *firstAudioTrackedSQLiteConn
	registration firstAudioSQLiteConnectionRegistration
}

type firstAudioRealSQLConnectionHarness struct {
	authorizer *firstAudioRealSQLAuthorizer
	sessionRef *firstAudioRealSQLProbeSessionRef
}

type firstAudioSQLPermit uint32

const (
	firstAudioSQLPermitNone firstAudioSQLPermit = iota
	firstAudioSQLPermitSnapshotRead
	firstAudioSQLPermitProbeSQL
	firstAudioSQLPermitTokenConsumption
	firstAudioSQLPermitProjectionRead
	firstAudioSQLPermitTransactionCleanup
)

type firstAudioRealSQLAuthorizer struct {
	controller              *firstAudioGuardController
	connections             *firstAudioSQLiteConnectionRegistry
	db                      *gorm.DB
	baselineSHA             string
	baselineCanonical       []byte
	pinnedBaselineSHA       string
	pinnedBaselineCanonical []byte
	registryID              string
	raw                     *sqlite3.SQLiteConn
	sessionRef              *firstAudioRealSQLProbeSessionRef
	reads                   map[string]map[string]struct{}
	permit                  atomic.Uint32
	startupPermit           atomic.Bool
	startupBegins           atomic.Uint32
	startupEnds             atomic.Uint32
	cleanupAvailable        atomic.Bool
	cleanupPreflight        atomic.Bool
	tracked                 *firstAudioTrackedSQLiteConn
	pinned                  *sql.Conn
	probeChallenge          atomic.Pointer[firstAudioGuardProbeChallenge]
	activeTx                atomic.Pointer[sql.Tx]
	txEpoch                 atomic.Uint64
	denials                 atomic.Uint64
	lastDenied              atomic.Value
}

func newFirstAudioRealSQLAuthorizer(controller *firstAudioGuardController, sessionRef *firstAudioRealSQLProbeSessionRef, baseline firstAudioDatabaseSnapshot) *firstAudioRealSQLAuthorizer {
	return &firstAudioRealSQLAuthorizer{controller: controller, connections: controller.connections, sessionRef: sessionRef, reads: firstAudioStartupReadManifest(), baselineSHA: baseline.SHA256, baselineCanonical: append([]byte(nil), baseline.Canonical...)}
}

func (p *firstAudioRealSQLAuthorizer) install(conn *sqlite3.SQLiteConn) error {
	if p == nil || p.controller == nil || conn == nil {
		return fmt.Errorf("real-SQL authorizer is incomplete")
	}
	registration, registered := p.connections.Lookup(conn)
	if !registered {
		return fmt.Errorf("authorizer connection was not registered by ConnectHook")
	}
	p.registryID, p.raw = registration.ID, conn
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
		active := p.controller.publisher.Load()
		allowed := false
		if p.registryID == registration.ID && p.raw == conn && p.cleanupPreflight.Load() && firstAudioCleanupPhaseAllowed(p.controller, true) && firstAudioAuthorizeCleanupTuple(firstAudioSQLPermit(p.permit.Load()), action, arg1, arg2, databaseName, triggerName, !conn.AutoCommit()) {
			session := p.sessionRef.session.Load()
			allowed = session != nil && session.challenge != nil && session.binding.connection == registration.ID && session.tracked == p.tracked && p.tracked != nil && p.tracked.raw == conn && p.tracked.txActive.Load() && session.txEpoch.Load() != 0 && session.txEpoch.Load() == p.txEpoch.Load() && session.txEpoch.Load() == p.tracked.txEpoch.Load()
		}
		switch {
		case allowed:
		case p.controller.IsActivePhase(firstAudioGuardDriverProbe):
			allowed = p.controller.ObserveDriverProbe(conn, action, arg1, arg2, databaseName, triggerName) == nil
		case p.controller.IsActivePhase(firstAudioGuardStartupProbe):
			if action == sqlite3.SQLITE_TRANSACTION {
				allowed = p.authorizeStartupTransaction(arg1, arg2, databaseName, triggerName, conn.AutoCommit())
			} else {
				allowed = firstAudioStartupAuthorizeAt(firstAudioStartupApp, p.reads, action, arg1, arg2, databaseName, triggerName)
			}
		case p.controller.IsActivePhase(firstAudioGuardPreflight):
			// A connection-local authorizer accepts permits only for the raw
			// connection whose identity was captured by its ConnectHook.
			allowed = p.registryID == registration.ID && p.raw == conn && firstAudioAuthorizePreflightTuple(firstAudioSQLPermit(p.permit.Load()), p.reads, action, arg1, arg2, databaseName, triggerName)
		}
		if allowed {
			return sqlite3.SQLITE_OK
		}
		p.denials.Add(1)
		phase := firstAudioGuardTerminal
		if active != nil {
			phase = active.phase
		}
		p.lastDenied.Store(fmt.Sprintf("phase=%s permit=%d action=%d db=%q source=%q arg1=%q arg2=%q", phase, p.permit.Load(), action, databaseName, triggerName, arg1, arg2))
		_ = p.controller.TerminalizeFromSQLiteCallback()
		return sqlite3.SQLITE_DENY
	})
	return nil
}

func firstAudioAuthorizeCleanupTuple(permit firstAudioSQLPermit, action int, arg1, arg2, databaseName, triggerName string, transactionActive bool) bool {
	return permit == firstAudioSQLPermitTransactionCleanup && action == sqlite3.SQLITE_TRANSACTION && arg1 == "ROLLBACK" && arg2 == "" && databaseName == "" && triggerName == "" && transactionActive
}

func (p *firstAudioRealSQLAuthorizer) authorizeStartupTransaction(action, arg2, databaseName, triggerName string, autoCommit bool) bool {
	if p == nil || !p.startupPermit.Load() || arg2 != "" || databaseName != "" || triggerName != "" {
		return false
	}
	switch action {
	case "BEGIN":
		return autoCommit && p.startupBegins.CompareAndSwap(0, 1)
	case "COMMIT", "ROLLBACK":
		return !autoCommit && p.startupBegins.Load() == 1 && p.startupEnds.CompareAndSwap(0, 1)
	default:
		return false
	}
}

func (p *firstAudioRealSQLAuthorizer) withStartupPermit(operation func() error) error {
	if p == nil || operation == nil || !p.controller.IsActivePhase(firstAudioGuardStartupProbe) || !p.startupPermit.CompareAndSwap(false, true) {
		return errFirstAudioGuardProbeRejected
	}
	defer p.startupPermit.Store(false)
	return operation()
}

func (p *firstAudioRealSQLAuthorizer) withPermit(permit firstAudioSQLPermit, requireActive bool, operation func() error) error {
	if p == nil || operation == nil || permit == firstAudioSQLPermitNone || !p.controller.IsActivePhase(firstAudioGuardPreflight) || !p.connections.IsLive(p.registryID, p.raw) {
		return errFirstAudioGuardProbeRejected
	}
	session := p.sessionRef.session.Load()
	active := session != nil && session.tracked != nil && session.tracked.txActive.Load() && session.txEpoch.Load() != 0 && session.txEpoch.Load() == session.tracked.txEpoch.Load() && session.binding.connection == p.registryID
	if session == nil || session.challenge == nil || !p.ownsActiveReceipt(session.challenge.receipt, session.binding) {
		return errFirstAudioGuardProbeRejected
	}
	if permit == firstAudioSQLPermitSnapshotRead {
		if active {
			return errFirstAudioGuardProbeRejected
		}
	} else if active != requireActive {
		return errFirstAudioGuardProbeRejected
	}
	if !p.permit.CompareAndSwap(uint32(firstAudioSQLPermitNone), uint32(permit)) {
		return errFirstAudioGuardProbeRejected
	}
	defer p.permit.Store(uint32(firstAudioSQLPermitNone))
	return operation()
}

func (p *firstAudioRealSQLAuthorizer) rollbackProbeTransaction() error {
	if p == nil || p.pinned == nil || p.tracked == nil || !p.cleanupPreflight.Load() || !firstAudioCleanupPhaseAllowed(p.controller, true) || !p.connections.IsLive(p.registryID, p.raw) || !p.tracked.txActive.Load() || p.txEpoch.Load() == 0 || p.txEpoch.Load() != p.tracked.txEpoch.Load() || !p.cleanupAvailable.CompareAndSwap(true, false) {
		return p.terminalizeAndDiscardUncertainConnection()
	}
	defer p.cleanupPreflight.Store(false)
	if !p.permit.CompareAndSwap(uint32(firstAudioSQLPermitNone), uint32(firstAudioSQLPermitTransactionCleanup)) {
		return p.terminalizeAndDiscardUncertainConnection()
	}
	defer p.permit.Store(uint32(firstAudioSQLPermitNone))
	tx := p.activeTx.Load()
	if tx == nil {
		return p.terminalizeAndDiscardUncertainConnection()
	}
	err := tx.Rollback()
	if err != nil || p.tracked.txActive.Load() || !p.raw.AutoCommit() {
		cleanupErr := p.terminalizeAndDiscardUncertainConnection()
		return fmt.Errorf("probe rollback unconfirmed: %w (rollback error=%v)", cleanupErr, err)
	}
	p.activeTx.CompareAndSwap(tx, nil)
	if challenge := p.probeChallenge.Load(); challenge != nil {
		challenge.InvalidateIncompleteTransaction(p.txEpoch.Load())
	}
	return nil
}

func firstAudioCleanupPhaseAllowed(controller *firstAudioGuardController, probeBeganInPreflight bool) bool {
	if controller == nil || !probeBeganInPreflight {
		return false
	}
	if controller.IsActivePhase(firstAudioGuardPreflight) {
		return true
	}
	active := controller.publisher.Load()
	return controller.IsTerminal() && active != nil && active.phase == firstAudioGuardTerminal
}

func (p *firstAudioRealSQLAuthorizer) terminalizeAndDiscardUncertainConnection() error {
	if p == nil {
		return errFirstAudioGuardProbeRejected
	}
	p.failBoundProbeReceipt()
	var terminalErr error
	if p.controller != nil {
		terminalErr = p.controller.TerminalizeFromSQLiteCallback()
		p.controller.Terminalize()
	}
	var leaseReleaseErr error
	if tx := p.activeTx.Load(); tx != nil {
		// Release database/sql's transaction lease after terminalization and
		// before Raw returns ErrBadConn. The terminal authorizer may reject the
		// driver's rollback; either outcome is followed by physical eviction.
		leaseReleaseErr = tx.Rollback()
		p.activeTx.CompareAndSwap(tx, nil)
	}
	if err := p.discardPinnedConnection(); err != nil {
		return fmt.Errorf("uncertain probe cleanup; physical connection retirement unconfirmed (transaction lease release error=%v): %w", leaseReleaseErr, err)
	}
	if terminalErr != nil {
		return fmt.Errorf("uncertain probe cleanup; terminal context publication failed after connection retirement: %w", terminalErr)
	}
	if leaseReleaseErr != nil {
		return fmt.Errorf("uncertain probe cleanup; transaction rollback was rejected after terminalization and connection was retired: %v: %w", leaseReleaseErr, errFirstAudioGuardProbeRejected)
	}
	return errFirstAudioGuardProbeRejected
}

func (p *firstAudioRealSQLAuthorizer) failBoundProbeReceipt() {
	if p == nil || p.sessionRef == nil || p.connections == nil || p.tracked == nil || p.raw == nil || p.registryID == "" || !p.connections.IsLive(p.registryID, p.raw) {
		return
	}
	expected := p.probeChallenge.Load()
	if expected == nil || expected.receipt == nil || expected.binding.connection != p.registryID {
		return
	}
	session := p.sessionRef.session.Load()
	if session == nil || session.challenge != expected || session.tracked != p.tracked || session.tracked.raw != p.raw || session.binding.connection != p.registryID || !sameFirstAudioProbeBinding(session.binding, expected.binding) {
		return
	}
	epoch := p.txEpoch.Load()
	if epoch == 0 || p.tracked.txEpoch.Load() != epoch || session.txEpoch.Load() != epoch || expected.txEpoch.Load() != epoch || expected.receipt.txEpoch.Load() != epoch || !sameFirstAudioProbeBinding(expected.receipt.binding, expected.binding) {
		return
	}
	expected.receipt.deny()
}

func (p *firstAudioRealSQLAuthorizer) discardPinnedConnection() error {
	if p == nil || p.pinned == nil || p.connections == nil || p.raw == nil || p.registryID == "" {
		return errFirstAudioGuardProbeRejected
	}
	rawErr := p.pinned.Raw(func(any) error { return driver.ErrBadConn })
	if p.connections.IsLive(p.registryID, p.raw) {
		return fmt.Errorf("physical SQLite connection %s remains registered after ErrBadConn", p.registryID)
	}
	if p.tracked == nil || !p.tracked.closeOK.Load() {
		return fmt.Errorf("physical SQLite connection %s was retired from registry without a confirmed successful Close", p.registryID)
	}
	if rawErr != nil && !errors.Is(rawErr, driver.ErrBadConn) {
		return rawErr
	}
	return nil
}

func TestFirstAudioMatrixRealSQLDiscardPinnedConnectionRetiresPhysicalIdentity(t *testing.T) {
	registry := newFirstAudioSQLiteConnectionRegistry()
	driverName := fmt.Sprintf("first_audio_discard_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(conn *sqlite3.SQLiteConn) error {
		_, err := registry.Register(conn)
		return err
	}, registry.Retire); err != nil {
		t.Fatal("register tracked SQLite driver:", err)
	}
	pool, err := sql.Open(driverName, filepath.Join(t.TempDir(), "discard.db"))
	if err != nil {
		t.Fatal("open isolated SQLite pool:", err)
	}
	defer pool.Close()
	pinned, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin isolated SQLite connection:", err)
	}
	defer pinned.Close()
	var tracked *firstAudioTrackedSQLiteConn
	if err := pinned.Raw(func(driverConn any) error {
		var ok bool
		tracked, ok = driverConn.(*firstAudioTrackedSQLiteConn)
		if !ok || tracked == nil || tracked.raw == nil {
			return fmt.Errorf("unexpected tracked connection %T", driverConn)
		}
		return nil
	}); err != nil {
		t.Fatal("resolve isolated SQLite connection:", err)
	}
	registration, live := registry.Lookup(tracked.raw)
	if !live || !registry.IsLive(registration.ID, tracked.raw) {
		t.Fatal("new physical connection is absent from registry")
	}
	controller := newFirstAudioGuardController()
	authorizer := &firstAudioRealSQLAuthorizer{
		controller:  controller,
		connections: registry,
		registryID:  registration.ID,
		raw:         tracked.raw,
		tracked:     tracked,
		pinned:      pinned,
	}
	if err := authorizer.terminalizeAndDiscardUncertainConnection(); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("terminalize and discard uncertain physical connection = %v, want rejected probe after confirmed cleanup", err)
	}
	if registry.IsLive(registration.ID, tracked.raw) || !tracked.closeOK.Load() {
		t.Fatal("discarded physical connection lacks registry retirement or confirmed successful Close")
	}
	if !controller.IsTerminal() {
		t.Fatal("uncertain physical cleanup did not terminalize controller")
	}
}

func TestFirstAudioMatrixRealSQLRejectsCrossConnectionWrongAndReplayedTokens(t *testing.T) {
	registry := newFirstAudioSQLiteConnectionRegistry()
	controller := newFirstAudioGuardController()
	controller.connections = registry
	type candidate struct {
		pinned     *sql.Conn
		tracked    *firstAudioTrackedSQLiteConn
		authorizer *firstAudioRealSQLAuthorizer
		sessionRef *firstAudioRealSQLProbeSessionRef
		binding    firstAudioGuardProbeBinding
		token      []byte
		challenge  *firstAudioGuardProbeChallenge
		tokenCalls atomic.Uint64
	}
	harnesses := make(map[string]*candidate)
	var harnessMu sync.Mutex
	driverName := fmt.Sprintf("first_audio_cross_token_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(raw *sqlite3.SQLiteConn) error {
		registration, err := registry.Register(raw)
		if err != nil {
			return err
		}
		sessionRef := &firstAudioRealSQLProbeSessionRef{}
		authorizer := newFirstAudioRealSQLAuthorizer(controller, sessionRef, firstAudioDatabaseSnapshot{})
		current := &candidate{authorizer: authorizer, sessionRef: sessionRef}
		harnessMu.Lock()
		harnesses[registration.ID] = current
		harnessMu.Unlock()
		if err := raw.RegisterFunc("first_audio_probe", func(token []byte) bool {
			current.tokenCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.challenge == nil || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() {
				return false
			}
			return session.challenge.ConsumeInTransaction(token, session.binding, session.txEpoch.Load()) == nil
		}, true); err != nil {
			return err
		}
		return nil
	}, registry.Retire); err != nil {
		t.Fatal("register tracked SQLite driver:", err)
	}
	pool, err := sql.Open(driverName, filepath.Join(t.TempDir(), "cross-connection-token.db"))
	if err != nil {
		t.Fatal("open isolated SQLite pool:", err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(9)
	pool.SetMaxIdleConns(9)
	candidates := make([]*candidate, 0, 9)
	defer func() {
		for _, item := range candidates {
			zeroFirstAudioSecret(item.token)
			_ = item.pinned.Close()
		}
	}()
	defer func() {
		for _, item := range candidates {
			if item.tracked == nil || !item.tracked.txActive.Load() {
				continue
			}
			item.authorizer.cleanupPreflight.Store(true)
			item.authorizer.cleanupAvailable.Store(true)
			if cleanupErr := item.authorizer.rollbackProbeTransaction(); cleanupErr != nil {
				t.Errorf("candidate %s deferred transaction cleanup failed: %v", item.binding.connection, cleanupErr)
			}
		}
	}()
	for range 9 {
		pinned, pinErr := pool.Conn(context.Background())
		if pinErr != nil {
			t.Fatal("pin physical SQLite connection:", pinErr)
		}
		var tracked *firstAudioTrackedSQLiteConn
		if rawErr := pinned.Raw(func(conn any) error {
			var ok bool
			tracked, ok = conn.(*firstAudioTrackedSQLiteConn)
			if !ok || tracked == nil || tracked.raw == nil {
				return fmt.Errorf("unexpected tracked connection %T", conn)
			}
			return nil
		}); rawErr != nil {
			_ = pinned.Close()
			t.Fatal("resolve tracked physical connection:", rawErr)
		}
		registration, live := registry.Lookup(tracked.raw)
		if !live || !registry.IsLive(registration.ID, tracked.raw) {
			_ = pinned.Close()
			t.Fatal("physical connection missing from registry")
		}
		harnessMu.Lock()
		current := harnesses[registration.ID]
		harnessMu.Unlock()
		if current == nil {
			_ = pinned.Close()
			t.Fatal("physical connection has no authorizer harness")
		}
		current.pinned, current.tracked = pinned, tracked
		current.binding = firstAudioGuardProbeBinding{
			runID: "cross_token_20261028", lessonID: 501, generation: 1, version: 2,
			connection: registration.ID, probeID: "probe-" + strings.TrimPrefix(registration.ID, "conn-"),
		}
		current.token, current.challenge, err = newFirstAudioPhysicalProbeChallenge(current.binding)
		if err != nil {
			_ = pinned.Close()
			t.Fatal("issue isolated connection challenge:", err)
		}
		candidates = append(candidates, current)
	}
	seenConnectionIDs := make(map[string]struct{}, len(candidates))
	seenRawConnections := make(map[*sqlite3.SQLiteConn]struct{}, len(candidates))
	for _, item := range candidates {
		if _, duplicate := seenConnectionIDs[item.binding.connection]; duplicate {
			t.Fatalf("test pinned duplicate physical connection ID %q", item.binding.connection)
		}
		if _, duplicate := seenRawConnections[item.tracked.raw]; duplicate {
			t.Fatalf("test pinned duplicate physical SQLite connection for %q", item.binding.connection)
		}
		seenConnectionIDs[item.binding.connection] = struct{}{}
		seenRawConnections[item.tracked.raw] = struct{}{}
	}
	preflightSpec := firstAudioValidPreflightSpec(2)
	preflightSpec.runID = candidates[0].binding.runID
	preflightSpec.currentLessonID = candidates[0].binding.lessonID
	preflightSpec.connectionIDs = make([]string, 0, 9)
	preflightSpec.probeContracts = make([]firstAudioGuardProbeContract, 0, 9)
	preflightSpec.rowContracts = make([]firstAudioGuardRowContract, 0, 9)
	for _, item := range candidates {
		if !item.challenge.receipt.arm() {
			t.Fatal("arm fresh connection receipt")
		}
		preflightSpec.connectionIDs = append(preflightSpec.connectionIDs, item.binding.connection)
		preflightSpec.probeContracts = append(preflightSpec.probeContracts, item.challenge.Contract())
		preflightSpec.rowContracts = append(preflightSpec.rowContracts, firstAudioGuardRowContract{
			table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", item.binding.lessonID, item.binding.connection),
			oldRow: []byte("typed-projection"), newRow: []byte("typed-projection"),
		})
		controller.probeReceipts[item.binding.connection] = item.challenge.receipt
	}
	preflight, err := newFirstAudioGuardContext(preflightSpec)
	if err != nil {
		t.Fatal("construct isolated nine-connection PREFLIGHT:", err)
	}
	controller.publisher.current.Store(preflight)
	for _, item := range candidates {
		item.authorizer.tracked, item.authorizer.pinned = item.tracked, item.pinned
		item.authorizer.probeChallenge.Store(item.challenge)
		if err := item.authorizer.install(item.tracked.raw); err != nil {
			t.Fatal("install per-connection SQLite authorizer:", err)
		}
	}
	owner, foreign := candidates[0], candidates[1]
	owner.sessionRef.session.Store(&firstAudioRealSQLProbeSession{challenge: owner.challenge, binding: owner.binding, tracked: owner.tracked})
	var tx *sql.Tx
	if err := owner.authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		tx, beginErr = owner.pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin owner physical transaction:", err)
	}
	owner.authorizer.activeTx.Store(tx)
	epoch := owner.tracked.txEpoch.Load()
	if !owner.tracked.txActive.Load() || epoch == 0 {
		t.Fatal("owner physical transaction did not enter active epoch")
	}
	owner.authorizer.txEpoch.Store(epoch)
	owner.sessionRef.session.Load().txEpoch.Store(epoch)
	if err := owner.challenge.BindTransactionEpoch(epoch); err != nil {
		t.Fatal("bind owner challenge transaction epoch:", err)
	}
	var accepted bool
	err = owner.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return tx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", foreign.token).Scan(&accepted)
	})
	if err != nil {
		t.Fatalf("real SQLite cross-connection token query: %v", err)
	}
	if accepted {
		t.Fatal("connection A accepted connection B's challenge token")
	}
	if got := firstAudioGuardProbeReceiptState(owner.challenge.receipt.state.Load()); got != firstAudioGuardProbeFailed {
		t.Fatalf("owner receipt state=%d, want FAILED after mismatched token", got)
	}
	if got := firstAudioGuardProbeReceiptState(foreign.challenge.receipt.state.Load()); got != firstAudioGuardProbeIssued {
		t.Fatalf("foreign receipt state=%d, want ISSUED and untouched", got)
	}
	if owner.challenge.receipt.denials.Load() == 0 {
		t.Fatal("rejected owner challenge lacks denial evidence")
	}
	owner.authorizer.cleanupPreflight.Store(true)
	owner.authorizer.cleanupAvailable.Store(true)
	if err := owner.authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("authorized rollback after rejected callback: %v", err)
	}
	if owner.tracked.txActive.Load() || !owner.tracked.raw.AutoCommit() || owner.authorizer.denials.Load() != 0 {
		t.Fatal("ordinary token rejection did not permit exactly the cleanup rollback")
	}
	if firstAudioGuardProbeReceiptState(foreign.challenge.receipt.state.Load()) != firstAudioGuardProbeIssued || !registry.IsLive(foreign.binding.connection, candidates[1].tracked.raw) {
		t.Fatal("cross-connection rejection damaged the foreign candidate connection or receipt")
	}

	wrongOwner := candidates[2]
	wrongOwner.sessionRef.session.Store(&firstAudioRealSQLProbeSession{challenge: wrongOwner.challenge, binding: wrongOwner.binding, tracked: wrongOwner.tracked})
	var wrongTokenTx *sql.Tx
	if err := wrongOwner.authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		wrongTokenTx, beginErr = wrongOwner.pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin fresh wrong-token candidate transaction:", err)
	}
	wrongOwner.authorizer.activeTx.Store(wrongTokenTx)
	wrongTokenEpoch := wrongOwner.tracked.txEpoch.Load()
	if !wrongOwner.tracked.txActive.Load() || wrongTokenEpoch == 0 {
		t.Fatal("wrong-token candidate transaction did not enter active epoch")
	}
	wrongOwner.authorizer.txEpoch.Store(wrongTokenEpoch)
	wrongOwner.sessionRef.session.Load().txEpoch.Store(wrongTokenEpoch)
	if err := wrongOwner.challenge.BindTransactionEpoch(wrongTokenEpoch); err != nil {
		t.Fatal("bind fresh wrong-token challenge transaction epoch:", err)
	}
	wrongToken := append([]byte(nil), wrongOwner.token...)
	wrongToken[0] ^= 0xff
	defer zeroFirstAudioSecret(wrongToken)
	var wrongTokenAccepted bool
	err = wrongOwner.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return wrongTokenTx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", wrongToken).Scan(&wrongTokenAccepted)
	})
	if err != nil {
		t.Fatalf("real SQLite invalid-token query: %v", err)
	}
	if wrongTokenAccepted || firstAudioGuardProbeReceiptState(wrongOwner.challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || wrongOwner.challenge.receipt.denials.Load() == 0 {
		t.Fatal("fresh challenge did not reject unrelated token with explicit denial evidence")
	}
	wrongOwner.authorizer.cleanupPreflight.Store(true)
	wrongOwner.authorizer.cleanupAvailable.Store(true)
	if err := wrongOwner.authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("authorized rollback after unrelated-token rejection: %v", err)
	}
	if wrongOwner.tracked.txActive.Load() || !wrongOwner.tracked.raw.AutoCommit() || wrongOwner.authorizer.denials.Load() != 0 {
		t.Fatal("wrong-token rejection did not finish through the authorized cleanup rollback")
	}
	if firstAudioGuardProbeReceiptState(foreign.challenge.receipt.state.Load()) != firstAudioGuardProbeIssued {
		t.Fatal("unrelated-token rejection changed the other connection's receipt")
	}

	replayOwner := candidates[3]
	replayOwner.sessionRef.session.Store(&firstAudioRealSQLProbeSession{challenge: replayOwner.challenge, binding: replayOwner.binding, tracked: replayOwner.tracked})
	var replayTx *sql.Tx
	if err := replayOwner.authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		replayTx, beginErr = replayOwner.pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin fresh replay-candidate transaction:", err)
	}
	replayOwner.authorizer.activeTx.Store(replayTx)
	replayEpoch := replayOwner.tracked.txEpoch.Load()
	if !replayOwner.tracked.txActive.Load() || replayEpoch == 0 {
		t.Fatal("replay candidate transaction did not enter active epoch")
	}
	replayOwner.authorizer.txEpoch.Store(replayEpoch)
	replayOwner.sessionRef.session.Load().txEpoch.Store(replayEpoch)
	if err := replayOwner.challenge.BindTransactionEpoch(replayEpoch); err != nil {
		t.Fatal("bind replay challenge transaction epoch:", err)
	}
	var firstUseAccepted bool
	err = replayOwner.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return replayTx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", replayOwner.token).Scan(&firstUseAccepted)
	})
	if err != nil || !firstUseAccepted || firstAudioGuardProbeReceiptState(replayOwner.challenge.receipt.state.Load()) != firstAudioGuardProbeTokenConsumed {
		t.Fatalf("fresh replay candidate first token use accepted=%v receipt=%d err=%v", firstUseAccepted, replayOwner.challenge.receipt.state.Load(), err)
	}
	var replayAccepted bool
	err = replayOwner.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return replayTx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", replayOwner.token).Scan(&replayAccepted)
	})
	if err != nil {
		t.Fatalf("real SQLite replay-token query: %v", err)
	}
	if replayAccepted || firstAudioGuardProbeReceiptState(replayOwner.challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || replayOwner.challenge.receipt.denials.Load() == 0 {
		t.Fatal("replayed token was accepted or failed without explicit receipt denial")
	}
	replayOwner.authorizer.cleanupPreflight.Store(true)
	replayOwner.authorizer.cleanupAvailable.Store(true)
	if err := replayOwner.authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("authorized rollback after replay rejection: %v", err)
	}
	if replayOwner.tracked.txActive.Load() || !replayOwner.tracked.raw.AutoCommit() || replayOwner.authorizer.denials.Load() != 0 {
		t.Fatal("replay rejection did not finish through the authorized cleanup rollback")
	}

	epochOwner := candidates[4]
	epochOwner.sessionRef.session.Store(&firstAudioRealSQLProbeSession{challenge: epochOwner.challenge, binding: epochOwner.binding, tracked: epochOwner.tracked})
	var firstEpochTx *sql.Tx
	if err := epochOwner.authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		firstEpochTx, beginErr = epochOwner.pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin first transaction for cross-transaction replay case:", err)
	}
	epochOwner.authorizer.activeTx.Store(firstEpochTx)
	firstEpoch := epochOwner.tracked.txEpoch.Load()
	if !epochOwner.tracked.txActive.Load() || firstEpoch == 0 {
		t.Fatal("first cross-transaction epoch is not active and nonzero")
	}
	epochOwner.authorizer.txEpoch.Store(firstEpoch)
	epochOwner.sessionRef.session.Load().txEpoch.Store(firstEpoch)
	if err := epochOwner.challenge.BindTransactionEpoch(firstEpoch); err != nil {
		t.Fatal("bind challenge to first cross-transaction epoch:", err)
	}
	var acceptedInFirstEpoch bool
	if err := epochOwner.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return firstEpochTx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", epochOwner.token).Scan(&acceptedInFirstEpoch)
	}); err != nil || !acceptedInFirstEpoch || epochOwner.tokenCalls.Load() != 1 || firstAudioGuardProbeReceiptState(epochOwner.challenge.receipt.state.Load()) != firstAudioGuardProbeTokenConsumed {
		t.Fatalf("first-epoch token consumption accepted=%t callbacks=%d state=%d err=%v", acceptedInFirstEpoch, epochOwner.tokenCalls.Load(), epochOwner.challenge.receipt.state.Load(), err)
	}
	epochOwner.authorizer.cleanupPreflight.Store(true)
	epochOwner.authorizer.cleanupAvailable.Store(true)
	if err := epochOwner.authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("cleanup first cross-transaction epoch: %v", err)
	}
	if firstAudioGuardProbeReceiptState(epochOwner.challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || epochOwner.challenge.receipt.denials.Load() == 0 || epochOwner.challenge.receipt.completeFor(epochOwner.binding, epochOwner.challenge.digest) {
		t.Fatal("rollback left an incomplete token-consumed challenge reusable or complete")
	}
	if !registry.IsLive(epochOwner.binding.connection, epochOwner.tracked.raw) {
		t.Fatal("confirmed first rollback retired the same physical candidate connection")
	}

	var secondEpochTx *sql.Tx
	if err := epochOwner.authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		secondEpochTx, beginErr = epochOwner.pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin second transaction for cross-transaction replay case:", err)
	}
	epochOwner.authorizer.activeTx.Store(secondEpochTx)
	secondEpoch := epochOwner.tracked.txEpoch.Load()
	if !epochOwner.tracked.txActive.Load() || secondEpoch <= firstEpoch {
		t.Fatalf("tracked transaction epoch did not advance: first=%d second=%d active=%t", firstEpoch, secondEpoch, epochOwner.tracked.txActive.Load())
	}
	epochOwner.authorizer.txEpoch.Store(secondEpoch)
	epochOwner.sessionRef.session.Load().txEpoch.Store(secondEpoch)
	if epochOwner.challenge.txEpoch.Load() != firstEpoch || epochOwner.tracked.txEpoch.Load() != secondEpoch || epochOwner.sessionRef.session.Load().txEpoch.Load() != secondEpoch || epochOwner.authorizer.txEpoch.Load() != secondEpoch || epochOwner.authorizer.activeTx.Load() != secondEpochTx {
		t.Fatalf("cross-transaction references are inconsistent: challenge=%d tracked=%d session=%d authorizer=%d activeTxMatches=%t; first=%d second=%d", epochOwner.challenge.txEpoch.Load(), epochOwner.tracked.txEpoch.Load(), epochOwner.sessionRef.session.Load().txEpoch.Load(), epochOwner.authorizer.txEpoch.Load(), epochOwner.authorizer.activeTx.Load() == secondEpochTx, firstEpoch, secondEpoch)
	}
	var acceptedInSecondEpoch bool
	if err := epochOwner.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return secondEpochTx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", epochOwner.token).Scan(&acceptedInSecondEpoch)
	}); err != nil {
		t.Fatalf("real SQLite cross-transaction token replay query: %v", err)
	}
	if acceptedInSecondEpoch || epochOwner.tokenCalls.Load() != 2 || firstAudioGuardProbeReceiptState(epochOwner.challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || epochOwner.challenge.receipt.denials.Load() < 2 || epochOwner.challenge.receipt.completeFor(epochOwner.binding, epochOwner.challenge.digest) {
		t.Fatalf("cross-transaction replay evidence incomplete: accepted=%t callbacks=%d state=%d denials=%d", acceptedInSecondEpoch, epochOwner.tokenCalls.Load(), epochOwner.challenge.receipt.state.Load(), epochOwner.challenge.receipt.denials.Load())
	}
	epochOwner.authorizer.cleanupPreflight.Store(true)
	epochOwner.authorizer.cleanupAvailable.Store(true)
	if err := epochOwner.authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("cleanup second cross-transaction epoch: %v", err)
	}
	if epochOwner.tracked.txActive.Load() || !epochOwner.tracked.raw.AutoCommit() || !registry.IsLive(epochOwner.binding.connection, epochOwner.tracked.raw) {
		t.Fatal("second cross-transaction rollback failed to preserve a clean live pinned connection")
	}

	bindingMutations := []struct {
		name   string
		mutate func(*firstAudioGuardProbeBinding)
	}{
		{name: "run_id", mutate: func(binding *firstAudioGuardProbeBinding) { binding.runID = "cross_token_wrong_run" }},
		{name: "lesson_id", mutate: func(binding *firstAudioGuardProbeBinding) { binding.lessonID++ }},
		{name: "generation", mutate: func(binding *firstAudioGuardProbeBinding) { binding.generation++ }},
		{name: "context_version", mutate: func(binding *firstAudioGuardProbeBinding) { binding.version++ }},
	}
	for index, mutation := range bindingMutations {
		candidate := candidates[5+index]
		t.Run(mutation.name, func(t *testing.T) {
			type otherReceiptSnapshot struct {
				state      firstAudioGuardProbeReceiptState
				denials    uint64
				connection *sqlite3.SQLiteConn
			}
			othersBefore := make(map[string]otherReceiptSnapshot, len(candidates)-1)
			for _, other := range candidates {
				if other == candidate {
					continue
				}
				othersBefore[other.binding.connection] = otherReceiptSnapshot{state: firstAudioGuardProbeReceiptState(other.challenge.receipt.state.Load()), denials: other.challenge.receipt.denials.Load(), connection: other.tracked.raw}
			}
			session := &firstAudioRealSQLProbeSession{challenge: candidate.challenge, binding: candidate.binding, tracked: candidate.tracked}
			candidate.sessionRef.session.Store(session)
			var tx *sql.Tx
			if err := candidate.authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
				var beginErr error
				tx, beginErr = candidate.pinned.BeginTx(context.Background(), nil)
				return beginErr
			}); err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			candidate.authorizer.activeTx.Store(tx)
			t.Cleanup(func() {
				if !candidate.tracked.txActive.Load() {
					return
				}
				epoch := candidate.tracked.txEpoch.Load()
				candidate.authorizer.activeTx.Store(tx)
				candidate.authorizer.txEpoch.Store(epoch)
				session.txEpoch.Store(epoch)
				candidate.authorizer.cleanupPreflight.Store(true)
				candidate.authorizer.cleanupAvailable.Store(true)
				if cleanupErr := candidate.authorizer.rollbackProbeTransaction(); cleanupErr != nil {
					t.Errorf("cleanup altered-%s binding candidate: %v", mutation.name, cleanupErr)
				}
			})
			epoch := candidate.tracked.txEpoch.Load()
			if !candidate.tracked.txActive.Load() || epoch == 0 {
				t.Fatalf("tracked transaction is not active at a nonzero epoch: epoch=%d", epoch)
			}
			candidate.authorizer.txEpoch.Store(epoch)
			session.txEpoch.Store(epoch)
			if err := candidate.challenge.BindTransactionEpoch(epoch); err != nil {
				t.Fatalf("bind challenge epoch: %v", err)
			}
			var accepted bool
			err := candidate.authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
				original := session.binding
				mutated := original
				mutation.mutate(&mutated)
				session.binding = mutated
				defer func() { session.binding = original }()
				return tx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", candidate.token).Scan(&accepted)
			})
			if err != nil {
				t.Fatalf("real SQLite UDF query with one altered %s binding: %v", mutation.name, err)
			}
			if accepted || candidate.tokenCalls.Load() != 1 || candidate.authorizer.denials.Load() != 0 || firstAudioGuardProbeReceiptState(candidate.challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || candidate.challenge.receipt.denials.Load() == 0 || candidate.challenge.receipt.completeFor(candidate.binding, candidate.challenge.digest) {
				t.Fatalf("altered %s binding was not rejected by the real UDF: accepted=%t callbacks=%d authorizerDenials=%d state=%d denials=%d", mutation.name, accepted, candidate.tokenCalls.Load(), candidate.authorizer.denials.Load(), candidate.challenge.receipt.state.Load(), candidate.challenge.receipt.denials.Load())
			}
			if !sameFirstAudioProbeBinding(session.binding, candidate.binding) {
				t.Fatalf("%s binding was not restored before cleanup", mutation.name)
			}
			for _, other := range candidates {
				if other == candidate {
					continue
				}
				before := othersBefore[other.binding.connection]
				if before.state != firstAudioGuardProbeReceiptState(other.challenge.receipt.state.Load()) || before.denials != other.challenge.receipt.denials.Load() || before.connection != other.tracked.raw || !registry.IsLive(other.binding.connection, before.connection) {
					t.Fatalf("%s binding case changed independent candidate %s", mutation.name, other.binding.connection)
				}
			}
			candidate.authorizer.cleanupPreflight.Store(true)
			candidate.authorizer.cleanupAvailable.Store(true)
			if err := candidate.authorizer.rollbackProbeTransaction(); err != nil {
				t.Fatalf("authorized rollback after %s binding rejection: %v", mutation.name, err)
			}
			if candidate.tracked.txActive.Load() || !candidate.tracked.raw.AutoCommit() || !registry.IsLive(candidate.binding.connection, candidate.tracked.raw) {
				t.Fatalf("%s binding candidate did not return to a clean live connection", mutation.name)
			}
		})
	}
}

const firstAudioDirectRowGuardChildMarker = "ENWORDS_FIRST_AUDIO_DIRECT_ROW_GUARD_CHILD"

type firstAudioCappedChildOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *firstAudioCappedChildOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		write := len(p)
		if write > remaining {
			write = remaining
		}
		_, _ = b.buffer.Write(p[:write])
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func (b *firstAudioCappedChildOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.buffer.String()
	if b.truncated {
		result += "\n[child output truncated at 256 KiB]"
	}
	return result
}

func TestFirstAudioMatrixRealSQLRejectsDirectRowGuardUDF(t *testing.T) {
	if os.Getenv(firstAudioDirectRowGuardChildMarker) == "1" {
		testFirstAudioDirectRowGuardUDFChild(t)
		return
	}

	childEnv := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, firstAudioDirectRowGuardChildMarker+"=") {
			childEnv = append(childEnv, entry)
		}
	}
	childEnv = append(childEnv, firstAudioDirectRowGuardChildMarker+"=1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFirstAudioMatrixRealSQLRejectsDirectRowGuardUDF$", "-test.v")
	command.Env = childEnv
	output := &firstAudioCappedChildOutput{limit: 256 << 10}
	command.Stdout = output
	command.Stderr = output
	err := command.Run() // Run waits/reaps the child after exit or CommandContext cancellation.
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatalf("isolated direct-UDF child was not reaped: err=%v output=%s", err, output.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("isolated direct-UDF child exceeded 30-second deadline and was terminated/reaped: %v; output=%s", ctx.Err(), output.String())
	}
	if err != nil {
		t.Fatalf("isolated direct-UDF child failed: %v; output=%s", err, output.String())
	}
	t.Log(output.String())
}

func testFirstAudioDirectRowGuardUDFChild(t *testing.T) {
	t.Helper()
	controller := newFirstAudioGuardController()
	registry := controller.connections
	sessionRef := &firstAudioRealSQLProbeSessionRef{}
	var rowGuardCalls atomic.Uint64
	var registeredRaw *sqlite3.SQLiteConn
	driverName := fmt.Sprintf("first_audio_direct_row_guard_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(raw *sqlite3.SQLiteConn) error {
		if _, err := registry.Register(raw); err != nil {
			return err
		}
		registeredRaw = raw
		return raw.RegisterFunc("first_audio_row_guard", func(args ...any) bool {
			rowGuardCalls.Add(1)
			return len(args) == len(firstAudioLessonProbeColumns)*4
		}, true)
	}, registry.Retire); err != nil {
		t.Fatal("register isolated tracked driver:", err)
	}
	path := filepath.Join(t.TempDir(), "direct-row-guard.db")
	pool, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal("open isolated direct-UDF pool:", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pinned, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin isolated direct-UDF connection:", err)
	}
	t.Cleanup(func() { _ = pinned.Close() })
	var tracked *firstAudioTrackedSQLiteConn
	if err := pinned.Raw(func(connection any) error {
		var ok bool
		tracked, ok = connection.(*firstAudioTrackedSQLiteConn)
		if !ok || tracked == nil || tracked.raw == nil || tracked.raw != registeredRaw {
			return fmt.Errorf("pinned direct-UDF connection does not match registered raw identity: %T", connection)
		}
		return nil
	}); err != nil {
		t.Fatal("resolve registered direct-UDF connection:", err)
	}
	registration, live := registry.Lookup(tracked.raw)
	if !live || registration.ID == "" || !registry.IsLive(registration.ID, tracked.raw) {
		t.Fatal("direct-UDF physical connection is not live in the registry")
	}
	binding := firstAudioGuardProbeBinding{runID: "direct_udf_20261029", lessonID: 501, generation: 1, version: 2, connection: registration.ID, probeID: "probe_direct_udf"}
	token, challenge, err := newFirstAudioPhysicalProbeChallenge(binding)
	if err != nil {
		t.Fatal("create direct-UDF challenge:", err)
	}
	defer zeroFirstAudioSecret(token)
	if !challenge.receipt.arm() {
		t.Fatal("arm direct-UDF challenge receipt")
	}
	preflightSpec := firstAudioValidPreflightSpec(2)
	preflightSpec.runID = binding.runID
	preflightSpec.currentLessonID = binding.lessonID
	preflightSpec.connectionIDs = []string{binding.connection}
	preflightSpec.probeContracts = []firstAudioGuardProbeContract{challenge.Contract()}
	preflightSpec.rowContracts = []firstAudioGuardRowContract{{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, binding.connection), oldRow: []byte("typed-projection"), newRow: []byte("typed-projection")}}
	controller.probeReceipts[binding.connection] = challenge.receipt
	preflight, err := newFirstAudioGuardContext(preflightSpec)
	if err != nil {
		t.Fatal("construct one-connection direct-UDF PREFLIGHT:", err)
	}
	controller.publisher.current.Store(preflight)
	authorizer := newFirstAudioRealSQLAuthorizer(controller, sessionRef, firstAudioDatabaseSnapshot{})
	authorizer.tracked = tracked
	authorizer.pinned = pinned
	authorizer.probeChallenge.Store(challenge)
	if err := authorizer.install(tracked.raw); err != nil {
		t.Fatal("install production-shaped SQL authorizer:", err)
	}
	session := &firstAudioRealSQLProbeSession{challenge: challenge, binding: binding, tracked: tracked}
	sessionRef.session.Store(session)
	var tx *sql.Tx
	if err := authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		tx, beginErr = pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin isolated direct-UDF transaction:", err)
	}
	authorizer.activeTx.Store(tx)
	t.Cleanup(func() {
		if !registry.IsLive(binding.connection, tracked.raw) {
			return
		}
		cleanupErr := authorizer.terminalizeAndDiscardUncertainConnection()
		if !errors.Is(cleanupErr, errFirstAudioGuardProbeRejected) {
			t.Errorf("terminalize/discard direct-UDF child after early failure: %v", cleanupErr)
		}
	})
	epoch := tracked.txEpoch.Load()
	if !tracked.txActive.Load() || epoch == 0 {
		t.Fatal("direct-UDF transaction is not active at a nonzero tracked epoch")
	}
	authorizer.txEpoch.Store(epoch)
	session.txEpoch.Store(epoch)
	if err := challenge.BindTransactionEpoch(epoch); err != nil {
		t.Fatal("bind direct-UDF challenge epoch:", err)
	}
	arguments := make([]string, len(firstAudioLessonProbeColumns)*4)
	for i := range arguments {
		arguments[i] = "NULL"
	}
	if len(arguments) != 72 {
		t.Fatalf("frozen direct-UDF argument count=%d, want 72", len(arguments))
	}
	statement := "SELECT first_audio_row_guard(" + strings.Join(arguments, ",") + ")"
	var result bool
	queryErr := authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return tx.QueryRowContext(context.Background(), statement).Scan(&result)
	})
	var sqliteErr sqlite3.Error
	if queryErr == nil || !errors.As(queryErr, &sqliteErr) || sqliteErr.Code != sqlite3.ErrError || !strings.Contains(strings.ToLower(queryErr.Error()), "not authorized") {
		t.Fatalf("direct top-level row-guard call error=%T %v sqlite_code=%d want SQLITE_ERROR=%d with authorization-denial text", queryErr, queryErr, sqliteErr.Code, sqlite3.ErrError)
	}
	deniedTuple := authorizer.lastDenied.Load()
	deniedTupleText, tupleIsString := deniedTuple.(string)
	wantDeniedTuple := fmt.Sprintf("phase=%s permit=%d action=%d db=%q source=%q arg1=%q arg2=%q", firstAudioGuardPreflight, firstAudioSQLPermitTokenConsumption, sqlite3.SQLITE_FUNCTION, "", "", "", "first_audio_row_guard")
	if authorizer.denials.Load() != 1 || !tupleIsString || deniedTupleText != wantDeniedTuple {
		t.Fatalf("authorizer denial did not match exact direct empty-source SQLITE_FUNCTION tuple: count=%d tuple=%v want=%q", authorizer.denials.Load(), deniedTuple, wantDeniedTuple)
	}
	directDenials := authorizer.denials.Load()
	if rowGuardCalls.Load() != 0 || !controller.IsTerminal() {
		t.Fatalf("direct row-guard UDF executed or controller remained live: callbacks=%d terminal=%t", rowGuardCalls.Load(), controller.IsTerminal())
	}
	cleanupErr := authorizer.terminalizeAndDiscardUncertainConnection()
	if !errors.Is(cleanupErr, errFirstAudioGuardProbeRejected) {
		t.Fatalf("terminalize/discard after direct-UDF authorizer denial=%v, want rejected after confirmed retirement", cleanupErr)
	}
	if firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || challenge.receipt.completeFor(binding, challenge.digest) || tracked.txActive.Load() || !tracked.closeOK.Load() || registry.IsLive(binding.connection, tracked.raw) {
		t.Fatalf("direct-UDF terminal evidence incomplete: receipt=%d complete=%t txActive=%t closeOK=%t live=%t", challenge.receipt.state.Load(), challenge.receipt.completeFor(binding, challenge.digest), tracked.txActive.Load(), tracked.closeOK.Load(), registry.IsLive(binding.connection, tracked.raw))
	}
	cleanupDenials := authorizer.denials.Load() - directDenials
	t.Logf("trace_id=%s module=first_audio_preflight event=direct_row_guard_udf_denied payload={sqlite_code:%d,direct_denials_before_cleanup:%d,cleanup_rollback_denials:%d,udf_callbacks:%d,receipt_state:%d,physical_close:%t,registry_live:%t}", binding.runID, sqliteErr.Code, directDenials, cleanupDenials, rowGuardCalls.Load(), challenge.receipt.state.Load(), tracked.closeOK.Load(), registry.IsLive(binding.connection, tracked.raw))
}

func TestFirstAudioMatrixRealSQLMissingCleanupAuthorityTerminalizesAndDiscardsActiveTransaction(t *testing.T) {
	registry := newFirstAudioSQLiteConnectionRegistry()
	driverName := fmt.Sprintf("first_audio_missing_cleanup_authority_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(conn *sqlite3.SQLiteConn) error {
		_, err := registry.Register(conn)
		return err
	}, registry.Retire); err != nil {
		t.Fatal("register tracked SQLite driver:", err)
	}
	pool, err := sql.Open(driverName, filepath.Join(t.TempDir(), "missing-cleanup-authority.db"))
	if err != nil {
		t.Fatal("open isolated SQLite pool:", err)
	}
	defer pool.Close()
	pinned, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin isolated SQLite connection:", err)
	}
	defer pinned.Close()
	var tracked *firstAudioTrackedSQLiteConn
	if err := pinned.Raw(func(driverConn any) error {
		var ok bool
		tracked, ok = driverConn.(*firstAudioTrackedSQLiteConn)
		if !ok || tracked == nil || tracked.raw == nil {
			return fmt.Errorf("unexpected tracked connection %T", driverConn)
		}
		return nil
	}); err != nil {
		t.Fatal("resolve isolated SQLite connection:", err)
	}
	registration, live := registry.Lookup(tracked.raw)
	if !live {
		t.Fatal("new physical connection is absent from registry")
	}
	tx, err := pinned.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("begin physical probe transaction:", err)
	}
	defer func() {
		// The failure path below evicts the physical driver connection while
		// database/sql.Tx still owns the Conn lease. Rollback releases that Go
		// lease during teardown; the underlying physical connection is already
		// closed and cannot return to the pool.
		_ = tx.Rollback()
	}()
	epoch := tracked.txEpoch.Load()
	if !tracked.txActive.Load() || epoch == 0 {
		t.Fatal("tracked physical transaction did not enter an active epoch")
	}
	controller := newFirstAudioGuardController()
	binding := firstAudioGuardProbeBinding{
		runID: "run-0001", lessonID: 101, generation: 1, version: 2,
		connection: registration.ID, probeID: "probe-" + registration.ID,
	}
	token, challenge, err := newFirstAudioPhysicalProbeChallenge(binding)
	if err != nil {
		t.Fatal("construct real-SQL cleanup candidate:", err)
	}
	defer zeroFirstAudioSecret(token)
	if err := challenge.BindTransactionEpoch(epoch); err != nil {
		t.Fatal("bind challenge to active SQLite transaction:", err)
	}
	if !challenge.receipt.arm() {
		t.Fatal("arm cleanup candidate receipt")
	}
	preflightSpec := firstAudioValidPreflightSpec(2)
	preflightSpec.connectionIDs[0] = binding.connection
	preflightSpec.probeContracts[0] = challenge.Contract()
	preflightSpec.rowContracts[0].identity = fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, binding.connection)
	preflight, err := newFirstAudioGuardContext(preflightSpec)
	if err != nil {
		t.Fatal("construct PREFLIGHT context:", err)
	}
	controller.publisher.current.Store(preflight)
	controller.mu.Lock()
	controller.probeReceipts[registration.ID] = challenge.receipt
	controller.mu.Unlock()
	sessionRef := &firstAudioRealSQLProbeSessionRef{}
	session := &firstAudioRealSQLProbeSession{challenge: challenge, binding: binding, tracked: tracked}
	session.txEpoch.Store(epoch)
	sessionRef.session.Store(session)
	authorizer := &firstAudioRealSQLAuthorizer{
		controller:  controller,
		connections: registry,
		registryID:  registration.ID,
		raw:         tracked.raw,
		sessionRef:  sessionRef,
		tracked:     tracked,
		pinned:      pinned,
	}
	authorizer.probeChallenge.Store(challenge)
	authorizer.activeTx.Store(tx)
	authorizer.txEpoch.Store(epoch)
	if err := authorizer.install(tracked.raw); err != nil {
		t.Fatal("install real SQLite authorizer before terminal cleanup:", err)
	}
	foreignBinding := binding
	foreignBinding.connection = "conn-foreign"
	foreignBinding.probeID = "probe-foreign"
	foreignToken, foreignChallenge, err := newFirstAudioPhysicalProbeChallenge(foreignBinding)
	if err != nil {
		t.Fatal("construct foreign connection challenge:", err)
	}
	defer zeroFirstAudioSecret(foreignToken)
	foreignSession := &firstAudioRealSQLProbeSession{challenge: foreignChallenge, binding: foreignBinding, tracked: tracked}
	foreignSession.txEpoch.Store(epoch)
	sessionRef.session.Store(foreignSession)
	authorizer.failBoundProbeReceipt()
	if firstAudioGuardProbeReceiptState(foreignChallenge.receipt.state.Load()) != firstAudioGuardProbeIssued || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeIssued {
		t.Fatal("mismatched session invalidated a foreign or unverified challenge receipt")
	}
	sessionRef.session.Store(session)
	// cleanupPreflight is deliberately absent while a transaction is active.
	// The helper must fail closed rather than leave this connection reusable.
	if !tracked.txActive.Load() || tracked.closeOK.Load() || tracked.raw.AutoCommit() {
		t.Fatal("test setup did not reach an active transaction before cleanup rejection")
	}
	if err := authorizer.rollbackProbeTransaction(); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("rollback with missing cleanup authority = %v, want rejected probe after terminal cleanup", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("missing cleanup authority left PREFLIGHT active or failed to terminalize")
	}
	if active := controller.publisher.Load(); active == nil || active.phase != firstAudioGuardTerminal {
		t.Fatalf("terminalized controller context=%#v; want TERMINAL", active)
	}
	if firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatal("terminal cleanup left its real-SQL receipt reusable or complete")
	}
	if authorizer.denials.Load() == 0 {
		t.Fatal("terminal transaction-lease release was not denied by the SQLite authorizer")
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(4, binding.lessonID, binding.generation, 1))
	if err != nil {
		t.Fatal("construct forbidden post-terminal JOURNEY:", err)
	}
	if err := controller.publisher.Publish(journey); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("JOURNEY publication after uncertain cleanup = %v, want terminal rejection", err)
	}
	if registry.IsLive(registration.ID, tracked.raw) || !tracked.closeOK.Load() {
		t.Fatal("uncertain active transaction connection was not retired after successful Close")
	}
	if tracked.txActive.Load() || !tracked.closeOK.Load() {
		t.Fatal("successful physical Close did not retire the formerly active SQLite transaction")
	}
}

func (p *firstAudioRealSQLAuthorizer) readTargetLessonProjection(tx *sql.Tx, lessonID int64) ([]any, error) {
	session := p.sessionRef.session.Load()
	if tx == nil || lessonID <= 0 || session == nil || session.binding.lessonID != lessonID {
		return nil, errFirstAudioGuardProbeRejected
	}
	var values []any
	err := p.withPermit(firstAudioSQLPermitProjectionRead, true, func() error {
		rows, err := tx.QueryContext(context.Background(), firstAudioLessonTypedProjectionSQL(), lessonID)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate typed lesson projection: %w", err)
			}
			return fmt.Errorf("typed lesson projection returned no row")
		}
		values = make([]any, len(firstAudioLessonProbeColumns)*2)
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		if rows.Next() {
			return fmt.Errorf("typed lesson projection returned multiple rows")
		}
		return rows.Err()
	})
	return values, err
}

func (p *firstAudioRealSQLAuthorizer) confirmSnapshotReadback(challenge *firstAudioGuardProbeChallenge) error {
	session := p.sessionRef.session.Load()
	if challenge == nil || challenge.receipt == nil || !challenge.receipt.requiresPhysicalReadback || session == nil || session.challenge != challenge || p.pinnedBaselineSHA == "" || p.pinnedBaselineCanonical == nil || p.db == nil || p.pinned == nil || p.tracked == nil || p.tracked.txActive.Load() || !p.raw.AutoCommit() || p.txEpoch.Load() == 0 || p.txEpoch.Load() != challenge.txEpoch.Load() || p.txEpoch.Load() != challenge.receipt.txEpoch.Load() || !p.connections.IsLive(p.registryID, p.raw) {
		return errFirstAudioGuardProbeRejected
	}
	var after firstAudioDatabaseSnapshot
	if err := p.withPermit(firstAudioSQLPermitSnapshotRead, false, func() error {
		var snapshotErr error
		after, snapshotErr = firstAudioCaptureDatabaseSnapshotOnPinnedConn(p.db, p.pinned)
		return snapshotErr
	}); err != nil {
		return fmt.Errorf("capture authorizer-owned post-rollback snapshot: %w", err)
	}
	if after.SHA256 != p.pinnedBaselineSHA || !reflect.DeepEqual(after.Canonical, p.pinnedBaselineCanonical) {
		return fmt.Errorf("authorizer-owned post-rollback snapshot differs from pinned baseline: baseline=%s after=%s", p.pinnedBaselineSHA, after.SHA256)
	}
	if !challenge.receipt.beginProbeActivity() {
		return errFirstAudioGuardProbeRejected
	}
	defer challenge.receipt.endProbeActivity()
	return p.connections.WithLiveRegistration(p.registryID, p.raw, func() error {
		if challenge.receipt.denials.Load() != 0 || !challenge.receipt.state.CompareAndSwap(uint32(firstAudioGuardProbeRollbackVerified), uint32(firstAudioGuardProbeReadbackVerified)) {
			challenge.receipt.deny()
			return errFirstAudioGuardProbeRejected
		}
		challenge.receipt.readbackRaw.Store(p.raw)
		challenge.receipt.physicalReadback.Store(true)
		return nil
	})
}

func (p *firstAudioRealSQLAuthorizer) capturePinnedBaseline() error {
	if p == nil || p.baselineSHA == "" || p.baselineCanonical == nil || p.db == nil || p.pinned == nil {
		return errFirstAudioGuardProbeRejected
	}
	var snapshot firstAudioDatabaseSnapshot
	err := p.withPermit(firstAudioSQLPermitSnapshotRead, false, func() error {
		var snapshotErr error
		snapshot, snapshotErr = firstAudioCaptureDatabaseSnapshotOnPinnedConn(p.db, p.pinned)
		return snapshotErr
	})
	if err != nil {
		return fmt.Errorf("capture authorizer-owned pinned baseline: %w", err)
	}
	if snapshot.SHA256 != p.baselineSHA || !reflect.DeepEqual(snapshot.Canonical, p.baselineCanonical) {
		return fmt.Errorf("pinned baseline differs from isolated setup snapshot: setup=%s pinned=%s", p.baselineSHA, snapshot.SHA256)
	}
	p.pinnedBaselineSHA = snapshot.SHA256
	p.pinnedBaselineCanonical = append([]byte(nil), snapshot.Canonical...)
	return nil
}

func (p *firstAudioRealSQLAuthorizer) ownsActiveReceipt(receipt *firstAudioGuardProbeReceipt, binding firstAudioGuardProbeBinding) bool {
	c := p.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.publisher.Load()
	if active == nil {
		return false
	}
	owned := c.probeReceipts[p.registryID]
	contract, hasContract := active.probes[p.registryID]
	return !c.isTerminalLocked() && active.phase == firstAudioGuardPreflight && owned == receipt && receipt != nil && hasContract && binding.matchesContract(contract) && sameFirstAudioProbeBinding(receipt.binding, binding) && receipt.armed.Load()
}

func firstAudioAuthorizePreflightTuple(permit firstAudioSQLPermit, reads map[string]map[string]struct{}, action int, arg1, arg2, databaseName, triggerName string) bool {
	if permit == firstAudioSQLPermitSnapshotRead {
		if triggerName != "" {
			return false
		}
		switch action {
		case sqlite3.SQLITE_SELECT:
			return true
		case sqlite3.SQLITE_READ:
			_, ok := reads[arg1][arg2]
			return ok && (databaseName == "main" || (databaseName == "" && arg2 == ""))
		}
		return false
	}
	if permit == firstAudioSQLPermitTokenConsumption {
		return triggerName == "" && ((action == sqlite3.SQLITE_SELECT && arg1 == "" && arg2 == "" && databaseName == "") || (action == sqlite3.SQLITE_FUNCTION && arg1 == "" && arg2 == "first_audio_probe" && databaseName == ""))
	}
	if permit == firstAudioSQLPermitProjectionRead {
		if triggerName != "" {
			return false
		}
		if action == sqlite3.SQLITE_SELECT {
			return arg1 == "" && arg2 == "" && databaseName == ""
		}
		if action == sqlite3.SQLITE_READ {
			_, ok := firstAudioLessonProbeColumnSet()[arg2]
			return ok && arg1 == "lessons" && databaseName == "main"
		}
		if action == sqlite3.SQLITE_FUNCTION {
			return arg1 == "" && arg2 == "typeof" && databaseName == ""
		}
		return false
	}
	if permit == firstAudioSQLPermitTransactionCleanup {
		return action == sqlite3.SQLITE_TRANSACTION && arg1 == "ROLLBACK" && arg2 == "" && databaseName == "" && triggerName == ""
	}
	if permit == firstAudioSQLPermitProbeSQL {
		if action == sqlite3.SQLITE_TRANSACTION {
			return arg1 == "BEGIN" && arg2 == "" && databaseName == "" && triggerName == ""
		}
		if triggerName == "" && databaseName == "main" {
			if action == sqlite3.SQLITE_UPDATE {
				return arg1 == "lessons" && arg2 == "id"
			}
			if action == sqlite3.SQLITE_READ {
				return arg1 == "lessons" && arg2 == "id"
			}
		}
		if triggerName == "fa_lessons_guard_update" {
			if action == sqlite3.SQLITE_SELECT {
				return arg1 == "" && arg2 == "" && databaseName == ""
			}
			if action == sqlite3.SQLITE_READ {
				_, ok := firstAudioLessonProbeColumnSet()[arg2]
				return arg1 == "lessons" && databaseName == "main" && ok
			}
			if action == sqlite3.SQLITE_FUNCTION {
				return arg1 == "" && databaseName == "" && (arg2 == "typeof" || arg2 == "first_audio_row_guard")
			}
		}
	}
	return false
}

func firstAudioLessonProbeColumnSet() map[string]struct{} {
	columns := make(map[string]struct{}, len(firstAudioLessonProbeColumns))
	for _, column := range firstAudioLessonProbeColumns {
		columns[column] = struct{}{}
	}
	return columns
}

func TestFirstAudioRealSQLPreflightAuthorizerHasFinitePhaseAndPermitTuples(t *testing.T) {
	reads := firstAudioStartupReadManifest()
	allowed := []struct {
		permit                 firstAudioSQLPermit
		action                 int
		arg1, arg2, db, source string
	}{
		{firstAudioSQLPermitSnapshotRead, sqlite3.SQLITE_SELECT, "", "", "", ""},
		{firstAudioSQLPermitSnapshotRead, sqlite3.SQLITE_READ, "users", "id", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_TRANSACTION, "BEGIN", "", "", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_UPDATE, "lessons", "id", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_READ, "lessons", "id", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_SELECT, "", "", "", "fa_lessons_guard_update"},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_READ, "lessons", "state_upd_at", "main", "fa_lessons_guard_update"},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_FUNCTION, "", "first_audio_row_guard", "", "fa_lessons_guard_update"},
		{firstAudioSQLPermitTokenConsumption, sqlite3.SQLITE_SELECT, "", "", "", ""},
		{firstAudioSQLPermitTokenConsumption, sqlite3.SQLITE_FUNCTION, "", "first_audio_probe", "", ""},
		{firstAudioSQLPermitProjectionRead, sqlite3.SQLITE_READ, "lessons", "finished_at", "main", ""},
		{firstAudioSQLPermitProjectionRead, sqlite3.SQLITE_FUNCTION, "", "typeof", "", ""},
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "ROLLBACK", "", "", ""},
	}
	for _, tuple := range allowed {
		if !firstAudioAuthorizePreflightTuple(tuple.permit, reads, tuple.action, tuple.arg1, tuple.arg2, tuple.db, tuple.source) {
			t.Errorf("reviewed tuple denied: %#v", tuple)
		}
	}
	denied := []struct {
		permit                 firstAudioSQLPermit
		action                 int
		arg1, arg2, db, source string
	}{
		{firstAudioSQLPermitSnapshotRead, sqlite3.SQLITE_UPDATE, "lessons", "id", "main", ""},
		{firstAudioSQLPermitSnapshotRead, sqlite3.SQLITE_FUNCTION, "", "coalesce", "", ""},
		{firstAudioSQLPermitSnapshotRead, sqlite3.SQLITE_READ, "lessons", "unmanifested", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_TRANSACTION, "COMMIT", "", "", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_UPDATE, "lessons", "status", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_READ, "lessons", "status", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_DELETE, "lessons", "", "main", ""},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_SELECT, "", "", "", "unreviewed_trigger"},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_READ, "lessons", "id", "temp", "fa_lessons_guard_update"},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_FUNCTION, "", "load_extension", "", "fa_lessons_guard_update"},
		{firstAudioSQLPermitTokenConsumption, sqlite3.SQLITE_FUNCTION, "", "sqlite_version", "", ""},
		{firstAudioSQLPermitTokenConsumption, sqlite3.SQLITE_READ, "users", "id", "main", ""},
		{firstAudioSQLPermitProjectionRead, sqlite3.SQLITE_READ, "lessons", "status", "temp", ""},
		{firstAudioSQLPermitProjectionRead, sqlite3.SQLITE_FUNCTION, "", "random", "", ""},
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "COMMIT", "", "", ""},
		{firstAudioSQLPermitNone, sqlite3.SQLITE_SELECT, "", "", "", ""},
	}
	for _, tuple := range denied {
		if firstAudioAuthorizePreflightTuple(tuple.permit, reads, tuple.action, tuple.arg1, tuple.arg2, tuple.db, tuple.source) {
			t.Errorf("unreviewed tuple allowed: %#v", tuple)
		}
	}
}

func TestFirstAudioMatrixTerminalCleanupPermitAllowsOnlyActiveRollback(t *testing.T) {
	allowed := firstAudioAuthorizeCleanupTuple(firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "ROLLBACK", "", "", "", true)
	if !allowed {
		t.Fatal("one-shot cleanup permit denied ROLLBACK on the active probe transaction")
	}
	denied := []struct {
		permit                 firstAudioSQLPermit
		action                 int
		arg1, arg2, db, source string
		active                 bool
	}{
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "COMMIT", "", "", "", true},
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_SELECT, "", "", "", "", true},
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "ROLLBACK", "", "main", "", true},
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "ROLLBACK", "", "", "trigger", true},
		{firstAudioSQLPermitTransactionCleanup, sqlite3.SQLITE_TRANSACTION, "ROLLBACK", "", "", "", false},
		{firstAudioSQLPermitProbeSQL, sqlite3.SQLITE_TRANSACTION, "ROLLBACK", "", "", "", true},
	}
	for _, tuple := range denied {
		if firstAudioAuthorizeCleanupTuple(tuple.permit, tuple.action, tuple.arg1, tuple.arg2, tuple.db, tuple.source, tuple.active) {
			t.Errorf("terminal cleanup accepted a non-cleanup tuple: %#v", tuple)
		}
	}
}

func TestFirstAudioMatrixCleanupPhaseAllowsOnlyPreflightOrItsTerminalCleanup(t *testing.T) {
	controller := newFirstAudioGuardController()
	driver, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil {
		t.Fatal("construct DRIVER_PROBE context:", err)
	}
	controller.publisher.current.Store(driver)
	if firstAudioCleanupPhaseAllowed(controller, true) {
		t.Fatal("cleanup permit accepted during DRIVER_PROBE")
	}
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		t.Fatal("construct STARTUP_PROBE context:", err)
	}
	controller.publisher.current.Store(startup)
	if firstAudioCleanupPhaseAllowed(controller, true) {
		t.Fatal("cleanup permit accepted during STARTUP_PROBE")
	}
	preflight, err := newFirstAudioGuardContext(firstAudioValidPreflightSpec(2))
	if err != nil {
		t.Fatal("construct PREFLIGHT context:", err)
	}
	controller.publisher.current.Store(preflight)
	if !firstAudioCleanupPhaseAllowed(controller, true) {
		t.Fatal("PREFLIGHT failed to retain its coordinator-only rollback permit")
	}
	if err := controller.TerminalizeFromSQLiteCallback(); err != nil {
		t.Fatal("terminalize callback candidate:", err)
	}
	if !firstAudioCleanupPhaseAllowed(controller, true) {
		t.Fatal("callback-terminal candidate lost the one rollback required to clean its PREFLIGHT transaction")
	}
	if firstAudioCleanupPhaseAllowed(controller, false) {
		t.Fatal("terminal candidate without a PREFLIGHT transaction retained cleanup authority")
	}
}

type firstAudioExpectedTypedProjection struct {
	values []any
}

func firstAudioLessonTypedProjectionSQL() string {
	columns := make([]string, 0, len(firstAudioLessonProbeColumns)*2)
	for _, column := range firstAudioLessonProbeColumns {
		quoted := quoteFirstAudioIdentifier(column)
		columns = append(columns, "typeof("+quoted+")", "CASE WHEN typeof("+quoted+")='text' THEN CAST("+quoted+" AS TEXT) ELSE "+quoted+" END")
	}
	return "SELECT " + strings.Join(columns, ",") + " FROM main.lessons WHERE id=?"
}

func TestFirstAudioMatrixRealSQLProbeTransactionAndReceiptUnderExactPermits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real-preflight.db")
	setup, err := store.Open(path)
	if err != nil {
		t.Fatal("open isolated setup database:", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		t.Fatal("get setup pool:", err)
	}
	identity, _, err := seedFirstAudioSetupFixture(setup, firstAudioTestCapability(), "preflight_20261028", time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("seed isolated lesson fixture:", err)
	}
	if err := installFirstAudioStartupGuards(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install fixed startup guards:", err)
	}
	if err := installFirstAudioLessonProbeTriggers(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install fixed lesson probe triggers:", err)
	}
	baseline, err := firstAudioCaptureDatabaseSnapshot(setup)
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("capture preflight baseline snapshot:", err)
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("close setup pool before guarded pool opens:", err)
	}

	controller := newFirstAudioGuardController()
	registry := controller.connections
	driverContext, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "preflight_20261028", currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil {
		t.Fatal("construct initial driver-probe context:", err)
	}
	if err := controller.PublishInitial(driverContext); err != nil {
		t.Fatal("publish initial driver-probe context:", err)
	}
	var connectionHarnessMu sync.Mutex
	connectionHarnesses := make(map[string]firstAudioRealSQLConnectionHarness)
	var guarded *gorm.DB
	var tokenCalls atomic.Uint64
	var rowGuardCalls atomic.Uint64
	var callbackErr atomic.Value
	driverName := fmt.Sprintf("first_audio_real_sql_preflight_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(conn *sqlite3.SQLiteConn) error {
		registration, err := registry.Register(conn)
		if err != nil {
			return err
		}
		sessionRef := &firstAudioRealSQLProbeSessionRef{}
		authorizer := newFirstAudioRealSQLAuthorizer(controller, sessionRef, baseline)
		connectionHarnessMu.Lock()
		connectionHarnesses[registration.ID] = firstAudioRealSQLConnectionHarness{authorizer: authorizer, sessionRef: sessionRef}
		connectionHarnessMu.Unlock()
		if err := conn.RegisterFunc("first_audio_probe", func(token []byte) bool {
			tokenCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() || session.challenge == nil || session.binding.connection != registration.ID {
				return false
			}
			if err := session.challenge.ConsumeInTransaction(token, session.binding, session.txEpoch.Load()); err != nil {
				callbackErr.Store(err.Error())
				return false
			}
			return true
		}, true); err != nil {
			return err
		}
		if err := conn.RegisterFunc("first_audio_row_guard", func(args ...any) bool {
			rowGuardCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() || session.challenge == nil || session.binding.connection != registration.ID || len(args) != 72 {
				return false
			}
			expected := session.expectedProjection.Load()
			actualOld := normalizeFirstAudioTypedSQLitePairs(args[:36])
			if expected == nil || !reflect.DeepEqual(expected.values, actualOld) {
				callbackErr.Store(fmt.Sprintf("trigger OLD row differs from projection: expected=%#v actual=%#v", expected, args[:36]))
				return false
			}
			actualNew := normalizeFirstAudioTypedSQLitePairs(args[36:])
			if !reflect.DeepEqual(actualOld, actualNew) {
				callbackErr.Store("trigger OLD/NEW projection mismatch")
				return false
			}
			session.callbackArgs.Store(&firstAudioCapturedTypedCallbackArgs{values: cloneFirstAudioSQLiteCallbackArgs(args)})
			if err := session.challenge.ObserveRowGuardInTransaction(session.binding, session.txEpoch.Load()); err != nil {
				callbackErr.Store(err.Error())
				return false
			}
			return true
		}, true); err != nil {
			return err
		}
		return authorizer.install(conn)
	}, registry.Retire); err != nil {
		t.Fatal("register tracked callback SQLite driver:", err)
	}

	guarded, err = gorm.Open(gormsqlite.Dialector{DriverName: driverName, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("open isolated guarded SQLite GORM pool:", err)
	}
	pool, err := guarded.DB()
	if err != nil {
		t.Fatal("get isolated guarded SQL pool:", err)
	}
	const physicalConnections = 3
	pool.SetMaxOpenConns(physicalConnections)
	pool.SetMaxIdleConns(physicalConnections)
	pool.SetConnMaxLifetime(0)
	t.Cleanup(func() { _ = pool.Close() })
	// Force creation and registration of the full configured pool while
	// DRIVER_PROBE admission is open. Hold the connections simultaneously so
	// database/sql cannot satisfy the requests with one reused connection.
	registeredPins := make([]*sql.Conn, 0, physicalConnections)
	defer func() {
		for _, pinned := range registeredPins {
			_ = pinned.Close()
		}
	}()
	for range physicalConnections {
		pinned, pinErr := pool.Conn(context.Background())
		if pinErr != nil {
			t.Fatal("open guarded physical SQLite connection:", pinErr)
		}
		registeredPins = append(registeredPins, pinned)
	}
	if got := registry.nextID.Load(); got != physicalConnections {
		t.Fatalf("ConnectHook registrations=%d, want exactly %d before startup", got, physicalConnections)
	}
	connectionHarnessMu.Lock()
	if len(connectionHarnesses) != physicalConnections {
		connectionHarnessMu.Unlock()
		t.Fatalf("ConnectHook authorizers=%d; want one independent authorizer per physical connection (%d)", len(connectionHarnesses), physicalConnections)
	}
	for _, harness := range connectionHarnesses {
		harness.authorizer.db = guarded
	}
	connectionHarnessMu.Unlock()
	for _, pinned := range registeredPins {
		if closeErr := pinned.Close(); closeErr != nil {
			t.Fatal("release registered SQLite connection before startup:", closeErr)
		}
	}
	registeredPins = nil
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: driverContext.runID, currentLessonID: identity.LessonID, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		t.Fatal("construct startup context:", err)
	}
	startupTransition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal("close driver-probe admission:", err)
	}
	if err := startupTransition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("publish startup after physical dialector observation:", err)
	}
	// NewApp is synchronous, but any pool connection may carry its startup
	// transaction. Keep the startup permit open on every registered connection.
	connectionHarnessMu.Lock()
	for _, harness := range connectionHarnesses {
		harness.authorizer.startupPermit.Store(true)
	}
	connectionHarnessMu.Unlock()
	var app *App
	app = NewApp(scheduler.RealClock{}, guarded)
	var startupBegins, startupEnds uint32
	connectionHarnessMu.Lock()
	for _, harness := range connectionHarnesses {
		harness.authorizer.startupPermit.Store(false)
		startupBegins += harness.authorizer.startupBegins.Load()
		startupEnds += harness.authorizer.startupEnds.Load()
	}
	connectionHarnessMu.Unlock()
	if startupBegins != 1 || startupEnds != 1 {
		t.Fatalf("NewApp startup transactions begin=%d end=%d; want one exact transaction", startupBegins, startupEnds)
	}
	if app == nil || app.DB != guarded || app.StartupError != nil {
		t.Fatalf("NewApp failed on guarded pool: app=%v startup=%v", app != nil, app.StartupError)
	}
	if user, err := app.Auth.GetUser(identity.Username); err != nil || user.Role != auth.RoleStudent || user.ID != identity.StudentID {
		t.Fatalf("guarded NewApp did not restore isolated student: user=%#v err=%v", user, err)
	}
	pins := make([]*sql.Conn, 0, physicalConnections)
	defer func() {
		for _, pinned := range pins {
			_ = pinned.Close()
		}
	}()
	identities := make([]firstAudioPinnedRealSQLConnection, 0, physicalConnections)
	seenIDs := make(map[string]struct{}, physicalConnections)
	for range physicalConnections {
		pinned, pinErr := pool.Conn(context.Background())
		if pinErr != nil {
			t.Fatal("pin physical SQLite connection after NewApp startup:", pinErr)
		}
		pins = append(pins, pinned)
		var tracked *firstAudioTrackedSQLiteConn
		if rawErr := pinned.Raw(func(driverConn any) error {
			var ok bool
			tracked, ok = driverConn.(*firstAudioTrackedSQLiteConn)
			if !ok || tracked == nil || tracked.raw == nil {
				return fmt.Errorf("unexpected tracked connection %T", driverConn)
			}
			return nil
		}); rawErr != nil {
			t.Fatal("resolve tracked physical SQLite connection:", rawErr)
		}
		registration, registered := registry.Lookup(tracked.raw)
		if !registered || !registry.IsLive(registration.ID, tracked.raw) {
			t.Fatal("pinned connection is absent from ConnectHook registry")
		}
		if _, duplicate := seenIDs[registration.ID]; duplicate {
			t.Fatalf("pool capacity pin reused physical connection %s", registration.ID)
		}
		seenIDs[registration.ID] = struct{}{}
		identities = append(identities, firstAudioPinnedRealSQLConnection{conn: pinned, raw: tracked.raw, tracked: tracked, registration: registration})
	}
	if got := registry.nextID.Load(); got != physicalConnections || len(seenIDs) != physicalConnections {
		t.Fatalf("pinned pool registrations=%d unique=%d; want exactly %d", got, len(seenIDs), physicalConnections)
	}
	// Compare every pinned connection to the setup baseline while startup
	// snapshot reads are still admitted. PREFLIGHT readback below is separately
	// authorized through each connection's one-use SNAPSHOT_READ permit.
	for _, pinnedIdentity := range identities {
		connectionHarnessMu.Lock()
		harness, exists := connectionHarnesses[pinnedIdentity.registration.ID]
		connectionHarnessMu.Unlock()
		if !exists {
			t.Fatalf("connection %s has no connection-local authorizer", pinnedIdentity.registration.ID)
		}
		if harness.authorizer.raw != pinnedIdentity.raw || harness.authorizer.registryID != pinnedIdentity.registration.ID {
			t.Fatalf("connection %s authorizer is bound to a different raw connection", pinnedIdentity.registration.ID)
		}
		snapshot, snapshotErr := firstAudioCaptureDatabaseSnapshotOnPinnedConn(guarded, pinnedIdentity.conn)
		if snapshotErr != nil || snapshot.SHA256 != baseline.SHA256 || !reflect.DeepEqual(snapshot.Canonical, baseline.Canonical) {
			t.Fatalf("connection %s startup snapshot differs from setup: hash=%s err=%v", pinnedIdentity.registration.ID, snapshot.SHA256, snapshotErr)
		}
	}

	bindings := make([]firstAudioGuardProbeBinding, 0, physicalConnections)
	challenges := make([]*firstAudioGuardProbeChallenge, 0, physicalConnections)
	tokens := make([][]byte, 0, physicalConnections)
	connectionIDs := make([]string, 0, physicalConnections)
	probeContracts := make([]firstAudioGuardProbeContract, 0, physicalConnections)
	rowContracts := make([]firstAudioGuardRowContract, 0, physicalConnections)
	defer func() {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
	}()
	for _, pinnedIdentity := range identities {
		binding := firstAudioGuardProbeBinding{
			runID: "preflight_20261028", lessonID: identity.LessonID, generation: 1,
			version: 2, connection: pinnedIdentity.registration.ID, probeID: "probe-" + strings.TrimPrefix(pinnedIdentity.registration.ID, "conn-"),
		}
		token, challenge, issueErr := newFirstAudioPhysicalProbeChallenge(binding)
		if issueErr != nil {
			t.Fatal("issue connection-bound preflight challenge:", issueErr)
		}
		bindings = append(bindings, binding)
		challenges = append(challenges, challenge)
		tokens = append(tokens, token)
		connectionIDs = append(connectionIDs, pinnedIdentity.registration.ID)
		probeContracts = append(probeContracts, challenge.Contract())
		rowContracts = append(rowContracts, firstAudioGuardRowContract{
			table: "lessons", operation: "UPDATE",
			identity: fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, binding.connection),
			oldRow:   []byte("typed-projection"), newRow: []byte("typed-projection"),
		})
	}
	preflight, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: bindings[0].runID, currentLessonID: bindings[0].lessonID, generation: 1,
		phase: firstAudioGuardPreflight, version: bindings[0].version, requirePhysicalProbes: true,
		connectionIDs: connectionIDs, probeContracts: probeContracts, rowContracts: rowContracts,
	})
	if err != nil {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
		t.Fatal("construct all-connection PREFLIGHT context:", err)
	}
	receipts := make([]*firstAudioGuardProbeReceipt, 0, physicalConnections)
	for _, challenge := range challenges {
		receipts = append(receipts, challenge.receipt)
	}
	if err := startupTransition.Publish(context.Background(), preflight, firstAudioGuardTransitionEvidence{
		probeBindings: bindings, probeReceipts: receipts,
	}); err != nil {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
		t.Fatal("publish PREFLIGHT after setup snapshot equality:", err)
	}
	if !controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("controller did not publish PREFLIGHT")
	}
	for i, pinnedIdentity := range identities {
		connectionHarnessMu.Lock()
		harness, exists := connectionHarnesses[pinnedIdentity.registration.ID]
		connectionHarnessMu.Unlock()
		if !exists {
			t.Fatalf("connection %s lost its connection-local authorizer", pinnedIdentity.registration.ID)
		}
		authorizer := harness.authorizer
		authorizer.tracked, authorizer.pinned = pinnedIdentity.tracked, pinnedIdentity.conn
		session := &firstAudioRealSQLProbeSession{challenge: challenges[i], binding: bindings[i], tracked: pinnedIdentity.tracked}
		harness.sessionRef.session.Store(session)
		runFirstAudioRealSQLConnectionProbe(t, authorizer, pinnedIdentity, challenges[i], bindings[i], tokens[i], session, harness.sessionRef, &tokenCalls, &rowGuardCalls, &callbackErr)
		if !registry.IsLive(pinnedIdentity.registration.ID, pinnedIdentity.raw) {
			t.Fatalf("successful probe unexpectedly replaced physical connection %s", pinnedIdentity.registration.ID)
		}
	}
	for _, challenge := range challenges {
		if !challenge.receipt.completeFor(challenge.binding, challenge.digest) {
			t.Fatalf("connection %s lacks a completed physical readback receipt", challenge.binding.connection)
		}
	}
	lastIdentity := identities[len(identities)-1]
	connectionHarnessMu.Lock()
	lastHarness := connectionHarnesses[lastIdentity.registration.ID]
	connectionHarnessMu.Unlock()
	if err := lastHarness.authorizer.capturePinnedBaseline(); err != nil {
		t.Fatal("final all-probes snapshot differs from the isolated setup baseline:", err)
	}
	if callbackErr.Load() != nil {
		t.Fatalf("real SQL callback recorded failure: %v", callbackErr.Load())
	}
	for connectionID, harness := range connectionHarnesses {
		if harness.authorizer.denials.Load() != 0 {
			t.Fatalf("connection %s authorizer denied %d tuples: %v", connectionID, harness.authorizer.denials.Load(), harness.authorizer.lastDenied.Load())
		}
	}
	for _, pinned := range pins {
		if err := pinned.Close(); err != nil {
			t.Fatal("release pinned preflight connection:", err)
		}
	}
	pins = nil
	for _, pinnedIdentity := range identities {
		registration, live := registry.Lookup(pinnedIdentity.raw)
		if !live || registration.ID != pinnedIdentity.registration.ID || !registry.IsLive(pinnedIdentity.registration.ID, pinnedIdentity.raw) {
			t.Fatalf("registry changed after releasing pinned connection %s", pinnedIdentity.registration.ID)
		}
	}
	if got := registry.nextID.Load(); got != physicalConnections {
		t.Fatalf("ConnectHook registrations changed after releasing pins: got=%d want=%d", got, physicalConnections)
	}
}

func runFirstAudioRealSQLConnectionProbe(t *testing.T, authorizer *firstAudioRealSQLAuthorizer, pinned firstAudioPinnedRealSQLConnection, challenge *firstAudioGuardProbeChallenge, binding firstAudioGuardProbeBinding, token []byte, session *firstAudioRealSQLProbeSession, sessionRef *firstAudioRealSQLProbeSessionRef, tokenCalls, rowGuardCalls *atomic.Uint64, callbackErr *atomic.Value) {
	t.Helper()
	defer zeroFirstAudioSecret(token)
	authorizer.probeChallenge.Store(challenge)
	sessionRef.session.Store(session)
	var tx *sql.Tx
	var err error
	rolledBack, sealed := false, false
	defer func() {
		if !rolledBack && tx != nil && session.tracked.txActive.Load() {
			if cleanupErr := authorizer.rollbackProbeTransaction(); cleanupErr != nil {
				t.Errorf("connection %s rollback cleanup failed: %v", binding.connection, cleanupErr)
			}
		}
		if !sealed {
			challenge.receipt.fail()
		}
		session.txEpoch.Store(0)
	}()
	if err := authorizer.capturePinnedBaseline(); err != nil {
		t.Fatalf("connection %s capture PREFLIGHT baseline: %v", binding.connection, err)
	}
	tokenBefore := tokenCalls.Load()
	if err := authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		tx, beginErr = pinned.conn.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatalf("connection %s begin probe transaction: %v denied=%v", binding.connection, err, authorizer.lastDenied.Load())
	}
	session.txEpoch.Store(session.tracked.txEpoch.Load())
	if !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 {
		t.Fatalf("connection %s transaction epoch did not advance", binding.connection)
	}
	authorizer.txEpoch.Store(session.txEpoch.Load())
	authorizer.activeTx.Store(tx)
	if err := challenge.BindTransactionEpoch(session.txEpoch.Load()); err != nil {
		t.Fatal("bind physical transaction epoch:", err)
	}
	if !authorizer.controller.IsActivePhase(firstAudioGuardPreflight) || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 {
		t.Fatalf("connection %s cannot arm cleanup outside its active PREFLIGHT transaction", binding.connection)
	}
	authorizer.cleanupPreflight.Store(true)
	authorizer.cleanupAvailable.Store(true)
	var tokenAccepted bool
	err = authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return tx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", token).Scan(&tokenAccepted)
	})
	if err != nil || !tokenAccepted {
		t.Fatalf("connection %s token rejected: accepted=%v err=%v callback=%v denied=%v", binding.connection, tokenAccepted, err, callbackErr.Load(), authorizer.lastDenied.Load())
	}
	zeroFirstAudioSecret(token)
	if tokenCalls.Load() != tokenBefore+1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeTokenConsumed {
		t.Fatalf("connection %s token callbacks delta=%d receipt=%d", binding.connection, tokenCalls.Load()-tokenBefore, challenge.receipt.state.Load())
	}
	projection, err := authorizer.readTargetLessonProjection(tx, binding.lessonID)
	if err != nil || len(projection) != 36 {
		t.Fatalf("connection %s typed projection len=%d err=%v", binding.connection, len(projection), err)
	}
	session.expectedProjection.Store(&firstAudioExpectedTypedProjection{values: normalizeFirstAudioTypedSQLitePairs(projection)})
	guardBefore := rowGuardCalls.Load()
	var result sql.Result
	err = authorizer.withPermit(firstAudioSQLPermitProbeSQL, true, func() error {
		var updateErr error
		result, updateErr = tx.ExecContext(context.Background(), "UPDATE main.lessons SET id=id WHERE id=?", binding.lessonID)
		return updateErr
	})
	if err != nil {
		t.Fatalf("connection %s exact probe update: %v callback=%v denied=%v", binding.connection, err, callbackErr.Load(), authorizer.lastDenied.Load())
	}
	matched, err := result.RowsAffected()
	if err != nil || matched != 1 {
		t.Fatalf("connection %s no-op UPDATE rows=%d err=%v", binding.connection, matched, err)
	}
	if rowGuardCalls.Load() != guardBefore+1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeRowGuardObserved {
		t.Fatalf("connection %s row guard callbacks delta=%d receipt=%d", binding.connection, rowGuardCalls.Load()-guardBefore, challenge.receipt.state.Load())
	}
	assertFirstAudioTypedProjectionCallback(t, firstAudioRowGuardCapturedArgs(t, sessionRef))
	if err := authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("connection %s explicit rollback: %v", binding.connection, err)
	}
	rolledBack = true
	session.txEpoch.Store(0)
	if err := newFirstAudioGuardProbeExecutor(challenge).VerifyRollback(challenge.receipt, binding, nil); err != nil {
		t.Fatalf("connection %s rollback receipt: %v", binding.connection, err)
	}
	if challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatalf("connection %s receipt completed before readback", binding.connection)
	}
	if err := authorizer.confirmSnapshotReadback(challenge); err != nil {
		t.Fatalf("connection %s full snapshot readback: %v", binding.connection, err)
	}
	if !challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatalf("connection %s receipt incomplete after readback", binding.connection)
	}
	sealed = true
}

// Trigger arguments are captured by the test callback in an atomic holder so
// the assertion reads the exact values passed by SQLite, not reconstructed SQL.
func firstAudioRowGuardCapturedArgs(t *testing.T, sessionRef *firstAudioRealSQLProbeSessionRef) []any {
	t.Helper()
	session := sessionRef.session.Load()
	if session == nil || session.challenge == nil {
		t.Fatal("row-guard callback session is missing")
	}
	captured := session.callbackArgs.Load()
	if captured == nil || len(captured.values) != 72 {
		t.Fatalf("row-guard captured args=%v, want exactly 72", captured)
	}
	return cloneFirstAudioSQLiteCallbackArgs(captured.values)
}

func assertFirstAudioTypedProjectionCallback(t *testing.T, args []any) {
	t.Helper()
	if len(args) != 72 {
		t.Fatalf("typed projection callback args=%d, want 72", len(args))
	}
	for i, column := range firstAudioLessonProbeColumns {
		oldType, oldOK := args[i*2].(string)
		newType, newOK := args[36+i*2].(string)
		if !oldOK || !newOK || oldType != newType {
			t.Errorf("column %s callback type tags old=%#v new=%#v", column, args[i*2], args[36+i*2])
			continue
		}
		oldPair := []any{oldType, args[i*2+1]}
		newPair := []any{newType, args[36+i*2+1]}
		if !reflect.DeepEqual(normalizeFirstAudioTypedSQLitePairs(oldPair), normalizeFirstAudioTypedSQLitePairs(newPair)) {
			oldValue, newValue := args[i*2+1], args[36+i*2+1]
			t.Errorf("column %s callback OLD/NEW differ: old=%#v new=%#v", column, oldValue, newValue)
		}
		oldValue := args[i*2+1]
		switch oldType {
		case "null":
			blob, isBlob := oldValue.([]byte)
			if oldValue != nil && !(isBlob && blob == nil) {
				t.Errorf("column %s NULL decoded as %#v", column, oldValue)
			}
		case "integer":
			if _, ok := oldValue.(int64); !ok {
				t.Errorf("column %s INTEGER decoded as %T", column, oldValue)
			}
		case "real":
			if _, ok := oldValue.(float64); !ok {
				t.Errorf("column %s REAL decoded as %T", column, oldValue)
			}
		case "text":
			if _, ok := oldValue.(string); !ok {
				t.Errorf("column %s TEXT decoded as %T", column, oldValue)
			}
		case "blob":
			if _, ok := oldValue.([]byte); !ok {
				t.Errorf("column %s BLOB decoded as %T", column, oldValue)
			}
		default:
			t.Errorf("column %s has unexpected SQLite storage class %q", column, oldType)
		}
	}
}

func normalizeFirstAudioTypedSQLitePairs(values []any) []any {
	normalized := cloneFirstAudioSQLiteCallbackArgs(values)
	for i := 0; i+1 < len(normalized); i += 2 {
		if typeTag, ok := normalized[i].(string); ok {
			switch typeTag {
			case "null":
				normalized[i+1] = nil
			case "real":
				if value, isFloat := normalized[i+1].(float64); isFloat {
					normalized[i+1] = math.Float64bits(value)
				}
			}
		}
	}
	return normalized
}

func TestFirstAudioMatrixTypedRealProjectionPreservesSignedZero(t *testing.T) {
	positive := normalizeFirstAudioTypedSQLitePairs([]any{"real", math.Float64frombits(0)})
	negative := normalizeFirstAudioTypedSQLitePairs([]any{"real", math.Float64frombits(uint64(1) << 63)})
	if reflect.DeepEqual(positive, negative) {
		t.Fatal("typed REAL comparison collapsed +0 and -0; expected IEEE-754 bitwise distinction")
	}
	if got := positive[1]; got != uint64(0) {
		t.Fatalf("+0 REAL normalized to %#v (%T), want uint64 zero bits", got, got)
	}
	if got := negative[1]; got != uint64(1)<<63 {
		t.Fatalf("-0 REAL normalized to %#v (%T), want sign-bit-only encoding", got, got)
	}
}

func TestFirstAudioMatrixProbeChallengeRejectsDifferentTransactionEpoch(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "transaction_epoch", lessonID: 1, generation: 1, version: 1, connection: "conn-1", probeID: "probe-1"}
	token, challenge, err := newFirstAudioGuardProbeChallenge(binding)
	if err != nil {
		t.Fatal("create transaction-bound challenge:", err)
	}
	defer zeroFirstAudioSecret(token)
	if err := challenge.BindTransactionEpoch(17); err != nil {
		t.Fatal("bind challenge epoch:", err)
	}
	if err := challenge.ConsumeInTransaction(token, binding, 18); err == nil {
		t.Fatal("challenge was consumed from a different transaction epoch")
	}
	if state := firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()); state != firstAudioGuardProbeFailed {
		t.Fatalf("mismatched transaction epoch receipt state=%v, want FAILED", state)
	}
}

//go:build cgo

package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
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
	tracked                 *firstAudioTrackedSQLiteConn
	pinned                  *sql.Conn
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
		if firstAudioAuthorizeCleanupTuple(firstAudioSQLPermit(p.permit.Load()), action, arg1, arg2, databaseName, triggerName, !conn.AutoCommit()) {
			allowed = true
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
			allowed = firstAudioAuthorizePreflightTuple(firstAudioSQLPermit(p.permit.Load()), p.reads, action, arg1, arg2, databaseName, triggerName)
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
	if p == nil || p.pinned == nil || p.tracked == nil || !p.connections.IsLive(p.registryID, p.raw) || !p.tracked.txActive.Load() || p.txEpoch.Load() == 0 || p.txEpoch.Load() != p.tracked.txEpoch.Load() || !p.cleanupAvailable.CompareAndSwap(true, false) {
		return errFirstAudioGuardProbeRejected
	}
	if !p.permit.CompareAndSwap(uint32(firstAudioSQLPermitNone), uint32(firstAudioSQLPermitTransactionCleanup)) {
		p.cleanupAvailable.Store(true)
		return errFirstAudioGuardProbeRejected
	}
	defer p.permit.Store(uint32(firstAudioSQLPermitNone))
	tx := p.activeTx.Load()
	if tx == nil {
		p.discardPinnedConnection()
		return errFirstAudioGuardProbeRejected
	}
	err := tx.Rollback()
	if err != nil || p.tracked.txActive.Load() || !p.raw.AutoCommit() {
		p.discardPinnedConnection()
		return fmt.Errorf("probe rollback unconfirmed: %w", errFirstAudioGuardProbeRejected)
	}
	p.activeTx.CompareAndSwap(tx, nil)
	return nil
}

func (p *firstAudioRealSQLAuthorizer) discardPinnedConnection() {
	if p != nil && p.pinned != nil {
		_ = p.pinned.Raw(func(any) error { return driver.ErrBadConn })
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
	sessionRef := &firstAudioRealSQLProbeSessionRef{}
	authorizer := newFirstAudioRealSQLAuthorizer(controller, sessionRef, baseline)
	var tokenCalls atomic.Uint64
	var rowGuardCalls atomic.Uint64
	var callbackErr atomic.Value
	driverName := fmt.Sprintf("first_audio_real_sql_preflight_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(conn *sqlite3.SQLiteConn) error {
		registration, err := registry.Register(conn)
		if err != nil {
			return err
		}
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

	guarded, err := gorm.Open(gormsqlite.Dialector{DriverName: driverName, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("open isolated guarded SQLite GORM pool:", err)
	}
	pool, err := guarded.DB()
	if err != nil {
		t.Fatal("get isolated guarded SQL pool:", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(0)
	authorizer.db = guarded
	t.Cleanup(func() { _ = pool.Close() })
	// Force ConnectHook registration while DRIVER_PROBE admission is open. Keep
	// the sole pool connection available to NewApp's startup transactions below.
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatal("open guarded physical SQLite connection:", err)
	}
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
	var app *App
	if err := authorizer.withStartupPermit(func() error {
		app = NewApp(scheduler.RealClock{}, guarded)
		return nil
	}); err != nil {
		t.Fatalf("run NewApp inside controller-owned startup transaction permit: %v", err)
	}
	if app == nil || app.DB != guarded || app.StartupError != nil {
		t.Fatalf("NewApp failed on controller-guarded pool: app=%v startup=%v", app != nil, app.StartupError)
	}
	if authorizer.startupBegins.Load() != 1 || authorizer.startupEnds.Load() != 1 {
		t.Fatalf("NewApp startup transactions begin=%d end=%d; want one exact transaction", authorizer.startupBegins.Load(), authorizer.startupEnds.Load())
	}
	if user, err := app.Auth.GetUser(identity.Username); err != nil || user.Role != auth.RoleStudent || user.ID != identity.StudentID {
		t.Fatalf("guarded NewApp did not restore isolated student: user=%#v err=%v", user, err)
	}
	pinned, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin physical SQLite connection after NewApp startup:", err)
	}
	defer pinned.Close()
	var raw *sqlite3.SQLiteConn
	if err := pinned.Raw(func(driverConn any) error {
		tracked, ok := driverConn.(*firstAudioTrackedSQLiteConn)
		if !ok || tracked == nil || tracked.raw == nil {
			return fmt.Errorf("unexpected tracked connection %T", driverConn)
		}
		raw = tracked.raw
		return nil
	}); err != nil {
		t.Fatal("resolve pinned physical connection identity:", err)
	}
	registration, live := registry.Lookup(raw)
	if !live || !registry.IsLive(registration.ID, raw) {
		t.Fatal("pinned connection is absent from ConnectHook registry")
	}
	var tracked *firstAudioTrackedSQLiteConn
	if err := pinned.Raw(func(driverConn any) error {
		var ok bool
		tracked, ok = driverConn.(*firstAudioTrackedSQLiteConn)
		if !ok || tracked == nil {
			return fmt.Errorf("unexpected tracked connection %T", driverConn)
		}
		return nil
	}); err != nil {
		t.Fatal("resolve tracked SQLite transaction state:", err)
	}
	authorizer.tracked = tracked
	authorizer.pinned = pinned
	binding := firstAudioGuardProbeBinding{
		runID: "preflight_20261028", lessonID: identity.LessonID, generation: 1,
		version: 2, connection: registration.ID, probeID: "probe-" + strings.TrimPrefix(registration.ID, "conn-"),
	}
	token, challenge, err := newFirstAudioPhysicalProbeChallenge(binding)
	if err != nil {
		t.Fatal("issue per-connection preflight challenge:", err)
	}
	session := &firstAudioRealSQLProbeSession{challenge: challenge, binding: binding, tracked: tracked}
	sessionRef.session.Store(session)
	var tx *sql.Tx
	rolledBack, sealed := false, false
	defer func() {
		if !rolledBack && tx != nil && tracked.txActive.Load() {
			if cleanupErr := authorizer.rollbackProbeTransaction(); cleanupErr != nil {
				t.Errorf("failure cleanup could not confirm rollback: %v; discard pinned SQLite connection", cleanupErr)
			}
		}
		if !sealed {
			challenge.receipt.fail()
		}
		session.txEpoch.Store(0)
		zeroFirstAudioSecret(token)
	}()

	beforePinned, err := firstAudioCaptureDatabaseSnapshotOnPinnedConn(guarded, pinned)
	if err != nil {
		zeroFirstAudioSecret(token)
		t.Fatal("capture guarded-pool snapshot before probe:", err)
	}
	if beforePinned.SHA256 != baseline.SHA256 {
		zeroFirstAudioSecret(token)
		t.Fatalf("guarded pool differs from closed setup baseline before probe: setup=%s guarded=%s", baseline.SHA256, beforePinned.SHA256)
	}
	preflight, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: binding.runID, currentLessonID: binding.lessonID, generation: binding.generation,
		phase: firstAudioGuardPreflight, version: binding.version,
		requirePhysicalProbes: true,
		connectionIDs:         []string{binding.connection}, probeContracts: []firstAudioGuardProbeContract{challenge.Contract()},
		rowContracts: []firstAudioGuardRowContract{{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, binding.connection), oldRow: []byte("typed-projection"), newRow: []byte("typed-projection")}},
	})
	if err != nil {
		zeroFirstAudioSecret(token)
		t.Fatal("construct per-connection PREFLIGHT context:", err)
	}
	if err := startupTransition.Publish(context.Background(), preflight, firstAudioGuardTransitionEvidence{
		probeBindings: []firstAudioGuardProbeBinding{binding}, probeReceipts: []*firstAudioGuardProbeReceipt{challenge.receipt},
	}); err != nil {
		zeroFirstAudioSecret(token)
		t.Fatal("publish PREFLIGHT after setup snapshot equality:", err)
	}
	if !challenge.receipt.armed.Load() || !controller.IsActivePhase(firstAudioGuardPreflight) {
		zeroFirstAudioSecret(token)
		t.Fatal("controller did not arm the receipt in PREFLIGHT")
	}
	if err := authorizer.capturePinnedBaseline(); err != nil {
		zeroFirstAudioSecret(token)
		t.Fatal("capture PREFLIGHT baseline on pinned connection:", err)
	}

	err = authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		tx, beginErr = pinned.BeginTx(context.Background(), nil)
		return beginErr
	})
	if err != nil {
		zeroFirstAudioSecret(token)
		t.Fatalf("begin pinned preflight transaction under PROBE_SQL: %v last_denial=%v", err, authorizer.lastDenied.Load())
	}
	session = sessionRef.session.Load()
	session.txEpoch.Store(tracked.txEpoch.Load())
	if !tracked.txActive.Load() || session.txEpoch.Load() == 0 {
		t.Fatal("tracked SQLite transaction state did not advance after BeginTx")
	}
	authorizer.txEpoch.Store(session.txEpoch.Load())
	authorizer.activeTx.Store(tx)
	if err := challenge.BindTransactionEpoch(session.txEpoch.Load()); err != nil {
		t.Fatal("bind challenge to the active tracked transaction epoch:", err)
	}
	authorizer.cleanupAvailable.Store(true)
	var tokenAccepted bool
	err = authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return tx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", token).Scan(&tokenAccepted)
	})
	if err != nil || !tokenAccepted {
		zeroFirstAudioSecret(token)
		controller.Terminalize()
		t.Fatalf("consume same-connection token: accepted=%v err=%v callback=%v authorizer=%v", tokenAccepted, err, callbackErr.Load(), authorizer.lastDenied.Load())
	}
	zeroFirstAudioSecret(token)
	if tokenCalls.Load() != 1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeTokenConsumed {
		t.Fatalf("token receipt state=%d callbacks=%d, want one consumed callback", challenge.receipt.state.Load(), tokenCalls.Load())
	}

	projectionValues, err := authorizer.readTargetLessonProjection(tx, identity.LessonID)
	if err != nil {
		controller.Terminalize()
		t.Fatalf("read fixed typed lesson projection: %v last_denial=%v", err, authorizer.lastDenied.Load())
	}
	if len(projectionValues) != 36 {
		t.Fatalf("typed lesson projection values=%d, want 36", len(projectionValues))
	}
	session.expectedProjection.Store(&firstAudioExpectedTypedProjection{values: normalizeFirstAudioTypedSQLitePairs(projectionValues)})

	var result sql.Result
	err = authorizer.withPermit(firstAudioSQLPermitProbeSQL, true, func() error {
		var updateErr error
		result, updateErr = tx.ExecContext(context.Background(), "UPDATE main.lessons SET id=id WHERE id=?", identity.LessonID)
		return updateErr
	})
	if err != nil {
		challenge.receipt.fail()
		controller.Terminalize()
		t.Fatalf("execute fixed lesson no-op update: %v callback=%v authorizer=%v", err, callbackErr.Load(), authorizer.lastDenied.Load())
	}
	matched, err := result.RowsAffected()
	if err != nil || matched != 1 {
		challenge.receipt.fail()
		t.Fatalf("fixed no-op update matched=%d err=%v want=1", matched, err)
	}
	if rowGuardCalls.Load() != 1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeRowGuardObserved {
		challenge.receipt.fail()
		t.Fatalf("row guard calls=%d receipt state=%d callback=%v", rowGuardCalls.Load(), challenge.receipt.state.Load(), callbackErr.Load())
	}
	assertFirstAudioTypedProjectionCallback(t, firstAudioRowGuardCapturedArgs(t, sessionRef))

	if err := authorizer.rollbackProbeTransaction(); err != nil {
		challenge.receipt.fail()
		controller.Terminalize()
		t.Fatalf("explicit preflight rollback: %v authorizer=%v", err, authorizer.lastDenied.Load())
	}
	rolledBack = true
	session.txEpoch.Store(0)
	if err := newFirstAudioGuardProbeExecutor(challenge).VerifyRollback(challenge.receipt, binding, nil); err != nil {
		t.Fatal("seal receipt after confirmed rollback:", err)
	}
	if challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatal("rollback receipt became consumable before snapshot readback")
	}
	if err := authorizer.confirmSnapshotReadback(challenge); err != nil {
		t.Fatal("confirm full snapshot readback on pinned connection:", err)
	}
	if !challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatal("real SQL probe receipt was not complete after rollback and full snapshot readback")
	}
	if !registry.IsLive(registration.ID, raw) {
		t.Fatal("successful preflight unexpectedly replaced its pinned physical connection")
	}
	if callbackErr.Load() != nil {
		t.Fatalf("real SQL callback recorded failure: %v", callbackErr.Load())
	}
	if authorizer.denials.Load() != 0 {
		t.Fatalf("real SQL authorizer denied %d tuples: %v", authorizer.denials.Load(), authorizer.lastDenied.Load())
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

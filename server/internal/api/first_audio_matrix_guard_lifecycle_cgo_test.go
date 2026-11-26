//go:build cgo

package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

var (
	errFirstAudioGuardAdmissionClosed = errors.New("first-audio guard admission closed")
	errFirstAudioGuardLeaseReleased   = errors.New("first-audio guard lease already released")
	errFirstAudioGuardDrainTimeout    = errors.New("first-audio guard lease drain timed out")
	errFirstAudioGuardProbeRejected   = errors.New("first-audio guard probe rejected")
)

type firstAudioGuardProbeBinding struct {
	runID      string
	lessonID   int64
	generation uint64
	version    uint64
	connection string
	probeID    string
}

// A driver probe receipt can only be completed by observing both callbacks of
// the exact synchronous dialector query on one ConnectHook-assigned identity.
// It is consumed by the controller at the one-way startup publication.
type firstAudioDriverProbeReceipt struct {
	runID            string
	lessonID         int64
	generation       uint64
	driverVersion    uint64
	connectionID     string
	rawConnection    *sqlite3.SQLiteConn
	registry         *firstAudioSQLiteConnectionRegistry
	selectObserved   atomic.Bool
	functionObserved atomic.Bool
	failed           atomic.Bool
	consumed         atomic.Bool
}

func newFirstAudioDriverProbeReceipt(ctx *firstAudioGuardContext, registry *firstAudioSQLiteConnectionRegistry, rawConnection *sqlite3.SQLiteConn) (*firstAudioDriverProbeReceipt, error) {
	registration, registered := registry.Lookup(rawConnection)
	if ctx == nil || ctx.phase != firstAudioGuardDriverProbe || !registered || registration.ID == "" || !firstAudioIDPattern.MatchString(registration.ID) {
		return nil, errFirstAudioGuardContextInvalid
	}
	return &firstAudioDriverProbeReceipt{runID: ctx.runID, lessonID: ctx.currentLessonID, generation: ctx.generation, driverVersion: ctx.version, connectionID: registration.ID, rawConnection: rawConnection, registry: registry}, nil
}

func (r *firstAudioDriverProbeReceipt) Observe(rawConnection *sqlite3.SQLiteConn, action int, arg1, arg2, databaseName, triggerName string) error {
	if r == nil {
		return errFirstAudioGuardProbeRejected
	}
	reject := func() error {
		r.failed.Store(true)
		return errFirstAudioGuardProbeRejected
	}
	if rawConnection != r.rawConnection || r.registry == nil || !r.registry.IsLive(r.connectionID, rawConnection) || r.consumed.Load() || r.failed.Load() || databaseName != "" || triggerName != "" {
		return reject()
	}
	switch action {
	case sqlite3.SQLITE_SELECT:
		if arg1 != "" || arg2 != "" {
			return reject()
		}
		if !r.selectObserved.CompareAndSwap(false, true) {
			return reject()
		}
	case sqlite3.SQLITE_FUNCTION:
		if arg1 != "" || arg2 != "sqlite_version" || !r.selectObserved.Load() || !r.functionObserved.CompareAndSwap(false, true) {
			return reject()
		}
	default:
		return reject()
	}
	return nil
}

func (r *firstAudioDriverProbeReceipt) matchesAndConsume(driver, startup *firstAudioGuardContext) bool {
	return r != nil && driver != nil && startup != nil && driver.phase == firstAudioGuardDriverProbe && startup.phase == firstAudioGuardStartupProbe && r.runID == driver.runID && r.lessonID == driver.currentLessonID && r.generation == driver.generation && r.driverVersion == driver.version && startup.runID == driver.runID && startup.currentLessonID == driver.currentLessonID && startup.generation == driver.generation && startup.version == driver.version+1 && r.connectionID != "" && r.registry != nil && r.rawConnection != nil && !r.failed.Load() && r.selectObserved.Load() && r.functionObserved.Load()
}

func (b firstAudioGuardProbeBinding) matchesContract(p firstAudioGuardProbeContract) bool {
	return b.runID == p.runID && b.lessonID == p.lessonID && b.generation == p.generation && b.version == p.version && b.connection == p.connectionID && b.probeID == p.probeID
}

type firstAudioGuardProbeReceiptState uint32

const (
	firstAudioGuardProbeIssued firstAudioGuardProbeReceiptState = iota
	firstAudioGuardProbeTokenConsumed
	firstAudioGuardProbeRowGuardObserved
	firstAudioGuardProbeRollbackVerified
	firstAudioGuardProbeReadbackVerified
	firstAudioGuardProbeFailed
)

type firstAudioGuardProbeReceipt struct {
	binding                  firstAudioGuardProbeBinding
	digest                   [sha256.Size]byte
	state                    atomic.Uint32
	txEpoch                  atomic.Uint64
	readbackRaw              atomic.Pointer[sqlite3.SQLiteConn]
	physicalReadback         atomic.Bool
	requiresPhysicalReadback bool
	denials                  atomic.Uint64
	armed                    atomic.Bool
	active                   atomic.Uint64
	activityDone             chan struct{}
}

const firstAudioGuardProbeClosedBit uint64 = 1 << 63
const firstAudioGuardProbeActiveMask uint64 = firstAudioGuardProbeClosedBit - 1

type firstAudioGuardProbeChallenge struct {
	binding firstAudioGuardProbeBinding
	digest  [sha256.Size]byte
	receipt *firstAudioGuardProbeReceipt
	txEpoch atomic.Uint64
}

func newFirstAudioGuardProbeChallenge(binding firstAudioGuardProbeBinding) ([]byte, *firstAudioGuardProbeChallenge, error) {
	return newFirstAudioGuardProbeChallengeFrom(rand.Reader, binding)
}

func newFirstAudioPhysicalProbeChallenge(binding firstAudioGuardProbeBinding) ([]byte, *firstAudioGuardProbeChallenge, error) {
	token, challenge, err := newFirstAudioGuardProbeChallenge(binding)
	if err != nil {
		return nil, nil, err
	}
	challenge.receipt.requiresPhysicalReadback = true
	return token, challenge, nil
}

func newFirstAudioGuardProbeChallengeFrom(source io.Reader, binding firstAudioGuardProbeBinding) ([]byte, *firstAudioGuardProbeChallenge, error) {
	if source == nil || !firstAudioRunIDPattern.MatchString(binding.runID) || binding.lessonID <= 0 || binding.generation == 0 || binding.version == 0 || !firstAudioIDPattern.MatchString(binding.connection) || !firstAudioIDPattern.MatchString(binding.probeID) {
		return nil, nil, errFirstAudioGuardContextInvalid
	}
	token := make([]byte, 32)
	if _, err := io.ReadFull(source, token); err != nil {
		zeroFirstAudioSecret(token)
		return nil, nil, fmt.Errorf("%w: challenge entropy unavailable", errFirstAudioGuardProbeRejected)
	}
	digest := sha256.Sum256(token)
	receipt := &firstAudioGuardProbeReceipt{binding: binding, digest: digest, activityDone: make(chan struct{}, 1)}
	return token, &firstAudioGuardProbeChallenge{binding: binding, digest: digest, receipt: receipt}, nil
}

func zeroFirstAudioSecret(secret []byte) {
	for i := range secret {
		secret[i] = 0
	}
}

func (c *firstAudioGuardProbeChallenge) Contract() firstAudioGuardProbeContract {
	if c == nil {
		return firstAudioGuardProbeContract{}
	}
	return firstAudioGuardProbeContract{
		connectionID: c.binding.connection, probeID: c.binding.probeID, tokenDigest: c.digest,
		runID: c.binding.runID, lessonID: c.binding.lessonID, generation: c.binding.generation, version: c.binding.version,
	}
}

func (c *firstAudioGuardProbeChallenge) Consume(token []byte, supplied firstAudioGuardProbeBinding) error {
	if c == nil || c.receipt == nil || !sameFirstAudioProbeBinding(c.binding, supplied) {
		if c != nil && c.receipt != nil {
			c.receipt.denyAttempt()
		}
		return errFirstAudioGuardProbeRejected
	}
	if !c.receipt.beginProbeActivity() {
		c.receipt.denyAttempt()
		return errFirstAudioGuardProbeRejected
	}
	defer c.receipt.endProbeActivity()
	return c.consumeAdmitted(token)
}

// BindTransactionEpoch ties a real-SQL probe challenge to the tracked driver's
// current transaction epoch. The logical lifecycle tests may continue using
// Consume directly; the real SQLite callbacks must use the transaction-bound
// methods below.
func (c *firstAudioGuardProbeChallenge) BindTransactionEpoch(epoch uint64) error {
	if c == nil || c.receipt == nil || epoch == 0 || firstAudioGuardProbeReceiptState(c.receipt.state.Load()) != firstAudioGuardProbeIssued || !c.txEpoch.CompareAndSwap(0, epoch) {
		return errFirstAudioGuardProbeRejected
	}
	c.receipt.txEpoch.Store(epoch)
	return nil
}

func (c *firstAudioGuardProbeChallenge) ConsumeInTransaction(token []byte, supplied firstAudioGuardProbeBinding, epoch uint64) error {
	if c == nil || epoch == 0 || c.txEpoch.Load() != epoch {
		if c != nil && c.receipt != nil {
			c.receipt.denyAttempt()
		}
		return errFirstAudioGuardProbeRejected
	}
	return c.Consume(token, supplied)
}

// InvalidateIncompleteTransaction models the protocol rule that ending a
// transaction cannot leave an unused token or an unconsummated row permit
// reusable. A row-guard-observed challenge remains eligible for the normal
// rollback/readback receipt path; incomplete states fail closed.
func (c *firstAudioGuardProbeChallenge) InvalidateIncompleteTransaction(epoch uint64) {
	if c == nil || c.receipt == nil {
		return
	}
	state := firstAudioGuardProbeReceiptState(c.receipt.state.Load())
	if state == firstAudioGuardProbeFailed || state == firstAudioGuardProbeRowGuardObserved || state == firstAudioGuardProbeRollbackVerified || state == firstAudioGuardProbeReadbackVerified {
		return
	}
	if epoch == 0 || c.txEpoch.Load() != epoch {
		c.receipt.denyAttempt()
		return
	}
	switch state {
	case firstAudioGuardProbeIssued, firstAudioGuardProbeTokenConsumed:
		c.receipt.deny()
	}
}

// consumeAdmitted models a callback that crossed its gate before a drain and
// finished its in-flight work after admission closed.
func (c *firstAudioGuardProbeChallenge) consumeAdmitted(token []byte) error {
	if c == nil || c.receipt == nil {
		return errFirstAudioGuardProbeRejected
	}
	digest := sha256.Sum256(token)
	if subtle.ConstantTimeCompare(digest[:], c.digest[:]) != 1 || !c.receipt.state.CompareAndSwap(uint32(firstAudioGuardProbeIssued), uint32(firstAudioGuardProbeTokenConsumed)) {
		c.receipt.deny()
		return errFirstAudioGuardProbeRejected
	}
	return nil
}

func (c *firstAudioGuardProbeChallenge) ObserveRowGuard(supplied firstAudioGuardProbeBinding) error {
	if c == nil || c.receipt == nil || !sameFirstAudioProbeBinding(c.binding, supplied) || !c.receipt.beginProbeActivity() {
		if c != nil && c.receipt != nil {
			c.receipt.denyAttempt()
		}
		return errFirstAudioGuardProbeRejected
	}
	defer c.receipt.endProbeActivity()
	if !c.receipt.state.CompareAndSwap(uint32(firstAudioGuardProbeTokenConsumed), uint32(firstAudioGuardProbeRowGuardObserved)) {
		if c != nil && c.receipt != nil {
			c.receipt.deny()
		}
		return errFirstAudioGuardProbeRejected
	}
	return nil
}

func (c *firstAudioGuardProbeChallenge) ObserveRowGuardInTransaction(supplied firstAudioGuardProbeBinding, epoch uint64) error {
	if c == nil || epoch == 0 || c.txEpoch.Load() != epoch {
		if c != nil && c.receipt != nil {
			c.receipt.denyAttempt()
		}
		return errFirstAudioGuardProbeRejected
	}
	return c.ObserveRowGuard(supplied)
}

type firstAudioGuardProbeExecutor struct {
	binding firstAudioGuardProbeBinding
	receipt *firstAudioGuardProbeReceipt
}

func newFirstAudioGuardProbeExecutor(challenge *firstAudioGuardProbeChallenge) *firstAudioGuardProbeExecutor {
	if challenge == nil {
		return nil
	}
	return &firstAudioGuardProbeExecutor{binding: challenge.binding, receipt: challenge.receipt}
}

func (e *firstAudioGuardProbeExecutor) VerifyRollback(receipt *firstAudioGuardProbeReceipt, supplied firstAudioGuardProbeBinding, rollbackErr error) error {
	if e == nil || e.receipt == nil || receipt != e.receipt || !sameFirstAudioProbeBinding(e.binding, supplied) || !receipt.beginProbeActivity() {
		if e != nil && e.receipt != nil {
			e.receipt.denyAttempt()
		}
		return errFirstAudioGuardProbeRejected
	}
	defer receipt.endProbeActivity()
	if rollbackErr != nil || !receipt.state.CompareAndSwap(uint32(firstAudioGuardProbeRowGuardObserved), uint32(firstAudioGuardProbeRollbackVerified)) {
		if e != nil && e.receipt != nil {
			e.receipt.deny()
		}
		return errFirstAudioGuardProbeRejected
	}
	return nil
}

func firstAudioConfirmLogicalReadbackForTest(challenge *firstAudioGuardProbeChallenge, snapshotsEqual bool) error {
	if challenge == nil || challenge.receipt == nil || challenge.receipt.requiresPhysicalReadback || challenge.txEpoch.Load() != 0 || challenge.receipt.txEpoch.Load() != 0 || !snapshotsEqual {
		if challenge != nil && challenge.receipt != nil {
			challenge.receipt.denyAttempt()
		}
		return errFirstAudioGuardProbeRejected
	}
	if !challenge.receipt.beginProbeActivity() {
		return errFirstAudioGuardProbeRejected
	}
	defer challenge.receipt.endProbeActivity()
	if challenge.receipt.denials.Load() != 0 || !challenge.receipt.state.CompareAndSwap(uint32(firstAudioGuardProbeRollbackVerified), uint32(firstAudioGuardProbeReadbackVerified)) {
		challenge.receipt.deny()
		return errFirstAudioGuardProbeRejected
	}
	return nil
}

func (r *firstAudioGuardProbeReceipt) fail() {
	if r == nil {
		return
	}
	for {
		state := r.state.Load()
		if state == uint32(firstAudioGuardProbeFailed) {
			return
		}
		if r.state.CompareAndSwap(state, uint32(firstAudioGuardProbeFailed)) {
			return
		}
	}
}

func (r *firstAudioGuardProbeReceipt) completeFor(binding firstAudioGuardProbeBinding, digest [sha256.Size]byte) bool {
	return r != nil && sameFirstAudioProbeBinding(r.binding, binding) && subtle.ConstantTimeCompare(r.digest[:], digest[:]) == 1 && r.denials.Load() == 0 && firstAudioGuardProbeReceiptState(r.state.Load()) == firstAudioGuardProbeReadbackVerified
}

func (r *firstAudioGuardProbeReceipt) deny() {
	if r == nil {
		return
	}
	r.denials.Add(1)
	r.fail()
}

func (r *firstAudioGuardProbeReceipt) denyAttempt() {
	if r == nil {
		return
	}
	for {
		state := r.active.Load()
		if state&firstAudioGuardProbeClosedBit != 0 || state&firstAudioGuardProbeActiveMask == firstAudioGuardProbeActiveMask {
			return
		}
		if r.active.CompareAndSwap(state, state+1) {
			r.deny()
			r.endProbeActivity()
			return
		}
	}
}

func (r *firstAudioGuardProbeReceipt) arm() bool {
	return r != nil && r.armed.CompareAndSwap(false, true)
}

func (r *firstAudioGuardProbeReceipt) beginProbeActivity() bool {
	if r == nil {
		return false
	}
	for {
		state := r.active.Load()
		if !r.armed.Load() || state&firstAudioGuardProbeClosedBit != 0 || state&firstAudioGuardProbeActiveMask == firstAudioGuardProbeActiveMask {
			return false
		}
		if r.active.CompareAndSwap(state, state+1) {
			return true
		}
	}
}

func (r *firstAudioGuardProbeReceipt) endProbeActivity() {
	if r == nil {
		return
	}
	for {
		state := r.active.Load()
		if state&firstAudioGuardProbeActiveMask == 0 {
			r.deny()
			return
		}
		if r.active.CompareAndSwap(state, state-1) {
			break
		}
	}
	select {
	case r.activityDone <- struct{}{}:
	default:
	}
}

func (r *firstAudioGuardProbeReceipt) closeProbeAdmission() error {
	if r == nil {
		return errFirstAudioGuardProbeRejected
	}
	for {
		state := r.active.Load()
		if state&firstAudioGuardProbeClosedBit != 0 || r.active.CompareAndSwap(state, state|firstAudioGuardProbeClosedBit) {
			break
		}
	}
	return nil
}

func (r *firstAudioGuardProbeReceipt) drainProbeActivity(ctx context.Context) error {
	if r == nil || !r.probeAdmissionClosed() {
		return errFirstAudioGuardProbeRejected
	}
	for r.active.Load()&firstAudioGuardProbeActiveMask != 0 {
		select {
		case <-r.activityDone:
		case <-ctx.Done():
			return fmt.Errorf("%w: probe callback drain timed out", errFirstAudioGuardDrainTimeout)
		case <-time.After(10 * time.Millisecond):
		}
	}
	return nil
}

func (r *firstAudioGuardProbeReceipt) probeGateDrained() bool {
	if r == nil {
		return false
	}
	state := r.active.Load()
	return state&firstAudioGuardProbeClosedBit != 0 && state&firstAudioGuardProbeActiveMask == 0
}

func (r *firstAudioGuardProbeReceipt) probeAdmissionClosed() bool {
	return r != nil && r.active.Load()&firstAudioGuardProbeClosedBit != 0
}

func closeAndDrainFirstAudioProbeReceipts(ctx context.Context, receipts []*firstAudioGuardProbeReceipt) error {
	var firstErr error
	for _, receipt := range receipts {
		if err := receipt.closeProbeAdmission(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, receipt := range receipts {
		if err := receipt.drainProbeActivity(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func sameFirstAudioProbeBinding(a, b firstAudioGuardProbeBinding) bool {
	return a == b
}

type firstAudioGuardController struct {
	mu                           sync.Mutex
	requirePreflightPrepare      bool
	requiredPreflightConnections int
	publisher                    firstAudioGuardContextPublisher
	startupVerificationReceipt   *firstAudioStartupVerificationReceipt
	startupBaselineHash          string
	driverProbe                  *firstAudioDriverProbeReceipt
	beforeReceiptArm             func() // deterministic test seam; called while exact-live registry lock is held
	connections                  *firstAudioSQLiteConnectionRegistry
	probeReceipts                map[string]*firstAudioGuardProbeReceipt
	admissionOpen                bool
	terminal                     bool
	callbackTerminal             atomic.Bool
	exclusive                    bool
	draining                     bool
	activeTransition             *firstAudioGuardTransitionLease
	gateVersion                  uint64
	leases                       map[uint64]int
	changed                      chan struct{}
}

func newFirstAudioGuardController() *firstAudioGuardController {
	return newFirstAudioGuardControllerWithPolicy(false, 0)
}

func newFirstAudioGuardControllerWithPolicy(requirePreflightPrepare bool, requiredPreflightConnections int) *firstAudioGuardController {
	if (requirePreflightPrepare && requiredPreflightConnections < 1) || (!requirePreflightPrepare && requiredPreflightConnections != 0) {
		panic("invalid first-audio controller policy")
	}
	controller := &firstAudioGuardController{requirePreflightPrepare: requirePreflightPrepare, requiredPreflightConnections: requiredPreflightConnections, connections: newFirstAudioSQLiteConnectionRegistry(), leases: make(map[uint64]int), probeReceipts: make(map[string]*firstAudioGuardProbeReceipt), changed: make(chan struct{})}
	controller.publisher.requirePreflightPrepare = requirePreflightPrepare
	if requirePreflightPrepare {
		controller.publisher.transitionCapability = &struct{}{}
	}
	return controller
}

// Test fixtures that begin at STARTUP_PROBE must still traverse and prove the
// mandatory DRIVER_PROBE phase. Production integration obtains these exact
// callbacks from ConnectHook; this helper supplies deterministic synthetic
// callbacks only to lifecycle unit tests.
func firstAudioTestPublishInitialStartup(controller *firstAudioGuardController, startup *firstAudioGuardContext) (*firstAudioGuardTransitionLease, error) {
	if controller == nil || startup == nil || startup.phase != firstAudioGuardStartupProbe || startup.version != 1 {
		return nil, errFirstAudioGuardContextTransition
	}
	driver, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: startup.runID, currentLessonID: startup.currentLessonID,
		generation: startup.generation, phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil {
		return nil, err
	}
	if err := controller.PublishInitial(driver); err != nil {
		return nil, err
	}
	driverConn, err := (&sqlite3.SQLiteDriver{}).Open(":memory:")
	if err != nil {
		return nil, err
	}
	rawConn, ok := driverConn.(*sqlite3.SQLiteConn)
	if !ok {
		_ = driverConn.Close()
		return nil, fmt.Errorf("unexpected synthetic driver connection %T", driverConn)
	}
	if _, err := controller.connections.Register(rawConn); err != nil {
		_ = driverConn.Close()
		return nil, err
	}
	defer func() {
		controller.connections.Retire(rawConn)
		_ = driverConn.Close()
	}()
	if err := controller.ObserveDriverProbe(rawConn, sqlite3.SQLITE_SELECT, "", "", "", ""); err != nil {
		return nil, err
	}
	if err := controller.ObserveDriverProbe(rawConn, sqlite3.SQLITE_FUNCTION, "", "sqlite_version", "", ""); err != nil {
		return nil, err
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		return nil, err
	}
	if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); err != nil {
		return nil, err
	}
	return transition, nil
}

func firstAudioTestOpenSQLiteConnection(t *testing.T) *sqlite3.SQLiteConn {
	t.Helper()
	driverConn, err := (&sqlite3.SQLiteDriver{}).Open(":memory:")
	if err != nil {
		t.Fatal("open synthetic test SQLite connection:", err)
	}
	rawConn, ok := driverConn.(*sqlite3.SQLiteConn)
	if !ok {
		_ = driverConn.Close()
		t.Fatalf("synthetic SQLite driver connection type=%T", driverConn)
	}
	t.Cleanup(func() { _ = driverConn.Close() })
	return rawConn
}

func (c *firstAudioGuardController) PublishInitial(candidate *firstAudioGuardContext) error {
	if c == nil || candidate == nil {
		return errFirstAudioGuardContextInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isTerminalLocked() || c.admissionOpen || c.exclusive || c.publisher.Load() != nil {
		return errFirstAudioGuardContextTransition
	}
	if candidate.phase != firstAudioGuardDriverProbe || candidate.version != 0 {
		return errFirstAudioGuardContextTransition
	}
	return c.publisher.Publish(candidate)
}

// IsActivePhase lets synchronous SQLite authorizer callbacks consult the
// controller's sole phase authority without trusting a cached phase pointer.
func (c *firstAudioGuardController) IsActivePhase(phase firstAudioGuardContextPhase) bool {
	if c == nil {
		return false
	}
	active := c.publisher.Load()
	return !c.callbackTerminal.Load() && !c.publisher.callbackTerminal.Load() && active != nil && active.phase == phase
}

func (c *firstAudioGuardController) IsTerminal() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isTerminalLocked()
}

func (c *firstAudioGuardController) isTerminalLocked() bool {
	return c.terminal || c.callbackTerminal.Load() || c.publisher.callbackTerminal.Load()
}

func firstAudioPhysicalReadbackRegistrations(receipts []*firstAudioGuardProbeReceipt) (map[string]*sqlite3.SQLiteConn, error) {
	registrations := make(map[string]*sqlite3.SQLiteConn)
	for _, receipt := range receipts {
		if receipt == nil {
			return nil, errFirstAudioGuardContextTransition
		}
		if !receipt.physicalReadback.Load() {
			if receipt.requiresPhysicalReadback || receipt.txEpoch.Load() != 0 {
				return nil, errFirstAudioGuardContextTransition
			}
			continue // Pure logical lifecycle fixtures have no physical SQL connection.
		}
		raw := receipt.readbackRaw.Load()
		if raw == nil || receipt.binding.connection == "" {
			return nil, errFirstAudioGuardContextTransition
		}
		registrations[receipt.binding.connection] = raw
	}
	return registrations, nil
}

// TerminalizeFromSQLiteCallback is safe in an authorizer/UDF callback: it
// flips an atomic latch and never acquires lifecycle or database locks.
func (c *firstAudioGuardController) TerminalizeFromSQLiteCallback() error {
	if c != nil {
		err := c.publisher.terminalizeFromCallback()
		c.callbackTerminal.Store(true)
		return err
	}
	return errFirstAudioGuardContextTransition
}

// Terminalize lets the coordinator close lifecycle admission after SQLite
// returns a guarded SQL error; unlike callback paths, it may take the lock.
func (c *firstAudioGuardController) Terminalize() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.markOutOfBandTerminalLocked()
}

// ObserveDriverProbe is called only by the guarded SQLite ConnectHook's
// authorizer callback. The controller owns the receipt so a phase publisher
// cannot manufacture observation evidence or substitute another connection.
func (c *firstAudioGuardController) ObserveDriverProbe(rawConnection *sqlite3.SQLiteConn, action int, arg1, arg2, databaseName, triggerName string) error {
	if c == nil {
		return errFirstAudioGuardContextTransition
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.publisher.Load()
	if c.isTerminalLocked() || c.exclusive || active == nil || active.phase != firstAudioGuardDriverProbe {
		return errFirstAudioGuardContextTransition
	}
	registration, registered := c.connections.Lookup(rawConnection)
	if !registered || registration.Raw != rawConnection {
		c.markOutOfBandTerminalLocked()
		return errFirstAudioGuardProbeRejected
	}
	if c.driverProbe == nil {
		receipt, err := newFirstAudioDriverProbeReceipt(active, c.connections, rawConnection)
		if err != nil {
			c.markOutOfBandTerminalLocked()
			return err
		}
		c.driverProbe = receipt
	} else if c.driverProbe.connectionID != registration.ID || c.driverProbe.rawConnection != rawConnection {
		c.driverProbe.failed.Store(true)
		c.markOutOfBandTerminalLocked()
		return errFirstAudioGuardProbeRejected
	}
	if err := c.driverProbe.Observe(rawConnection, action, arg1, arg2, databaseName, triggerName); err != nil {
		c.markOutOfBandTerminalLocked()
		return err
	}
	return nil
}

// Acquire binds a request to the active context while holding the admission
// gate. Callers must not load or retain a context before calling this method.
func (c *firstAudioGuardController) Acquire() (*firstAudioGuardLease, error) {
	if c == nil {
		return nil, errFirstAudioGuardAdmissionClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasProbeDenialLocked() {
		c.markOutOfBandTerminalLocked()
		return nil, errFirstAudioGuardAdmissionClosed
	}
	if !c.admissionOpen || c.isTerminalLocked() || c.exclusive {
		return nil, errFirstAudioGuardAdmissionClosed
	}
	ctx := c.publisher.Load()
	if ctx == nil || ctx.version != c.gateVersion {
		return nil, errFirstAudioGuardAdmissionClosed
	}
	c.leases[ctx.version]++
	return &firstAudioGuardLease{owner: c, version: ctx.version, context: ctx}, nil
}

func (c *firstAudioGuardController) hasProbeDenialLocked() bool {
	for _, receipt := range c.probeReceipts {
		if receipt != nil && receipt.denials.Load() != 0 {
			return true
		}
	}
	return false
}

type firstAudioGuardLease struct {
	owner    *firstAudioGuardController
	version  uint64
	context  *firstAudioGuardContext
	released atomic.Bool
}

// Context returns the immutable snapshot bound to this lease. Any future SQL
// callback/execution must retain the lease until all use of the snapshot ends.
func (l *firstAudioGuardLease) Context() *firstAudioGuardContext {
	if l == nil || l.released.Load() {
		return nil
	}
	return l.context
}

func (l *firstAudioGuardLease) Release() error {
	if l == nil || l.owner == nil || !l.released.CompareAndSwap(false, true) {
		return errFirstAudioGuardLeaseReleased
	}
	c := l.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.leases[l.version] <= 0 {
		c.markOutOfBandTerminalLocked()
		return fmt.Errorf("%w: lease count underflow", errFirstAudioGuardLeaseReleased)
	}
	c.leases[l.version]--
	if c.leases[l.version] == 0 {
		delete(c.leases, l.version)
	}
	c.signalLocked()
	return nil
}

type firstAudioGuardTransitionLease struct {
	owner    *firstAudioGuardController
	released bool // protected by owner.mu
	busy     bool // protects transition work performed outside owner.mu
}

func (c *firstAudioGuardController) CloseAndDrain(ctx context.Context) (*firstAudioGuardTransitionLease, error) {
	if c == nil {
		return nil, errFirstAudioGuardAdmissionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.isTerminalLocked() || c.exclusive || c.draining {
		c.mu.Unlock()
		return nil, errFirstAudioGuardAdmissionClosed
	}
	c.admissionOpen = false
	c.draining = true
	c.signalLocked()
	for c.activeLeasesLocked() != 0 {
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			c.mu.Lock()
			c.markOutOfBandTerminalLocked()
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: %v", errFirstAudioGuardDrainTimeout, ctx.Err())
		}
		c.mu.Lock()
		if c.isTerminalLocked() {
			c.mu.Unlock()
			return nil, errFirstAudioGuardAdmissionClosed
		}
	}
	c.exclusive = true
	c.draining = false
	lease := &firstAudioGuardTransitionLease{owner: c}
	c.activeTransition = lease
	c.mu.Unlock()
	return lease, nil
}

func (l *firstAudioGuardTransitionLease) Publish(ctx context.Context, candidate *firstAudioGuardContext, evidence firstAudioGuardTransitionEvidence) error {
	if l == nil || l.owner == nil || candidate == nil {
		return errFirstAudioGuardContextInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	evidence.probeBindings = append([]firstAudioGuardProbeBinding(nil), evidence.probeBindings...)
	evidence.probeReceipts = append([]*firstAudioGuardProbeReceipt(nil), evidence.probeReceipts...)
	if evidence.expectedConnections != nil {
		expected := make(map[string]*sqlite3.SQLiteConn, len(evidence.expectedConnections))
		for id, raw := range evidence.expectedConnections {
			expected[id] = raw
		}
		evidence.expectedConnections = expected
	}
	c := l.owner
	c.mu.Lock()
	if l.released || l.busy || !c.exclusive || c.isTerminalLocked() || c.activeLeasesLocked() != 0 {
		c.mu.Unlock()
		return errFirstAudioGuardAdmissionClosed
	}
	active := c.publisher.Load()
	if c.hasProbeDenialLocked() {
		c.markOutOfBandTerminalLocked()
		l.released = true
		c.mu.Unlock()
		return errFirstAudioGuardContextTransition
	}
	if active != nil && active.phase == firstAudioGuardDriverProbe && candidate.phase == firstAudioGuardStartupProbe {
		if c.driverProbe == nil {
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		err := c.publisher.publishDriverStartup(candidate, c.driverProbe)
		if err == nil {
			c.driverProbe = nil
		} else {
			c.markOutOfBandTerminalLocked()
			l.released = true
		}
		c.mu.Unlock()
		return err
	}
	if active != nil && active.phase == firstAudioGuardStartupProbe && candidate.phase == firstAudioGuardPreflightPrepare {
		if !c.requirePreflightPrepare || candidate.version != 2 || candidate.generation != 1 || len(candidate.connectionIDs) != 0 || len(candidate.probes) != 0 || len(evidence.probeBindings) != 0 || len(evidence.probeReceipts) != 0 {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		permit := &firstAudioValidatedTransition{owner: c, lease: l, candidate: candidate, capability: c.publisher.transitionCapability, evidence: evidence}
		err := c.publisher.publishGuardedTransition(permit)
		if err != nil {
			c.markOutOfBandTerminalLocked()
			l.released = true
		}
		c.mu.Unlock()
		return err
	}
	if active != nil && active.phase == firstAudioGuardStartupProbe && candidate.phase == firstAudioGuardPreflight {
		if c.requirePreflightPrepare {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		registered, ok := evidence.preflightReceiptsFor(candidate)
		if !ok {
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		previous := c.probeReceipts
		c.probeReceipts = registered
		if err := c.publisher.Publish(candidate); err != nil {
			c.probeReceipts = previous
			c.mu.Unlock()
			return err
		}
		for _, receipt := range registered {
			if !receipt.arm() {
				c.markOutOfBandTerminalLocked()
				c.mu.Unlock()
				return errFirstAudioGuardContextTransition
			}
		}
		c.mu.Unlock()
		return nil
	}
	if active != nil && active.phase == firstAudioGuardPreflightPrepare && candidate.phase == firstAudioGuardPreflight {
		if !c.requirePreflightPrepare {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		permit := &firstAudioValidatedTransition{owner: c, lease: l, candidate: candidate, capability: c.publisher.transitionCapability, evidence: evidence}
		err := c.publisher.publishGuardedTransition(permit)
		if err != nil {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return err
		}
		if c.isTerminalLocked() {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardAdmissionClosed
		}
		c.mu.Unlock()
		return nil
	}
	var receipts []*firstAudioGuardProbeReceipt
	if active != nil && active.phase == firstAudioGuardPreflight && candidate.phase == firstAudioGuardJourney {
		if !evidence.matchesPreflight(active, candidate, c.probeReceipts) {
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		receipts = evidence.probeReceipts
	}
	if len(receipts) > 0 {
		l.busy = true
		c.mu.Unlock()
		drainErr := closeAndDrainFirstAudioProbeReceipts(ctx, receipts)
		c.mu.Lock()
		if drainErr != nil {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return drainErr
		}
		if l.released || !l.busy || !c.exclusive || c.isTerminalLocked() || c.activeLeasesLocked() != 0 || c.publisher.Load() != active {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardAdmissionClosed
		}
		if !evidence.matchesPreflight(active, candidate, c.probeReceipts) {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardContextTransition
		}
		for _, receipt := range receipts {
			if !receipt.probeGateDrained() {
				c.markOutOfBandTerminalLocked()
				l.released = true
				c.mu.Unlock()
				return errFirstAudioGuardContextTransition
			}
		}
		l.busy = false
	}
	if candidate.phase == firstAudioGuardJourney && !evidence.validFor(active, candidate) {
		c.mu.Unlock()
		return errFirstAudioGuardContextTransition
	}
	if candidate.phase == firstAudioGuardTerminal {
		c.mu.Unlock()
		return errFirstAudioGuardContextTransition
	}
	publish := func() error { return c.publisher.Publish(candidate) }
	var err error
	if candidate.phase == firstAudioGuardJourney && len(receipts) > 0 {
		registrations, registrationErr := firstAudioPhysicalReadbackRegistrations(receipts)
		if registrationErr != nil {
			c.mu.Unlock()
			return registrationErr
		}
		if len(registrations) > 0 {
			err = c.connections.WithLiveRegistrations(registrations, publish)
		} else {
			err = publish()
		}
	} else {
		err = publish()
	}
	c.mu.Unlock()
	return err
}

func (l *firstAudioGuardTransitionLease) OpenJourney() error {
	if l == nil || l.owner == nil {
		return errFirstAudioGuardAdmissionClosed
	}
	c := l.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.publisher.Load()
	if l.released || l.busy || !c.exclusive || c.isTerminalLocked() || c.hasProbeDenialLocked() || active == nil || active.phase != firstAudioGuardJourney || c.activeLeasesLocked() != 0 {
		return errFirstAudioGuardContextTransition
	}
	c.gateVersion = active.version
	c.exclusive = false
	c.admissionOpen = true
	l.released = true
	c.activeTransition = nil
	c.signalLocked()
	return nil
}

func (l *firstAudioGuardTransitionLease) MarkTerminal(ctx context.Context) error {
	if l == nil || l.owner == nil {
		return errFirstAudioGuardAdmissionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c := l.owner
	c.mu.Lock()
	if l.released || l.busy || !c.exclusive || c.activeLeasesLocked() != 0 {
		c.mu.Unlock()
		return errFirstAudioGuardAdmissionClosed
	}
	active := c.publisher.Load()
	if active == nil {
		c.markOutOfBandTerminalLocked()
		l.released = true
		c.mu.Unlock()
		return nil
	}
	var receipts []*firstAudioGuardProbeReceipt
	if active.phase == firstAudioGuardPreflight {
		for _, receipt := range c.probeReceipts {
			receipts = append(receipts, receipt)
		}
	}
	if len(receipts) > 0 {
		l.busy = true
		c.mu.Unlock()
		drainErr := closeAndDrainFirstAudioProbeReceipts(ctx, receipts)
		c.mu.Lock()
		if drainErr != nil {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return drainErr
		}
		if l.released || !l.busy || !c.exclusive || c.activeLeasesLocked() != 0 || c.publisher.Load() != active {
			c.markOutOfBandTerminalLocked()
			l.released = true
			c.mu.Unlock()
			return errFirstAudioGuardAdmissionClosed
		}
		l.busy = false
	}
	terminal, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: active.runID, currentLessonID: active.currentLessonID,
		generation: active.generation, phase: firstAudioGuardTerminal, version: active.version + 1,
	})
	if err == nil {
		err = c.publisher.Publish(terminal)
	}
	c.markOutOfBandTerminalLocked()
	c.probeReceipts = make(map[string]*firstAudioGuardProbeReceipt)
	l.released = true
	c.activeTransition = nil
	c.mu.Unlock()
	return err
}

func (c *firstAudioGuardController) activeLeasesLocked() int {
	active := 0
	for _, count := range c.leases {
		active += count
	}
	return active
}

func (c *firstAudioGuardController) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *firstAudioGuardController) markOutOfBandTerminalLocked() {
	for _, receipt := range c.probeReceipts {
		_ = receipt.closeProbeAdmission()
	}
	c.admissionOpen = false
	c.terminal = true
	c.callbackTerminal.Store(true)
	c.publisher.callbackTerminal.Store(true)
	c.exclusive = false
	c.draining = false
	c.signalLocked()
}

type firstAudioGuardTransitionEvidence struct {
	probeBindings       []firstAudioGuardProbeBinding
	probeReceipts       []*firstAudioGuardProbeReceipt
	expectedConnections map[string]*sqlite3.SQLiteConn
	snapshotUnchanged   bool
	registryReconciled  bool
	unexpectedDenials   uint64
	resetCommitted      bool
	writeCommitted      bool
	readbackVerified    bool
}

func (e firstAudioGuardTransitionEvidence) validFor(active, candidate *firstAudioGuardContext) bool {
	if active == nil || candidate == nil || !e.snapshotUnchanged || !e.registryReconciled || e.unexpectedDenials != 0 {
		return false
	}
	switch active.phase {
	case firstAudioGuardPreflight:
		return e.matchesPreflight(active, candidate, nil)
	case firstAudioGuardJourney:
		return candidate.phase == firstAudioGuardJourney && e.writeCommitted && e.readbackVerified
	case firstAudioGuardResetApply:
		return candidate.phase == firstAudioGuardJourney && e.resetCommitted && e.readbackVerified
	default:
		return false
	}
}

func (e firstAudioGuardTransitionEvidence) matchesPreflight(active, candidate *firstAudioGuardContext, registered map[string]*firstAudioGuardProbeReceipt) bool {
	if active == nil || candidate == nil || candidate.phase != firstAudioGuardJourney || len(e.probeBindings) == 0 || len(e.probeBindings) != len(active.probes) || len(e.probeBindings) != len(e.probeReceipts) {
		return false
	}
	verified := make(map[string]struct{}, len(e.probeBindings))
	for i, binding := range e.probeBindings {
		contract, ok := active.probes[binding.connection]
		receipt := e.probeReceipts[i]
		if !ok || !binding.matchesContract(contract) || receipt == nil || !receipt.completeFor(binding, contract.tokenDigest) || (registered != nil && registered[binding.connection] != receipt) {
			return false
		}
		if _, duplicate := verified[binding.connection]; duplicate {
			return false
		}
		verified[binding.connection] = struct{}{}
	}
	for id := range active.probes {
		if _, ok := verified[id]; !ok {
			return false
		}
	}
	return true
}

func (e firstAudioGuardTransitionEvidence) preflightReceiptsFor(candidate *firstAudioGuardContext) (map[string]*firstAudioGuardProbeReceipt, bool) {
	if candidate == nil || candidate.phase != firstAudioGuardPreflight || len(candidate.probes) == 0 || len(e.probeBindings) != len(candidate.probes) || len(e.probeReceipts) != len(candidate.probes) {
		return nil, false
	}
	registered := make(map[string]*firstAudioGuardProbeReceipt, len(e.probeReceipts))
	for i, binding := range e.probeBindings {
		contract, ok := candidate.probes[binding.connection]
		receipt := e.probeReceipts[i]
		if !ok || !binding.matchesContract(contract) || receipt == nil || receipt.requiresPhysicalReadback != candidate.requirePhysicalProbes || !sameFirstAudioProbeBinding(receipt.binding, binding) || subtle.ConstantTimeCompare(receipt.digest[:], contract.tokenDigest[:]) != 1 || receipt.armed.Load() || firstAudioGuardProbeReceiptState(receipt.state.Load()) != firstAudioGuardProbeIssued || receipt.active.Load() != 0 {
			return nil, false
		}
		if _, exists := registered[binding.connection]; exists {
			return nil, false
		}
		registered[binding.connection] = receipt
	}
	return registered, len(registered) == len(candidate.probes)
}

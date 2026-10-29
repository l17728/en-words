package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	errFirstAudioAdmissionClosed       = errors.New("first-audio operation admission closed")
	errFirstAudioSessionRevoked        = errors.New("first-audio websocket session revoked")
	errFirstAudioResetInProgress       = errors.New("first-audio reset already in progress")
	errFirstAudioResetDeadlineRequired = errors.New("first-audio reset requires an absolute deadline")
	errFirstAudioAdmissionTerminal     = errors.New("first-audio admission terminal")
	errFirstAudioSessionCloseFailed    = errors.New("first-audio websocket close failed")
)

type firstAudioSessionState uint8

const (
	firstAudioSessionReserved firstAudioSessionState = iota + 1
	firstAudioSessionActive
	firstAudioSessionRevoked
	firstAudioSessionReleased
)

type firstAudioAdmission struct {
	mu         sync.Mutex
	runID      string
	generation uint64
	accepting  bool
	resetting  bool
	terminal   bool
	nextID     uint64
	operations map[uint64]*firstAudioOperationLease
	sessions   map[uint64]*firstAudioSessionLease
	changed    chan struct{}
}

type firstAudioOperationLease struct {
	gate       *firstAudioAdmission
	id         uint64
	runID      string
	generation uint64
	ctx        context.Context
	once       sync.Once
}

type firstAudioSessionLease struct {
	gate       *firstAudioAdmission
	id         uint64
	runID      string
	generation uint64
	state      firstAudioSessionState
	close      func(context.Context) error
	once       sync.Once
}

type firstAudioResetBarrier struct {
	gate            *firstAudioAdmission
	priorGeneration uint64
	ctx             context.Context
	published       atomic.Bool
}

type firstAudioOperationContextKey struct{}

func newFirstAudioAdmission(runID string, generation uint64) (*firstAudioAdmission, error) {
	if !firstAudioRunIDPattern.MatchString(runID) || generation == 0 {
		return nil, errFirstAudioGuardContextInvalid
	}
	return &firstAudioAdmission{
		runID: runID, generation: generation, accepting: true,
		operations: make(map[uint64]*firstAudioOperationLease),
		sessions:   make(map[uint64]*firstAudioSessionLease), changed: make(chan struct{}),
	}, nil
}

// AcquireOperation is the single admission point for REST, WS message, timer,
// and delayed callback work. The lease remains live through side effects and
// response/frame enqueue so reset cannot cross an admitted operation.
func (g *firstAudioAdmission) AcquireOperation(parent context.Context) (context.Context, *firstAudioOperationLease, error) {
	if parent == nil {
		return nil, nil, errFirstAudioGuardContextInvalid
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal {
		return nil, nil, errFirstAudioAdmissionTerminal
	}
	if !g.accepting || g.resetting {
		return nil, nil, errFirstAudioAdmissionClosed
	}
	g.nextID++
	lease := &firstAudioOperationLease{gate: g, id: g.nextID, runID: g.runID, generation: g.generation}
	lease.ctx = context.WithValue(parent, firstAudioOperationContextKey{}, lease)
	g.operations[lease.id] = lease
	g.signalLocked()
	return lease.ctx, lease, nil
}

func (g *firstAudioAdmission) OperationContextActive(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	lease, ok := ctx.Value(firstAudioOperationContextKey{}).(*firstAudioOperationLease)
	if !ok || lease == nil || lease.gate != g {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.operations[lease.id] == lease && lease.runID == g.runID && lease.generation == g.generation && !g.terminal
}

func (l *firstAudioOperationLease) Release() {
	if l == nil || l.gate == nil {
		return
	}
	l.once.Do(func() {
		g := l.gate
		g.mu.Lock()
		if g.operations[l.id] == l {
			delete(g.operations, l.id)
			g.signalLocked()
		}
		g.mu.Unlock()
	})
}

// ReserveSession precedes WS Upgrade. Activate must follow a successful
// upgrade; a reset that wins the race revokes the reservation and prevents it
// from reaching Room/worker code. closeSocket runs outside the admission lock
// with the reset deadline and must honor its context.
func (g *firstAudioAdmission) ReserveSession(closeSocket func(context.Context) error) (*firstAudioSessionLease, error) {
	if closeSocket == nil {
		return nil, errFirstAudioGuardContextInvalid
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal {
		return nil, errFirstAudioAdmissionTerminal
	}
	if !g.accepting || g.resetting {
		return nil, errFirstAudioAdmissionClosed
	}
	g.nextID++
	lease := &firstAudioSessionLease{gate: g, id: g.nextID, runID: g.runID, generation: g.generation, state: firstAudioSessionReserved, close: closeSocket}
	g.sessions[lease.id] = lease
	g.signalLocked()
	return lease, nil
}

func (l *firstAudioSessionLease) Activate() error {
	if l == nil || l.gate == nil {
		return errFirstAudioSessionRevoked
	}
	g := l.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal {
		return errFirstAudioAdmissionTerminal
	}
	if g.sessions[l.id] != l || l.state != firstAudioSessionReserved || l.runID != g.runID || l.generation != g.generation || !g.accepting || g.resetting {
		return errFirstAudioSessionRevoked
	}
	l.state = firstAudioSessionActive
	g.signalLocked()
	return nil
}

func (l *firstAudioSessionLease) Revoked() bool {
	if l == nil || l.gate == nil {
		return true
	}
	g := l.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	return l.state == firstAudioSessionRevoked || l.state == firstAudioSessionReleased || g.sessions[l.id] != l
}

func (l *firstAudioSessionLease) Release() {
	if l == nil || l.gate == nil {
		return
	}
	l.once.Do(func() {
		g := l.gate
		g.mu.Lock()
		if g.sessions[l.id] == l {
			l.state = firstAudioSessionReleased
			delete(g.sessions, l.id)
			g.signalLocked()
		}
		g.mu.Unlock()
	})
}

// BeginReset closes admission under the acquisition lock, revokes all old
// sessions, closes their sockets outside the lock, and waits for both session
// and operation leases to drain. A timeout permanently fails the gate closed.
func (g *firstAudioAdmission) BeginReset(ctx context.Context) (*firstAudioResetBarrier, error) {
	if ctx == nil {
		return nil, errFirstAudioGuardContextInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return nil, errFirstAudioResetDeadlineRequired
	}
	g.mu.Lock()
	if g.terminal {
		g.mu.Unlock()
		return nil, errFirstAudioAdmissionTerminal
	}
	if g.resetting || !g.accepting {
		g.mu.Unlock()
		return nil, errFirstAudioResetInProgress
	}
	g.accepting, g.resetting = false, true
	barrier := &firstAudioResetBarrier{gate: g, priorGeneration: g.generation, ctx: ctx}
	closeSockets := make([]func(context.Context) error, 0, len(g.sessions))
	for _, session := range g.sessions {
		if session.state == firstAudioSessionReserved || session.state == firstAudioSessionActive {
			session.state = firstAudioSessionRevoked
			if session.close != nil {
				closeSockets = append(closeSockets, session.close)
			}
		}
	}
	g.signalLocked()
	g.mu.Unlock()
	closeCtx, cancelClose := context.WithCancel(ctx)
	defer cancelClose()
	closeResults := make(chan error, len(closeSockets))
	for _, closeSocket := range closeSockets {
		go func(closeFn func(context.Context) error) {
			var closeErr error
			defer func() {
				if recover() != nil {
					closeErr = errFirstAudioSessionCloseFailed
				}
				closeResults <- closeErr
			}()
			closeErr = closeFn(closeCtx)
		}(closeSocket)
	}
	for range closeSockets {
		select {
		case closeErr := <-closeResults:
			if closeErr != nil {
				cancelClose()
				g.terminalize()
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				return nil, errFirstAudioSessionCloseFailed
			}
		case <-ctx.Done():
			g.terminalize()
			return nil, ctx.Err()
		}
	}
	for {
		g.mu.Lock()
		if g.terminal {
			g.mu.Unlock()
			return nil, errFirstAudioAdmissionTerminal
		}
		if len(g.operations) == 0 && len(g.sessions) == 0 {
			g.mu.Unlock()
			if err := ctx.Err(); err != nil {
				g.terminalize()
				return nil, err
			}
			return barrier, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			g.terminalize()
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// PublishNext is legal only after the caller's database reset and same-pool
// readback succeed. Uncertain paths must terminalize instead of reopening.
func (b *firstAudioResetBarrier) PublishNext() (uint64, error) {
	if b == nil || b.gate == nil || !b.published.CompareAndSwap(false, true) {
		return 0, errFirstAudioResetInProgress
	}
	g := b.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.terminal || !g.resetting || g.accepting || g.generation != b.priorGeneration || len(g.operations) != 0 || len(g.sessions) != 0 {
		b.published.Store(false)
		return 0, errFirstAudioAdmissionTerminal
	}
	if err := b.ctx.Err(); err != nil {
		g.terminal, g.accepting, g.resetting = true, false, false
		g.signalLocked()
		return 0, err
	}
	g.generation++
	g.resetting, g.accepting = false, true
	g.signalLocked()
	return g.generation, nil
}

func (g *firstAudioAdmission) terminalize() {
	g.mu.Lock()
	g.terminal, g.accepting, g.resetting = true, false, false
	g.signalLocked()
	g.mu.Unlock()
}

func (g *firstAudioAdmission) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *firstAudioAdmission) counts() (operations, sessions int, generation uint64, accepting, terminal bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.operations), len(g.sessions), g.generation, g.accepting, g.terminal
}

func TestFirstAudioJourneyAdmissionCanceledOperationContextIsInactive(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-canceled-operation", 1)
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	ctx, lease, err := gate.AcquireOperation(parent)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.OperationContextActive(ctx) {
		t.Fatal("new operation context is not active")
	}
	cancel()
	if gate.OperationContextActive(ctx) {
		t.Fatal("canceled operation context remains active")
	}
	lease.Release()
}

func TestFirstAudioJourneyAdmissionResetBarrier(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-barrier", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, operation, err := gate.AcquireOperation(context.Background())
	if err != nil || !gate.OperationContextActive(ctx) {
		t.Fatalf("operation acquisition failed: active=%v err=%v", gate.OperationContextActive(ctx), err)
	}
	var closeCalls atomic.Int32
	session, err := gate.ReserveSession(func(context.Context) error { closeCalls.Add(1); return nil })
	if err != nil {
		t.Fatal("session reservation failed:", err)
	}
	activeSession, err := gate.ReserveSession(func(context.Context) error { closeCalls.Add(1); return nil })
	if err != nil {
		t.Fatal("active session reservation failed:", err)
	}
	if err := activeSession.Activate(); err != nil {
		t.Fatal("active session activation failed:", err)
	}
	resetDone := make(chan *firstAudioResetBarrier, 1)
	resetErr := make(chan error, 1)
	resetCtx, cancelReset := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelReset()
	go func() {
		barrier, beginErr := gate.BeginReset(resetCtx)
		resetDone <- barrier
		resetErr <- beginErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		_, _, _, accepting, _ := gate.counts()
		if !accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset did not close admission")
		}
		time.Sleep(time.Millisecond)
	}
	if closeCalls.Load() != 2 || !session.Revoked() || !activeSession.Revoked() {
		t.Fatalf("sessions were not revoked: close_calls=%d reserved=%v active=%v", closeCalls.Load(), session.Revoked(), activeSession.Revoked())
	}
	if !gate.OperationContextActive(ctx) {
		t.Fatal("pre-close operation was invalidated before drain completed")
	}
	if _, _, err := gate.AcquireOperation(context.Background()); !errors.Is(err, errFirstAudioAdmissionClosed) {
		t.Fatalf("new operation during reset error=%v", err)
	}
	if err := session.Activate(); !errors.Is(err, errFirstAudioSessionRevoked) {
		t.Fatalf("revoked session activation error=%v", err)
	}
	select {
	case <-resetDone:
		t.Fatal("reset crossed live operation/session leases")
	default:
	}
	operation.Release()
	session.Release()
	session.Release()
	activeSession.Release()
	barrier := <-resetDone
	if err := <-resetErr; err != nil || barrier == nil {
		t.Fatalf("reset drain failed: barrier=%v err=%v", barrier, err)
	}
	generation, err := barrier.PublishNext()
	if err != nil || generation != 2 {
		t.Fatalf("generation publication=%d err=%v", generation, err)
	}
	if gate.OperationContextActive(ctx) {
		t.Fatal("old-generation context remained active after reset")
	}
	newCtx, nextOperation, err := gate.AcquireOperation(context.Background())
	if err != nil || !gate.OperationContextActive(newCtx) {
		t.Fatalf("new generation operation rejected: %v", err)
	}
	nextOperation.Release()
}

func TestFirstAudioJourneyAdmissionResetTimeoutIsTerminal(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-timeout", 7)
	if err != nil {
		t.Fatal(err)
	}
	_, operation, err := gate.AcquireOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resetDone := make(chan error, 1)
	go func() {
		barrier, resetErr := gate.BeginReset(ctx)
		if barrier != nil {
			resetDone <- errors.New("reset unexpectedly drained a live operation")
			return
		}
		resetDone <- resetErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		_, _, _, accepting, _ := gate.counts()
		if !accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed reset did not close admission")
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-resetDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out reset error=%v", err)
	}
	if _, _, _, accepting, terminal := gate.counts(); accepting || !terminal {
		t.Fatalf("timeout did not fail closed: accepting=%v terminal=%v", accepting, terminal)
	}
	operation.Release()
	if _, _, err := gate.AcquireOperation(context.Background()); !errors.Is(err, errFirstAudioAdmissionTerminal) {
		t.Fatalf("terminal gate admitted work: %v", err)
	}
}

func TestFirstAudioJourneyAdmissionExpiredBeforePublishIsTerminal(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-publish-timeout", 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	barrier, err := gate.BeginReset(ctx)
	if err != nil || barrier == nil {
		t.Fatalf("empty drain barrier=%v err=%v", barrier, err)
	}
	<-ctx.Done()
	if generation, err := barrier.PublishNext(); generation != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired publish generation=%d err=%v", generation, err)
	}
	if _, _, _, accepting, terminal := gate.counts(); accepting || !terminal {
		t.Fatalf("expired publish did not fail closed: accepting=%v terminal=%v", accepting, terminal)
	}
}

func TestFirstAudioJourneyAdmissionHonorsCanceledInputs(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-canceled", 3)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := gate.AcquireOperation(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation acquisition error=%v", err)
	}
	if barrier, err := gate.BeginReset(canceled); barrier != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reset barrier=%v err=%v", barrier, err)
	}
	if _, _, _, accepting, terminal := gate.counts(); !accepting || terminal {
		t.Fatalf("canceled reset changed gate state: accepting=%v terminal=%v", accepting, terminal)
	}
	if barrier, err := gate.BeginReset(context.Background()); barrier != nil || !errors.Is(err, errFirstAudioResetDeadlineRequired) {
		t.Fatalf("unbounded reset barrier=%v err=%v", barrier, err)
	}
	if _, err := gate.ReserveSession(nil); !errors.Is(err, errFirstAudioGuardContextInvalid) {
		t.Fatalf("nil socket close callback error=%v", err)
	}
}

func TestFirstAudioJourneyAdmissionSocketClosePanicIsTerminal(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-close-panic", 1)
	if err != nil {
		t.Fatal(err)
	}
	session, err := gate.ReserveSession(func(context.Context) error { panic("synthetic socket close panic") })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if barrier, err := gate.BeginReset(ctx); barrier != nil || !errors.Is(err, errFirstAudioSessionCloseFailed) {
		t.Fatalf("socket-close panic barrier=%v err=%v", barrier, err)
	}
	if _, _, _, accepting, terminal := gate.counts(); accepting || !terminal {
		t.Fatalf("socket-close panic did not fail closed: accepting=%v terminal=%v", accepting, terminal)
	}
	session.Release()
}

func TestFirstAudioJourneyAdmissionCloseCallbackRespectsResetDeadline(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-close-timeout", 1)
	if err != nil {
		t.Fatal(err)
	}
	closeStarted := make(chan struct{}, 1)
	closeExited := make(chan struct{})
	var closeCalls atomic.Int32
	session, err := gate.ReserveSession(func(ctx context.Context) error {
		closeCalls.Add(1)
		closeStarted <- struct{}{}
		<-ctx.Done()
		close(closeExited)
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resetDone := make(chan error, 1)
	go func() {
		barrier, resetErr := gate.BeginReset(ctx)
		if barrier != nil {
			resetDone <- errors.New("blocking close unexpectedly produced a reset barrier")
			return
		}
		resetDone <- resetErr
	}()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("socket close callback did not start")
	}
	if err := <-resetDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocking close reset error=%v", err)
	}
	select {
	case <-closeExited:
	case <-time.After(time.Second):
		t.Fatal("context-aware socket close callback did not exit after reset deadline")
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("socket close call count=%d, want 1", closeCalls.Load())
	}
	if _, _, _, accepting, terminal := gate.counts(); accepting || !terminal {
		t.Fatalf("blocked close did not fail closed: accepting=%v terminal=%v", accepting, terminal)
	}
	session.Release()
}

func TestFirstAudioJourneyAdmissionConcurrentResetLinearization(t *testing.T) {
	gate, err := newFirstAudioAdmission("capture-reset-race", 1)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var mu sync.Mutex
	var acquired []*firstAudioOperationLease
	var rejected atomic.Int32
	var unexpected []error
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, lease, acquireErr := gate.AcquireOperation(context.Background())
			if acquireErr == nil {
				mu.Lock()
				acquired = append(acquired, lease)
				mu.Unlock()
			} else if errors.Is(acquireErr, errFirstAudioAdmissionClosed) {
				rejected.Add(1)
			} else {
				mu.Lock()
				unexpected = append(unexpected, acquireErr)
				mu.Unlock()
			}
		}()
	}
	close(start)
	resetCtx, cancelReset := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelReset()
	result := make(chan struct {
		barrier *firstAudioResetBarrier
		err     error
	}, 1)
	go func() {
		barrier, beginErr := gate.BeginReset(resetCtx)
		result <- struct {
			barrier *firstAudioResetBarrier
			err     error
		}{barrier, beginErr}
	}()
	workers.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		_, _, _, accepting, _ := gate.counts()
		if !accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent reset did not close admission")
		}
		time.Sleep(time.Millisecond)
	}
	var duringResetRejected atomic.Int32
	var duringResetUnexpected atomic.Int32
	var rejectWorkers sync.WaitGroup
	for i := 0; i < 16; i++ {
		rejectWorkers.Add(1)
		go func() {
			defer rejectWorkers.Done()
			_, lease, err := gate.AcquireOperation(context.Background())
			if errors.Is(err, errFirstAudioAdmissionClosed) {
				duringResetRejected.Add(1)
			} else {
				duringResetUnexpected.Add(1)
				if lease != nil {
					lease.Release()
				}
			}
		}()
	}
	rejectWorkers.Wait()
	if duringResetRejected.Load() != 16 || duringResetUnexpected.Load() != 0 {
		t.Fatalf("operations admitted or unexpectedly failed during reset: rejected=%d unexpected=%d", duringResetRejected.Load(), duringResetUnexpected.Load())
	}
	if _, _, err := gate.AcquireOperation(context.Background()); !errors.Is(err, errFirstAudioAdmissionClosed) {
		t.Fatalf("operation admitted after reset linearization: %v", err)
	}
	mu.Lock()
	seenIDs := make(map[uint64]struct{}, len(acquired))
	for _, lease := range acquired {
		if lease.generation != 1 {
			unexpected = append(unexpected, errors.New("operation captured an unexpected generation"))
		}
		if _, exists := seenIDs[lease.id]; exists {
			unexpected = append(unexpected, errors.New("operation lease ID was reused"))
		}
		seenIDs[lease.id] = struct{}{}
		lease.Release()
	}
	unexpectedErrs := append([]error(nil), unexpected...)
	acquiredCount := len(acquired)
	mu.Unlock()
	if len(unexpectedErrs) != 0 || acquiredCount+int(rejected.Load()) != 64 {
		t.Fatalf("acquire race evidence: acquired=%d rejected=%d unexpected=%v", acquiredCount, rejected.Load(), unexpectedErrs)
	}
	reset := <-result
	if reset.err != nil || reset.barrier == nil {
		t.Fatalf("reset failed: barrier=%v err=%v", reset.barrier, reset.err)
	}
	generation, err := reset.barrier.PublishNext()
	if err != nil || generation != 2 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
	if operations, sessions, generation, accepting, terminal := gate.counts(); operations != 0 || sessions != 0 || generation != 2 || !accepting || terminal {
		t.Fatalf("invalid final state ops=%d sessions=%d generation=%d accepting=%v terminal=%v", operations, sessions, generation, accepting, terminal)
	}
}

func TestFirstAudioJourneyAdmissionConcurrentSessionActivationAndReset(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		gate, err := newFirstAudioAdmission("capture-session-race", 1)
		if err != nil {
			t.Fatal(err)
		}
		var closeCalls atomic.Int32
		closeCalled := make(chan struct{}, 1)
		session, err := gate.ReserveSession(func(context.Context) error {
			closeCalls.Add(1)
			closeCalled <- struct{}{}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		resetCtx, cancelReset := context.WithTimeout(context.Background(), 2*time.Second)
		activateDone := make(chan error, 1)
		resetDone := make(chan struct {
			barrier *firstAudioResetBarrier
			err     error
		}, 1)
		go func() {
			<-start
			activateDone <- session.Activate()
		}()
		go func() {
			<-start
			barrier, resetErr := gate.BeginReset(resetCtx)
			resetDone <- struct {
				barrier *firstAudioResetBarrier
				err     error
			}{barrier, resetErr}
		}()
		close(start)
		activateErr := <-activateDone
		if activateErr != nil && !errors.Is(activateErr, errFirstAudioSessionRevoked) {
			t.Fatalf("iteration %d activation error=%v", iteration, activateErr)
		}
		deadline := time.Now().Add(time.Second)
		for {
			_, _, _, accepting, _ := gate.counts()
			if !accepting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d reset did not close admission", iteration)
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-closeCalled:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d socket close callback did not run", iteration)
		}
		if !session.Revoked() || closeCalls.Load() != 1 {
			t.Fatalf("iteration %d session escaped reset: revoked=%v close_calls=%d", iteration, session.Revoked(), closeCalls.Load())
		}
		session.Release()
		session.Release()
		result := <-resetDone
		if result.err != nil || result.barrier == nil {
			t.Fatalf("iteration %d reset barrier=%v err=%v", iteration, result.barrier, result.err)
		}
		if _, err := result.barrier.PublishNext(); err != nil {
			t.Fatalf("iteration %d publish generation: %v", iteration, err)
		}
		if err := session.Activate(); !errors.Is(err, errFirstAudioSessionRevoked) {
			t.Fatalf("iteration %d stale session activated after reset: %v", iteration, err)
		}
		if closeCalls.Load() != 1 {
			t.Fatalf("iteration %d socket closed %d times", iteration, closeCalls.Load())
		}
		cancelReset()
	}
}

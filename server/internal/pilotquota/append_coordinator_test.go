package pilotquota

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	clock   *manualClock
	due     time.Time
	fn      func()
	stopped bool
}

func newManualClock() *manualClock { return &manualClock{now: time.Unix(100, 0)} }

func waitSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitResult(t *testing.T, ch <-chan error, name string) error {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

func releaseOnce(t *testing.T, ch chan struct{}) func() {
	t.Helper()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}
func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *manualClock) AfterFunc(d time.Duration, fn func()) DeadlineTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{clock: c, due: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	return t
}
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*manualTimer
	for _, t := range c.timers {
		if !t.stopped && !t.due.After(c.now) {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

func (c *manualClock) JumpWithoutFiring(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeJournal struct {
	mu             sync.Mutex
	data           []byte
	calls          int
	outcome        JournalWrite
	writeErr       error
	offsetOverride *int64
	panicWrite     bool
	onWrite        func()
	events         *[]string
	entered        chan struct{}
	release        chan struct{}
}

func (j *fakeJournal) AppendAndSync(frame []byte) JournalAppendResult {
	if j.events != nil {
		*j.events = append(*j.events, "journal-append")
	}
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	if j.entered != nil {
		select {
		case j.entered <- struct{}{}:
		default:
		}
	}
	if j.release != nil {
		<-j.release
	}
	if j.panicWrite {
		panic("journal secret sentinel")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	offset := int64(len(j.data))
	outcome := j.outcome
	if outcome == 0 {
		outcome = JournalDurable
	}
	switch outcome {
	case JournalNotWritten:
		return JournalAppendResult{Outcome: outcome, Offset: -1, Err: j.writeErr}
	case JournalDurable:
		j.data = append(j.data, frame...)
		if j.offsetOverride != nil {
			offset = *j.offsetOverride
		}
		if j.onWrite != nil {
			j.onWrite()
		}
		if j.offsetOverride == nil {
			offset += int64(len(frame))
		}
		return JournalAppendResult{Outcome: outcome, Offset: offset, Err: j.writeErr}
	default:
		return JournalAppendResult{Outcome: outcome, Offset: -1, Err: j.writeErr}
	}
}

type fakeAnchor struct {
	mu        sync.Mutex
	tuple     AnchorTuple
	readErr   error
	readPanic bool
	advance   AnchorAdvance
	casErr    error
	casPanic  bool
	onRead    func()
	onCAS     func()
	events    *[]string
}

func (a *fakeAnchor) Read() (AnchorReadResult, error) {
	if a.events != nil {
		*a.events = append(*a.events, "anchor-read")
	}
	if a.readPanic {
		panic("anchor read secret sentinel")
	}
	if a.onRead != nil {
		a.onRead()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readErr != nil {
		return AnchorReadResult{}, a.readErr
	}
	return AnchorReadResult{Tuple: a.tuple}, nil
}
func (a *fakeAnchor) CompareAndAdvance(expected, next AnchorTuple) (AnchorAdvance, error) {
	if a.events != nil {
		*a.events = append(*a.events, "anchor-cas")
	}
	if a.casPanic {
		panic("anchor cas secret sentinel")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	result := a.advance
	if result == 0 {
		result = AnchorAdvanced
	}
	if a.casErr != nil {
		return result, a.casErr
	}
	if a.tuple != expected {
		return AnchorConflict, nil
	}
	if result == AnchorAdvanced {
		a.tuple = next
	}
	if a.onCAS != nil {
		a.onCAS()
	}
	return result, nil
}

type fakeKeys struct {
	key         []byte
	ok          bool
	panicLookup bool
	onLookup    func()
	last        []byte
}

func (k *fakeKeys) KeyFor([16]byte) ([]byte, bool) {
	if k.onLookup != nil {
		k.onLookup()
	}
	if k.panicLookup {
		panic("key provider secret sentinel")
	}
	k.last = append([]byte(nil), k.key...)
	return k.last, k.ok
}

func newAppendTest(t *testing.T) (*AppendCoordinator, *fakeJournal, *fakeAnchor, *fakeKeys, *manualClock, AnchorTimeSample) {
	t.Helper()
	initial := AnchorTuple{RestoreGeneration: 7, TrustedTimeHighWaterNS: 100, BootID: [16]byte{9}, BoottimeNS: 200}
	journal := &fakeJournal{}
	anchor := &fakeAnchor{tuple: initial}
	keys := &fakeKeys{key: bytes.Repeat([]byte{0x4a}, 32), ok: true}
	clock := newManualClock()
	coordinator, err := newAppendCoordinator(initial, 0, journal, keys, anchor, clock, 10*time.Second)
	if err != nil {
		t.Fatalf("create coordinator: %v", err)
	}
	sample := AnchorTimeSample{TrustedTimeHighWaterNS: 100, BootID: [16]byte{9}, BoottimeNS: 201}
	return coordinator, journal, anchor, keys, clock, sample
}

func permitRecord() Record {
	return Record{Type: Permit, KeyID: [16]byte{1}, OperationID: [16]byte{2}, Sequence: 1, RecordTimeNS: 100, PrincipalRef: [32]byte{3}, RequestID: [16]byte{4}}
}

func TestTransactionTerminalOutcomesAreSingleAndMonotonic(t *testing.T) {
	tx := newTransaction(time.Now().Add(time.Second))
	if !tx.finish(transactionCommitting, transactionAckable) {
		t.Fatal("first terminal transition lost")
	}
	if tx.finish(transactionCommitting, transactionUncertain) {
		t.Fatal("terminal transaction transitioned twice")
	}
	select {
	case <-tx.done:
	default:
		t.Fatal("terminal channel was not closed")
	}
	c, _, _, _, clock, _ := newAppendTest(t)
	c.trip(tx)
	c.abort(tx)
	if c.breaker.Load() {
		t.Fatal("late uncertainty revoked ACKABLE")
	}

	tx2 := newTransaction(time.Now().Add(time.Second))
	c.abort(tx2)
	if transactionState(tx2.state.Load()) != transactionAborted || c.breaker.Load() {
		t.Fatal("known clean abort was not nonfatal")
	}
	c.trip(tx2)
	if c.breaker.Load() || transactionState(tx2.state.Load()) != transactionAborted {
		t.Fatal("late timer changed a clean abort")
	}

	tx3 := newTransaction(time.Now().Add(time.Second))
	tx3.finish(transactionCommitting, transactionUncertain)
	c.breaker.Store(false)
	c.trip(tx3)
	if !c.breaker.Load() {
		t.Fatal("re-observing an uncertain transaction did not trip the breaker")
	}
	c.breaker.Store(false)
	c.abort(tx3)
	if !c.breaker.Load() {
		t.Fatal("aborting an uncertain transaction did not remain fail-closed")
	}

	tx4 := newTransaction(clock.Now().Add(-time.Second))
	c.breaker.Store(false)
	c.abort(tx4)
	if !c.breaker.Load() || transactionState(tx4.state.Load()) != transactionUncertain {
		t.Fatal("expired abort did not become uncertain and trip the breaker")
	}
}

func TestGlobalBreakerPreventsOtherTransactionAck(t *testing.T) {
	c, j, a, _, clock, sample := newAppendTest(t)
	casEntered, releaseCAS := make(chan struct{}), make(chan struct{})
	release := releaseOnce(t, releaseCAS)
	t.Cleanup(release)
	a.onCAS = func() {
		close(casEntered)
		<-releaseCAS
	}
	result := make(chan error, 1)
	go func() { result <- c.Append(permitRecord(), sample) }()
	waitSignal(t, casEntered, "Append to pause at anchor CAS") // Actual Append is before ACK publication.
	waiting := newTransaction(clock.Now().Add(-time.Second))
	c.trip(waiting) // Inject the expired waiter's terminal transition through the timer arbiter.
	release()
	if err := waitResult(t, result, "paused Append"); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("active append error = %v", err)
	}
	if !c.breaker.Load() || c.head.Sequence != 0 || a.tuple.Sequence != 1 || len(j.data) == 0 {
		t.Fatal("global breaker did not quarantine the durable-but-unacknowledged append")
	}
}

func TestPublishAckableRejectsExpiredBrokenAndTerminalTransactions(t *testing.T) {
	t.Run("deadline wins", func(t *testing.T) {
		c, _, _, _, clock, _ := newAppendTest(t)
		tx := newTransaction(clock.Now().Add(-time.Second))
		tx.state.Store(uint32(transactionWriteAuthorized))
		if c.publishAckable(tx, tx.deadline, AnchorTuple{Sequence: 1}, 10) {
			t.Fatal("expired transaction published ACKABLE")
		}
		if transactionState(tx.state.Load()) != transactionUncertain || !c.breaker.Load() || c.head.Sequence != 0 {
			t.Fatal("expired publication did not fail closed")
		}
	})
	t.Run("breaker already tripped", func(t *testing.T) {
		c, _, _, _, clock, _ := newAppendTest(t)
		tx := newTransaction(clock.Now().Add(time.Second))
		tx.state.Store(uint32(transactionWriteAuthorized))
		c.breaker.Store(true)
		if c.publishAckable(tx, tx.deadline, AnchorTuple{Sequence: 1}, 10) {
			t.Fatal("transaction published after global breaker")
		}
		if transactionState(tx.state.Load()) != transactionUncertain || c.head.Sequence != 0 {
			t.Fatal("breaker rejection did not quarantine transaction")
		}
	})
	t.Run("terminal transaction", func(t *testing.T) {
		c, _, _, _, clock, _ := newAppendTest(t)
		tx := newTransaction(clock.Now().Add(time.Second))
		if !tx.finish(transactionCommitting, transactionAborted) {
			t.Fatal("setup abort failed")
		}
		if c.publishAckable(tx, tx.deadline, AnchorTuple{Sequence: 1}, 10) {
			t.Fatal("terminal transaction published ACKABLE")
		}
		if transactionState(tx.state.Load()) != transactionAborted || c.breaker.Load() || c.head.Sequence != 0 {
			t.Fatal("terminal outcome changed during publication")
		}
	})
}

func TestAppendCoordinatorOrdersDurabilityAndPublishesOnlyAfterAnchor(t *testing.T) {
	c, j, a, keys, _, sample := newAppendTest(t)
	var events []string
	j.events, a.events = &events, &events
	a.onCAS = func() {
		if c.head.Sequence != 0 {
			t.Error("head published before anchor CAS completed")
		}
	}
	if err := c.Append(permitRecord(), sample); err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(j.data) == 0 {
		t.Fatal("journal did not receive frame")
	}
	if a.tuple.Sequence != 1 || a.tuple.Digest != FrameDigest(j.data) || a.tuple.RestoreGeneration != 7 || a.tuple.BoottimeNS != sample.BoottimeNS {
		t.Fatalf("unexpected anchored tuple: %+v", a.tuple)
	}
	if c.head != a.tuple || c.bytes != int64(len(j.data)) {
		t.Fatalf("published state differs from anchor: head=%+v bytes=%d", c.head, c.bytes)
	}
	if fmt.Sprint(events) != "[anchor-read journal-append anchor-cas]" {
		t.Fatalf("operation order = %v", events)
	}
	if !bytes.Equal(keys.last, make([]byte, len(keys.last))) {
		t.Fatal("temporary key copy was not cleared")
	}
}

func TestAppendCoordinatorRejectsInvalidInputBeforeAnyMutation(t *testing.T) {
	for name, mutate := range map[string]func(*Record){
		"sequence":            func(r *Record) { r.Sequence++ },
		"head":                func(r *Record) { r.Previous[0] = 1 },
		"invalid":             func(r *Record) { r.Type = RecordType(99) },
		"timestamp":           func(r *Record) { r.RecordTimeNS++ },
		"boottime regression": func(r *Record) {},
	} {
		t.Run(name, func(t *testing.T) {
			c, j, a, _, _, sample := newAppendTest(t)
			r := permitRecord()
			mutate(&r)
			if name == "boottime regression" {
				sample.BoottimeNS = 199
			}
			if err := c.Append(r, sample); !errors.Is(err, ErrCoordinatorRejected) {
				t.Fatalf("Append error = %v", err)
			}
			if len(j.data) != 0 || a.tuple.Sequence != 0 || c.bytes != 0 {
				t.Fatalf("invalid input mutated state")
			}
		})
	}
}

func TestAppendCoordinatorRejectsInvalidConfiguration(t *testing.T) {
	initial := AnchorTuple{RestoreGeneration: 1, BootID: [16]byte{1}}
	j, a, keys, clock := &fakeJournal{}, &fakeAnchor{tuple: initial}, &fakeKeys{key: bytes.Repeat([]byte{1}, 32), ok: true}, newManualClock()
	checks := []struct {
		name   string
		create func() error
	}{
		{"zero generation", func() error {
			v := initial
			v.RestoreGeneration = 0
			_, e := newAppendCoordinator(v, 0, j, keys, a, clock, time.Second)
			return e
		}},
		{"zero boot id", func() error {
			v := initial
			v.BootID = [16]byte{}
			_, e := newAppendCoordinator(v, 0, j, keys, a, clock, time.Second)
			return e
		}},
		{"time overflow", func() error {
			v := initial
			v.BoottimeNS = uint64(^uint64(0))
			_, e := newAppendCoordinator(v, 0, j, keys, a, clock, time.Second)
			return e
		}},
		{"negative bytes", func() error { _, e := newAppendCoordinator(initial, -1, j, keys, a, clock, time.Second); return e }},
		{"missing journal", func() error { _, e := newAppendCoordinator(initial, 0, nil, keys, a, clock, time.Second); return e }},
		{"missing keys", func() error { _, e := newAppendCoordinator(initial, 0, j, nil, a, clock, time.Second); return e }},
		{"missing anchor", func() error { _, e := newAppendCoordinator(initial, 0, j, keys, nil, clock, time.Second); return e }},
		{"missing clock", func() error { _, e := newAppendCoordinator(initial, 0, j, keys, a, nil, time.Second); return e }},
		{"zero timeout", func() error { _, e := newAppendCoordinator(initial, 0, j, keys, a, clock, 0); return e }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.create(); !errors.Is(err, ErrCoordinatorRejected) {
				t.Fatalf("constructor error = %v", err)
			}
		})
	}
	if j.calls != 0 || a.tuple.Sequence != 0 {
		t.Fatal("invalid constructor called an adapter")
	}
	if _, err := NewAppendCoordinator(initial, 0, j, keys, a, clock); err != nil {
		t.Fatalf("valid public constructor: %v", err)
	}
}

func TestAppendCoordinatorMissingKeyAndEncodingFailureDoNotMutate(t *testing.T) {
	c, j, a, keys, _, sample := newAppendTest(t)
	keys.ok = false
	if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorRejected) {
		t.Fatalf("missing key error = %v", err)
	}
	keys.ok = true
	r := permitRecord()
	r.Type = Issued
	if err := c.Append(r, sample); !errors.Is(err, ErrCoordinatorRejected) {
		t.Fatalf("encoding error = %v", err)
	}
	if j.calls != 0 || len(j.data) != 0 || a.tuple.Sequence != 0 {
		t.Fatal("pre-write failure mutated adapters")
	}
}

func TestAppendCoordinatorNotWrittenPreservesBytesAndAllowsNextAttempt(t *testing.T) {
	c, j, a, _, _, sample := newAppendTest(t)
	j.outcome, j.writeErr = JournalNotWritten, errors.New("private adapter error")
	before := append([]byte(nil), j.data...)
	if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrJournalNotWritten) {
		t.Fatalf("Append error = %v", err)
	}
	if !bytes.Equal(j.data, before) || a.tuple.Sequence != 0 || c.breaker.Load() {
		t.Fatalf("NotWritten did not preserve state")
	}
	j.outcome, j.writeErr = JournalDurable, nil
	if err := c.Append(permitRecord(), sample); err != nil {
		t.Fatalf("new operation after proven NotWritten: %v", err)
	}
	if bytes.Equal(j.data, before) || a.tuple.Sequence != 1 {
		t.Fatal("valid new operation after proven NotWritten did not commit")
	}
}

func TestAppendCoordinatorPostAppendAnchorFailuresTripBreaker(t *testing.T) {
	for _, outcome := range []AnchorAdvance{AnchorConflict, AnchorUnavailable, AnchorAmbiguous} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			c, j, a, _, _, sample := newAppendTest(t)
			a.advance = outcome
			if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
				t.Fatalf("Append error = %v", err)
			}
			if len(j.data) == 0 || !c.breaker.Load() || c.head.Sequence != 0 {
				t.Fatalf("post-append uncertainty not quarantined")
			}
			if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
				t.Fatalf("second append error = %v", err)
			}
			if len(j.data) == 0 {
				t.Fatal("first append absent")
			}
		})
	}
}

func TestAppendCoordinatorMalformedJournalResultsTripBreaker(t *testing.T) {
	wrongOffset := int64(1)
	startingOffset := int64(0)
	for _, tc := range []struct {
		name    string
		outcome JournalWrite
		err     error
		offset  *int64
	}{
		{"not-written unproven", JournalNotWritten, nil, nil},
		{"durable error", JournalDurable, errors.New("private"), nil},
		{"durable wrong offset", JournalDurable, nil, &wrongOffset},
		{"durable starting offset", JournalDurable, nil, &startingOffset},
		{"unknown", JournalWrite(99), nil, nil},
		{"uncertain", JournalUncertain, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, j, _, _, _, sample := newAppendTest(t)
			j.outcome, j.writeErr, j.offsetOverride = tc.outcome, tc.err, tc.offset
			if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
				t.Fatalf("append error = %v", err)
			}
			if !c.breaker.Load() {
				t.Fatal("malformed journal result did not trip breaker")
			}
		})
	}
}

func TestAppendCoordinatorAnchorMismatchQuarantines(t *testing.T) {
	c, j, a, _, _, sample := newAppendTest(t)
	a.tuple.TrustedTimeHighWaterNS++
	if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("Append error = %v", err)
	}
	a.tuple = c.head
	if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("retry error = %v", err)
	}
	if len(j.data) != 0 {
		t.Fatal("anchor mismatch appended bytes")
	}
}

func TestAppendCoordinatorAdapterPanicsAreBounded(t *testing.T) {
	for _, location := range []string{"journal", "read", "cas", "key"} {
		t.Run(location, func(t *testing.T) {
			c, j, a, keys, _, sample := newAppendTest(t)
			switch location {
			case "journal":
				j.panicWrite = true
			case "read":
				a.readPanic = true
			case "cas":
				a.casPanic = true
			case "key":
				keys.panicLookup = true
			}
			err := c.Append(permitRecord(), sample)
			if err == nil {
				t.Fatal("expected bounded failure")
			}
			if bytes.Contains([]byte(err.Error()), []byte("secret sentinel")) {
				t.Fatalf("raw panic leaked: %v", err)
			}
			if location == "journal" || location == "cas" {
				if !c.breaker.Load() {
					t.Fatal("post-mutation panic did not trip breaker")
				}
			}
			if location == "read" && len(j.data) != 0 {
				t.Fatal("read panic appended")
			}
			if location == "read" || location == "key" {
				if !c.breaker.Load() {
					t.Fatal("pre-mutation adapter panic did not quarantine coordinator")
				}
				if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
					t.Fatalf("retry after panic = %v", err)
				}
			}
		})
	}
}

func TestAppendCoordinatorDeadlineWhileBlockedTripsBreaker(t *testing.T) {
	c, j, _, _, clock, sample := newAppendTest(t)
	j.entered, j.release = make(chan struct{}, 1), make(chan struct{})
	release := releaseOnce(t, j.release)
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { done <- c.Append(permitRecord(), sample) }()
	waitSignal(t, j.entered, "journal adapter entry")
	clock.Advance(10 * time.Second)
	release()
	if err := waitResult(t, done, "blocked Append"); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("late append error = %v", err)
	}
	if !c.breaker.Load() {
		t.Fatal("deadline did not trip breaker")
	}
}

func TestAppendCoordinatorDeadlineAroundReadAndAnchorCASTrips(t *testing.T) {
	t.Run("read timeout", func(t *testing.T) {
		c, j, a, _, clock, sample := newAppendTest(t)
		a.onRead = func() { clock.Advance(10 * time.Second) }
		if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
			t.Fatalf("append error = %v", err)
		}
		if j.calls != 0 || a.tuple.Sequence != 0 || !c.breaker.Load() {
			t.Fatal("read timeout mutated state")
		}
	})
	t.Run("anchor completion timeout", func(t *testing.T) {
		c, j, a, _, clock, sample := newAppendTest(t)
		a.onCAS = func() { clock.Advance(10 * time.Second) }
		if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
			t.Fatalf("append error = %v", err)
		}
		if len(j.data) == 0 || a.tuple.Sequence != 1 || c.head.Sequence != 0 || !c.breaker.Load() {
			t.Fatal("late anchor completion was published or acknowledged")
		}
	})
	t.Run("late timer callback", func(t *testing.T) {
		c, j, _, _, clock, sample := newAppendTest(t)
		j.onWrite = func() { clock.JumpWithoutFiring(10 * time.Second) }
		if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
			t.Fatalf("append error = %v", err)
		}
		if len(j.data) == 0 || !c.breaker.Load() {
			t.Fatal("deadline check failed when timer delivery was delayed")
		}
	})
}

func TestAppendCoordinatorDeadlineBeforeMutationDoesNotAppend(t *testing.T) {
	c, j, a, keys, clock, sample := newAppendTest(t)
	keys.onLookup = func() { clock.JumpWithoutFiring(10 * time.Second) }
	if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("deadline error = %v", err)
	}
	if j.calls != 0 || len(j.data) != 0 || a.tuple.Sequence != 0 || !c.breaker.Load() {
		t.Fatal("expired pre-write transaction mutated journal or anchor")
	}
}

func TestAppendCoordinatorSerializesAndIncludesLockWaitInDeadline(t *testing.T) {
	c, j, _, _, clock, sample := newAppendTest(t)
	j.entered, j.release = make(chan struct{}, 2), make(chan struct{})
	release := releaseOnce(t, j.release)
	t.Cleanup(release)
	first, second, secondStarted := make(chan error, 1), make(chan error, 1), make(chan struct{})
	go func() { first <- c.Append(permitRecord(), sample) }()
	waitSignal(t, j.entered, "first journal adapter entry")
	go func() { close(secondStarted); second <- c.Append(permitRecord(), sample) }()
	waitSignal(t, secondStarted, "second append start")
	deadline := time.Now().Add(time.Second)
	for {
		clock.mu.Lock()
		timerCount := len(clock.timers)
		clock.mu.Unlock()
		if timerCount == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second transaction did not begin deadline before lock wait")
		}
		time.Sleep(time.Millisecond)
	}
	j.mu.Lock()
	calls := j.calls
	j.mu.Unlock()
	if calls != 1 {
		t.Fatalf("second operation penetrated blocked adapter: calls=%d", calls)
	}
	clock.Advance(10 * time.Second)
	release()
	if err := waitResult(t, first, "first serialized Append"); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("first error = %v", err)
	}
	if err := waitResult(t, second, "second serialized Append"); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("second error = %v", err)
	}
	if j.calls != 1 {
		t.Fatalf("journal calls = %d, want 1", j.calls)
	}
}

func TestAppendCoordinatorTransientReadUnavailableCanRetry(t *testing.T) {
	c, j, a, _, _, sample := newAppendTest(t)
	a.readErr = errors.New("temporary")
	if err := c.Append(permitRecord(), sample); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("first error = %v", err)
	}
	if len(j.data) != 0 || c.breaker.Load() {
		t.Fatal("transient read failure mutated or quarantined")
	}
	a.readErr = nil
	if err := c.Append(permitRecord(), sample); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestAppendCoordinatorDoesNotRevokeAckableForLateResponse(t *testing.T) {
	c, _, _, _, clock, sample := newAppendTest(t)
	if err := c.Append(permitRecord(), sample); err != nil {
		t.Fatalf("append: %v", err)
	}
	clock.Advance(11 * time.Second) // response delivery occurs after ACKABLE won
	if c.breaker.Load() {
		t.Fatal("late response revoked an already durable ACKABLE result")
	}
}

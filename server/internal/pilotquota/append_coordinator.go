package pilotquota

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const quotaTransactionDeadline = 10 * time.Second

var (
	ErrCoordinatorUnavailable = errors.New("quota coordinator unavailable")
	ErrCoordinatorRejected    = errors.New("quota transaction rejected")
	ErrJournalNotWritten      = errors.New("quota journal append not written")
	errAdapterPanic           = errors.New("quota adapter panic")
)

type AnchorTuple struct {
	Sequence               uint64
	Digest                 [32]byte
	RestoreGeneration      uint64
	TrustedTimeHighWaterNS uint64
	BootID                 [16]byte
	BoottimeNS             uint64
}

type AnchorTimeSample struct {
	TrustedTimeHighWaterNS uint64
	BootID                 [16]byte
	BoottimeNS             uint64
}

type AnchorReadResult struct {
	Tuple       AnchorTuple
	Unavailable bool
}

type AnchorAdvance uint8

const (
	AnchorAdvanced AnchorAdvance = iota + 1
	AnchorConflict
	AnchorUnavailable
	AnchorAmbiguous
)

type JournalWrite uint8

const (
	JournalNotWritten JournalWrite = iota + 1
	JournalDurable
	JournalUncertain
)

type JournalAppendResult struct {
	Outcome JournalWrite
	Offset  int64
	Err     error
}

type DurableJournal interface {
	AppendAndSync(frame []byte) JournalAppendResult
}

type JournalKeyProvider interface {
	KeyFor(keyID [16]byte) ([]byte, bool)
}

type MonotonicAnchor interface {
	Read() (AnchorReadResult, error)
	CompareAndAdvance(expected, next AnchorTuple) (AnchorAdvance, error)
}

type DeadlineTimer interface{ Stop() bool }

type DeadlineClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) DeadlineTimer
}

type transactionState uint32

const (
	transactionCommitting transactionState = iota
	transactionWriteAuthorized
	transactionAckable
	transactionAborted
	transactionUncertain
)

type transaction struct {
	state    atomic.Uint32
	done     chan struct{}
	deadline time.Time
}

func newTransaction(deadline time.Time) *transaction {
	t := &transaction{done: make(chan struct{}), deadline: deadline}
	t.state.Store(uint32(transactionCommitting))
	return t
}

func (t *transaction) finish(from, to transactionState) bool {
	if !t.state.CompareAndSwap(uint32(from), uint32(to)) {
		return false
	}
	close(t.done)
	return true
}

type AppendCoordinator struct {
	journal DurableJournal
	keys    JournalKeyProvider
	anchor  MonotonicAnchor
	clock   DeadlineClock
	timeout time.Duration
	serial  chan struct{}
	breaker atomic.Bool
	arbiter sync.Mutex // serializes breaker transitions against ACKABLE publication
	stateMu sync.Mutex // protects head and byte count; serial ownership is required
	head    AnchorTuple
	bytes   int64
}

// NewAppendCoordinator creates an injected append core. The caller must supply
// a CLOCK_BOOTTIME-backed clock and state established by separately reviewed
// startup reconciliation. No production clock, journal, or anchor is provided.
func NewAppendCoordinator(initial AnchorTuple, journalBytes int64, journal DurableJournal, keys JournalKeyProvider, anchor MonotonicAnchor, clock DeadlineClock) (*AppendCoordinator, error) {
	return newAppendCoordinator(initial, journalBytes, journal, keys, anchor, clock, quotaTransactionDeadline)
}

func newAppendCoordinator(initial AnchorTuple, journalBytes int64, journal DurableJournal, keys JournalKeyProvider, anchor MonotonicAnchor, clock DeadlineClock, timeout time.Duration) (*AppendCoordinator, error) {
	if !validAnchorTuple(initial) || journalBytes < 0 || journalBytes > maxJournalBytes || journal == nil || keys == nil || anchor == nil || clock == nil || timeout <= 0 {
		return nil, ErrCoordinatorRejected
	}
	return &AppendCoordinator{journal: journal, keys: keys, anchor: anchor, clock: clock, timeout: timeout, serial: make(chan struct{}, 1), head: initial, bytes: journalBytes}, nil
}

// Append commits one already-authorized typed record. The sample is acquired
// by the caller once for this transaction; trusted clock acquisition and all
// product admission policy remain outside this component.
func (c *AppendCoordinator) Append(record Record, sample AnchorTimeSample) error {
	if c.breaker.Load() {
		return ErrCoordinatorUnavailable
	}
	started := c.clock.Now()
	deadline := started.Add(c.timeout)
	tx := newTransaction(deadline)
	timer := c.clock.AfterFunc(c.timeout, func() {
		c.trip(tx)
	})
	defer timer.Stop()

	select {
	case c.serial <- struct{}{}:
		defer func() { <-c.serial }()
	case <-tx.done:
		return ErrCoordinatorUnavailable
	}
	if !c.active(tx) {
		return ErrCoordinatorUnavailable
	}

	c.stateMu.Lock()
	expected := c.head
	journalBytes := c.bytes
	c.stateMu.Unlock()
	read, readErr := safeAnchorRead(c.anchor)
	if !c.active(tx) {
		return ErrCoordinatorUnavailable
	}
	if readErr != nil || read.Unavailable {
		if errors.Is(readErr, errAdapterPanic) {
			c.trip(tx)
			return ErrCoordinatorUnavailable
		}
		c.abort(tx)
		return ErrCoordinatorUnavailable
	}
	if !validAnchorTuple(read.Tuple) || read.Tuple != expected {
		c.trip(tx)
		return ErrCoordinatorUnavailable
	}
	if err := validateAppendInput(record, expected, sample); err != nil {
		c.abort(tx)
		return ErrCoordinatorRejected
	}
	key, ok, keyPanic := safeJournalKey(c.keys, record.KeyID)
	if keyPanic {
		c.trip(tx)
		return ErrCoordinatorUnavailable
	}
	if !ok || len(key) < 32 {
		c.abort(tx)
		zero(key)
		return ErrCoordinatorRejected
	}
	frame, err := EncodeFrame(record, key)
	zero(key)
	if err != nil || len(frame) > maxFrameSize || journalBytes > maxJournalBytes-int64(len(frame)) || expected.Sequence >= maxJournalRows {
		c.abort(tx)
		return ErrCoordinatorRejected
	}
	if !c.authorizeWrite(tx, deadline) {
		return ErrCoordinatorUnavailable
	}
	write := safeAppend(c.journal, frame)
	if !c.active(tx) {
		return ErrCoordinatorUnavailable
	}
	switch write.Outcome {
	case JournalNotWritten:
		if write.Err == nil || write.Offset != -1 {
			c.trip(tx)
			return ErrCoordinatorUnavailable
		}
		c.abort(tx)
		return ErrJournalNotWritten
	case JournalDurable:
		if write.Err != nil || write.Offset != journalBytes+int64(len(frame)) {
			c.trip(tx)
			return ErrCoordinatorUnavailable
		}
	case JournalUncertain:
		c.trip(tx)
		return ErrCoordinatorUnavailable
	default:
		c.trip(tx)
		return ErrCoordinatorUnavailable
	}
	if !c.active(tx) {
		return ErrCoordinatorUnavailable
	}

	next := expected
	next.Sequence++
	next.Digest = FrameDigest(frame)
	next.TrustedTimeHighWaterNS = sample.TrustedTimeHighWaterNS
	next.BootID = sample.BootID
	next.BoottimeNS = sample.BoottimeNS
	advance, advanceErr := safeAnchorAdvance(c.anchor, expected, next)
	if !c.active(tx) {
		return ErrCoordinatorUnavailable
	}
	if advanceErr != nil || advance != AnchorAdvanced {
		c.trip(tx)
		return ErrCoordinatorUnavailable
	}
	if !c.publishAckable(tx, deadline, next, int64(len(frame))) {
		return ErrCoordinatorUnavailable
	}
	return nil
}

func validateAppendInput(r Record, current AnchorTuple, sample AnchorTimeSample) error {
	if current.Sequence == math.MaxUint64 || r.Sequence != current.Sequence+1 || r.Previous != current.Digest ||
		r.RecordTimeNS != sample.TrustedTimeHighWaterNS ||
		sample.TrustedTimeHighWaterNS < current.TrustedTimeHighWaterNS || sample.BootID == ([16]byte{}) ||
		sample.BoottimeNS > math.MaxInt64 || sample.TrustedTimeHighWaterNS > math.MaxInt64 {
		return ErrCoordinatorRejected
	}
	if sample.BootID == current.BootID && sample.BoottimeNS < current.BoottimeNS {
		return ErrCoordinatorRejected
	}
	return nil
}

func validAnchorTuple(v AnchorTuple) bool {
	return v.RestoreGeneration != 0 && v.BootID != ([16]byte{}) && v.TrustedTimeHighWaterNS <= math.MaxInt64 && v.BoottimeNS <= math.MaxInt64
}

func (c *AppendCoordinator) beforeDeadline(deadline time.Time) bool {
	return c.clock.Now().Before(deadline)
}

func (c *AppendCoordinator) abort(tx *transaction) {
	c.arbiter.Lock()
	defer c.arbiter.Unlock()
	state := transactionState(tx.state.Load())
	if state != transactionCommitting && state != transactionWriteAuthorized {
		if state == transactionUncertain {
			c.breaker.Store(true)
		}
		return
	}
	if c.breaker.Load() || !c.beforeDeadline(tx.deadline) {
		if tx.finish(state, transactionUncertain) {
			c.breaker.Store(true)
		}
		return
	}
	// All transaction transitions are serialized by arbiter, so this CAS must
	// succeed after the state/deadline checks above.
	tx.finish(state, transactionAborted)
}

func (c *AppendCoordinator) trip(tx *transaction) {
	c.arbiter.Lock()
	defer c.arbiter.Unlock()
	state := transactionState(tx.state.Load())
	if state == transactionUncertain {
		c.breaker.Store(true)
		return
	}
	if state != transactionCommitting && state != transactionWriteAuthorized {
		return
	}
	if tx.finish(state, transactionUncertain) {
		c.breaker.Store(true)
		return
	}
	// A prior ACKABLE is terminal and remains successful even if the response
	// path encounters a late condition after the durable decision.
	// A clean ABORTED or successful ACKABLE operation is already terminal.
}

func (c *AppendCoordinator) active(tx *transaction) bool {
	c.arbiter.Lock()
	defer c.arbiter.Unlock()
	state := transactionState(tx.state.Load())
	if c.breaker.Load() || (state != transactionCommitting && state != transactionWriteAuthorized) || !c.beforeDeadline(tx.deadline) {
		if (state == transactionCommitting || state == transactionWriteAuthorized) && tx.finish(state, transactionUncertain) {
			c.breaker.Store(true)
		}
		return false
	}
	return true
}

// authorizeWrite is the mutation boundary. If timeout wins the arbiter first,
// the journal adapter is never called. If authorization wins first, a later
// timeout marks the operation uncertain because bytes may be entering storage.
func (c *AppendCoordinator) authorizeWrite(tx *transaction, deadline time.Time) bool {
	c.arbiter.Lock()
	defer c.arbiter.Unlock()
	if c.breaker.Load() || transactionState(tx.state.Load()) != transactionCommitting || !c.beforeDeadline(deadline) {
		if tx.finish(transactionCommitting, transactionUncertain) {
			c.breaker.Store(true)
		}
		return false
	}
	return tx.state.CompareAndSwap(uint32(transactionCommitting), uint32(transactionWriteAuthorized))
}

func (c *AppendCoordinator) publishAckable(tx *transaction, deadline time.Time, next AnchorTuple, frameLength int64) bool {
	c.arbiter.Lock()
	defer c.arbiter.Unlock()
	if c.breaker.Load() || transactionState(tx.state.Load()) != transactionWriteAuthorized || !c.beforeDeadline(deadline) {
		if tx.finish(transactionWriteAuthorized, transactionUncertain) {
			c.breaker.Store(true)
		}
		return false
	}
	if !tx.finish(transactionWriteAuthorized, transactionAckable) {
		return false
	}
	c.stateMu.Lock()
	c.head = next
	c.bytes += frameLength
	c.stateMu.Unlock()
	return true
}

func safeAnchorRead(a MonotonicAnchor) (result AnchorReadResult, err error) {
	defer func() {
		if recover() != nil {
			result, err = AnchorReadResult{}, errAdapterPanic
		}
	}()
	return a.Read()
}

func safeJournalKey(k JournalKeyProvider, id [16]byte) (key []byte, ok, panicked bool) {
	defer func() {
		if recover() != nil {
			zero(key)
			key, ok, panicked = nil, false, true
		}
	}()
	key, ok = k.KeyFor(id)
	return key, ok, false
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func safeAnchorAdvance(a MonotonicAnchor, expected, next AnchorTuple) (outcome AnchorAdvance, err error) {
	defer func() {
		if recover() != nil {
			outcome, err = AnchorAmbiguous, ErrCoordinatorUnavailable
		}
	}()
	return a.CompareAndAdvance(expected, next)
}

func safeAppend(j DurableJournal, frame []byte) (result JournalAppendResult) {
	defer func() {
		if recover() != nil {
			result = JournalAppendResult{Outcome: JournalUncertain, Offset: -1, Err: ErrCoordinatorUnavailable}
		}
	}()
	return j.AppendAndSync(append([]byte(nil), frame...))
}

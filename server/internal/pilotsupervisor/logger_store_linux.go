//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	hostLoggerGlobalRate    = 1600.0
	hostLoggerGlobalBurst   = 3200.0
	hostLoggerRunRate       = 100.0
	hostLoggerRunBurst      = 200.0
	hostLoggerMaxRunBuckets = 16
	hostLoggerMaxDedupKeys  = 65536
	hostLoggerQueueRunBytes = 4 << 20
	hostLoggerQueueBytes    = 64 << 20
	hostLoggerQueueJobs     = 4096
	hostLoggerQueueWaiters  = 16
	hostLoggerStoreBytes    = 256 << 20
	hostLoggerStoreRecords  = 1000000
)

var errHostLoggerStore = errors.New("host logger store unavailable")
var errHostLoggerRestartRequired = errors.New("host logger restart required")

// All hooks are private, fixed at construction or installed by package tests
// before admission. Hooks must not call back into the store. File hooks execute
// without mu; send is nonblocking and executes under mu.
type hostLoggerStoreHooks struct {
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time
	write  func([]byte) (int, error)
	sync   func() error
	unlock func() error
	close  func() error
	wait   func(context.Context, int, int16, time.Time) error
	send   func(int, []byte) (int, error)
}

type hostLoggerJob struct {
	run, digest string
	key         hostLoggerIndexKey
	dedup       bool
	line        []byte
	charge      int
	waiters     int
	inflight    bool
	finished    bool
	err         error
	done        chan struct{}
	reserved    bool
}

type hostLoggerWaiter struct {
	run      string
	expected hostLoggerACK
	job      *hostLoggerJob
	charge   int // additional waiter or already-durable replay frame
	rejected bool
	released bool
}

type hostLoggerIndexKey struct {
	operationID string
	event       string
}

type hostLoggerRateBucket struct {
	tokens float64
	last   time.Time
}

type hostLoggerRunContext struct {
	peer   hostLoggerPeerIdentity
	active bool
}

// hostLoggerStore receives a pre-opened, protected regular JSONL file. The
// launcher owns path selection and permissions; Reaper receives only a socket.
// One store instance holds an exclusive flock and serializes append+sync+ACK.
type hostLoggerStore struct {
	mu                                         sync.Mutex
	file                                       *os.File
	fd                                         int
	index                                      map[hostLoggerIndexKey]string
	global                                     hostLoggerRateBucket
	perRun                                     map[string]hostLoggerRateBucket
	contexts                                   map[string]*hostLoggerRunContext
	closed                                     bool
	poisoned                                   bool
	closing                                    bool
	hooks                                      hostLoggerStoreHooks
	queues                                     map[string][]*hostLoggerJob
	order                                      []string
	nextRun                                    int
	pending                                    map[hostLoggerIndexKey]*hostLoggerJob
	runBytes                                   map[string]int
	bytes, jobs, waiters, handlers             int
	maxRunBytes, maxBytes, maxJobs, maxWaiters int
	wake, changed, writerDone, closeDone       chan struct{}
	closeStarted, cleanupStarted, closeExpired bool
	closeErr                                   error
	// Fixed-cardinality diagnostic counters, guarded by mu.
	diagnostics map[string]uint64
	// Includes durable lines and all reserved unique appends. An ambiguous
	// in-flight failure retains its reservation until strict startup recovery.
	storeBytes, storeRecords       int64
	maxStoreBytes, maxStoreRecords int64
}

func openHostLoggerStore(file *os.File) (*hostLoggerStore, error) {
	return openHostLoggerStoreLimits(file, hostLoggerStoreBytes, hostLoggerStoreRecords)
}

// Package-private reduced limits exercise exactly the production admission and
// recovery path; callers cannot raise either hard limit.
func openHostLoggerStoreLimits(file *os.File, maxBytes, maxRecords int64) (*hostLoggerStore, error) {
	if maxBytes <= 0 || maxBytes > hostLoggerStoreBytes || maxRecords <= 0 || maxRecords > hostLoggerStoreRecords {
		return nil, errHostLoggerStore
	}
	if file == nil || file.Fd() == ^uintptr(0) {
		return nil, errHostLoggerStore
	}
	fd := int(file.Fd())
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE == unix.O_RDONLY || flags&unix.O_APPEND == 0 {
		return nil, errHostLoggerStore
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return nil, errHostLoggerStore
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG ||
		st.Mode&0o777 != 0o600 || st.Uid != uint32(unix.Geteuid()) || st.Gid != uint32(unix.Getegid()) {
		return nil, errHostLoggerStore
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errHostLoggerStore
	}
	store := &hostLoggerStore{
		file: file, fd: fd, index: make(map[hostLoggerIndexKey]string),
		perRun: make(map[string]hostLoggerRateBucket), contexts: make(map[string]*hostLoggerRunContext),
		queues: make(map[string][]*hostLoggerJob), pending: make(map[hostLoggerIndexKey]*hostLoggerJob),
		runBytes: make(map[string]int), diagnostics: make(map[string]uint64),
		maxRunBytes: hostLoggerQueueRunBytes, maxBytes: hostLoggerQueueBytes,
		maxJobs: hostLoggerQueueJobs, maxWaiters: hostLoggerQueueWaiters,
		wake: make(chan struct{}, 1), changed: make(chan struct{}),
		writerDone: make(chan struct{}), closeDone: make(chan struct{}),
		maxStoreBytes: maxBytes, maxStoreRecords: maxRecords,
	}
	store.hooks = hostLoggerStoreHooks{
		now: time.Now, after: time.After, write: file.Write, sync: file.Sync,
		unlock: func() error { return unix.Flock(fd, unix.LOCK_UN) }, close: file.Close,
		wait: loggerWait,
		send: func(fd int, ack []byte) (int, error) {
			return unix.SendmsgN(fd, ack, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		},
	}
	if err := store.rebuildIndex(); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return nil, errHostLoggerStore
	}
	store.diagnostics["logger_restart_replay_loaded"] = uint64(len(store.index))
	go store.writer()
	return store, nil
}

// RegisterRunContext binds a live-run rate bucket to trusted launcher process
// credentials and the opaque process_id recorded in events. Context IDs are
// generated outside Gateway IPC and limited to 16 concurrent registrations.
func (s *hostLoggerStore) RegisterRunContext(contextID string, peer hostLoggerPeerIdentity) error {
	if s == nil || !hostLogHex32.MatchString(contextID) || peer.PID <= 0 ||
		!hostLogHex32.MatchString(peer.EventProcessID) {
		return errHostLoggerStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing || s.poisoned {
		return errHostLoggerStore
	}
	if _, ok := s.contexts[contextID]; ok {
		return errHostLoggerStore
	}
	if len(s.contexts) >= hostLoggerMaxRunBuckets {
		return errHostLoggerStore
	}
	s.contexts[contextID] = &hostLoggerRunContext{peer: peer}
	s.order = append(s.order, contextID)
	s.perRun[contextID] = hostLoggerRateBucket{tokens: hostLoggerRunBurst, last: s.hooks.now()}
	return nil
}

// ReleaseRunContext recycles the bounded bucket after the trusted launcher
// closes the run's logger endpoint and guarantees no packet handler remains.
func (s *hostLoggerStore) ReleaseRunContext(contextID string, peer hostLoggerPeerIdentity) error {
	if s == nil {
		return errHostLoggerStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	registered, ok := s.contexts[contextID]
	if s.closed || s.closing || s.poisoned || !ok || registered.peer != peer || registered.active || s.runBytes[contextID] != 0 {
		return errHostLoggerStore
	}
	delete(s.contexts, contextID)
	delete(s.perRun, contextID)
	delete(s.runBytes, contextID)
	delete(s.queues, contextID)
	for i, id := range s.order {
		if id == contextID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			s.nextRun = 0
			break
		}
	}
	return nil
}

func (s *hostLoggerStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	timer := s.hooks.after(hostLoggerACKTimeout)
	if !s.closeStarted {
		s.closeStarted, s.closing = true, true
		s.signalLocked()
		go s.cleanup()
	}
	done := s.closeDone
	s.mu.Unlock()
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	case <-timer:
		s.mu.Lock()
		defer s.mu.Unlock()
		select {
		case <-done:
			return s.closeErr
		default:
		}
		if !s.closeExpired {
			s.closeExpired = true
			s.diagnostics["logger_shutdown_restart_required"]++
			s.signalLocked()
		}
		return errHostLoggerRestartRequired
	}
}

func (s *hostLoggerStore) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// There is exactly one coordinator and one OS cleanup sequence for this store.
// A caller timeout before drain permanently forbids initiating OS cleanup.
func (s *hostLoggerStore) cleanup() {
	for {
		s.mu.Lock()
		writerStopped := false
		select {
		case <-s.writerDone:
			writerStopped = true
		default:
		}
		if s.closeExpired && !s.cleanupStarted {
			s.closeErr = errHostLoggerRestartRequired
			close(s.closeDone)
			s.mu.Unlock()
			return
		}
		if writerStopped && s.handlers == 0 {
			s.cleanupStarted = true
			unlock, closeFile := s.hooks.unlock, s.hooks.close
			s.mu.Unlock()
			err := unlock()
			if err == nil {
				err = closeFile()
			}
			s.mu.Lock()
			if err != nil {
				s.closeErr = errHostLoggerRestartRequired
				s.diagnostics["logger_shutdown_restart_required"]++
			} else {
				s.closed = true
				if s.closeExpired {
					s.closeErr = errHostLoggerRestartRequired
				}
			}
			close(s.closeDone)
			s.mu.Unlock()
			return
		}
		changed := s.changed
		s.mu.Unlock()
		<-changed
	}
}

func (s *hostLoggerStore) rebuildIndex() error {
	st, err := s.file.Stat()
	if err != nil || st.Size() > s.maxStoreBytes {
		return errHostLoggerStore
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return errHostLoggerStore
	}
	r := bufio.NewReaderSize(s.file, hostLoggerMaxRecord+2)
	for {
		line, err := r.ReadSlice('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err == bufio.ErrBufferFull || len(line) > hostLoggerMaxRecord+1 || err != nil && err != io.EOF ||
			len(line) < 2 || line[len(line)-1] != '\n' || err == io.EOF {
			return errHostLoggerStore
		}
		eventBytes := line[:len(line)-1]
		s.storeBytes += int64(len(line))
		s.storeRecords++
		if s.storeBytes > s.maxStoreBytes || s.storeRecords > s.maxStoreRecords {
			return errHostLoggerStore
		}
		var event hostReaperEvent
		if decodeCanonicalJSON(eventBytes, &event) != nil || validateHostReaperEvent(event) != nil {
			return errHostLoggerStore
		}
		if isHostLoggerDeduplicatedEvent(event.Event) {
			key := hostLoggerIndexKey{operationID: event.OperationID, event: event.Event}
			digest := hostLogDigest(eventBytes)
			if _, exists := s.index[key]; exists {
				return errHostLoggerStore
			}
			if len(s.index) >= hostLoggerMaxDedupKeys {
				return errHostLoggerStore
			}
			s.index[key] = digest
		}
		if err == io.EOF {
			break
		}
	}
	_, err = s.file.Seek(0, io.SeekEnd)
	return err
}

// AcceptPacket returns one correlated ACK only after the event line is
// durably synchronized. For deduplicated events, identical replays are ACKed
// without appending; a conflicting digest is rejected without side effects.
func (s *hostLoggerStore) AcceptPacket(frame []byte, contextID string) ([]byte, error) {
	if s == nil {
		return nil, errHostLoggerStore
	}
	deadline, err := s.claimHandler(contextID)
	if err != nil {
		return nil, err
	}
	defer s.releaseHandler(contextID)
	waiter, err := s.admit(context.Background(), deadline, frame, contextID)
	if err != nil {
		return nil, err
	}
	defer s.releaseWaiter(waiter)
	return s.awaitACK(context.Background(), deadline, waiter)
}

func (s *hostLoggerStore) claimHandler(run string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registration := s.contexts[run]
	if registration == nil || registration.active || s.closed || s.closing || s.poisoned || s.handlers >= s.maxWaiters {
		return time.Time{}, errHostLoggerStore
	}
	deadline := s.hooks.now().Add(hostLoggerACKTimeout)
	registration.active = true
	s.handlers++
	return deadline, nil
}

func (s *hostLoggerStore) releaseHandler(run string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if registration := s.contexts[run]; registration != nil {
		registration.active = false
	}
	s.handlers--
	s.signalLocked()
}

// admission is the only allocator for pending jobs/waiters. Its caller holds a
// context handler lease until ACK transmission (or local failure) is finished.
func (s *hostLoggerStore) admit(ctx context.Context, deadline time.Time, frame []byte, run string) (*hostLoggerWaiter, error) {
	allowed, err := s.receiveFrameRate(run)
	if err != nil {
		return nil, err
	}
	return s.admitReceived(ctx, deadline, frame, run, allowed)
}

// Called exactly once after receiving a frame from a credential-checked
// context, including malformed/truncated frames, never on a receive timeout.
func (s *hostLoggerStore) receiveFrameRate(run string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contexts[run] == nil || s.closed || s.closing || s.poisoned {
		return false, errHostLoggerStore
	}
	return s.allowRate(run, s.hooks.now()), nil
}

func (s *hostLoggerStore) admitReceived(ctx context.Context, deadline time.Time, frame []byte, run string, rateAllowed bool) (*hostLoggerWaiter, error) {
	_, expected, err := decodeHostLoggerEnvelope(frame)
	if err != nil {
		return nil, errHostLoggerStore
	}
	eventBytes := extractEnvelopeEvent(frame)
	var event hostReaperEvent
	if len(eventBytes) == 0 || decodeCanonicalJSON(eventBytes, &event) != nil {
		return nil, errHostLoggerStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.deadlineErrorLocked(ctx, deadline); err != nil {
		return nil, err
	}
	registration := s.contexts[run]
	if registration == nil || event.ProcessID != registration.peer.EventProcessID || s.closed || s.closing || s.poisoned {
		return nil, errHostLoggerStore
	}
	key := hostLoggerIndexKey{operationID: event.OperationID, event: event.Event}
	dedup := isHostLoggerDeduplicatedEvent(event.Event)
	digest := hostLogDigest(eventBytes)
	var job *hostLoggerJob
	durable, rejected := false, false
	if dedup {
		if old, ok := s.index[key]; ok {
			durable, rejected = old == digest, old != digest
		} else if old := s.pending[key]; old != nil {
			if old.digest != digest {
				rejected = true
			} else {
				job = old
			}
		} else if len(s.index)+len(s.pending) >= hostLoggerMaxDedupKeys {
			rejected = true
		}
	}
	newJob := !durable && !rejected && job == nil
	if !rateAllowed {
		rejected, newJob, job = true, false, nil
	}
	if newJob && (int64(len(eventBytes)+1) > s.maxStoreBytes-s.storeBytes || s.storeRecords >= s.maxStoreRecords) {
		s.diagnostics["logger_store_full"]++
		rejected, newJob = true, false
	}
	if s.waiters >= s.maxWaiters || s.bytes+len(frame) > s.maxBytes || s.runBytes[run]+len(frame) > s.maxRunBytes || newJob && s.jobs >= s.maxJobs {
		s.diagnostics["logger_queue_rejected_full"]++
		return nil, errHostLoggerStore
	}
	waiter := &hostLoggerWaiter{run: run, expected: expected, rejected: rejected, charge: len(frame)}
	s.waiters++
	s.bytes += len(frame)
	s.runBytes[run] += len(frame)
	if newJob {
		job = &hostLoggerJob{run: run, key: key, dedup: dedup, digest: digest, charge: len(frame), done: make(chan struct{}), line: append(append([]byte(nil), eventBytes...), '\n')}
		job.reserved = true
		s.storeBytes += int64(len(job.line))
		s.storeRecords++
		waiter.charge = 0 // original frame charge belongs to the job until writer completion
		s.jobs++
		s.queues[run] = append(s.queues[run], job)
		if dedup {
			s.pending[key] = job
		}
	}
	waiter.job = job
	if job != nil {
		job.waiters++
	}
	s.diagnostics["logger_queue_admitted"]++
	s.signalLocked()
	return waiter, nil
}

func (s *hostLoggerStore) deadlineErrorLocked(ctx context.Context, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return errPeerCancelled
		}
		return errPeerTimeout
	}
	if !s.hooks.now().Before(deadline) {
		return errPeerTimeout
	}
	return nil
}

func (s *hostLoggerStore) waiterErrorLocked(ctx context.Context, deadline time.Time, w *hostLoggerWaiter) error {
	if err := s.deadlineErrorLocked(ctx, deadline); err != nil {
		if errors.Is(err, errPeerTimeout) && w.job != nil && w.job.inflight && !w.job.finished {
			s.diagnostics["logger_ack_deadline_exceeded"]++
			s.poisonLocked()
		}
		return err
	}
	if s.poisoned || s.closed || w.released {
		return errHostLoggerStore
	}
	if w.job != nil && w.job.finished {
		return w.job.err
	}
	return nil
}

func (s *hostLoggerStore) awaitACK(ctx context.Context, deadline time.Time, w *hostLoggerWaiter) ([]byte, error) {
	s.mu.Lock()
	timer := s.hooks.after(deadline.Sub(s.hooks.now()))
	s.mu.Unlock()
	for {
		s.mu.Lock()
		if err := s.waiterErrorLocked(ctx, deadline, w); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if w.job == nil || w.job.finished {
			status := "accepted"
			if w.rejected {
				status = "rejected"
			}
			ack, err := encodeStoreACK(w.expected, status)
			s.mu.Unlock()
			return ack, err
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		case <-timer:
		}
	}
}

func (s *hostLoggerStore) releaseWaiter(w *hostLoggerWaiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.released {
		return
	}
	w.released = true
	s.waiters--
	s.bytes -= w.charge
	s.runBytes[w.run] -= w.charge
	if j := w.job; j != nil {
		j.waiters--
		if j.waiters == 0 && !j.inflight && !j.finished {
			queue := s.queues[j.run]
			for i, item := range queue {
				if item == j {
					s.queues[j.run] = append(queue[:i], queue[i+1:]...)
					break
				}
			}
			s.finishJobLocked(j, errPeerCancelled)
		}
	}
	s.signalLocked()
}

func (s *hostLoggerStore) finishJobLocked(j *hostLoggerJob, err error) {
	if j.finished {
		return
	}
	j.finished, j.err = true, err
	if !j.inflight {
		s.releaseStoreReservationLocked(j)
	}
	s.bytes -= j.charge
	s.runBytes[j.run] -= j.charge
	s.jobs--
	if j.dedup {
		delete(s.pending, j.key)
	}
	close(j.done)
}

func (s *hostLoggerStore) releaseStoreReservationLocked(j *hostLoggerJob) {
	if j.reserved {
		j.reserved = false
		s.storeBytes -= int64(len(j.line))
		s.storeRecords--
	}
}

func (s *hostLoggerStore) poisonLocked() {
	if s.poisoned {
		return
	}
	s.poisoned = true
	s.diagnostics["logger_store_poisoned"]++
	for run, queue := range s.queues {
		for _, j := range queue {
			s.finishJobLocked(j, errHostLoggerStore)
		}
		delete(s.queues, run)
	}
	s.signalLocked()
}

func (s *hostLoggerStore) writer() {
	defer func() { s.mu.Lock(); close(s.writerDone); s.signalLocked(); s.mu.Unlock() }()
	for {
		s.mu.Lock()
		if s.poisoned || s.closing && s.jobs == 0 {
			s.mu.Unlock()
			return
		}
		var job *hostLoggerJob
		for n := 0; n < len(s.order); n++ {
			i := (s.nextRun + n) % len(s.order)
			run := s.order[i]
			if queue := s.queues[run]; len(queue) != 0 {
				job = queue[0]
				s.queues[run] = queue[1:]
				s.nextRun = (i + 1) % len(s.order)
				break
			}
		}
		if job == nil {
			s.mu.Unlock()
			<-s.wake
			continue
		}
		job.inflight = true
		write, syncFile := s.hooks.write, s.hooks.sync
		s.signalLocked()
		s.mu.Unlock()
		n, err := write(job.line)
		stage := "logger_append_failed"
		if err == nil && n != len(job.line) {
			err = io.ErrShortWrite
		}
		if err == nil {
			stage = "logger_sync_failed"
			err = syncFile()
		}
		s.mu.Lock()
		if err != nil {
			if stage == "logger_append_failed" && n == 0 {
				s.releaseStoreReservationLocked(job)
			}
			s.diagnostics[stage]++
			s.poisonLocked()
			s.finishJobLocked(job, errHostLoggerStore)
		} else {
			// Durable evidence is retained even when an earlier timeout poisoned the
			// process; such a result is never eligible for an ACK in this process.
			if job.dedup {
				s.index[job.key] = job.digest
			}
			if s.poisoned {
				s.finishJobLocked(job, errHostLoggerStore)
			} else {
				s.finishJobLocked(job, nil)
			}
		}
		s.signalLocked()
		s.mu.Unlock()
	}
}

func encodeStoreACK(expected hostLoggerACK, status string) ([]byte, error) {
	expected.Status = status
	return encodeHostLoggerACK(expected)
}

func extractEnvelopeEvent(frame []byte) []byte {
	var envelope hostLoggerEnvelope
	if decodeCanonicalJSON(frame, &envelope) != nil {
		return nil
	}
	return append([]byte(nil), envelope.Event...)
}

func isHostLoggerDeduplicatedEvent(event string) bool {
	return event == "run_root_dispatch_intent" || event == "bootstrap.finish"
}

func hostLogDigest(eventBytes []byte) string {
	return hex.EncodeToString(sha256Sum(eventBytes))
}

func (s *hostLoggerStore) allowRate(runID string, now time.Time) bool {
	bucket, known := s.perRun[runID]
	if !known && len(s.perRun) >= hostLoggerMaxRunBuckets {
		return false
	}
	globalAllowed := refillLoggerBucket(&s.global, now, hostLoggerGlobalRate, hostLoggerGlobalBurst)
	runAllowed := refillLoggerBucket(&bucket, now, hostLoggerRunRate, hostLoggerRunBurst)
	// Account independently: an exhausted bucket must not exempt this frame
	// from consuming a token available in the other bucket.
	if globalAllowed {
		s.global.tokens--
	}
	if runAllowed {
		bucket.tokens--
	}
	s.perRun[runID] = bucket
	return globalAllowed && runAllowed
}

func refillLoggerBucket(bucket *hostLoggerRateBucket, now time.Time, rate, burst float64) bool {
	if bucket.last.IsZero() {
		bucket.tokens = burst
		bucket.last = now
	}
	if now.Before(bucket.last) {
		return false
	}
	bucket.tokens += now.Sub(bucket.last).Seconds() * rate
	if bucket.tokens > burst {
		bucket.tokens = burst
	}
	bucket.last = now
	return bucket.tokens >= 1
}

// serveOnePacket pins the registered context through receive, durability and
// the atomic nonblocking ACK send. All phases share the claim-time deadline.
func (s *hostLoggerStore) serveOnePacket(ctx context.Context, fd int, contextID string) error {
	return s.serveOnePacketGuarded(ctx, fd, contextID, nil)
}

// serveOnePacketGuarded performs pidfd liveness checks at the packet-admission
// and ACK boundaries. The guard is a pure poll of the launcher's retained
// process handle; it must not call back into the store.
func (s *hostLoggerStore) serveOnePacketGuarded(ctx context.Context, fd int, contextID string, guard func() bool) error {
	if s == nil || ctx == nil {
		return errHostLoggerStore
	}
	deadline, err := s.claimHandler(contextID)
	if err != nil {
		return err
	}
	defer s.releaseHandler(contextID)
	s.mu.Lock()
	expected, wait, send := s.contexts[contextID].peer, s.hooks.wait, s.hooks.send
	s.mu.Unlock()
	if !hostLoggerPeerMatches(fd, expected) {
		return errHostLoggerStore
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return errHostLoggerStore
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return errHostLoggerStore
	}
	// A quiet classroom may have no events for minutes. The per-event ACK
	// deadline starts only after recvmsg has obtained a complete frame.
	if err := wait(ctx, fd, unix.POLLIN, time.Time{}); err != nil {
		return err
	}
	buf, oob := make([]byte, hostLoggerMaxRecord+1), make([]byte, 4096)
	n, oobn, flags, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	if err == nil && n == 0 && oobn == 0 && flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) == 0 {
		return errHostLoggerEOF
	}
	// Ancillary/truncated payloads are still received frames. Account before
	// rejecting them, while closing any received rights on every error path.
	rateAllowed := false
	var rateErr error
	if err == nil {
		rateAllowed, rateErr = s.receiveFrameRate(contextID)
	}
	receivedFDs, credentials, controlErr := inspectRecoveryControl(oob[:oobn])
	for _, receivedFD := range receivedFDs {
		_ = unix.Close(receivedFD)
	}
	expectedCredentials := &unix.Ucred{Pid: expected.PID, Uid: expected.UID, Gid: expected.GID}
	if controlErr != nil || flags&unix.MSG_CTRUNC != 0 || len(receivedFDs) != 0 ||
		!validRecoveryCredentialSet(credentials, expectedCredentials) {
		return errHostLoggerStore
	}
	if err != nil || rateErr != nil || n == 0 || flags&unix.MSG_TRUNC != 0 || n > hostLoggerMaxRecord {
		return errHostLoggerStore
	}
	deadline = s.hooks.now().Add(hostLoggerACKTimeout)
	if guard != nil && !guard() {
		return errPeerCancelled
	}
	w, err := s.admitReceived(ctx, deadline, buf[:n], contextID, rateAllowed)
	if err != nil {
		return err
	}
	defer s.releaseWaiter(w)
	ack, err := s.awaitACK(ctx, deadline, w)
	if err != nil {
		return err
	}
	if guard != nil && !guard() {
		return errPeerCancelled
	}
	for {
		if err = wait(ctx, fd, unix.POLLOUT, deadline); err != nil {
			return err
		}
		if guard != nil && !guard() {
			return errPeerCancelled
		}
		s.mu.Lock()
		if err = s.waiterErrorLocked(ctx, deadline, w); err != nil {
			s.mu.Unlock()
			return err
		}
		written, sendErr := send(fd, ack)
		s.mu.Unlock()
		if sendErr == unix.EINTR || sendErr == unix.EAGAIN || sendErr == unix.EWOULDBLOCK {
			continue
		}
		// Persistence is already known. A transport failure retires only this
		// channel through the caller/launcher, never poisons unrelated contexts.
		if sendErr != nil || written != len(ack) {
			return errHostLoggerStore
		}
		return nil
	}
}

func hostLoggerPeerMatches(fd int, expected hostLoggerPeerIdentity) bool {
	if expected.PID <= 0 {
		return false
	}
	typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_SEQPACKET {
		return false
	}
	peer, err := unix.Getpeername(fd)
	if err != nil {
		return false
	}
	if _, ok := peer.(*unix.SockaddrUnix); !ok {
		return false
	}
	credentials, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	return err == nil && credentials != nil && credentials.Pid == expected.PID &&
		credentials.Uid == expected.UID && credentials.Gid == expected.GID
}

//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Timers are driven only by Advance. Wall time below is a test-hang guard,
// never the oracle for a queue/deadline result.
type loggerQueueClock struct {
	mu         sync.Mutex
	now        time.Time
	timers     []loggerQueueTimer
	registered chan struct{}
}
type loggerQueueTimer struct {
	at time.Time
	ch chan time.Time
}

func newLoggerQueueClock() *loggerQueueClock {
	return &loggerQueueClock{now: time.Unix(1800000000, 0), registered: make(chan struct{}, 64)}
}
func (c *loggerQueueClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *loggerQueueClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.timers = append(c.timers, loggerQueueTimer{c.now.Add(d), ch})
	}
	c.registered <- struct{}{}
	return ch
}
func (c *loggerQueueClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.timers[:0]
	for _, timer := range c.timers {
		if !c.now.Before(timer.at) {
			timer.ch <- c.now
		} else {
			keep = append(keep, timer)
		}
	}
	c.timers = keep
}
func loggerQueueReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("queue fixture did not reach barrier")
	}
	var zero T
	return zero
}
func loggerQueueWait(t *testing.T, s *hostLoggerStore, predicate func() bool) {
	t.Helper()
	for {
		s.mu.Lock()
		ok, changed := predicate(), s.changed
		s.mu.Unlock()
		if ok {
			return
		}
		loggerQueueReceive(t, changed)
	}
}
func loggerQueueFrame(t *testing.T, operation int) ([]byte, hostLoggerACK) {
	t.Helper()
	event := testHostReaperEvent()
	event.OperationID = fmt.Sprintf("%032x", operation)
	frame, ack, err := encodeHostLoggerEnvelope(event)
	if err != nil {
		t.Fatal(err)
	}
	return frame, ack
}
func loggerQueueClockInstall(s *hostLoggerStore) *loggerQueueClock {
	c := newLoggerQueueClock()
	s.mu.Lock()
	s.hooks.now, s.hooks.after = c.Now, c.After
	for run, bucket := range s.perRun {
		bucket.last = c.Now()
		s.perRun[run] = bucket
	}
	s.global = hostLoggerRateBucket{tokens: hostLoggerGlobalBurst, last: c.Now()}
	s.mu.Unlock()
	return c
}
func loggerQueueAdmit(t *testing.T, s *hostLoggerStore, frame []byte, run string) *hostLoggerWaiter {
	t.Helper()
	w, err := s.admit(context.Background(), s.hooks.now().Add(hostLoggerACKTimeout), frame, run)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.releaseWaiter(w) })
	return w
}

func loggerQueueSetTokens(s *hostLoggerStore, clock *loggerQueueClock, global, run float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.global = hostLoggerRateBucket{tokens: global, last: clock.Now()}
	s.perRun[testLoggerContextID()] = hostLoggerRateBucket{tokens: run, last: clock.Now()}
}

func loggerQueueRejected(t *testing.T, s *hostLoggerStore, w *hostLoggerWaiter) {
	t.Helper()
	ack, err := s.awaitACK(context.Background(), s.hooks.now().Add(hostLoggerACKTimeout), w)
	var got hostLoggerACK
	if err != nil || decodeCanonicalJSON(ack, &got) != nil {
		t.Fatalf("rejected ACK: %v", err)
	}
	want := w.expected
	want.Status = "rejected"
	if got != want || w.job != nil {
		t.Fatal("rejection was uncorrelated or attached to a durability job")
	}
	s.releaseWaiter(w)
}

func TestLoggerQueueEveryReceivedFrameRateLimited(t *testing.T) {
	for _, kind := range []string{"durable", "pending", "conflict"} {
		for _, bucket := range []string{"global", "run"} {
			t.Run(kind+"/"+bucket, func(t *testing.T) {
				file, s := newTestHostLoggerStore(t)
				clock := loggerQueueClockInstall(s)
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				t.Cleanup(func() { once.Do(func() { close(release) }) })
				if kind == "pending" {
					s.mu.Lock()
					s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
					s.mu.Unlock()
				}
				frame, _ := loggerQueueFrame(t, 801)
				first := loggerQueueAdmit(t, s, frame, testLoggerContextID())
				if kind == "pending" {
					loggerQueueReceive(t, entered)
				} else {
					loggerQueueWait(t, s, func() bool { return first.job.finished })
					s.releaseWaiter(first)
				}
				if kind == "conflict" {
					event := testHostReaperEvent()
					event.OperationID = fmt.Sprintf("%032x", 801)
					event.TraceID = strings.Repeat("f", 32)
					var err error
					frame, _, err = encodeHostLoggerEnvelope(event)
					if err != nil {
						t.Fatal(err)
					}
				}
				global, run := 10.0, 10.0
				if bucket == "global" {
					global = 1
				} else {
					run = 1
				}
				loggerQueueSetTokens(s, clock, global, run)
				within := loggerQueueAdmit(t, s, frame, testLoggerContextID())
				if kind == "conflict" {
					loggerQueueRejected(t, s, within)
				} else if within.rejected {
					t.Fatal("first in-budget replay rejected")
				}
				over := loggerQueueAdmit(t, s, frame, testLoggerContextID())
				loggerQueueRejected(t, s, over)
				s.mu.Lock()
				wantGlobal, wantRun := global-2, run-2
				if global < 2 {
					wantGlobal = 0
				}
				if run < 2 {
					wantRun = 0
				}
				ok := !s.poisoned && s.storeRecords == 1 && s.global.tokens == wantGlobal &&
					s.perRun[testLoggerContextID()].tokens == wantRun
				s.mu.Unlock()
				if !ok {
					t.Fatal("replay/conflict bypassed token accounting or changed durable reservations")
				}
				once.Do(func() { close(release) })
				loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
				s.releaseWaiter(within)
				s.releaseWaiter(first)
				if bytes.Count(readLoggerFile(t, file), []byte{'\n'}) != 1 {
					t.Fatal("rate-limited duplicate appended")
				}
			})
		}
	}
}

func TestLoggerQueueRateBucketsDebitIndependently(t *testing.T) {
	for _, tc := range []struct {
		name                string
		global, run         float64
		wantGlobal, wantRun float64
	}{
		{name: "global_empty_run_available", global: 0, run: 2, wantGlobal: 0, wantRun: 1},
		{name: "run_empty_global_available", global: 2, run: 0, wantGlobal: 1, wantRun: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := newTestHostLoggerStore(t)
			clock := loggerQueueClockInstall(s)
			loggerQueueSetTokens(s, clock, tc.global, tc.run)
			s.mu.Lock()
			allowed := s.allowRate(testLoggerContextID(), clock.Now())
			s.mu.Unlock()
			if allowed {
				t.Fatal("frame passed with one empty bucket")
			}
			s.mu.Lock()
			global, run := s.global.tokens, s.perRun[testLoggerContextID()].tokens
			s.mu.Unlock()
			if global != tc.wantGlobal || run != tc.wantRun {
				t.Fatalf("independent debit got global=%v run=%v; want %v/%v", global, run, tc.wantGlobal, tc.wantRun)
			}
		})
	}
}

func TestLoggerQueueRateRejectedAtCapacityHasNoACKOrAllocation(t *testing.T) {
	for _, limit := range []string{"waiters", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			file, s := newTestHostLoggerStore(t)
			clock := loggerQueueClockInstall(s)
			frame, _ := loggerQueueFrame(t, 802)
			s.mu.Lock()
			s.global = hostLoggerRateBucket{tokens: 0, last: clock.Now()}
			s.perRun[testLoggerContextID()] = hostLoggerRateBucket{tokens: 2, last: clock.Now()}
			if limit == "waiters" {
				s.maxWaiters = 0
			} else {
				s.maxBytes = len(frame) - 1
			}
			allowed := s.allowRate(testLoggerContextID(), clock.Now())
			globalTokens := s.global.tokens
			runTokens := s.perRun[testLoggerContextID()].tokens
			s.mu.Unlock()
			if allowed || globalTokens != 0 || runTokens != 1 {
				t.Fatalf("rate accounting before capacity rejection: allowed=%v global=%v run=%v", allowed, globalTokens, runTokens)
			}
			w, err := s.admitReceived(context.Background(), clock.Now().Add(hostLoggerACKTimeout), frame, testLoggerContextID(), allowed)
			if err == nil || w != nil {
				t.Fatal("capacity-saturated rate rejection unexpectedly allocated an ACK waiter")
			}
			s.mu.Lock()
			unchanged := s.waiters == 0 && s.jobs == 0 && s.bytes == 0 && s.runBytes[testLoggerContextID()] == 0 &&
				s.storeRecords == 0 && s.storeBytes == 0 && len(s.index) == 0 && len(s.pending) == 0 && !s.poisoned
			diagnostics := s.diagnostics["logger_queue_rejected_full"]
			s.mu.Unlock()
			if !unchanged || diagnostics != 1 {
				t.Fatalf("capacity rejection allocated state or lacked diagnostic: unchanged=%v diagnostics=%d", unchanged, diagnostics)
			}
			if got := readLoggerFile(t, file); len(got) != 0 {
				t.Fatal("rate-rejected frame appended at full capacity")
			}
		})
	}
}

func TestLoggerQueueMalformedConsumesTokenSilentReceiveDoesNot(t *testing.T) {
	_, s := newTestHostLoggerStore(t)
	clock := loggerQueueClockInstall(s)
	loggerQueueSetTokens(s, clock, 3, 3)
	if ack, err := s.AcceptPacket([]byte("{"), testLoggerContextID()); err == nil || ack != nil {
		t.Fatal("malformed frame received an ACK")
	}
	if ack, err := s.AcceptPacket([]byte("{"), strings.Repeat("f", 32)); err == nil || ack != nil {
		t.Fatal("unknown context accepted")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	s.mu.Lock()
	s.hooks.wait = func(context.Context, int, int16, time.Time) error { return errPeerTimeout }
	s.mu.Unlock()
	if err := s.serveOnePacket(context.Background(), fds[0], testLoggerContextID()); !errors.Is(err, errPeerTimeout) {
		t.Fatalf("silent receive: %v", err)
	}
	s.mu.Lock()
	ok := s.global.tokens == 2 && s.perRun[testLoggerContextID()].tokens == 2 &&
		s.jobs == 0 && s.waiters == 0 && !s.poisoned
	s.mu.Unlock()
	if !ok {
		t.Fatal("malformed/silent/context rate boundary violated")
	}
	s.mu.Lock()
	s.hooks.wait = func(context.Context, int, int16, time.Time) error { return nil }
	s.mu.Unlock()
	for _, malformed := range [][]byte{[]byte("{"), bytes.Repeat([]byte("x"), hostLoggerMaxRecord+2)} {
		if _, err := unix.SendmsgN(fds[1], malformed, nil, nil, unix.MSG_NOSIGNAL); err != nil {
			t.Fatal(err)
		}
		if err := s.serveOnePacket(context.Background(), fds[0], testLoggerContextID()); err == nil {
			t.Fatal("malformed socket frame accepted")
		}
	}
	s.mu.Lock()
	charged := s.global.tokens == 0 && s.perRun[testLoggerContextID()].tokens == 0 && !s.poisoned && s.jobs == 0
	s.mu.Unlock()
	if !charged {
		t.Fatal("malformed/truncated authenticated socket frames bypassed token accounting")
	}
	ack := make([]byte, 4096)
	if _, _, _, _, err := unix.Recvmsg(fds[1], ack, nil, unix.MSG_DONTWAIT); err != unix.EAGAIN && err != unix.EWOULDBLOCK {
		t.Fatalf("malformed socket frame received an ACK: %v", err)
	}
}

func TestLoggerQueuePersistentLimitsAndCancelReuse(t *testing.T) {
	for _, limit := range []string{"bytes", "records"} {
		t.Run(limit, func(t *testing.T) {
			file, s := newTestHostLoggerStore(t)
			loggerQueueClockInstall(s)
			firstFrame, _ := loggerQueueFrame(t, 901)
			secondFrame, _ := loggerQueueFrame(t, 902)
			lineBytes := int64(len(extractEnvelopeEvent(firstFrame)) + 1)
			entered, release := make(chan struct{}), make(chan struct{})
			var once, syncOnce sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			s.mu.Lock()
			if limit == "bytes" {
				s.maxStoreBytes = 2 * lineBytes
			} else {
				s.maxStoreRecords = 2
			}
			s.hooks.sync = func() error {
				syncOnce.Do(func() { close(entered); <-release })
				return file.Sync()
			}
			s.mu.Unlock()
			first := loggerQueueAdmit(t, s, firstFrame, testLoggerContextID())
			loggerQueueReceive(t, entered)
			second := loggerQueueAdmit(t, s, secondFrame, testLoggerContextID())
			s.mu.Lock()
			atBoundary := s.storeBytes == 2*lineBytes && s.storeRecords == 2
			s.mu.Unlock()
			if !atBoundary {
				t.Fatal("unique appends did not reserve persistent limits before writer")
			}
			thirdFrame, _ := loggerQueueFrame(t, 903)
			over := loggerQueueAdmit(t, s, thirdFrame, testLoggerContextID())
			loggerQueueRejected(t, s, over)
			s.releaseWaiter(second)
			replacement := loggerQueueAdmit(t, s, thirdFrame, testLoggerContextID())
			if replacement.rejected || replacement.job == nil {
				t.Fatal("queued cancellation did not release persistent reservation")
			}
			once.Do(func() { close(release) })
			loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
			s.releaseWaiter(first)
			s.releaseWaiter(replacement)
			nextFrame, _ := loggerQueueFrame(t, 904)
			loggerQueueRejected(t, s, loggerQueueAdmit(t, s, nextFrame, testLoggerContextID()))
			// Full does not invalidate prior evidence; exact replay still works
			// when frame rate and waiter capacity permit it.
			replay, expected := loggerQueueFrame(t, 901)
			ack, err := s.AcceptPacket(replay, testLoggerContextID())
			if err != nil || validateHostLoggerACK(ack, expected) != nil {
				t.Fatalf("full-store replay: %v", err)
			}
			s.mu.Lock()
			bounded := !s.poisoned && s.storeBytes == 2*lineBytes && s.storeRecords == 2 &&
				s.diagnostics["logger_store_full"] == 2
			s.mu.Unlock()
			if !bounded || bytes.Count(readLoggerFile(t, file), []byte{'\n'}) != 2 {
				t.Fatal("persistent boundary changed evidence or exceeded reservations")
			}
		})
	}
}

func TestLoggerQueueConcurrentPersistentReservations(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	loggerQueueClockInstall(s)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	s.mu.Lock()
	s.maxStoreRecords = 1
	s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
	s.mu.Unlock()
	frames := make([][]byte, 8)
	for i := range frames {
		frames[i], _ = loggerQueueFrame(t, 1000+i)
	}
	type result struct {
		w   *hostLoggerWaiter
		err error
	}
	results := make(chan result, len(frames))
	start := make(chan struct{})
	for _, frame := range frames {
		go func(frame []byte) {
			<-start
			w, err := s.admit(context.Background(), s.hooks.now().Add(hostLoggerACKTimeout), frame, testLoggerContextID())
			results <- result{w, err}
		}(frame)
	}
	close(start)
	admitted := 0
	for range frames {
		r := loggerQueueReceive(t, results)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.w.rejected {
			loggerQueueRejected(t, s, r.w)
		} else {
			admitted++
			t.Cleanup(func() { s.releaseWaiter(r.w) })
		}
	}
	loggerQueueReceive(t, entered)
	if admitted != 1 {
		t.Fatalf("persistent reservation oversubscribed: %d", admitted)
	}
	once.Do(func() { close(release) })
	loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
	if bytes.Count(readLoggerFile(t, file), []byte{'\n'}) != 1 {
		t.Fatal("concurrent reservation appended more than hard limit")
	}
}

func TestLoggerQueueReopenRejectsPersistentOverflowWithoutTruncation(t *testing.T) {
	for _, limit := range []string{"bytes", "records", "hard_bytes"} {
		t.Run(limit, func(t *testing.T) {
			file, s := newTestHostLoggerStore(t)
			for i := 0; i < 2; i++ {
				frame, _ := loggerQueueFrame(t, 1100+i)
				if _, err := s.AcceptPacket(frame, testLoggerContextID()); err != nil {
					t.Fatal(err)
				}
			}
			before := readLoggerFile(t, file)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := os.OpenFile(file.Name(), os.O_RDWR|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			maxBytes, maxRecords := int64(hostLoggerStoreBytes), int64(hostLoggerStoreRecords)
			switch limit {
			case "bytes":
				maxBytes = int64(len(before) - 1)
			case "records":
				maxRecords = 1
			case "hard_bytes":
				if err := reopened.Truncate(hostLoggerStoreBytes + 1); err != nil {
					t.Fatal(err)
				}
			}
			info, err := reopened.Stat()
			if err != nil {
				t.Fatal(err)
			}
			opened, err := openHostLoggerStoreLimits(reopened, maxBytes, maxRecords)
			if err == nil || opened != nil {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatal("oversized persistent log reopened")
			}
			after, err := reopened.Stat()
			if err != nil || after.Size() != info.Size() {
				t.Fatal("failed startup truncated evidence")
			}
			prefix := make([]byte, len(before))
			if _, err := reopened.ReadAt(prefix, 0); err != nil || !bytes.Equal(prefix, before) {
				t.Fatal("failed startup changed existing records")
			}
			// Failure released flock; a second descriptor can acquire it.
			other, err := os.OpenFile(file.Name(), os.O_RDWR|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if err := unix.Flock(int(other.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal("failed startup retained flock")
			}
			_ = unix.Flock(int(other.Fd()), unix.LOCK_UN)
		})
	}
}

func TestLoggerQueueReopenAtPersistentBoundary(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	frame, expected := loggerQueueFrame(t, 1201)
	if _, err := s.AcceptPacket(frame, testLoggerContextID()); err != nil {
		t.Fatal(err)
	}
	before := readLoggerFile(t, file)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := os.OpenFile(file.Name(), os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := openHostLoggerStoreLimits(reopened, int64(len(before)), 1)
	if err != nil {
		_ = reopened.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	if err := recovered.RegisterRunContext(testLoggerContextID(), testLoggerPeer()); err != nil {
		t.Fatal(err)
	}
	ack, err := recovered.AcceptPacket(frame, testLoggerContextID())
	if err != nil || validateHostLoggerACK(ack, expected) != nil {
		t.Fatalf("boundary recovery lost durable replay: %v", err)
	}
	next, _ := loggerQueueFrame(t, 1202)
	loggerQueueRejected(t, recovered, loggerQueueAdmit(t, recovered, next, testLoggerContextID()))
	if !bytes.Equal(readLoggerFile(t, reopened), before) {
		t.Fatal("boundary recovery changed evidence")
	}
}

func TestLoggerQueuePendingDedupAndNonce(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	s.mu.Lock()
	s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
	s.mu.Unlock()
	frame, expected := loggerQueueFrame(t, 101)
	first := loggerQueueAdmit(t, s, frame, testLoggerContextID())
	loggerQueueReceive(t, entered)
	secondFrame, secondExpected := loggerQueueFrame(t, 101)
	if expected.DeliveryNonce == secondExpected.DeliveryNonce {
		t.Fatal("test needs independent nonces")
	}
	second := loggerQueueAdmit(t, s, secondFrame, testLoggerContextID())
	if first.job != second.job {
		t.Fatal("pending duplicate created a second writer job")
	}
	conflictEvent := testHostReaperEvent()
	conflictEvent.OperationID = fmt.Sprintf("%032x", 101)
	conflictEvent.TraceID = strings.Repeat("f", 32)
	conflictFrame, _, err := encodeHostLoggerEnvelope(conflictEvent)
	if err != nil {
		t.Fatal(err)
	}
	conflict := loggerQueueAdmit(t, s, conflictFrame, testLoggerContextID())
	if !conflict.rejected || conflict.job != nil {
		t.Fatal("conflicting pending event admitted")
	}
	releaseOnce.Do(func() { close(release) })
	for _, pair := range []struct {
		w        *hostLoggerWaiter
		expected hostLoggerACK
	}{{first, expected}, {second, secondExpected}} {
		ack, err := s.awaitACK(context.Background(), time.Now().Add(time.Second), pair.w)
		if err != nil || validateHostLoggerACK(ack, pair.expected) != nil {
			t.Fatalf("wrong independently correlated ACK: %v", err)
		}
	}
	if got := bytes.Count(readLoggerFile(t, file), []byte{'\n'}); got != 1 {
		t.Fatalf("duplicate append: %d", got)
	}
}

func TestLoggerQueueRetainsInflightChargeAfterCancellation(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	frame, _ := loggerQueueFrame(t, 102)
	s.mu.Lock()
	s.maxRunBytes, s.maxBytes = len(frame), len(frame)
	s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
	s.mu.Unlock()
	w := loggerQueueAdmit(t, s, frame, testLoggerContextID())
	loggerQueueReceive(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.awaitACK(ctx, time.Now().Add(time.Second), w); !errors.Is(err, errPeerCancelled) {
		t.Fatalf("cancel: %v", err)
	}
	s.releaseWaiter(w)
	s.mu.Lock()
	retained, poisoned := s.bytes, s.poisoned
	s.mu.Unlock()
	if retained != len(frame) || poisoned {
		t.Fatalf("inflight charge/state changed: %d %v", retained, poisoned)
	}
	if _, err := s.admit(context.Background(), time.Now().Add(time.Second), frame, testLoggerContextID()); err == nil {
		t.Fatal("admitted over retained charge")
	}
	once.Do(func() { close(release) })
	loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
	s.mu.Lock()
	remaining := s.bytes
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("charge leaked: %d", remaining)
	}
	if _, err := s.awaitACK(context.Background(), time.Now().Add(time.Second), w); err == nil {
		t.Fatal("canceled waiter received ACK")
	}
}

func TestLoggerQueueDeadlinePoisonsOnlyInflight(t *testing.T) {
	for _, inflight := range []bool{false, true} {
		t.Run(fmt.Sprint(inflight), func(t *testing.T) {
			file, s := newTestHostLoggerStore(t)
			clock := loggerQueueClockInstall(s)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			s.mu.Lock()
			s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
			s.mu.Unlock()
			frame, _ := loggerQueueFrame(t, 103)
			first := loggerQueueAdmit(t, s, frame, testLoggerContextID())
			loggerQueueReceive(t, entered)
			target := first
			if !inflight {
				frame, _ = loggerQueueFrame(t, 104)
				target = loggerQueueAdmit(t, s, frame, testLoggerContextID())
			}
			done := make(chan error, 1)
			deadline := clock.Now().Add(hostLoggerACKTimeout)
			go func() { _, err := s.awaitACK(context.Background(), deadline, target); done <- err }()
			loggerQueueReceive(t, clock.registered)
			clock.Advance(hostLoggerACKTimeout)
			if err := loggerQueueReceive(t, done); !errors.Is(err, errPeerTimeout) {
				t.Fatalf("deadline: %v", err)
			}
			s.releaseWaiter(target)
			s.mu.Lock()
			poisoned := s.poisoned
			s.mu.Unlock()
			if poisoned != inflight {
				t.Fatalf("wrong poison phase: %v", poisoned)
			}
			once.Do(func() { close(release) })
			loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
			if inflight {
				// The timeout won the mutex before the writer published its
				// successful Sync. Late durability must not restore ACK eligibility.
				s.mu.Lock()
				lateRejected := s.poisoned && first.job.finished && first.job.err != nil
				s.mu.Unlock()
				if !lateRejected {
					t.Fatal("late Sync publication restored timed-out inflight job")
				}
			}
			if got := bytes.Count(readLoggerFile(t, file), []byte{'\n'}); got != 1 {
				t.Fatalf("queued canceled job written: %d", got)
			}
			s.releaseWaiter(first)
		})
	}
}

func TestLoggerQueueRoundRobinAndPerRunFIFO(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	secondRun := strings.Repeat("b", 32)
	if err := s.RegisterRunContext(secondRun, testLoggerPeer()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var writesMu sync.Mutex
	var writes []string
	var n atomic.Int32
	s.mu.Lock()
	s.hooks.write = func(line []byte) (int, error) {
		var event hostReaperEvent
		if err := decodeCanonicalJSON(bytes.TrimSuffix(line, []byte{'\n'}), &event); err != nil {
			return 0, err
		}
		writesMu.Lock()
		writes = append(writes, event.OperationID)
		writesMu.Unlock()
		return file.Write(line)
	}
	s.hooks.sync = func() error {
		if n.Add(1) == 1 {
			close(entered)
			<-release
		}
		return file.Sync()
	}
	s.mu.Unlock()
	frame, _ := loggerQueueFrame(t, 1)
	loggerQueueAdmit(t, s, frame, testLoggerContextID())
	loggerQueueReceive(t, entered)
	for _, v := range []struct {
		id  int
		run string
	}{{2, testLoggerContextID()}, {3, testLoggerContextID()}, {4, secondRun}, {5, secondRun}} {
		frame, _ = loggerQueueFrame(t, v.id)
		loggerQueueAdmit(t, s, frame, v.run)
	}
	once.Do(func() { close(release) })
	loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
	writesMu.Lock()
	defer writesMu.Unlock()
	want := []int{1, 4, 2, 5, 3}
	for i, id := range want {
		if len(writes) != len(want) || writes[i] != fmt.Sprintf("%032x", id) {
			t.Fatalf("not round-robin FIFO: %v", writes)
		}
	}
}

func TestLoggerQueueWriteFailureStopsQueuedJobs(t *testing.T) {
	for _, fault := range []string{"short", "write", "partial", "sync"} {
		t.Run(fault, func(t *testing.T) {
			file, s := newTestHostLoggerStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var calls atomic.Int32
			s.mu.Lock()
			s.hooks.write = func(line []byte) (int, error) {
				calls.Add(1)
				close(entered)
				<-release
				if fault == "short" {
					return 0, nil
				}
				if fault == "write" {
					return 0, io.ErrUnexpectedEOF
				}
				if fault == "partial" {
					return file.Write(line[:len(line)/2])
				}
				return file.Write(line)
			}
			s.hooks.sync = func() error { return io.ErrUnexpectedEOF }
			s.mu.Unlock()
			frame, _ := loggerQueueFrame(t, 201)
			first := loggerQueueAdmit(t, s, frame, testLoggerContextID())
			loggerQueueReceive(t, entered)
			frame, _ = loggerQueueFrame(t, 202)
			second := loggerQueueAdmit(t, s, frame, testLoggerContextID())
			once.Do(func() { close(release) })
			for _, w := range []*hostLoggerWaiter{first, second} {
				if _, err := s.awaitACK(context.Background(), time.Now().Add(time.Second), w); err == nil {
					t.Fatal("failed writer produced ACK")
				}
			}
			loggerQueueReceive(t, s.writerDone)
			if calls.Load() != 1 {
				t.Fatal("poisoned writer dispatched queued job")
			}
			s.mu.Lock()
			wantRecords, wantBytes := int64(0), int64(0)
			if fault == "partial" || fault == "sync" {
				wantRecords, wantBytes = 1, int64(len(first.job.line))
			}
			conservative := s.storeRecords == wantRecords && s.storeBytes == wantBytes
			s.mu.Unlock()
			if !conservative {
				t.Fatal("unwritten reservations leaked or ambiguous effects were undercounted")
			}
			s.releaseWaiter(first)
			s.releaseWaiter(second)
			before := readLoggerFile(t, file)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := os.OpenFile(file.Name(), os.O_RDWR|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := openHostLoggerStore(reopened)
			if fault == "partial" {
				defer reopened.Close()
				if err == nil || recovered != nil {
					if recovered != nil {
						_ = recovered.Close()
					}
					t.Fatal("partial line was not quarantined by strict startup")
				}
			} else {
				if err != nil {
					_ = reopened.Close()
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = recovered.Close() })
				recovered.mu.Lock()
				rebuilt := recovered.storeRecords == wantRecords && recovered.storeBytes == int64(len(before))
				recovered.mu.Unlock()
				if !rebuilt {
					t.Fatal("reopen did not rebuild counters from complete lines")
				}
			}
			if !bytes.Equal(readLoggerFile(t, reopened), before) {
				t.Fatal("recovery mutated failure evidence")
			}
		})
	}
}

func TestLoggerQueueCloseSingleCleanupOwner(t *testing.T) {
	for _, stage := range []string{"unlock", "close"} {
		t.Run(stage, func(t *testing.T) {
			_, s := newTestHostLoggerStore(t)
			clock := loggerQueueClockInstall(s)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var unlocks, closes atomic.Int32
			s.mu.Lock()
			unlock, closeFile := s.hooks.unlock, s.hooks.close
			s.hooks.unlock = func() error {
				unlocks.Add(1)
				if stage == "unlock" {
					close(entered)
					<-release
				}
				return unlock()
			}
			s.hooks.close = func() error {
				closes.Add(1)
				if stage == "close" {
					close(entered)
					<-release
				}
				return closeFile()
			}
			s.mu.Unlock()
			done := make(chan error, 1)
			go func() { done <- s.Close() }()
			loggerQueueReceive(t, entered)
			s.mu.Lock()
			ownerStarted := s.cleanupStarted && !s.closeExpired
			s.mu.Unlock()
			if !ownerStarted {
				t.Fatal("OS cleanup started without its unique owner")
			}
			clock.Advance(hostLoggerACKTimeout)
			if err := loggerQueueReceive(t, done); !errors.Is(err, errHostLoggerRestartRequired) {
				t.Fatalf("Close deadline: %v", err)
			}
			again := make(chan error, 1)
			go func() { again <- s.Close() }()
			once.Do(func() { close(release) })
			if err := loggerQueueReceive(t, again); !errors.Is(err, errHostLoggerRestartRequired) {
				t.Fatalf("expired Close lost failure: %v", err)
			}
			if unlocks.Load() != 1 || closes.Load() != 1 {
				t.Fatalf("duplicate OS cleanup: %d %d", unlocks.Load(), closes.Load())
			}
		})
	}
}

func TestLoggerQueueCapacityAndContextCeiling(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var syncCalls atomic.Int32
	s.mu.Lock()
	s.hooks.sync = func() error {
		if syncCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return file.Sync()
	}
	s.mu.Unlock()
	if s.maxRunBytes != 4<<20 || s.maxBytes != 64<<20 || s.maxJobs != 4096 || s.maxWaiters != 16 {
		t.Fatal("production limits changed")
	}
	runs := []string{testLoggerContextID()}
	for i := 1; i < 16; i++ {
		run := fmt.Sprintf("%032x", i)
		runs = append(runs, run)
		if err := s.RegisterRunContext(run, testLoggerPeer()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.claimHandler(run); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.releaseHandler(run) })
	}
	if _, err := s.claimHandler(testLoggerContextID()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.releaseHandler(testLoggerContextID()) })
	total := 0
	for i, run := range runs {
		frame, _ := loggerQueueFrame(t, 700+i)
		total += len(frame)
		loggerQueueAdmit(t, s, frame, run)
		if i == 0 {
			loggerQueueReceive(t, entered)
		}
	}
	if err := s.RegisterRunContext(strings.Repeat("c", 32), testLoggerPeer()); err == nil {
		t.Fatal("17th context admitted")
	}
	if _, err := s.claimHandler(testLoggerContextID()); err == nil {
		t.Fatal("second active handler admitted")
	}
	overflow, _ := loggerQueueFrame(t, 800)
	if _, err := s.admit(context.Background(), time.Now().Add(time.Second), overflow, testLoggerContextID()); err == nil {
		t.Fatal("17th waiter admitted")
	}
	s.mu.Lock()
	handlers, waiters, jobs, charged := s.handlers, s.waiters, s.jobs, s.bytes
	s.mu.Unlock()
	if handlers != 16 || waiters != 16 || jobs != 16 || charged != total {
		t.Fatalf("rejection grew state: %d %d %d %d", handlers, waiters, jobs, charged)
	}
	once.Do(func() { close(release) })
	loggerQueueWait(t, s, func() bool { return s.jobs == 0 })
}

func TestLoggerQueueACKSendFailureStaysLocal(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	server, client := os.NewFile(uintptr(fds[0]), "queue-server"), os.NewFile(uintptr(fds[1]), "queue-client")
	defer server.Close()
	defer client.Close()
	frame, _ := loggerQueueFrame(t, 301)
	if _, err := unix.SendmsgN(int(client.Fd()), frame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.hooks.send = func(int, []byte) (int, error) { return 0, unix.EPIPE }
	s.mu.Unlock()
	if err := s.serveOnePacket(context.Background(), int(server.Fd()), testLoggerContextID()); err == nil {
		t.Fatal("ACK transport failure hidden")
	}
	s.mu.Lock()
	poisoned := s.poisoned
	s.mu.Unlock()
	if poisoned {
		t.Fatal("known durable ACK failure poisoned shared store")
	}
	replay, expected := loggerQueueFrame(t, 301)
	ack, err := s.AcceptPacket(replay, testLoggerContextID())
	if err != nil || validateHostLoggerACK(ack, expected) != nil {
		t.Fatalf("durable replay failed: %v", err)
	}
	if bytes.Count(readLoggerFile(t, file), []byte{'\n'}) != 1 {
		t.Fatal("ACK-lost replay duplicated line")
	}
}

func TestLoggerQueuePostSyncDeadlineAndPreAdmissionCancel(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	clock := loggerQueueClockInstall(s)
	frame, _ := loggerQueueFrame(t, 401)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.admit(ctx, clock.Now().Add(hostLoggerACKTimeout), frame, testLoggerContextID()); !errors.Is(err, errPeerCancelled) {
		t.Fatalf("pre-admission cancel: %v", err)
	}
	s.mu.Lock()
	empty := s.jobs == 0 && s.waiters == 0 && s.bytes == 0 && !s.poisoned
	s.mu.Unlock()
	if !empty {
		t.Fatal("pre-admission cancel changed store")
	}
	deadline := clock.Now().Add(hostLoggerACKTimeout)
	w := loggerQueueAdmit(t, s, frame, testLoggerContextID())
	loggerQueueWait(t, s, func() bool { return w.job.finished })
	s.mu.Lock()
	published := w.job.err == nil && !s.poisoned
	s.mu.Unlock()
	if !published {
		t.Fatal("successful Sync was not published before deadline")
	}
	clock.Advance(hostLoggerACKTimeout)
	if _, err := s.awaitACK(context.Background(), deadline, w); !errors.Is(err, errPeerTimeout) {
		t.Fatalf("late durable ACK: %v", err)
	}
	s.releaseWaiter(w)
	s.mu.Lock()
	poisoned := s.poisoned
	s.mu.Unlock()
	if poisoned {
		t.Fatal("post-Sync deadline poisoned healthy store")
	}
	replay, expected := loggerQueueFrame(t, 401)
	ack, err := s.AcceptPacket(replay, testLoggerContextID())
	if err != nil || validateHostLoggerACK(ack, expected) != nil {
		t.Fatalf("post-Sync replay: %v", err)
	}
	if bytes.Count(readLoggerFile(t, file), []byte{'\n'}) != 1 {
		t.Fatal("durable replay wrote twice")
	}
}

func TestLoggerQueueQueuedCancellationAndLimits(t *testing.T) {
	for _, limit := range []string{"run_bytes", "global_bytes", "jobs", "waiters"} {
		t.Run(limit, func(t *testing.T) {
			file, s := newTestHostLoggerStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			frame, _ := loggerQueueFrame(t, 501)
			s.mu.Lock()
			switch limit {
			case "run_bytes":
				s.maxRunBytes = 2 * len(frame)
			case "global_bytes":
				s.maxBytes = 2 * len(frame)
			case "jobs":
				s.maxJobs = 2
			case "waiters":
				s.maxWaiters = 2
			}
			s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
			s.mu.Unlock()
			first := loggerQueueAdmit(t, s, frame, testLoggerContextID())
			loggerQueueReceive(t, entered)
			secondFrame, _ := loggerQueueFrame(t, 502)
			if len(frame) != len(secondFrame) {
				t.Fatal("fixture frame sizes differ")
			}
			second := loggerQueueAdmit(t, s, secondFrame, testLoggerContextID())
			s.mu.Lock()
			beforeBytes, beforeJobs, beforeWaiters := s.bytes, s.jobs, s.waiters
			s.mu.Unlock()
			thirdFrame, _ := loggerQueueFrame(t, 503)
			if _, err := s.admit(context.Background(), time.Now().Add(time.Second), thirdFrame, testLoggerContextID()); err == nil {
				t.Fatal("over-capacity admitted")
			}
			s.mu.Lock()
			unchanged := s.bytes == beforeBytes && s.jobs == beforeJobs && s.waiters == beforeWaiters
			s.mu.Unlock()
			if !unchanged {
				t.Fatal("rejection allocated queue state")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := s.awaitACK(ctx, time.Now().Add(time.Second), second); !errors.Is(err, errPeerCancelled) {
				t.Fatalf("queued cancel: %v", err)
			}
			s.releaseWaiter(second)
			s.mu.Lock()
			canceled := s.jobs == 1 && len(s.queues[testLoggerContextID()]) == 0 && !s.poisoned
			s.mu.Unlock()
			if !canceled {
				t.Fatal("queued cancellation did not remove job")
			}
			once.Do(func() { close(release) })
			if _, err := s.awaitACK(context.Background(), time.Now().Add(time.Second), first); err != nil {
				t.Fatal(err)
			}
			if bytes.Count(readLoggerFile(t, file), []byte{'\n'}) != 1 {
				t.Fatal("canceled or rejected frame appended")
			}
		})
	}
}

func TestLoggerQueueStuckWriterCloseRetainsFD(t *testing.T) {
	file, s := newTestHostLoggerStore(t)
	clock := loggerQueueClockInstall(s)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.mu.Lock()
	unlock, closeFile := s.hooks.unlock, s.hooks.close
	var cleanupCalls atomic.Int32
	s.hooks.unlock = func() error { cleanupCalls.Add(1); return unlock() }
	s.hooks.close = func() error { cleanupCalls.Add(1); return closeFile() }
	s.hooks.sync = func() error { close(entered); <-release; return file.Sync() }
	s.mu.Unlock()
	// Test-owned recovery after Close refused cleanup. No production path
	// retries a close/unlock whose outcome is ambiguous.
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		loggerQueueReceive(t, s.writerDone)
		_ = unlock()
		_ = closeFile()
	})
	frame, _ := loggerQueueFrame(t, 601)
	w := loggerQueueAdmit(t, s, frame, testLoggerContextID())
	loggerQueueReceive(t, entered)
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	loggerQueueReceive(t, clock.registered)
	clock.Advance(hostLoggerACKTimeout)
	if err := loggerQueueReceive(t, done); !errors.Is(err, errHostLoggerRestartRequired) {
		t.Fatalf("stuck close: %v", err)
	}
	if cleanupCalls.Load() != 0 {
		t.Fatal("closed active writer FD")
	}
	s.mu.Lock()
	expiredBeforeOwner := s.closeExpired && !s.cleanupStarted
	s.mu.Unlock()
	if !expiredBeforeOwner {
		t.Fatal("timeout failed to fence OS cleanup ownership")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("FD not retained")
	}
	once.Do(func() { close(release) })
	loggerQueueReceive(t, s.writerDone)
	s.releaseWaiter(w)
	if err := s.Close(); !errors.Is(err, errHostLoggerRestartRequired) {
		t.Fatalf("expired close restarted cleanup: %v", err)
	}
	if cleanupCalls.Load() != 0 {
		t.Fatal("late writer completion started forbidden cleanup")
	}
}

func TestLoggerQueueOSCleanupFailureNeverRetries(t *testing.T) {
	for _, stage := range []string{"unlock", "close"} {
		t.Run(stage, func(t *testing.T) {
			_, s := newTestHostLoggerStore(t)
			s.mu.Lock()
			unlock, closeFile := s.hooks.unlock, s.hooks.close
			var unlocks, closes atomic.Int32
			s.hooks.unlock = func() error {
				unlocks.Add(1)
				if stage == "unlock" {
					return io.ErrUnexpectedEOF
				}
				return unlock()
			}
			s.hooks.close = func() error { closes.Add(1); return io.ErrUnexpectedEOF }
			s.mu.Unlock()
			// Hooks above prove syscalls were not attempted on the injected
			// failure; only this test may release its retained resources.
			t.Cleanup(func() { _ = unlock(); _ = closeFile() })
			for i := 0; i < 2; i++ {
				if err := s.Close(); !errors.Is(err, errHostLoggerRestartRequired) {
					t.Fatalf("cleanup failure: %v", err)
				}
			}
			if unlocks.Load() != 1 {
				t.Fatal("unlock retried")
			}
			wantClose := int32(0)
			if stage == "close" {
				wantClose = 1
			}
			if closes.Load() != wantClose {
				t.Fatal("close repeated or ran after unlock error")
			}
		})
	}
}

func TestLoggerQueuePoisonWinsBeforeACKSend(t *testing.T) {
	_, s := newTestHostLoggerStore(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	server, client := os.NewFile(uintptr(fds[0]), "queue-ack-server"), os.NewFile(uintptr(fds[1]), "queue-ack-client")
	defer server.Close()
	defer client.Close()
	// Enable credentials before queuing the frame so Linux attaches the peer
	// credentials to this packet deterministically.
	if err := unix.SetsockoptInt(int(server.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	frame, _ := loggerQueueFrame(t, 901)
	if _, err := unix.SendmsgN(int(client.Fd()), frame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	eligible, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var sends atomic.Int32
	s.mu.Lock()
	wait, send := s.hooks.wait, s.hooks.send
	var eligibleOnce sync.Once
	s.hooks.wait = func(ctx context.Context, fd int, events int16, deadline time.Time) error {
		if events == unix.POLLOUT {
			eligibleOnce.Do(func() { close(eligible) })
			<-release
			// Readiness is the controlled test barrier. Returning directly keeps
			// the race window deterministic; production uses loggerWaitWithPoll.
			return nil
		}
		return wait(ctx, fd, events, deadline)
	}
	s.hooks.send = func(fd int, ack []byte) (int, error) { sends.Add(1); return send(fd, ack) }
	s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.serveOnePacket(context.Background(), int(server.Fd()), testLoggerContextID()) }()
	select {
	case <-eligible:
	case err := <-done:
		t.Fatalf("packet handler returned before ACK barrier: %v", err)
	case <-time.After(5 * time.Second):
		once.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("packet handler did not exit after releasing the test barrier")
		}
		t.Fatal("packet handler did not reach ACK readiness barrier")
	}
	s.mu.Lock()
	s.poisonLocked()
	s.mu.Unlock()
	once.Do(func() { close(release) })
	if err := loggerQueueReceive(t, done); err == nil {
		t.Fatal("poisoned store accepted ACK")
	}
	if sends.Load() != 0 {
		t.Fatal("ACK eligibility was not rechecked after poison")
	}
}

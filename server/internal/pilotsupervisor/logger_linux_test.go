//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newTestHostLoggerClient(t *testing.T) (*hostLoggerClient, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientFile := os.NewFile(uintptr(fds[0]), "test-host-logger-client")
	peerFile := os.NewFile(uintptr(fds[1]), "test-host-logger-peer")
	client, err := newHostLoggerClient(clientFile, hostLoggerPeerIdentity{PID: int32(os.Getpid()), UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())})
	if err != nil {
		_ = clientFile.Close()
		_ = peerFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = peerFile.Close() })
	return client, peerFile
}

func init() { runHostLoggerPlatformTests = runLinuxHostLoggerTests }

func TestLoggerWaitZeroDeadlineWaitsForReadabilityAndCancellation(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]); _ = unix.Close(fds[1]) })
	if _, err := unix.SendmsgN(fds[1], []byte("frame"), nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := loggerWait(context.Background(), fds[0], unix.POLLIN, time.Time{}); err != nil {
		t.Fatalf("zero-deadline wait rejected readable socket: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pollEntered := make(chan struct{})
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- loggerWaitWithPoll(ctx, fds[0], unix.POLLIN, time.Time{}, func(_ []unix.PollFd, _ int) (int, error) {
			close(pollEntered)
			<-ctx.Done()
			return 0, nil
		})
	}()
	<-pollEntered
	cancel()
	if err := <-waitResult; !errors.Is(err, errPeerCancelled) {
		t.Fatalf("zero-deadline blocked wait did not preserve cancellation: %v", err)
	}
}

func runLinuxHostLoggerTests(t *testing.T) {
	t.Run("logger_store_durable_dedup_and_restart", func(t *testing.T) {
		file, store := newTestHostLoggerStore(t)
		frame, expected, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		// Drop the returned ACK to model a lost response after the durable append.
		if _, err := store.AcceptPacket(frame, testLoggerContextID()); err != nil {
			t.Fatalf("first durable append failed: %v", err)
		}
		before := readLoggerFile(t, file)
		// A durable exact replay still consumes an authenticated frame token.
		// Exhausted buckets reject it without changing the durable evidence.
		clock := loggerQueueClockInstall(store)
		now := clock.Now()
		store.mu.Lock()
		store.global = hostLoggerRateBucket{tokens: 0, last: now}
		store.perRun[testLoggerContextID()] = hostLoggerRateBucket{tokens: 0, last: now}
		store.mu.Unlock()
		replay, replayExpected, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		replayACK, err := store.AcceptPacket(replay, testLoggerContextID())
		var rejected hostLoggerACK
		if err != nil || decodeCanonicalJSON(replayACK, &rejected) != nil || rejected.Status != "rejected" ||
			rejected.DeliveryNonce != replayExpected.DeliveryNonce || !bytes.Equal(readLoggerFile(t, file), before) {
			t.Fatalf("rate-limited replay appended or lacked rejection: err=%v", err)
		}
		if replayExpected.DeliveryNonce == expected.DeliveryNonce {
			t.Fatal("replay did not use a fresh delivery nonce")
		}
		store.mu.Lock()
		store.global.tokens = hostLoggerGlobalBurst
		bucket := store.perRun[testLoggerContextID()]
		bucket.tokens = hostLoggerRunBurst
		store.perRun[testLoggerContextID()] = bucket
		store.mu.Unlock()
		allowedACK, err := store.AcceptPacket(replay, testLoggerContextID())
		if err != nil || validateHostLoggerACK(allowedACK, replayExpected) != nil || !bytes.Equal(readLoggerFile(t, file), before) {
			t.Fatalf("refilled replay failed or wrote again: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopenedFile, err := os.OpenFile(file.Name(), os.O_RDWR|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := openHostLoggerStore(reopenedFile)
		if err != nil {
			_ = reopenedFile.Close()
			t.Fatalf("valid JSONL index rebuild failed: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		restartReplay, restartExpected, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil || restartExpected.DeliveryNonce == replayExpected.DeliveryNonce {
			t.Fatalf("restart replay nonce was not refreshed: %v", err)
		}
		if err := reopened.RegisterRunContext(testLoggerContextID(), testLoggerPeer()); err != nil {
			t.Fatal(err)
		}
		ackAfterRestart, err := reopened.AcceptPacket(restartReplay, testLoggerContextID())
		if err != nil || validateHostLoggerACK(ackAfterRestart, restartExpected) != nil || !bytes.Equal(readLoggerFile(t, reopenedFile), before) {
			t.Fatalf("restart replay duplicated durable event: err=%v", err)
		}
		finish := testHostReaperEvent()
		finish.Event = "bootstrap.finish"
		finishFrame, finishExpected, err := encodeHostLoggerEnvelope(finish)
		if err != nil {
			t.Fatal(err)
		}
		finishACK, err := reopened.AcceptPacket(finishFrame, testLoggerContextID())
		if err != nil || validateHostLoggerACK(finishACK, finishExpected) != nil {
			t.Fatalf("finish event was not persisted: %s %v", finishACK, err)
		}
		finishReplay, finishReplayExpected, err := encodeHostLoggerEnvelope(finish)
		if err != nil {
			t.Fatal(err)
		}
		finishReplayACK, err := reopened.AcceptPacket(finishReplay, testLoggerContextID())
		if err != nil || validateHostLoggerACK(finishReplayACK, finishReplayExpected) != nil || bytes.Count(readLoggerFile(t, reopenedFile), []byte(`"event":"bootstrap.finish"`)) != 1 {
			t.Fatalf("finish replay was not deduplicated: %s %v", finishReplayACK, err)
		}
	})

	t.Run("logger_same_operation_event_different_digest_rejected", func(t *testing.T) {
		file, store := newTestHostLoggerStore(t)
		first, _, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AcceptPacket(first, testLoggerContextID()); err != nil {
			t.Fatal(err)
		}
		before := readLoggerFile(t, file)
		changed := testHostReaperEvent()
		changed.IOMS++
		conflict, expected, err := encodeHostLoggerEnvelope(changed)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := store.AcceptPacket(conflict, testLoggerContextID())
		if err != nil || validateHostLoggerACK(ack, expected) == nil || !bytes.Contains(ack, []byte(`"status":"rejected"`)) {
			t.Fatalf("digest conflict not rejected: ack=%s err=%v", ack, err)
		}
		if !bytes.Equal(readLoggerFile(t, file), before) {
			t.Fatal("conflicting event was appended")
		}
	})

	t.Run("logger_store_binds_event_process_id_to_context", func(t *testing.T) {
		file, store := newTestHostLoggerStore(t)
		before := readLoggerFile(t, file)
		event := testHostReaperEvent()
		event.ProcessID = strings.Repeat("4", 32)
		frame, _, err := encodeHostLoggerEnvelope(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AcceptPacket(frame, testLoggerContextID()); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("event process identity was not bound to registered context: %v", err)
		}
		if !bytes.Equal(before, readLoggerFile(t, file)) {
			t.Fatal("mismatched process identity was persisted")
		}
	})

	t.Run("logger_distinct_barriers_not_deduplicated", func(t *testing.T) {
		file, store := newTestHostLoggerStore(t)
		for _, name := range []string{"after_prepared", "after_executing"} {
			event := testHostReaperEvent()
			event.Event = "barrier"
			event.BarrierName = &name
			frame, expected, err := encodeHostLoggerEnvelope(event)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := store.AcceptPacket(frame, testLoggerContextID())
			if err != nil || validateHostLoggerACK(ack, expected) != nil {
				t.Fatalf("barrier %s rejected: %s %v", name, ack, err)
			}
		}
		got := readLoggerFile(t, file)
		if bytes.Count(got, []byte(`"event":"barrier"`)) != 2 {
			t.Fatalf("repeatable barriers were deduplicated: %s", got)
		}
	})

	t.Run("logger_startup_index_rebuild_fail_closed_matrix", func(t *testing.T) {
		for _, mode := range []string{"torn", "duplicate", "conflicting_digest", "oversized_torn", "oversized_terminated"} {
			t.Run(mode, func(t *testing.T) {
				file, err := os.CreateTemp(t.TempDir(), "host-log-*.jsonl")
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Chmod(0o600); err != nil {
					t.Fatal(err)
				}
				event := testHostReaperEvent()
				eventBytes, _ := json.Marshal(event)
				content := append([]byte(nil), eventBytes...)
				if mode == "duplicate" {
					content = append(content, '\n')
					content = append(content, eventBytes...)
					content = append(content, '\n')
				} else if mode == "conflicting_digest" {
					content = append(content, '\n')
					event.IOMS++
					conflicting, _ := json.Marshal(event)
					content = append(content, conflicting...)
					content = append(content, '\n')
				} else if mode == "oversized_torn" || mode == "oversized_terminated" {
					content = bytes.Repeat([]byte("x"), hostLoggerMaxRecord+100)
					if mode == "oversized_terminated" {
						content = append(content, '\n')
					}
				}
				if _, err := file.Write(content); err != nil {
					t.Fatal(err)
				}
				_ = file.Close()
				file, err = os.OpenFile(file.Name(), os.O_RDWR|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := openHostLoggerStore(file); !errors.Is(err, errHostLoggerStore) {
					_ = file.Close()
					t.Fatalf("%s log was not rejected: %v", mode, err)
				}
				_ = file.Close()
			})
		}
	})

	t.Run("logger_store_bounded_state_limits", func(t *testing.T) {
		file, store := newTestHostLoggerStore(t)
		contextIDs := []string{testLoggerContextID()}
		peers := []hostLoggerPeerIdentity{testLoggerPeer()}
		for i := 1; i < hostLoggerMaxRunBuckets; i++ {
			contextID := fmt.Sprintf("%032x", i+200)
			peer := testLoggerPeer()
			peer.EventProcessID = fmt.Sprintf("%032x", i+100)
			if err := store.RegisterRunContext(contextID, peer); err != nil {
				t.Fatalf("register run context %d: %v", i, err)
			}
			contextIDs = append(contextIDs, contextID)
			peers = append(peers, peer)
		}
		for i := 0; i < hostLoggerMaxRunBuckets; i++ {
			event := testHostReaperEvent()
			event.Event = "barrier"
			barrier := "after_prepared"
			event.BarrierName = &barrier
			event.ProcessID = peers[i].EventProcessID
			frame, expected, err := encodeHostLoggerEnvelope(event)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := store.AcceptPacket(frame, contextIDs[i])
			if err != nil || validateHostLoggerACK(ack, expected) != nil {
				t.Fatalf("run bucket %d was not accepted: %s %v", i, ack, err)
			}
		}
		if got := len(store.contexts); got != hostLoggerMaxRunBuckets || len(store.perRun) != hostLoggerMaxRunBuckets {
			t.Fatalf("active run context bound not reached exactly: contexts=%d buckets=%d", len(store.contexts), len(store.perRun))
		}
		extraContext := fmt.Sprintf("%032x", 999)
		extraPeer := testLoggerPeer()
		extraPeer.EventProcessID = fmt.Sprintf("%032x", 999)
		if err := store.RegisterRunContext(contextIDs[0], peers[0]); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("duplicate live context registration was accepted: %v", err)
		}
		if err := store.RegisterRunContext(extraContext, extraPeer); !errors.Is(err, errHostLoggerStore) || len(store.contexts) != hostLoggerMaxRunBuckets {
			t.Fatalf("over-limit live context was not rejected boundedly: %v contexts=%d", err, len(store.contexts))
		}
		if err := store.ReleaseRunContext(contextIDs[1], peers[1]); err != nil {
			t.Fatalf("release completed run context: %v", err)
		}
		if err := store.RegisterRunContext(extraContext, extraPeer); err != nil {
			t.Fatalf("new run context did not reuse released bucket: %v", err)
		}
		extra := testHostReaperEvent()
		extra.Event = "barrier"
		barrier := "after_prepared"
		extra.BarrierName = &barrier
		extra.ProcessID = extraPeer.EventProcessID
		frame, expected, err := encodeHostLoggerEnvelope(extra)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := store.AcceptPacket(frame, extraContext)
		if err != nil || validateHostLoggerACK(ack, expected) != nil {
			t.Fatalf("reused run bucket rejected new active context: %s %v", ack, err)
		}
		before := readLoggerFile(t, file)
		for i := 0; i < hostLoggerMaxDedupKeys; i++ {
			store.index[hostLoggerIndexKey{operationID: fmt.Sprintf("%032x", i+1), event: "run_root_dispatch_intent"}] = "digest"
		}
		newEvent := testHostReaperEvent()
		newEvent.ProcessID = testLoggerPeer().EventProcessID
		frame, expected, err = encodeHostLoggerEnvelope(newEvent)
		if err != nil {
			t.Fatal(err)
		}
		ack, err = store.AcceptPacket(frame, testLoggerContextID())
		if err != nil || validateHostLoggerACK(ack, expected) == nil || len(store.index) != hostLoggerMaxDedupKeys || !bytes.Equal(readLoggerFile(t, file), before) {
			t.Fatalf("over-limit dedup key was not rejected boundedly: %s %v index=%d", ack, err, len(store.index))
		}
	})

	t.Run("logger_store_rejects_read_only_log_fd", func(t *testing.T) {
		created, err := os.CreateTemp(t.TempDir(), "readonly-log-*.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		path := created.Name()
		if err := created.Chmod(0o600); err != nil {
			t.Fatal(err)
		}
		if err := created.Close(); err != nil {
			t.Fatal(err)
		}
		readOnly, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer readOnly.Close()
		if _, err := openHostLoggerStore(readOnly); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("read-only descriptor accepted at startup: %v", err)
		}
	})

	t.Run("logger_store_rejects_nonappend_log_fd", func(t *testing.T) {
		created, err := os.CreateTemp(t.TempDir(), "nonappend-log-*.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		path := created.Name()
		if err := created.Chmod(0o600); err != nil {
			t.Fatal(err)
		}
		if err := created.Close(); err != nil {
			t.Fatal(err)
		}
		withoutAppend, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer withoutAppend.Close()
		if _, err := openHostLoggerStore(withoutAppend); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("non-append descriptor accepted at startup: %v", err)
		}
	})

	t.Run("logger_ack_socket_single_flight", func(t *testing.T) {
		client, peer := newTestHostLoggerClient(t)
		// Ensure the receiver has credentials enabled before the frame enters
		// the socket queue; setting SO_PASSCRED in the goroutine is racy.
		if err := unix.SetsockoptInt(int(peer.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
			t.Fatal(err)
		}
		_, store := newTestHostLoggerStore(t)
		peerDone := make(chan error, 1)
		go func() {
			peerDone <- store.serveOnePacket(context.Background(), int(peer.Fd()), testLoggerContextID())
		}()
		if err := client.SendCritical(context.Background(), testHostReaperEvent()); err != nil {
			t.Fatal(err)
		}
		if err := <-peerDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("logger_store_serve_timeout_without_packet", func(t *testing.T) {
		storeFile, store := newTestHostLoggerStore(t)
		_ = storeFile
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		server := os.NewFile(uintptr(fds[0]), "timeout-logger-server")
		client := os.NewFile(uintptr(fds[1]), "timeout-logger-client")
		defer server.Close()
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
		defer cancel()
		err = store.serveOnePacket(ctx, int(server.Fd()), testLoggerContextID())
		if !errors.Is(err, errPeerTimeout) {
			t.Fatalf("silent peer did not hit bounded receive deadline: %v", err)
		}
	})

	t.Run("logger_store_context_release_requires_idle_handler", func(t *testing.T) {
		_, store := newTestHostLoggerStore(t)
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		server := os.NewFile(uintptr(fds[0]), "context-lifecycle-server")
		client := os.NewFile(uintptr(fds[1]), "context-lifecycle-client")
		defer server.Close()
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- store.serveOnePacket(ctx, int(server.Fd()), testLoggerContextID()) }()
		deadline := time.Now().Add(time.Second)
		for {
			store.mu.Lock()
			active := store.contexts[testLoggerContextID()].active
			store.mu.Unlock()
			if active {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("packet handler did not acquire its context lease")
			}
			time.Sleep(time.Millisecond)
		}
		if err := store.ReleaseRunContext(testLoggerContextID(), testLoggerPeer()); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("released context while a packet handler was active: %v", err)
		}
		if err := store.serveOnePacket(context.Background(), int(server.Fd()), testLoggerContextID()); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("parallel packet handler was accepted: %v", err)
		}
		if err := <-done; !errors.Is(err, errPeerTimeout) {
			t.Fatalf("active handler did not finish at deadline: %v", err)
		}
		if err := store.ReleaseRunContext(testLoggerContextID(), testLoggerPeer()); err != nil {
			t.Fatalf("idle context release failed: %v", err)
		}
		if err := store.RegisterRunContext(testLoggerContextID(), testLoggerPeer()); err != nil {
			t.Fatalf("context ID could not be reused after handler exit: %v", err)
		}
	})

	t.Run("logger_store_zero_payload_rights_closed", func(t *testing.T) {
		_, store := newTestHostLoggerStore(t)
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		server := os.NewFile(uintptr(fds[0]), "rights-logger-server")
		client := os.NewFile(uintptr(fds[1]), "rights-logger-client")
		defer server.Close()
		defer client.Close()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		before := countOpenFDs(t)
		n, err := unix.SendmsgN(int(client.Fd()), nil, unix.UnixRights(int(r.Fd())), nil, unix.MSG_NOSIGNAL)
		if err != nil || n != 0 {
			t.Fatalf("send zero-payload rights frame: n=%d err=%v", n, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := store.serveOnePacket(ctx, int(server.Fd()), testLoggerContextID()); !errors.Is(err, errHostLoggerStore) {
			t.Fatalf("ancillary-only packet was not rejected: %v", err)
		}
		if got := countOpenFDs(t); got != before {
			t.Fatalf("zero-payload SCM_RIGHTS leaked fd: before=%d after=%d", before, got)
		}
	})

	t.Run("logger_ack_transport_cancel_poisons", func(t *testing.T) {
		client, peer := newTestHostLoggerClient(t)
		peerFD := int(peer.Fd())
		recvTimeout := unix.NsecToTimeval(int64(500 * time.Millisecond))
		if err := unix.SetsockoptTimeval(peerFD, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &recvTimeout); err != nil {
			t.Fatalf("bound mock peer receive: %v", err)
		}
		peerDone := make(chan struct{})
		peerResult := make(chan error, 1)
		go func() {
			defer close(peerDone)
			frame := make([]byte, hostLoggerMaxRecord+1)
			n, oobn, flags, _, err := unix.Recvmsg(peerFD, frame, nil, 0)
			if err == nil && (n == 0 || n > hostLoggerMaxRecord || oobn != 0 || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0) {
				err = fmt.Errorf("peer received invalid request frame: n=%d oobn=%d flags=%#x", n, oobn, flags)
			}
			peerResult <- err
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
		defer cancel()
		err := client.SendCritical(ctx, testHostReaperEvent())
		select {
		case peerErr := <-peerResult:
			if peerErr != nil {
				t.Fatalf("peer did not receive a valid request before ACK timeout: %v", peerErr)
			}
		case <-time.After(time.Second):
			if closeErr := client.Close(); closeErr != nil {
				t.Errorf("close client to release peer reader: %v", closeErr)
			}
			select {
			case <-peerDone:
			case <-time.After(time.Second):
				t.Fatal("peer reader did not exit after bounded close and receive timeout")
			}
			t.Fatal("peer did not receive request before bounded ACK timeout")
		}
		if !errors.Is(err, errPeerTimeout) {
			t.Fatalf("got %v, want bounded ack timeout", err)
		}
		if err := client.SendCritical(context.Background(), testHostReaperEvent()); !errors.Is(err, errHostLoggerUnavailable) {
			t.Fatalf("poisoned channel reused: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("close client to release peer reader: %v", err)
		}
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Fatal("peer reader did not exit after client close")
		}
	})

	t.Run("logger_ack_context_cancel_is_preserved", func(t *testing.T) {
		client, _ := newTestHostLoggerClient(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := client.SendCritical(ctx, testHostReaperEvent()); !errors.Is(err, errPeerCancelled) {
			t.Fatalf("context cancellation was not preserved: %v", err)
		}
		if err := client.SendCritical(context.Background(), testHostReaperEvent()); !errors.Is(err, errHostLoggerUnavailable) {
			t.Fatalf("poisoned channel reused: %v", err)
		}
	})

	t.Run("logger_ack_scm_rights_closed_before_reject", func(t *testing.T) {
		client, peer := newTestHostLoggerClient(t)
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		before := countOpenFDs(t)
		peerDone := make(chan struct{})
		go func() {
			defer close(peerDone)
			request := make([]byte, hostLoggerMaxRecord+1)
			n, _, _, _, recvErr := unix.Recvmsg(int(peer.Fd()), request, nil, 0)
			if recvErr != nil || n == 0 {
				return
			}
			_, expected, decodeErr := decodeHostLoggerEnvelope(request[:n])
			if decodeErr != nil {
				return
			}
			ack, encodeErr := encodeHostLoggerACK(hostLoggerACK{Version: 1, TraceID: expected.TraceID,
				ProcessID: expected.ProcessID, OperationID: expected.OperationID, Event: expected.Event,
				DeliveryNonce: expected.DeliveryNonce, EventSHA256: expected.EventSHA256, Status: "accepted"})
			if encodeErr == nil {
				_, _ = unix.SendmsgN(int(peer.Fd()), ack, unix.UnixRights(int(r.Fd())), nil, unix.MSG_NOSIGNAL)
			}
		}()
		if err := client.SendCritical(context.Background(), testHostReaperEvent()); !errors.Is(err, errHostLoggerUnavailable) {
			t.Fatalf("SCM_RIGHTS ACK accepted: %v", err)
		}
		<-peerDone
		if got := countOpenFDs(t); got != before {
			t.Fatalf("received SCM_RIGHTS leaked fd: before=%d after=%d", before, got)
		}
	})

	t.Run("logger_ack_ctrunc_rights_closed_before_reject", func(t *testing.T) {
		client, peer := newTestHostLoggerClient(t)
		client.oobSize = unix.CmsgLen(0) + 4
		r1, w1, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r1.Close()
		defer w1.Close()
		r2, w2, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r2.Close()
		defer w2.Close()
		before := countOpenFDs(t)
		peerDone := make(chan struct{})
		go func() {
			defer close(peerDone)
			request := make([]byte, hostLoggerMaxRecord+1)
			n, _, _, _, recvErr := unix.Recvmsg(int(peer.Fd()), request, nil, 0)
			if recvErr != nil || n == 0 {
				return
			}
			_, expected, decodeErr := decodeHostLoggerEnvelope(request[:n])
			if decodeErr != nil {
				return
			}
			ack, encodeErr := encodeHostLoggerACK(hostLoggerACK{Version: 1, TraceID: expected.TraceID,
				ProcessID: expected.ProcessID, OperationID: expected.OperationID, Event: expected.Event,
				DeliveryNonce: expected.DeliveryNonce, EventSHA256: expected.EventSHA256, Status: "accepted"})
			if encodeErr == nil {
				_, _ = unix.SendmsgN(int(peer.Fd()), ack, unix.UnixRights(int(r1.Fd()), int(r2.Fd())), nil, unix.MSG_NOSIGNAL)
			}
		}()
		if err := client.SendCritical(context.Background(), testHostReaperEvent()); !errors.Is(err, errHostLoggerUnavailable) {
			t.Fatalf("MSG_CTRUNC ACK accepted: %v", err)
		}
		<-peerDone
		if got := countOpenFDs(t); got != before {
			t.Fatalf("truncated SCM_RIGHTS leaked fd: before=%d after=%d", before, got)
		}
	})

	t.Run("logger_ack_rejects_stream_socket", func(t *testing.T) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(uintptr(fds[0]), "test-stream")
		peer := os.NewFile(uintptr(fds[1]), "test-stream-peer")
		defer file.Close()
		defer peer.Close()
		if _, err := newHostLoggerClient(file, hostLoggerPeerIdentity{PID: int32(os.Getpid()), UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())}); !errors.Is(err, errHostLoggerUnavailable) {
			t.Fatalf("stream socket accepted: %v", err)
		}
	})

	t.Run("logger_ack_rejects_unexpected_peer_credentials", func(t *testing.T) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(uintptr(fds[0]), "test-untrusted-peer")
		peer := os.NewFile(uintptr(fds[1]), "test-untrusted-peer-peer")
		defer file.Close()
		defer peer.Close()
		valid := hostLoggerPeerIdentity{PID: int32(os.Getpid()), UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())}
		if !hostLoggerPeerMatches(int(file.Fd()), valid) {
			t.Fatal("server rejected trusted peer credentials")
		}
		cases := map[string]hostLoggerPeerIdentity{
			"pid": {PID: valid.PID + 1, UID: valid.UID, GID: valid.GID},
			"uid": {PID: valid.PID, UID: valid.UID + 1, GID: valid.GID},
			"gid": {PID: valid.PID, UID: valid.UID, GID: valid.GID + 1},
		}
		for field, expected := range cases {
			t.Run(field, func(t *testing.T) {
				if _, err := newHostLoggerClient(file, expected); !errors.Is(err, errHostLoggerUnavailable) {
					t.Fatalf("unexpected SO_PEERCRED %s accepted: %v", field, err)
				}
				if hostLoggerPeerMatches(int(file.Fd()), expected) {
					t.Fatalf("logger server accepted unexpected SO_PEERCRED %s", field)
				}
			})
		}
	})
}

func newTestHostLoggerStore(t *testing.T) (*os.File, *hostLoggerStore) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "host-log-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	file, err = os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openHostLoggerStore(file)
	if err != nil {
		_ = file.Close()
		t.Fatalf("open store: %v", err)
	}
	if err := store.RegisterRunContext(testLoggerContextID(), testLoggerPeer()); err != nil {
		_ = store.Close()
		t.Fatalf("register default run context: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return file, store
}

func testLoggerContextID() string { return strings.Repeat("a", 32) }

func testLoggerPeer() hostLoggerPeerIdentity {
	return hostLoggerPeerIdentity{PID: int32(os.Getpid()), UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid()), EventProcessID: strings.Repeat("2", 32)}
}

func readLoggerFile(t *testing.T, file *os.File) []byte {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	return content
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

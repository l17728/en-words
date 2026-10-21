//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestConnectHostLoggerClientUsesAuthenticatedReadinessChannel(t *testing.T) {
	contextID := "0123456789abcdef0123456789abcdef"
	const generation = 9
	ready, err := encodeHostLoggerChannelReady(contextID, generation, "ok")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, closeListener := serveLoggerReadinessOnce(t, ready)
	defer closeListener()

	client, err := connectHostLoggerClient(context.Background(), endpoint,
		hostLoggerServiceIdentity{UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())}, contextID, generation)
	if err != nil {
		t.Fatalf("connect authenticated logger: %v", err)
	}
	if client.contextID != contextID || client.generation != generation || client.poisoned {
		t.Fatalf("connected client did not retain authenticated binding: %+v", client)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close connected client: %v", err)
	}
}

func TestConnectHostLoggerClientRejectsInvalidConfigurationAndReadiness(t *testing.T) {
	contextID := "0123456789abcdef0123456789abcdef"
	wrongContext := "1123456789abcdef0123456789abcdef"
	badReady, err := encodeHostLoggerChannelReady(wrongContext, 9, "ok")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, closeListener := serveLoggerReadinessOnce(t, badReady)
	defer closeListener()

	service := hostLoggerServiceIdentity{UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())}
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		endpoint   string
		contextID  string
		generation uint64
	}{
		{name: "nil context", endpoint: endpoint, contextID: contextID, generation: 9},
		{name: "filesystem endpoint", ctx: context.Background(), endpoint: filepathLoggerEndpoint(), contextID: contextID, generation: 9},
		{name: "bad context id", ctx: context.Background(), endpoint: endpoint, contextID: "bad", generation: 9},
		{name: "zero generation", ctx: context.Background(), endpoint: endpoint, contextID: contextID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := connectHostLoggerClient(tc.ctx, tc.endpoint, service, tc.contextID, tc.generation); err == nil {
				t.Fatal("invalid client configuration was accepted")
			}
		})
	}

	if _, err := connectHostLoggerClient(context.Background(), endpoint, service, contextID, 9); err == nil {
		t.Fatal("readiness frame for a different context was accepted")
	}
}

func TestConnectHostLoggerClientRejectsWrongServiceCredentials(t *testing.T) {
	contextID := "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name    string
		service hostLoggerServiceIdentity
	}{
		{name: "uid", service: hostLoggerServiceIdentity{UID: uint32(unix.Getuid()) + 1, GID: uint32(unix.Getgid())}},
		{name: "gid", service: hostLoggerServiceIdentity{UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid()) + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready, err := encodeHostLoggerChannelReady(contextID, 9, "ok")
			if err != nil {
				t.Fatal(err)
			}
			endpoint, cleanup := serveLoggerReadinessOnce(t, ready)
			defer cleanup()
			if _, err := connectHostLoggerClient(context.Background(), endpoint, tc.service, contextID, 9); !errors.Is(err, errHostLoggerUnavailable) {
				t.Fatalf("logger with mismatched SO_PEERCRED %s accepted: %v", tc.name, err)
			}
		})
	}
}

func TestConnectHostLoggerClientDoesNotRetryMissingListenerIntoSuccess(t *testing.T) {
	endpoint := fmt.Sprintf("\x00en-words-logger-absent-%d", time.Now().UnixNano())
	_, err := connectHostLoggerClient(context.Background(), endpoint,
		hostLoggerServiceIdentity{UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())},
		"0123456789abcdef0123456789abcdef", 9)
	if !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("absent logger endpoint classification = %v", err)
	}
}

func TestNewHostLoggerClientRejectsRegularFileAndMismatchedPID(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "not-a-socket-*")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := newHostLoggerClient(regular, hostLoggerPeerIdentity{PID: int32(unix.Getpid())}); !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("regular file accepted as logger channel: %v", err)
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientFile := os.NewFile(uintptr(fds[0]), "logger-peer-check")
	peerFile := os.NewFile(uintptr(fds[1]), "logger-peer-check-peer")
	defer clientFile.Close()
	defer peerFile.Close()
	expected := hostLoggerPeerIdentity{PID: int32(unix.Getpid()) + 1, UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid())}
	if _, err := newHostLoggerClient(clientFile, expected); !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("SO_PEERCRED PID mismatch accepted: %v", err)
	}
}

func TestHostLoggerRunRateWaitHonorsCancellationAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "cancelled", ctx: cancelledLoggerContext(), want: errPeerCancelled},
		{name: "deadline", ctx: context.Background(), want: errPeerTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &hostLoggerClient{rateTokens: 0, rateLast: time.Now()}
			deadline := time.Now().Add(time.Second)
			if tc.name == "deadline" {
				deadline = time.Now().Add(-time.Millisecond)
			}
			if err := client.waitRunRate(tc.ctx, deadline); !errors.Is(err, tc.want) {
				t.Fatalf("waitRunRate error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHostLoggerRunRateWaitCanBeCancelledWhileThrottled(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	ctx := &loggerWaitObservedContext{Context: base, entered: make(chan struct{})}
	client := &hostLoggerClient{rateTokens: 0, rateLast: time.Now()}
	done := make(chan error, 1)
	go func() { done <- client.waitRunRate(ctx, time.Now().Add(time.Second)) }()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("rate wait did not enter its cancellation select")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errPeerCancelled) {
			t.Fatalf("in-flight rate wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight rate wait did not observe cancellation")
	}

	deadlineClient := &hostLoggerClient{rateTokens: 0, rateLast: time.Now()}
	if err := deadlineClient.waitRunRate(context.Background(), time.Now().Add(5*time.Millisecond)); !errors.Is(err, errPeerTimeout) {
		t.Fatalf("in-flight deadline shorter than refill returned %v", err)
	}
}

func TestHostLoggerSendCriticalRejectsInvalidBindingsAndCancellation(t *testing.T) {
	ctx := context.Background()
	event := testHostReaperEvent()
	var nilClient *hostLoggerClient
	if err := nilClient.SendCritical(ctx, event); !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("nil client classification = %v", err)
	}
	if err := (&hostLoggerClient{}).SendCritical(nil, event); !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("nil context classification = %v", err)
	}
	if err := (&hostLoggerClient{}).SendCritical(ctx, event); !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("closed/uninitialized client classification = %v", err)
	}
	boundFile, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	bound := &hostLoggerClient{file: boundFile, fd: int(boundFile.Fd()), contextID: "0123456789abcdef0123456789abcdef", generation: 1}
	if err := bound.SendCritical(ctx, event); !errors.Is(err, errHostLoggerProtocol) {
		t.Fatalf("wrong event-process binding classification = %v", err)
	}
	bound.poisoned = true
	event.ProcessID = hostLoggerEventProcessID(bound.contextID, bound.generation)
	if err := bound.SendCritical(ctx, event); !errors.Is(err, errHostLoggerUnavailable) {
		t.Fatalf("poisoned client reused: %v", err)
	}
	_ = bound.Close()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fds[0]), "logger-send-cancel")
	peer := os.NewFile(uintptr(fds[1]), "logger-send-cancel-peer")
	defer file.Close()
	defer peer.Close()
	client := &hostLoggerClient{file: file, fd: fds[0], oobSize: 4096, contextID: "0123456789abcdef0123456789abcdef", generation: 1}
	event.ProcessID = hostLoggerEventProcessID(client.contextID, client.generation)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := client.SendCritical(cancelled, event); !errors.Is(err, errPeerCancelled) {
		t.Fatalf("cancelled send classification = %v", err)
	}
	if !client.poisoned {
		t.Fatal("ambiguous cancelled send did not poison the channel")
	}
}

func TestCloseReceivedLoggerRightsClosesRealDuplicatedFD(t *testing.T) {
	var pipeFDs [2]int
	if err := unix.Pipe(pipeFDs[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipeFDs[0])
	defer unix.Close(pipeFDs[1])
	duplicateFD, err := unix.Dup(pipeFDs[0])
	if err != nil {
		t.Fatal(err)
	}
	closeReceivedLoggerRights(unix.UnixRights(duplicateFD))
	if _, err := unix.FcntlInt(uintptr(duplicateFD), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("received descriptor still open or check failed: %v", err)
	}
	closeReceivedLoggerRights(nil)
}

type loggerWaitObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *loggerWaitObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func cancelledLoggerContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func serveLoggerReadinessOnce(t *testing.T, readiness []byte) (string, func()) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("\x00en-words-logger-test-%d", time.Now().UnixNano())
	if err = unix.Bind(fd, &unix.SockaddrUnix{Name: endpoint}); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	if err = unix.Listen(fd, 1); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		conn, _, acceptErr := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if acceptErr != nil {
			accepted <- acceptErr
			return
		}
		defer unix.Close(conn)
		_, sendErr := unix.SendmsgN(conn, readiness, nil, nil, unix.MSG_NOSIGNAL)
		accepted <- sendErr
	}()
	cleanup := func() {
		select {
		case err := <-accepted:
			if err != nil && !errors.Is(err, unix.ECONNRESET) && !errors.Is(err, unix.EPIPE) {
				t.Errorf("readiness fixture: %v", err)
			}
		default:
			// Closing a listening descriptor from another goroutine does not
			// reliably interrupt Linux accept(2). Wake the acceptor with a real
			// local connection, then close the listener after it drains.
			wakeFD, wakeErr := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			if wakeErr == nil {
				wakeErr = unix.Connect(wakeFD, &unix.SockaddrUnix{Name: endpoint})
				_ = unix.Close(wakeFD)
			}
			if wakeErr != nil {
				t.Errorf("wake readiness fixture accept: %v", wakeErr)
			}
			select {
			case err := <-accepted:
				if err != nil && !errors.Is(err, unix.ECONNRESET) && !errors.Is(err, unix.EPIPE) {
					t.Errorf("readiness fixture: %v", err)
				}
			case <-time.After(time.Second):
				t.Error("readiness fixture did not accept client after wakeup")
			}
		}
		_ = unix.Close(fd)
	}
	return endpoint, cleanup
}

func filepathLoggerEndpoint() string { return "/tmp/not-an-abstract-logger-endpoint" }

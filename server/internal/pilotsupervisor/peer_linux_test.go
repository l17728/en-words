//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPeerIPC_BootstrapDeadlineKillsAndReaps(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	start := time.Now()
	_, err = readLinuxBootstrapFD(int(r.Fd()), 25*time.Millisecond)
	if !errors.Is(err, errPeerTimeout) {
		t.Fatalf("got %v, want ipc_timeout", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("bootstrap deadline exceeded bound")
	}
}

func TestPeerIPC_LinuxBootstrapReadsOneClosedFrame(t *testing.T) {
	frame, err := encodePeerBootstrap(validPeerBootstrapForTest())
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := w.Write(frame)
		closeErr := w.Close()
		if writeErr != nil {
			writeDone <- writeErr
		} else {
			writeDone <- closeErr
		}
	}()
	got, readErr := readLinuxBootstrapFD(int(r.Fd()), time.Second)
	_ = r.Close()
	if writeErr := <-writeDone; writeErr != nil {
		t.Fatal(writeErr)
	}
	if readErr != nil || got != validPeerBootstrapForTest() {
		t.Fatalf("read bootstrap: %#v, %v", got, readErr)
	}
}

func TestPeerIPC_LinuxBootstrapRejectsTrailingBytes(t *testing.T) {
	frame, err := encodePeerBootstrap(validPeerBootstrapForTest())
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() { _, _ = w.Write(append(frame, 0x7f)); _ = w.Close() }()
	if _, err := readLinuxBootstrapFD(int(r.Fd()), time.Second); !errors.Is(err, errPeerBootstrapInvalid) {
		t.Fatalf("got %v", err)
	}
}

func linuxTestListener(t *testing.T) (*linuxUnixListener, *linuxPeerParent, string) {
	t.Helper()
	// Keep the Unix socket path below Linux's sun_path limit even when test
	// names or the repository's checkout path are long.
	dir, err := os.MkdirTemp("/tmp", "p-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := openLinuxPeerParent(os.Getpid(), uint32(os.Getuid()))
	if err != nil {
		t.Skipf("pidfd unavailable on this kernel: %v", err)
	}
	t.Cleanup(func() { _ = parent.close() })
	generation, nonce := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	endpoint, err := linuxEndpoint(dir, generation, nonce)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenLinuxPeer(dir, endpoint, parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.listener.Close() })
	return listener, parent, endpoint
}

func linuxConnect(t *testing.T, endpoint string) *net.UnixConn {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestPeerIPC_RealParentIsAccepted(t *testing.T) {
	listener, parent, endpoint := linuxTestListener(t)
	client := linuxConnect(t, endpoint)
	accepted := make(chan struct {
		peer authenticatedIPCPeer
		err  error
	}, 1)
	go func() {
		conn, err := listener.listener.AcceptUnix()
		if err != nil {
			accepted <- struct {
				peer authenticatedIPCPeer
				err  error
			}{err: err}
			return
		}
		peer, err := authenticateLinuxPeer(conn, listener, parent, nil)
		accepted <- struct {
			peer authenticatedIPCPeer
			err  error
		}{peer, err}
	}()
	_ = client.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := client.Write([]byte("connect")); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.peer.state == nil || result.peer.state.pid != uint32(os.Getpid()) || result.peer.state.principal != fmt.Sprintf("uid:%d", os.Getuid()) {
			t.Fatalf("unexpected authenticated identity: %#v", result.peer.state)
		}
		if err := result.peer.claimGrant(); err != nil {
			t.Fatal(err)
		}
		if err := result.peer.close(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accept/authentication timed out")
	}
}

func TestPeerIPC_RejectsIndependentWrongPID(t *testing.T) {
	listener, parent, endpoint := linuxTestListener(t)
	_ = linuxConnect(t, endpoint)
	conn, err := listener.listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	wrong := &linuxPeerParent{pid: os.Getpid() + 1, uid: parent.uid, fd: parent.fd, ops: parent.ops}
	_, err = authenticateLinuxPeer(conn, listener, wrong, nil)
	_ = conn.Close()
	if !errors.Is(err, errPeerRejected) {
		t.Fatalf("got %v, want ipc_peer_rejected", err)
	}
}

func TestPeerIPC_CredentialQueryFailureIsClosed(t *testing.T) {
	listener, parent, endpoint := linuxTestListener(t)
	client := linuxConnect(t, endpoint)
	conn, err := listener.listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	ops := realLinuxPeerOps
	ops.getPeerCred = func(int) (*unix.Ucred, error) { return nil, fmt.Errorf("injected query failure") }
	_, err = authenticateLinuxPeer(conn, listener, parent, &ops)
	_ = conn.Close()
	_ = client.Close()
	if !errors.Is(err, errPeerRejected) {
		t.Fatalf("got %v, want ipc_peer_rejected", err)
	}
}

func TestPeerIPC_AcceptCancellationIsBounded(t *testing.T) {
	listener, parent, _ := linuxTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := acceptLinuxPeer(ctx, listener, parent, nil)
	if !errors.Is(err, errPeerCancelled) {
		t.Fatalf("got %v, want ipc_cancelled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation exceeded one-second bound")
	}
}

func TestPeerIPC_ParentWatcherClosesWorkerResources(t *testing.T) {
	fd, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	var dead atomic.Bool
	parent := &linuxPeerParent{pid: 42, uid: 1000, fd: fd, ops: linuxPeerOps{
		pollFD: func(int) (bool, error) { return dead.Load(), nil },
	}}
	conn := &peerTrackedConn{}
	peer, err := newAuthenticatedIPCPeer(conn, parent, 42, "uid:1000")
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := watchLinuxParent(ctx, parent, func() error { closed.Add(1); return peer.close() })
	dead.Store(true)
	select {
	case err := <-done:
		if !errors.Is(err, errPeerParentGone) {
			t.Fatalf("watcher got %v, want ipc_parent_gone", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("parent watcher exceeded 100ms polling budget")
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("resource close count=%d, want 1", got)
	}
	if !conn.closed.Load() || !peer.state.closed.Load() {
		t.Fatal("parent watcher did not close the active peer connection")
	}
}

func TestPeerIPC_ParentWatcherReportsCleanupFailure(t *testing.T) {
	fd, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	var dead atomic.Bool
	parent := &linuxPeerParent{pid: 42, uid: 1000, fd: fd, ops: linuxPeerOps{
		pollFD: func(int) (bool, error) { return dead.Load(), nil },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := watchLinuxParent(ctx, parent, func() error { return errors.New("injected close failure") })
	dead.Store(true)
	select {
	case err := <-done:
		if !errors.Is(err, errPeerListenerFailed) || failureStage(err) != "parent-exit-cleanup" {
			t.Fatalf("watcher got %v stage=%s, want cleanup failure", err, failureStage(err))
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("parent watcher did not report cleanup failure")
	}
	_ = parent.close()
}

func TestPeerIPC_ClosedParentHandleIsAlwaysGone(t *testing.T) {
	fd, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	parent := &linuxPeerParent{pid: 42, uid: 1000, fd: fd, ops: linuxPeerOps{
		pollFD: func(int) (bool, error) { return false, nil },
	}}
	if err := parent.checkAlive(); err != nil {
		t.Fatalf("live parent rejected: %v", err)
	}
	if err := parent.close(); err != nil {
		t.Fatal(err)
	}
	if err := parent.checkAlive(); !errors.Is(err, errPeerParentGone) {
		t.Fatalf("closed parent got %v, want ipc_parent_gone", err)
	}
}

func TestPeerIPC_StaleEndpointIdentityFailsClosed(t *testing.T) {
	listener, parent, endpoint := linuxTestListener(t)
	client := linuxConnect(t, endpoint)
	conn, err := listener.listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := os.Chmod(endpoint, 0666); err != nil {
		t.Fatal(err)
	}
	if err := listener.verifyIdentity(); !errors.Is(err, errPeerListenerFailed) {
		t.Fatalf("got %v, want ipc_listener_failed", err)
	}
	if _, err := authenticateLinuxPeer(conn, listener, parent, nil); !errors.Is(err, errPeerListenerFailed) {
		t.Fatalf("authentication got %v, want ipc_listener_failed", err)
	}
	_ = client.Close()
	if filepath.Dir(endpoint) == "" {
		t.Fatal("empty control directory")
	}
}

func TestPeerIPC_ReadFrameRejectsSCMRights(t *testing.T) {
	listener, _, endpoint := linuxTestListener(t)
	client := linuxConnect(t, endpoint)
	conn, err := listener.listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, err = client.WriteMsgUnix([]byte{0, 0, 0, 1, 'x'}, unix.UnixRights(int(os.Stdin.Fd())), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readLinuxPeerFrame(conn, time.Second); !errors.Is(err, errPeerRejected) {
		t.Fatalf("got %v, want ipc_peer_rejected", err)
	}
}

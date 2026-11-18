//go:build pilot_e2e && windows

package pilotsupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsTryConnectWithGatewayMask(t *testing.T, name, sid string, gatewayMask uint32) error {
	t.Helper()
	sddl := fmt.Sprintf("D:P(A;;0x%08X;;;%s)(A;;0x001F01FF;;;SY)", gatewayMask, sid)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	server, err := windows.CreateNamedPipe(namePtr, windowsPipeOpenMode, windowsPipeMode, windowsPipeInstances, 4096, 4096, 0, &sa)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(server)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	ov := windows.Overlapped{HEvent: event}
	connectErr := windows.ConnectNamedPipe(server, &ov)
	if connectErr != nil && connectErr != windows.ERROR_IO_PENDING && connectErr != windows.ERROR_PIPE_CONNECTED {
		t.Fatal(connectErr)
	}
	client, clientErr := windows.CreateFile(namePtr, windowsClientPipeAccess, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if clientErr == nil {
		defer windows.CloseHandle(client)
		if result, waitErr := windows.WaitForSingleObject(event, 1000); waitErr != nil || result != windows.WAIT_OBJECT_0 {
			t.Fatalf("connect wait result=%d err=%v", result, waitErr)
		}
	}
	_ = windows.CancelIoEx(server, &ov)
	return clientErr
}

func TestPeerIPC_WindowsPipeAccessContract(t *testing.T) {
	if windowsGatewayPipeAccess != 0x00100083 || windowsClientPipeAccess != 0x00100003 {
		t.Fatalf("unexpected pipe access masks: gateway=%#08x client=%#08x", windowsGatewayPipeAccess, windowsClientPipeAccess)
	}
	if windowsPipeOpenMode&windows.FILE_FLAG_FIRST_PIPE_INSTANCE == 0 || windowsPipeMode&windows.PIPE_REJECT_REMOTE_CLIENTS == 0 || windowsPipeInstances != 1 {
		t.Fatal("named-pipe first-instance, remote-client, or single-instance restriction missing")
	}
	_, sid := windowsTestParent(t)
	actualOpenMode, actualPipeMode, actualInstances := uint32(0), uint32(0), uint32(0)
	ops := realWindowsPeerOps
	ops.createPipe = func(name *uint16, openMode, pipeMode, instances, outSize, inSize, timeout uint32, sa *windows.SecurityAttributes) (windows.Handle, error) {
		actualOpenMode, actualPipeMode, actualInstances = openMode, pipeMode, instances
		return windows.CreateNamedPipe(name, openMode, pipeMode, instances, outSize, inSize, timeout, sa)
	}
	name, err := windowsEndpoint(testEpoch, "11111111111111111111111111111111", "22222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	server, err := createWindowsPeerPipe(name, sid, &ops)
	if err != nil {
		t.Fatalf("production listener constructor: %v", err)
	}
	defer server.close()
	if actualOpenMode != windowsPipeOpenMode || actualPipeMode != windowsPipeMode || actualInstances != windowsPipeInstances {
		t.Fatalf("production CreateNamedPipe args open=%#x mode=%#x instances=%d", actualOpenMode, actualPipeMode, actualInstances)
	}
	for _, tc := range []struct {
		name string
		mask uint32
		want bool
	}{
		{name: "minimal-data-only-is-denied", mask: 0x00100003},
		{name: "read-attributes-connects", mask: 0x00100083, want: true},
		{name: "write-attributes-does-not-substitute", mask: 0x00100103},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, err := windowsEndpoint(testEpoch, "abcdef0123456789abcdef0123456789", "0123456789abcdef0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			name += "-" + tc.name
			err = windowsTryConnectWithGatewayMask(t, name, sid, tc.mask)
			if (err == nil) != tc.want {
				t.Fatalf("CreateFile err=%v, want success=%v", err, tc.want)
			}
			if !tc.want && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("CreateFile err=%v, want ERROR_ACCESS_DENIED", err)
			}
		})
	}
}

func windowsTestParent(t *testing.T) (*windowsPeerParent, string) {
	t.Helper()
	pid := uint32(os.Getpid())
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	sid, created, err := windowsProcessIdentity(h)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	parent, err := newWindowsPeerParent(h, pid, sid, created, nil)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.close() })
	return parent, sid
}

func windowsTestPipe(t *testing.T, ops *windowsPeerOps) (*windowsPipeServer, *windowsPeerParent, string) {
	t.Helper()
	parent, sid := windowsTestParent(t)
	name, err := windowsEndpoint(testEpoch, "abcdef0123456789abcdef0123456789", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	server, err := createWindowsPeerPipe(name, sid, ops)
	if err != nil {
		t.Fatalf("create pipe: %v (stage=%s)", err, failureStage(err))
	}
	t.Cleanup(func() { _ = server.close() })
	return server, parent, name
}

func TestPeerIPC_RealParentIsAccepted(t *testing.T) {
	server, parent, name := windowsTestPipe(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		peer authenticatedIPCPeer
		err  error
	}
	done := make(chan result, 1)
	go func() { peer, err := server.accept(ctx, 2*time.Second, parent); done <- result{peer, err} }()
	// Ensure ConnectNamedPipe has reached its pending/listening state before
	// the client opens the endpoint; otherwise CreateFile may race setup.
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		accepting := server.accepting
		server.mu.Unlock()
		if accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accept did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	requestedAccess := uint32(0)
	client, err := openWindowsPipeClientWith(name, func(name *uint16, access, share uint32, sa *windows.SecurityAttributes, disposition uint32, flags uint32, template windows.Handle) (windows.Handle, error) {
		requestedAccess = access
		return windows.CreateFile(name, access, share, sa, disposition, flags, template)
	})
	if err != nil {
		t.Fatalf("open pipe client: %v (stage=%s cause=%v)", err, failureStage(err), failureCause(err))
	}
	if requestedAccess != windowsClientPipeAccess {
		t.Fatalf("production CreateFile requested access %#x, want %#x", requestedAccess, windowsClientPipeAccess)
	}
	defer windows.CloseHandle(client)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("accept: %v (stage=%s)", got.err, failureStage(got.err))
		}
		if got.peer.state == nil || got.peer.state.pid != uint32(os.Getpid()) || got.peer.state.principal != parent.sid {
			t.Fatalf("unexpected peer identity: %#v", got.peer.state)
		}
		if err := got.peer.claimGrant(); err != nil {
			t.Fatal(err)
		}
		if err := got.peer.close(); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("named-pipe peer authentication timed out")
	}
}

func TestPeerIPC_RejectsIndependentWrongPID(t *testing.T) {
	ops := realWindowsPeerOps
	parentPID := uint32(os.Getpid())
	ops.clientPID = func(windows.Handle) (uint32, error) { return parentPID + 1, nil }
	server, parent, name := windowsTestPipe(t, &ops)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := server.accept(ctx, 2*time.Second, parent); done <- err }()
	readyDeadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		accepting := server.accepting
		server.mu.Unlock()
		if accepting {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("accept did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	client, err := openWindowsPipeClient(name)
	if err != nil {
		t.Fatalf("open pipe client: %v (stage=%s cause=%v)", err, failureStage(err), failureCause(err))
	}
	defer windows.CloseHandle(client)
	select {
	case err := <-done:
		if !errors.Is(err, errPeerRejected) {
			t.Fatalf("got %v (stage=%s), want ipc_peer_rejected", err, failureStage(err))
		}
		if server.handle != windows.InvalidHandle {
			t.Fatal("rejected client left the single pipe instance occupied")
		}
	case <-ctx.Done():
		t.Fatal("named-pipe negative test timed out")
	}
}

func TestPeerIPC_TransfersPipeHandleOnceWhenCloseRacesAuthentication(t *testing.T) {
	var server *windowsPipeServer
	var pidCalls atomic.Int32
	var listenerHandleCloses atomic.Int32
	authPaused := make(chan struct{})
	continueAuth := make(chan struct{})
	ops := realWindowsPeerOps
	ops.clientPID = func(h windows.Handle) (uint32, error) {
		var pid uint32
		err := windows.GetNamedPipeClientProcessId(h, &pid)
		if pidCalls.Add(1) == 2 {
			close(authPaused)
			<-continueAuth
		}
		return pid, err
	}
	ops.closeHandle = func(h windows.Handle) error {
		listenerHandleCloses.Add(1)
		return windows.CloseHandle(h)
	}
	var parent *windowsPeerParent
	var name string
	server, parent, name = windowsTestPipe(t, &ops)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := server.accept(ctx, 2*time.Second, parent); done <- err }()
	readyDeadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		accepting := server.accepting
		server.mu.Unlock()
		if accepting {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("accept did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	client, err := openWindowsPipeClient(name)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(client)
	select {
	case <-authPaused:
	case <-ctx.Done():
		t.Fatal("authentication barrier was not reached")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- server.close() }()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		closing := server.closeRequested
		server.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not enter the authentication barrier")
		}
		time.Sleep(time.Millisecond)
	}
	close(continueAuth)
	select {
	case err := <-done:
		if !errors.Is(err, errPeerCancelled) {
			t.Fatalf("accept got %v, want ipc_cancelled", err)
		}
	case <-ctx.Done():
		t.Fatal("close/authentication race timed out")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Close did not join accept")
	}
	if got := listenerHandleCloses.Load(); got != 0 {
		t.Fatalf("listener closed HANDLE %d times after transfer; peer must be sole owner", got)
	}
	server.mu.Lock()
	deferredClose := server.handle
	stillAccepting := server.accepting
	server.mu.Unlock()
	if deferredClose != windows.InvalidHandle || stillAccepting {
		t.Fatalf("listener retained handle=%v or accepting=%t after transfer race", deferredClose, stillAccepting)
	}
}

func TestPeerIPC_AcceptCancellationClosesPendingPipe(t *testing.T) {
	server, parent, _ := windowsTestPipe(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := server.accept(ctx, 10*time.Second, parent); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		accepting := server.accepting
		server.mu.Unlock()
		if accepting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accept did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	if err := server.close(); err != nil {
		t.Fatalf("close pending pipe: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errPeerCancelled) {
			t.Fatalf("accept got %v, want ipc_cancelled", err)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("pending ConnectNamedPipe did not cancel within one second")
	}
	cancel()
	if server.handle != windows.InvalidHandle {
		t.Fatal("cancelled accept retained its pipe handle")
	}
	server.mu.Lock()
	acceptStillRunning := server.accepting || server.acceptDone != nil
	server.mu.Unlock()
	if acceptStillRunning {
		t.Fatal("listener Close returned before accept completed")
	}
}

func TestPeerIPC_RejectsDACLReadbackFailure(t *testing.T) {
	parent, sid := windowsTestParent(t)
	_ = parent
	name, err := windowsEndpoint(testEpoch, "11111111111111111111111111111111", "22222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	ops := realWindowsPeerOps
	ops.security = func(windows.Handle) (*windows.SECURITY_DESCRIPTOR, error) {
		return nil, errors.New("injected DACL query failure")
	}
	if _, err := createWindowsPeerPipe(name, sid, &ops); !errors.Is(err, errPeerListenerFailed) {
		t.Fatalf("got %v, want ipc_listener_failed", err)
	}
}

func TestPeerIPC_ListenerCloseFailureIsVisible(t *testing.T) {
	ops := realWindowsPeerOps
	ops.closeHandle = func(h windows.Handle) error {
		_ = windows.CloseHandle(h)
		return errors.New("injected close result uncertainty")
	}
	server, _, _ := windowsTestPipe(t, &ops)
	if err := server.close(); !errors.Is(err, errPeerListenerFailed) || failureStage(err) != "close-listener" {
		t.Fatalf("close got %v stage=%s, want listener failure", err, failureStage(err))
	}
}

func TestPeerIPC_ParentExitIsDetected(t *testing.T) {
	parent, _ := windowsTestParent(t)
	if err := parent.checkAlive(); err != nil {
		t.Fatalf("live parent rejected: %v", err)
	}
	// A signaled-process result is injected while preserving the pinned handle.
	ops := parent.ops
	ops.wait = func(windows.Handle, uint32) (uint32, error) { return windows.WAIT_OBJECT_0, nil }
	parent.ops = ops
	if err := parent.checkAlive(); !errors.Is(err, errPeerParentGone) {
		t.Fatalf("got %v, want ipc_parent_gone", err)
	}
}

func TestPeerIPC_ParentWatcherClosesWorkerResources(t *testing.T) {
	var dead atomic.Bool
	parent := &windowsPeerParent{handle: windows.Handle(1), pid: 42, sid: "S-1-5-21-1-2-3-1001", created: 123,
		ops: windowsPeerOps{wait: func(windows.Handle, uint32) (uint32, error) {
			if dead.Load() {
				return windows.WAIT_OBJECT_0, nil
			}
			return uint32(windows.WAIT_TIMEOUT), nil
		}},
	}
	conn := &peerTrackedConn{}
	peer, err := newAuthenticatedIPCPeer(conn, parent, 42, parent.sid)
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := watchWindowsParent(ctx, parent, func() error { closed.Add(1); return peer.close() })
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
	var dead atomic.Bool
	parent := &windowsPeerParent{handle: windows.Handle(1), pid: 42, sid: "S-1-5-21-1-2-3-1001", created: 123,
		ops: windowsPeerOps{wait: func(windows.Handle, uint32) (uint32, error) {
			if dead.Load() {
				return windows.WAIT_OBJECT_0, nil
			}
			return uint32(windows.WAIT_TIMEOUT), nil
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := watchWindowsParent(ctx, parent, func() error { return errors.New("injected close failure") })
	dead.Store(true)
	select {
	case err := <-done:
		if !errors.Is(err, errPeerListenerFailed) || failureStage(err) != "parent-exit-cleanup" {
			t.Fatalf("watcher got %v stage=%s, want cleanup failure", err, failureStage(err))
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("parent watcher did not report cleanup failure")
	}
}

func TestPeerIPC_BootstrapDeadlineKillsAndReaps(t *testing.T) {
	var readHandle, writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, nil, peerBootstrapMaxFrame); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(readHandle)
	defer windows.CloseHandle(writeHandle)
	start := time.Now()
	_, err := readWindowsBootstrapPipe(readHandle, 30*time.Millisecond)
	if !errors.Is(err, errPeerTimeout) {
		t.Fatalf("got %v, want ipc_timeout", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("bootstrap deadline exceeded one second")
	}
}

func TestPeerIPC_WindowsBootstrapReadsOneClosedFrame(t *testing.T) {
	var readHandle, writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, nil, peerBootstrapMaxFrame); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(readHandle)
	frame, err := encodePeerBootstrap(validPeerBootstrapForTest())
	if err != nil {
		t.Fatal(err)
	}
	var written uint32
	if err := windows.WriteFile(writeHandle, frame, &written, nil); err != nil {
		t.Fatal(err)
	}
	if written != uint32(len(frame)) {
		t.Fatalf("written=%d, want %d", written, len(frame))
	}
	if err := windows.CloseHandle(writeHandle); err != nil {
		t.Fatal(err)
	}
	got, err := readWindowsBootstrapPipe(readHandle, time.Second)
	if err != nil || got != validPeerBootstrapForTest() {
		t.Fatalf("read bootstrap: %#v, %v", got, err)
	}
}

func TestPeerIPC_WindowsBootstrapRejectsTrailingBytes(t *testing.T) {
	var readHandle, writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, nil, peerBootstrapMaxFrame); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(readHandle)
	frame, err := encodePeerBootstrap(validPeerBootstrapForTest())
	if err != nil {
		t.Fatal(err)
	}
	frame = append(frame, 0x7f)
	var written uint32
	if err := windows.WriteFile(writeHandle, frame, &written, nil); err != nil {
		t.Fatal(err)
	}
	if err := windows.CloseHandle(writeHandle); err != nil {
		t.Fatal(err)
	}
	if _, err := readWindowsBootstrapPipe(readHandle, time.Second); !errors.Is(err, errPeerBootstrapInvalid) {
		t.Fatalf("got %v", err)
	}
}

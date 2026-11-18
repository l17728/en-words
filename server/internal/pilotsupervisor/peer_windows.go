//go:build pilot_e2e && windows

package pilotsupervisor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsPeerOps struct {
	clientPID   func(windows.Handle) (uint32, error)
	wait        func(windows.Handle, uint32) (uint32, error)
	security    func(windows.Handle) (*windows.SECURITY_DESCRIPTOR, error)
	createPipe  func(*uint16, uint32, uint32, uint32, uint32, uint32, uint32, *windows.SecurityAttributes) (windows.Handle, error)
	closeHandle func(windows.Handle) error
}

var realWindowsPeerOps = windowsPeerOps{
	clientPID: func(h windows.Handle) (uint32, error) {
		var pid uint32
		err := windows.GetNamedPipeClientProcessId(h, &pid)
		return pid, err
	},
	wait: windows.WaitForSingleObject,
	security: func(h windows.Handle) (*windows.SECURITY_DESCRIPTOR, error) {
		return windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	},
	createPipe:  windows.CreateNamedPipe,
	closeHandle: windows.CloseHandle,
}

const (
	windowsGatewayPipeAccess = uint32(0x00100083) // FILE_READ_DATA | FILE_READ_ATTRIBUTES | FILE_WRITE_DATA | SYNCHRONIZE
	windowsClientPipeAccess  = uint32(0x00100003) // FILE_READ_DATA | FILE_WRITE_DATA | SYNCHRONIZE
	windowsPipeOpenMode      = uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED | windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
	windowsPipeMode          = uint32(windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS)
	windowsPipeInstances     = uint32(1)
)

var peekNamedPipeProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

func readWindowsBootstrapPipe(handle windows.Handle, timeout time.Duration) (peerBootstrap, error) {
	if handle == 0 || handle == windows.InvalidHandle || timeout <= 0 {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	deadline := time.Now().Add(timeout)
	readExact := func(dst []byte) (bool, error) {
		for len(dst) > 0 {
			available, eof, err := peekWindowsPipe(handle)
			if err != nil {
				return false, errPeerBootstrapInvalid
			}
			if available >= uint32(len(dst)) {
				var read uint32
				if err := windows.ReadFile(handle, dst, &read, nil); err != nil || read == 0 {
					return false, errPeerBootstrapInvalid
				}
				dst = dst[read:]
				continue
			}
			if eof {
				return false, errPeerBootstrapInvalid
			}
			if !time.Now().Before(deadline) {
				return false, errPeerTimeout
			}
			time.Sleep(10 * time.Millisecond)
		}
		return true, nil
	}
	var prefix [4]byte
	complete, err := readExact(prefix[:])
	if err == errPeerTimeout {
		return peerBootstrap{}, err
	}
	if err != nil || !complete {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	n := int(prefix[0])<<24 | int(prefix[1])<<16 | int(prefix[2])<<8 | int(prefix[3])
	if n <= 0 || n > peerBootstrapMaxBody {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	body := make([]byte, n)
	complete, err = readExact(body)
	if err == errPeerTimeout {
		return peerBootstrap{}, err
	}
	if err != nil || !complete {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	for {
		available, eof, err := peekWindowsPipe(handle)
		if err != nil {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		if available != 0 {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		if eof {
			break
		}
		if !time.Now().Before(deadline) {
			return peerBootstrap{}, errPeerTimeout
		}
		time.Sleep(10 * time.Millisecond)
	}
	return decodePeerBootstrapBody(body)
}

func peekWindowsPipe(handle windows.Handle) (uint32, bool, error) {
	var available uint32
	r, _, callErr := peekNamedPipeProc.Call(uintptr(handle), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
	if r == 0 {
		if callErr == windows.ERROR_BROKEN_PIPE {
			return 0, true, nil
		}
		return 0, false, callErr
	}
	return available, false, nil
}

type windowsPeerParent struct {
	mu      sync.Mutex
	handle  windows.Handle
	pid     uint32
	sid     string
	created uint64
	ops     windowsPeerOps
}

func newWindowsPeerParent(handle windows.Handle, pid uint32, sid string, created uint64, ops *windowsPeerOps) (*windowsPeerParent, error) {
	if handle == 0 || handle == windows.InvalidHandle || pid == 0 || sid == "" {
		return nil, errPeerRejected
	}
	actualPID, err := windows.GetProcessId(handle)
	if err != nil || actualPID != pid {
		return nil, errPeerParentGone
	}
	selected := realWindowsPeerOps
	if ops != nil {
		selected = *ops
	}
	p := &windowsPeerParent{handle: handle, pid: pid, sid: sid, created: created, ops: selected}
	actualSID, actualCreated, err := windowsProcessIdentity(handle)
	if err != nil || actualSID != sid || actualCreated != created || p.checkAlive() != nil {
		return nil, errPeerParentGone
	}
	return p, nil
}

func (p *windowsPeerParent) checkAlive() error {
	if p == nil {
		return errPeerParentGone
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || p.handle == windows.InvalidHandle {
		return errPeerParentGone
	}
	result, err := p.ops.wait(p.handle, 0)
	if err != nil || result == windows.WAIT_OBJECT_0 {
		return errPeerParentGone
	}
	if result != uint32(windows.WAIT_TIMEOUT) {
		return errPeerParentGone
	}
	return nil
}

func (p *windowsPeerParent) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || p.handle == windows.InvalidHandle {
		return nil
	}
	h := p.handle
	p.handle = windows.InvalidHandle
	return windows.CloseHandle(h)
}

func (p *windowsPeerParent) identity() (string, uint64, error) {
	if p == nil {
		return "", 0, errPeerParentGone
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || p.handle == windows.InvalidHandle {
		return "", 0, errPeerParentGone
	}
	return windowsProcessIdentity(p.handle)
}

func windowsProcessIdentity(process windows.Handle) (string, uint64, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", 0, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", 0, err
	}
	sid := user.User.Sid.String()
	var created, exited, kernel, userTime windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &userTime); err != nil {
		return "", 0, err
	}
	return sid, uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

func windowsEndpoint(runEpoch, generation, nonce string) (string, error) {
	if !epochPattern.MatchString(runEpoch) || !workerGenerationPattern.MatchString(generation) || !noncePattern.MatchString(nonce) {
		return "", errPeerBootstrapInvalid
	}
	return `\\.\pipe\enwords-` + runEpoch + "-" + generation + "-" + nonce, nil
}

type windowsPipeServer struct {
	mu             sync.Mutex
	handle         windows.Handle
	pending        *windows.Overlapped
	accepting      bool
	acceptDone     chan struct{}
	closeRequested bool
	name           string
	gatewaySID     string
	ops            windowsPeerOps
}

func createWindowsPeerPipe(name, gatewaySID string, ops *windowsPeerOps) (*windowsPipeServer, error) {
	if !strings.HasPrefix(name, `\\.\pipe\enwords-`) || gatewaySID == "" {
		return nil, peerFailure{code: errPeerListenerFailed, stage: "validate-name-or-gateway-sid"}
	}
	if _, err := windows.StringToSid(gatewaySID); err != nil {
		return nil, peerFailure{code: errPeerListenerFailed, stage: "parse-gateway-sid"}
	}
	sddl := fmt.Sprintf("D:P(A;;0x%08X;;;%s)(A;;0x001F01FF;;;SY)", windowsGatewayPipeAccess, gatewaySID)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, peerFailure{code: errPeerListenerFailed, stage: "build-security-descriptor"}
	}
	// SecurityDescriptorFromString already returns a self-relative descriptor.
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd, InheritHandle: 0}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, peerFailure{code: errPeerListenerFailed, stage: "encode-pipe-name"}
	}
	selected := realWindowsPeerOps
	if ops != nil {
		selected = *ops
	}
	h, err := selected.createPipe(namePtr, windowsPipeOpenMode, windowsPipeMode, windowsPipeInstances, 4096, 4096, 0, &sa)
	if err != nil {
		return nil, peerFailure{code: errPeerListenerFailed, stage: "create-named-pipe"}
	}
	server := &windowsPipeServer{handle: h, name: name, gatewaySID: gatewaySID, ops: selected}
	if err := verifyWindowsPipeDACL(h, gatewaySID, selected); err != nil {
		if closeErr := selected.closeHandle(h); closeErr != nil {
			return nil, peerFailure{code: errPeerListenerFailed, stage: "close-listener-after-dacl-failure", cause: closeErr}
		}
		return nil, err
	}
	return server, nil
}

func verifyWindowsPipeDACL(handle windows.Handle, gatewaySID string, ops windowsPeerOps) error {
	sd, err := ops.security(handle)
	if err != nil || sd == nil {
		return peerFailure{code: errPeerListenerFailed, stage: "read-security-descriptor"}
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return peerFailure{code: errPeerListenerFailed, stage: "verify-protected-dacl"}
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return peerFailure{code: errPeerListenerFailed, stage: "verify-dacl-ace-count"}
	}
	systemSID, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return peerFailure{code: errPeerListenerFailed, stage: "parse-system-sid"}
	}
	gatewaySIDObj, err := windows.StringToSid(gatewaySID)
	if err != nil || gatewaySIDObj.Equals(systemSID) {
		return peerFailure{code: errPeerListenerFailed, stage: "validate-gateway-sid"}
	}
	gotGateway, gotSystem := false, false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return peerFailure{code: errPeerListenerFailed, stage: "verify-allow-ace"}
		}
		sid := (*windows.SID)(unsafe.Pointer(uintptr(unsafe.Pointer(ace)) + unsafe.Offsetof(ace.SidStart)))
		if sid.Equals(gatewaySIDObj) && ace.Mask == windows.ACCESS_MASK(windowsGatewayPipeAccess) {
			gotGateway = true
		} else if sid.Equals(systemSID) && ace.Mask == windows.ACCESS_MASK(0x001F01FF) {
			gotSystem = true
		} else {
			return peerFailure{code: errPeerListenerFailed, stage: "verify-ace-sid-and-mask"}
		}
	}
	if !gotGateway || !gotSystem {
		return peerFailure{code: errPeerListenerFailed, stage: "verify-allowlist-complete"}
	}
	return nil
}

func (s *windowsPipeServer) close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	h := s.handle
	pending := s.pending
	if s.accepting {
		s.closeRequested = true
		done := s.acceptDone
		s.mu.Unlock()
		if pending != nil {
			cancelErr := windows.CancelIoEx(h, pending)
			if cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
				// Still join accept; report the cancellation failure only after
				// the listener owner has completed its cleanup path.
				if done != nil {
					select {
					case <-done:
					case <-time.After(time.Second):
						return errPeerTimeout
					}
				}
				return peerFailure{code: errPeerListenerFailed, stage: "cancel-pending-connect", cause: cancelErr}
			}
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(time.Second):
				return errPeerTimeout
			}
		}
		s.mu.Lock()
		h = s.handle
		if h == 0 || h == windows.InvalidHandle {
			s.mu.Unlock()
			return nil
		}
		s.handle = windows.InvalidHandle
		s.mu.Unlock()
		cancelErr := windows.CancelIoEx(h, nil)
		closeErr := s.ops.closeHandle(h)
		if cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
			return peerFailure{code: errPeerListenerFailed, stage: "cancel-listener-io", cause: cancelErr}
		}
		if closeErr != nil {
			return peerFailure{code: errPeerListenerFailed, stage: "close-listener", cause: closeErr}
		}
		return closeErr
	}
	if h == 0 || h == windows.InvalidHandle {
		s.mu.Unlock()
		return nil
	}
	s.handle = windows.InvalidHandle
	s.mu.Unlock()
	cancelErr := windows.CancelIoEx(h, nil)
	closeErr := s.ops.closeHandle(h)
	if cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
		return peerFailure{code: errPeerListenerFailed, stage: "cancel-listener-io", cause: cancelErr}
	}
	if closeErr != nil {
		return peerFailure{code: errPeerListenerFailed, stage: "close-listener", cause: closeErr}
	}
	return closeErr
}

func (s *windowsPipeServer) retireHandle(h windows.Handle) error {
	if s == nil || h == 0 || h == windows.InvalidHandle {
		return nil
	}
	s.mu.Lock()
	if s.handle != h {
		s.mu.Unlock()
		return nil
	}
	s.handle = windows.InvalidHandle
	s.closeRequested = true
	s.mu.Unlock()
	cancelErr := windows.CancelIoEx(h, nil)
	closeErr := s.ops.closeHandle(h)
	if cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
		if closeErr != nil {
			return fmt.Errorf("cancel pending pipe I/O: %v; close handle: %w", cancelErr, closeErr)
		}
		return fmt.Errorf("cancel pending pipe I/O: %w", cancelErr)
	}
	return closeErr
}

// cancelConnect returns true when a background reaper owns event/ov. This
// keeps kernel-referenced OVERLAPPED storage alive without blocking worker
// shutdown beyond the bounded cancellation budget.
func (s *windowsPipeServer) cancelConnect(h windows.Handle, ov *windows.Overlapped, event windows.Handle) (bool, error) {
	if h == 0 || h == windows.InvalidHandle {
		return false, nil
	}
	cancelErr := windows.CancelIoEx(h, ov)
	result, waitErr := windows.WaitForSingleObject(event, 1000)
	if waitErr == nil && result == windows.WAIT_OBJECT_0 {
		var transferred uint32
		_ = windows.GetOverlappedResult(h, ov, &transferred, false)
		if err := s.retireHandle(h); err != nil {
			return false, peerFailure{code: errPeerListenerFailed, stage: "retire-cancelled-pipe", cause: err}
		}
		if cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
			return false, peerFailure{code: errPeerListenerFailed, stage: "cancel-connect-io", cause: cancelErr}
		}
		return false, nil
	}
	retireErr := s.retireHandle(h)
	go func(overlapped *windows.Overlapped, eventHandle windows.Handle) {
		_, _ = windows.WaitForSingleObject(eventHandle, windows.INFINITE)
		// The pipe handle has been retired and its numeric value may be
		// reused. Event completion is sufficient to release OVERLAPPED;
		// never query completion through the stale handle.
		_ = overlapped
		_ = windows.CloseHandle(eventHandle)
	}(ov, event)
	if retireErr != nil {
		return true, peerFailure{code: errPeerListenerFailed, stage: "retire-pending-pipe", cause: retireErr}
	}
	if cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
		return true, peerFailure{code: errPeerListenerFailed, stage: "cancel-connect-io", cause: cancelErr}
	}
	if waitErr != nil {
		return true, peerFailure{code: errPeerListenerFailed, stage: "wait-cancelled-connect", cause: waitErr}
	}
	if result != uint32(windows.WAIT_TIMEOUT) {
		return true, peerFailure{code: errPeerListenerFailed, stage: "wait-cancelled-connect"}
	}
	return true, nil
}

func (s *windowsPipeServer) accept(ctx context.Context, timeout time.Duration, parent *windowsPeerParent) (authenticatedIPCPeer, error) {
	if s == nil || parent == nil {
		return authenticatedIPCPeer{}, errPeerListenerFailed
	}
	ov := new(windows.Overlapped)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "create-connect-event"}
	}
	closeEvent := true
	defer func() {
		if closeEvent {
			_ = windows.CloseHandle(event)
		}
	}()
	ov.HEvent = event
	s.mu.Lock()
	h := s.handle
	if h == 0 || h == windows.InvalidHandle || s.closeRequested || s.accepting {
		s.mu.Unlock()
		return authenticatedIPCPeer{}, errPeerListenerFailed
	}
	s.accepting = true
	done := make(chan struct{})
	s.acceptDone = done
	s.pending = ov
	defer func() {
		s.mu.Lock()
		if s.acceptDone == done {
			s.pending = nil
			s.accepting = false
			s.acceptDone = nil
		}
		close(done)
		s.mu.Unlock()
	}()
	err = windows.ConnectNamedPipe(h, ov)
	if err != nil && err != windows.ERROR_IO_PENDING && err != windows.ERROR_PIPE_CONNECTED {
		s.pending = nil
		s.mu.Unlock()
		if closeErr := s.retireHandle(h); closeErr != nil {
			return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "retire-after-connect-error", cause: closeErr}
		}
		return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "connect-named-pipe"}
	}
	if err != windows.ERROR_IO_PENDING {
		s.pending = nil
	}
	closedAtStart := s.closeRequested
	s.mu.Unlock()
	cancelPending := func() error {
		transferred, cancelErr := s.cancelConnect(h, ov, event)
		if transferred {
			closeEvent = false
		}
		return cancelErr
	}
	if h == 0 || h == windows.InvalidHandle {
		return authenticatedIPCPeer{}, errPeerListenerFailed
	}
	if closedAtStart {
		if closeErr := s.retireHandle(h); closeErr != nil {
			return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "retire-closed-listener", cause: closeErr}
		}
		return authenticatedIPCPeer{}, errPeerCancelled
	}
	deadline := time.Now().Add(timeout)
	if err == windows.ERROR_IO_PENDING {
		for {
			if ctx.Err() != nil {
				if cancelErr := cancelPending(); cancelErr != nil {
					return authenticatedIPCPeer{}, cancelErr
				}
				return authenticatedIPCPeer{}, errPeerCancelled
			}
			if parent.checkAlive() != nil {
				if cancelErr := cancelPending(); cancelErr != nil {
					return authenticatedIPCPeer{}, cancelErr
				}
				return authenticatedIPCPeer{}, errPeerParentGone
			}
			if time.Now().After(deadline) {
				if cancelErr := cancelPending(); cancelErr != nil {
					return authenticatedIPCPeer{}, cancelErr
				}
				return authenticatedIPCPeer{}, errPeerTimeout
			}
			result, waitErr := windows.WaitForSingleObject(event, 100)
			if waitErr != nil {
				if cancelErr := cancelPending(); cancelErr != nil {
					return authenticatedIPCPeer{}, cancelErr
				}
				return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "wait-connect-event"}
			}
			if result == windows.WAIT_OBJECT_0 {
				break
			}
			if result != uint32(windows.WAIT_TIMEOUT) {
				if cancelErr := cancelPending(); cancelErr != nil {
					return authenticatedIPCPeer{}, cancelErr
				}
				return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "unexpected-connect-wait-result"}
			}
		}
		var transferred uint32
		if completeErr := windows.GetOverlappedResult(h, ov, &transferred, false); completeErr != nil {
			s.mu.Lock()
			closing := s.closeRequested
			s.pending = nil
			s.mu.Unlock()
			if closeErr := s.retireHandle(h); closeErr != nil {
				return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "retire-after-connect-completion-error", cause: closeErr}
			}
			if ctx.Err() != nil || closing {
				return authenticatedIPCPeer{}, errPeerCancelled
			}
			if parent.checkAlive() != nil {
				return authenticatedIPCPeer{}, errPeerParentGone
			}
			return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "complete-connect-overlapped"}
		}
	}
	s.mu.Lock()
	s.pending = nil
	if s.closeRequested {
		s.mu.Unlock()
		if closeErr := s.retireHandle(h); closeErr != nil {
			return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "retire-after-close-request", cause: closeErr}
		}
		if parent.checkAlive() != nil {
			return authenticatedIPCPeer{}, errPeerParentGone
		}
		return authenticatedIPCPeer{}, errPeerCancelled
	}
	s.mu.Unlock()
	peer, pipeTransferred, authErr := authenticateWindowsPeer(h, s.gatewaySID, parent, s.ops)
	if authErr != nil {
		if pipeTransferred {
			s.mu.Lock()
			if s.handle == h {
				s.handle = windows.InvalidHandle
			}
			s.mu.Unlock()
		} else {
			if closeErr := s.retireHandle(h); closeErr != nil {
				return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "retire-after-peer-auth-error", cause: closeErr}
			}
		}
		return authenticatedIPCPeer{}, authErr
	}
	s.mu.Lock()
	owned := s.handle == h
	closing := s.closeRequested
	if owned {
		// The os.File in peer now owns the HANDLE. Relinquish listener
		// ownership before any further Close can observe it.
		s.handle = windows.InvalidHandle
	}
	s.mu.Unlock()
	if !owned || closing {
		if closeErr := peer.close(); closeErr != nil {
			return authenticatedIPCPeer{}, peerFailure{code: errPeerListenerFailed, stage: "close-cancelled-peer", cause: closeErr}
		}
		if parent.checkAlive() != nil {
			return authenticatedIPCPeer{}, errPeerParentGone
		}
		return authenticatedIPCPeer{}, errPeerCancelled
	}
	return peer, nil
}

// watchWindowsParent polls the pinned process handle while a worker is idle
// or a peer is connected. The callback must cancel accept and close both the
// listener and any active peer connection before reporting parent loss.
func watchWindowsParent(ctx context.Context, parent *windowsPeerParent, closeWorkerResources func() error) <-chan error {
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		select {
		case <-ctx.Done():
			done <- errPeerCancelled
		case <-ticker.C:
			for {
				if parent.checkAlive() != nil {
					if closeErr := closeWorkerResources(); closeErr != nil {
						done <- peerFailure{code: errPeerListenerFailed, stage: "parent-exit-cleanup", cause: closeErr}
						return
					}
					done <- errPeerParentGone
					return
				}
				select {
				case <-ctx.Done():
					done <- errPeerCancelled
					return
				case <-ticker.C:
				}
			}
		}
	}()
	return done
}

func authenticateWindowsPeer(pipe windows.Handle, expectedSID string, parent *windowsPeerParent, ops windowsPeerOps) (authenticatedIPCPeer, bool, error) {
	if pipe == 0 || parent == nil || parent.checkAlive() != nil {
		return authenticatedIPCPeer{}, false, errPeerParentGone
	}
	pid1, err := ops.clientPID(pipe)
	if err != nil || pid1 == 0 || pid1 != parent.pid {
		return authenticatedIPCPeer{}, false, errPeerRejected
	}
	sid, created, err := parent.identity()
	if err != nil || sid != expectedSID || sid != parent.sid || created != parent.created {
		return authenticatedIPCPeer{}, false, errPeerRejected
	}
	pid2, err := ops.clientPID(pipe)
	if err != nil || pid2 != pid1 || pid2 != parent.pid || parent.checkAlive() != nil {
		if parent.checkAlive() != nil {
			return authenticatedIPCPeer{}, false, errPeerParentGone
		}
		return authenticatedIPCPeer{}, false, errPeerRejected
	}
	file := os.NewFile(uintptr(pipe), "authenticated-peer-pipe")
	if file == nil {
		return authenticatedIPCPeer{}, false, errPeerRejected
	}
	peer, err := newAuthenticatedIPCPeer(file, parent, pid1, sid)
	if err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return authenticatedIPCPeer{}, true, peerFailure{code: errPeerListenerFailed, stage: "close-unaccepted-peer-handle", cause: closeErr}
		}
		return authenticatedIPCPeer{}, true, err
	}
	return peer, true, nil
}

func openWindowsPipeClient(name string) (windows.Handle, error) {
	return openWindowsPipeClientWith(name, windows.CreateFile)
}

func openWindowsPipeClientWith(name string, createFile func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error)) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, peerFailure{code: errPeerListenerFailed, stage: "encode-pipe-name", cause: err}
	}
	h, err := createFile(namePtr, windowsClientPipeAccess, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return 0, peerFailure{code: errPeerListenerFailed, stage: "open-named-pipe-client", cause: err}
	}
	return h, nil
}

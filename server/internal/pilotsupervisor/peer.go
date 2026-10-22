//go:build pilot_e2e

package pilotsupervisor

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errPeerRejected       = errors.New("ipc_peer_rejected")
	errPeerClosed         = errors.New("ipc_peer_closed")
	errPeerParentGone     = errors.New("ipc_parent_gone")
	errPeerListenerFailed = errors.New("ipc_listener_failed")
	errPeerCancelled      = errors.New("ipc_cancelled")
	errPeerTimeout        = errors.New("ipc_timeout")
)

type peerLiveness interface {
	checkAlive() error
	close() error
}

type peerFailure struct {
	code  error
	stage string
	cause error
}

func (e peerFailure) Error() string { return e.code.Error() }
func (e peerFailure) Unwrap() error { return e.code }
func failureStage(err error) string {
	var failure peerFailure
	if errors.As(err, &failure) {
		return failure.stage
	}
	return ""
}
func failureCause(err error) error {
	var failure peerFailure
	if errors.As(err, &failure) {
		return failure.cause
	}
	return nil
}

type peerConnState struct {
	conn         io.ReadWriteCloser
	ctx          context.Context
	cancel       context.CancelFunc
	parent       peerLiveness
	pid          uint32
	principal    string
	generation   uint64
	closed       atomic.Bool
	grantUsed    atomic.Bool
	dispatched   atomic.Bool
	dispatchDone chan struct{}
	closeOnce    sync.Once
	closeErr     error
}

// authenticatedIPCPeer is an opaque capability issued only by a platform
// adapter after checking the OS identity of a real, accepted connection.
// Keeping the connection object, rather than an integer handle, prevents
// descriptor reuse from transferring authorization to another connection.
type authenticatedIPCPeer struct{ state *peerConnState }

var nextPeerGeneration atomic.Uint64

func newAuthenticatedIPCPeer(conn io.ReadWriteCloser, parent peerLiveness, pid uint32, principal string) (authenticatedIPCPeer, error) {
	if conn == nil || parent == nil || pid == 0 || principal == "" {
		return authenticatedIPCPeer{}, errPeerRejected
	}
	if err := parent.checkAlive(); err != nil {
		return authenticatedIPCPeer{}, errPeerParentGone
	}
	ctx, cancel := context.WithCancel(context.Background())
	return authenticatedIPCPeer{state: &peerConnState{
		conn: conn, parent: parent, pid: pid, principal: principal,
		ctx: ctx, cancel: cancel,
		generation: nextPeerGeneration.Add(1), dispatchDone: make(chan struct{}),
	}}, nil
}

func (p authenticatedIPCPeer) claimGrant() error {
	if p.state == nil {
		return errPeerRejected
	}
	if p.state.closed.Load() {
		return errPeerRejected
	}
	if err := p.state.parent.checkAlive(); err != nil {
		return errPeerParentGone
	}
	if !p.state.grantUsed.CompareAndSwap(false, true) {
		return errPeerRejected
	}
	return nil
}

func (p authenticatedIPCPeer) close() error {
	if p.state == nil {
		return nil
	}
	p.state.closeOnce.Do(func() {
		p.state.closed.Store(true)
		if p.state.cancel != nil {
			p.state.cancel()
		}
		if p.state.conn != nil {
			if err := p.state.conn.Close(); err != nil {
				p.state.closeErr = peerFailure{code: errPeerListenerFailed, stage: "peer-close", cause: err}
			}
		}
		// Cancellation and Close unblock a cooperative authorized operation.
		// Keep shutdown bounded; the outer worker watchdog must terminate and
		// reap a worker whose callback ignores cancellation.
		if p.state.dispatched.Load() {
			select {
			case <-p.state.dispatchDone:
			case <-time.After(time.Second):
				if p.state.closeErr == nil {
					p.state.closeErr = errPeerTimeout
				}
			}
		}
	})
	return p.state.closeErr
}

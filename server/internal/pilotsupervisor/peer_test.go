//go:build pilot_e2e

package pilotsupervisor

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type peerTrackedConn struct{ closed atomic.Bool }

func (*peerTrackedConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (*peerTrackedConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *peerTrackedConn) Close() error              { c.closed.Store(true); return nil }

type testPeerLiveness struct{ dead atomic.Bool }

func (p *testPeerLiveness) checkAlive() error {
	if p.dead.Load() {
		return errPeerParentGone
	}
	return nil
}
func (*testPeerLiveness) close() error { return nil }

func TestPeerFailureKeepsPublicCodeAndPrivateCauseSeparate(t *testing.T) {
	cause := errors.New("private socket detail")
	failure := peerFailure{code: errPeerListenerFailed, stage: "accept-peer", cause: cause}
	if got := failure.Error(); got != errPeerListenerFailed.Error() {
		t.Fatalf("public error=%q, want stable code %q", got, errPeerListenerFailed)
	}
	if !errors.Is(failure, errPeerListenerFailed) {
		t.Fatal("wrapped public code is not discoverable with errors.Is")
	}
	if got := failureStage(failure); got != "accept-peer" {
		t.Fatalf("failure stage=%q, want accept-peer", got)
	}
	if got := failureCause(failure); got != cause {
		t.Fatalf("private cause=%v, want original cause", got)
	}
	if got := failureCause(errPeerRejected); got != nil {
		t.Fatalf("ordinary error unexpectedly has private cause %v", got)
	}
}

func newTestPeer(t *testing.T) (authenticatedIPCPeer, *testPeerLiveness) {
	t.Helper()
	parent := &testPeerLiveness{}
	peer, err := newAuthenticatedIPCPeer(grantTestConn{}, parent, 42, "uid:1000")
	if err != nil {
		t.Fatal(err)
	}
	return peer, parent
}

func TestPeerProofSingleGrantAndDispatch(t *testing.T) {
	peer, _ := newTestPeer(t)
	if err := peer.claimGrant(); err != nil {
		t.Fatal(err)
	}
	if err := peer.claimGrant(); !errors.Is(err, errPeerRejected) {
		t.Fatalf("second grant claim: %v", err)
	}
	grant := &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
	if err := grant.dispatch(1, func(context.Context, grantWire) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := grant.dispatch(1, func(context.Context, grantWire) error { return nil }); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("second dispatch: %v", err)
	}
}

func TestPeerProofRejectsClosedOrDeadParent(t *testing.T) {
	t.Run("closed connection", func(t *testing.T) {
		peer, _ := newTestPeer(t)
		if err := peer.close(); err != nil {
			t.Fatal(err)
		}
		if err := peer.claimGrant(); !errors.Is(err, errPeerRejected) {
			t.Fatalf("claim after close: %v", err)
		}
	})
	t.Run("parent exited", func(t *testing.T) {
		peer, parent := newTestPeer(t)
		parent.dead.Store(true)
		if err := peer.claimGrant(); !errors.Is(err, errPeerParentGone) {
			t.Fatalf("claim after parent exit: %v", err)
		}
	})
}

func TestPeerProofConcurrentDispatchHasOneWinner(t *testing.T) {
	peer, _ := newTestPeer(t)
	if err := peer.claimGrant(); err != nil {
		t.Fatal(err)
	}
	grant := &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if grant.dispatch(1, func(context.Context, grantWire) error { return nil }) == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("dispatch winners=%d, want 1", got)
	}
}

func TestPeerCloseCancelsInFlightAuthorizedAction(t *testing.T) {
	peer, _ := newTestPeer(t)
	if err := peer.claimGrant(); err != nil {
		t.Fatal(err)
	}
	grant := &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
	started := make(chan struct{})
	proceed := make(chan struct{})
	dispatched := make(chan error, 1)
	var sideEffects atomic.Int32
	go func() {
		dispatched <- grant.dispatch(1, func(ctx context.Context, _ grantWire) error {
			close(started)
			<-proceed
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sideEffects.Add(1)
			return nil
		})
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- peer.close() }()
	select {
	case <-peer.state.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel the action context")
	}
	close(proceed)
	select {
	case err := <-dispatched:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dispatch got %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel in-flight action")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("peer close remained blocked after cancellation")
	}
	if got := sideEffects.Load(); got != 0 {
		t.Fatalf("canceled action performed %d side effects", got)
	}
}

func TestPeerCloseBoundsUncooperativeAuthorizedAction(t *testing.T) {
	peer, _ := newTestPeer(t)
	if err := peer.claimGrant(); err != nil {
		t.Fatal(err)
	}
	grant := &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
	started := make(chan struct{})
	finish := make(chan struct{})
	dispatched := make(chan error, 1)
	go func() {
		dispatched <- grant.dispatch(1, func(context.Context, grantWire) error {
			close(started)
			<-finish // models an operation that violates the cancellation contract
			return nil
		})
	}()
	<-started
	start := time.Now()
	if err := peer.close(); !errors.Is(err, errPeerTimeout) {
		t.Fatalf("close got %v, want ipc_timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("uncooperative dispatch held close for %s", elapsed)
	}
	close(finish)
	if err := <-dispatched; err != nil {
		t.Fatal(err)
	}
}

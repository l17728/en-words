//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

const testEpoch = "0123456789abcdef0123456789abcdef"
const testWorkerGeneration = "abcdef0123456789abcdef0123456789"

type grantTestConn struct{}

func (grantTestConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (grantTestConn) Write(p []byte) (int, error) { return len(p), nil }
func (grantTestConn) Close() error                { return nil }

type grantTestParent struct{}

func (grantTestParent) checkAlive() error { return nil }
func (grantTestParent) close() error      { return nil }

// testAuthenticatedPeer is deliberately local to grant protocol tests. OS
// adapter tests exercise proofs issued from real accepted connections.
func testAuthenticatedPeer() authenticatedIPCPeer {
	peer, _ := newAuthenticatedIPCPeer(grantTestConn{}, grantTestParent{}, 1, "test-principal")
	return peer
}

func testDigest(t *testing.T, scope Scope, payload string) string {
	t.Helper()
	digest, err := canonicalRequestDigest(scope, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func testGeneration(fields GrantFields) string {
	if fields.WorkerGeneration != "" {
		return fields.WorkerGeneration
	}
	return testWorkerGeneration
}

func bootstrapForFields(fields GrantFields) peerBootstrap {
	b := validPeerBootstrapForTest()
	b.RunEpoch = fields.RunEpoch
	b.WorkerGeneration = testGeneration(fields)
	return b
}

func bootstrapForGeneration(generation string) peerBootstrap {
	b := validPeerBootstrapForTest()
	b.WorkerGeneration = generation
	return b
}

func issueTestGrant(t *testing.T, key ed25519.PrivateKey, fields GrantFields) ([]byte, []byte) {
	t.Helper()
	if fields.WorkerGeneration == "" {
		fields.WorkerGeneration = testWorkerGeneration
	}
	raw, signature, err := IssueGrant(key, fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw, signature
}

func verifyTestGrant(pub ed25519.PublicKey, raw, signature []byte, fields GrantFields, expectedPrincipal, expectedCase string, now int64, peer authenticatedIPCPeer) (*VerifiedGrant, error) {
	return VerifyGrant(pub, raw, signature, validPeerBootstrapForTest(), expectedPrincipal, fields.Scope, expectedCase, fields.RequestDigest, now, peer)
}

func TestGrantRoundTripCreateAndCaseScopes(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fieldsList := []GrantFields{
		{RunEpoch: testEpoch, RequestID: "req_01", PrincipalRef: "student_9", Scope: ScopeCreate, ExpiresAtMS: 70_000, RequestDigest: testDigest(t, ScopeCreate, `{"book_id":7,"student_id":9}`)},
		{RunEpoch: testEpoch, RequestID: "req_02", PrincipalRef: "student_9", Scope: ScopeStart, ExpiresAtMS: 70_000, RequestDigest: testDigest(t, ScopeStart, `{"case_id":"abcdef0123456789abcdef0123456789"}`), CaseID: "abcdef0123456789abcdef0123456789"},
	}
	for i, scope := range []Scope{ScopeReceiptRead, ScopeRecovery, ScopeCleanup} {
		caseID := "abcdef0123456789abcdef0123456789"
		fieldsList = append(fieldsList, GrantFields{RunEpoch: testEpoch, RequestID: fmt.Sprintf("req_scope_%d", i), PrincipalRef: "teacher_1", Scope: scope, ExpiresAtMS: 70_000, RequestDigest: testDigest(t, scope, `{"case_id":"abcdef0123456789abcdef0123456789"}`), CaseID: caseID})
	}
	for _, fields := range fieldsList {
		raw, sig := issueTestGrant(t, key, fields)
		grant, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, fields.CaseID, 10_000, testAuthenticatedPeer())
		if err != nil {
			t.Fatalf("verify %s: %v", fields.Scope, err)
		}
		var claims grantWire
		err = grant.dispatch(10_000, func(_ context.Context, w grantWire) error { claims = w; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if claims.Scope != fields.Scope || claims.RequestID != fields.RequestID || claims.PrincipalRef != fields.PrincipalRef || claims.CaseID != fields.CaseID {
			t.Fatalf("claims mismatch: %#v", claims)
		}
		if err := grant.dispatch(10_000, func(context.Context, grantWire) error { return nil }); !errors.Is(err, errGrantInvalid) {
			t.Fatalf("grant reused: %v", err)
		}
	}
}

func TestGrantRejectsContextMismatchAndExpiryBoundaries(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fields := GrantFields{RunEpoch: testEpoch, RequestID: "req_03", PrincipalRef: "teacher_1", Scope: ScopeCreate, ExpiresAtMS: 60_000, RequestDigest: testDigest(t, ScopeCreate, `{"x":1}`)}
	raw, sig := issueTestGrant(t, key, fields)
	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 0x80
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name             string
		pub              ed25519.PublicKey
		raw, sig         []byte
		principal, epoch string
		scope            Scope
		caseID, digest   string
		now              int64
		peer             authenticatedIPCPeer
	}{
		{"signature", pub, raw, badSig, fields.PrincipalRef, testEpoch, ScopeCreate, "", fields.RequestDigest, 1, testAuthenticatedPeer()},
		{"public key", wrongPub, raw, sig, fields.PrincipalRef, testEpoch, ScopeCreate, "", fields.RequestDigest, 1, testAuthenticatedPeer()},
		{"run", pub, raw, sig, fields.PrincipalRef, "ffffffffffffffffffffffffffffffff", ScopeCreate, "", fields.RequestDigest, 1, testAuthenticatedPeer()},
		{"principal", pub, raw, sig, "another_student", testEpoch, ScopeCreate, "", fields.RequestDigest, 1, testAuthenticatedPeer()},
		{"scope", pub, raw, sig, fields.PrincipalRef, testEpoch, ScopeCleanup, "", fields.RequestDigest, 1, testAuthenticatedPeer()},
		{"case selector", pub, raw, sig, fields.PrincipalRef, testEpoch, ScopeCreate, "abcdef0123456789abcdef0123456789", fields.RequestDigest, 1, testAuthenticatedPeer()},
		{"digest", pub, raw, sig, fields.PrincipalRef, testEpoch, ScopeCreate, "", testDigest(t, ScopeCreate, `{"x":2}`), 1, testAuthenticatedPeer()},
		{"expired", pub, raw, sig, fields.PrincipalRef, testEpoch, ScopeCreate, "", fields.RequestDigest, 60_000, testAuthenticatedPeer()},
		{"unauthenticated peer", pub, raw, sig, fields.PrincipalRef, testEpoch, ScopeCreate, "", fields.RequestDigest, 1, authenticatedIPCPeer{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := fields
			candidate.RunEpoch = tt.epoch
			candidate.Scope = tt.scope
			candidate.RequestDigest = tt.digest
			if _, err := VerifyGrant(tt.pub, tt.raw, tt.sig, bootstrapForFields(candidate), tt.principal, candidate.Scope, tt.caseID, candidate.RequestDigest, tt.now, tt.peer); !errors.Is(err, errGrantInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", 0, testAuthenticatedPeer()); err != nil {
		t.Fatalf("60s exact boundary rejected: %v", err)
	}
	if _, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", 1, testAuthenticatedPeer()); err != nil {
		t.Fatalf("expiry-1ms rejected: %v", err)
	}
	if _, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", 59_999, testAuthenticatedPeer()); err != nil {
		t.Fatalf("now+1ms deadline rejected: %v", err)
	}
	tooLong := fields
	tooLong.ExpiresAtMS = 60_001
	raw2, sig2 := issueTestGrant(t, key, tooLong)
	if _, err := verifyTestGrant(pub, raw2, sig2, tooLong, tooLong.PrincipalRef, "", 0, testAuthenticatedPeer()); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("60s+1ms accepted: %v", err)
	}
	for _, expiry := range []int64{0, -1} {
		invalid := fields
		invalid.ExpiresAtMS = expiry
		if _, _, err := IssueGrant(key, invalid); !errors.Is(err, errGrantInvalid) {
			t.Errorf("expiry %d: got %v", expiry, err)
		}
	}
	if _, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", -1, testAuthenticatedPeer()); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("negative clock accepted: %v", err)
	}
	verified, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", 1, testAuthenticatedPeer())
	if err != nil {
		t.Fatal(err)
	}
	if err := verified.dispatch(fields.ExpiresAtMS, func(context.Context, grantWire) error { return nil }); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("expired consumption accepted: %v", err)
	}
	if err := verified.dispatch(1, func(context.Context, grantWire) error { return nil }); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("clock rollback enabled reuse: %v", err)
	}
}

func TestGrantRejectsStaleOrFutureWorkerGeneration(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fields := GrantFields{RunEpoch: testEpoch, WorkerGeneration: testWorkerGeneration, RequestID: "req_generation", PrincipalRef: "teacher_1", Scope: ScopeCreate, ExpiresAtMS: 20_000, RequestDigest: testDigest(t, ScopeCreate, `{}`)}
	raw, sig := issueTestGrant(t, key, fields)
	checks := []struct {
		name             string
		frame, signature []byte
		bootstrap        peerBootstrap
	}{}
	for _, generation := range []string{"00000000000000000000000000000000", "ffffffffffffffffffffffffffffffff", "malformed"} {
		checks = append(checks, struct {
			name             string
			frame, signature []byte
			bootstrap        peerBootstrap
		}{"bootstrap " + generation, raw, sig, bootstrapForGeneration(generation)})
	}
	missingField := bytes.Replace(raw, []byte(`,"worker_generation":"`+testWorkerGeneration+`","request_id":`), []byte(`,"request_id":`), 1)
	if bytes.Equal(missingField, raw) {
		t.Fatal("failed to construct missing-generation wire")
	}
	checks = append(checks, struct {
		name             string
		frame, signature []byte
		bootstrap        peerBootstrap
	}{"missing signed field", missingField, sig, validPeerBootstrapForTest()})
	tampered := bytes.Replace(raw, []byte(testWorkerGeneration), []byte("00000000000000000000000000000000"), 1)
	checks = append(checks, struct {
		name             string
		frame, signature []byte
		bootstrap        peerBootstrap
	}{"tampered after signing", tampered, sig, validPeerBootstrapForTest()})
	badWire := grantWire{Version: grantVersion, RunEpoch: testEpoch, WorkerGeneration: "malformed", RequestID: fields.RequestID, PrincipalRef: fields.PrincipalRef, Scope: fields.Scope, ExpiresAtMS: fields.ExpiresAtMS, RequestDigest: fields.RequestDigest}
	malformedSigned, err := json.Marshal(badWire)
	if err != nil {
		t.Fatal(err)
	}
	checks = append(checks, struct {
		name             string
		frame, signature []byte
		bootstrap        peerBootstrap
	}{"malformed signed field", malformedSigned, ed25519.Sign(key, malformedSigned), validPeerBootstrapForTest()})
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			peer := testAuthenticatedPeer()
			if _, err := VerifyGrant(pub, check.frame, check.signature, check.bootstrap, fields.PrincipalRef, fields.Scope, "", fields.RequestDigest, 1, peer); !errors.Is(err, errGrantInvalid) {
				t.Fatalf("accepted: %v", err)
			}
			if peer.state.grantUsed.Load() {
				t.Fatal("rejected grant consumed peer authorization")
			}
		})
	}
	if _, _, err := IssueGrant(key, GrantFields{RunEpoch: testEpoch, RequestID: "req_missing_generation", PrincipalRef: "teacher_1", Scope: ScopeCreate, ExpiresAtMS: 20_000, RequestDigest: fields.RequestDigest}); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("missing worker generation accepted: %v", err)
	}
}

func TestGrantCopiesShareSingleUseState(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fields := GrantFields{RunEpoch: testEpoch, RequestID: "req_copy", PrincipalRef: "admin", Scope: ScopeCreate, ExpiresAtMS: 20_000, RequestDigest: testDigest(t, ScopeCreate, `{}`)}
	raw, sig := issueTestGrant(t, key, fields)
	grant, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", 1, testAuthenticatedPeer())
	if err != nil {
		t.Fatal(err)
	}
	copyGrant := *grant
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, candidate := range []*VerifiedGrant{grant, &copyGrant} {
		wg.Add(1)
		go func(g *VerifiedGrant) {
			defer wg.Done()
			if err := g.dispatch(2, func(context.Context, grantWire) error { return nil }); err == nil {
				wins.Add(1)
			}
		}(candidate)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("successful consumes=%d, want 1", wins.Load())
	}
}

func TestGrantPreservesParentGoneClassification(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fields := GrantFields{RunEpoch: testEpoch, RequestID: "req_parent_gone", PrincipalRef: "teacher_1", Scope: ScopeCreate, ExpiresAtMS: 10_000, RequestDigest: testDigest(t, ScopeCreate, `{}`)}
	raw, sig := issueTestGrant(t, key, fields)
	parent := &testPeerLiveness{}
	peer, err := newAuthenticatedIPCPeer(grantTestConn{}, parent, 1, "test-principal")
	if err != nil {
		t.Fatal(err)
	}
	parent.dead.Store(true)
	if _, err := verifyTestGrant(pub, raw, sig, fields, fields.PrincipalRef, "", 1, peer); !errors.Is(err, errPeerParentGone) {
		t.Fatalf("got %v, want ipc_parent_gone", err)
	}
}

func TestGrantDispatchRejectsInvalidCapabilityState(t *testing.T) {
	noAction := func(context.Context, grantWire) error { return nil }
	for _, tc := range []struct {
		name  string
		grant func() *VerifiedGrant
	}{
		{name: "nil grant"},
		{name: "nil grant state", grant: func() *VerifiedGrant { return &VerifiedGrant{} }},
		{name: "nil peer state", grant: func() *VerifiedGrant { return &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}}} }},
		{name: "unclaimed peer", grant: func() *VerifiedGrant {
			peer, _ := newTestPeer(t)
			return &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
		}},
		{name: "closed peer", grant: func() *VerifiedGrant {
			peer, _ := newTestPeer(t)
			_ = peer.close()
			return &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
		}},
		{name: "already consumed", grant: func() *VerifiedGrant {
			peer, _ := newTestPeer(t)
			_ = peer.claimGrant()
			g := &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
			g.state.consumed.Store(true)
			return g
		}},
		{name: "expired at boundary", grant: func() *VerifiedGrant {
			peer, _ := newTestPeer(t)
			_ = peer.claimGrant()
			return &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 1}, peer: peer}}
		}},
		{name: "lifetime too long", grant: func() *VerifiedGrant {
			peer, _ := newTestPeer(t)
			_ = peer.claimGrant()
			return &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: maxGrantLifetimeMS + 2}, peer: peer}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var grant *VerifiedGrant
			if tc.grant != nil {
				grant = tc.grant()
			}
			if err := grant.dispatch(1, noAction); !errors.Is(err, errGrantInvalid) {
				t.Fatalf("malformed capability dispatched: %v", err)
			}
		})
	}
	if (&VerifiedGrant{state: &grantState{}}).dispatch(-1, noAction) == nil {
		t.Fatal("negative local clock accepted")
	}
	if (&VerifiedGrant{state: &grantState{}}).dispatch(1, nil) == nil {
		t.Fatal("nil action accepted")
	}
}

func TestGrantDispatchPreservesParentAndActionFailures(t *testing.T) {
	peer, parent := newTestPeer(t)
	if err := peer.claimGrant(); err != nil {
		t.Fatal(err)
	}
	grant := &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
	parent.dead.Store(true)
	if err := grant.dispatch(1, func(context.Context, grantWire) error { return nil }); !errors.Is(err, errPeerParentGone) {
		t.Fatalf("dead parent classification = %v", err)
	}

	peer, _ = newTestPeer(t)
	if err := peer.claimGrant(); err != nil {
		t.Fatal(err)
	}
	grant = &VerifiedGrant{state: &grantState{wire: grantWire{ExpiresAtMS: 100}, peer: peer}}
	want := errors.New("action failed")
	if err := grant.dispatch(1, func(context.Context, grantWire) error { return want }); !errors.Is(err, want) {
		t.Fatalf("dispatch did not preserve action failure: %v", err)
	}
	select {
	case <-peer.state.dispatchDone:
	default:
		t.Fatal("dispatch completion signal was not closed")
	}
}

func TestGrantCanonicalSchemaAndByteLimit(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := testDigest(t, ScopeCreate, `{}`)
	base := GrantFields{RunEpoch: testEpoch, RequestID: "req_04", PrincipalRef: "admin", Scope: ScopeCreate, ExpiresAtMS: 20_000, RequestDigest: digest}
	for name, edit := range map[string]func(*GrantFields){"create selector": func(f *GrantFields) { f.CaseID = "abcdef0123456789abcdef0123456789" }, "case scope no selector": func(f *GrantFields) { f.Scope = ScopeStart }, "bad case selector": func(f *GrantFields) { f.Scope = ScopeStart; f.CaseID = "not-hex" }, "unknown scope": func(f *GrantFields) { f.Scope = "admin" }, "bad digest": func(f *GrantFields) { f.RequestDigest = "xyz" }} {
		fields := base
		edit(&fields)
		if _, _, err := IssueGrant(key, fields); !errors.Is(err, errGrantInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	raw, sig := issueTestGrant(t, key, base)
	for name, altered := range map[string][]byte{"duplicate": append([]byte(`{"version":1,`), raw[1:]...), "whitespace": append([]byte(" "), raw...), "trailing": append(append([]byte(nil), raw...), []byte(` {}`)...), "unknown": append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"extra":true}`)...), "oversized": make([]byte, maxGrantBytes+1)} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyGrant(pub, altered, sig, validPeerBootstrapForTest(), base.PrincipalRef, base.Scope, "", base.RequestDigest, 1, testAuthenticatedPeer()); !errors.Is(err, errGrantInvalid) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	if _, err := VerifyGrant(pub, nil, sig, validPeerBootstrapForTest(), base.PrincipalRef, base.Scope, "", base.RequestDigest, 1, testAuthenticatedPeer()); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("empty frame accepted: %v", err)
	}
	for _, scope := range []Scope{ScopeReceiptRead, ScopeRecovery, ScopeCleanup} {
		other := testDigest(t, scope, `{}`)
		if other == digest {
			t.Fatalf("scope %s digest collision", scope)
		}
	}
}

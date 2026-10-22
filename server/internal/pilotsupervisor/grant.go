//go:build pilot_e2e

// Package pilotsupervisor contains the test-only authorization primitives for
// the isolated pilot harness. It is excluded from normal server builds.
package pilotsupervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
)

const grantVersion = 1
const maxGrantLifetimeMS int64 = 60_000
const maxGrantBytes = 4096

var (
	errGrantInvalid = errors.New("pilot grant invalid")
	epochPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	identifier      = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)
	caseIDPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Scope is intentionally a closed set. New capabilities require a reviewed
// protocol change instead of accepting arbitrary scope strings.
type Scope string

const (
	ScopeCreate      Scope = "create"
	ScopeStart       Scope = "start"
	ScopeReceiptRead Scope = "receipt_read"
	ScopeRecovery    Scope = "recovery"
	ScopeCleanup     Scope = "cleanup"
)

// grantWire uses a struct (not a map) so JSON field order is stable. The
// canonical-byte comparison rejects duplicate keys, unknown fields, alternate
// whitespace, and noncanonical encodings before signature validation.
type grantWire struct {
	Version          int    `json:"version"`
	RunEpoch         string `json:"run_epoch"`
	WorkerGeneration string `json:"worker_generation"`
	RequestID        string `json:"request_id"`
	PrincipalRef     string `json:"principal_ref"`
	Scope            Scope  `json:"scope"`
	ExpiresAtMS      int64  `json:"expires_at_ms"`
	RequestDigest    string `json:"request_digest"`
	CaseID           string `json:"case_id,omitempty"`
}

type grantState struct {
	wire     grantWire
	peer     authenticatedIPCPeer
	consumed atomic.Bool
}

// VerifiedGrant is a process-local single-use capability. Copies share the
// same private state, so copying the handle cannot duplicate authorization.
type VerifiedGrant struct{ state *grantState }

// VerifiedHarnessGrant names the SDD capability type. VerifiedGrant remains
// a source-compatible alias for the focused protocol tests.
type VerifiedHarnessGrant = VerifiedGrant

// dispatch consumes a connection-bound grant once. Peer state uses atomics so
// Close can cancel an in-flight action without waiting on an unbounded lock.
func (g *VerifiedGrant) dispatch(nowMS int64, action func(context.Context, grantWire) error) error {
	if g == nil || g.state == nil || nowMS < 0 || action == nil || g.state.peer.state == nil {
		return errGrantInvalid
	}
	peer := g.state.peer.state
	if peer.closed.Load() || !peer.grantUsed.Load() || !g.state.consumed.CompareAndSwap(false, true) {
		return errGrantInvalid
	}
	if nowMS >= g.state.wire.ExpiresAtMS || g.state.wire.ExpiresAtMS-nowMS > maxGrantLifetimeMS {
		return errGrantInvalid
	}
	if err := peer.parent.checkAlive(); err != nil {
		return errPeerParentGone
	}
	if !peer.dispatched.CompareAndSwap(false, true) {
		return errGrantInvalid
	}
	defer close(peer.dispatchDone)
	if peer.closed.Load() {
		return errGrantInvalid
	}
	return action(peer.ctx, g.state.wire)
}

// canonicalRequestDigest binds canonical bytes to a versioned operation
// domain. The adapter must encode its fixed typed schema before calling this.
func canonicalRequestDigest(scope Scope, canonicalPayload []byte) (string, error) {
	var domain string
	switch scope {
	case ScopeCreate:
		domain = "pilot/create-case/v1\x00"
	case ScopeStart:
		domain = "pilot/start-case/v1\x00"
	case ScopeReceiptRead:
		domain = "pilot/receipt-read/v1\x00"
	case ScopeRecovery:
		domain = "pilot/recovery/v1\x00"
	case ScopeCleanup:
		domain = "pilot/cleanup/v1\x00"
	default:
		return "", errGrantInvalid
	}
	sum := sha256.Sum256(append([]byte(domain), canonicalPayload...))
	return hex.EncodeToString(sum[:]), nil
}

// IssueGrant signs a canonical grant. The private key must remain in the
// Gateway process and must never be serialized or passed to the worker.
func IssueGrant(privateKey ed25519.PrivateKey, fields GrantFields) ([]byte, []byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, nil, errGrantInvalid
	}
	wire := grantWire{
		Version: grantVersion, RunEpoch: fields.RunEpoch, WorkerGeneration: fields.WorkerGeneration, RequestID: fields.RequestID,
		PrincipalRef: fields.PrincipalRef, Scope: fields.Scope,
		ExpiresAtMS: fields.ExpiresAtMS, RequestDigest: fields.RequestDigest,
		CaseID: fields.CaseID,
	}
	if validateGrant(wire) != nil {
		return nil, nil, errGrantInvalid
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, nil, errGrantInvalid
	}
	return encoded, ed25519.Sign(privateKey, encoded), nil
}

// GrantFields is the Gateway-side input to IssueGrant. Create grants must
// omit CaseID; case-scoped grants must provide one.
type GrantFields struct {
	RunEpoch, WorkerGeneration, RequestID, PrincipalRef string
	Scope                                               Scope
	ExpiresAtMS                                         int64
	RequestDigest, CaseID                               string
}

// VerifyGrant authenticates a grant and binds it to the expected run, scope,
// typed request digest and authenticated local IPC peer. nowMS is injected to
// make expiry-boundary tests deterministic.
func VerifyGrant(publicKey ed25519.PublicKey, raw, signature []byte, bootstrap peerBootstrap, expectedPrincipalRef string, expectedScope Scope, expectedCaseID, expectedDigest string, nowMS int64, peer authenticatedIPCPeer) (*VerifiedHarnessGrant, error) {
	if peer.state == nil || len(raw) == 0 || len(raw) > maxGrantBytes || len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize ||
		!validPeerBootstrap(bootstrap) || nowMS < 0 {
		return nil, errGrantInvalid
	}
	var wire grantWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil {
		return nil, errGrantInvalid
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) || ed25519.Verify(publicKey, raw, signature) == false {
		return nil, errGrantInvalid
	}
	if validateGrant(wire) != nil || wire.RunEpoch != bootstrap.RunEpoch || wire.WorkerGeneration != bootstrap.WorkerGeneration || wire.PrincipalRef != expectedPrincipalRef || wire.Scope != expectedScope ||
		wire.CaseID != expectedCaseID || wire.RequestDigest != expectedDigest || nowMS >= wire.ExpiresAtMS ||
		wire.ExpiresAtMS-nowMS > maxGrantLifetimeMS {
		return nil, errGrantInvalid
	}
	if err := peer.claimGrant(); err != nil {
		return nil, err
	}
	return &VerifiedHarnessGrant{state: &grantState{wire: wire, peer: peer}}, nil
}

func validateGrant(w grantWire) error {
	if w.Version != grantVersion || !epochPattern.MatchString(w.RunEpoch) || !workerGenerationPattern.MatchString(w.WorkerGeneration) ||
		!identifier.MatchString(w.RequestID) || !identifier.MatchString(w.PrincipalRef) ||
		!digestPattern.MatchString(w.RequestDigest) || w.ExpiresAtMS <= 0 {
		return errGrantInvalid
	}
	switch w.Scope {
	case ScopeCreate:
		if w.CaseID != "" {
			return errGrantInvalid
		}
	case ScopeStart, ScopeReceiptRead, ScopeRecovery, ScopeCleanup:
		if !caseIDPattern.MatchString(w.CaseID) {
			return errGrantInvalid
		}
	default:
		return fmt.Errorf("%w: unsupported scope", errGrantInvalid)
	}
	return nil
}

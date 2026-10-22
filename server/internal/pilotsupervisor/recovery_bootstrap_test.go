//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestRecoveryBootstrapCanonicalWireRoundTrip(t *testing.T) {
	request := recoveryBootstrapRequest{
		Version: 1, Type: "RECOVERY_BOOTSTRAP", Generation: 17,
		LaunchNonce:      "0123456789abcdef0123456789abcdef",
		TargetGeneration: 900, TargetLaunchNonce: "abcdef0123456789abcdef0123456789",
		TargetRunEpoch: "fedcba9876543210fedcba9876543210",
	}
	want := []byte(`{"version":1,"type":"RECOVERY_BOOTSTRAP","generation":17,"launch_nonce":"0123456789abcdef0123456789abcdef","target_generation":900,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`)
	packet, err := encodeRecoveryBootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet, want) {
		t.Fatalf("wire packet mismatch\n got: %s\nwant: %s", packet, want)
	}
	decoded, err := decodeRecoveryBootstrap(packet)
	if err != nil || decoded != request {
		t.Fatalf("decode = %#v, %v; want %#v", decoded, err, request)
	}
}

func TestRecoveryBootstrapRejectsNoncanonicalAndMalformedPackets(t *testing.T) {
	valid := `{"version":1,"type":"RECOVERY_BOOTSTRAP","generation":2,"launch_nonce":"0123456789abcdef0123456789abcdef","target_generation":1,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`
	tests := []struct {
		name   string
		packet string
	}{
		{"duplicate-key", `{"version":1,"version":1,"type":"RECOVERY_BOOTSTRAP","generation":2,"launch_nonce":"0123456789abcdef0123456789abcdef","target_generation":1,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`},
		{"unknown-key", valid[:len(valid)-1] + `,"extra":0}`},
		{"wrong-key-order", `{"type":"RECOVERY_BOOTSTRAP","version":1,"generation":2,"launch_nonce":"0123456789abcdef0123456789abcdef","target_generation":1,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`},
		{"whitespace", ` {"version":1,"type":"RECOVERY_BOOTSTRAP","generation":2,"launch_nonce":"0123456789abcdef0123456789abcdef","target_generation":1,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`},
		{"trailing-json", valid + `{}`},
		{"bad-nonce", `{"version":1,"type":"RECOVERY_BOOTSTRAP","generation":2,"launch_nonce":"ABCDEF0123456789ABCDEF0123456789","target_generation":1,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`},
		{"zero-generation", `{"version":1,"type":"RECOVERY_BOOTSTRAP","generation":0,"launch_nonce":"0123456789abcdef0123456789abcdef","target_generation":1,"target_launch_nonce":"abcdef0123456789abcdef0123456789","target_run_epoch":"fedcba9876543210fedcba9876543210"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeRecoveryBootstrap([]byte(test.packet)); !errors.Is(err, errRecoveryBootstrapInvalid) {
				t.Fatalf("decode error = %v, want %v", err, errRecoveryBootstrapInvalid)
			}
		})
	}
	if _, err := decodeRecoveryBootstrap(bytes.Repeat([]byte{'x'}, recoveryBootstrapMaxPacket+1)); !errors.Is(err, errRecoveryBootstrapInvalid) {
		t.Fatalf("oversized packet error = %v", err)
	}
}

func TestEncodeRecoveryBootstrapRejectsInvalidTargetAndIdentity(t *testing.T) {
	base := recoveryBootstrapRequest{
		Version: 1, Type: "RECOVERY_BOOTSTRAP", Generation: 17,
		LaunchNonce:      "0123456789abcdef0123456789abcdef",
		TargetGeneration: 900, TargetLaunchNonce: "abcdef0123456789abcdef0123456789",
		TargetRunEpoch: "fedcba9876543210fedcba9876543210",
	}
	tests := []struct {
		name string
		edit func(*recoveryBootstrapRequest)
	}{
		{"wrong-version", func(r *recoveryBootstrapRequest) { r.Version = 2 }},
		{"wrong-type", func(r *recoveryBootstrapRequest) { r.Type = "BOOTSTRAP" }},
		{"zero-target-generation", func(r *recoveryBootstrapRequest) { r.TargetGeneration = 0 }},
		{"bad-target-nonce", func(r *recoveryBootstrapRequest) { r.TargetLaunchNonce = "not-a-nonce" }},
		{"bad-run-epoch", func(r *recoveryBootstrapRequest) { r.TargetRunEpoch = "../etc/passwd" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.edit(&request)
			if _, err := encodeRecoveryBootstrap(request); !errors.Is(err, errRecoveryBootstrapInvalid) {
				t.Fatalf("encode error = %v, want %v", err, errRecoveryBootstrapInvalid)
			}
		})
	}
}

func TestRecoveryBootstrapAckAcceptsOnlyBoundResults(t *testing.T) {
	base := recoveryBootstrapAck{
		Version: 1, Type: "RECOVERY_BOOTSTRAP_ACK", Generation: 18,
		LaunchNonce: "0123456789abcdef0123456789abcdef",
	}
	for _, result := range []string{"already_terminal", "finalized", "recovery_proof_missing", "recovery_not_ready", "rejected", "restart_required"} {
		t.Run(result, func(t *testing.T) {
			ack := base
			ack.Result = result
			packet, err := encodeRecoveryBootstrapAck(ack)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeRecoveryBootstrapAck(packet)
			if err != nil || decoded != ack {
				t.Fatalf("decoded = %#v, %v; want %#v", decoded, err, ack)
			}
		})
	}
	base.Result = "ok"
	if _, err := encodeRecoveryBootstrapAck(base); !errors.Is(err, errRecoveryBootstrapInvalid) {
		t.Fatalf("unlisted ACK result error = %v", err)
	}
}

func TestDecodeRecoveryBootstrapAckRequiresCanonicalValidPacket(t *testing.T) {
	ack := recoveryBootstrapAck{
		Version: 1, Type: "RECOVERY_BOOTSTRAP_ACK", Generation: 18,
		LaunchNonce: "0123456789abcdef0123456789abcdef", Result: "finalized",
	}
	valid, err := encodeRecoveryBootstrapAck(ack)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRecoveryBootstrapAck(valid)
	if err != nil || decoded != ack {
		t.Fatalf("decode = %#v, %v", decoded, err)
	}
	invalid := []struct {
		name   string
		packet []byte
	}{
		{"empty", nil},
		{"oversized", bytes.Repeat([]byte{'x'}, recoveryBootstrapMaxPacket+1)},
		{"malformed-json", []byte("{")},
		{"unknown-field", append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"extra":true}`)...)},
		{"duplicate-field", bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)},
		{"noncanonical-whitespace", append([]byte{' '}, valid...)},
		{"trailing-json", append(append([]byte{}, valid...), []byte(`{}`)...)},
		{"invalid-result", []byte(`{"version":1,"type":"RECOVERY_BOOTSTRAP_ACK","generation":18,"launch_nonce":"0123456789abcdef0123456789abcdef","result":"unknown"}`)},
		{"wrong-version", bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":2`), 1)},
		{"wrong-type", bytes.Replace(valid, []byte(`"type":"RECOVERY_BOOTSTRAP_ACK"`), []byte(`"type":"BOOTSTRAP_ACK"`), 1)},
		{"zero-generation", bytes.Replace(valid, []byte(`"generation":18`), []byte(`"generation":0`), 1)},
		{"malformed-nonce", bytes.Replace(valid, []byte(`0123456789abcdef0123456789abcdef`), []byte(`0123456789abcdef0123456789abcdeG`), 1)},
		{"reordered-keys", []byte(`{"type":"RECOVERY_BOOTSTRAP_ACK","version":1,"generation":18,"launch_nonce":"0123456789abcdef0123456789abcdef","result":"finalized"}`)},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeRecoveryBootstrapAck(test.packet); !errors.Is(err, errRecoveryBootstrapInvalid) {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
}

func TestRecoveryBootstrapAckMustMatchRequestGenerationAndNonce(t *testing.T) {
	request := validRecoveryRequest(22)
	ack := recoveryBootstrapAck{
		Version: 1, Type: "RECOVERY_BOOTSTRAP_ACK", Generation: request.Generation,
		LaunchNonce: request.LaunchNonce, Result: "finalized",
	}
	if !recoveryBootstrapAckMatches(request, ack) {
		t.Fatal("matching ACK was rejected")
	}
	ack.Generation++
	if recoveryBootstrapAckMatches(request, ack) {
		t.Fatal("ACK with mismatched generation was accepted")
	}
	ack.Generation = request.Generation
	ack.LaunchNonce = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if recoveryBootstrapAckMatches(request, ack) {
		t.Fatal("ACK with mismatched nonce was accepted")
	}
}

type fakeRecoveryBootstrapLedger struct {
	lastGeneration uint64
	lastNonce      string
	snapshot       recoveryTargetSnapshot
	loadError      error
	consumeError   error
	finalizeError  error
	finalizeResult bool
	finalizeTarget recoveryTargetIdentity
	finalizeDigest string
	loadCalls      int
	finalizeCalls  int
	events         []string
}

func (ledger *fakeRecoveryBootstrapLedger) ConsumeRecoveryControl(_ context.Context, generation uint64, nonce string) (bool, error) {
	ledger.events = append(ledger.events, "consume")
	if generation <= ledger.lastGeneration {
		return false, nil
	}
	ledger.lastGeneration = generation
	ledger.lastNonce = nonce
	if ledger.consumeError != nil {
		return true, ledger.consumeError
	}
	return true, nil
}

func (ledger *fakeRecoveryBootstrapLedger) LoadRecoveryTarget(_ context.Context, _ recoveryTargetIdentity) (recoveryTargetSnapshot, error) {
	ledger.events = append(ledger.events, "load")
	ledger.loadCalls++
	if ledger.loadError != nil {
		return recoveryTargetSnapshot{}, ledger.loadError
	}
	return ledger.snapshot, nil
}

func (ledger *fakeRecoveryBootstrapLedger) FinalizeRunTerminal(_ context.Context, target recoveryTargetIdentity, evidenceDigest string) (bool, error) {
	ledger.events = append(ledger.events, "finalize")
	ledger.finalizeCalls++
	ledger.finalizeTarget = target
	ledger.finalizeDigest = evidenceDigest
	return ledger.finalizeResult, ledger.finalizeError
}

func validRecoveryRequest(generation uint64) recoveryBootstrapRequest {
	return recoveryBootstrapRequest{
		Version: 1, Type: "RECOVERY_BOOTSTRAP", Generation: generation,
		LaunchNonce:      "0123456789abcdef0123456789abcdef",
		TargetGeneration: 900, TargetLaunchNonce: "abcdef0123456789abcdef0123456789",
		TargetRunEpoch: "fedcba9876543210fedcba9876543210",
	}
}

func readyRecoverySnapshot() recoveryTargetSnapshot {
	return recoveryTargetSnapshot{
		Identity: recoveryTargetIdentity{
			Generation: 900, LaunchNonce: "abcdef0123456789abcdef0123456789",
			RunEpoch: "fedcba9876543210fedcba9876543210",
		},
		Found: true, RunState: "containment_empty", EvidencePresent: true, EvidenceRows: 1,
		EvidenceMatches: true, EvidenceDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		OperationsTerminal: true, RequiredReceiptsPresent: true, RequiredReceiptsMatch: true, CleanupState: "complete",
	}
}

func TestHandleRecoveryBootstrapConsumesControlBeforeTargetRead(t *testing.T) {
	ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 5, snapshot: readyRecoverySnapshot(), finalizeResult: true}
	request := validRecoveryRequest(6)
	ack := handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "finalized" || ledger.lastGeneration != request.Generation || ledger.lastNonce != request.LaunchNonce {
		t.Fatalf("ack=%q consumed=(%d,%q)", ack.Result, ledger.lastGeneration, ledger.lastNonce)
	}
	if ledger.loadCalls != 1 || ledger.finalizeCalls != 1 {
		t.Fatalf("load/finalize calls = %d/%d", ledger.loadCalls, ledger.finalizeCalls)
	}
	if !reflect.DeepEqual(ledger.events, []string{"consume", "load", "finalize"}) {
		t.Fatalf("ledger event order = %v", ledger.events)
	}
	wantTarget := recoveryTargetIdentity{
		Generation: request.TargetGeneration, LaunchNonce: request.TargetLaunchNonce, RunEpoch: request.TargetRunEpoch,
	}
	if ledger.finalizeTarget != wantTarget || ledger.finalizeDigest != ledger.snapshot.EvidenceDigest {
		t.Fatalf("finalize target/digest = %+v/%q, want %+v/%q", ledger.finalizeTarget, ledger.finalizeDigest, wantTarget, ledger.snapshot.EvidenceDigest)
	}
}

func TestHandleRecoveryBootstrapRejectsNonUniqueEvidenceRows(t *testing.T) {
	for _, rows := range []int{0, 2} {
		t.Run(fmt.Sprintf("rows-%d", rows), func(t *testing.T) {
			snapshot := readyRecoverySnapshot()
			snapshot.EvidenceRows = rows
			ledger := &fakeRecoveryBootstrapLedger{snapshot: snapshot, finalizeResult: true}
			ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(1), ledger)
			if ack.Result != "rejected" || ledger.finalizeCalls != 0 {
				t.Fatalf("ack=%q finalize calls=%d", ack.Result, ledger.finalizeCalls)
			}
		})
	}
}

func TestHandleRecoveryBootstrapProofMissingPreservesTargetAndConsumesControl(t *testing.T) {
	for _, cleanup := range []string{"pending", "indeterminate"} {
		t.Run(cleanup, func(t *testing.T) {
			snapshot := readyRecoverySnapshot()
			snapshot.EvidencePresent = false
			snapshot.EvidenceMatches = false
			snapshot.RunState = "terminating"
			snapshot.CleanupState = cleanup
			ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 10, snapshot: snapshot}
			before := ledger.snapshot
			ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(11), ledger)
			if ack.Result != "recovery_proof_missing" {
				t.Fatalf("result = %q", ack.Result)
			}
			if !reflect.DeepEqual(before, ledger.snapshot) || ledger.lastGeneration != 11 || ledger.lastNonce == "" {
				t.Fatalf("target/control state not preserved/consumed: before=%+v after=%+v generation=%d", before, ledger.snapshot, ledger.lastGeneration)
			}
			if ledger.finalizeCalls != 0 {
				t.Fatalf("finalizer called %d times", ledger.finalizeCalls)
			}
		})
	}
}

func TestHandleRecoveryBootstrapNotReadyPredicates(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*recoveryTargetSnapshot)
	}{
		{"run-state", func(s *recoveryTargetSnapshot) { s.RunState = "terminating" }},
		{"operation-nonterminal", func(s *recoveryTargetSnapshot) { s.OperationsTerminal = false }},
		{"receipt-missing", func(s *recoveryTargetSnapshot) { s.RequiredReceiptsPresent = false }},
		{"receipt-mismatch", func(s *recoveryTargetSnapshot) { s.RequiredReceiptsMatch = false }},
		{"cleanup-not-eligible", func(s *recoveryTargetSnapshot) { s.CleanupState = "indeterminate" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			snapshot := readyRecoverySnapshot()
			mutation.edit(&snapshot)
			ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 20, snapshot: snapshot}
			before := ledger.snapshot
			ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(21), ledger)
			if ack.Result != "recovery_not_ready" {
				t.Fatalf("result = %q", ack.Result)
			}
			if !reflect.DeepEqual(before, ledger.snapshot) || ledger.lastGeneration != 21 || ledger.finalizeCalls != 0 {
				t.Fatalf("not-ready changed target/finalized: before=%+v after=%+v generation=%d finalizers=%d", before, ledger.snapshot, ledger.lastGeneration, ledger.finalizeCalls)
			}
		})
	}
}

func TestHandleRecoveryBootstrapAlreadyTerminalDoesNotReFinalize(t *testing.T) {
	snapshot := readyRecoverySnapshot()
	snapshot.Terminal = true
	ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 40, snapshot: snapshot}
	before := ledger.snapshot
	ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(41), ledger)
	if ack.Result != "already_terminal" || ledger.finalizeCalls != 0 || !reflect.DeepEqual(before, ledger.snapshot) {
		t.Fatalf("ack=%q finalizers=%d target=%+v", ack.Result, ledger.finalizeCalls, ledger.snapshot)
	}
}

func TestHandleRecoveryBootstrapRejectsConflictingEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*recoveryTargetSnapshot)
	}{
		{"identity-mismatch", func(s *recoveryTargetSnapshot) { s.EvidenceMatches = false }},
		{"malformed-digest", func(s *recoveryTargetSnapshot) { s.EvidenceDigest = "ABCDEF" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := readyRecoverySnapshot()
			test.edit(&snapshot)
			ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 50, snapshot: snapshot}
			before := ledger.snapshot
			ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(51), ledger)
			if ack.Result != "rejected" || ledger.lastGeneration != 51 || ledger.finalizeCalls != 0 || !reflect.DeepEqual(before, ledger.snapshot) {
				t.Fatalf("ack=%q generation=%d finalizers=%d target=%+v", ack.Result, ledger.lastGeneration, ledger.finalizeCalls, ledger.snapshot)
			}
		})
	}
}

func TestHandleRecoveryBootstrapRejectsStaleAndMismatchedTargets(t *testing.T) {
	for _, generation := range []uint64{7, 8} {
		t.Run(map[bool]string{true: "equal-generation", false: "lower-generation"}[generation == 8], func(t *testing.T) {
			ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 8, lastNonce: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", snapshot: readyRecoverySnapshot()}
			beforeNonce := ledger.lastNonce
			ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(generation), ledger)
			if ack.Result != "rejected" || ledger.loadCalls != 0 || ledger.lastGeneration != 8 || ledger.lastNonce != beforeNonce {
				t.Fatalf("ack=%q loadCalls=%d generation=%d nonce=%q", ack.Result, ledger.loadCalls, ledger.lastGeneration, ledger.lastNonce)
			}
		})
	}
	for _, mismatch := range []struct {
		name string
		edit func(*recoveryBootstrapRequest)
	}{
		{"target-generation", func(r *recoveryBootstrapRequest) { r.TargetGeneration++ }},
		{"target-launch-nonce", func(r *recoveryBootstrapRequest) { r.TargetLaunchNonce = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }},
		{"target-run-epoch", func(r *recoveryBootstrapRequest) { r.TargetRunEpoch = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }},
	} {
		t.Run("authenticated-target-mismatch-"+mismatch.name, func(t *testing.T) {
			ledger := &fakeRecoveryBootstrapLedger{lastGeneration: 8, snapshot: readyRecoverySnapshot()}
			request := validRecoveryRequest(9)
			mismatch.edit(&request)
			before := ledger.snapshot
			ack := handleRecoveryBootstrap(context.Background(), request, ledger)
			if ack.Result != "rejected" || ledger.lastGeneration != 9 || ledger.finalizeCalls != 0 || !reflect.DeepEqual(before, ledger.snapshot) {
				t.Fatalf("ack=%q generation=%d finalizers=%d target=%+v", ack.Result, ledger.lastGeneration, ledger.finalizeCalls, ledger.snapshot)
			}
		})
	}
}

func TestHandleRecoveryBootstrapRestartRequiredConsumesTuple(t *testing.T) {
	request := validRecoveryRequest(12)
	ledger := &fakeRecoveryBootstrapLedger{
		lastGeneration: 11, snapshot: readyRecoverySnapshot(), loadError: errors.New("injected ledger read failure"),
	}
	ack := handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "restart_required" || ledger.lastGeneration != request.Generation || ledger.lastNonce != request.LaunchNonce {
		t.Fatalf("ack=%q consumed=(%d,%q)", ack.Result, ledger.lastGeneration, ledger.lastNonce)
	}
	// Replaying the committed tuple is rejected; the retry must be issued with
	// a strictly higher generation after the old Reaper has been reaped.
	ack = handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "rejected" || ledger.loadCalls != 1 {
		t.Fatalf("replay ack=%q loadCalls=%d", ack.Result, ledger.loadCalls)
	}
	ledger.loadError = nil
	ledger.snapshot = readyRecoverySnapshot()
	ledger.finalizeResult = true
	request.Generation++
	request.LaunchNonce = "1123456789abcdef0123456789abcdef"
	ack = handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "finalized" || ledger.lastGeneration != request.Generation || ledger.finalizeCalls != 1 {
		t.Fatalf("higher-generation retry ack=%q generation=%d finalizers=%d", ack.Result, ledger.lastGeneration, ledger.finalizeCalls)
	}
}

func TestHandleRecoveryBootstrapControlConsumeUncertainRequiresHigherGeneration(t *testing.T) {
	request := validRecoveryRequest(61)
	ledger := &fakeRecoveryBootstrapLedger{
		lastGeneration: 60, snapshot: readyRecoverySnapshot(),
		consumeError: errors.New("injected ambiguous durable control commit"),
	}
	ack := handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "restart_required" || ledger.lastGeneration != request.Generation || ledger.lastNonce != request.LaunchNonce || ledger.loadCalls != 0 {
		t.Fatalf("ack=%q consumed=(%d,%q) loadCalls=%d", ack.Result, ledger.lastGeneration, ledger.lastNonce, ledger.loadCalls)
	}
	// The failed Reaper is assumed reaped before this higher-generation retry.
	ack = handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "rejected" || ledger.loadCalls != 0 {
		t.Fatalf("same-tuple replay ack=%q loadCalls=%d", ack.Result, ledger.loadCalls)
	}
	ledger.consumeError = nil
	request.Generation++
	request.LaunchNonce = "1123456789abcdef0123456789abcdef"
	ledger.finalizeResult = true
	ack = handleRecoveryBootstrap(context.Background(), request, ledger)
	if ack.Result != "finalized" || ledger.lastGeneration != request.Generation || ledger.finalizeCalls != 1 {
		t.Fatalf("higher-generation recovery ack=%q generation=%d finalizers=%d", ack.Result, ledger.lastGeneration, ledger.finalizeCalls)
	}
}

func TestHandleRecoveryBootstrapFinalizerOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		committed     bool
		finalizeError error
		want          string
	}{
		{"cas-not-ready", false, nil, "recovery_not_ready"},
		{"uncertain-commit", false, errors.New("injected commit uncertainty"), "restart_required"},
		{"committed", true, nil, "finalized"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &fakeRecoveryBootstrapLedger{
				lastGeneration: 30, snapshot: readyRecoverySnapshot(),
				finalizeResult: test.committed, finalizeError: test.finalizeError,
			}
			ack := handleRecoveryBootstrap(context.Background(), validRecoveryRequest(31), ledger)
			if ack.Result != test.want || ledger.finalizeCalls != 1 {
				t.Fatalf("ack=%q finalizers=%d want=%q", ack.Result, ledger.finalizeCalls, test.want)
			}
		})
	}
}

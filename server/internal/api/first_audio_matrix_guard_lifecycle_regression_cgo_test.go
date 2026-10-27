//go:build cgo

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestFirstAudioGuardProbeChallengeReceiptIsBoundSingleUseAndOrdered(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"}
	seed := bytes.Repeat([]byte{0x5A}, 32)
	token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if len(token) != 32 || challenge.Contract().tokenDigest != sha256.Sum256(token) {
		t.Fatal("challenge digest or token length is invalid")
	}
	if !challenge.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := challenge.Consume(token, binding); err != nil {
		t.Fatal("valid challenge rejected:", err)
	}
	if err := challenge.Consume(token, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("duplicate token use error = %v, want rejected", err)
	}
	if challenge.receipt.completeFor(binding, challenge.Contract().tokenDigest) {
		t.Fatal("reused token incorrectly completed a probe receipt")
	}

	secret, success, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(secret)
	if !success.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := success.Consume(secret, binding); err != nil {
		t.Fatal(err)
	}
	if err := success.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	executor := newFirstAudioGuardProbeExecutor(success)
	if err := executor.VerifyRollback(success.receipt, binding, nil); err != nil {
		t.Fatal("verified rollback rejected:", err)
	}
	if !success.receipt.completeFor(binding, success.Contract().tokenDigest) {
		t.Fatal("probe receipt did not reach rollback-verified state")
	}
	if err := success.Consume(secret, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("post-success token replay error = %v, want rejected", err)
	}
	if success.receipt.completeFor(binding, success.Contract().tokenDigest) {
		t.Fatal("post-success denial left probe eligible for preflight")
	}
	productionToken, productionChallenge, err := newFirstAudioGuardProbeChallenge(binding)
	if err != nil {
		t.Fatal("crypto/rand challenge construction failed:", err)
	}
	defer zeroFirstAudioSecret(productionToken)
	if len(productionToken) != 32 || productionChallenge.Contract().tokenDigest != sha256.Sum256(productionToken) {
		t.Fatal("crypto/rand challenge has invalid length or digest")
	}
}

func TestFirstAudioGuardProbeReplayDenialBlocksJourneyDespiteCallerEvidence(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.PublishInitial(startup); err != nil {
		t.Fatal(err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	activePreflight := controller.publisher.Load()
	firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
	challenge := &firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}
	if err := challenge.Consume(bytes.Repeat([]byte{0x7F}, 32), bindings[0]); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("replayed challenge = %v, want rejected", err)
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true, unexpectedDenials: 0}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("journey after probe replay = %v, want denial", err)
	}
	if controller.publisher.Load() != activePreflight {
		t.Fatal("replayed challenge advanced the active context")
	}
}

func TestFirstAudioGuardProbeActivityEndRejectsUnderflow(t *testing.T) {
	receipt := &firstAudioGuardProbeReceipt{activityDone: make(chan struct{}, 1)}
	receipt.endProbeActivity()
	if receipt.active.Load()&firstAudioGuardProbeActiveMask != 0 {
		t.Fatal("activity underflow changed the active count")
	}
	if receipt.denials.Load() != 1 {
		t.Fatalf("underflow denial count = %d, want 1", receipt.denials.Load())
	}
}

func TestFirstAudioGuardProbeChallengeRejectsBindingEntropyAndReceiptFailures(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"}
	seed := bytes.Repeat([]byte{0x11}, 32)
	if _, _, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed[:4]), binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("short entropy error = %v, want rejected", err)
	}
	if _, _, err := newFirstAudioGuardProbeChallengeFrom(nil, binding); !errors.Is(err, errFirstAudioGuardContextInvalid) {
		t.Fatalf("nil entropy source error = %v, want invalid binding", err)
	}

	token, wrongBindingChallenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if !wrongBindingChallenge.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	wrongConnection := binding
	wrongConnection.connection = "conn-b"
	if err := wrongBindingChallenge.Consume(token, wrongConnection); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("wrong connection error = %v, want rejected", err)
	}
	if state := firstAudioGuardProbeReceiptState(wrongBindingChallenge.receipt.state.Load()); state != firstAudioGuardProbeFailed {
		t.Fatalf("wrong binding receipt state = %d, want failed", state)
	}

	badToken := bytes.Repeat([]byte{0x22}, 32)
	unusedValidToken, wrongTokenChallenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(unusedValidToken)
	if !wrongTokenChallenge.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := wrongTokenChallenge.Consume(badToken, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("wrong token error = %v, want rejected", err)
	}
	zeroFirstAudioSecret(badToken)

	outOfOrderToken, outOfOrder, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(outOfOrderToken)
	if !outOfOrder.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := outOfOrder.ObserveRowGuard(binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("out-of-order row guard error = %v, want rejected", err)
	}

	rollbackToken, rollbackFailure, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !rollbackFailure.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := rollbackFailure.Consume(rollbackToken, binding); err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(rollbackToken)
	if err := rollbackFailure.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	wrongReceipt := &firstAudioGuardProbeReceipt{binding: binding}
	if err := newFirstAudioGuardProbeExecutor(rollbackFailure).VerifyRollback(wrongReceipt, binding, nil); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("foreign receipt error = %v, want rejected", err)
	}
	if rollbackFailure.receipt.completeFor(binding, rollbackFailure.Contract().tokenDigest) {
		t.Fatal("foreign receipt verified rollback")
	}
	rollbackToken, failedRollback, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !failedRollback.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := failedRollback.Consume(rollbackToken, binding); err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(rollbackToken)
	if err := failedRollback.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	rollbackErr := errors.New("injected rollback failure")
	if err := newFirstAudioGuardProbeExecutor(failedRollback).VerifyRollback(failedRollback.receipt, binding, rollbackErr); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("failed rollback error = %v, want rejected", err)
	}
	if failedRollback.receipt.completeFor(binding, failedRollback.Contract().tokenDigest) || firstAudioGuardProbeReceiptState(failedRollback.receipt.state.Load()) != firstAudioGuardProbeFailed {
		t.Fatal("failed rollback completed its probe receipt")
	}
}

func TestFirstAudioGuardControllerGatesStartupAndVerifiedPreflight(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal("acquire after verified preflight:", err)
	}
	if lease.Context().Phase() != firstAudioGuardJourney || lease.Context().Version() != 3 {
		t.Fatalf("lease context = %s/%d, want JOURNEY/3", lease.Context().Phase(), lease.Context().Version())
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); !errors.Is(err, errFirstAudioGuardLeaseReleased) {
		t.Fatalf("double release = %v, want rejected", err)
	}
}

func TestFirstAudioGuardControllerPublishesPreflightBeforeProbes(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.PublishInitial(startup); err != nil {
		t.Fatal(err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bindings := []firstAudioGuardProbeBinding{
		{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"},
		{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-b", probeID: "probe-b"},
	}
	spec := firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardPreflight, version: 2}
	tokens := make([][]byte, 0, len(bindings))
	receipts := make([]*firstAudioGuardProbeReceipt, 0, len(bindings))
	challenges := make([]*firstAudioGuardProbeChallenge, 0, len(bindings))
	for i, binding := range bindings {
		seed := bytes.Repeat([]byte{byte(0x30 + i)}, 32)
		token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
		zeroFirstAudioSecret(seed)
		if err != nil {
			t.Fatal(err)
		}
		if firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeIssued {
			t.Fatal("new probe was advanced before PREFLIGHT publication")
		}
		tokens = append(tokens, token)
		receipts = append(receipts, challenge.receipt)
		challenges = append(challenges, challenge)
		spec.connectionIDs = append(spec.connectionIDs, binding.connection)
		spec.probeContracts = append(spec.probeContracts, challenge.Contract())
		spec.rowContracts = append(spec.rowContracts, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: "lesson:101/conn:" + binding.connection, oldRow: []byte("lesson-row"), newRow: []byte("lesson-row")})
	}
	preflight, err := newFirstAudioGuardContext(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	activePreflight := controller.publisher.Load()
	if activePreflight == nil || activePreflight.Phase() != firstAudioGuardPreflight {
		t.Fatal("PREFLIGHT context was not published before probe execution")
	}
	for i, challenge := range challenges {
		if err := challenge.Consume(tokens[i], bindings[i]); err != nil {
			t.Fatal("consume probe token:", err)
		}
		zeroFirstAudioSecret(tokens[i])
		if err := challenge.ObserveRowGuard(bindings[i]); err != nil {
			t.Fatal("observe row guard:", err)
		}
		if err := newFirstAudioGuardProbeExecutor(challenge).VerifyRollback(challenge.receipt, bindings[i], nil); err != nil {
			t.Fatal("verify probe rollback:", err)
		}
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
	if err := transition.Publish(context.Background(), journey, evidence); err != nil {
		t.Fatal(err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		zeroFirstAudioSecret(token)
	}
}

func TestFirstAudioGuardChallengeCannotRunBeforePreflightArmsIt(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"}
	seed := bytes.Repeat([]byte{0x3C}, 32)
	token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if err := challenge.Consume(token, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("pre-PREFLIGHT consume = %v, want rejected", err)
	}
	if challenge.receipt.denials.Load() != 1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeFailed {
		t.Fatal("pre-PREFLIGHT probe attempt did not fail closed")
	}
}

func TestFirstAudioGuardControllerCloseDrainAndReopenWithNewContext(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	oldLease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	oldContext := oldLease.Context()
	drainResult := make(chan *firstAudioGuardTransitionLease, 1)
	drainError := make(chan error, 1)
	go func() {
		transition, err := controller.CloseAndDrain(context.Background())
		drainResult <- transition
		drainError <- err
	}()
	waitFirstAudioAdmissionClosed(t, controller)
	if err := oldLease.Release(); err != nil {
		t.Fatal(err)
	}
	transition := <-drainResult
	if err := <-drainError; err != nil {
		t.Fatal(err)
	}
	candidate, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(4, 101, 1, 4))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{snapshotUnchanged: true, registryReconciled: true, writeCommitted: true, readbackVerified: true}
	if err := transition.Publish(context.Background(), candidate, evidence); err != nil {
		t.Fatal("publish next complete context:", err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	newLease, err := controller.Acquire()
	if err != nil {
		t.Fatal("admission did not reopen:", err)
	}
	if newLease.Context() == oldContext || newLease.Context().Version() != 4 {
		t.Fatal("new lease did not bind the newly published pointer/version")
	}
	if err := newLease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerDrainTimeoutIsOutOfBandTerminal(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	before := controller.publisher.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controller.CloseAndDrain(ctx); !errors.Is(err, errFirstAudioGuardDrainTimeout) {
		t.Fatalf("drain error = %v, want timeout", err)
	}
	if controller.publisher.Load() != before {
		t.Fatal("drain timeout replaced a context still held by an active lease")
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("admission after timeout = %v, want closed", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerLeaseAcquisitionIsBoundToGateVersion(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	old, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := old.Context().Version()
	drained := make(chan *firstAudioGuardTransitionLease, 1)
	drainErr := make(chan error, 1)
	go func() {
		transition, err := controller.CloseAndDrain(context.Background())
		drained <- transition
		drainErr <- err
	}()
	waitFirstAudioAdmissionClosed(t, controller)
	if err := old.Release(); err != nil {
		t.Fatal(err)
	}
	transition := <-drained
	if err := <-drainErr; err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("acquire while closed = %v", err)
	}
	candidate, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(oldVersion+1, 101, 1, int(oldVersion+1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), candidate, firstAudioGuardTransitionEvidence{snapshotUnchanged: true, registryReconciled: true, writeCommitted: true, readbackVerified: true}); err != nil {
		t.Fatal(err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	newLease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if newLease.version == oldVersion || newLease.Context() != controller.publisher.Load() {
		t.Fatal("acquire did not bind the active context under the gate")
	}
	if err := newLease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerRejectsIncompletePreflightReceiptSet(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.PublishInitial(startup); err != nil {
		t.Fatal(err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	for _, secret := range tokens {
		zeroFirstAudioSecret(secret)
	}
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts[:1], snapshotUnchanged: true, registryReconciled: true}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("incomplete receipt set publish error = %v, want transition denial", err)
	}
	if active := controller.publisher.Load(); active == nil || active.phase != firstAudioGuardPreflight {
		t.Fatal("incomplete preflight evidence changed the active context")
	}
	if err := transition.OpenJourney(); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("open after rejected journey = %v, want transition denial", err)
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("admission after incomplete probe set = %v, want closed", err)
	}
	if err := transition.MarkTerminal(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardPreflightReceiptMustMatchActiveTokenDigest(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.PublishInitial(startup); err != nil {
		t.Fatal(err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	activePreflight := controller.publisher.Load()
	firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
	// Keep the same identity binding but create a completed receipt for another token.
	seed := bytes.Repeat([]byte{0xE7}, 32)
	token, foreign, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if !foreign.receipt.arm() {
		t.Fatal("could not arm foreign unit-test challenge")
	}
	if err := foreign.Consume(token, bindings[0]); err != nil {
		t.Fatal(err)
	}
	if err := foreign.ObserveRowGuard(bindings[0]); err != nil {
		t.Fatal(err)
	}
	if err := newFirstAudioGuardProbeExecutor(foreign).VerifyRollback(foreign.receipt, bindings[0], nil); err != nil {
		t.Fatal(err)
	}
	if foreign.receipt.completeFor(bindings[0], preflight.probes[bindings[0].connection].tokenDigest) {
		t.Fatal("same-identity foreign digest satisfied the active preflight contract")
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: []*firstAudioGuardProbeReceipt{foreign.receipt, receipts[1]}, snapshotUnchanged: true, registryReconciled: true}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("foreign token receipt publish error = %v, want transition denial", err)
	}
	if controller.publisher.Load() != activePreflight {
		t.Fatal("foreign token receipt replaced active preflight context")
	}
}

func TestFirstAudioGuardConcurrentCloseAndDrainHasSingleOwner(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan *firstAudioGuardTransitionLease, 1)
	firstErr := make(chan error, 1)
	go func() { tr, e := controller.CloseAndDrain(context.Background()); firstResult <- tr; firstErr <- e }()
	waitFirstAudioAdmissionClosed(t, controller)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if tr, err := controller.CloseAndDrain(ctx); !errors.Is(err, errFirstAudioGuardAdmissionClosed) || tr != nil {
		t.Fatalf("second drain = (%v, %v), want immediate single-owner rejection", tr, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if tr := <-firstResult; tr == nil {
		t.Fatal("first drain did not receive transition ownership")
	}
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardPreflightPublishWaitsForProbeActivityAndTimesOutTerminal(t *testing.T) {
	newPreflightController := func() (*firstAudioGuardController, *firstAudioGuardTransitionLease, *firstAudioGuardContext, []firstAudioGuardProbeBinding, []*firstAudioGuardProbeReceipt) {
		t.Helper()
		c := newFirstAudioGuardController()
		startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.PublishInitial(startup); err != nil {
			t.Fatal(err)
		}
		tr, err := c.CloseAndDrain(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		pre, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
		if err := tr.Publish(context.Background(), pre, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
			t.Fatal(err)
		}
		firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
		return c, tr, c.publisher.Load(), bindings, receipts
	}
	t.Run("waits for admitted probe callback", func(t *testing.T) {
		c, tr, pre, bindings, receipts := newPreflightController()
		if !receipts[0].beginProbeActivity() {
			t.Fatal("failed to start probe activity")
		}
		journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
		done := make(chan error, 1)
		go func() { done <- tr.Publish(context.Background(), journey, evidence) }()
		deadline := time.Now().Add(time.Second)
		for !receipts[0].probeAdmissionClosed() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !receipts[0].probeAdmissionClosed() {
			t.Fatal("probe admission did not close")
		}
		if receipts[0].probeGateDrained() {
			t.Fatal("probe drain completed while callback remained active")
		}
		late := &firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}
		denials := receipts[0].denials.Load()
		if err := late.Consume([]byte("late callback"), bindings[0]); !errors.Is(err, errFirstAudioGuardProbeRejected) {
			t.Fatalf("post-close callback = %v, want rejected", err)
		}
		if receipts[0].denials.Load() != denials || firstAudioGuardProbeReceiptState(receipts[0].state.Load()) != firstAudioGuardProbeRollbackVerified {
			t.Fatal("post-close callback mutated the completed receipt")
		}
		if c.publisher.Load() != pre {
			t.Fatal("context pointer changed before probe callback drained")
		}
		receipts[0].endProbeActivity()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := tr.OpenJourney(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("in-flight replay invalidates proof before publication", func(t *testing.T) {
		c, tr, pre, bindings, receipts := newPreflightController()
		if !receipts[0].beginProbeActivity() {
			t.Fatal("failed to admit in-flight probe callback")
		}
		challenge := &firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}
		journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
		done := make(chan error, 1)
		go func() { done <- tr.Publish(context.Background(), journey, evidence) }()
		deadline := time.Now().Add(time.Second)
		for !receipts[0].probeAdmissionClosed() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !receipts[0].probeAdmissionClosed() {
			t.Fatal("probe admission did not close")
		}
		if err := challenge.consumeAdmitted([]byte("late replay from admitted callback")); !errors.Is(err, errFirstAudioGuardProbeRejected) {
			t.Fatalf("in-flight replay = %v, want rejection", err)
		}
		receipts[0].endProbeActivity()
		if err := <-done; !errors.Is(err, errFirstAudioGuardContextTransition) {
			t.Fatalf("journey publish after in-flight replay = %v, want transition denial", err)
		}
		if c.publisher.Load() != pre {
			t.Fatal("in-flight replay changed context pointer")
		}
		c.mu.Lock()
		terminal := c.terminal
		c.mu.Unlock()
		if !terminal {
			t.Fatal("in-flight replay did not terminalize controller")
		}
	})
	t.Run("timeout fails closed without pointer swap", func(t *testing.T) {
		c, tr, pre, bindings, receipts := newPreflightController()
		if !receipts[0].beginProbeActivity() {
			t.Fatal("failed to start probe activity")
		}
		defer receipts[0].endProbeActivity()
		journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := tr.Publish(ctx, journey, evidence); !errors.Is(err, errFirstAudioGuardDrainTimeout) {
			t.Fatalf("publish error = %v, want drain timeout", err)
		}
		if c.publisher.Load() != pre {
			t.Fatal("timeout replaced active context")
		}
		if _, err := c.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
			t.Fatalf("admission after timeout = %v, want closed", err)
		}
		c.mu.Lock()
		terminal := c.terminal
		c.mu.Unlock()
		if !terminal {
			t.Fatal("probe drain timeout did not mark out-of-band terminal")
		}
		for i, receipt := range receipts {
			if !receipt.probeAdmissionClosed() {
				t.Fatalf("receipt %d remained open after drain timeout", i)
			}
		}
		if (&firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}).Consume([]byte("late"), bindings[0]) == nil {
			t.Fatal("callback accepted after probe admission closed")
		}
	})
}

func TestFirstAudioGuardLateProbeIsRejectedAfterJourney(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	controller.mu.Lock()
	var challenge *firstAudioGuardProbeChallenge
	for _, receipt := range controller.probeReceipts {
		challenge = &firstAudioGuardProbeChallenge{binding: receipt.binding, digest: receipt.digest, receipt: receipt}
		break
	}
	controller.mu.Unlock()
	if challenge == nil {
		t.Fatal("controller lost registered probe receipt")
	}
	denials := challenge.receipt.denials.Load()
	if err := challenge.Consume(bytes.Repeat([]byte{0x55}, 32), challenge.binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("late probe consume error = %v, want rejected", err)
	}
	if challenge.receipt.denials.Load() != denials {
		t.Fatal("closed-gate late probe changed the completed receipt denial count")
	}
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatalf("ordinary admission changed after expected late-probe rejection: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerCloseAcquireRaceDrainsAllAdmittedLeases(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	const callers = 24
	start := make(chan struct{})
	results := make(chan error, callers)
	var callersWG sync.WaitGroup
	for range callers {
		callersWG.Add(1)
		go func() {
			defer callersWG.Done()
			<-start
			lease, err := controller.Acquire()
			if err != nil {
				if !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
					results <- fmt.Errorf("unexpected acquire error: %w", err)
					return
				}
				results <- nil
				return
			}
			results <- lease.Release()
		}()
	}
	close(start)
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	callersWG.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if firstAudioActiveLeaseCountForTest(controller) != 0 {
		t.Fatal("transition acquired while application leases remained")
	}
	if err := transition.MarkTerminal(context.Background()); err != nil {
		t.Fatal("terminalize drained controller:", err)
	}
	if active := controller.publisher.Load(); active == nil || active.Phase() != firstAudioGuardTerminal {
		t.Fatal("drained terminal transition did not publish terminal context")
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("admission reopened after terminal transition: %v", err)
	}
}

func TestFirstAudioGuardControllerResetRequiresVerifiedNextGeneration(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reset, err := newFirstAudioGuardContext(firstAudioValidResetSpec(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), reset, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("publish reset context:", err)
	}
	next, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(5, 202, 2, 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), next, firstAudioGuardTransitionEvidence{snapshotUnchanged: true, registryReconciled: true, resetCommitted: true, readbackVerified: true}); err != nil {
		t.Fatal("publish verified next-generation context:", err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal("admission did not reopen for next generation:", err)
	}
	if lease.Context().currentLessonID != 202 || lease.Context().generation != 2 || lease.Context().version != 5 {
		t.Fatalf("next generation context = lesson %d generation %d version %d", lease.Context().currentLessonID, lease.Context().generation, lease.Context().version)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardPublisherRejectsIllegalTransitionsAndTerminalBackEdges(t *testing.T) {
	var publisher firstAudioGuardContextPublisher
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(1, 101, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(journey); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("initial JOURNEY publish = %v, want transition denial", err)
	}
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(startup); err != nil {
		t.Fatal(err)
	}
	terminal, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardTerminal, version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(terminal); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(startup); !errors.Is(err, errFirstAudioGuardContextStale) && !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("terminal back-edge = %v, want rejected", err)
	}
}

func firstAudioTestOpenJourney(t *testing.T) *firstAudioGuardController {
	t.Helper()
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.PublishInitial(startup); err != nil {
		t.Fatal(err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal("publish preflight context:", err)
	}
	firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{
		probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true,
	}
	if err := transition.Publish(context.Background(), journey, evidence); err != nil {
		t.Fatal("publish verified journey context:", err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	return controller
}

func firstAudioTestPreflightEvidence(bindings []firstAudioGuardProbeBinding, receipts []*firstAudioGuardProbeReceipt) firstAudioGuardTransitionEvidence {
	return firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts}
}

func firstAudioTestCompleteProbes(t *testing.T, tokens [][]byte, bindings []firstAudioGuardProbeBinding, receipts []*firstAudioGuardProbeReceipt) {
	t.Helper()
	if len(tokens) != len(bindings) || len(receipts) != len(bindings) {
		t.Fatal("probe fixture dimensions do not match")
	}
	for i, receipt := range receipts {
		challenge := &firstAudioGuardProbeChallenge{binding: bindings[i], digest: receipt.digest, receipt: receipt}
		if err := challenge.Consume(tokens[i], bindings[i]); err != nil {
			t.Fatal("consume preflight probe:", err)
		}
		zeroFirstAudioSecret(tokens[i])
		if err := challenge.ObserveRowGuard(bindings[i]); err != nil {
			t.Fatal("observe preflight row guard:", err)
		}
		if err := newFirstAudioGuardProbeExecutor(challenge).VerifyRollback(receipt, bindings[i], nil); err != nil {
			t.Fatal("verify preflight rollback:", err)
		}
	}
}

func firstAudioTestPreflight(t *testing.T, version uint64) (*firstAudioGuardContext, [][]byte, []firstAudioGuardProbeBinding, []*firstAudioGuardProbeReceipt) {
	t.Helper()
	connections := []string{"conn-a", "conn-b"}
	spec := firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardPreflight, version: version}
	var tokens [][]byte
	var bindings []firstAudioGuardProbeBinding
	var receipts []*firstAudioGuardProbeReceipt
	for i, connection := range connections {
		binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: version, connection: connection, probeID: fmt.Sprintf("probe-%d", i)}
		tokenSeed := bytes.Repeat([]byte{byte(0xA0 + i)}, 32)
		token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(tokenSeed), binding)
		if err != nil {
			t.Fatal(err)
		}
		zeroFirstAudioSecret(tokenSeed)
		spec.probeContracts = append(spec.probeContracts, challenge.Contract())
		spec.connectionIDs = append(spec.connectionIDs, connection)
		spec.rowContracts = append(spec.rowContracts, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: "lesson:101/conn:" + connection, oldRow: []byte("lesson-row"), newRow: []byte("lesson-row")})
		tokens = append(tokens, token)
		bindings = append(bindings, binding)
		receipts = append(receipts, challenge.receipt)
	}
	ctx, err := newFirstAudioGuardContext(spec)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, tokens, bindings, receipts
}

func waitFirstAudioAdmissionClosed(t *testing.T, controller *firstAudioGuardController) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		controller.mu.Lock()
		closed := !controller.admissionOpen
		controller.mu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("admission did not close")
		}
		time.Sleep(time.Millisecond)
	}
}

func firstAudioActiveLeaseCountForTest(controller *firstAudioGuardController) int {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.activeLeasesLocked()
}

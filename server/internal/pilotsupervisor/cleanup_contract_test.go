//go:build pilot_e2e

package pilotsupervisor

import (
	"errors"
	"testing"
)

func TestPilotCleanupAnchorContract(t *testing.T) {
	anchor := testCleanupAnchor()

	tests := []struct {
		name      string
		anchor    cleanupAnchor
		principal cleanupDigest
		wantErr   bool
	}{
		{name: "valid-same-principal", anchor: anchor, principal: anchor.principalRef},
		{name: "missing-receipt", anchor: cleanupAnchor{sourceIdentity: anchor.sourceIdentity, objectIdentity: anchor.objectIdentity, principalRef: anchor.principalRef}, principal: anchor.principalRef, wantErr: true},
		{name: "missing-source-identity", anchor: cleanupAnchor{receiptID: anchor.receiptID, objectIdentity: anchor.objectIdentity, principalRef: anchor.principalRef}, principal: anchor.principalRef, wantErr: true},
		{name: "missing-object-identity", anchor: cleanupAnchor{receiptID: anchor.receiptID, sourceIdentity: anchor.sourceIdentity, principalRef: anchor.principalRef}, principal: anchor.principalRef, wantErr: true},
		{name: "missing-anchor-principal", anchor: cleanupAnchor{receiptID: anchor.receiptID, sourceIdentity: anchor.sourceIdentity, objectIdentity: anchor.objectIdentity}, principal: anchor.principalRef, wantErr: true},
		{name: "cross-principal", anchor: anchor, principal: cleanupTestDigest(99), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := bindCleanupIntent(tt.anchor, tt.principal, cleanupTestDigest(5), cleanupTestDigest(6))
			if tt.wantErr && !errors.Is(err, errCleanupTargetInvalid) {
				t.Fatalf("bindCleanupIntent() error = %v, want ipc_operation_target_invalid", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("bindCleanupIntent() unexpected error: %v", err)
			}
		})
	}
}

func TestPilotCleanupOwnerClaimContract(t *testing.T) {
	anchor := testCleanupAnchor()
	prepared, err := bindCleanupIntent(anchor, anchor.principalRef, cleanupTestDigest(5), cleanupTestDigest(6))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimCleanupOwner(prepared, 0); !errors.Is(err, errCleanupTargetInvalid) {
		t.Fatalf("claimCleanupOwner(zero generation) error = %v, want ipc_operation_target_invalid", err)
	}
	claimed, err := claimCleanupOwner(prepared, 7)
	if err != nil {
		t.Fatalf("claimCleanupOwner(valid generation) error: %v", err)
	}
	if claimed.ownerGeneration != 7 {
		t.Fatalf("owner generation = %d, want 7", claimed.ownerGeneration)
	}
	if _, err := claimCleanupOwner(claimed, 8); !errors.Is(err, errCleanupTargetInvalid) {
		t.Fatalf("claimCleanupOwner(second generation) error = %v, want ipc_operation_target_invalid", err)
	}
}

func TestPilotCleanupRecoveryContract(t *testing.T) {
	anchor := testCleanupAnchor()
	intent, err := bindCleanupIntent(anchor, anchor.principalRef, cleanupTestDigest(5), cleanupTestDigest(6))
	if err != nil {
		t.Fatal(err)
	}
	preparedIntent := intent
	intent, err = claimCleanupOwner(intent, 7)
	if err != nil {
		t.Fatal(err)
	}
	witness := testCleanupWitness(intent, anchor)
	wrongIdentity := cleanupTestDigest(77)

	tests := []struct {
		name        string
		witness     *cleanupWitness
		observation cleanupObservation
		identity    cleanupDigest
		want        cleanupAction
		intent      *cleanupIntent
	}{
		{name: "first-present-matching-anchor", observation: cleanupObservationPresent, identity: anchor.objectIdentity, want: cleanupActionPersistInitialPresentThenUnlink},
		{name: "first-absent-is-indeterminate", observation: cleanupObservationAbsent, want: cleanupActionIndeterminate},
		{name: "first-unknown-is-indeterminate", observation: cleanupObservationUnknown, want: cleanupActionIndeterminate},
		{name: "first-replacement-is-indeterminate", observation: cleanupObservationPresent, identity: wrongIdentity, want: cleanupActionIndeterminate},
		{name: "durable-witness-same-object-still-present", witness: witness, observation: cleanupObservationPresent, identity: anchor.objectIdentity, want: cleanupActionContinueUnlink},
		{name: "durable-witness-and-absent-recover-completed", witness: witness, observation: cleanupObservationAbsent, want: cleanupActionCompleteFromWitness},
		{name: "durable-witness-but-replaced-object", witness: witness, observation: cleanupObservationPresent, identity: wrongIdentity, want: cleanupActionIndeterminate},
		{name: "durable-witness-but-unknown", witness: witness, observation: cleanupObservationUnknown, want: cleanupActionIndeterminate},
		{name: "witness-anchor-drift", witness: &cleanupWitness{anchor: cleanupAnchor{receiptID: cleanupTestDigest(88), sourceIdentity: anchor.sourceIdentity, objectIdentity: anchor.objectIdentity, principalRef: anchor.principalRef}, objectIdentity: anchor.objectIdentity, operationID: intent.operationID, runEpoch: intent.runEpoch, ownerGeneration: intent.ownerGeneration}, observation: cleanupObservationAbsent, want: cleanupActionIndeterminate},
		{name: "witness-from-different-operation", witness: func() *cleanupWitness {
			w := testCleanupWitness(intent, anchor)
			w.operationID = cleanupTestDigest(55)
			return w
		}(), observation: cleanupObservationAbsent, want: cleanupActionIndeterminate},
		{name: "witness-from-different-run", witness: func() *cleanupWitness {
			w := testCleanupWitness(intent, anchor)
			w.runEpoch = cleanupTestDigest(56)
			return w
		}(), observation: cleanupObservationAbsent, want: cleanupActionIndeterminate},
		{name: "witness-from-different-owner-generation", witness: func() *cleanupWitness { w := testCleanupWitness(intent, anchor); w.ownerGeneration++; return w }(), observation: cleanupObservationAbsent, want: cleanupActionIndeterminate},
		{name: "intent-principal-drift", witness: witness, observation: cleanupObservationAbsent, intent: func() *cleanupIntent { i := intent; i.operationPrincipal = cleanupTestDigest(99); return &i }(), want: cleanupActionIndeterminate},
		{name: "prepared-intent-cannot-plan", observation: cleanupObservationPresent, identity: anchor.objectIdentity, intent: &preparedIntent, want: cleanupActionIndeterminate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selectedIntent := intent
			if tt.intent != nil {
				selectedIntent = *tt.intent
			}
			if got := planCleanupAction(selectedIntent, tt.witness, tt.observation, tt.identity); got != tt.want {
				t.Fatalf("planCleanupAction() = %d, want %d", got, tt.want)
			}
		})
	}
}

func testCleanupWitness(intent cleanupIntent, anchor cleanupAnchor) *cleanupWitness {
	return &cleanupWitness{
		anchor:          anchor,
		objectIdentity:  anchor.objectIdentity,
		operationID:     intent.operationID,
		runEpoch:        intent.runEpoch,
		ownerGeneration: intent.ownerGeneration,
	}
}

func testCleanupAnchor() cleanupAnchor {
	return cleanupAnchor{
		receiptID:      cleanupTestDigest(1),
		sourceIdentity: cleanupTestDigest(2),
		objectIdentity: cleanupTestDigest(3),
		principalRef:   cleanupTestDigest(4),
	}
}

func cleanupTestDigest(value byte) cleanupDigest {
	var digest cleanupDigest
	for i := range digest {
		digest[i] = value
	}
	return digest
}

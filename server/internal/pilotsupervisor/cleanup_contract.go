//go:build pilot_e2e

package pilotsupervisor

import "errors"

// Cleanup identity inputs are digests produced by the Reaper from pinned OS
// identities and immutable ledger receipts. This contract model performs no IO.
type cleanupDigest [32]byte

type cleanupAnchor struct {
	receiptID      cleanupDigest
	sourceIdentity cleanupDigest
	objectIdentity cleanupDigest
	principalRef   cleanupDigest
}

type cleanupIntent struct {
	anchor             cleanupAnchor
	operationPrincipal cleanupDigest
	operationID        cleanupDigest
	runEpoch           cleanupDigest
	ownerGeneration    uint64
}

type cleanupWitness struct {
	anchor          cleanupAnchor
	objectIdentity  cleanupDigest
	operationID     cleanupDigest
	runEpoch        cleanupDigest
	ownerGeneration uint64
}

type cleanupObservation uint8

const (
	cleanupObservationUnknown cleanupObservation = iota
	cleanupObservationPresent
	cleanupObservationAbsent
)

type cleanupAction uint8

const (
	cleanupActionIndeterminate cleanupAction = iota
	cleanupActionPersistInitialPresentThenUnlink
	cleanupActionContinueUnlink
	cleanupActionCompleteFromWitness
)

var errCleanupTargetInvalid = errors.New("ipc_operation_target_invalid")

// bindCleanupIntent models validation performed before a prepared ledger row
// is inserted. Callers must provide only a typed reference; the Reaper resolves
// these digests from the durable creation receipt, never from request bytes.
func bindCleanupIntent(anchor cleanupAnchor, operationPrincipal, operationID, runEpoch cleanupDigest) (cleanupIntent, error) {
	if isZeroCleanupDigest(anchor.receiptID) ||
		isZeroCleanupDigest(anchor.sourceIdentity) ||
		isZeroCleanupDigest(anchor.objectIdentity) ||
		isZeroCleanupDigest(anchor.principalRef) ||
		isZeroCleanupDigest(operationPrincipal) ||
		anchor.principalRef != operationPrincipal ||
		isZeroCleanupDigest(operationID) ||
		isZeroCleanupDigest(runEpoch) {
		return cleanupIntent{}, errCleanupTargetInvalid
	}
	return cleanupIntent{anchor: anchor, operationPrincipal: operationPrincipal, operationID: operationID, runEpoch: runEpoch}, nil
}

// claimCleanupOwner models the prepared->executing owner CAS. A generation is
// fixed exactly once, after the prepared intent has been durably inserted.
func claimCleanupOwner(intent cleanupIntent, ownerGeneration uint64) (cleanupIntent, error) {
	if !validCleanupIntent(intent) || intent.ownerGeneration != 0 || ownerGeneration == 0 {
		return cleanupIntent{}, errCleanupTargetInvalid
	}
	intent.ownerGeneration = ownerGeneration
	return intent, nil
}

// planCleanupAction is a pure contract function. Persistence adapters must
// check the returned action inside the owner/generation CAS transaction, hold
// the exclusive resource lock, and fsync the initial-present witness before
// executing an unlink. It is not evidence verification or a filesystem API.
func planCleanupAction(intent cleanupIntent, witness *cleanupWitness, observation cleanupObservation, observedIdentity cleanupDigest) cleanupAction {
	if !validCleanupIntent(intent) || intent.ownerGeneration == 0 {
		return cleanupActionIndeterminate
	}

	if witness == nil {
		if observation == cleanupObservationPresent && observedIdentity == intent.anchor.objectIdentity {
			return cleanupActionPersistInitialPresentThenUnlink
		}
		return cleanupActionIndeterminate
	}

	if witness.anchor != intent.anchor ||
		witness.objectIdentity != intent.anchor.objectIdentity ||
		witness.operationID != intent.operationID ||
		witness.runEpoch != intent.runEpoch ||
		witness.ownerGeneration != intent.ownerGeneration {
		return cleanupActionIndeterminate
	}
	switch observation {
	case cleanupObservationPresent:
		if observedIdentity == intent.anchor.objectIdentity {
			return cleanupActionContinueUnlink
		}
	case cleanupObservationAbsent:
		return cleanupActionCompleteFromWitness
	}
	return cleanupActionIndeterminate
}

func validCleanupIntent(intent cleanupIntent) bool {
	return validCleanupAnchor(intent.anchor) &&
		!isZeroCleanupDigest(intent.operationPrincipal) &&
		intent.anchor.principalRef == intent.operationPrincipal &&
		!isZeroCleanupDigest(intent.operationID) &&
		!isZeroCleanupDigest(intent.runEpoch)
}

func validCleanupAnchor(anchor cleanupAnchor) bool {
	return !isZeroCleanupDigest(anchor.receiptID) &&
		!isZeroCleanupDigest(anchor.sourceIdentity) &&
		!isZeroCleanupDigest(anchor.objectIdentity) &&
		!isZeroCleanupDigest(anchor.principalRef)
}

func isZeroCleanupDigest(digest cleanupDigest) bool {
	return digest == cleanupDigest{}
}

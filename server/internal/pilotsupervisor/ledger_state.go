//go:build pilot_e2e

package pilotsupervisor

import "errors"

// LedgerState is the durable business-operation state vocabulary. Persistence
// adapters must validate every update with ValidateLedgerTransition in the
// same transaction that writes the new state and stores its evidence receipt.
type LedgerState string

const (
	LedgerPrepared      LedgerState = "prepared"
	LedgerExecuting     LedgerState = "executing"
	LedgerCompleted     LedgerState = "completed"
	LedgerAbandoned     LedgerState = "abandoned"
	LedgerIndeterminate LedgerState = "indeterminate"
)

var errLedgerTransition = errors.New("pilot ledger transition rejected")
var errLedgerEvidence = errors.New("pilot ledger transition evidence required")

// ledgerTransitionEvidence is an internal, transaction-local proof category.
// A persistence adapter may select a non-default value only after verifying
// the matching durable marker/cleanup record inside its ledger transaction.
type ledgerTransitionEvidence uint8

const (
	ledgerEvidenceNone ledgerTransitionEvidence = iota
	ledgerEvidenceEffectMarkerVerified
	ledgerEvidenceNoEffectAndCleanupVerified
)

// CanTransitionLedgerState reports whether a single durable CAS transition is
// permitted by docs/43. Terminal states are irreversible. In particular,
// cleanup evidence cannot turn an indeterminate effect into abandoned.
func CanTransitionLedgerState(from, to LedgerState) bool {
	switch from {
	case LedgerPrepared:
		return to == LedgerExecuting || to == LedgerAbandoned || to == LedgerIndeterminate
	case LedgerExecuting:
		return to == LedgerCompleted || to == LedgerAbandoned || to == LedgerIndeterminate
	default:
		return false
	}
}

// ValidateLedgerTransition checks both the state edge and its required proof
// category. The persistence adapter must derive evidence from durable records
// in the same transaction; this helper does not inspect or authenticate them.
func ValidateLedgerTransition(from, to LedgerState, evidence ledgerTransitionEvidence) error {
	if !CanTransitionLedgerState(from, to) {
		return errLedgerTransition
	}
	switch {
	case to == LedgerIndeterminate:
		if evidence != ledgerEvidenceNone {
			return errLedgerTransition
		}
	case to == LedgerCompleted:
		if evidence != ledgerEvidenceEffectMarkerVerified {
			return errLedgerEvidence
		}
	case to == LedgerAbandoned:
		if evidence != ledgerEvidenceNoEffectAndCleanupVerified {
			return errLedgerEvidence
		}
	case evidence != ledgerEvidenceNone:
		return errLedgerTransition
	}
	return nil
}

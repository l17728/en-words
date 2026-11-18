//go:build pilot_e2e

package pilotsupervisor

import (
	"errors"
	"testing"
)

func TestPilotLedgerTransitionFunction(t *testing.T) {
	allowed := map[[2]LedgerState]ledgerTransitionEvidence{
		{LedgerPrepared, LedgerExecuting}:      ledgerEvidenceNone,
		{LedgerPrepared, LedgerAbandoned}:      ledgerEvidenceNoEffectAndCleanupVerified,
		{LedgerPrepared, LedgerIndeterminate}:  ledgerEvidenceNone,
		{LedgerExecuting, LedgerCompleted}:     ledgerEvidenceEffectMarkerVerified,
		{LedgerExecuting, LedgerAbandoned}:     ledgerEvidenceNoEffectAndCleanupVerified,
		{LedgerExecuting, LedgerIndeterminate}: ledgerEvidenceNone,
	}
	states := []LedgerState{LedgerPrepared, LedgerExecuting, LedgerCompleted, LedgerAbandoned, LedgerIndeterminate, "unknown"}
	for _, from := range states {
		for _, to := range states {
			evidence, expected := allowed[[2]LedgerState{from, to}]
			t.Run(string(from)+"-to-"+string(to), func(t *testing.T) {
				if got := CanTransitionLedgerState(from, to); got != expected {
					t.Fatalf("CanTransitionLedgerState(%q, %q) = %t, want %t", from, to, got, expected)
				}
				err := ValidateLedgerTransition(from, to, ledgerEvidenceNone)
				if expected && err != nil {
					if evidence == ledgerEvidenceNone {
						t.Fatalf("transition unexpectedly rejected: %v", err)
					}
					if !errors.Is(err, errLedgerEvidence) {
						t.Fatalf("transition without proof error = %v, want evidence-required", err)
					}
					if err := ValidateLedgerTransition(from, to, evidence); err != nil {
						t.Fatalf("transition with proof rejected: %v", err)
					}
				}
				if !expected {
					if !errors.Is(err, errLedgerTransition) {
						t.Fatalf("transition error = %v, want stable rejection", err)
					}
					if proofErr := ValidateLedgerTransition(from, to, ledgerEvidenceEffectMarkerVerified); !errors.Is(proofErr, errLedgerTransition) {
						t.Fatalf("invalid edge with proof error = %v, want stable rejection", proofErr)
					}
				}
			})
		}
	}

	t.Run("wrong-evidence-categories", func(t *testing.T) {
		cases := []struct {
			from, to LedgerState
			proof    ledgerTransitionEvidence
			want     error
		}{
			{LedgerPrepared, LedgerExecuting, ledgerEvidenceEffectMarkerVerified, errLedgerTransition},
			{LedgerPrepared, LedgerExecuting, ledgerEvidenceNoEffectAndCleanupVerified, errLedgerTransition},
			{LedgerPrepared, LedgerAbandoned, ledgerEvidenceEffectMarkerVerified, errLedgerEvidence},
			{LedgerExecuting, LedgerCompleted, ledgerEvidenceNoEffectAndCleanupVerified, errLedgerEvidence},
			{LedgerExecuting, LedgerAbandoned, ledgerEvidenceEffectMarkerVerified, errLedgerEvidence},
			{LedgerExecuting, LedgerIndeterminate, ledgerEvidenceEffectMarkerVerified, errLedgerTransition},
			{LedgerExecuting, LedgerIndeterminate, ledgerEvidenceNoEffectAndCleanupVerified, errLedgerTransition},
		}
		for _, tc := range cases {
			err := ValidateLedgerTransition(tc.from, tc.to, tc.proof)
			if !errors.Is(err, tc.want) {
				t.Errorf("ValidateLedgerTransition(%q, %q, %d) = %v, want %v", tc.from, tc.to, tc.proof, err, tc.want)
			}
		}
	})
}

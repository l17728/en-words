package api

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreserveAtomicOutcomeDistinguishesUnknownFromCleanFailure(t *testing.T) {
	writeErr := errors.New("commit could not be confirmed")
	require.ErrorIs(t, preserveAtomicOutcome(writeErr, "outcome_uncertain"), errAtomicOutcomeUncertain)
	require.NotErrorIs(t, preserveAtomicOutcome(writeErr, "failed_cleaned"), errAtomicOutcomeUncertain)
	require.NoError(t, preserveAtomicOutcome(nil, "outcome_uncertain"))
}

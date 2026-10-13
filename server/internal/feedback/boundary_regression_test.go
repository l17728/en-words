package feedback

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestModelCandidatesCannotBypassWordsBoundary(t *testing.T) {
	p := NewParser(func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) })
	for _, words := range []int{40, 49, 50, 60, 100, 101} {
		planned := p.Plan(ParseResult{Intents: []Intent{{Kind: IntentReduce, Confidence: 0.99, TargetWords: words}}})
		require.Len(t, planned, 1)
		if words < 50 || words > 100 {
			require.NotEqual(t, ActionApplyConfig, planned[0].Action)
		} else {
			require.Equal(t, ActionApplyConfig, planned[0].Action)
		}
	}
	low := p.Plan(ParseResult{Intents: []Intent{{Kind: IntentReduce, Confidence: 0.1, TargetWords: 50}}})
	require.Equal(t, ActionClarify, low[0].Action)
}

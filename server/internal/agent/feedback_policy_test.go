package agent

import (
	"enwords/internal/scripts"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFeedbackPolicyFreezesWordsAndRendersFacts(t *testing.T) {
	for _, tc := range []struct {
		kind                  string
		current, target, want int
	}{{"A_reduce", 70, 0, 60}, {"A_reduce", 50, 0, 50}, {"B_increase", 95, 0, 100}, {"B_increase", 60, 80, 80}} {
		got, err := ResolveFeedbackWords(tc.kind, tc.current, tc.target)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	_, err := ResolveFeedbackWords("unknown", 70, 0)
	require.Error(t, err)
	_, err = ResolveFeedbackWords("A_reduce", 70, 30)
	require.Error(t, err)
	bank := scripts.NewBank()
	bank.SeedAll()
	for _, mode := range []string{"config", "postpone", "boost", "escalate", "need_confirm", "clarify"} {
		require.NotEmpty(t, RenderFeedbackFacts(bank, 1, FeedbackReplyFacts{Mode: mode, Key: "wordsPerLesson", Target: 60, Date: "2026-10-14", Count: 2}))
	}
	require.Contains(t, RenderFeedbackFacts(bank, 1, FeedbackReplyFacts{Mode: "config", Key: "lessonsPerDay", Target: 2}), "2")
	require.Empty(t, RenderFeedbackFacts(bank, 1, FeedbackReplyFacts{Mode: "unknown"}))
}

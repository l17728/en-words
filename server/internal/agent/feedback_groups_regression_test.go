package agent

import (
	"enwords/internal/feedback"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestFeedbackActionsRetainIntentOwnershipAndAnchor(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	date := anchor.AddDate(0, 0, 1)
	parser := feedback.NewParser(func() time.Time { return anchor.AddDate(0, 0, 30) })
	system := NewSystemAgent(nil, nil, parser)
	parsed := feedback.ParseResult{Intents: []feedback.Intent{
		{Kind: feedback.IntentFrequency, LessonsPerDay: 2, Confidence: 1},
		{Kind: feedback.IntentPostpone, Date: &date, Confidence: 1},
		{Kind: feedback.IntentHardWord, Word: "cat", Confidence: 1},
		{Kind: feedback.IntentComplaint, Confidence: 1},
	}}
	acts := system.Decide(SystemInput{Trigger: "feedback_submitted", StudentID: 7, FeedbackAnchor: anchor, ParsedFeedback: &parsed})
	grouped := map[int][]string{}
	for _, act := range acts {
		grouped[act.IntentIndex] = append(grouped[act.IntentIndex], act.Kind)
	}
	require.Equal(t, []string{"adjust_frequency"}, grouped[0])
	require.Equal(t, []string{"postpone", "memory_append", "reply_applied"}, grouped[1])
	require.Equal(t, []string{"boost", "reply_applied"}, grouped[2])
	require.Equal(t, []string{"escalate", "reply_escalated"}, grouped[3])
}

func TestFeedbackContradictoryWordTargetsNeverEmitWrites(t *testing.T) {
	parser := feedback.NewParser(time.Now)
	system := NewSystemAgent(nil, nil, parser)
	parsed := feedback.ParseResult{Intents: []feedback.Intent{{Kind: feedback.IntentReduce, TargetWords: 50, Confidence: 1}, {Kind: feedback.IntentReduce, TargetWords: 60, Confidence: 1}}}
	acts := system.Decide(SystemInput{Trigger: "feedback_submitted", StudentID: 7, ParsedFeedback: &parsed})
	require.Len(t, acts, 2)
	for index, act := range acts {
		require.Equal(t, "reply_needconfirm", act.Kind)
		require.Equal(t, index, act.IntentIndex)
	}
}

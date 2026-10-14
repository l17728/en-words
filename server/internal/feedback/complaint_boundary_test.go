package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComplaintBoundaryPreservesIndependentModelRequest(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	p := NewParser(func() time.Time { return anchor })
	calls := 0
	p.SetLLM(submissionSemanticParser(func(ctx context.Context, text string, at time.Time) ([]Intent, error) {
		calls++
		require.Equal(t, "请把每课改为60词", text)
		require.Equal(t, anchor, at)
		return []Intent{{Kind: IntentReduce, TargetWords: 60, Confidence: 1}, {Kind: IntentComplaint, Confidence: 0.9}}, nil
	}))
	text := "投诉老师每天词太多；请把每课改为60词"
	got, err := p.ParseContextAt(context.Background(), text, anchor)
	require.NoError(t, err)
	require.Equal(t, text, got.Raw)
	require.Equal(t, 1, calls)
	plans := p.PlanAt(got, anchor)
	require.Len(t, plans, 2)
	require.Equal(t, ActionEscalate, plans[0].Action)
	require.Equal(t, ActionApplyConfig, plans[1].Action)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.ParseContextAt(ctx, "投诉老师每天词太多", anchor)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

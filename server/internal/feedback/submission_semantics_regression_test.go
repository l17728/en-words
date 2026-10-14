package feedback

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
	"time"
)

type submissionSemanticParser func(context.Context, string, time.Time) ([]Intent, error)

func (f submissionSemanticParser) ParseIntents(ctx context.Context, text string, at time.Time) ([]Intent, error) {
	return f(ctx, text, at)
}

func TestSubmissionParserCancellationNeverFallsBackToAction(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	p := NewParser(func() time.Time { return anchor })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
		calls++
		return nil, errors.New("transport")
	}))
	got, err := p.ParseContextAt(ctx, "每次降到50个词", anchor)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, got.Intents)
	require.Zero(t, calls)
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport_returns_after_cancel", true: "candidate_returns_after_cancel"}[success], func(t *testing.T) {
			active, stop := context.WithCancel(context.Background())
			defer stop()
			p.SetLLM(submissionSemanticParser(func(ctx context.Context, _ string, _ time.Time) ([]Intent, error) {
				stop()
				if success {
					return []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}, nil
				}
				return nil, ctx.Err()
			}))
			got, err := p.ParseContextAt(active, "每次降到50个词", anchor)
			require.ErrorIs(t, err, context.Canceled)
			require.Empty(t, got.Intents)
		})
	}
}

func TestSubmissionParserAndPlannerUseOriginalDateAnchor(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, zone)
	p := NewParser(func() time.Time { return anchor.AddDate(0, 0, 30) })
	// A network failure may fall back, but recovery must still mean tomorrow of submission day.
	p.SetLLM(submissionSemanticParser(func(_ context.Context, _ string, at time.Time) ([]Intent, error) {
		require.Equal(t, anchor, at)
		return nil, errors.New("fixture network failure")
	}))
	parsed, err := p.ParseContextAt(context.Background(), "明天有事，请假", anchor)
	require.NoError(t, err)
	var postpone *Intent
	for i := range parsed.Intents {
		if parsed.Intents[i].Kind == IntentPostpone {
			postpone = &parsed.Intents[i]
		}
	}
	require.NotNil(t, postpone)
	require.NotNil(t, postpone.Date)
	require.Equal(t, "2026-10-14", postpone.Date.Format("2006-01-02"))
	plan := p.PlanAt(ParseResult{Intents: []Intent{*postpone}}, anchor)
	require.Len(t, plan, 1)
	require.Equal(t, ActionPostpone, plan[0].Action)
	require.Equal(t, ActionNeedConfirm, p.Plan(ParseResult{Intents: []Intent{*postpone}})[0].Action, "control proves current date differs from frozen anchor")
}

func TestSubmissionSameKindDifferentTargetsRemainDistinct(t *testing.T) {
	p := NewParser(func() time.Time { return time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC) })
	words := p.ParseRules("cat 我老忘，dog 我老忘，cat 我老忘")
	var names []string
	for _, it := range words.Intents {
		if it.Kind == IntentHardWord {
			names = append(names, it.Word)
		}
	}
	require.ElementsMatch(t, []string{"cat", "dog"}, names, "equivalent duplicate can merge; different words cannot")
	parsed := p.ParseRules("每次降到50个词，每次降到60个词")
	require.Len(t, parsed.Intents, 2)
	for _, plan := range p.Plan(parsed) {
		require.Equal(t, ActionNeedConfirm, plan.Action, "conflicting quantities must not silently pick the first")
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		plans := p.Plan(ParseResult{Intents: []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: value}}})
		require.Equal(t, ActionClarify, plans[0].Action)
	}
}

func TestSubmissionModelCandidatesUseSameSemanticDeduplication(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	p := NewParser(func() time.Time { return anchor })
	p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
		return []Intent{{Kind: IntentReduce, Confidence: .8}, {Kind: IntentReduce, Confidence: 1}, {Kind: IntentHardWord, Word: "cat", Confidence: 1}, {Kind: IntentHardWord, Word: "dog", Confidence: 1}}, nil
	}))
	parsed, err := p.ParseContextAt(context.Background(), "model candidate fixture", anchor)
	require.NoError(t, err)
	require.Len(t, parsed.Intents, 3)
	plans := p.PlanAt(parsed, anchor)
	require.Equal(t, ActionApplyConfig, plans[0].Action)
	require.Equal(t, 0, plans[0].Intent.TargetWords)
	require.Equal(t, "cat", plans[1].Intent.Word)
	require.Equal(t, "dog", plans[2].Intent.Word)
	p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
		return []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}, {Kind: IntentReduce, TargetWords: 60, Confidence: 1}}, nil
	}))
	parsed, err = p.ParseContextAt(context.Background(), "model candidate fixture", anchor)
	require.NoError(t, err)
	require.Len(t, parsed.Intents, 2)
	for _, plan := range p.PlanAt(parsed, anchor) {
		require.Equal(t, ActionNeedConfirm, plan.Action)
	}
}

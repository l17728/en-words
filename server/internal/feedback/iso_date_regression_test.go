package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestISODateRulesPreserveExplicitYearAndDay(t *testing.T) {
	anchor := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, text := range []string{"2026-10-15请假", "2026-10-16请假", "课程改到2027-01-02", "每天两次，2026-10-16请假", "2025-10-16请假"} {
		t.Run(text, func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			parsed := p.ParseRules(text)
			require.Equal(t, text, parsed.Raw)
			found := false
			for _, in := range parsed.Intents {
				if in.Kind == IntentPostpone {
					found = true
					require.NotNil(t, in.Date)
					want := "2026-10-16"
					if text == "2026-10-15请假" {
						want = "2026-10-15"
					}
					if text == "课程改到2027-01-02" {
						want = "2027-01-02"
					}
					if text == "2025-10-16请假" {
						want = "2025-10-16"
					}
					require.Equal(t, want, in.Date.Format("2006-01-02"))
				}
			}
			require.True(t, found)
			if text == "2025-10-16请假" {
				for _, plan := range p.PlanAt(parsed, anchor) {
					require.NotEqual(t, ActionPostpone, plan.Action)
				}
			}
		})
	}
}

func TestISODateModelSameDayDifferentHourIsAllowed(t *testing.T) {
	anchor := time.Date(2026, 10, 14, 9, 0, 0, 0, time.FixedZone("fixture", 8*3600))
	p := NewParser(func() time.Time { return anchor })
	p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
		date := time.Date(2026, 10, 16, 18, 0, 0, 0, anchor.Location())
		return []Intent{{Kind: IntentPostpone, Date: &date, Confidence: 1}}, nil
	}))
	parsed, err := p.ParseContextAt(context.Background(), "2026-10-16请假", anchor)
	require.NoError(t, err)
	plans := p.PlanAt(parsed, anchor)
	require.Len(t, plans, 1)
	require.Equal(t, ActionPostpone, plans[0].Action)
}

func TestISODateInvalidAlternativeAndModelMismatchCannotWrite(t *testing.T) {
	anchor := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, text := range []string{"2026-02-30请假", "2026-13-01请假", "2026-10-16或者2026-10-17请假", "2026-10-16，或者2026-10-17请假", "2026-10请假", "2026/10/16请假", "2026-1-2请假"} {
		t.Run(text, func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			for _, plan := range p.PlanAt(p.ParseRules(text), anchor) {
				require.Contains(t, []ActionType{ActionNeedConfirm, ActionClarify}, plan.Action)
			}
			for _, kind := range []IntentKind{IntentReduce, IntentIncrease, IntentFrequency, IntentPostpone, IntentHardWord} {
				p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
					date := anchor.AddDate(0, 0, 1)
					return []Intent{{Kind: kind, Date: &date, TargetWords: 50, LessonsPerDay: 2, Word: "apple", Confidence: 1}}, nil
				}))
				parsed, err := p.ParseContextAt(context.Background(), text, anchor)
				require.NoError(t, err)
				for _, plan := range p.PlanAt(parsed, anchor) {
					require.Contains(t, []ActionType{ActionNeedConfirm, ActionClarify}, plan.Action)
				}
			}
		})
	}
	p := NewParser(func() time.Time { return anchor })
	p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
		date := anchor.AddDate(0, 0, 1)
		return []Intent{{Kind: IntentPostpone, Date: &date, Confidence: 1}}, nil
	}))
	parsed, err := p.ParseContextAt(context.Background(), "2026-10-16请假", anchor)
	require.NoError(t, err)
	plans := p.PlanAt(parsed, anchor)
	require.Len(t, plans, 1)
	require.Equal(t, ActionNeedConfirm, plans[0].Action)
}

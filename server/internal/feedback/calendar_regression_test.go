package feedback

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSubmissionCalendarRejectsImpossibleAndAmbiguousDates(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	anchor := time.Date(2028, 1, 31, 23, 50, 0, 0, zone)
	p := NewParser(func() time.Time { return anchor })
	for _, text := range []string{"2月30日请假", "13月1日请假", "2月0日请假", "下月31日请假", "周一或周二请假"} {
		t.Run(text, func(t *testing.T) {
			got, err := p.ParseContextAt(context.Background(), text, anchor)
			require.NoError(t, err)
			plans := p.PlanAt(got, anchor)
			require.NotEmpty(t, plans)
			for _, plan := range plans {
				require.Equal(t, ActionNeedConfirm, plan.Action, "invalid explicit date cannot turn into tomorrow")
			}
		})
	}
	for _, tc := range []struct{ text, date string }{{"下月1日请假", "2028-02-01"}, {"2月29日请假", "2028-02-29"}, {"明天请假", "2028-02-01"}} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := p.ParseContextAt(context.Background(), tc.text, anchor)
			require.NoError(t, err)
			require.Len(t, got.Intents, 1)
			require.NotNil(t, got.Intents[0].Date)
			require.Equal(t, tc.date, got.Intents[0].Date.Format("2006-01-02"))
			_, offset := got.Intents[0].Date.Zone()
			require.Equal(t, 8*3600, offset)
		})
	}
}

func TestSubmissionComplaintDoesNotSilentlyReduceWords(t *testing.T) {
	p := NewParser(time.Now)
	for _, text := range []string{"投诉", "投诉老师每天词太多"} {
		got := p.ParseRules(text)
		plans := p.Plan(got)
		require.Len(t, plans, 1)
		require.Equal(t, IntentComplaint, plans[0].Intent.Kind)
		require.Equal(t, ActionEscalate, plans[0].Action)
	}
}

func TestSubmissionExpiredOrOutOfWindowDatesNeverSchedule(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 23, 59, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	p := NewParser(func() time.Time { return anchor })
	for _, target := range []time.Time{anchor.Add(-time.Second), anchor, anchor.AddDate(0, 0, 31)} {
		plans := p.PlanAt(ParseResult{Intents: []Intent{{Kind: IntentPostpone, Date: &target, Confidence: 1}}}, anchor)
		require.Len(t, plans, 1)
		require.Equal(t, ActionNeedConfirm, plans[0].Action)
	}
}

func TestSubmissionExplicitDateAmbiguityCannotBeHiddenByModel(t *testing.T) {
	anchor := time.Date(2028, 1, 31, 9, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	for _, model := range []bool{false, true} {
		for _, text := range []string{"明天或后天请假", "明天2月30日请假", "2月30日请假", "13月1日请假", "周一或周二请假"} {
			t.Run(map[bool]string{false: "rules_", true: "model_"}[model]+text, func(t *testing.T) {
				p := NewParser(func() time.Time { return anchor })
				if model {
					p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
						normalized := anchor.AddDate(0, 0, 1)
						return []Intent{{Kind: IntentPostpone, Date: &normalized, Confidence: 1}}, nil
					}))
				}
				got, err := p.ParseContextAt(context.Background(), text, anchor)
				require.NoError(t, err)
				plans := p.PlanAt(got, anchor)
				require.NotEmpty(t, plans)
				for _, plan := range plans {
					require.Equal(t, ActionNeedConfirm, plan.Action, "raw explicit dates must constrain both rules and model candidates")
				}
			})
		}
	}
}

func TestSubmissionDifferentPostponeTargetsNeedConfirmationTogether(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	first, second := anchor.AddDate(0, 0, 1), anchor.AddDate(0, 0, 2)
	p := NewParser(func() time.Time { return anchor })
	plans := p.PlanAt(ParseResult{Intents: []Intent{{Kind: IntentPostpone, Date: &first, Confidence: 1}, {Kind: IntentPostpone, Date: &second, Confidence: 1}}}, anchor)
	require.Len(t, plans, 2)
	for _, plan := range plans {
		require.Equal(t, ActionNeedConfirm, plan.Action)
	}
}

func TestSubmissionModelCannotTurnLiteralComplaintIntoAdjustment(t *testing.T) {
	anchor := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	p := NewParser(func() time.Time { return anchor })
	p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
		return []Intent{{Kind: IntentReduce, Confidence: 1}}, nil
	}))
	got, err := p.ParseContextAt(context.Background(), "投诉老师每天词太多", anchor)
	require.NoError(t, err)
	plans := p.PlanAt(got, anchor)
	require.NotEmpty(t, plans)
	escalated := false
	for _, plan := range plans {
		require.NotEqual(t, ActionApplyConfig, plan.Action, "a literal complaint is not permission to adjust learning settings")
		escalated = escalated || plan.Action == ActionEscalate
	}
	require.True(t, escalated)
}

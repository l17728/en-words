package feedback

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAmbiguousDateAcrossClausesCannotPostpone(t *testing.T) {
	anchor := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, text := range []string{"请假，明天或后天再上", "请假；明天或后天再上"} {
		for _, fallback := range []bool{false, true} {
			t.Run(text+fmt.Sprint(fallback), func(t *testing.T) {
				p := NewParser(func() time.Time { return anchor })
				if fallback {
					p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
						return nil, errors.New("provider unavailable")
					}))
				}
				parsed, _ := p.ParseContextAt(context.Background(), text, anchor)
				plans := p.PlanAt(parsed, anchor)
				require.NotEmpty(t, plans)
				for _, plan := range plans {
					require.NotEqual(t, ActionPostpone, plan.Action)
				}
			})
		}
	}
}

func TestAlternativeCalendarDatesCannotChooseOneForStudent(t *testing.T) {
	anchor := time.Date(2027, 4, 29, 10, 30, 0, 0, time.FixedZone("fixture", 8*3600))
	for _, text := range []string{"课程挪到五月一号或者二号", "改期到5月1日或2日", "请假；五月一日或者二日再上", "课程改期到五月一号，或者二号", "课程改期到五月一号；或者二号"} {
		t.Run(text, func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
				chosen := time.Date(2027, 5, 1, 0, 0, 0, 0, anchor.Location())
				return []Intent{{Kind: IntentPostpone, Date: &chosen, Confidence: 1}}, nil
			}))
			parsed, _ := p.ParseContextAt(context.Background(), text, anchor)
			for _, plan := range p.PlanAt(parsed, anchor) {
				require.NotEqual(t, ActionPostpone, plan.Action)
			}
		})
	}
}

func TestInvalidCalendarTextConstrainsAllWriteKinds(t *testing.T) {
	anchor := time.Date(2027, 4, 29, 10, 0, 0, 0, time.UTC)
	for _, kind := range []IntentKind{IntentReduce, IntentIncrease, IntentFrequency, IntentPostpone, IntentHardWord} {
		for _, text := range []string{"课程改到二月三十日", "改期到十三月一日", "课程挪到五月一号或者二号"} {
			t.Run(string(kind)+text, func(t *testing.T) {
				p := NewParser(func() time.Time { return anchor })
				p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
					date := anchor.AddDate(0, 0, 1)
					return []Intent{{Kind: kind, TargetWords: 50, LessonsPerDay: 2, Date: &date, Word: "apple", Confidence: 1}}, nil
				}))
				parsed, _ := p.ParseContextAt(context.Background(), text, anchor)
				for _, plan := range p.PlanAt(parsed, anchor) {
					require.Contains(t, []ActionType{ActionNeedConfirm, ActionClarify}, plan.Action)
				}
			})
		}
	}
}

func TestCalendarRulesPreserveLegalDatesAndNextNaturalWeek(t *testing.T) {
	for _, tc := range []struct{ text, anchor, want string }{
		{"改期到五月一号", "2027-04-29", "2027-05-01"},
		{"每课50个词，改期到五月一号", "2027-04-29", "2027-05-01"},
		{"课程改到下周二", "2027-04-26", "2027-05-04"},
		{"课程改到下周二", "2027-04-29", "2027-05-04"},
		{"课程改到下星期日", "2027-05-02", "2027-05-09"},
	} {
		t.Run(tc.text+tc.anchor, func(t *testing.T) {
			anchor, err := time.Parse("2006-01-02", tc.anchor)
			require.NoError(t, err)
			p := NewParser(func() time.Time { return anchor })
			parsed := p.Parse(tc.text)
			found := false
			for _, intent := range parsed.Intents {
				if intent.Kind == IntentPostpone {
					found = true
					require.NotNil(t, intent.Date)
					require.Equal(t, tc.want, intent.Date.Format("2006-01-02"))
					if intent.Date.Sub(anchor) <= 7*24*time.Hour {
						require.False(t, intent.NeedsConfirm)
					}
				}
			}
			require.True(t, found)
		})
	}
}

func TestRecordedHoldoutAlternativeDateResponseCannotPostpone(t *testing.T) {
	// Both HO-027 repetitions returned this response; source fingerprint d4d4e68f44254312733c3e7d880b2932b40ae56ee53ceeafa0d8ee9054017759.
	const response = `{"intents":[{"kind":"D_postpone","target_words":0,"lessons_per_day":0,"date":"2027-05-01","word":"","confidence":1,"needs_confirm":false}]}`
	anchor := time.Date(2027, 4, 29, 10, 30, 0, 0, time.FixedZone("fixture", 8*3600))
	p := NewParser(func() time.Time { return anchor })
	p.SetLLM(NewZhipuParser(&fixtureFeedbackChat{response: response}))
	parsed, err := p.ParseContextAt(context.Background(), "课程挪到五月一号或者二号", anchor)
	require.NoError(t, err)
	plans := p.PlanAt(parsed, anchor)
	require.Len(t, plans, 1)
	require.Equal(t, ActionNeedConfirm, plans[0].Action)
}

func TestModelDateMustMatchExplicitCalendarDayNotHour(t *testing.T) {
	anchor := time.Date(2027, 4, 29, 9, 0, 0, 0, time.FixedZone("fixture", 8*3600))
	for _, tc := range []struct {
		text  string
		day   int
		month time.Month
		allow bool
	}{
		{"明天再上课", 30, time.April, true}, {"明天再上课", 1, time.May, false},
		{"课程改到下周二", 4, time.May, true}, {"课程改到下周二", 3, time.May, false},
		{"课程挪到五月一号", 1, time.May, true}, {"课程挪到五月一号", 2, time.May, false},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.text, tc.day), func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
				date := time.Date(2027, tc.month, tc.day, 18, 0, 0, 0, anchor.Location())
				return []Intent{{Kind: IntentPostpone, Date: &date, Confidence: 1}}, nil
			}))
			parsed, _ := p.ParseContextAt(context.Background(), tc.text, anchor)
			plans := p.PlanAt(parsed, anchor)
			require.Len(t, plans, 1)
			if tc.allow {
				require.Equal(t, ActionPostpone, plans[0].Action)
			} else {
				require.Equal(t, ActionNeedConfirm, plans[0].Action)
			}
		})
	}
}

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

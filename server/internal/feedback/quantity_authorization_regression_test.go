package feedback

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"enwords/internal/obs"
	"github.com/stretchr/testify/require"
)

func TestExplicitQuantityCrossKindCannotAuthorizeWrites(t *testing.T) {
	anchor := time.Date(2029, 12, 30, 9, 0, 0, 0, time.UTC)
	next := anchor.AddDate(0, 0, 1)
	texts := []string{"每日安排0节课", "每日安排零课时", "每日学习三次课", "一日上12堂课", "每天-1节课", "每日1.5课时", "每日半节课", "每日一次半", "每日一次，或两次", "每日999999999999999999999999课时", "每课0个词", "每节课零个单词", "每课-50个词", "每课50.5个词", "每课120个词", "每课50个词，或60个词"}
	for _, prefix := range []string{"每日", "每天", "一天", "一日"} {
		for _, verb := range []string{"安排", "学习", "上"} {
			for _, count := range []string{"0节", "零课时", "三次"} {
				texts = append(texts, prefix+verb+count)
			}
		}
	}
	for _, unit := range []string{"课时", "节课", "次课", "堂课", "次", "遍", "节", "课"} {
		texts = append(texts, "每日安排0"+unit)
	}
	candidates := []Intent{{Kind: IntentReduce, Confidence: 1}, {Kind: IntentIncrease, Confidence: 1}, {Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}, {Kind: IntentPostpone, Date: &next, Confidence: 1}, {Kind: IntentHardWord, Word: "apple", Confidence: 1}}
	for n, text := range texts {
		t.Run(fmt.Sprintf("%d_%s", n, text), func(t *testing.T) {
			for _, candidate := range candidates {
				p := NewParser(func() time.Time { return anchor })
				p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) { return []Intent{candidate}, nil }))
				parsed, err := p.ParseContextAt(context.Background(), text, anchor)
				require.NoError(t, err)
				require.Equal(t, candidate.Kind, parsed.Intents[0].Kind, "guard must preserve model classification")
				require.True(t, parsed.Intents[0].NeedsConfirm, "%s must be constrained for %s", text, candidate.Kind)
				require.Equal(t, ActionNeedConfirm, p.PlanAt(parsed, anchor)[0].Action)
				// A caller cannot bypass raw constraints by planning without Parse.
				require.Equal(t, ActionNeedConfirm, p.PlanAt(ParseResult{Raw: text, Intents: []Intent{candidate}}, anchor)[0].Action)
			}
			p := NewParser(func() time.Time { return anchor })
			for _, action := range p.PlanAt(p.ParseRules(text), anchor) {
				require.NotContains(t, []ActionType{ActionApplyConfig, ActionPostpone, ActionBoost}, action.Action)
			}
		})
	}
}

func TestExplicitQuantityDimensionAndCandidateAgreement(t *testing.T) {
	anchor := time.Now()
	for _, tc := range []struct {
		text   string
		intent Intent
	}{
		{"每日安排两节课", Intent{Kind: IntentIncrease, Confidence: 1}},
		{"每日安排两节课", Intent{Kind: IntentReduce, TargetWords: 50, Confidence: 1}},
		{"每课50个词", Intent{Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}},
		{"每课50个词", Intent{Kind: IntentReduce, Confidence: 1}},
		{"每课50个词", Intent{Kind: IntentIncrease, TargetWords: 80, Confidence: 1}},
		{"每课50或60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"每课50或者60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"每课五十或者六十个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"每课50，或者60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"降到50或60个", Intent{Kind: IntentReduce, TargetWords: 60, Confidence: 1}},
		{"每课50至60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"每课50/60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"每课50-60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"每课50、60个词", Intent{Kind: IntentIncrease, TargetWords: 60, Confidence: 1}},
		{"以前每天三节课", Intent{Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}},
	} {
		t.Run(tc.text+string(tc.intent.Kind), func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) { return []Intent{tc.intent}, nil }))
			result, err := p.ParseContextAt(context.Background(), tc.text, anchor)
			require.NoError(t, err)
			require.Equal(t, ActionNeedConfirm, p.PlanAt(result, anchor)[0].Action)
			require.Equal(t, ActionNeedConfirm, p.PlanAt(ParseResult{Raw: tc.text, Intents: []Intent{tc.intent}}, anchor)[0].Action)
			if strings.ContainsAny(tc.text, "或至/、-") {
				for _, action := range p.PlanAt(p.ParseRules(tc.text), anchor) {
					require.NotContains(t, []ActionType{ActionApplyConfig, ActionPostpone, ActionBoost}, action.Action)
				}
			}
		})
	}
}

func TestExplicitQuantityPositiveIndependentDimensionsAndHistory(t *testing.T) {
	anchor := time.Now()
	for _, tc := range []struct {
		text       string
		candidates []Intent
	}{
		{"每日安排两节课", []Intent{{Kind: IntentFrequency, LessonsPerDay: 2, Confidence: 1}}},
		{"每日安排一课时", []Intent{{Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}}},
		{"每课50个词", []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}},
		{"降到50个", []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}},
		{"每课五十个词", []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}},
		{"每课六十个词", []Intent{{Kind: IntentReduce, TargetWords: 60, Confidence: 1}}},
		{"每课一百个词", []Intent{{Kind: IntentIncrease, TargetWords: 100, Confidence: 1}}},
		{"每天50个词", []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}},
		{"每日安排两节课，每课50个词", []Intent{{Kind: IntentFrequency, LessonsPerDay: 2, Confidence: 1}, {Kind: IntentReduce, TargetWords: 50, Confidence: 1}}},
		{"太多了；每天一次", []Intent{{Kind: IntentReduce, Confidence: 1}, {Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}}},
		{"每天一次，半小时", []Intent{{Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}}},
		{"每天一次，10分钟", []Intent{{Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}}},
		{"以前每天三节课；现在每天一次", []Intent{{Kind: IntentFrequency, LessonsPerDay: 1, Confidence: 1}}},
		{"过去每课0个词；以后每课50个词", []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) { return tc.candidates, nil }))
			parsed, err := p.ParseContextAt(context.Background(), tc.text, anchor)
			require.NoError(t, err)
			for _, item := range p.PlanAt(parsed, anchor) {
				require.Equal(t, ActionApplyConfig, item.Action)
			}
			p.SetLLM(nil)
			rules := p.ParseRules(tc.text)
			require.Len(t, rules.Intents, len(tc.candidates))
			for _, item := range p.PlanAt(rules, anchor) {
				require.Equal(t, ActionApplyConfig, item.Action)
			}
		})
	}
}

func TestExplicitQuantityRejectionTraceIsBoundedAndPrivate(t *testing.T) {
	anchor := time.Now()
	for _, model := range []bool{false, true} {
		for _, raw := range []string{"每日安排0节课 private-fixture", "每日1.5课时 private-fixture", "每日半节课 private-fixture"} {
			t.Run(fmt.Sprint(model), func(t *testing.T) {
				var sink bytes.Buffer
				p := NewParser(func() time.Time { return anchor })
				p.SetLogger(obs.NewWithSink(&sink))
				if model {
					p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
						return []Intent{{Kind: IntentIncrease, Confidence: 1}}, nil
					}))
				}
				_, err := p.ParseContextAt(obs.WithTrace(context.Background(), "quantity-trace"), raw, anchor)
				require.NoError(t, err)
				require.Contains(t, sink.String(), `"event":"feedback_constraint_rejected"`)
				require.Contains(t, sink.String(), `"trace_id":"quantity-trace"`)
				require.Contains(t, sink.String(), `"constraint":"daily_frequency"`)
				require.Contains(t, sink.String(), `"reason":"invalid_quantity"`)
				require.NotContains(t, sink.String(), "private-fixture")
				require.NotContains(t, sink.String(), "每日安排")
			})
		}
	}
}

package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFrequencyReadsCompleteQuantityWithoutInventingDefault(t *testing.T) {
	anchor := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		text   string
		count  int
		action ActionType
	}{
		{"每天一次", 1, ActionApplyConfig}, {"每天学一次", 1, ActionApplyConfig},
		{"每天1次", 1, ActionApplyConfig}, {"每天2遍", 2, ActionApplyConfig},
		{"每天二次", 2, ActionApplyConfig}, {"每天两次", 2, ActionApplyConfig},
		{"每天三次", 3, ActionNeedConfirm}, {"每天0次", 0, ActionNeedConfirm},
		{"每天3次", 3, ActionNeedConfirm}, {"每天4次", 4, ActionNeedConfirm},
		{"每天10次", 10, ActionNeedConfirm}, {"每天12次", 12, ActionNeedConfirm},
		{"每天3遍", 3, ActionNeedConfirm}, {"每天学几次", 0, ActionClarify},
		{"每天学习次数", 0, ActionClarify},
		{"每天次", 0, ActionClarify}, {"每天学次", 0, ActionClarify},
		{"每天4节", 4, ActionNeedConfirm}, {"每天5次", 5, ActionNeedConfirm},
		{"每天6次", 6, ActionNeedConfirm}, {"每天7次", 7, ActionNeedConfirm},
		{"每天8次", 8, ActionNeedConfirm}, {"每天9次", 9, ActionNeedConfirm},
		{"每天四次", 4, ActionNeedConfirm}, {"每天五次", 5, ActionNeedConfirm},
		{"每天六次", 6, ActionNeedConfirm}, {"每天七次", 7, ActionNeedConfirm},
		{"每天八次", 8, ActionNeedConfirm}, {"每天九次", 9, ActionNeedConfirm},
		{"每天零次", 0, ActionNeedConfirm}, {"每天〇次", 0, ActionNeedConfirm},
		{"每天十次", 10, ActionNeedConfirm}, {"每天二十次", 0, ActionNeedConfirm},
		{"每天三四次", 0, ActionNeedConfirm},
		{"每天学习 一 次", 1, ActionApplyConfig}, {"一日上 2 节", 2, ActionApplyConfig},
		{"一天学习两遍", 2, ActionApplyConfig}, {"每天10分钟", 0, ActionClarify},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := NewParser(func() time.Time { return anchor })
			parsed, err := p.ParseContextAt(context.Background(), tc.text, anchor)
			require.NoError(t, err)
			plans := p.PlanAt(parsed, anchor)
			require.Len(t, plans, 1)
			require.Equal(t, tc.action, plans[0].Action)
			if tc.action != ActionClarify {
				require.Equal(t, IntentFrequency, plans[0].Intent.Kind)
				require.Equal(t, tc.count, plans[0].Intent.LessonsPerDay)
			}
		})
	}
}

func TestFrequencyMalformedOrAmbiguousQuantityNeverApplies(t *testing.T) {
	for _, text := range []string{"每天-2次", "每天1.5次", "每天一次半", "每天一次或两次", "每天一次，或两次"} {
		t.Run(text, func(t *testing.T) {
			p := NewParser(time.Now)
			for _, plan := range p.Plan(p.Parse(text)) {
				require.NotEqual(t, ActionApplyConfig, plan.Action)
			}
		})
	}
}

func TestExplicitInvalidFrequencyCannotBeMisclassifiedIntoOtherWrites(t *testing.T) {
	for _, kind := range []IntentKind{IntentReduce, IntentIncrease, IntentPostpone, IntentHardWord} {
		for _, text := range []string{"每天0次", "每天12次", "每天三遍"} {
			t.Run(string(kind)+text, func(t *testing.T) {
				p := NewParser(time.Now)
				p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
					tomorrow := time.Now().AddDate(0, 0, 1)
					return []Intent{{Kind: kind, TargetWords: 0, Date: &tomorrow, Word: "apple", Confidence: 1}}, nil
				}))
				for _, plan := range p.Plan(p.Parse(text)) {
					require.NotEqual(t, ActionApplyConfig, plan.Action)
					require.NotEqual(t, ActionPostpone, plan.Action)
					require.NotEqual(t, ActionBoost, plan.Action)
				}
			})
		}
	}
}

func TestLegalFrequencyDoesNotBlockIndependentPostponeOrHardWord(t *testing.T) {
	for _, tc := range []struct {
		text   string
		kind   IntentKind
		action ActionType
	}{
		{"每天两次，apple太难", IntentHardWord, ActionBoost},
		{"每天两次，明天再上课", IntentPostpone, ActionPostpone},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := NewParser(time.Now)
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
				tomorrow := time.Now().AddDate(0, 0, 1)
				return []Intent{{Kind: tc.kind, Word: "apple", Date: &tomorrow, Confidence: 1}}, nil
			}))
			plans := p.Plan(p.Parse(tc.text))
			require.Len(t, plans, 1)
			require.Equal(t, tc.action, plans[0].Action)
		})
	}
}

func TestFrequencyModelCannotOverrideExplicitInvalidOrDifferentQuantity(t *testing.T) {
	for _, tc := range []struct {
		text     string
		proposed int
		expected ActionType
	}{
		{"每天4次", 1, ActionNeedConfirm}, {"每天12次", 1, ActionNeedConfirm},
		{"每天三遍", 1, ActionNeedConfirm}, {"每天2次", 1, ActionNeedConfirm},
		{"每天一次；每天两次", 1, ActionNeedConfirm},
		{"每天一次；每天-2次", 1, ActionNeedConfirm},
		{"每天一次半", 1, ActionNeedConfirm}, {"每天一次或两次", 1, ActionNeedConfirm},
		{"每天一次，或两次", 1, ActionNeedConfirm},
		{"我想上午和下午各学一回", 2, ActionApplyConfig},
		{"每天只安排一个学习时段", 1, ActionApplyConfig},
		{"每天学两次", 2, ActionApplyConfig},
		{"每天一次，50个词", 1, ActionApplyConfig},
		{"每天一次，半小时", 1, ActionApplyConfig},
		{"每天一次，10分钟", 1, ActionApplyConfig},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := NewParser(time.Now)
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
				return []Intent{{Kind: IntentFrequency, LessonsPerDay: tc.proposed, Confidence: 1}}, nil
			}))
			plans := p.Plan(p.Parse(tc.text))
			require.Len(t, plans, 1)
			require.Equal(t, tc.expected, plans[0].Action)
		})
	}
}

func TestFrequencyAllowsIndependentExplicitWordQuantity(t *testing.T) {
	p := NewParser(time.Now)
	plans := p.Plan(p.Parse("每天一次，50个词"))
	require.Len(t, plans, 2)
	require.Equal(t, IntentFrequency, plans[0].Intent.Kind)
	require.Equal(t, 1, plans[0].Intent.LessonsPerDay)
	require.Equal(t, ActionApplyConfig, plans[0].Action)
	require.Equal(t, 50, plans[1].Intent.TargetWords)
	require.Equal(t, ActionApplyConfig, plans[1].Action)
}

func TestFrequencyAllowsIndependentDurationExplanation(t *testing.T) {
	for _, text := range []string{"每天一次，半小时", "每天一次，10分钟"} {
		t.Run(text, func(t *testing.T) {
			p := NewParser(time.Now)
			plans := p.Plan(p.Parse(text))
			require.NotEmpty(t, plans)
			require.Equal(t, IntentFrequency, plans[0].Intent.Kind)
			require.Equal(t, 1, plans[0].Intent.LessonsPerDay)
			require.Equal(t, ActionApplyConfig, plans[0].Action)
		})
	}
}

func TestFrequencyOneRemainsIndependentOfRelativeWordReduction(t *testing.T) {
	p := NewParser(time.Now)
	parsed := p.Parse("太多了；每天一次")
	plans := p.Plan(parsed)
	require.Len(t, plans, 2)
	require.Equal(t, IntentReduce, plans[0].Intent.Kind)
	require.Equal(t, ActionApplyConfig, plans[0].Action)
	require.Equal(t, IntentFrequency, plans[1].Intent.Kind)
	require.Equal(t, 1, plans[1].Intent.LessonsPerDay)
	require.Equal(t, ActionApplyConfig, plans[1].Action)
}

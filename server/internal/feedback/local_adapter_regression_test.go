package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type fixtureFeedbackChat struct {
	response string
	err      error
	user     string
}

func (f *fixtureFeedbackChat) ChatJSON(_ context.Context, _, user string, out any) error {
	f.user = user
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.response), out)
}

func TestLocalFeedbackCandidatesAreValidatedBeforeActions(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.FixedZone("fixture", 8*3600))
	for _, tc := range []struct {
		name, response string
		action         ActionType
	}{
		{"reduce", `{"intents":[{"kind":"A_reduce","target_words":50,"confidence":0.9}]}`, ActionApplyConfig},
		{"frequency", `{"intents":[{"kind":"C_frequency","lessons_per_day":2,"confidence":0.9}]}`, ActionApplyConfig},
		{"bad date", `{"intents":[{"kind":"D_postpone","date":"2026-99-12","confidence":0.9}]}`, ActionClarify},
		{"future date", `{"intents":[{"kind":"D_postpone","date":"2026-10-13","confidence":0.9}]}`, ActionPostpone},
		{"low confidence", `{"intents":[{"kind":"A_reduce","target_words":50,"confidence":0.1}]}`, ActionClarify},
		{"unknown kind", `{"intents":[{"kind":"delete_everything","confidence":1}]}`, ActionClarify},
		{"empty kind", `{"intents":[{"kind":"","confidence":1}]}`, ActionClarify},
		{"hard word", `{"intents":[{"kind":"E_hard_word","word":" CAT ","confidence":1}]}`, ActionBoost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var complete map[string][]map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.response), &complete))
			for _, intent := range complete["intents"] {
				for key, value := range map[string]any{"target_words": 0, "lessons_per_day": 0, "date": "", "word": "", "confidence": 1, "needs_confirm": false} {
					if _, exists := intent[key]; !exists {
						intent[key] = value
					}
				}
			}
			payload, err := json.Marshal(complete)
			require.NoError(t, err)
			fake := &fixtureFeedbackChat{response: string(payload)}
			p := NewParser(func() time.Time { return now })
			p.SetLLM(NewZhipuParser(fake))
			parsed := p.Parse("fixture feedback")
			require.Len(t, parsed.Intents, 1)
			plan := p.Plan(parsed)
			require.Len(t, plan, 1)
			require.Equal(t, tc.action, plan[0].Action)
			require.Contains(t, fake.user, "2026-10-12")
			if tc.name == "future date" {
				require.Equal(t, now.Location(), parsed.Intents[0].Date.Location())
			}
			if tc.name == "hard word" {
				require.Equal(t, "cat", parsed.Intents[0].Word)
			}
		})
	}
}

func TestLocalFeedbackTransportOrEmptyOutputFallsBackToRules(t *testing.T) {
	for _, fake := range []*fixtureFeedbackChat{{err: errors.New("fixture timeout")}, {response: `{"intents":[]}`}, {response: `broken`}} {
		adapter := NewZhipuParser(fake)
		_, err := adapter.ParseIntents(context.Background(), "每次降到50个词", time.Now())
		require.Error(t, err)
		p := NewParser(func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) })
		p.SetLLM(adapter)
		parsed := p.Parse("每次降到50个词")
		require.Len(t, parsed.Intents, 1)
		require.Equal(t, IntentReduce, parsed.Intents[0].Kind)
		require.Equal(t, 50, parsed.Intents[0].TargetWords)
	}
}

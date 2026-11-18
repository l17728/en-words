package feedback

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestStructuredFeedbackRejectsMissingNullOrUnknownFields(t *testing.T) {
	for name, raw := range map[string]string{
		"missing target":      `{"intents":[{"kind":"A_reduce","confidence":1}]}`,
		"null target":         `{"intents":[{"kind":"A_reduce","target_words":null,"lessons_per_day":0,"date":"","word":"","confidence":1,"needs_confirm":false}]}`,
		"unknown kind":        `{"intents":[{"kind":"delete_everything","target_words":0,"lessons_per_day":0,"date":"","word":"","confidence":1,"needs_confirm":false}]}`,
		"confidence over one": `{"intents":[{"kind":"A_reduce","target_words":50,"lessons_per_day":0,"date":"","word":"","confidence":2,"needs_confirm":false}]}`,
		"null confirmation":   `{"intents":[{"kind":"A_reduce","target_words":50,"lessons_per_day":0,"date":"","word":"","confidence":1,"needs_confirm":null}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			adapter := NewZhipuParser(&fixtureFeedbackChat{response: raw})
			_, err := adapter.ParseIntents(context.Background(), "每次降到50个词", time.Now())
			require.Error(t, err)
			p := NewParser(time.Now)
			p.SetLLM(adapter)
			parsed := p.Parse("每次降到50个词")
			require.Len(t, parsed.Intents, 1)
			require.Equal(t, 50, parsed.Intents[0].TargetWords)
		})
	}
}

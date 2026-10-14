package feedback

import (
	"bytes"
	"context"
	"enwords/internal/obs"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestFeedbackFallbackLogsOnlyActualRuleDegradation(t *testing.T) {
	for _, mode := range []string{"success", "transport", "empty", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			var sink bytes.Buffer
			p := NewParser(time.Now)
			p.SetLogger(obs.NewWithSink(&sink))
			ctx, cancel := context.WithCancel(obs.WithTrace(context.Background(), "feedback-fixture-trace"))
			defer cancel()
			p.SetLLM(submissionSemanticParser(func(context.Context, string, time.Time) ([]Intent, error) {
				switch mode {
				case "transport":
					return nil, errors.New("upstream failed")
				case "empty":
					return nil, nil
				case "canceled":
					cancel()
					return nil, context.Canceled
				}
				return []Intent{{Kind: IntentReduce, TargetWords: 50, Confidence: 1}}, nil
			}))
			_, err := p.ParseContextAt(ctx, "每次降到50个词", time.Now())
			if mode == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			if mode == "transport" || mode == "empty" {
				require.Contains(t, sink.String(), `"event":"provider_fallback"`)
				require.Contains(t, sink.String(), `"trace_id":"feedback-fixture-trace"`)
				require.Contains(t, sink.String(), `"reason":"model_unavailable_or_invalid_intents"`)
			} else {
				require.NotContains(t, sink.String(), `"event":"provider_fallback"`)
			}
			require.NotContains(t, sink.String(), "每次降到50个词")
		})
	}
}

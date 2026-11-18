package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This is a small local matrix rather than a machine-specific SLA: it proves
// that the durable transition path stays linear as a lesson advances and
// records elapsed time for CI diagnostics.
func TestPilotTransitionPerformanceMatrix(t *testing.T) {
	const steps = 8
	latencies := make([]time.Duration, 0, steps)
	for i := 1; i <= steps; i++ {
		_, pilot, principal, plan := pilotCreationFixtureDraft(t)
		receipt, err := pilot.CreateIsolatedPilotLesson(context.Background(), principal, plan)
		require.NoError(t, err)
		started := time.Now()
		_, err = pilot.CommitPilotTransition(context.Background(), PilotTransition{
			Command:   PilotCommand{LessonID: receipt.LessonID, StudentID: receipt.StudentID, CommandID: "perf-" + string(rune('a'+i)), LogicalInputRevision: 1, QuestionKey: "pending", Kind: "lesson.start", Payload: `{"step":true}`},
			StateJSON: json.RawMessage(`{"phase":"show_word","step":1}`), FactsJSON: json.RawMessage(`{"phase":"show_word","step":1}`), ResultJSON: json.RawMessage(`{"accepted":true}`),
		})
		require.NoError(t, err)
		latencies = append(latencies, time.Since(started))
	}
	var total time.Duration
	for _, latency := range latencies {
		total += latency
	}
	t.Logf("pilot transition matrix steps=%d total=%s mean=%s max=%s", steps, total, total/steps, maxPilotLatency(latencies))
}

func maxPilotLatency(values []time.Duration) time.Duration {
	var max time.Duration
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

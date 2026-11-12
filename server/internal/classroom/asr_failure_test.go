package classroom

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestASRFailurePreservesRepeatAndAnswerOpportunity(t *testing.T) {
	for _, phase := range []string{PhaseRepeating, PhaseWaitAnswer} {
		t.Run(phase, func(t *testing.T) {
			m, cfg := newMachine(t, batchN(1))
			if phase == PhaseRepeating {
				teachToRepeating(m, cfg)
			} else {
				teachAllWords(m, cfg)
			}
			require.Equal(t, phase, m.Phase())
			before := m.Snapshot()
			require.NoError(t, m.PauseForInputFailure())
			require.True(t, m.Paused())
			require.True(t, m.Snapshot().InputFailed)
			require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "猫"}))
			require.Equal(t, before.Phase, m.Phase())
			require.Equal(t, before.AnswerStage, m.Snapshot().AnswerStage)
			m.Resume()
			require.False(t, m.Paused())
			require.False(t, m.Snapshot().InputFailed)
			require.Equal(t, before.Phase, m.Phase())
			require.Equal(t, before.AnswerStage, m.Snapshot().AnswerStage)
			if phase == PhaseRepeating {
				require.NoError(t, m.OnEvent(Event{Type: EvRepeatDone}))
				require.Equal(t, PhaseWaitAnswer, m.Phase())
			} else {
				require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "猫"}))
				require.Equal(t, PhaseJudging, m.Phase())
			}
		})
	}
}

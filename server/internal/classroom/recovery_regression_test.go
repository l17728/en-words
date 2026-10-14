package classroom

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRestoreRejectsInvalidRuntimePositionsWithoutMutation(t *testing.T) {
	for _, name := range []string{"unknown_phase", "negative_quiz_index", "quiz_index_beyond_queue", "missing_active_quiz_item"} {
		t.Run(name, func(t *testing.T) {
			m, cfg := newMachine(t, batchN(1))
			teachAllWords(m, cfg)
			before := m.Snapshot()
			invalid := m.Snapshot()
			switch name {
			case "unknown_phase":
				invalid.Phase = "unknown_phase"
			case "negative_quiz_index":
				invalid.QuizIdx = -1
			case "quiz_index_beyond_queue":
				invalid.QuizIdx = len(invalid.QuizQueue) + 1
			case "missing_active_quiz_item":
				invalid.QuizQueue = nil
			}
			require.Error(t, m.Restore(invalid))
			require.Equal(t, before, m.Snapshot(), "rejected snapshot must not mutate live classroom")
		})
	}
}

func TestRecoveryReplaysSegmentWithoutAdvancingOrDuplicatingPendingActions(t *testing.T) {
	m, _ := newMachine(t, batchN(1))
	require.NoError(t, m.OnEvent(Event{Type: EvWordClicked}))
	original := m.DrainActions()
	before := m.Snapshot()
	m.RecoverDelivery(false)
	actions := m.DrainActions()
	require.Len(t, actions, 1)
	require.Equal(t, ActPlayTTS, actions[0].Type)
	var playback Action
	for _, a := range original {
		if a.Type == ActPlayTTS {
			playback = a
		}
	}
	require.Equal(t, playback, actions[0])
	require.Equal(t, before, m.Snapshot())
	m.state.Paused = true
	m.RecoverDelivery(false)
	require.Empty(t, m.DrainActions())
}

func TestRecoveryInterruptedJudgingKeepsQuestionAndAttemptsWithoutPenalty(t *testing.T) {
	m, cfg := newMachine(t, batchN(1))
	teachAllWords(m, cfg)
	m.DrainActions()
	before := m.Snapshot()
	require.NoError(t, m.OnEvent(Event{Type: EvAnswerSubmitted, Text: "interrupted answer"}))
	require.Equal(t, PhaseJudging, m.Phase())
	m.DrainActions()
	m.RecoverDelivery(true)
	require.Equal(t, PhaseJudging, m.Phase())
	require.Empty(t, m.DrainActions())
	m.RecoverDelivery(false)
	after := m.Snapshot()
	require.Equal(t, PhaseWaitAnswer, after.Phase)
	require.Empty(t, after.PendingText)
	require.Equal(t, len(before.AnswerMs), len(after.AnswerMs))
	require.ElementsMatch(t, before.AnswerMs, after.AnswerMs)
	require.Equal(t, before.AnswerStage, after.AnswerStage)
	require.Equal(t, before.Judged, after.Judged)
	require.Equal(t, before.QuizQueue, after.QuizQueue)
	require.Equal(t, before.QuizIdx, after.QuizIdx)
	actions := m.DrainActions()
	require.Len(t, actions, 1)
	require.Equal(t, ActShowQuizItem, actions[0].Type)
}

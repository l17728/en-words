package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommitPilotTransitionPersistsReceiptCheckpointAndOutbox(t *testing.T) {
	_, pilot, principal, plan := pilotCreationFixtureDraft(t)
	receipt, err := pilot.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, err)
	tr := PilotTransition{
		Command:   PilotCommand{LessonID: receipt.LessonID, StudentID: receipt.StudentID, CommandID: "command-1", LogicalInputRevision: 1, QuestionKey: "pending", Kind: "lesson.start", Payload: `{"start":true}`},
		StateJSON: json.RawMessage(`{"phase":"show_word"}`), FactsJSON: json.RawMessage(`{"phase":"show_word"}`), ResultJSON: json.RawMessage(`{"accepted":true}`),
		Effects: []PilotEffect{{Kind: "show_word", Payload: json.RawMessage(`{"word_id":92}`)}},
	}
	got, err := pilot.CommitPilotTransition(context.Background(), tr)
	require.NoError(t, err)
	require.False(t, got.Replayed)
	require.Equal(t, int64(1), got.FromVersion)
	require.Equal(t, int64(2), got.ToVersion)
	replay, err := pilot.CommitPilotTransition(context.Background(), tr)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, got.DecisionID, replay.DecisionID)
}

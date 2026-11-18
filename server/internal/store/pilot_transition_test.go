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

func TestPilotCompletionClaimAndFinishAreIdempotent(t *testing.T) {
	_, pilot, principal, plan := pilotCreationFixtureDraft(t)
	receipt, err := pilot.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, err)
	transition, err := pilot.CommitPilotTransition(context.Background(), PilotTransition{
		Command:   PilotCommand{LessonID: receipt.LessonID, StudentID: receipt.StudentID, CommandID: "command-complete", LogicalInputRevision: 1, QuestionKey: "pending", Kind: "lesson.start", Payload: `{"start":true}`},
		StateJSON: json.RawMessage(`{"phase":"show_word"}`), FactsJSON: json.RawMessage(`{"phase":"show_word"}`), ResultJSON: json.RawMessage(`{"accepted":true}`),
	})
	require.NoError(t, err)
	fingerprint := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	require.NoError(t, pilot.EnqueuePilotCompletion(context.Background(), receipt.LessonID, receipt.StudentID, receipt.BookID, json.RawMessage(`{"done":true}`), json.RawMessage(`{"plan":1}`), 1, fingerprint, 100))
	require.NoError(t, pilot.EnqueuePilotCompletion(context.Background(), receipt.LessonID, receipt.StudentID, receipt.BookID, json.RawMessage(`{"done":true}`), json.RawMessage(`{"plan":1}`), 1, fingerprint, 101))
	job, claimed, err := pilot.ClaimPilotCompletion(context.Background(), receipt.LessonID, "worker-1", 200, 1000)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, int64(1), job.AttemptCount)
	_, claimed, err = pilot.ClaimPilotCompletion(context.Background(), receipt.LessonID, "worker-2", 300, 1000)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, pilot.CompletePilotCompletion(context.Background(), receipt.LessonID, "worker-1", transition.DecisionID, 400))
	require.Error(t, pilot.CompletePilotCompletion(context.Background(), receipt.LessonID, "worker-1", transition.DecisionID, 401))
}

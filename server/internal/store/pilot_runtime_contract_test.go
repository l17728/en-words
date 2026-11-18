package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPilotCommandValidationAndFingerprint(t *testing.T) {
	cmd := PilotCommand{LessonID: 9, StudentID: 7, CommandID: "c1", LogicalInputRevision: 0, QuestionKey: "q1", Kind: "answer.confirm_text", Payload: `{"text":"cat"}`}
	require.NoError(t, cmd.Validate(0, "q1"))
	fp, err := cmd.Fingerprint()
	require.NoError(t, err)
	require.Len(t, fp, 64)
	require.ErrorIs(t, cmd.Validate(1, "q1"), ErrPilotStaleRevision)
	cmd.Payload = ""
	_, err = cmd.Fingerprint()
	require.ErrorIs(t, err, ErrPilotInvalidCommand)
}

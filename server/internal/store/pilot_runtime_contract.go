package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

var (
	ErrPilotInvalidCommand = errors.New("store: invalid pilot command")
	ErrPilotStaleRevision  = errors.New("store: stale pilot revision")
)

// PilotCommand is the validated input envelope used by the future Room
// adapter. It deliberately excludes lesson state and decision fields.
type PilotCommand struct {
	LessonID             int64  `json:"lesson_id"`
	StudentID            int64  `json:"student_id"`
	CommandID            string `json:"command_id"`
	LogicalInputRevision int64  `json:"logical_input_revision"`
	QuestionKey          string `json:"question_key"`
	Kind                 string `json:"kind"`
	Payload              string `json:"payload"`
}

func (c PilotCommand) Validate(currentRevision int64, currentQuestion string) error {
	if c.LessonID <= 0 || c.StudentID <= 0 || strings.TrimSpace(c.CommandID) == "" ||
		c.LogicalInputRevision < 0 || strings.TrimSpace(c.QuestionKey) == "" ||
		strings.TrimSpace(c.Kind) == "" || len(c.Payload) > 2048 || c.LogicalInputRevision != currentRevision || c.QuestionKey != currentQuestion {
		if c.LogicalInputRevision != currentRevision {
			return ErrPilotStaleRevision
		}
		return ErrPilotInvalidCommand
	}
	return nil
}

func (c PilotCommand) Fingerprint() (string, error) {
	if strings.TrimSpace(c.Payload) == "" {
		return "", ErrPilotInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

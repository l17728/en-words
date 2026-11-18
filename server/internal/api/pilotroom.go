package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"enwords/internal/store"
)

// PilotRoom is the isolated runtime adapter. It never shares mutable state
// with the legacy Room; all durable transitions go through PilotStore.
type PilotRoom struct {
	app       *App
	lessonID  int64
	studentID int64
	taskID    string
	question  string
	revision  int64
}

func (a *App) EnterPilotRoom(ctx context.Context, lessonID, studentID int64, taskID string) (*PilotRoom, store.PilotCreatedState, error) {
	if a == nil || a.Pilot == nil {
		return nil, store.PilotCreatedState{}, store.ErrInitializationConflict
	}
	principal, err := store.BindPilotSystemPrincipal(studentID, strings.TrimSpace(taskID))
	if err != nil {
		return nil, store.PilotCreatedState{}, err
	}
	state, err := a.Pilot.LoadCreatedState(ctx, principal, lessonID)
	if err != nil {
		return nil, store.PilotCreatedState{}, err
	}
	question := state.QuestionKey
	if question == "" {
		question = "pending"
	}
	revision := state.LogicalInputRevision
	if revision == 0 {
		revision = state.CheckpointVersion - 1
	}
	return &PilotRoom{app: a, lessonID: lessonID, studentID: studentID, taskID: taskID, question: question, revision: revision}, state, nil
}

func (r *PilotRoom) Submit(ctx context.Context, commandID, kind, payload string) (store.PilotTransitionReceipt, error) {
	if r == nil || r.app == nil || r.app.Pilot == nil || strings.TrimSpace(commandID) == "" {
		return store.PilotTransitionReceipt{}, store.ErrPilotInvalidCommand
	}
	if len(payload) > 2048 || !json.Valid([]byte(payload)) {
		return store.PilotTransitionReceipt{}, store.ErrPilotInvalidCommand
	}
	next := r.revision + 1
	command := store.PilotCommand{LessonID: r.lessonID, StudentID: r.studentID, CommandID: commandID, LogicalInputRevision: next, QuestionKey: r.question, Kind: kind, Payload: payload}
	stateJSON := json.RawMessage(fmt.Sprintf(`{"phase":"%s","revision":%d}`, kind, next))
	factsJSON := json.RawMessage(fmt.Sprintf(`{"phase":"%s","revision":%d}`, kind, next))
	resultJSON := json.RawMessage(`{"accepted":true}`)
	receipt, err := r.app.Pilot.CommitPilotTransition(ctx, store.PilotTransition{Command: command, StateJSON: stateJSON, FactsJSON: factsJSON, ResultJSON: resultJSON})
	if err != nil {
		if replay, found, lookupErr := r.app.Pilot.LookupPilotReceipt(ctx, command); lookupErr == nil && found {
			r.revision = replay.ToVersion - 1
			r.question = kind
			return replay, nil
		}
		return store.PilotTransitionReceipt{}, err
	}
	if !receipt.Replayed {
		r.revision = next
		r.question = kind
	}
	return receipt, nil
}

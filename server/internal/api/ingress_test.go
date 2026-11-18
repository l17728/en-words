package api

import (
	"enwords/internal/classroom"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestClientIngressRejectsMissingDuplicateAndStale(t *testing.T) {
	_, _, _, r := setupRoom(t)
	ctx := func() TurnContext { r.mu.Lock(); defer r.mu.Unlock(); return r.contextLocked() }
	before := ctx()
	rejected := r.HandleClientEvent(ClientEnvelope{}, classroom.Event{Type: classroom.EvWordClicked})
	require.Equal(t, "event_rejected", rejected[0]["type"])
	require.Equal(t, before, ctx())
	envelope := ClientEnvelope{Context: before, EventID: "click-1"}
	r.HandleClientEvent(envelope, classroom.Event{Type: classroom.EvWordClicked})
	require.NotEqual(t, before, ctx())
	rejected = r.HandleClientEvent(envelope, classroom.Event{Type: classroom.EvWordClicked})
	require.Equal(t, "event_rejected", rejected[0]["type"])
	_, rejected = r.HandleClientVoice(ClientEnvelope{Context: before, EventID: "late-asr"}, "猫")
	require.Equal(t, "event_rejected", rejected[0]["type"])
	current := ctx()
	// A valid no-op still consumes its event ID; replay across voice/event paths
	// cannot inject a second answer even if the phase did not change.
	e := ClientEnvelope{Context: current, EventID: "same-turn"}
	r.HandleClientEvent(e, classroom.Event{Type: classroom.EvWordClicked})
	_, rejected = r.HandleClientVoice(e, "猫")
	require.Equal(t, "event_rejected", rejected[0]["type"])
}

func TestClientIngressRoomEpochInvalidatesOldCapture(t *testing.T) {
	app, _, lid, r := setupRoom(t)
	r.mu.Lock()
	before := r.contextLocked()
	sid := r.studentID
	r.mu.Unlock()
	app.Rooms.Drop(lid)
	next, _, err := app.Rooms.Enter(lid, sid)
	require.NoError(t, err)
	_, frames := next.HandleClientVoice(ClientEnvelope{Context: before, EventID: "old-room"}, "cat")
	require.Equal(t, "event_rejected", frames[0]["type"])
}

func TestClientIngressPauseResumeInvalidatesCapture(t *testing.T) {
	_, clock, _, r := setupRoom(t)
	r.mu.Lock()
	before := r.contextLocked()
	r.mu.Unlock()
	clock.Advance(61 * time.Minute)
	r.SessionTimeout()
	r.Resume()
	_, frames := r.HandleClientVoice(ClientEnvelope{Context: before, EventID: "before-pause"}, "cat")
	require.Equal(t, "event_rejected", frames[0]["type"])
}

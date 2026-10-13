package api

import (
	"testing"
	"time"

	"enwords/internal/classroom"
	"github.com/stretchr/testify/require"
)

func TestInitialDeliveryWaitsForJoinBeforeStartingAudio(t *testing.T) {
	_, clock, _, room := setupRoom(t)
	clock.Advance(30 * time.Second)
	room.Tick()
	require.Equal(t, classroom.PhaseShowWord, room.Machine().Phase())
	client := &fakeClient{}
	room.Join(client)
	room.Tick()
	require.Equal(t, classroom.PhaseShowWord, room.Machine().Phase(), "join starts a fresh display timeout")
	clock.Advance(time.Second)
	room.Tick()
	require.Equal(t, classroom.PhaseTTSSeg, room.Machine().Phase())
	var segments []int
	for _, frame := range client.frames {
		if action, ok := frame["action"].(classroom.Action); ok && action.Type == classroom.ActPlayTTS {
			segments = append(segments, action.SegIdx)
		}
	}
	require.Equal(t, []int{1}, segments, "first delivered business frame must be segment one")
}

func TestExplicitRESTEventActivatesDeliveryWithoutWebSocket(t *testing.T) {
	_, clock, _, room := setupRoom(t)
	clock.Advance(30 * time.Second)
	room.Tick()
	room.mu.Lock()
	envelope := ClientEnvelope{Context: room.contextLocked(), EventID: "rest-first-click"}
	room.mu.Unlock()
	room.HandleClientEvent(envelope, classroom.Event{Type: classroom.EvWordClicked})
	require.Equal(t, classroom.PhaseTTSSeg, room.Machine().Phase())
	before := room.Machine().Snapshot().TtsSegIdx
	clock.Advance(30 * time.Second)
	room.Tick()
	require.Greater(t, room.Machine().Snapshot().TtsSegIdx, before, "REST-only delivery retains normal timeout progression")
}

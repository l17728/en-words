package api

import (
	"strconv"
	"testing"

	"enwords/internal/classroom"
	"github.com/stretchr/testify/require"
)

func TestTTSPauseRetryAndExplicitSkipIngress(t *testing.T) {
	_, _, _, r := setupRoom(t)
	current := func() TurnContext { r.mu.Lock(); defer r.mu.Unlock(); return r.contextLocked() }
	r.HandleClientEvent(ClientEnvelope{Context: current(), EventID: "begin"}, classroom.Event{Type: classroom.EvWordClicked})
	before := r.Machine().Snapshot()
	segment := strconv.Itoa(before.TtsSegIdx)
	old := current()
	r.HandleClientEvent(ClientEnvelope{Context: old, EventID: "fail-1"}, classroom.Event{Type: "tts_failed", Text: segment})
	require.True(t, r.Machine().Paused())
	require.Equal(t, before.TtsSegIdx, r.Machine().Snapshot().TtsSegIdx)
	r.mu.Lock()
	pausedSegment := r.snapshotLocked()["tts_seg_idx"]
	r.mu.Unlock()
	require.Equal(t, before.TtsSegIdx, pausedSegment, "rejoined clients need the paused segment index")
	rejected := r.HandleClientEvent(ClientEnvelope{Context: old, EventID: "late-fail"}, classroom.Event{Type: "tts_failed", Text: segment})
	require.Equal(t, "event_rejected", rejected[0]["type"])
	r.Resume()
	require.False(t, r.Machine().Paused())
	require.Equal(t, before.TtsSegIdx, r.Machine().Snapshot().TtsSegIdx)
	rejected = r.HandleClientEvent(ClientEnvelope{Context: old, EventID: "old-ack"}, classroom.Event{Type: classroom.EvTTSFinished})
	require.Equal(t, "event_rejected", rejected[0]["type"])
	r.HandleClientEvent(ClientEnvelope{Context: current(), EventID: "fail-2"}, classroom.Event{Type: "tts_failed", Text: segment})
	require.True(t, r.Machine().Paused())
	r.HandleClientEvent(ClientEnvelope{Context: current(), EventID: "explicit-skip"}, classroom.Event{Type: "tts_skip", Text: segment})
	require.False(t, r.Machine().Paused())
	require.NotEqual(t, before.TtsSegIdx, r.Machine().Snapshot().TtsSegIdx)
}

package api

import (
	"enwords/internal/classroom"
	"enwords/internal/localmodel"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestTTSSegmentGetsFreshDeadlineEvenWhenPhaseNameUnchanged(t *testing.T) {
	_, clock, _, room := setupRoom(t)
	room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
	firstBudget, ok := room.Machine().TimeoutFor()
	require.True(t, ok)
	clock.Advance(firstBudget - time.Millisecond)
	room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
	before := room.Machine().Snapshot()
	require.Equal(t, classroom.PhaseTTSSeg, before.Phase)
	nextBudget, ok := room.Machine().TimeoutFor()
	require.True(t, ok)
	clock.Advance(nextBudget - time.Millisecond)
	room.Tick()
	require.Equal(t, before, room.Machine().Snapshot(), "previous segment's elapsed time must not expire this segment")
	clock.Advance(time.Millisecond)
	room.Tick()
	require.NotEqual(t, before, room.Machine().Snapshot(), "fresh deadline still eventually expires")
}

func TestLocalMediaBudgetPreventsPrematureTimeout(t *testing.T) {
	for _, phase := range []string{classroom.PhaseTTSSeg, classroom.PhaseRepeating, classroom.PhaseWaitAnswer} {
		t.Run(phase, func(t *testing.T) {
			app, clock, _, room := setupRoom(t)
			switch phase {
			case classroom.PhaseTTSSeg:
				room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
			case classroom.PhaseRepeating:
				room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
				for i := 0; i < 4; i++ {
					room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
				}
			case classroom.PhaseWaitAnswer:
				for i := 0; i < 5; i++ {
					teachOneRoom(room, ttsCfg{en: 3, rp: 1})
				}
			}
			require.Equal(t, phase, room.Machine().Phase())
			// Capabilities only: this test performs no HTTP/model request.
			app.Local = &localmodel.Client{Services: map[string]localmodel.Service{
				"tts": {Model: "fixture-tts", Capabilities: []string{"tts"}},
				"asr": {Model: "fixture-asr", Capabilities: []string{"asr"}},
			}}
			base, ok := room.Machine().TimeoutFor()
			require.True(t, ok)
			extra := 24 * time.Second
			if phase == classroom.PhaseTTSSeg {
				extra = 20 * time.Second
			}
			before := room.Machine().Snapshot()
			clock.Advance(base + extra - time.Millisecond)
			room.Tick()
			require.Equal(t, before, room.Machine().Snapshot())
			clock.Advance(time.Millisecond)
			room.Tick()
			require.NotEqual(t, before, room.Machine().Snapshot(), "media budget must be finite")
		})
	}
}

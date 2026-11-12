package api

import (
	"fmt"
	"testing"
	"time"

	"enwords/internal/classroom"
	"github.com/stretchr/testify/require"
)

func TestASRFailureIngressPreservesTurnAndRejectsLateEvents(t *testing.T) {
	for _, target := range []string{classroom.PhaseRepeating, classroom.PhaseWaitAnswer} {
		t.Run(target, func(t *testing.T) {
			_, clock, _, room := setupRoom(t)
			current := func() TurnContext { room.mu.Lock(); defer room.mu.Unlock(); return room.contextLocked() }
			sequence := 0
			send := func(kind string, context TurnContext) []Msg {
				sequence++
				return room.HandleClientEvent(ClientEnvelope{Context: context, EventID: fmt.Sprintf("asr-%d", sequence)}, classroom.Event{Type: kind})
			}
			for i := 0; i < 100 && room.Machine().Phase() != target; i++ {
				switch room.Machine().Phase() {
				case classroom.PhaseShowWord:
					send(classroom.EvWordClicked, current())
				case classroom.PhaseTTSSeg:
					send(classroom.EvTTSFinished, current())
				case classroom.PhaseRepeating:
					send(classroom.EvRepeatDone, current())
				default:
					t.Fatalf("unexpected phase %s", room.Machine().Phase())
				}
			}
			require.Equal(t, target, room.Machine().Phase())
			before := room.Machine().Snapshot()
			old := current()
			processed := send("asr_failed", old)
			require.NotEmpty(t, processed)
			require.True(t, room.Machine().Paused())
			require.True(t, room.Machine().Snapshot().InputFailed)
			require.Equal(t, before.AnswerStage, room.Machine().Snapshot().AnswerStage)
			require.Equal(t, "event_rejected", send("asr_failed", old)[0]["type"])
			require.Equal(t, "event_rejected", send(classroom.EvAnswerSubmitted, old)[0]["type"])
			clock.Advance(5 * time.Minute)
			room.Tick()
			require.True(t, room.Machine().Paused())
			room.Resume()
			room.Tick()
			require.False(t, room.Machine().Paused())
			require.False(t, room.Machine().Snapshot().InputFailed)
			require.Equal(t, before.Phase, room.Machine().Phase())
			require.Equal(t, "event_rejected", send(classroom.EvRepeatDone, old)[0]["type"])
			if target == classroom.PhaseRepeating {
				send(classroom.EvRepeatDone, current())
				require.NotEqual(t, before.WordIdx, room.Machine().Snapshot().WordIdx)
			} else {
				item := room.Machine().CurrentItem().ID()
				send(classroom.EvAnswerSubmitted, current())
				require.Equal(t, item, room.Machine().CurrentItem().ID())
			}
		})
	}
}

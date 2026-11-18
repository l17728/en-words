package api

import (
	"enwords/internal/classroom"
	"enwords/internal/localmodel"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type reminderDelayClient struct {
	fakeClient
	clock *fakeClock
	delay time.Duration
	fired bool
}

func (c *reminderDelayClient) Send(m Msg) error {
	_ = c.fakeClient.Send(m)
	if action, ok := m["action"].(classroom.Action); ok && action.Type == classroom.ActNudgeAnswer && c.delay > 0 && !c.fired {
		c.fired = true
		c.clock.Advance(c.delay)
	}
	return nil
}

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

func TestConfiguredShowWordLeadPreventsEarlyTickAndExpiresAtBoundary(t *testing.T) {
	_, clock, _, room := setupRoom(t)
	room.Join(&fakeClient{})
	before := room.Machine().Snapshot()
	require.Equal(t, classroom.PhaseShowWord, before.Phase)
	budget, ok := room.Machine().TimeoutFor()
	require.True(t, ok)
	require.Equal(t, 250*time.Millisecond, budget)

	room.Tick()
	require.Equal(t, before, room.Machine().Snapshot(), "first tick before the configured lead must not start playback")
	clock.Advance(budget - time.Millisecond)
	room.Tick()
	require.Equal(t, before, room.Machine().Snapshot(), "249 ms must not expire a 250 ms lead")
	clock.Advance(time.Millisecond)
	room.Tick()
	require.Equal(t, classroom.PhaseTTSSeg, room.Machine().Phase(), "at 250 ms the next scheduler tick may start playback")
}

func TestAnswerReminderStartsFreshTimeoutWindow(t *testing.T) {
	_, clock, _, room := setupRoom(t)
	client := &reminderDelayClient{clock: clock}
	room.Join(client)
	require.True(t, room.deliveryReady, "join must arm the timer")
	for i := 0; i < 5; i++ {
		teachOneRoom(room, ttsCfg{en: 3, rp: 1})
	}
	require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
	budget, ok := room.Machine().TimeoutFor()
	require.True(t, ok)
	client.delay = budget
	clock.Advance(budget)
	room.Tick()
	require.True(t, client.fired, "the nudge must reach the client before the delayed Tick returns")
	reminded := room.Machine().Snapshot()
	require.Equal(t, classroom.PhaseWaitAnswer, reminded.Phase)
	require.Equal(t, 1, reminded.AnswerStage)
	require.True(t, room.deliveryReady, "nudge must keep the timer armed")
	countFrames := func(kind string) int {
		count := 0
		for _, frame := range client.frames {
			if action, ok := frame["action"].(classroom.Action); ok && action.Type == kind {
				count++
			}
		}
		return count
	}
	require.Equal(t, 1, countFrames(classroom.ActNudgeAnswer))
	clock.Advance(time.Millisecond)
	room.Tick()
	require.Equal(t, reminded, room.Machine().Snapshot(), "the next tick must not spend the student's second answer window")
	require.Equal(t, 1, countFrames(classroom.ActNudgeAnswer), "a timer tick must not duplicate the reminder")
	clock.Advance(budget - time.Millisecond)
	room.Tick()
	require.NotEqual(t, reminded, room.Machine().Snapshot(), "the second deadline must still expire")
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

package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/classroom"
	"github.com/stretchr/testify/require"
)

type blockedRuntimeJudge struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (b *blockedRuntimeJudge) Judge(word string, meanings []string, text string) (string, error) {
	return b.JudgeContext(context.Background(), word, meanings, text)
}
func (b *blockedRuntimeJudge) JudgeContext(ctx context.Context, _ string, _ []string, _ string) (string, error) {
	b.calls.Add(1)
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return "correct", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestRuntimeCanceledOrRetiredJudgmentCannotConsumeAnswer(t *testing.T) {
	for _, operation := range []string{"pause", "drop", "request_cancel"} {
		t.Run(operation, func(t *testing.T) {
			a, clock, lid, room := setupRoom(t)
			for i := 0; i < 5; i++ {
				teachOneRoom(room, ttsCfg{en: 3, rp: 1})
			}
			before := room.Machine().Snapshot()
			if operation == "pause" {
				clock.Advance(61 * time.Minute)
			}
			model := &blockedRuntimeJudge{entered: make(chan struct{}), release: make(chan struct{})}
			a.Judge.LLM = model
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			room.mu.Lock()
			envelope := ClientEnvelope{Context: room.contextLocked(), EventID: "runtime-answer"}
			room.mu.Unlock()
			finished := make(chan []Msg, 1)
			defer close(model.release)
			go func() {
				finished <- room.HandleClientEventContext(ctx, envelope, classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "nonliteral-model-answer"})
			}()
			select {
			case <-model.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model not started")
			}
			duplicate := room.HandleClientEvent(envelope, classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "nonliteral-model-answer"})
			require.True(t, hasFrame(duplicate, "event_rejected"))
			require.EqualValues(t, 1, model.calls.Load())
			switch operation {
			case "pause":
				room.SessionTimeout()
			case "drop":
				a.Rooms.Drop(lid)
			case "request_cancel":
				cancel()
			}
			select {
			case frames := <-finished:
				require.False(t, hasFrame(frames, "judge_result"))
			case <-time.After(3 * time.Second):
				t.Fatal("canceled model did not return")
			}
			room.mu.Lock()
			after := room.machine.Snapshot()
			room.mu.Unlock()
			require.Equal(t, before.Judged, after.Judged)
			require.Equal(t, before.QuizIdx, after.QuizIdx)
			require.Equal(t, before.AnswerStage, after.AnswerStage)
		})
	}
}

func TestRuntimeJoinReissuesOnlyUnconfirmedPlaybackAndInvalidatesOldTurn(t *testing.T) {
	_, _, _, room := setupRoom(t)
	room.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
	require.Equal(t, classroom.PhaseTTSSeg, room.Machine().Phase())
	first := &fakeClient{}
	room.Join(first)
	room.mu.Lock()
	old := room.contextLocked()
	before := room.machine.Snapshot()
	room.mu.Unlock()
	replacement := &fakeClient{}
	room.Join(replacement)
	var replayed []classroom.Action
	for _, frame := range replacement.frames {
		if action, ok := frame["action"].(classroom.Action); ok && action.Type == classroom.ActPlayTTS {
			replayed = append(replayed, action)
		}
	}
	require.Len(t, replayed, 1, "rejoin must deliver exactly the unacknowledged segment")
	require.Equal(t, before.TtsSegIdx, replayed[0].SegIdx)
	room.mu.Lock()
	current := room.contextLocked()
	after := room.machine.Snapshot()
	room.mu.Unlock()
	require.NotEqual(t, old, current)
	require.Equal(t, before.TtsSegIdx, after.TtsSegIdx)
	rejected := room.HandleClientEvent(ClientEnvelope{Context: old, EventID: "late-tts-finished"}, classroom.Event{Type: classroom.EvTTSFinished}, first)
	require.True(t, hasFrame(rejected, "event_rejected"))
}

func TestRetiredFinishedRoomTickCannotCommitPendingSettlement(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	before, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	status := before.Status
	room.mu.Lock()
	state := room.machine.Snapshot()
	state.Phase = classroom.PhaseLessonEnd
	state.Finished = true
	require.NoError(t, room.machine.Restore(state))
	room.finished = false
	room.mu.Unlock()
	beforeNotifs := len(a.Notifications(room.studentID))
	a.Rooms.Drop(lid)
	room.Tick()
	after, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, status, after.Status)
	require.Nil(t, after.FinishedAt)
	require.Len(t, a.Notifications(room.studentID), beforeNotifs)
	_, exists := a.Report(lid)
	require.False(t, exists)
}

func TestRuntimeModelWaitDoesNotBlockRoomControl(t *testing.T) {
	for _, operation := range []string{"enter", "session_timeout"} {
		t.Run(operation, func(t *testing.T) {
			a, _, lessonID, room := setupRoom(t)
			for i := 0; i < 5; i++ {
				teachOneRoom(room, ttsCfg{en: 3, rp: 1})
			}
			require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
			model := &blockedRuntimeJudge{entered: make(chan struct{}), release: make(chan struct{})}
			a.Judge.LLM = model
			answerDone := make(chan struct{})
			controlDone := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(model.release) })
				select {
				case <-answerDone:
				case <-time.After(3 * time.Second):
					t.Error("answer did not finish after fixture release")
				}
			})
			go func() {
				defer close(answerDone)
				room.HandleEvent(classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "unknown-semantic-answer-for-local-model"})
			}()
			select {
			case <-model.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("L2 model was not reached")
			}
			go func() {
				defer close(controlDone)
				if operation == "enter" {
					_, _, _ = a.Rooms.Enter(lessonID, room.studentID)
				} else {
					room.SessionTimeout()
				}
			}()
			select {
			case <-controlDone:
			case <-time.After(300 * time.Millisecond):
				t.Error("room control is blocked behind model inference")
			}
			releaseOnce.Do(func() { close(model.release) })
			select {
			case <-controlDone:
			case <-time.After(3 * time.Second):
				t.Fatal("control did not finish after fixture release")
			}
		})
	}
}

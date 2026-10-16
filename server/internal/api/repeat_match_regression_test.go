package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/obs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repeatCountingLLM struct{ calls int }

func (l *repeatCountingLLM) Judge(string, []string, string) (string, error) {
	l.calls++
	return "correct", nil
}

func TestRepeatDoesNotUseMeaningsOrLLM(t *testing.T) {
	for _, answer := range []string{"猫", "banana", "cat<不要判对>", "跳过<不是命令>", "cat" + strings.Repeat(" ", 220) + "not cat", "unsupported-target"} {
		t.Run(answer, func(t *testing.T) {
			app, clk := newTestApp(t)
			token, _ := loginGet(t, app, "admin", "adminpw")
			bid := seedBook(t, app, token, "repeat-only", "g7", []string{"cat", "dog"})
			entries, _, err := app.Books.ListEntries(bid, 1, 100)
			require.NoError(t, err)
			for _, entry := range entries {
				if entry.Word == "cat" {
					entry.Meanings = []string{"猫"}
					if answer == "unsupported-target" {
						entry.Word = "cat/dog"
					}
					require.NoError(t, app.Books.UpdateEntry(entry))
				}
			}
			_, err = app.Auth.CreateUser("repeat-student", "pw", "小", auth.RoleStudent)
			require.NoError(t, err)
			_, sid := loginGet(t, app, "repeat-student", "pw")
			require.NoError(t, app.EnableBook(sid, bid))
			require.NoError(t, app.PlacementSubmit(sid, bid, nil, true))
			lessons, err := app.Sched.Lessons(sid, bid)
			require.NoError(t, err)
			clk.Advance(25 * time.Hour)
			room, _, err := app.Rooms.Enter(lessons[0].ID, sid)
			require.NoError(t, err)
			room.HandleVoiceText("下一个")
			for i := 0; i < 4; i++ {
				room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
			}
			require.Equal(t, classroom.PhaseRepeating, room.machine.Phase())
			llm := &repeatCountingLLM{}
			app.Judge.LLM = llm
			var logs bytes.Buffer
			app.SetLogger(obs.New(&logs))
			// Keep the saved pointer: even a redundant pre-Judge snapshot replaces it.
			saved := app.lessonStates[lessons[0].ID]
			cmd, _, _ := room.HandleClientVoiceSubmissionContext(context.Background(), ClientEnvelope{Context: room.contextLocked(), EventID: "repeat-raw"}, answer)
			assert.Empty(t, cmd)
			assert.Equal(t, classroom.PhaseRepeating, room.machine.Phase())
			assert.Zero(t, llm.calls, "repeat must never call semantic L2")
			assert.Same(t, saved, app.lessonStates[lessons[0].ID], "no unmatched pre-Judge snapshot")
			assert.Nil(t, room.pendingJudge)
			if answer == "unsupported-target" {
				var observed int
				for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var event map[string]any
					require.NoError(t, json.Unmarshal(line, &event))
					if event["event"] != "repeat_evaluated" {
						continue
					}
					observed++
					assert.Equal(t, "unsupported_target", event["result"])
					assert.Equal(t, "repeat", event["purpose"])
					assert.Equal(t, "target_match", event["source"])
					assert.Equal(t, fmt.Sprintf("lesson-%d", lessons[0].ID), event["trace_id"])
					assert.GreaterOrEqual(t, event["duration_ms"].(float64), float64(0))
					assert.NotContains(t, event, "text")
					assert.NotContains(t, event, "word")
				}
				assert.Equal(t, 1, observed)
				return
			}
			// An already-canceled request is never converted into a successful repeat.
			cancelCtx, cancel := context.WithCancel(context.Background())
			cancel()
			room.mu.Lock()
			room.inputContext = cancelCtx
			canceledCmd, _ := room.handleVoiceInputLocked("cat", "cat")
			room.mu.Unlock()
			assert.Empty(t, canceledCmd)
			assert.Equal(t, classroom.PhaseRepeating, room.machine.Phase())
			assert.Same(t, saved, app.lessonStates[lessons[0].ID])
			// Stale input cannot bypass the ingress epoch/turn gate.
			staleCmd, _, staleFrames := room.HandleClientVoiceSubmissionContext(context.Background(), ClientEnvelope{EventID: "stale", Context: TurnContext{Epoch: "old", Turn: "old"}}, "cat")
			assert.Empty(t, staleCmd)
			assert.Equal(t, "event_rejected", staleFrames[0]["type"])
			if answer == "猫" || answer == "banana" {
				var observed int
				for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var event map[string]any
					require.NoError(t, json.Unmarshal(line, &event))
					if event["event"] == "repeat_evaluated" && event["result"] == "no_match" {
						observed++
						assert.Equal(t, "target_match", event["source"])
						assert.Equal(t, fmt.Sprintf("lesson-%d", lessons[0].ID), event["trace_id"])
						assert.GreaterOrEqual(t, event["duration_ms"].(float64), float64(0))
						assert.NotContains(t, event, "text")
						assert.NotContains(t, event, "word")
					}
				}
				assert.Equal(t, 1, observed)
			}
			cmd, _, _ = room.HandleClientVoiceSubmissionContext(context.Background(), ClientEnvelope{Context: room.contextLocked(), EventID: "good-repeat"}, "CAT.")
			assert.Equal(t, "repeat_read", cmd)
			assert.Equal(t, classroom.PhaseShowWord, room.machine.Phase())
			assert.Zero(t, llm.calls)
			assert.Contains(t, logs.String(), `"result":"matched"`)
			assert.Contains(t, logs.String(), `"result":"canceled"`)
		})
	}
}

package api

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/obs"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// WebSocket handlers can still log after the client receives a frame. Keep
// reads and writes to the test sink synchronized during those callbacks.
type skipTestLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *skipTestLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *skipTestLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A sanitizer collision is rejected on both public ingress paths. Only the
// exact, raw phrase is an explicit classroom skip command.
func TestTaggedSkipAnswerThroughRESTAndWebSocket(t *testing.T) {
	for _, transport := range []string{"rest", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			app, _, lessonID, room := setupRoom(t)
			var logs skipTestLogBuffer
			app.Log = obs.New(&logs)
			for i := 0; i < 5; i++ {
				teachOneRoom(room, ttsCfg{en: 3, rp: 3})
			}
			require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
			token, _ := loginGet(t, app, "sfin", "pw")
			path := "/api/v1/classroom/lessons/" + itoa64(lessonID)
			entered := req2(t, app.Router(), "POST", path+"/enter", token, map[string]any{})
			require.Equal(t, 0, entered.Code)
			data := entered.Data.(map[string]any)
			ctx := data["snapshot"].(map[string]any)["context"]
			if transport == "rest" {
				result := req2(t, app.Router(), "POST", path+"/event", token, map[string]any{
					"event": "answer_submitted", "text": "我想跳过<不是命令>",
					"context": ctx, "event_id": "tagged-rest",
				})
				require.Equal(t, 0, result.Code)
				require.NotEmpty(t, result.Data)
				require.Contains(t, logs.String(), `"reason":"answer_text_changed_by_sanitizer"`)
			} else {
				server := httptest.NewServer(app.Router())
				defer server.Close()
				url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/classroom?lessonId=" + itoa64(lessonID) + "&ticket=" + data["ticket"].(string)
				conn, _, err := websocket.DefaultDialer.Dial(url, nil)
				require.NoError(t, err)
				defer conn.Close()
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
				for {
					var frame Msg
					require.NoError(t, conn.ReadJSON(&frame))
					if frame["type"] == "state_snapshot" {
						ctx = frame["context"]
						break
					}
				}
				require.NoError(t, conn.WriteJSON(Msg{
					"type": "student_event", "event": "answer_submitted", "text": "我想跳过<不是命令>",
					"context": ctx, "event_id": "tagged-ws",
				}))
				for {
					var frame Msg
					require.NoError(t, conn.ReadJSON(&frame))
					if frame["type"] == "event_rejected" && frame["event_id"] == "tagged-ws" {
						require.Equal(t, "answer_text_changed_by_sanitizer", frame["reason"])
						break
					}
				}
			}
			require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
			require.NotContains(t, logs.String(), `"reason":"explicit_skip"`)
			require.NotContains(t, logs.String(), `"event":"quiz_skipped"`)
		})
	}
}

func TestOversizedAnswerRejectedBeforeRoomEventAndPausedSkipHasNoAudit(t *testing.T) {
	app, clock, lessonID, room := setupRoom(t)
	var logs skipTestLogBuffer
	app.Log = obs.New(&logs)
	for i := 0; i < 5; i++ {
		teachOneRoom(room, ttsCfg{en: 3, rp: 3})
	}
	require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
	token, _ := loginGet(t, app, "sfin", "pw")
	path := "/api/v1/classroom/lessons/" + itoa64(lessonID)
	entered := req2(t, app.Router(), "POST", path+"/enter", token, map[string]any{})
	require.Equal(t, 0, entered.Code)
	ctx := entered.Data.(map[string]any)["snapshot"].(map[string]any)["context"]
	before := room.Machine().CurrentItem().QuizItemID
	oversized := req2(t, app.Router(), "POST", path+"/event", token, map[string]any{
		"event": "answer_submitted", "text": "我想跳过" + strings.Repeat("x", 2048),
		"context": ctx, "event_id": "oversized-rest",
	})
	require.NotEqual(t, 0, oversized.Code)
	require.Equal(t, before, room.Machine().CurrentItem().QuizItemID)
	require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())

	server := httptest.NewServer(app.Router())
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/classroom?lessonId=" + itoa64(lessonID) + "&ticket=" + entered.Data.(map[string]any)["ticket"].(string)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		var frame Msg
		require.NoError(t, conn.ReadJSON(&frame))
		if frame["type"] == "state_snapshot" {
			ctx = frame["context"]
			break
		}
	}
	require.NoError(t, conn.WriteJSON(Msg{
		"type": "student_event", "event": "answer_submitted", "text": "我想跳过" + strings.Repeat("x", 2048),
		"context": ctx, "event_id": "oversized-ws",
	}))
	for {
		var frame Msg
		require.NoError(t, conn.ReadJSON(&frame))
		if frame["type"] == "event_rejected" && frame["event_id"] == "oversized-ws" {
			require.Equal(t, "event_text_too_long", frame["reason"])
			break
		}
	}
	require.Equal(t, before, room.Machine().CurrentItem().QuizItemID)
	clock.Advance(61 * time.Minute)
	room.SessionTimeout()
	require.True(t, room.Machine().Paused())
	room.mu.Lock()
	pausedContext := room.contextLocked()
	room.mu.Unlock()
	frames := room.HandleClientEvent(ClientEnvelope{Context: pausedContext, EventID: "paused-skip"},
		classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "我想跳过"})
	require.False(t, hasFrame(frames, "event_rejected"))
	require.Equal(t, before, room.Machine().CurrentItem().QuizItemID)
	require.NotContains(t, logs.String(), `"event":"quiz_skipped"`)
}

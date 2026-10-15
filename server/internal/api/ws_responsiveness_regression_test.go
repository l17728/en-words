package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/classroom"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type wsBlockedJudge struct {
	entered, release, canceled chan struct{}
	once                       sync.Once
	calls                      atomic.Int32
	ignoreCancellation         bool
}

func (b *wsBlockedJudge) Judge(w string, m []string, s string) (string, error) {
	return b.JudgeContext(context.Background(), w, m, s)
}
func (b *wsBlockedJudge) JudgeContext(ctx context.Context, _ string, _ []string, _ string) (string, error) {
	b.calls.Add(1)
	b.once.Do(func() { close(b.entered) })
	if b.ignoreCancellation {
		<-b.release
		return "correct", nil
	}
	select {
	case <-b.release:
		return "correct", nil
	case <-ctx.Done():
		close(b.canceled)
		return "", ctx.Err()
	}
}

func TestWSSlowJudgeDoesNotBlockHeartbeatOrDisconnect(t *testing.T) {
	for _, operation := range []string{"heartbeat", "disconnect", "queue_full", "duplicate", "replacement", "session_timeout", "delayed_cleanup"} {
		t.Run(operation, func(t *testing.T) {
			app, clock, lid, room := setupRoom(t)
			for i := 0; i < 5; i++ {
				teachOneRoom(room, ttsCfg{en: 3, rp: 1})
			}
			srv := httptest.NewServer(app.Router())
			defer srv.Close()
			response := req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/enter", mustStuToken(t, app), map[string]any{})
			data := response.Data.(map[string]any)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/classroom?lessonId="+itoa64(lid)+"&ticket="+data["ticket"].(string), nil)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
			var first Msg
			require.NoError(t, conn.ReadJSON(&first))
			require.Equal(t, "state_snapshot", first["type"])
			room.mu.Lock()
			turn := room.contextLocked()
			before := room.machine.Snapshot()
			room.mu.Unlock()
			model := &wsBlockedJudge{entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
			app.Judge.LLM = model
			model.ignoreCancellation = operation == "delayed_cleanup"
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(model.release) }) }
			defer release()
			require.NoError(t, conn.WriteJSON(Msg{"type": "student_event", "event": classroom.EvAnswerSubmitted, "text": "nonliteral-model-answer", "event_id": "ws-block-answer", "context": turn}))
			select {
			case <-model.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model did not start")
			}
			if operation == "delayed_cleanup" {
				require.NoError(t, conn.Close())
				// Deliberately exceed the bounded reader cleanup wait. The worker's
				// own finalizer must still remove this client when the provider returns.
				time.Sleep(10100 * time.Millisecond)
				release()
				require.Eventually(t, func() bool { room.mu.Lock(); defer room.mu.Unlock(); return len(room.clients) == 0 }, 3*time.Second, 10*time.Millisecond)
				room.mu.Lock()
				after := room.machine.Snapshot()
				room.mu.Unlock()
				require.Equal(t, before.Judged, after.Judged)
			} else if operation == "disconnect" || operation == "queue_full" || operation == "replacement" || operation == "session_timeout" {
				switch operation {
				case "disconnect":
					require.NoError(t, conn.Close())
				case "queue_full":
					for i := 0; i < 18; i++ {
						if conn.WriteJSON(Msg{"type": "resume"}) != nil {
							break
						}
					}
				case "replacement":
					other, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/classroom?lessonId="+itoa64(lid)+"&ticket="+data["ticket"].(string), nil)
					require.NoError(t, err)
					defer other.Close()
				case "session_timeout":
					clock.Advance(61 * time.Minute)
					room.SessionTimeout()
				}
				select {
				case <-model.canceled:
				case <-time.After(500 * time.Millisecond):
					t.Fatal("socket close did not cancel blocked L2 within 500ms")
				}
				room.mu.Lock()
				after := room.machine.Snapshot()
				room.mu.Unlock()
				require.Equal(t, before.Judged, after.Judged)
			} else if operation == "duplicate" {
				require.NoError(t, conn.WriteJSON(Msg{"type": "student_event", "event": classroom.EvAnswerSubmitted, "text": "nonliteral-model-answer", "event_id": "ws-block-answer", "context": turn}))
				release()
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
				for {
					var frame Msg
					require.NoError(t, conn.ReadJSON(&frame))
					if frame["type"] == "event_rejected" {
						break
					}
				}
				require.EqualValues(t, 1, model.calls.Load())
			} else {
				require.NoError(t, conn.WriteJSON(Msg{"type": "heartbeat"}))
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(500*time.Millisecond)))
				for {
					var frame Msg
					require.NoError(t, conn.ReadJSON(&frame), "heartbeat must arrive while model remains blocked")
					if frame["type"] == "heartbeat_ack" {
						break
					}
				}
			}
		})
	}
}

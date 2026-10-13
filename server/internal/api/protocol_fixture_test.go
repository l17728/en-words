package api

import (
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// The event completion marker drains responses to the preceding event, retaining
// the latest server-issued context instead of guessing phase/turn identifiers.
func sendFixtureWS(t *testing.T, c *websocket.Conn, ctx *any, event Msg, id string) []Msg {
	t.Helper()
	event["context"] = *ctx
	event["event_id"] = id
	require.NoError(t, c.WriteJSON(event))
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	var frames []Msg
	for {
		var f Msg
		require.NoError(t, c.ReadJSON(&f))
		if v, ok := f["context"]; ok {
			*ctx = v
		}
		if f["type"] == "event_processed" && f["event_id"] == id {
			return frames
		}
		require.NotEqual(t, "event_rejected", f["type"], "fixture must use latest server context")
		frames = append(frames, f)
	}
}

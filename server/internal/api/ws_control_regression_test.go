package api

import (
	"context"
	"enwords/internal/obs"
	"errors"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedWSFrameWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type failingWSFrameWriter struct{ err error }

func (w *failingWSFrameWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *failingWSFrameWriter) WriteJSON(any) error              { return nil }
func (w *failingWSFrameWriter) Close() error                     { return w.err }

func (w *blockedWSFrameWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *blockedWSFrameWriter) WriteJSON(any) error {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return nil
}
func (w *blockedWSFrameWriter) Close() error { return nil }

func TestWebSocketLeaseContextOutlivesHTTPRequest(t *testing.T) {
	requestCtx, cancel := context.WithCancel(obs.WithTrace(context.Background(), "trace-upgrade-fixture"))
	leaseCtx := websocketLeaseContext(requestCtx)
	cancel()
	require.ErrorIs(t, requestCtx.Err(), context.Canceled)
	require.NoError(t, leaseCtx.Err(), "HTTP 101 handler return must not cancel the upgraded lease")
	require.Equal(t, "trace-upgrade-fixture", obs.TraceID(leaseCtx))
}

func TestWSWriterPermitDrainsBeforeGenerationAdvance(t *testing.T) {
	generations := newGenerationCoordinator()
	lease, err := generations.acquire(context.Background(), 808)
	require.NoError(t, err)
	writer := &blockedWSFrameWriter{started: make(chan struct{}), release: make(chan struct{})}
	client := newWSClientWithWriter(writer)
	client.writePermit = lease.Token().Permit
	removeCloser, err := lease.AddCloser(func() { _ = client.Close() })
	require.NoError(t, err)
	require.NoError(t, client.Send(Msg{"type": "old_generation_frame"}))
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach the blocked socket write")
	}
	workerDone := make(chan struct{})
	releaseWSLeaseAfterWorkers(lease, client, workerDone, removeCloser)
	retirement, err := generations.retire(808)
	require.NoError(t, err)
	close(workerDone)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, retirement.Wait(ctx), context.DeadlineExceeded, "drain must include the in-flight socket write")
	select {
	case <-client.writerDone:
		t.Fatal("writer exited before the blocked write was released")
	default:
	}

	close(writer.release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, retirement.Wait(ctx))
	require.NoError(t, retirement.Advance())
	select {
	case <-client.writerDone:
	default:
		t.Fatal("generation advanced before writer pump exited")
	}
	if _, err := lease.Token().Permit(); !errors.Is(err, errGenerationRetired) {
		t.Fatalf("old writer token admitted after advance: %v", err)
	}
}

func TestWSHeartbeatCapacityAndClosedSend(t *testing.T) {
	c := &wsClient{send: make(chan Msg, 64), control: make(chan Msg, 1), done: make(chan struct{})}
	for i := 0; i < 64; i++ {
		require.NoError(t, c.Send(Msg{"type": "fixture"}))
	}
	for i := 0; i < 100; i++ {
		require.NoError(t, c.Send(Msg{"type": "heartbeat_ack"}))
	}
	require.Len(t, c.control, 1, "redundant unsequenced ACKs coalesce")
	require.Len(t, c.send, 64, "heartbeat never displaces ordinary queued messages")
	require.ErrorIs(t, c.Send(Msg{"type": "overflow"}), errBackpressure)
	require.True(t, c.IsClosed(), "overflow must force recovery instead of silently losing business frames")
	select {
	case <-c.done:
	default:
		t.Fatal("overflow did not close the connection lifecycle")
	}
	require.NoError(t, c.Close())
	for i := 0; i < 100; i++ {
		require.ErrorIs(t, c.Send(Msg{"type": "heartbeat_ack"}), errClosed)
		require.ErrorIs(t, c.Send(Msg{"type": "fixture"}), errClosed)
	}
	require.NoError(t, c.Close())
}

func TestWSClientCloseReportsAndRetainsSocketCloseError(t *testing.T) {
	want := errors.New("fixture socket close failure")
	client := newWSClientWithWriter(&failingWSFrameWriter{err: want})
	require.ErrorIs(t, client.Close(), want)
	select {
	case <-client.writerDone:
	case <-time.After(time.Second):
		t.Fatal("writer did not exit after socket close")
	}
	require.ErrorIs(t, client.Close(), want, "idempotent close must preserve the original failure")
}

func TestWSOrdinaryFramesProgressDuringContinuousHeartbeats(t *testing.T) {
	clients := make(chan *wsClient, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := newWSClient(conn)
		clients <- c
		<-c.done
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	c := <-clients
	defer c.Close()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
				_ = c.Send(Msg{"type": "heartbeat_ack"})
				runtime.Gosched()
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	for i := 0; i < 32; i++ {
		require.NoError(t, c.Send(Msg{"type": "ordinary", "index": i}))
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	received := 0
	for received < 32 {
		var message Msg
		require.NoError(t, conn.ReadJSON(&message))
		if message["type"] == "ordinary" {
			require.EqualValues(t, received, message["index"])
			received++
		}
	}
}

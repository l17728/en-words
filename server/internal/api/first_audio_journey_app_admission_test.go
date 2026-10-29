package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type firstAudioAdmissionBridge struct{ gate *firstAudioAdmission }

func (b firstAudioAdmissionBridge) AcquireOperation(parent context.Context) (context.Context, func(), error) {
	ctx, lease, err := b.gate.AcquireOperation(parent)
	if err != nil {
		return nil, nil, err
	}
	return ctx, lease.Release, nil
}

func (b firstAudioAdmissionBridge) OperationContextActive(ctx context.Context) bool {
	return b.gate.OperationContextActive(ctx)
}

func (b firstAudioAdmissionBridge) ReserveSession(closeSocket func(context.Context) error) (firstAudioWebSocketSession, error) {
	return b.gate.ReserveSession(closeSocket)
}

type firstAudioRejectSessionBridge struct {
	firstAudioAdmissionBridge
	attempts atomic.Int32
}

type firstAudioBlockingWSAdmissionBridge struct {
	firstAudioAdmissionBridge
	acquisitions    atomic.Int32
	messageAcquired chan struct{}
	allowMessage    chan struct{}
}

func setFirstAudioGinTestMode(t *testing.T) {
	t.Helper()
	previous := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previous) })
}

func (b *firstAudioBlockingWSAdmissionBridge) AcquireOperation(parent context.Context) (context.Context, func(), error) {
	ctx, lease, err := b.gate.AcquireOperation(parent)
	if err != nil {
		return nil, nil, err
	}
	if b.acquisitions.Add(1) == 3 {
		close(b.messageAcquired)
		select {
		case <-b.allowMessage:
		case <-parent.Done():
			lease.Release()
			return nil, nil, parent.Err()
		}
	}
	return ctx, lease.Release, nil
}

func (b *firstAudioRejectSessionBridge) ReserveSession(func(context.Context) error) (firstAudioWebSocketSession, error) {
	b.attempts.Add(1)
	return nil, errFirstAudioAdmissionClosed
}

func TestFirstAudioJourneyAdmissionAppHandlerLeaseDrainsBeforeReset(t *testing.T) {
	setFirstAudioGinTestMode(t)
	gate, err := newFirstAudioAdmission("app_handler_drain_20261029", 1)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{firstAudioRequestAdmission: firstAudioAdmissionBridge{gate: gate}}
	entered := make(chan context.Context, 1)
	resume := make(chan struct{})
	router := gin.New()
	router.Use(app.firstAudioOperationLeaseMW())
	router.GET("/held", func(c *gin.Context) {
		entered <- c.Request.Context()
		<-resume
		c.Status(http.StatusNoContent)
	})
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/held", nil))
		requestDone <- response
	}()
	operationCtx := <-entered
	if !gate.OperationContextActive(operationCtx) {
		t.Fatal("handler did not receive its active operation context")
	}

	resetCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resetDone := make(chan struct {
		barrier *firstAudioResetBarrier
		err     error
	}, 1)
	go func() {
		barrier, resetErr := gate.BeginReset(resetCtx)
		resetDone <- struct {
			barrier *firstAudioResetBarrier
			err     error
		}{barrier, resetErr}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		closed := !gate.accepting
		gate.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset did not close operation admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-resetDone:
		t.Fatal("reset crossed a handler with a live operation lease")
	default:
	}
	close(resume)
	response := <-requestDone
	if response.Code != http.StatusNoContent {
		t.Fatalf("handler status=%d want=%d", response.Code, http.StatusNoContent)
	}
	result := <-resetDone
	if result.err != nil || result.barrier == nil {
		t.Fatalf("reset did not drain handler lease: barrier=%v err=%v", result.barrier, result.err)
	}
	if generation, publishErr := result.barrier.PublishNext(); publishErr != nil || generation != 2 {
		t.Fatalf("publish next generation=%d err=%v", generation, publishErr)
	}
}

func TestFirstAudioJourneyAdmissionAppRejectsBeforeHandlerWhenClosed(t *testing.T) {
	setFirstAudioGinTestMode(t)
	gate, err := newFirstAudioAdmission("app_closed_20261029", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	barrier, err := gate.BeginReset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{firstAudioRequestAdmission: firstAudioAdmissionBridge{gate: gate}}
	var calls atomic.Int32
	router := gin.New()
	router.Use(app.firstAudioOperationLeaseMW())
	router.GET("/closed", func(c *gin.Context) { calls.Add(1); c.Status(http.StatusNoContent) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/closed", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed admission status=%d want=%d", response.Code, http.StatusServiceUnavailable)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler reached %d times while admission was closed", calls.Load())
	}
	if generation, publishErr := barrier.PublishNext(); publishErr != nil || generation != 2 {
		t.Fatalf("publish next generation=%d err=%v", generation, publishErr)
	}
}

func TestFirstAudioJourneyAdmissionRouterInstallsRequestLease(t *testing.T) {
	setFirstAudioGinTestMode(t)
	gate, err := newFirstAudioAdmission("app_router_gate_20261029", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	barrier, err := gate.BeginReset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(nil, nil)
	app.firstAudioRequestAdmission = firstAudioAdmissionBridge{gate: gate}
	response := httptest.NewRecorder()
	app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed router admission status=%d want=%d", response.Code, http.StatusServiceUnavailable)
	}
	if generation, publishErr := barrier.PublishNext(); publishErr != nil || generation != 2 {
		t.Fatalf("publish next generation=%d err=%v", generation, publishErr)
	}
}

func TestFirstAudioJourneyAdmissionWebSocketReservesBeforeLessonRead(t *testing.T) {
	setFirstAudioGinTestMode(t)
	gate, err := newFirstAudioAdmission("app_ws_reservation_20261029", 1)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &firstAudioRejectSessionBridge{firstAudioAdmissionBridge: firstAudioAdmissionBridge{gate: gate}}
	app := NewApp(nil, nil)
	app.firstAudioRequestAdmission = bridge
	response := httptest.NewRecorder()
	app.Router().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ws/classroom", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("session reservation status=%d want=%d", response.Code, http.StatusServiceUnavailable)
	}
	if bridge.attempts.Load() != 1 {
		t.Fatalf("session reservation attempts=%d want=1", bridge.attempts.Load())
	}
	if operations, sessions, _, _, _ := gate.counts(); operations != 0 || sessions != 0 {
		t.Fatalf("request lease leaked after reservation rejection: operations=%d sessions=%d", operations, sessions)
	}
}

func TestFirstAudioJourneyAdmissionWebSocketMessageDrainsBeforeReset(t *testing.T) {
	app, _, lessonID, _ := setupRoom(t)
	studentToken := mustStuToken(t, app)
	gate, err := newFirstAudioAdmission("ws_message_reset_drain_20261029", 1)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &firstAudioBlockingWSAdmissionBridge{
		firstAudioAdmissionBridge: firstAudioAdmissionBridge{gate: gate},
		messageAcquired:           make(chan struct{}), allowMessage: make(chan struct{}),
	}
	var allowOnce sync.Once
	allowMessage := func() { allowOnce.Do(func() { close(bridge.allowMessage) }) }
	defer allowMessage()
	app.firstAudioRequestAdmission = bridge
	router := app.Router()
	enter := req2(t, router, http.MethodPost, "/api/v1/classroom/lessons/"+itoa64(lessonID)+"/enter", studentToken, map[string]any{})
	if enter.Code != 0 {
		t.Fatalf("enter lesson: code=%d msg=%s", enter.Code, enter.Msg)
	}
	data, ok := enter.Data.(map[string]any)
	if !ok || data["ticket"] == "" {
		t.Fatalf("lesson enter returned no WebSocket ticket: %#v", enter.Data)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/classroom?lessonId=" + itoa64(lessonID) + "&ticket=" + url.QueryEscape(data["ticket"].(string))
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 3 * time.Second
	peer, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("connect classroom WebSocket: %v", err)
	}
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	var initial Msg
	if err := peer.ReadJSON(&initial); err != nil {
		t.Fatalf("read initial classroom frame: %v", err)
	}
	if err := peer.WriteJSON(Msg{"type": "student_event", "event": "tts_finished", "event_id": "reset-drain-event"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bridge.messageAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket message did not acquire its operation lease")
	}

	resetCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type resetResult struct {
		barrier *firstAudioResetBarrier
		err     error
	}
	resetDone := make(chan resetResult, 1)
	go func() {
		barrier, resetErr := gate.BeginReset(resetCtx)
		resetDone <- resetResult{barrier: barrier, err: resetErr}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		closed := !gate.accepting
		gate.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset did not close admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-resetDone:
		t.Fatal("reset crossed an operation held by a WebSocket message")
	default:
	}
	allowMessage()
	var result resetResult
	select {
	case result = <-resetDone:
	case <-time.After(3 * time.Second):
		t.Fatal("reset did not drain WebSocket and writer goroutines")
	}
	if result.err != nil || result.barrier == nil {
		t.Fatalf("reset result barrier=%v err=%v", result.barrier, result.err)
	}
	if operations, sessions, _, _, _ := gate.counts(); operations != 0 || sessions != 0 {
		t.Fatalf("leases remain after WebSocket drain: operations=%d sessions=%d", operations, sessions)
	}
	if generation, publishErr := result.barrier.PublishNext(); publishErr != nil || generation != 2 {
		t.Fatalf("publish next generation=%d err=%v", generation, publishErr)
	}
}

func TestFirstAudioWebSocketCloserRevokesBeforeUpgrade(t *testing.T) {
	serverConn, clientConn, finish := openAdmissionTestWebSocket(t)
	defer finish()
	closer := &firstAudioWebSocketCloser{}
	if err := closer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closer.Attach(serverConn) {
		t.Fatal("socket attached after its reservation was revoked")
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := clientConn.ReadMessage(); err == nil {
		t.Fatal("client socket remained open after pre-upgrade revocation")
	}
}

func TestFirstAudioWebSocketCloserClosesAttachedSocket(t *testing.T) {
	serverConn, clientConn, finish := openAdmissionTestWebSocket(t)
	defer finish()
	closer := &firstAudioWebSocketCloser{}
	if !closer.Attach(serverConn) {
		t.Fatal("active reservation rejected socket attachment")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := closer.Close(closeCtx); err != nil {
		t.Fatalf("close active socket: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := clientConn.ReadMessage(); err == nil {
		t.Fatal("active socket remained open after reset revocation")
	}
}

func openAdmissionTestWebSocket(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()
	serverConns := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConns <- conn
		_, _, _ = conn.ReadMessage()
	}))
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 3 * time.Second
	client, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial admission test socket: %v", err)
	}
	serverConn := <-serverConns
	return serverConn, client, func() {
		_ = client.Close()
		_ = serverConn.Close()
		server.Close()
	}
}

func TestWSWritePumpJoinsAfterClose(t *testing.T) {
	clients := make(chan *wsClient, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := newWSClient(conn)
		clients <- client
		<-client.done
	}))
	defer server.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	client := <-clients
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.writeDone:
	case <-time.After(time.Second):
		t.Fatal("write pump did not join after socket close")
	}
}

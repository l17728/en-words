// WS 课堂频道（SDD §11）：ticket 鉴权 → Join（首帧快照）→
// 读泵（student_event / voice_text / heartbeat）→ 双工广播。
package api

import (
	"context"
	"encoding/json"
	"enwords/internal/auth"
	"enwords/internal/obs"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"enwords/internal/classroom"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var (
	errClosed       = errors.New("ws: closed")
	errBackpressure = errors.New("ws: backpressure")
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 同源部署
}

// wsClient 适配 Client 接口；写串行化（gorilla 单写者约束）。
type wsClient struct {
	conn           *websocket.Conn
	send           chan Msg
	control        chan Msg
	done           chan struct{}
	writeDone      chan struct{}
	mu             sync.Mutex
	closed         bool
	onBackpressure func()
}

type firstAudioWebSocketCloser struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	revoked bool
}

func (c *firstAudioWebSocketCloser) Attach(conn *websocket.Conn) bool {
	c.mu.Lock()
	if c.revoked {
		c.mu.Unlock()
		_ = conn.Close()
		return false
	}
	c.conn = conn
	c.mu.Unlock()
	return true
}

func (c *firstAudioWebSocketCloser) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.revoked = true
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (c *wsClient) IsClosed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

func (c *wsClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	return nil
}

func newWSClient(conn *websocket.Conn) *wsClient {
	c := &wsClient{conn: conn, send: make(chan Msg, 64), control: make(chan Msg, 1), done: make(chan struct{}), writeDone: make(chan struct{})}
	go c.writePump()
	return c
}

func (c *wsClient) writePump() {
	if c.writeDone != nil {
		defer close(c.writeDone)
	}
	write := func(m Msg) bool {
		_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.conn.WriteJSON(m); err != nil {
			_ = c.Close()
			return false
		}
		return true
	}
	for {
		select {
		case <-c.done:
			return
		default:
		}
		// Reserve heartbeat capacity, while allowing ordinary traffic to progress
		// even when the peer continuously sends heartbeat requests.
		select {
		case m := <-c.control:
			if !write(m) {
				return
			}
			select {
			case m = <-c.send:
				if !write(m) {
					return
				}
			default:
			}
			continue
		default:
		}
		select {
		case m := <-c.send:
			if !write(m) {
				return
			}
		case m := <-c.control:
			if !write(m) {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *wsClient) Send(m Msg) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errClosed
	}
	if m["type"] == "heartbeat_ack" {
		select {
		case c.control <- m:
		default:
		}
		// ACKs carry no request ID: one already queued ACK covers the latest
		// heartbeat too. Never coalesce business frames this way.
		return nil
	}
	select {
	case c.send <- m:
		return nil
	default:
		// Do not continue a stream after silently losing a business command.
		// This path must not re-enter Room or acquire c.mu through Close.
		c.closed = true
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if c.onBackpressure != nil {
			c.onBackpressure()
		}
		return errBackpressure
	}
}

// handleWS GET /ws/classroom?lessonId=&ticket=
func (a *App) handleWS(c *gin.Context) {
	var session firstAudioWebSocketSession
	var socketCloser *firstAudioWebSocketCloser
	transferred := false
	releaseSession := true
	if a.firstAudioRequestAdmission != nil {
		socketCloser = &firstAudioWebSocketCloser{}
		var err error
		session, err = a.firstAudioRequestAdmission.ReserveSession(socketCloser.Close)
		if err != nil || session == nil {
			if session != nil {
				session.Release()
			}
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": http.StatusServiceUnavailable, "msg": "classroom temporarily unavailable"})
			return
		}
		defer func() {
			if !transferred && releaseSession {
				session.Release()
			}
		}()
	}
	lessonID, err := strconv.ParseInt(c.Query("lessonId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "bad lessonId"})
		return
	}
	ticket := c.Query("ticket")
	tk, ok := a.CheckTicket(ticket)
	if !ok || tk.LessonID != lessonID {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "bad ticket"})
		return
	}
	lesson, err := a.Sched.Lesson(lessonID)
	if err != nil || lesson.StudentID != tk.StudentID {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "lesson unavailable"})
		return
	}
	if !a.activeAccount(c.Request.Context(), tk.StudentID, auth.RoleStudent, "") {
		c.JSON(http.StatusUnauthorized, fail(401, "account unavailable"))
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	if session != nil {
		if !socketCloser.Attach(conn) || session.Activate() != nil {
			_ = conn.Close()
			return
		}
	}
	conn.SetReadLimit(16 << 10)
	client := newWSClient(conn)
	defer func() {
		if transferred {
			return
		}
		_ = client.Close()
		if client.writeDone != nil {
			select {
			case <-client.writeDone:
			case <-time.After(10 * time.Second):
				releaseSession = false
				if a.Log != nil {
					a.Log.ErrorCtx(c.Request.Context(), "ws", "ws_setup_cleanup_timeout", map[string]any{"lesson_id": lessonID})
				}
			}
		}
	}()
	client.onBackpressure = func() {
		a.Log.ErrorCtx(obs.WithTrace(context.Background(), "lesson-"+strconv.FormatInt(lessonID, 10)), "ws", "ws_backpressure", map[string]any{"lesson_id": lessonID, "student_id": tk.StudentID, "capacity": cap(client.send)})
	}
	room, _, err := a.Rooms.Enter(lessonID, tk.StudentID)
	if err != nil {
		_ = client.Close()
		return
	}
	room.Join(client)
	a.Log.Info("ws", "client_connected", map[string]any{
		"lesson_id": lessonID, "student_id": tk.StudentID}) // §14 WS 连接

	// The upgraded connection outlives Gin's HTTP request context.
	ctx, cancel := context.WithCancel(obs.WithTrace(context.Background(), obs.TraceID(c.Request.Context())))
	go func() {
		select {
		case <-client.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	type queuedWSInput struct {
		message  Msg
		envelope ClientEnvelope
		ctx      context.Context
		release  func()
	}
	inputs := make(chan queuedWSInput, 16)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		defer func() {
			cancel()
			_ = client.Close()
			room.Leave(client)
			a.Log.InfoCtx(obs.WithTrace(ctx, "lesson-"+strconv.FormatInt(lessonID, 10)), "ws", "client_disconnected", map[string]any{"lesson_id": lessonID, "student_id": tk.StudentID})
		}()
		stopping := false
		for input := range inputs {
			if input.release != nil && (stopping || ctx.Err() != nil) {
				input.release()
				continue
			}
			stopAfterInput := false
			func() {
				if input.release != nil {
					defer input.release()
				}
				operationCtx := input.ctx
				if operationCtx == nil {
					operationCtx = ctx
				}
				if ctx.Err() != nil || operationCtx.Err() != nil ||
					(a.firstAudioRequestAdmission != nil && !a.firstAudioRequestAdmission.OperationContextActive(operationCtx)) ||
					!room.IsActive(client) || !a.activeAccount(operationCtx, tk.StudentID, auth.RoleStudent, "") {
					stopAfterInput = true
					return
				}
				m, envelope := input.message, input.envelope
				typ, _ := m["type"].(string)
				switch typ {
				case "student_event":
					ev, _ := m["event"].(string)
					text, _ := m["text"].(string)
					if !validClassroomEvent(ev) {
						return
					}
					frames := room.HandleClientEventContext(operationCtx, envelope, classroom.Event{Type: ev, Text: sanitizeText(text)}, client)
					if len(frames) > 0 && frames[0]["type"] == "event_rejected" {
						for _, frame := range frames {
							_ = client.Send(frame)
						}
					}
				case "voice_text":
					text, _ := m["text"].(string)
					if !validVoiceText(text) {
						_ = client.Send(Msg{"type": "event_rejected", "event_id": envelope.EventID, "submission_id": envelope.SubmissionID, "reason": "invalid_text"})
						return
					}
					if _, body, matched := room.voiceParser.MatchFeedback(text); matched {
						if _, err := normalizeFeedback(FeedbackSubmission{SubmissionID: envelope.SubmissionID, Text: body, Channel: "voice"}); err != nil {
							failure := Msg(feedbackError(err))
							failure["type"] = "feedback_reply"
							failure["submission_id"] = envelope.SubmissionID
							_ = client.Send(failure)
							return
						}
					}
					cmd, body, frames := room.HandleClientVoiceSubmissionContext(operationCtx, envelope, text, client)
					if len(frames) > 0 && frames[0]["type"] == "event_rejected" {
						for _, frame := range frames {
							_ = client.Send(frame)
						}
					}
					if cmd == "feedback" {
						rec, err := a.SubmitFeedback(operationCtx, tk.StudentID, FeedbackSubmission{SubmissionID: envelope.SubmissionID, Text: body, Channel: "voice"})
						if err != nil {
							failure := Msg(feedbackError(err))
							failure["type"] = "feedback_reply"
							failure["submission_id"] = envelope.SubmissionID
							_ = client.Send(failure)
						} else {
							_ = client.Send(Msg{"type": "feedback_reply", "submission_id": rec.SubmissionID, "status": rec.Status, "reply": rec.Reply, "feedback": rec})
						}
					}
				case "resume":
					room.resumeClient(operationCtx, client)
				}
			}()
			if stopAfterInput {
				stopping = true
				_ = client.Close()
			}
		}
	}()
	go func() {
		defer cancel()
		defer func() {
			cancel()
			close(inputs)
			_ = client.Close()
			joined := true
			cleanupTimer := time.NewTimer(10 * time.Second)
			defer cleanupTimer.Stop()
			for _, done := range []<-chan struct{}{workerDone, client.writeDone} {
				if done == nil {
					continue
				}
				select {
				case <-done:
				case <-cleanupTimer.C:
					joined = false
				}
				if !joined {
					break
				}
			}
			if !joined {
				a.Log.ErrorCtx(ctx, "ws", "ws_worker_cleanup_timeout", map[string]any{"lesson_id": lessonID})
			}
			if joined && session != nil {
				session.Release()
			}
		}()
		for {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil || !utf8.Valid(raw) {
				return
			}
			var m Msg
			if err := json.Unmarshal(raw, &m); err != nil {
				return
			}
			if m["type"] == "heartbeat" {
				_ = client.Send(Msg{"type": "heartbeat_ack", "ts": time.Now().Unix()})
				continue
			}
			operationCtx, release, acquireErr := ctx, func() {}, error(nil)
			if a.firstAudioRequestAdmission != nil {
				operationCtx, release, acquireErr = a.firstAudioRequestAdmission.AcquireOperation(ctx)
			}
			if acquireErr != nil || operationCtx == nil || release == nil ||
				(a.firstAudioRequestAdmission != nil && !a.firstAudioRequestAdmission.OperationContextActive(operationCtx)) {
				if release != nil {
					release()
				}
				_ = client.Close()
				return
			}
			if !room.IsActive(client) || !a.activeAccount(operationCtx, tk.StudentID, auth.RoleStudent, "") {
				release()
				return
			}
			var envelope ClientEnvelope
			submissionValue, submissionPresent := m["submission_id"]
			if err := json.Unmarshal(raw, &envelope); err != nil || (submissionPresent && submissionValue == nil) {
				rejected := Msg{"type": "event_rejected", "reason": "invalid_envelope"}
				if id, ok := m["event_id"].(string); ok && len(id) <= 128 {
					rejected["event_id"] = id
				}
				if id, ok := m["submission_id"].(string); ok && feedbackSubmissionID.MatchString(id) {
					rejected["submission_id"] = id
				}
				_ = client.Send(rejected)
				release()
				continue
			}
			select {
			case inputs <- queuedWSInput{message: m, envelope: envelope, ctx: operationCtx, release: release}:
			case <-ctx.Done():
				release()
				return
			default:
				release()
				a.Log.ErrorCtx(obs.WithTrace(ctx, "lesson-"+strconv.FormatInt(lessonID, 10)), "ws", "ws_input_backpressure", map[string]any{"lesson_id": lessonID, "capacity": cap(inputs), "reason": "input_queue_full"})
				return
			}
		}
	}()
	transferred = true
}

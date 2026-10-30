// WS 课堂频道（SDD §11）：ticket 鉴权 → Join（首帧快照）→
// 读泵（student_event / voice_text / heartbeat）→ 双工广播。
package api

import (
	"context"
	"encoding/json"
	"enwords/internal/auth"
	"enwords/internal/obs"
	"errors"
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
	conn           wsFrameConn
	send           chan Msg
	control        chan Msg
	done           chan struct{}
	writerDone     chan struct{}
	writePermit    func() (*generationEffect, error)
	mu             sync.Mutex
	closed         bool
	closeDone      chan struct{}
	closeErr       error
	onBackpressure func()
}

type wsFrameConn interface {
	SetWriteDeadline(time.Time) error
	WriteJSON(any) error
	Close() error
}

func (c *wsClient) IsClosed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

func (c *wsClient) Close() error {
	c.mu.Lock()
	if c.closed {
		done := c.closeDone
		c.mu.Unlock()
		if done != nil {
			<-done
		}
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		return err
	}
	if c.closeDone == nil {
		c.closeDone = make(chan struct{})
	}
	done := c.closeDone
	c.closed = true
	close(c.done)
	c.mu.Unlock()
	var err error
	if c.conn != nil {
		err = c.conn.Close()
	}
	c.mu.Lock()
	c.closeErr = err
	close(done)
	c.mu.Unlock()
	return err
}

func newWSClient(conn *websocket.Conn) *wsClient {
	return newWSClientWithWriter(conn)
}

func websocketLeaseContext(requestCtx context.Context) context.Context {
	return obs.WithTrace(context.Background(), obs.TraceID(requestCtx))
}

func newWSClientWithWriter(conn wsFrameConn) *wsClient {
	c := &wsClient{conn: conn, send: make(chan Msg, 64), control: make(chan Msg, 1), done: make(chan struct{}), writerDone: make(chan struct{}), closeDone: make(chan struct{})}
	go c.writePump()
	return c
}

func releaseWSLeaseAfterWorkers(lease *generationLease, client *wsClient, workerDone <-chan struct{}, removeCloser func()) {
	go func() {
		<-workerDone
		<-client.writerDone
		if removeCloser != nil {
			removeCloser()
		}
		lease.Release()
	}()
}

func (c *wsClient) writePump() {
	defer close(c.writerDone)
	write := func(m Msg) bool {
		var permit *generationEffect
		if c.writePermit != nil {
			var err error
			permit, err = c.writePermit()
			if err != nil {
				_ = c.Close()
				return false
			}
			defer permit.Release()
		}
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
		var closeErr error
		if c.conn != nil {
			closeErr = c.conn.Close()
		}
		c.closeErr = closeErr
		if c.closeDone == nil {
			c.closeDone = make(chan struct{})
		}
		close(c.closeDone)
		if c.onBackpressure != nil {
			c.onBackpressure()
		}
		return errors.Join(errBackpressure, closeErr)
	}
}

// handleWS GET /ws/classroom?lessonId=&ticket=
func (a *App) handleWS(c *gin.Context) {
	originalRequestCtx := c.Request.Context()
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
	// Validate the bearer ticket and its bound lesson before allocating a
	// generation map entry; arbitrary unauthenticated IDs must stay O(1).
	leaseCtx := websocketLeaseContext(originalRequestCtx)
	lease, err := a.generations.acquire(leaseCtx, lessonID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "classroom generation unavailable"})
		return
	}
	transferredLease := false
	defer func() {
		if !transferredLease {
			lease.Release()
		}
	}()
	requestCtx := withGenerationLease(lease.Context(), lease)
	c.Request = c.Request.WithContext(requestCtx)
	// Revalidate against the admitted generation: a reset may have revoked a
	// previously valid ticket between the preflight check and lease acquisition.
	currentTicket, stillValid := a.CheckTicket(ticket)
	currentLesson, lessonErr := a.Sched.Lesson(lessonID)
	if !stillValid || currentTicket.LessonID != lessonID || currentTicket.StudentID != tk.StudentID || lessonErr != nil || currentLesson.StudentID != tk.StudentID || !a.activeAccount(requestCtx, tk.StudentID, auth.RoleStudent, "") {
		lease.Release()
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "classroom authorization expired"})
		return
	}
	handshakeEffect, err := lease.Token().Permit()
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "classroom generation retired"})
		return
	}
	if originalRequestCtx.Err() != nil {
		handshakeEffect.Release()
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		handshakeEffect.Release()
		return
	}
	conn.SetReadLimit(16 << 10)
	client := newWSClient(conn)
	client.writePermit = lease.Token().Permit
	client.onBackpressure = func() {
		a.Log.ErrorCtx(obs.WithTrace(context.Background(), "lesson-"+strconv.FormatInt(lessonID, 10)), "ws", "ws_backpressure", map[string]any{"lesson_id": lessonID, "student_id": tk.StudentID, "capacity": cap(client.send)})
	}
	room, _, err := a.Rooms.EnterContext(requestCtx, lessonID, tk.StudentID)
	if err != nil {
		handshakeEffect.Release()
		_ = client.Close()
		<-client.writerDone
		return
	}
	room.JoinContext(requestCtx, client) // 首帧 state_snapshot（J4）
	handshakeEffect.Release()
	removeCloser, err := lease.AddCloser(func() { _ = client.Close() })
	if err != nil {
		_ = client.Close()
		room.Leave(client)
		<-client.writerDone
		return
	}
	a.Log.Info("ws", "client_connected", map[string]any{
		"lesson_id": lessonID, "student_id": tk.StudentID}) // §14 WS 连接

	// The upgraded connection outlives Gin's HTTP request context.
	ctx, cancel := context.WithCancel(obs.WithTrace(lease.Context(), obs.TraceID(c.Request.Context())))
	ctx = withGenerationLease(ctx, lease)
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
		for {
			var input queuedWSInput
			select {
			case <-ctx.Done():
				return
			case input = <-inputs:
			}
			if ctx.Err() != nil || !room.IsActive(client) || !a.activeAccount(ctx, tk.StudentID, auth.RoleStudent, "") {
				return
			}
			m, envelope := input.message, input.envelope
			typ, _ := m["type"].(string)
			switch typ {
			case "student_event":
				ev, _ := m["event"].(string)
				text, _ := m["text"].(string)
				if !validClassroomEvent(ev) {
					continue
				}
				frames := room.HandleClientEventContext(ctx, envelope, classroom.Event{Type: ev, Text: sanitizeText(text)}, client)
				if len(frames) > 0 && frames[0]["type"] == "event_rejected" {
					for _, f := range frames {
						_ = client.Send(f)
					}
				}
			case "voice_text": // Omni 中继 / 前端 Web Speech 上报的转写文本
				text, _ := m["text"].(string)
				if !validVoiceText(text) {
					_ = client.Send(Msg{"type": "event_rejected", "event_id": envelope.EventID, "submission_id": envelope.SubmissionID, "reason": "invalid_text"})
					continue
				}
				if _, body, matched := room.voiceParser.MatchFeedback(text); matched {
					if _, err := normalizeFeedback(FeedbackSubmission{SubmissionID: envelope.SubmissionID, Text: body, Channel: "voice"}); err != nil {
						failure := Msg(feedbackError(err))
						failure["type"] = "feedback_reply"
						failure["submission_id"] = envelope.SubmissionID
						_ = client.Send(failure)
						continue
					}
				}
				cmd, body, frames := room.HandleClientVoiceSubmissionContext(ctx, envelope, text, client)
				if len(frames) > 0 && frames[0]["type"] == "event_rejected" {
					for _, f := range frames {
						_ = client.Send(f)
					}
				}
				if cmd == "feedback" {
					rec, err := a.SubmitFeedback(ctx, tk.StudentID, FeedbackSubmission{SubmissionID: envelope.SubmissionID, Text: body, Channel: "voice"})
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
				room.resumeClient(ctx, client)
			}
		}
	}()
	go func() {
		defer cancel()
		defer func() {
			cancel()
			_ = client.Close()
			select {
			case <-workerDone:
			case <-time.After(10 * time.Second):
				a.Log.ErrorCtx(ctx, "ws", "ws_worker_cleanup_timeout", map[string]any{"lesson_id": lessonID})
				return
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
			if !room.IsActive(client) {
				return
			}
			if !a.activeAccount(ctx, tk.StudentID, auth.RoleStudent, "") {
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
				continue
			}
			if m["type"] == "heartbeat" {
				_ = client.Send(Msg{"type": "heartbeat_ack", "ts": time.Now().Unix()})
				continue
			}
			select {
			case inputs <- queuedWSInput{m, envelope}:
			case <-ctx.Done():
				return
			default:
				a.Log.ErrorCtx(obs.WithTrace(ctx, "lesson-"+strconv.FormatInt(lessonID, 10)), "ws", "ws_input_backpressure", map[string]any{"lesson_id": lessonID, "capacity": cap(inputs), "reason": "input_queue_full"})
				return
			}
		}
	}()
	transferredLease = true
	releaseWSLeaseAfterWorkers(lease, client, workerDone, removeCloser)
}

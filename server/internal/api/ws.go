// WS 课堂频道（SDD §11）：ticket 鉴权 → Join（首帧快照）→
// 读泵（student_event / voice_text / heartbeat）→ 双工广播。
package api

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"enwords/internal/duplex"
	"enwords/internal/store"

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

func duplexReason(err error) string {
	switch {
	case errors.Is(err, duplex.ErrStale):
		return "stale_stream"
	case errors.Is(err, duplex.ErrTextHash):
		return "text_hash_mismatch"
	case errors.Is(err, duplex.ErrTimeout):
		return "stream_expired"
	case errors.Is(err, duplex.ErrProtocol):
		return "invalid_audio_format"
	default:
		return "invalid_schema"
	}
}

const duplexReceiptRecoveryWindow = 30 * time.Second

func duplexReceiptNeedsRecovery(receipt store.DuplexReceipt, now time.Time) bool {
	if receipt.Status != "committing" || receipt.UpdatedAt.IsZero() {
		return false
	}
	return !receipt.UpdatedAt.After(now) && now.Sub(receipt.UpdatedAt) >= duplexReceiptRecoveryWindow
}

func pcm16WAV(pcm []byte, sampleRate int) []byte {
	data := make([]byte, 44+len(pcm))
	copy(data[0:], []byte("RIFF"))
	putLE32(data[4:], uint32(36+len(pcm)))
	copy(data[8:], []byte("WAVEfmt "))
	putLE32(data[16:], 16)
	putLE16(data[20:], 1)
	putLE16(data[22:], 1)
	putLE32(data[24:], uint32(sampleRate))
	putLE32(data[28:], uint32(sampleRate*2))
	putLE16(data[32:], 2)
	putLE16(data[34:], 16)
	copy(data[36:], []byte("data"))
	putLE32(data[40:], uint32(len(pcm)))
	copy(data[44:], pcm)
	return data
}

func putLE16(dst []byte, value uint16) { dst[0] = byte(value); dst[1] = byte(value >> 8) }
func putLE32(dst []byte, value uint32) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
	dst[3] = byte(value >> 24)
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
	var shadowSession *duplex.Session
	var shadowIdentity duplex.Identity
	var shadowPCM bytes.Buffer
	var shadowNextSeq uint64
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
				if len(text) > 2048 {
					_ = client.Send(Msg{"type": "event_rejected", "reason": "event_text_too_long", "event_id": envelope.EventID})
					continue
				}
				frames := room.HandleClientEventContext(ctx, envelope, classroom.Event{Type: ev, Text: text}, client)
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
			case "voice_stream_start":
				if !a.DuplexShadow || a.Local == nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "feature_disabled"})
					continue
				}
				if shadowSession != nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "already_active"})
					continue
				}
				room.mu.Lock()
				turn := room.contextLocked()
				inputRevision := room.turnVersion
				room.mu.Unlock()
				shadowIdentity = duplex.Identity{Session: newToken(), Epoch: turn.Epoch, InputRevision: inputRevision, Utterance: newToken()}
				var err error
				shadowSession, err = duplex.New(shadowIdentity)
				if err != nil || shadowSession.Start(time.Now()) != nil {
					shadowSession = nil
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "invalid_schema"})
					continue
				}
				shadowPCM.Reset()
				shadowNextSeq = 0
				_ = client.Send(Msg{"type": "voice_stream_start_ack", "event_id": envelope.EventID, "session_id": shadowIdentity.Session, "epoch": shadowIdentity.Epoch, "input_revision": shadowIdentity.InputRevision, "utterance_id": shadowIdentity.Utterance})
			case "voice_stream_chunk":
				if shadowSession == nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "stale_stream"})
					continue
				}
				seq, seqOK := m["chunk_seq"].(float64)
				encoded, audioOK := m["audio"].(string)
				pcm, err := base64.StdEncoding.DecodeString(encoded)
				if !seqOK || !audioOK || uint64(seq) != shadowNextSeq {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "chunk_out_of_order"})
					shadowSession = nil
					continue
				}
				if err = shadowSession.Chunk(shadowIdentity, uint64(seq), pcm, time.Now()); err != nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": duplexReason(err)})
					shadowSession = nil
					continue
				}
				_, _ = shadowPCM.Write(pcm)
				shadowNextSeq++
				_ = client.Send(Msg{"type": "voice_stream_ack", "event_id": envelope.EventID, "chunk_seq": uint64(seq)})
			case "voice_stream_cancel":
				if shadowSession != nil {
					shadowSession.Cancel()
					shadowSession = nil
					shadowPCM.Reset()
				}
				_ = client.Send(Msg{"type": "voice_stream_canceled", "event_id": envelope.EventID})
			case "voice_stream_end":
				if shadowSession == nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "stale_stream"})
					continue
				}
				if err := shadowSession.End(time.Now()); err != nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": duplexReason(err)})
					shadowSession = nil
					continue
				}
				text, err := a.Local.Transcribe(ctx, pcm16WAV(shadowPCM.Bytes(), 16000), "recording.wav", "zh")
				if err != nil || !validVoiceText(text) {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "asr_failed"})
					shadowSession = nil
					continue
				}
				finalRevision := shadowIdentity.InputRevision + 1
				if err := shadowSession.Preview(shadowIdentity, finalRevision, text); err != nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "asr_failed"})
					shadowSession = nil
					continue
				}
				_ = client.Send(Msg{"type": "voice_final", "event_id": envelope.EventID, "session_id": shadowIdentity.Session, "epoch": shadowIdentity.Epoch, "input_revision": finalRevision, "utterance_id": shadowIdentity.Utterance, "text": text})
				_ = client.Send(Msg{"type": "voice_confirmation_required", "event_id": envelope.EventID, "session_id": shadowIdentity.Session, "epoch": shadowIdentity.Epoch, "input_revision": finalRevision, "utterance_id": shadowIdentity.Utterance, "text": text, "expires_ms": 8000})
			case "voice_confirm":
				if shadowSession == nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "stale_stream"})
					continue
				}
				final, _ := m["text"].(string)
				revision, _ := m["input_revision"].(float64)
				textHash, _ := m["text_hash"].(string)
				if err := shadowSession.ConfirmWithHash(shadowIdentity, uint64(revision), final, textHash, time.Now()); err != nil {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": duplexReason(err)})
					continue
				}
				var receipt store.DuplexReceipt
				if a.DuplexStore != nil {
					var err error
					receipt, err = a.DuplexStore.PrepareReceipt(ctx, store.DuplexReceipt{
						DecisionID: shadowIdentity.Utterance,
						LessonID:   lessonID, StudentID: tk.StudentID,
						Epoch: shadowIdentity.Epoch, InputRevision: uint64(revision),
						UtteranceID: shadowIdentity.Utterance, TextHash: textHash, Status: "committing",
					})
					if err != nil {
						_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "receipt_conflict"})
						continue
					}
					if receipt.Status == "committed" {
						_ = client.Send(Msg{"type": "voice_confirmed", "event_id": envelope.EventID, "utterance_id": shadowIdentity.Utterance, "command": "replayed", "commit_receipt": receipt.DecisionID, "replayed": true})
						shadowSession = nil
						continue
					}
					if duplexReceiptNeedsRecovery(receipt, time.Now().UTC()) {
						_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "commit_pending_recovery", "utterance_id": shadowIdentity.Utterance})
						continue
					}
				}
				cmd, _, frames := room.HandleClientVoiceSubmissionContext(ctx, envelope, final, client)
				for _, frame := range frames {
					_ = client.Send(frame)
				}
				if a.DuplexStore != nil && !(len(frames) > 0 && frames[0]["type"] == "event_rejected") {
					if _, err := a.DuplexStore.FinalizeReceipt(ctx, tk.StudentID, lessonID, receipt.DecisionID); err != nil {
						_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "commit_pending_recovery", "utterance_id": shadowIdentity.Utterance})
						continue
					}
				}
				confirmed := Msg{"type": "voice_confirmed", "event_id": envelope.EventID, "utterance_id": shadowIdentity.Utterance, "command": cmd}
				if a.DuplexStore != nil {
					confirmed["commit_receipt"] = receipt.DecisionID
				}
				_ = client.Send(confirmed)
				shadowSession = nil
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

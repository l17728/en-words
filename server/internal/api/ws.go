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
	"os"
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

func (a *App) deliverDuplexFeedback(ctx context.Context, student, lesson int64, client Client, eventID string) {
	if a.DuplexStore == nil {
		return
	}
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	rows, err := a.DuplexStore.PendingFeedbackDeliveries(deliveryCtx, student, lesson)
	if err != nil {
		a.Log.ErrorCtx(ctx, "duplex", "feedback_outbox_read_failed", map[string]any{"lesson_id": lesson, "error": err.Error()})
		return
	}
	for _, row := range rows {
		rec, submitErr := a.SubmitFeedback(deliveryCtx, student, FeedbackSubmission{SubmissionID: row.SubmissionID, Text: row.Text, Channel: "voice"})
		if submitErr != nil {
			var permanent *FeedbackSubmissionError
			if errors.As(submitErr, &permanent) && !permanent.Retryable {
				if err := a.DuplexStore.FailFeedbackDelivery(deliveryCtx, student, row.SubmissionID); err != nil {
					a.Log.ErrorCtx(deliveryCtx, "duplex", "feedback_outbox_quarantine_failed", map[string]any{"submission_id": row.SubmissionID, "error": err.Error()})
				}
			}
			f := Msg(feedbackError(submitErr))
			f["type"] = "feedback_reply"
			f["submission_id"] = row.SubmissionID
			_ = client.Send(f)
			continue
		}
		if err := client.Send(Msg{"type": "feedback_reply", "event_id": eventID, "submission_id": rec.SubmissionID, "status": rec.Status, "reply": rec.Reply, "feedback": rec}); err != nil {
			a.Log.WarnCtx(deliveryCtx, "duplex", "feedback_outbox_send_pending", map[string]any{"lesson_id": lesson, "submission_id": row.SubmissionID})
			continue
		}
		if err := a.DuplexStore.CompleteFeedbackDelivery(deliveryCtx, student, row.SubmissionID); err != nil {
			a.Log.ErrorCtx(ctx, "duplex", "feedback_outbox_ack_failed", map[string]any{"lesson_id": lesson, "submission_id": row.SubmissionID, "error": err.Error()})
			continue
		}
	}
}

const duplexReceiptRecoveryWindow = 30 * time.Second

func duplexReceiptNeedsRecovery(receipt store.DuplexReceipt, now time.Time) bool {
	if receipt.Status != "committing" || receipt.UpdatedAt.IsZero() {
		return false
	}
	return !receipt.UpdatedAt.After(now) && now.Sub(receipt.UpdatedAt) >= duplexReceiptRecoveryWindow
}

// nextDuplexInputRevision makes each recognition attempt unique within the
// classroom epoch. Classroom turns may remain unchanged after an unrecognized
// or rejected answer, but a new audio lease still needs a fresh durable receipt.
func nextDuplexInputRevision(turnRevision, previousRevision uint64) (uint64, bool) {
	base := turnRevision
	if previousRevision > base {
		base = previousRevision
	}
	if base == ^uint64(0) {
		return 0, false
	}
	return base + 1, true
}

func (r *Room) nextDuplexInputRevisionLocked() (uint64, bool) {
	revision, ok := nextDuplexInputRevision(r.turnVersion, r.duplexInputRevision)
	if ok {
		r.duplexInputRevision = revision
	}
	return revision, ok
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
	if a.DuplexStore != nil {
		go a.deliverDuplexFeedback(requestCtx, tk.StudentID, lessonID, client, "")
	}
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
				inputRevision, revisionOK := room.nextDuplexInputRevisionLocked()
				room.mu.Unlock()
				if !revisionOK {
					_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "revision_exhausted"})
					continue
				}
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
				var feedbackAfterCommit *FeedbackSubmission
				var feedbackEventID string
				commitAccepted := false
				func() {
					room.mutationMu.Lock()
					defer room.mutationMu.Unlock()
					if shadowSession == nil {
						_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "stale_stream"})
						return
					}
					final, _ := m["text"].(string)
					revision, _ := m["input_revision"].(float64)
					textHash, _ := m["text_hash"].(string)
					if err := shadowSession.ConfirmWithHash(shadowIdentity, uint64(revision), final, textHash, time.Now()); err != nil {
						_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": duplexReason(err)})
						return
					}
					if _, body, matched := room.voiceParser.MatchFeedback(final); matched {
						id := "duplex_" + shadowIdentity.Utterance
						if !feedbackSubmissionID.MatchString(id) {
							_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "invalid_feedback_submission_id"})
							return
						}
						envelope.SubmissionID = id
						normalized, validationErr := normalizeFeedback(FeedbackSubmission{SubmissionID: id, Text: body, Channel: "voice"})
						if validationErr != nil {
							failure := Msg(feedbackError(validationErr))
							failure["type"] = "feedback_reply"
							failure["submission_id"] = id
							_ = client.Send(failure)
							return
						}
						feedbackAfterCommit = &normalized
						feedbackEventID = envelope.EventID
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
							a.Log.ErrorCtx(ctx, "duplex", "receipt_prepare_failed", map[string]any{
								"lesson_id": lessonID, "student_id": tk.StudentID,
								"epoch": shadowIdentity.Epoch, "input_revision": revision,
								"utterance_id": shadowIdentity.Utterance, "error": err.Error(),
							})
							_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "receipt_conflict"})
							return
						}
						if receipt.Status == "committed" {
							_ = client.Send(Msg{"type": "voice_confirmed", "event_id": envelope.EventID, "utterance_id": shadowIdentity.Utterance, "command": "replayed", "commit_receipt": receipt.DecisionID, "replayed": true})
							shadowSession = nil
							commitAccepted = true
							return
						}
						if duplexReceiptNeedsRecovery(receipt, time.Now().UTC()) {
							_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "commit_pending_recovery", "utterance_id": shadowIdentity.Utterance})
							return
						}
					}
					var beforeState classroom.State
					var beforeRoom roomMutationSnapshot
					if a.DuplexStore != nil {
						beforeRoom = room.captureMutationSnapshot()
						beforeState = beforeRoom.state
						room.beginDeferredEffects()
					}
					cmd, body, frames := room.handleClientVoiceSubmissionContext(ctx, envelope, final, client)
					if cmd == "feedback" && feedbackAfterCommit != nil {
						feedbackAfterCommit.Text = body
					}
					rejected := len(frames) > 0 && frames[0]["type"] == "event_rejected"
					if rejected {
						if a.DuplexStore != nil {
							room.discardDeferredEffects()
							if err := a.DuplexStore.RejectReceipt(ctx, tk.StudentID, lessonID, receipt.DecisionID); err != nil {
								a.Log.ErrorCtx(ctx, "duplex", "receipt_reject_failed", map[string]any{
									"lesson_id": lessonID, "student_id": tk.StudentID,
									"decision_id": receipt.DecisionID, "error": err.Error(),
								})
							}
						}
						for _, frame := range frames {
							_ = client.Send(frame)
						}
						shadowSession = nil
						return
					}
					effectsStatus := "completed"
					if a.DuplexStore != nil && !rejected {
						var delivery *store.DuplexFeedbackDelivery
						if feedbackAfterCommit != nil {
							delivery = &store.DuplexFeedbackDelivery{SubmissionID: feedbackAfterCommit.SubmissionID, StudentID: tk.StudentID, LessonID: lessonID, Text: feedbackAfterCommit.Text}
						}
						if err := a.DuplexStore.CommitSnapshot(ctx, tk.StudentID, lessonID, receipt.DecisionID, beforeState, room.durableSnapshot(), delivery); err != nil {
							stored, re := a.DuplexStore.LookupReceipt(ctx, tk.StudentID, lessonID, receipt.DecisionID)
							var runtimeSnapshot store.RuntimeRecord
							se := a.DB.WithContext(ctx).First(&runtimeSnapshot, "key = ?", "snapshot/"+strconv.FormatInt(lessonID, 10)).Error
							want, _ := json.Marshal(room.durableSnapshot())
							if re == nil && se == nil && stored.Status == "committed" && runtimeSnapshot.Value == string(want) {
								a.Log.WarnCtx(ctx, "duplex", "commit_outcome_recovered", map[string]any{"lesson_id": lessonID, "decision_id": receipt.DecisionID})
							} else {
								if re == nil && stored.Status != "committed" {
									_ = room.restoreMutationSnapshot(beforeRoom)
								} else {
									room.mu.Lock()
									room.retired = true
									room.mu.Unlock()
								}
								_ = client.Send(Msg{"type": "voice_stream_error", "event_id": envelope.EventID, "reason": "commit_pending_recovery", "utterance_id": shadowIdentity.Utterance})
								return
							}
						}
						// flushDeferredEffects broadcasts through Room.emitLocked. Its
						// returned frames are for REST/tests and must not be sent again
						// by this WebSocket handler.
						flushed := room.flushDeferredEffects(ctx)
						for _, frame := range flushed {
							if frame["type"] == "classroom_error" {
								effectsStatus = "pending"
								break
							}
						}
					}
					if a.DuplexStore != nil {
						// Durable mode suppresses Room.emitLocked until the commit, so
						// these acknowledgement frames must be sent here. In memory-only
						// mode ingress already broadcasts them through the Room.
						for _, frame := range frames {
							_ = client.Send(frame)
						}
					}
					confirmed := Msg{"type": "voice_confirmed", "event_id": envelope.EventID, "utterance_id": shadowIdentity.Utterance, "command": cmd}
					confirmed["effects_status"] = effectsStatus
					if a.DuplexStore != nil {
						confirmed["commit_receipt"] = receipt.DecisionID
					}
					if feedbackAfterCommit != nil {
						confirmed["submission_id"] = feedbackAfterCommit.SubmissionID
					}
					_ = client.Send(confirmed)
					shadowSession = nil
					commitAccepted = true
				}()
				if commitAccepted && feedbackAfterCommit != nil && !(a.TestMode && os.Getenv("ENWORDS_E2E_DEFER_FEEDBACK_DISPATCH") == "1") {
					go a.deliverDuplexFeedback(ctx, tk.StudentID, lessonID, client, feedbackEventID)
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

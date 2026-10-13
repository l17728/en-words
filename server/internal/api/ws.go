// WS 课堂频道（SDD §11）：ticket 鉴权 → Join（首帧快照）→
// 读泵（student_event / voice_text / heartbeat）→ 双工广播。
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

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
	conn    *websocket.Conn
	send    chan Msg
	control chan Msg
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
}

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
	c := &wsClient{conn: conn, send: make(chan Msg, 64), control: make(chan Msg, 1), done: make(chan struct{})}
	go c.writePump()
	return c
}

func (c *wsClient) writePump() {
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
		return errBackpressure // 队列满丢弃非关键帧（快照可经 REST 兜底重取）
	}
}

// handleWS GET /ws/classroom?lessonId=&ticket=
func (a *App) handleWS(c *gin.Context) {
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
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := newWSClient(conn)
	room, _, err := a.Rooms.Enter(lessonID, tk.StudentID)
	if err != nil {
		_ = client.Close()
		return
	}
	room.Join(client) // 首帧 state_snapshot（J4）
	a.Log.Info("ws", "client_connected", map[string]any{
		"lesson_id": lessonID, "student_id": tk.StudentID}) // §14 WS 连接

	go func() {
		defer func() {
			room.Leave(client)
			_ = client.Close()
			a.Log.Info("ws", "client_disconnected", map[string]any{ // §14 WS 断开
				"lesson_id": lessonID, "student_id": tk.StudentID})
		}()
		for {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			var m Msg
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			if !room.IsActive(client) {
				return
			}
			var envelope ClientEnvelope
			raw, _ := json.Marshal(m)
			_ = json.Unmarshal(raw, &envelope)
			typ, _ := m["type"].(string)
			switch typ {
			case "heartbeat":
				_ = client.Send(Msg{"type": "heartbeat_ack", "ts": time.Now().Unix()})
			case "student_event":
				ev, _ := m["event"].(string)
				text, _ := m["text"].(string)
				if !validClassroomEvent(ev) {
					continue
				}
				frames := room.HandleClientEvent(envelope, classroom.Event{Type: ev, Text: sanitizeText(text)}, client)
				if len(frames) > 0 && frames[0]["type"] == "event_rejected" {
					for _, f := range frames {
						_ = client.Send(f)
					}
				}
			case "voice_text": // Omni 中继 / 前端 Web Speech 上报的转写文本
				text, _ := m["text"].(string)
				cmd, frames := room.HandleClientVoice(envelope, sanitizeText(text), client)
				if len(frames) > 0 && frames[0]["type"] == "event_rejected" {
					for _, f := range frames {
						_ = client.Send(f)
					}
				}
				if cmd == "feedback" { // D-14 语音反馈闭环（G-2 同管线）
					select {
					case fb := <-room.FeedbackCh:
						rec := a.HandleFeedbackContext(c.Request.Context(), tk.StudentID, fb)
						_ = client.Send(Msg{"type": "feedback_reply",
							"status": rec.Status, "reply": rec.Reply})
					default:
					}
				}
			case "resume":
				room.Resume()
			}
		}
	}()
}

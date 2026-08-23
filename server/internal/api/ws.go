// WS 课堂频道（SDD §11）：ticket 鉴权 → Join（首帧快照）→
// 读泵（student_event / voice_text / heartbeat）→ 双工广播。
package api

import (
	"errors"
	"net/http"
	"strconv"
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
	conn *websocket.Conn
	send chan Msg
	done chan struct{}
}

func newWSClient(conn *websocket.Conn) *wsClient {
	c := &wsClient{conn: conn, send: make(chan Msg, 64), done: make(chan struct{})}
	go c.writePump()
	return c
}

func (c *wsClient) writePump() {
	for {
		select {
		case m := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteJSON(m); err != nil {
				close(c.done)
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *wsClient) Send(m Msg) error {
	select {
	case c.send <- m:
		return nil
	case <-c.done:
		return errClosed
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
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := newWSClient(conn)
	room, _, err := a.Rooms.Enter(lessonID, tk.StudentID)
	if err != nil {
		_ = conn.Close()
		return
	}
	room.Join(client) // 首帧 state_snapshot（J4）

	go func() {
		defer func() {
			room.Leave(client)
			_ = conn.Close()
		}()
		for {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			var m Msg
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "heartbeat":
				_ = client.Send(Msg{"type": "heartbeat_ack", "ts": time.Now().Unix()})
			case "student_event":
				ev, _ := m["event"].(string)
				text, _ := m["text"].(string)
				room.HandleEvent(classroom.Event{Type: ev, Text: text})
			case "voice_text": // Omni 中继 / 前端 Web Speech 上报的转写文本
				text, _ := m["text"].(string)
				cmd, _ := room.HandleVoiceText(text)
				if cmd == "feedback" { // D-14 语音反馈闭环（G-2 同管线）
					select {
					case fb := <-room.FeedbackCh:
						rec := a.HandleFeedback(tk.StudentID, fb)
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

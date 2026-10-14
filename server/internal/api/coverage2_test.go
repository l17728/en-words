package api

// 覆盖率最后冲刺：wsClient 背压/关闭、房间 SessionTimeout/Tick/FireTimeout、
// helpers 错误路径、hub Drop/Room 查询、feedback POST 校验。
import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/classroom"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRoom(t *testing.T) (*App, *fakeClock, int64, *Room) {
	t.Helper()
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "fin库", "g7", []string{"w1", "w2", "w3", "w4", "w5"})
	_, _ = app.Auth.CreateUser("sfin", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "sfin", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	clk.Advance(25 * time.Hour)
	room, _, err := app.Rooms.Enter(lessons[0].ID, uid)
	require.NoError(t, err)
	return app, clk, lessons[0].ID, room
}

// ---- 房间超时家族：Tick/FireTimeout/SessionTimeout ----

func TestCov_RoomTimeouts(t *testing.T) {
	app, clk, lid, room := setupRoom(t)
	// Timers begin only after a client has received the initial classroom frames.
	room.Join(&fakeClient{})
	_ = app
	// Tick 未到时 → no-op
	room.Tick()
	assert.Equal(t, "show_word", room.Machine().Phase())
	_ = lid

	// 时钟推进 9s > 8s(show_word 超时) → Tick 触发自动朗读
	clk.Advance(9 * time.Second)
	room.Tick()
	assert.Equal(t, "tts_seg", room.Machine().Phase(), "展示超时自动朗读(SM-12)")

	// FireTimeout 强制当前相位超时（跳段）
	frames := room.FireTimeout()
	assert.NotEmpty(t, frames)

	// SessionTimeout：61min 驻留 → 暂停 + 帧（先排空旧帧再触发，快照帧含 paused=true 即证）
	room.Machine().DrainActions()
	clk.Advance(61 * time.Minute)
	room.SessionTimeout()
	require.True(t, room.Machine().Paused())
	// 暂停后 Tick 不再触发
	clk.Advance(10 * time.Second)
	room.Tick()
	assert.True(t, room.Machine().Paused())
	// Resume 恢复（广播含重呈现帧）
	frames = room.Resume()
	assert.False(t, room.Machine().Paused())
	assert.NotEmpty(t, frames)
}

// ---- wsClient 背压与关闭 ----

func TestCov_WSClientBackpressure(t *testing.T) {
	app, _, lid, _ := setupRoom(t)
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	resp := req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/enter",
		mustStuToken(t, app), map[string]any{})
	em, _ := resp.Data.(map[string]any)
	ticket, _ := em["ticket"].(string)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/classroom?lessonId=" + itoa64(lid) + "&ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	defer conn.Close()

	// 找到挂在这房间的 wsClient（通过广播验证）——发送大量事件制造帧
	// Deliberate stale-context flood; use server-issued context and unique IDs.
	snapshot, _ := em["snapshot"].(map[string]any)
	turnContext := snapshot["context"]
	for i := 0; i < 200; i++ {
		_ = conn.WriteJSON(Msg{"type": "student_event", "event": "word_clicked", "context": turnContext, "event_id": "flood-click-" + itoa(i)})
		_ = conn.WriteJSON(Msg{"type": "student_event", "event": "tts_finished", "context": turnContext, "event_id": "flood-tts-" + itoa(i)})
	}
	// OS socket buffering makes actual saturation nondeterministic. A saturated
	// queue must close; if writes drain fast enough heartbeat remains usable.
	_ = conn.WriteJSON(Msg{"type": "heartbeat"})
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m Msg
	gotAck := false
	closed := false
	for !gotAck { // Read deadline bounds the full backlog, not an arbitrary frame count.
		if err := conn.ReadJSON(&m); err != nil {
			t.Logf("flood read termination: %T %v", err, err)
			_, closed = err.(*websocket.CloseError)
			closed = closed || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.Errno(10054))
			break
		}
		if m["type"] == "heartbeat_ack" {
			gotAck = true
		}
	}
	require.True(t, gotAck || closed, "flood must retain heartbeat or explicitly close, not hang")
	_ = conn.Close()
	// Either outcome must be recoverable using a new ticket for the same lesson.
	reentered := req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/enter", mustStuToken(t, app), map[string]any{})
	require.Equal(t, 0, reentered.Code)
	data := reentered.Data.(map[string]any)
	newConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/classroom?lessonId="+itoa64(lid)+"&ticket="+data["ticket"].(string), nil)
	require.NoError(t, err)
	defer newConn.Close()
	require.NoError(t, newConn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var fresh Msg
	require.NoError(t, newConn.ReadJSON(&fresh))
	require.Equal(t, "state_snapshot", fresh["type"])
	ctx := fresh["context"]
	frames := sendFixtureWS(t, newConn, &ctx, Msg{"type": "student_event", "event": "word_clicked"}, "after-flood-reconnect")
	require.False(t, hasFrame(frames, "event_rejected"))
	require.NotEmpty(t, frames)
}

func mustStuToken(t *testing.T, app *App) string {
	t.Helper()
	tok, _ := loginGet(t, app, "sfin", "pw")
	return tok
}

// ---- helpers 错误路径 + ticket 复用 ----

func TestCov_HelpersAndDrop(t *testing.T) {
	app, _, lid, _ := setupRoom(t)
	// hub.Room 命中/未命中
	_, ok := app.Rooms.Room(lid)
	assert.True(t, ok)
	_, ok = app.Rooms.Room(99999)
	assert.False(t, ok)
	// Drop
	app.Rooms.Drop(lid)
	_, ok = app.Rooms.Room(lid)
	assert.False(t, ok)

	// mustSchema panic 路径
	assert.Panics(t, func() { mustSchema("{bad") })

	// 错误登录体（非法 JSON → 空 username → bad credentials）
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader([]byte("not-json")))
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	assert.Equal(t, errUnauthorized, resp.Code)

	// 错误 token
	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer garbage.token.here")
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	assert.Equal(t, errUnauthorized, doJSON(t, w).Code)
}

// ---- Agent 工具：未知工具 + schema 失败 + 学生调用 agent 通道 ----

func TestCov_AgentToolErrors(t *testing.T) {
	app, _ := newTestApp(t)
	stokAgent, _ := app.Auth.SignService("system-agent")
	// 未知工具
	resp := req2(t, app.Router(), "POST", "/api/v1/agent/tools/call", stokAgent,
		map[string]any{"name": "no.such", "args": map[string]any{}})
	assert.Equal(t, errForbidden, resp.Code)
	// schema 缺字段
	args, _ := json.Marshal(map[string]any{"scope": "student_profile"})
	resp = req2(t, app.Router(), "POST", "/api/v1/agent/tools/call", stokAgent,
		map[string]any{"name": "memory.append", "args": json.RawMessage(args)})
	assert.Equal(t, errForbidden, resp.Code, "缺必填字段被拒")
	// admin 调 memory.append（scope 不含 admin）
	atok, _ := loginGet(t, app, "admin", "adminpw")
	resp = req2(t, app.Router(), "POST", "/api/v1/agent/tools/call", atok,
		map[string]any{"name": "memory.append", "args": map[string]any{}})
	assert.Equal(t, errForbidden, resp.Code)
	// config.set 空白 reason（高风险双保险）
	cargs, _ := json.Marshal(map[string]any{"key": "wordsPerLesson", "value": 80, "reason": "  "})
	resp = req2(t, app.Router(), "POST", "/api/v1/agent/tools/call", stokAgent,
		map[string]any{"name": "config.set", "args": json.RawMessage(cargs)})
	assert.Equal(t, errForbidden, resp.Code, "空白 reason 拒绝")
	// config.set 合法
	cargs2, _ := json.Marshal(map[string]any{"key": "wordsPerLesson", "value": 70, "reason": "cov"})
	resp = req2(t, app.Router(), "POST", "/api/v1/agent/tools/call", stokAgent,
		map[string]any{"name": "config.set", "args": json.RawMessage(cargs2)})
	assert.Equal(t, errForbidden, resp.Code, "student-scoped service cannot write global configuration")
	resp = req2(t, app.Router(), "POST", "/api/v1/config/set", atok,
		map[string]any{"scope": "global", "key": "wordsPerLesson", "value": 70, "reason": "cov"})
	assert.Equal(t, 0, resp.Code, resp.Msg)
}

// ---- enter 时房间已 finished（重进下课课）----

func TestCov_EnterFinishedLesson(t *testing.T) {
	app, clk, lid, _ := setupRoom(t)
	room, _ := app.Rooms.Room(lid)
	// 快进到下课
	// 快进到下课：按相位逐事件驱动（tts/repeating 都要喂）
	m := room.Machine()
	guard := 0
	for m.Phase() != "lesson_end" && guard < 2000 {
		guard++
		switch m.Phase() {
		case "show_word":
			room.HandleEvent(classroom.Event{Type: "word_clicked"})
		case "repeating":
			room.HandleEvent(classroom.Event{Type: "repeat_done"})
		case "wait_answer", "judging", "reteach_show":
			// 教学流不会进入（不答错则小测/期末需作答）
			room.HandleEvent(classroom.Event{Type: "answer_submitted", Text: "测试"})
		default:
			room.HandleEvent(classroom.Event{Type: "tts_finished"})
		}
	}
	require.Less(t, guard, 2000, "快进应收敛")
	clk.Advance(time.Minute)
	// 再次 Enter（finished 房间复用，状态保持 finished）
	_, snap, err := app.Rooms.Enter(lid, room.studentID)
	require.NoError(t, err)
	assert.Equal(t, "finished", room.Status())
	assert.Equal(t, "lesson_end", snap["phase"])
}

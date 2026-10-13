package api

// 覆盖率门补充测试（G1）：REST 端点扫全 + 真 WS 连接往返 + ticket/房间生命周期。
import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"enwords/internal/auth"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func req2(t *testing.T, h http.Handler, method, path, token string, body any) Resp {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return doJSON(t, w)
}

// ---- /me + 词库可见性 + 词条列表 + 摸底词表 ----

func TestCov_MeWordsPlacementGet(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "cov库", "g7", []string{"apple", "bee"})

	_, _ = app.Auth.CreateUser("cv1", "pw", "小美", auth.RoleStudent)
	stok, uid := loginGet(t, app, "cv1", "pw")

	// /me
	resp := req2(t, app.Router(), "GET", "/api/v1/me", stok, nil)
	require.Equal(t, 0, resp.Code)
	m, _ := resp.Data.(map[string]any)
	assert.Equal(t, "cv1", m["username"])
	assert.Equal(t, "student", m["role"])

	// 未登录 401
	resp = req2(t, app.Router(), "GET", "/api/v1/me", "", nil)
	assert.Equal(t, errUnauthorized, resp.Code)
	// 学生访问管理面 403
	resp = req2(t, app.Router(), "POST", "/api/v1/wordbooks", stok,
		map[string]string{"name": "x", "grade": "g7"})
	assert.Equal(t, errForbidden, resp.Code)

	// 词条列表（分页）
	resp = req2(t, app.Router(), "GET", "/api/v1/wordbooks/"+itoa64(bid)+"/words?page=1&size=10", stok, nil)
	require.Equal(t, 0, resp.Code)
	wm, _ := resp.Data.(map[string]any)
	assert.Equal(t, float64(2), wm["total"])
	// 坏库
	resp = req2(t, app.Router(), "GET", "/api/v1/wordbooks/999/words", stok, nil)
	assert.Equal(t, errBadRequest, resp.Code)

	// 摸底词表（仅英文 C2 + 草稿恢复字段 R17）
	resp = req2(t, app.Router(), "GET", "/api/v1/student/books/"+itoa64(bid)+"/placement", stok, nil)
	require.Equal(t, 0, resp.Code)
	pm, _ := resp.Data.(map[string]any)
	list, _ := pm["words"].([]any)
	require.Len(t, list, 2)
	w0, _ := list[0].(map[string]any)
	assert.Equal(t, "apple", w0["word"])
	draft, _ := pm["knownWordIds"].([]any)
	assert.Empty(t, draft, "未摸底无草稿")

	_ = uid
}

// ---- 未发布词库不可选/摸底坏库报错 ----

func TestCov_EnableDraftBookRejected(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	// 建库但不发布
	resp := req2(t, app.Router(), "POST", "/api/v1/wordbooks", atok,
		map[string]string{"name": "draft", "grade": "g8"})
	require.Equal(t, 0, resp.Code)
	m, _ := resp.Data.(map[string]any)
	bid := int64(m["ID"].(float64))

	_, _ = app.Auth.CreateUser("cv2", "pw", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "cv2", "pw")
	resp = req2(t, app.Router(), "POST", "/api/v1/student/books/"+itoa64(bid)+"/enable", stok, nil)
	assert.Equal(t, errBadRequest, resp.Code, "草稿库不可选")

	// 摸底坏库
	resp = req2(t, app.Router(), "POST", "/api/v1/student/books/999/placement", stok,
		map[string]any{"knownWordIds": []int64{}, "finish": true})
	assert.Equal(t, errBadRequest, resp.Code)
	_ = uid
}

// ---- 配置管理端点（admin）----

func TestCov_ConfigEndpoints(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	// 读
	resp := req2(t, app.Router(), "GET", "/api/v1/config", atok, nil)
	require.Equal(t, 0, resp.Code)
	cm, _ := resp.Data.(map[string]any)
	assert.Equal(t, float64(60), cm["wordsPerLesson"])
	// 合法写
	resp = req2(t, app.Router(), "POST", "/api/v1/config/set", atok,
		map[string]any{"scope": "global", "key": "wordsPerLesson", "value": 80, "reason": "cov"})
	require.Equal(t, 0, resp.Code, resp.Msg)
	assert.Equal(t, 80, app.Cfg.Int(0, "wordsPerLesson"))
	// 越界写
	resp = req2(t, app.Router(), "POST", "/api/v1/config/set", atok,
		map[string]any{"scope": "global", "key": "wordsPerLesson", "value": 120, "reason": "x"})
	assert.Equal(t, errBadRequest, resp.Code)
	// 学生层写
	_, _ = app.Auth.CreateUser("cv3", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "cv3", "pw")
	resp = req2(t, app.Router(), "POST", "/api/v1/config/set", atok,
		map[string]any{"scope": "student", "studentId": uid, "key": "wordsPerLesson",
			"value": 50, "reason": "cov"})
	require.Equal(t, 0, resp.Code)
	assert.Equal(t, 50, app.Cfg.Int(uid, "wordsPerLesson"))
	// 学生无权
	stok, _ := loginGet(t, app, "cv3", "pw")
	resp = req2(t, app.Router(), "GET", "/api/v1/config", stok, nil)
	assert.Equal(t, errForbidden, resp.Code)
}

// ---- 管理端用户创建 + 审计 ----

func TestCov_AdminUsersAudit(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	resp := req2(t, app.Router(), "POST", "/api/v1/admin/users", atok,
		map[string]string{"username": "n1", "password": "p", "nickname": "新", "role": "student"})
	require.Equal(t, 0, resp.Code)
	// 重复创建失败
	resp = req2(t, app.Router(), "POST", "/api/v1/admin/users", atok,
		map[string]string{"username": "n1", "password": "p", "nickname": "新", "role": "student"})
	assert.Equal(t, errBadRequest, resp.Code)
	// 审计查询
	resp = req2(t, app.Router(), "GET", "/api/v1/admin/audit", atok, nil)
	require.Equal(t, 0, resp.Code)
}

// ---- Agent 工具清单 + 课堂错误路径 ----

func TestCov_AgentToolsAndClassroomErrors(t *testing.T) {
	app, clk := newTestApp(t)
	ttok, _ := app.Auth.SignService("teacher-agent")
	// 工具清单：teacher 可见 memory.append、无 config.set
	resp := req2(t, app.Router(), "GET", "/api/v1/agent/tools", ttok, nil)
	require.Equal(t, 0, resp.Code)
	list, _ := resp.Data.([]any)
	require.NotEmpty(t, list)
	names := map[string]bool{}
	for _, it := range list {
		m, _ := it.(map[string]any)
		names[m["Name"].(string)] = true
	}
	assert.True(t, names["memory.append"])
	assert.False(t, names["config.set"], "teacher 不可见 config.set")
	// 学生不可用 agent 通道
	_, _ = app.Auth.CreateUser("cv4", "pw", "小", auth.RoleStudent)
	stok, _ := loginGet(t, app, "cv4", "pw")
	resp = req2(t, app.Router(), "GET", "/api/v1/agent/tools", stok, nil)
	assert.Equal(t, errForbidden, resp.Code)

	// 课堂错误路径：坏课 enter / 无房间 event、voice、resume
	// Missing and foreign lessons share a denial, preventing existence disclosure.
	resp = req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/999/enter", stok, nil)
	assert.Equal(t, errForbidden, resp.Code)
	resp = req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/1/event", stok,
		map[string]string{"event": "word_clicked"})
	assert.Equal(t, errForbidden, resp.Code, "无房间")
	resp = req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/1/voice", stok,
		map[string]string{"text": "下一个"})
	assert.Equal(t, errForbidden, resp.Code)
	resp = req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/1/resume", stok, nil)
	assert.Equal(t, errForbidden, resp.Code)
	_ = clk
}

// ---- 反馈 E 类 boost 闭环 + mine 历史 ----

func TestCov_FeedbackBoostAndMine(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "b库", "g7", []string{"apple", "bee"})
	_, _ = app.Auth.CreateUser("cv5", "pw", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "cv5", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))

	resp := req2(t, app.Router(), "POST", "/api/v1/feedbacks", stok,
		map[string]string{"text": "apple 我老忘"})
	require.Equal(t, 0, resp.Code, "boost 反馈")
	fb, _ := resp.Data.(map[string]any)
	assert.Equal(t, "applied", fb["status"])

	e, err := app.Books.EntryByKey(bid, "apple")
	require.NoError(t, err)
	card, err := app.Sched.GetCard(uid, bid, e.ID)
	require.NoError(t, err)
	assert.True(t, card.BoostFlag, "boost 落到记忆卡(SC-04)")

	// 空反馈拒绝
	resp = req2(t, app.Router(), "POST", "/api/v1/feedbacks", stok, map[string]string{"text": " "})
	assert.Equal(t, errBadRequest, resp.Code)

	// mine 历史
	resp = req2(t, app.Router(), "GET", "/api/v1/feedbacks/mine", stok, nil)
	require.Equal(t, 0, resp.Code)
	ms, _ := resp.Data.([]any)
	assert.Len(t, ms, 1)

	// 通知已读标记
	resp = req2(t, app.Router(), "POST", "/api/v1/student/notifications/read", stok, nil)
	require.Equal(t, 0, resp.Code)
	_ = clk
}

// ---- 学生课程列表端点 ----

func TestCov_StudentLessonsList(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "l库", "g9", []string{"w1"})
	_, _ = app.Auth.CreateUser("cv6", "pw", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "cv6", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	resp := req2(t, app.Router(), "GET", "/api/v1/student/lessons?bookId="+itoa64(bid), stok, nil)
	require.Equal(t, 0, resp.Code)
	ls, _ := resp.Data.([]any)
	require.Len(t, ls, 1)
	// 课报不存在
	lm, _ := ls[0].(map[string]any)
	resp = req2(t, app.Router(), "GET", "/api/v1/student/lessons/"+itoa64(int64(lm["ID"].(float64)))+"/report", stok, nil)
	assert.Equal(t, errNotFound, resp.Code)
}

// ---- Ticket 生命周期（R4-arch#3：含过期判定，需真实 lesson）----

func TestCov_TicketLifecycle(t *testing.T) {
	app, clk, lid, _ := setupRoom(t) // 真实 lesson（openAt 已到）
	t1 := app.IssueTicket(lid, 3)
	require.NotEmpty(t, t1)
	// 同绑定复用同一 ticket
	t2 := app.IssueTicket(lid, 3)
	assert.Equal(t, t1, t2)
	// 不同课不同 ticket
	assert.NotEqual(t, t1, app.IssueTicket(lid+1, 3))
	// 校验（lesson 存在且未过期）
	tk, ok := app.CheckTicket(t1)
	require.True(t, ok)
	assert.Equal(t, lid, tk.LessonID)
	_, ok = app.CheckTicket("bogus")
	assert.False(t, ok)
	// 过期：推进 26h（> openAt+overdueHours+1h 窗口）
	clk.Advance(80 * time.Hour)
	_, ok = app.CheckTicket(t1)
	assert.False(t, ok, "过期后失效")
	// 回收后失效
	t3 := app.IssueTicket(lid, 3)
	app.RevokeTicket(lid, 3)
	_, ok = app.CheckTicket(t3)
	assert.False(t, ok)
}

// ---- 真 WS 连接往返（upgrade→快照→心跳→事件→语音）----

func TestCov_RealWSRoundtrip(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "ws库", "g7", []string{"w1", "w2", "w3", "w4", "w5"})
	_, _ = app.Auth.CreateUser("sws", "pw", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "sws", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	lessonID := lessons[0].ID
	clk.Advance(25 * time.Hour)

	resp := req2(t, app.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lessonID)+"/enter",
		stok, map[string]any{})
	require.Equal(t, 0, resp.Code, resp.Msg)
	em, _ := resp.Data.(map[string]any)
	ticket, _ := em["ticket"].(string)

	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	// 坏 ticket 拒绝（401 握手失败）
	_, hr, err := websocket.DefaultDialer.Dial(base+"/ws/classroom?lessonId="+itoa64(lessonID)+"&ticket=bad", nil)
	require.Error(t, err)
	require.Equal(t, 401, hr.StatusCode)

	// 坏 lessonId
	_, hr, err = websocket.DefaultDialer.Dial(base+"/ws/classroom?lessonId=abc&ticket=x", nil)
	require.Error(t, err)
	require.Equal(t, 401, hr.StatusCode)

	// 正常连接
	conn, _, err := websocket.DefaultDialer.Dial(base+"/ws/classroom?lessonId="+itoa64(lessonID)+"&ticket="+ticket, nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 首帧快照
	var snap Msg
	require.NoError(t, conn.ReadJSON(&snap))
	require.Equal(t, "state_snapshot", snap["type"])
	assert.Equal(t, "w1", snap["word"])

	// 心跳
	require.NoError(t, conn.WriteJSON(Msg{"type": "heartbeat"}))
	var ack Msg
	require.NoError(t, conn.ReadJSON(&ack))
	assert.Equal(t, "heartbeat_ack", ack["type"])

	// 学生事件（点击单词）→ ui_action 流（首帧可能含 join 前遗留的 show_word，逐帧找 play_tts）
	var turnContext any = snap["context"]
	frames := sendFixtureWS(t, conn, &turnContext, Msg{"type":"student_event", "event":"word_clicked"}, "fixture-click")
	foundPlay := false
	for _, frame := range frames {
		if action, ok := frame["action"].(map[string]any); ok && action["type"] == "play_tts" { foundPlay = true }
	}
	assert.True(t, foundPlay)
	for i := 0; i < 4; i++ {
		sendFixtureWS(t, conn, &turnContext, Msg{"type":"student_event", "event":"tts_finished"}, "fixture-tts-"+itoa(i))
	}
	sendFixtureWS(t, conn, &turnContext, Msg{"type":"voice_text", "text":"w1"}, "fixture-repeat")
	frames = sendFixtureWS(t, conn, &turnContext, Msg{"type":"voice_text", "text":"下一个"}, "fixture-next")

	found := false
	for _, f := range frames {
		if f["type"] == "ui_action" {
			a, _ := f["action"].(map[string]any)
			if a["type"] == "play_tts" {
				found = true
			}
		}
	}
	assert.True(t, found, "语音指令在 show_word 相位应触发 play_tts")

	// resume（未暂停为幂等 no-op）
	require.NoError(t, conn.WriteJSON(Msg{"type": "resume"}))
}

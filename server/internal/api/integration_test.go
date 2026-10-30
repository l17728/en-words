// 集成测试（docs/03 §3 IT-01..IT-12）：REST 全链 + 课堂 Room 管线 +
// 语音通道 + 断线/重启恢复 + 反馈闭环 + 越权审计。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/tools"
	"enwords/internal/voicecmd"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 测试基建 ----

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = tAdd(c.t, d) }

func tAdd(t time.Time, d time.Duration) time.Time { return t.Add(d) }

func newTestApp(t *testing.T) (*App, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	app := NewApp(clk, nil)
	_, err := app.Auth.CreateUser("admin", "adminpw", "管理员", auth.RoleAdmin)
	require.NoError(t, err)
	return app, clk
}

func doJSON(t *testing.T, r *httptest.ResponseRecorder) Resp {
	t.Helper()
	var out Resp
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &out), "body: %s", r.Body.String())
	return out
}

func loginGet(t *testing.T, app *App, user, pw string) (token string, uid int64) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)
	m, _ := resp.Data.(map[string]any)
	tok, _ := m["token"].(string)
	require.NotEmpty(t, tok)
	fid, _ := m["user_id"].(float64) // JSON number → float64
	return tok, int64(fid)
}

// seedBook: 建库→导词→发布，返回 bookID
func seedBook(t *testing.T, app *App, token string, name, grade string, words []string) int64 {
	t.Helper()
	router := app.Router()
	body, _ := json.Marshal(map[string]string{"name": name, "grade": grade, "description": "t"})
	req := httptest.NewRequest("POST", "/api/v1/wordbooks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)
	m, _ := resp.Data.(map[string]any)
	bookID := int64(m["ID"].(float64))

	var entries []map[string]any
	for _, wd := range words {
		entries = append(entries, map[string]any{"word": wd, "meanings": []string{"n. 测试"}})
	}
	body, _ = json.Marshal(map[string]any{"entries": entries})
	req = httptest.NewRequest("POST", "/api/v1/wordbooks/"+itoa64(bookID)+"/import/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	resp = doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)

	req = httptest.NewRequest("POST", "/api/v1/wordbooks/"+itoa64(bookID)+"/publish", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 0, doJSON(t, w).Code)
	return bookID
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// fakeClient 测试连接（收集广播帧）。
type fakeClient struct {
	frames []Msg
}

func (f *fakeClient) Send(m Msg) error { f.frames = append(f.frames, m); return nil }

func hasFrame(frames []Msg, typ string) bool {
	for _, f := range frames {
		if f["type"] == typ {
			return true
		}
	}
	return false
}

// teachOneRoom 经 Room 管线教一个词
func teachOneRoom(r *Room, cfg ttsCfg) {
	r.HandleEvent(classroom.Event{Type: classroom.EvWordClicked})
	for i := 0; i < cfg.en+1; i++ { // en×N + zh×1
		r.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
	}
	for i := 0; i < cfg.rp; i++ {
		r.HandleEvent(classroom.Event{Type: classroom.EvRepeatDone})
	}
}

type ttsCfg struct{ en, rp int }

// answerCurrent 正确作答当前题（词义="n. 测试"→答案"测试"）
func answerCurrent(r *Room) []Msg {
	return r.HandleEvent(classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "测试"})
}

// ---- IT-01 登录→建词库→导入→发布 ----

func TestIT01_LoginBookImportPublish(t *testing.T) {
	app, _ := newTestApp(t)
	tok, _ := loginGet(t, app, "admin", "adminpw")
	require.NotEmpty(t, tok)
	bid := seedBook(t, app, tok, "初一测试库", "g7", []string{"apple", "bee", "cat"})
	assert.NotZero(t, bid)

	// 创建学生验证已发布词库可见性
	_, _ = app.Auth.CreateUser("stu", "pw", "小明", auth.RoleStudent)
	stuTok, _ := loginGet(t, app, "stu", "pw")
	req := httptest.NewRequest("GET", "/api/v1/wordbooks", nil)
	req.Header.Set("Authorization", "Bearer "+stuTok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code)
	list, _ := resp.Data.([]any)
	require.Len(t, list, 1)
	m, _ := list[0].(map[string]any)
	assert.Equal(t, float64(bid), m["ID"])
}

// ---- IT-02 导入去重 ----

func TestIT02_ImportDedup(t *testing.T) {
	app, _ := newTestApp(t)
	tok, _ := loginGet(t, app, "admin", "adminpw")
	router := app.Router()
	body, _ := json.Marshal(map[string]string{"name": "d", "grade": "g8"})
	req := httptest.NewRequest("POST", "/api/v1/wordbooks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	m, _ := doJSON(t, w).Data.(map[string]any)
	bid := int64(m["ID"].(float64))

	body, _ = json.Marshal(map[string]any{"entries": []map[string]any{
		{"word": "apple", "meanings": []string{"n. 苹果"}},
		{"word": "APPLE", "meanings": []string{"n. 苹果"}}, // 归一化重复
		{"word": "bee", "meanings": []string{"n. 蜜蜂"}},
	}})
	req = httptest.NewRequest("POST", "/api/v1/wordbooks/"+itoa64(bid)+"/import/commit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code)
	rm, _ := resp.Data.(map[string]any)
	assert.Equal(t, float64(2), rm["Inserted"], "重复 APPLE 跳过")
}

// ---- IT-03 选库→摸底→首课 ----

func TestIT03_PlacementFirstLesson(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "g9库", "g9", []string{"w1", "w2", "w3", "w4", "w5"})

	_, _ = app.Auth.CreateUser("s3", "pw", "小明", auth.RoleStudent)
	stok, uid := loginGet(t, app, "s3", "pw")

	// 选库
	req := httptest.NewRequest("POST", "/api/v1/student/books/"+itoa64(bid)+"/enable", nil)
	req.Header.Set("Authorization", "Bearer "+stok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	require.Equal(t, 0, doJSON(t, w).Code, "enable")

	// 摸底（勾 w1）
	body, _ := json.Marshal(map[string]any{"knownWordIds": []int64{1}, "finish": true})
	req = httptest.NewRequest("POST", "/api/v1/student/books/"+itoa64(bid)+"/placement", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	require.Equal(t, 0, doJSON(t, w).Code)

	// 首课存在：4 词（w1 已 mastered 排除）
	lessons, err := app.Sched.Lessons(uid, bid)
	require.NoError(t, err)
	require.Len(t, lessons, 1)
	assert.Len(t, lessons[0].BatchWordIDs, 4)
	assert.True(t, clk.Now().Before(lessons[0].OpenAt), "开放时间在未来")

	// 未到开放时间 enter 拒绝（D1）
	req = httptest.NewRequest("POST", "/api/v1/classroom/lessons/"+itoa64(lessons[0].ID)+"/enter", nil)
	req.Header.Set("Authorization", "Bearer "+stok)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	assert.Equal(t, errForbidden, resp.Code, "未开放不可进入")
}

// ---- IT-04 课堂全流程（enter→教学→final 完整测试→下课→自动排课）----

func TestIT04_ClassroomFullFlow(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "flow库", "g7", []string{"w1", "w2", "w3", "w4", "w5"})
	_, _ = app.Auth.CreateUser("s4", "pw", "小明", auth.RoleStudent)
	stok, uid := loginGet(t, app, "s4", "pw")

	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	lessonID := lessons[0].ID
	clk.Advance(25 * time.Hour) // 到开放时间

	// enter → ticket + snapshot
	body, _ := json.Marshal(nil)
	req := httptest.NewRequest("POST", "/api/v1/classroom/lessons/"+itoa64(lessonID)+"/enter", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)
	em, _ := resp.Data.(map[string]any)
	ticket, _ := em["ticket"].(string)
	require.NotEmpty(t, ticket)
	snap, _ := em["snapshot"].(map[string]any)
	assert.Equal(t, classroom.PhaseShowWord, snap["phase"])

	// WS 连接（伪造 ticket 已验；这里直接 Enter+Join 模拟连接后行为）
	room, _, err := app.Rooms.Enter(lessonID, uid)
	require.NoError(t, err)
	fc := &fakeClient{}
	room.Join(fc)

	// 教 5 词（D-21 定案：教学中零测试，全部教完才 final）
	cfg := ttsCfg{en: 3, rp: 3}
	for i := 0; i < 5; i++ {
		teachOneRoom(room, cfg)
	}
	// final 完整测试 5 题全对 → 无错词不进回顾直接下课
	for i := 0; i < 5; i++ {
		answerCurrent(room)
	}
	assert.Equal(t, "finished", room.Status(), "全对无回顾直接下课")

	// 帧序列完整
	assert.True(t, hasFrame(fc.frames, "ui_action"))
	assert.True(t, hasFrame(fc.frames, "judge_result"))
	assert.True(t, hasFrame(fc.frames, "lesson_summary"))

	// 结课结算：课报 + 自动排下一课（IT-04 断言⑤）
	res, ok := app.Report(lessonID)
	require.True(t, ok)
	assert.Equal(t, 5, res.Report.TotalWords)
	assert.Equal(t, 5, res.Report.CorrectCount)
	assert.Equal(t, "扎实", res.Report.QualSolid, "首答率100%")
	lessons2, _ := app.Sched.Lessons(uid, bid)
	require.Len(t, lessons2, 2, "结课自动排下一课")
	assert.Equal(t, scheduler.LessonScheduledStr, lessons2[1].Status)

	// ticket 已回收（课堂结束）
	_, still := app.CheckTicket(ticket)
	assert.False(t, still, "课堂结束 ticket 失效")

	// 课报详情端点
	req = httptest.NewRequest("GET", "/api/v1/student/lessons/"+itoa64(lessonID)+"/report", nil)
	req.Header.Set("Authorization", "Bearer "+stok)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	assert.Equal(t, 0, doJSON(t, w).Code)
}

// ---- IT-05 语音双通道：voice_text 指令 + 文本作答 ----

func TestIT05_VoiceDualChannel(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "v库", "g7", []string{"cat", "dog", "owl", "bee", "fox"})
	_, _ = app.Auth.CreateUser("s5", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "s5", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	clk.Advance(25 * time.Hour)
	room, _, err := app.Rooms.Enter(lessons[0].ID, uid)
	require.NoError(t, err)

	// 语音指令"下一个"=点击单词（L1）
	cmd, _ := room.HandleVoiceText("下一个！")
	assert.Equal(t, "next", cmd)
	assert.Equal(t, classroom.PhaseTTSSeg, room.Machine().Phase())

	// 跟读=老师听学生读词判读（用户定案 2026-08-25：不再口令"我读完了"/按钮）
	for i := 0; i < 4; i++ {
		room.HandleEvent(classroom.Event{Type: classroom.EvTTSFinished})
	}
	require.Equal(t, classroom.PhaseRepeating, room.Machine().Phase())
	// 无关文本≠读对 → 不推进（继续听，相位不变；旧口令也已被判读取代）
	cmd, _ = room.HandleVoiceText("我读完了")
	assert.Equal(t, "", cmd)
	assert.Equal(t, classroom.PhaseRepeating, room.Machine().Phase(), "没读对不推进")
	// 读对真实词 cat；不依赖旧判分器将含数字的 w1 截断成 w。
	cmd, _ = room.HandleVoiceText("cat")
	assert.Equal(t, "repeat_read", cmd)
	assert.Equal(t, classroom.PhaseShowWord, room.Machine().Phase())

	// 语音反馈捕获（FR-L2）
	cmd, _ = room.HandleVoiceText("我想反馈每天词太多了")
	assert.Equal(t, "feedback", cmd)
	select {
	case fb := <-room.FeedbackCh:
		assert.Contains(t, fb, "太多")
	default:
		t.Fatal("反馈未入队")
	}

	// 语音作答（测试阶段转写→判分）：教完剩余词到完整测试（新流程：教完全部词才测）
	cfg := ttsCfg{en: 3, rp: 3}
	teachOneRoom(room, cfg) // dog
	teachOneRoom(room, cfg) // owl
	teachOneRoom(room, cfg) // bee
	teachOneRoom(room, cfg) // fox → final 完整测试
	require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
	fc := &fakeClient{}
	room.Join(fc)
	cmd, _ = room.HandleVoiceText("测试") // 非指令→作答→Mock 判分 correct
	assert.Equal(t, "", cmd, "非指令文本走作答")
	assert.True(t, hasFrame(fc.frames, "judge_result"), "语音转写作答判分")
}

func TestQuizSkipFromAnswerEventUsesSkipTransition(t *testing.T) {
	app, clk := newTestApp(t)
	var logs bytes.Buffer
	app.Log = obs.New(&logs)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "quiz-skip库", "g7", []string{"cat", "dog", "owl", "bee", "fox"})
	_, _ = app.Auth.CreateUser("quiz-skip-student", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "quiz-skip-student", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	clk.Advance(25 * time.Hour)
	room, _, err := app.Rooms.Enter(lessons[0].ID, uid)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		teachOneRoom(room, ttsCfg{en: 3, rp: 3})
	}
	require.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
	item := room.Machine().CurrentItem().QuizItemID
	before := room.contextLocked()
	frames := room.HandleClientEvent(ClientEnvelope{Context: before, EventID: "typed-explicit-skip"},
		classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "跳过！"})
	assert.NotEqual(t, item, room.Machine().CurrentItem().QuizItemID)
	assert.Equal(t, classroom.JudgmentWrong, room.Machine().Snapshot().Judged[item])
	assert.Equal(t, classroom.PhaseWaitAnswer, room.Machine().Phase())
	assert.False(t, hasFrame(frames, "judge_result"), "explicit skip advances without invoking judgment")
	late := room.HandleClientEvent(ClientEnvelope{Context: before, EventID: "late-explicit-skip"},
		classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "跳过"})
	assert.Equal(t, item, room.Machine().Snapshot().QuizQueue[0], "the original item stays recorded")
	assert.True(t, hasFrame(late, "event_rejected"), "stale turn must be rejected")
	assert.Contains(t, logs.String(), `"event":"quiz_skipped"`)
	assert.NotContains(t, logs.String(), "跳过")
	voiceItem := room.Machine().CurrentItem().QuizItemID
	command, _ := room.HandleVoiceText("跳过")
	assert.Equal(t, voicecmd.CmdSkip, command)
	assert.NotEqual(t, voiceItem, room.Machine().CurrentItem().QuizItemID, "voice skip advances one quiz item")

	// A short confirmation arriving while judgement is already in flight cannot
	// enqueue a second judge request or alter the current quiz item.
	room.mu.Lock()
	require.NoError(t, room.machine.OnEvent(classroom.Event{Type: classroom.EvAnswerSubmitted, Text: "未完成的答案"}))
	room.machine.DrainActions() // discard the deliberately unserviced initial request
	room.mu.Unlock()
	judgingItem := room.Machine().CurrentItem().QuizItemID
	command, _ = room.HandleVoiceText("不会")
	assert.Equal(t, "confirmation_ignored", command)
	assert.Equal(t, classroom.PhaseJudging, room.Machine().Phase())
	assert.Equal(t, judgingItem, room.Machine().CurrentItem().QuizItemID)
	assert.Empty(t, room.Machine().DrainActions(), "duplicate confirmation must not queue another judge request")
}

// ---- IT-06 断线重连快照 ----

func TestIT06_ReconnectSnapshot(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "r库", "g7", []string{"w1", "w2", "w3", "w4", "w5"})
	_, _ = app.Auth.CreateUser("s6", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "s6", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	clk.Advance(25 * time.Hour)

	room, snap, err := app.Rooms.Enter(lessons[0].ID, uid)
	require.NoError(t, err)
	fc1 := &fakeClient{}
	room.Join(fc1)
	// 教 2 词后"断线"
	teachOneRoom(room, ttsCfg{en: 3, rp: 3})
	teachOneRoom(room, ttsCfg{en: 3, rp: 3})
	room.Leave(fc1)

	// 重连：新客户端 Join 收全量快照，进度不丢（第3词）
	fc2 := &fakeClient{}
	room.Join(fc2)
	require.Len(t, fc2.frames, 1)
	s := fc2.frames[0]
	assert.Equal(t, "state_snapshot", s["type"])
	assert.Equal(t, "w3", s["word"], "断点续学第3词")
	_ = snap
}

// ---- IT-07 反馈闭环 A 类（词量调整）----

func TestIT07_FeedbackReduce(t *testing.T) {
	app, _ := newTestApp(t)
	_, _ = app.Auth.CreateUser("s7", "pw", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "s7", "pw")

	body, _ := json.Marshal(map[string]string{"text": "每次词太多了，累"})
	req := httptest.NewRequest("POST", "/api/v1/feedbacks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)
	fb, _ := resp.Data.(map[string]any)
	assert.Equal(t, "applied", fb["status"])
	assert.Contains(t, fb["reply"], "词量")

	// 配置生效 60→50
	assert.Equal(t, 50, app.Cfg.Int(uid, "wordsPerLesson"), "反馈自动降量10")

	// 通知到达（G-6）
	ns := app.Notifications(uid)
	require.NotEmpty(t, ns)
	assert.Contains(t, ns[0].Title, "反馈")
}

// ---- IT-08 反馈 D 类超界（推迟>7天→need_confirm）----

func TestIT08_FeedbackPostponeBoundary(t *testing.T) {
	app, _ := newTestApp(t)
	_, _ = app.Auth.CreateUser("s8", "pw", "小", auth.RoleStudent)
	stok, _ := loginGet(t, app, "s8", "pw")

	// 界内 D：明天有事（无可推迟课→回复但无副作用；此处验证意图路径）
	body, _ := json.Marshal(map[string]string{"text": "明天有事，先缓缓"})
	req := httptest.NewRequest("POST", "/api/v1/feedbacks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	assert.Equal(t, 0, doJSON(t, w).Code)

	// 超界：下个月15号 → need_confirm
	body, _ = json.Marshal(map[string]string{"text": "下个月15号都有事"})
	req = httptest.NewRequest("POST", "/api/v1/feedbacks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code)
	fb, _ := resp.Data.(map[string]any)
	assert.Equal(t, "need_confirm", fb["status"], "推迟>7天需确认(G-4)")
}

// ---- IT-09 逾期巡检 ----

func TestIT09_OverdueReplan(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "o库", "g7", []string{"w1", "w2"})
	_, _ = app.Auth.CreateUser("s9", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "s9", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	require.Len(t, lessons, 1)

	// 未逾期：不动（openAt=8/24 10:00，now 推进到 8/25 09:00 仍 < openAt+24h）
	clk.Advance(47 * time.Hour)
	replanned, err := app.CronOverdue()
	require.NoError(t, err)
	assert.Empty(t, replanned)

	// 逾期：expired + 次日重排 + 通知（S-10 闭环）
	clk.Advance(2 * time.Hour) // now = 8/25 11:00 > openAt+24h
	replanned, err = app.CronOverdue()
	require.NoError(t, err)
	require.Len(t, replanned, 1)
	got, _ := app.Sched.Lesson(lessons[0].ID)
	assert.Equal(t, scheduler.LessonExpiredStr, got.Status)
	newL, _ := app.Sched.Lesson(replanned[0])
	nextDay := clk.Now().AddDate(0, 0, 1)
	expected := time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), lessons[0].OpenAt.Hour(), lessons[0].OpenAt.Minute(), 0, 0, lessons[0].OpenAt.Location())
	assert.Equal(t, expected, newL.OpenAt)
	ns := app.Notifications(uid)
	require.NotEmpty(t, ns, "逾期重排通知学生(S-10)")
	assert.Contains(t, ns[0].Body, "重新安排课程")
	assert.Contains(t, ns[0].Body, expected.Format("2006-01-02 15:04"))
}

// ---- IT-10 Agent 越权 ----

func TestIT10_AgentScopeDenied(t *testing.T) {
	app, _ := newTestApp(t)
	ttok, _ := app.Auth.SignService("teacher-agent")
	args, _ := json.Marshal(map[string]any{"key": "wordsPerLesson", "value": 80, "reason": "x"})
	body, _ := json.Marshal(map[string]any{"name": "config.set", "args": json.RawMessage(args)})
	req := httptest.NewRequest("POST", "/api/v1/agent/tools/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+ttok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp := doJSON(t, w)
	assert.Equal(t, errForbidden, resp.Code, "teacher 调 config.set 被拒")
	assert.Contains(t, resp.Msg, "scope")

	// 审计含 denied 记录
	assert.NotEmpty(t, app.ToolAudit.Entries)
	assert.Equal(t, "denied", app.ToolAudit.Entries[0].Result)

	// system-agent 合法调用成功（memory.append）
	stok, _ := app.Auth.SignService("system-agent")
	margs, _ := json.Marshal(map[string]any{
		"scope": "student_profile", "student_id": 9, "kind": "obs", "content": "测试生安静", "importance": 3})
	body, _ = json.Marshal(map[string]any{"name": "memory.append", "args": json.RawMessage(margs)})
	req = httptest.NewRequest("POST", "/api/v1/agent/tools/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	resp = doJSON(t, w)
	require.Equal(t, errForbidden, resp.Code, "external service token has no trusted task")
	ctx, err := app.newAgentTaskContext(context.Background(), 9, 0, 0, "feedback_submitted", tools.RoleSystemAgent)
	require.NoError(t, err)
	_, err = app.callAgentTool(ctx, "memory.append", json.RawMessage(margs))
	require.NoError(t, err)

}

// ---- IT-11 工具审计链完整（trace 深度贯穿归 GORM 版）----

func TestIT11_AuditTrail(t *testing.T) {
	app, _ := newTestApp(t)
	_, _ = app.Auth.CreateUser("s11", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "s11", "pw")
	_ = uid
	stok, _ := app.Auth.SignService("system-agent")
	args, _ := json.Marshal(map[string]any{
		"scope": "student_profile", "student_id": 1, "kind": "obs", "content": "x", "importance": 3})
	body, _ := json.Marshal(map[string]any{"name": "memory.append", "args": json.RawMessage(args)})
	req := httptest.NewRequest("POST", "/api/v1/agent/tools/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+stok)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	require.Equal(t, errForbidden, doJSON(t, w).Code)
	ctx, err := app.newAgentTaskContext(context.Background(), uid, 0, 0, "feedback_submitted", tools.RoleSystemAgent)
	require.NoError(t, err)
	_, err = app.callAgentTool(ctx, "memory.append", map[string]any{"scope": "student_profile", "kind": "obs", "content": "x", "importance": 3})
	require.NoError(t, err)
	entries := app.ToolAudit.Snapshot()
	require.NotEmpty(t, entries)
	e := entries[len(entries)-1]
	assert.Equal(t, "system_agent", e.ActorID)
	assert.Equal(t, tools.RoleSystemAgent, e.ActorRole)
	assert.Equal(t, "memory.append", e.Tool)
	assert.True(t, e.OK)
	assert.GreaterOrEqual(t, e.DurationMs, int64(1))
}

// ---- IT-12 课堂重启恢复 ----

func TestIT12_RoomRestartRestore(t *testing.T) {
	app, clk := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "p库", "g7", []string{"w1", "w2", "w3", "w4", "w5"})
	_, _ = app.Auth.CreateUser("s12", "pw", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "s12", "pw")
	require.NoError(t, app.EnableBook(uid, bid))
	require.NoError(t, app.PlacementSubmit(uid, bid, nil, true))
	lessons, _ := app.Sched.Lessons(uid, bid)
	clk.Advance(25 * time.Hour)

	room, _, err := app.Rooms.Enter(lessons[0].ID, uid)
	require.NoError(t, err)
	teachOneRoom(room, ttsCfg{en: 3, rp: 3})
	teachOneRoom(room, ttsCfg{en: 3, rp: 3})

	// 模拟进程重启：房间丢弃（快照已随每次迁移持久化到 App）
	app.Rooms.Drop(lessons[0].ID)

	room2, snap, err := app.Rooms.Enter(lessons[0].ID, uid)
	require.NoError(t, err)
	assert.Equal(t, "w3", snap["word"], "重启后从第3词断点续课")
	assert.Equal(t, classroom.PhaseShowWord, room2.Machine().Phase())
	teachOneRoom(room2, ttsCfg{en: 3, rp: 3})
	assert.Equal(t, "w4", room2.Machine().CurrentWord().Word, "恢复后继续教学推进正常")
}

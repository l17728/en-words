// 课堂 Hub：每课一房间。职责（SDD §4.2/§5/§11/§12.4）：
// 持有 classroom.Machine（平台唯一事实源）、多连接广播（双标签页）、
// 超时驱动（计时器归属平台）、判分接线（judge_request→judge→OnJudged）、
// 语音指令（voicecmd）、结课结算（scheduler+SystemAgent+D-13 课报）、
// 状态快照持久化（App 内存版；GORM 版换 lessons 列存，接口不变 → IT-12 重启恢复）。
package api

import (
	"sync"
	"time"

	"enwords/internal/agent"
	"enwords/internal/classroom"
	"enwords/internal/voicecmd"
)

// Msg WS 帧。
type Msg map[string]any

// Client 抽象连接（真实 WS / 测试替身）。
type Client interface {
	Send(m Msg) error
}

// Room 一间课堂。
type Room struct {
	mu           sync.Mutex
	app          *App
	lessonID     int64
	bookID       int64
	studentID    int64
	machine      *classroom.Machine
	words        map[int64]classroom.Word
	clients      map[Client]bool
	seq          int
	lastAnswer   string
	finished     bool
	voiceParser  *voicecmd.Parser
	lessonStatus string // open | in_progress | finished
	FeedbackCh   chan string
	phaseSince   time.Time
	lastPhase    string
}

// Hub 房间表。
type Hub struct {
	app   *App
	mu    sync.Mutex
	rooms map[int64]*Room
}

func NewHub(app *App) *Hub {
	return &Hub{app: app, rooms: map[int64]*Room{}}
}

// Enter 进入/重进课堂：房间懒建（有持久化快照则恢复，IT-12）。
func (h *Hub) Enter(lessonID, studentID int64) (*Room, Msg, error) {
	h.mu.Lock()
	r, ok := h.rooms[lessonID]
	if !ok {
		r = &Room{
			app: h.app, lessonID: lessonID, studentID: studentID,
			words: map[int64]classroom.Word{}, clients: map[Client]bool{},
			voiceParser: voicecmd.New(voicecmd.DefaultTable()),
			FeedbackCh: make(chan string, 8), lessonStatus: "in_progress",
		}
		lesson, err := h.app.Sched.Lesson(lessonID)
		if err != nil {
			h.mu.Unlock()
			return nil, nil, err
		}
		r.bookID = lesson.BookID
		entries, err := h.app.Books.EntriesByIDs(lesson.BookID, lesson.BatchWordIDs)
		if err != nil {
			h.mu.Unlock()
			return nil, nil, err
		}
		cw := make([]classroom.Word, 0, len(entries))
		for _, e := range entries {
			w := classroom.Word{ID: e.ID, Word: e.Word, Meanings: e.Meanings}
			cw = append(cw, w)
			r.words[e.ID] = w
		}
		r.machine = classroom.NewMachine(lessonID, lesson.BookID, studentID,
			cw, h.app.Cfg.Snapshot(studentID), h.app.Clock)
		// IT-12：进程重启（房间丢失）→ 从持久化快照恢复；全新机器才 Start
		restored := false
		if saved := h.app.lessonState(lessonID); saved != nil {
			if err := r.machine.Restore(*saved); err == nil {
				restored = true
				r.lessonStatus = "in_progress"
			}
		}
		if !restored {
			if err := r.machine.Start(); err != nil {
				h.mu.Unlock()
				return nil, nil, err
			}
		}
		r.phaseSince = h.app.Clock.Now()
		r.lastPhase = r.machine.Phase()
		h.rooms[lessonID] = r
	}
	h.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	return r, r.snapshotLocked(), nil
}

// Join 注册连接并补发全量状态（重连快照，J4）。
func (r *Room) Join(c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[c] = true
	_ = c.Send(r.snapshotLocked())
}

func (r *Room) Leave(c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, c)
}

func (r *Room) snapshotLocked() Msg {
	phase := r.machine.Phase()
	m := Msg{
		"type": "state_snapshot", "seq": r.nextSeqLocked(),
		"lesson_id": r.lessonID, "phase": phase,
		"paused": r.machine.Paused(),
	}
	if phase == classroom.PhaseShowWord {
		m["word"] = r.machine.CurrentWord().Word
	}
	if it, ok := r.currentItemView(); ok {
		m["quiz_word"] = it.Word
		m["quiz_item"] = itemPayload(it)
	}
	return m
}

func itemPayload(it classroom.QuizItemView) Msg {
	return Msg{"word_id": it.WordID, "kind": it.Kind, "round": it.Round, "seq": it.Seq}
}

func (r *Room) currentItemView() (classroom.QuizItemView, bool) {
	switch r.machine.Phase() {
	case classroom.PhaseWaitAnswer, classroom.PhaseJudging, classroom.PhaseReteachShow:
		it := r.machine.CurrentItem()
		if it.WordID != 0 {
			return it, true
		}
	}
	return classroom.QuizItemView{}, false
}

func (r *Room) nextSeqLocked() int {
	r.seq++
	return r.seq
}

// broadcastLocked 排空 machine 动作→广播→返回（供 REST/WS 双通道）；
// 判分接线：phase==Judging 时同步 Mock 判分；phase 变化时记录 phaseSince；
// 每次迁移后快照持久化（IT-12）。
func (r *Room) broadcastLocked() []Msg {
	var out []Msg
	for _, a := range r.machine.DrainActions() {
		m := Msg{"type": "ui_action", "seq": r.nextSeqLocked(), "action": a}
		r.emitLocked(m)
		out = append(out, m)
	}
	if ph := r.machine.Phase(); ph != r.lastPhase {
		r.lastPhase = ph
		r.phaseSince = r.app.Clock.Now()
	}
	if r.machine.Phase() == classroom.PhaseJudging {
		out = append(out, r.judgeLocked()...)
	}
	if r.machine.Phase() == classroom.PhaseLessonEnd {
		r.maybeFinishLocked()
	}
	r.app.saveLessonState(r.lessonID, r.machine.Snapshot())
	return out
}

func (r *Room) emitLocked(m Msg) {
	for c := range r.clients {
		_ = c.Send(m)
	}
}

// judgeLocked 判分：judge L1(+L2) → OnJudged → 后续动作广播。
func (r *Room) judgeLocked() []Msg {
	var out []Msg
	it := r.machine.CurrentItem()
	w := r.words[it.WordID]
	j := r.app.Judge.Judge(w.Word, w.Meanings, r.lastAnswer)
	jm := Msg{"type": "judge_result", "seq": r.nextSeqLocked(),
		"quiz_item": itemPayload(it), "judgment": string(j), "asr_text": r.lastAnswer}
	r.emitLocked(jm)
	out = append(out, jm)
	_ = r.machine.OnJudged(it.ID(), classroom.Judgment(j))
	out = append(out, r.broadcastActionsOnly()...)
	return out
}

// broadcastActionsOnly 只排空动作不再触发判分（防递归）。
func (r *Room) broadcastActionsOnly() []Msg {
	var out []Msg
	for _, a := range r.machine.DrainActions() {
		m := Msg{"type": "ui_action", "seq": r.nextSeqLocked(), "action": a}
		r.emitLocked(m)
		out = append(out, m)
	}
	if ph := r.machine.Phase(); ph != r.lastPhase {
		r.lastPhase = ph
		r.phaseSince = r.app.Clock.Now()
	}
	if r.machine.Phase() == classroom.PhaseLessonEnd {
		r.maybeFinishLocked()
	}
	r.app.saveLessonState(r.lessonID, r.machine.Snapshot())
	return out
}

// HandleEvent 学生事件入口；返回本步产生的全部帧（REST/WS 双通道）。
func (r *Room) HandleEvent(ev classroom.Event) []Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.Type == classroom.EvAnswerSubmitted {
		r.lastAnswer = ev.Text
	}
	_ = r.machine.OnEvent(ev)
	return r.broadcastLocked()
}

// FireTimeout 强制触发当前相位超时（测试钩子/平台兜底）。
func (r *Room) FireTimeout() []Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.machine.OnTimeout(r.machine.Phase())
	return r.broadcastLocked()
}

// Tick 生产计时循环节拍：相位驻留超时才触发。
func (r *Room) Tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.machine.Paused() || r.finished {
		return
	}
	d, ok := r.machine.TimeoutFor()
	if !ok {
		return
	}
	if r.app.Clock.Now().Sub(r.phaseSince) >= d {
		_ = r.machine.OnTimeout(r.machine.Phase())
		r.broadcastLocked()
	}
}

// HandleVoiceText 语音转写：指令优先→反馈捕获→作答（02 §12.4）。
// 返回 (指令类型, 产生的帧)。
func (r *Room) HandleVoiceText(text string) (string, []Msg) {
	if _, payload, ok := r.voiceParser.MatchFeedback(text); ok {
		select {
		case r.FeedbackCh <- payload:
		default:
		}
		return "feedback", nil
	}
	ph := r.machine.Phase()
	quiz := ph == classroom.PhaseWaitAnswer || ph == classroom.PhaseJudging
	if cmd, ok := r.voiceParser.Match(text); ok {
		if !quiz || r.voiceParser.AllowedInQuiz(cmd) {
			return cmd, r.applyCommand(cmd)
		}
	}
	return "", r.HandleEvent(classroom.Event{Type: classroom.EvAnswerSubmitted, Text: text})
}

func (r *Room) applyCommand(cmd string) []Msg {
	var ev classroom.Event
	switch cmd {
	case voicecmd.CmdNext:
		ev = classroom.Event{Type: classroom.EvWordClicked}
	case voicecmd.CmdRepeatDone:
		ev = classroom.Event{Type: classroom.EvRepeatDone}
	case voicecmd.CmdSkip:
		if r.machine.Phase() == classroom.PhaseRepeating {
			ev = classroom.Event{Type: classroom.EvRepeatSkipped}
		}
	default:
		return nil // replay/pause/exit/nav 由前端处理
	}
	if ev.Type == "" {
		return nil
	}
	return r.HandleEvent(ev)
}

// SessionTimeout 会话超时暂停（R2-09）。
func (r *Room) SessionTimeout() []Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.machine.OnSessionTimeout()
	return r.broadcastLocked()
}

// Resume 续课。
func (r *Room) Resume() []Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.machine.Resume()
	return r.broadcastLocked()
}

// maybeFinishLocked 结课结算：scheduler step 推进 + SystemAgent 决策 + 课报 + ticket 回收。
func (r *Room) maybeFinishLocked() {
	if r.finished || r.machine.Phase() != classroom.PhaseLessonEnd {
		return
	}
	r.finished = true
	r.lessonStatus = "finished"
	res := r.machine.Result()
	if res == nil {
		return
	}
	_ = r.app.Sched.SettleLessonFinished(r.studentID, r.bookID, res.Correct, r.app.Clock.Now())
	// 结课即排下一课（F2 只排一课；已排/无词则忽略）
	if _, err := r.app.Sched.PlanNext(r.studentID, r.bookID); err != nil {
		r.app.Log.Info("scheduler", "plan_after_lesson", map[string]any{
			"lesson_id": r.lessonID, "result": err.Error()})
	}
	// D-13：系统 Agent 结课决策（S-01..S-04/S-15，Mock 确定性）
	in := agent.SystemInput{Trigger: "lesson_finished",
		StudentID: r.studentID, BookID: r.bookID,
		CorrectRate: res.Report.FirstTryRate, AvgAnswerMs: res.Report.AvgAnswerMs}
	for _, a := range r.app.System.Decide(in) {
		if a.Kind == "adjust_words_down" {
			cur := r.app.Cfg.Int(r.studentID, "wordsPerLesson")
			_ = r.app.Cfg.SetStudent("system_agent", r.studentID, "wordsPerLesson",
				max40(cur-10), "课报评估：首答率<60% 自动降量(S-03)")
		}
	}
	sm := Msg{"type": "lesson_summary", "seq": r.nextSeqLocked(),
		"report": res.Report, "correct": res.Correct}
	r.emitLocked(sm)
	r.app.RevokeTicket(r.lessonID, r.studentID)
	r.app.saveReport(r.lessonID, res)
	r.app.Notify(r.studentID, "课报已生成",
		"正确率 "+strconvPercent(res.Report.FirstTryRate)+"，"+res.Report.QualSolid)
}

func strconvPercent(f float64) string {
	return itoa(int(f*100+0.5)) + "%"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		return "-" + s
	}
	return s
}

func max40(n int) int {
	if n < 40 {
		return 40
	}
	return n
}

// Status 房间状态。
func (r *Room) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lessonStatus
}

// Machine 暴露状态机（测试用）。
func (r *Room) Machine() *classroom.Machine { return r.machine }

// Room 按 lessonID 取房间。
func (h *Hub) Room(lessonID int64) (*Room, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[lessonID]
	return r, ok
}

// Drop 模拟进程重启（IT-12）：移除房间但快照已持久化于 App。
func (h *Hub) Drop(lessonID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms, lessonID)
}

// RunTimers 生产计时循环（main 启动；测试不启动）。
func (h *Hub) RunTimers() {
	tick := time.NewTicker(time.Second)
	sess := time.NewTicker(30 * time.Second)
	for {
		select {
		case <-tick.C:
			h.mu.Lock()
			rooms := make([]*Room, 0, len(h.rooms))
			for _, r := range h.rooms {
				rooms = append(rooms, r)
			}
			h.mu.Unlock()
			for _, r := range rooms {
				r.Tick()
			}
		case <-sess.C:
			h.mu.Lock()
			rooms := make([]*Room, 0, len(h.rooms))
			for _, r := range h.rooms {
				rooms = append(rooms, r)
			}
			h.mu.Unlock()
			for _, r := range rooms {
				r.SessionTimeout()
			}
		}
	}
}

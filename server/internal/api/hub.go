// 课堂 Hub：每课一房间。职责（SDD §4.2/§5/§11/§12.1）：
// 持有 classroom.Machine（平台唯一事实源）、多连接广播（双标签页）、
// 超时驱动（计时器归属平台）、判分接线（judge_request→judge→OnJudged）、
// 语音指令（voicecmd）、结课结算（scheduler+SystemAgent+D-13 课报）、
// 状态快照持久化（App 内存版；GORM 版换 lessons 列存，接口不变 → IT-12 重启恢复）。
package api

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/judge"
	"enwords/internal/scripts"
	"enwords/internal/voicecmd"
	"gorm.io/gorm"
)

// seedRng 话术确定性随机源（seed=lessonId，04 §7.1）。
func seedRng(lessonID int64) *scripts.RNG { return scripts.NewSeededRNG(lessonID) }

// Msg WS 帧。
type Msg map[string]any

// Client 抽象连接（真实 WS / 测试替身）。
type Client interface {
	Send(m Msg) error
}

// Room 一间课堂。
type Room struct {
	deliveryReady     bool
	teacherPending    string
	teacherText       string
	epoch             string
	lastTurnSignature string
	turnVersion       uint64
	seenEvents        map[string]bool
	eventOrder        []string
	mu                sync.Mutex
	app               *App
	lessonID          int64
	bookID            int64
	studentID         int64
	machine           *classroom.Machine
	words             map[int64]classroom.Word
	clients           map[Client]bool
	currentCli        Client // 当前活跃学生连接（StrictMode 重复 Join 时踢旧）
	seq               int
	lastAnswer        string
	finished          bool
	voiceParser       *voicecmd.Parser
	lessonStatus      string // open | in_progress | finished
	FeedbackCh        chan string
	phaseSince        time.Time
	lastPhase         string
	lastActions       []classroom.Action // 最近一批 machine 动作（话术层输入）
	encourageCounter  int                // 自上次鼓励的判对数（encourageEveryN）
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
	owned, err := h.app.Sched.Lesson(lessonID)
	if err != nil || owned.StudentID != studentID {
		return nil, nil, fmt.Errorf("lesson unavailable")
	}
	h.mu.Lock()
	r, ok := h.rooms[lessonID]
	if ok && r.studentID != studentID {
		h.mu.Unlock()
		return nil, nil, fmt.Errorf("lesson unavailable")
	}
	if !ok {
		r = &Room{
			app: h.app, lessonID: lessonID, studentID: studentID,
			words: map[int64]classroom.Word{}, clients: map[Client]bool{},
			voiceParser: voicecmd.New(voicecmd.DefaultTable()),
			FeedbackCh:  make(chan string, 8), lessonStatus: "in_progress",
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
		var saved *classroom.State
		if h.app.DB != nil {
			var check classroom.State
			err := h.app.readRuntimeError(fmt.Sprintf("snapshot/%d", lessonID), &check)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				h.mu.Unlock()
				return nil, nil, fmt.Errorf("saved classroom unavailable: %w", err)
			}
			if err == nil {
				saved = &check
			}
		} else {
			saved = h.app.lessonState(lessonID)
		}
		if saved != nil {
			if err := r.machine.Restore(*saved); err != nil {
				h.app.Log.Error("classroom", "snapshot_failed", map[string]any{"lesson_id": lessonID, "error": err.Error()})
				h.mu.Unlock()
				return nil, nil, fmt.Errorf("saved classroom unavailable: %w", err)
			}
			restored = true
			r.lessonStatus = "in_progress"
			h.app.Log.Info("classroom", "snapshot_restored", map[string]any{"lesson_id": lessonID, "phase": saved.Phase})
		}
		if !restored {
			if err := r.machine.Start(); err != nil {
				h.mu.Unlock()
				return nil, nil, err
			}
		}
		if restored {
			r.teacherPending = "state_restored"
		} else {
			r.teacherPending = "lesson_started"
		}
		r.phaseSince = h.app.Clock.Now()
		r.lastPhase = r.machine.Phase()
		h.rooms[lessonID] = r
	}
	h.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.teacherPending != "" {
		event := r.teacherPending
		r.teacherPending = ""
		r.teacherLifecycleLocked(event)
	}
	return r, r.snapshotLocked(), nil
}

// A newly entered room must not emit its first audio before there is a
// delivery path. Explicit REST input also activates REST-only classrooms.
func (r *Room) activateDeliveryLocked() {
	if !r.deliveryReady {
		r.deliveryReady = true
		r.phaseSince = r.app.Clock.Now()
		r.app.Log.Info("classroom", "delivery_ready", map[string]any{"lesson_id": r.lessonID})
	}
}

// Join 注册连接并补发全量状态（重连快照，J4）。
func (r *Room) Join(c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activateDeliveryLocked()
	// StrictMode/重连保护：同一学生重复 Join 时踢掉旧连接，防帧双投递
	if r.currentCli != nil && r.currentCli != c {
		delete(r.clients, r.currentCli)
		_ = r.currentCli.Send(Msg{"type": "kick", "reason": "new_connection"})
		if closer, ok := r.currentCli.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	r.currentCli = c
	r.clients[c] = true
	_ = c.Send(r.snapshotLocked())
}

func (r *Room) Leave(c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, c)
}

func (r *Room) IsActive(c Client) bool { r.mu.Lock(); defer r.mu.Unlock(); return r.currentCli == c }

func (r *Room) snapshotLocked() Msg {
	phase := r.machine.Phase()
	m := Msg{
		"type": "state_snapshot", "seq": r.nextSeqLocked(), "teacher_text": r.teacherText,
		"lesson_id": r.lessonID, "phase": phase,
		"paused": r.machine.Paused(), "context": r.contextLocked(),
	}
	// 教学相位（show/tts/repeating/reteach）补全 word+zhMeaning：
	// 重进课堂任何相位都能立即渲染完整中英对照（修复"重进只有…"）
	w := r.machine.CurrentWord()
	switch phase {
	case classroom.PhaseShowWord, classroom.PhaseTTSSeg, classroom.PhaseRepeating, classroom.PhaseReteachShow:
		m["word"] = w.Word
		if len(w.Meanings) > 0 {
			m["zhMeaning"] = w.Meanings[0]
		}
	}
	if it, ok := r.currentItemView(); ok {
		m["quiz_word"] = it.Word
		m["quiz_item"] = itemPayload(it)
	} else if phase != classroom.PhaseLessonEnd {
		// R5：全相位兜底可渲染词（前端 bigWord 依赖 quiz_word||word）
		m["quiz_word"] = w.Word
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

func (r *Room) actionMsg(a classroom.Action) Msg {
	if w, ok := r.words[a.WordID]; ok {
		if a.Word == "" {
			if a.Lang == "zh" {
				a.Word = firstMeaning(w)
			} else {
				a.Word = w.Word
			}
		}
		if a.Type == classroom.ActShowWord && a.WithZh {
			a.Meanings = w.Meanings
		}
	}
	m := Msg{"type": "ui_action", "seq": r.nextSeqLocked(), "action": a}
	if a.Type == classroom.ActPlayTTS {
		turn := r.contextLocked()
		r.app.Log.Info("classroom", "tts_segment_scheduled", map[string]any{
			"student_id": r.studentID, "lesson_id": r.lessonID, "epoch": turn.Epoch, "turn": turn.Turn,
			"segment": a.SegIdx, "language": a.Lang, "delay_before_ms": a.DelayBeforeMs,
			"result": "scheduled",
		})
	}
	if a.Type == classroom.ActShowQuizItem || a.Type == classroom.ActStartQuiz {
		m["quiz_item"] = itemPayload(r.machine.CurrentItem())
	}
	return m
}

// broadcastLocked 排空 machine 动作→广播→返回（供 REST/WS 双通道）；
// 判分接线：phase==Judging 时同步 Mock 判分；phase 变化时记录 phaseSince + §14 日志；
// 每次迁移后快照持久化（IT-12）；老师话术层随关键动作异步注入（D-10）。
func (r *Room) broadcastLocked() []Msg {
	var out []Msg
	r.lastActions = r.machine.DrainActions()
	for _, a := range r.lastActions {
		if a.Type == classroom.ActLessonSummary {
			continue
		}
		m := r.actionMsg(a)
		r.emitLocked(m)
		out = append(out, m)
	}
	if ph := r.machine.Phase(); ph != r.lastPhase {
		r.app.Log.Info("classroom", "phase_changed", map[string]any{
			"lesson_id": r.lessonID, "from": r.lastPhase, "to": ph})
		pm := Msg{"type": "phase_changed", "seq": r.nextSeqLocked(), // §11.2 帧
			"from": r.lastPhase, "phase": ph, "lesson_id": r.lessonID}
		r.emitLocked(pm)
		out = append(out, pm)
		r.lastPhase = ph
		r.phaseSince = r.app.Clock.Now()
	}
	out = append(out, r.emitTeacherUtterancesLocked()...)
	if r.machine.Phase() == classroom.PhaseJudging {
		out = append(out, r.judgeLocked()...)
	}
	if r.machine.Phase() == classroom.PhaseLessonEnd {
		out = append(out, r.maybeFinishLocked()...)
	}
	r.app.saveLessonState(r.lessonID, r.machine.Snapshot())
	return out
}

// emitTeacherUtterancesLocked 老师话术层（D-10 快路径伴生）：
// 由本步动作推导事件 → 决策表选槽 → seed=lessonId 渲染 → say 帧 + 日志。
func (r *Room) emitTeacherUtterancesLocked() []Msg {
	var out []Msg
	ctx, err := r.teacherTaskLocked()
	if err != nil {
		r.app.Log.Error("agent", "agent_action_failed", map[string]any{"lesson_id": r.lessonID, "error": err.Error()})
		return nil
	}
	for _, action := range r.lastActions {
		event := ""
		switch action.Type {
		case classroom.ActStartQuiz:
			event = "start_quiz"
		case classroom.ActShowCross:
			event = "judged_wrong"
		case classroom.ActShowCheck:
			r.encourageCounter++
			event = "judged_correct"
		case classroom.ActNudgeRepeat:
			event = "repeat_timeout"
		case classroom.ActNudgeAnswer:
			event = "answer_timeout"
		case classroom.ActShowWord:
			if r.machine.Snapshot().WordIdx > 0 {
				event = "word_done"
			}
		}
		if event != "" {
			out = append(out, r.teacherUtteranceLocked(ctx, r.teacherInputLocked(event, action.WordID))...)
		}
	}
	return out
}

func (r *Room) reteachCount(wordID int64) int {
	return r.machine.Snapshot().ReteachN[wordID]
}

func (r *Room) studentName() string {
	if u, err := r.app.Auth.GetUserByID(r.studentID); err == nil && u.Nickname != "" {
		return u.Nickname
	}
	return "同学"
}

func firstMeaning(w classroom.Word) string {
	if len(w.Meanings) > 0 {
		return w.Meanings[0]
	}
	return ""
}

func (r *Room) emitLocked(m Msg) {
	m["context"] = r.contextLocked()
	for c := range r.clients {
		_ = c.Send(m)
	}
}

// judgeLocked 判分：judge L1(+L2) → OnJudged → 后续动作广播（§14 判分日志）。
func (r *Room) judgeLocked() []Msg {
	var out []Msg
	it := r.machine.CurrentItem()
	w := r.words[it.WordID]
	j := r.app.Judge.Judge(w.Word, w.Meanings, r.lastAnswer)
	r.app.Log.Info("judge", "quiz_judged", map[string]any{
		"lesson_id": r.lessonID, "word": w.Word, "word_id": it.WordID,
		"kind": it.Kind, "round": it.Round,
		"answer_length": len([]rune(r.lastAnswer)), "judgment": string(j)})
	jm := Msg{"type": "judge_result", "seq": r.nextSeqLocked(),
		"quiz_item": itemPayload(it), "judgment": string(j), "asr_text": r.lastAnswer}
	r.emitLocked(jm)
	out = append(out, jm)
	_ = r.machine.OnJudged(it.ID(), classroom.Judgment(j))
	out = append(out, r.broadcastActionsOnly()...)
	return out
}

// broadcastActionsOnly 只排空动作不再触发判分（防递归）；同样喂话术层与相位帧。
func (r *Room) broadcastActionsOnly() []Msg {
	var out []Msg
	r.lastActions = r.machine.DrainActions()
	for _, a := range r.lastActions {
		if a.Type == classroom.ActLessonSummary {
			continue
		}
		m := r.actionMsg(a)
		if a.Type == "play_tts" {
			fmt.Printf("[hub] emit play_tts lesson=%d seg=%d lang=%q word=%q\n",
				r.lessonID, a.SegIdx, a.Lang, a.Word)
		}
		r.emitLocked(m)
		out = append(out, m)
	}
	if ph := r.machine.Phase(); ph != r.lastPhase {
		r.app.Log.Info("classroom", "phase_changed", map[string]any{
			"lesson_id": r.lessonID, "from": r.lastPhase, "to": ph})
		pm := Msg{"type": "phase_changed", "seq": r.nextSeqLocked(),
			"from": r.lastPhase, "phase": ph, "lesson_id": r.lessonID}
		r.emitLocked(pm)
		out = append(out, pm)
		r.lastPhase = ph
		r.phaseSince = r.app.Clock.Now()
	}
	out = append(out, r.emitTeacherUtterancesLocked()...)
	if r.machine.Phase() == classroom.PhaseLessonEnd {
		out = append(out, r.maybeFinishLocked()...)
	}
	r.app.saveLessonState(r.lessonID, r.machine.Snapshot())
	return out
}

// HandleEvent 学生事件入口；返回本步产生的全部帧（REST/WS 双通道）。
func (r *Room) HandleEvent(ev classroom.Event) []Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handleEventLocked(ev)
}
func (r *Room) handleEventLocked(ev classroom.Event) []Msg {
	r.activateDeliveryLocked()
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
	if r.machine.Phase() == classroom.PhaseLessonEnd && !r.finished {
		r.maybeFinishLocked()
		return
	}
	if r.machine.Paused() || r.finished {
		return
	}
	if !r.deliveryReady {
		return
	}
	d, ok := r.machine.TimeoutFor()
	if !ok {
		return
	}
	// Local audio includes server-side synthesis/transcription before playback or
	// an answer can arrive. Keep that service budget separate from speaking time.
	if r.app.Local != nil {
		switch r.machine.Phase() {
		case classroom.PhaseTTSSeg, classroom.PhaseReteachShow:
			if r.app.Local.Has("tts", "tts") {
				d += 20 * time.Second
			}
		case classroom.PhaseRepeating, classroom.PhaseWaitAnswer:
			if r.app.Local.Has("asr", "asr") {
				d += 24 * time.Second
			}
		}
	}
	if r.app.Clock.Now().Sub(r.phaseSince) >= d {
		_ = r.machine.OnTimeout(r.machine.Phase())
		r.broadcastLocked()
	}
}

// HandleVoiceText 语音转写：指令优先→跟读判读→反馈捕获→作答（02 §12.1）。
// 返回 (指令类型, 产生的帧)。
func (r *Room) HandleVoiceText(text string) (string, []Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handleVoiceLocked(text)
}
func (r *Room) handleVoiceLocked(text string) (string, []Msg) {
	r.activateDeliveryLocked()
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
		// 跟读相位不放行指令（除跳过）：转写交给下方"听读判读"——
		// 用户定案 2026-08-25：老师听学生读完即下一词，不靠口令/按钮。
		if ph != classroom.PhaseRepeating {
			if !quiz || r.voiceParser.AllowedInQuiz(cmd) {
				return cmd, r.applyCommand(cmd)
			}
		} else if cmd == voicecmd.CmdSkip {
			return cmd, r.applyCommand(cmd)
		}
	}
	// 跟读判读：转写匹配当前词（判分器 L1 归一化）→ 老师确认听到，进下一词
	if ph == classroom.PhaseRepeating {
		w := r.machine.CurrentWord()
		if st := r.machine.Snapshot(); st.InReteach {
			if it := r.machine.CurrentItem(); it.WordID != 0 {
				w = r.words[it.WordID]
			}
		}
		if j := r.app.Judge.Judge(w.Word, w.Meanings, text); j == judge.JudgmentCorrect || j == judge.JudgmentNear {
			r.app.Log.Info("classroom", "repeat_verified", map[string]any{
				"lesson_id": r.lessonID, "word": w.Word, "answer_length": len([]rune(text))})
			return "repeat_read", r.handleEventLocked(classroom.Event{Type: classroom.EvRepeatDone})
		}
		return "", nil // 没读对：继续听（超时兜底推进）
	}
	return "", r.handleEventLocked(classroom.Event{Type: classroom.EvAnswerSubmitted, Text: text})
}

func (r *Room) applyCommand(cmd string) []Msg {
	var ev classroom.Event
	switch cmd {
	case voicecmd.CmdNext:
		ev = classroom.Event{Type: classroom.EvWordClicked}
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
	return r.handleEventLocked(ev)
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
func (r *Room) maybeFinishLocked() []Msg {
	if r.finished || r.machine.Phase() != classroom.PhaseLessonEnd {
		return nil
	}
	res := r.machine.Result()
	if res == nil {
		return nil
	}
	if err := r.app.commitLesson(r.lessonID, r.studentID, r.bookID, res, r.machine.Snapshot()); err != nil {
		r.app.Log.Error("scheduler", "settle_failed", map[string]any{"lesson_id": r.lessonID, "error": err.Error()})
		m := Msg{"type": "settlement_failed", "lesson_id": r.lessonID, "retryable": true}
		r.emitLocked(m)
		return []Msg{m}
	}
	r.finished = true
	r.lessonStatus = "finished"
	m := Msg{"type": "lesson_summary", "seq": r.nextSeqLocked(), "report": res.Report, "correct": res.Correct}
	r.emitLocked(m)
	r.app.RevokeTicket(r.lessonID, r.studentID)
	return append([]Msg{m}, r.teacherLifecycleLocked("lesson_end")...)
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

// Package api 装配全部领域服务（内存实现版；GORM 版后补），
// 提供 REST 路由与课堂 WS hub。装配关系对应 SDD §1.1/§2。
package api

import (
	"sync"
	"time"

	"enwords/internal/agent"
	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/feedback"
	"enwords/internal/judge"
	"enwords/internal/memory"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/scripts"
	"enwords/internal/tools"
	"enwords/internal/wordbook"
)

// Clock 可注入时钟（测试快进；生产 RealClock）。
type Clock interface {
	Now() time.Time
}

// App 全量装配。
type App struct {
	Auth    *auth.Service
	Books   *wordbook.Service
	Cfg     *config.Store
	Sched   *scheduler.Service
	Judge   *judge.Judge
	FB      *feedback.Parser
	Mem     *memory.Service
	Bank    *scripts.Bank
	Teacher *agent.TeacherAgent
	System  *agent.SystemAgent
	Tools   *tools.Registry
	ToolAudit *tools.MemAuditor
	Log     *obs.Logger
	Clock   Clock

	Rooms   *Hub
	tickets map[string]*Ticket // ticket → 绑定
	reports map[int64]*classroom.Result        // lessonID → 课报
	lessonStates map[int64]*classroom.State    // lessonID → 课堂快照（IT-12）
	notifications []Notification
	nextNotifID int64
	feedbackRecs []FeedbackRecord
	nextFeedbackID int64
	mu      sync.Mutex
}

// Ticket 课堂连接票据（AR-01/R2：绑定 lesson+student，课堂生命周期内可复用）。
type Ticket struct {
	LessonID  int64
	StudentID int64
	CreatedAt time.Time
}

// NewApp 装配（默认真实时钟；测试传 FakeClock）。
func NewApp(clock Clock) *App {
	if clock == nil {
		clock = scheduler.RealClock{}
	}
	cfgStore, _ := config.NewStore(nil, nil)
	sched := scheduler.NewService(scheduler.NewMemRepo(), cfgStore, clock)
	bank := scripts.NewBank()
	bank.SeedAll()
	mem := memory.NewService(memory.NewMemRepo())
	fb := feedback.NewParser(clock.Now)
	authSvc := auth.NewService("enwords-dev-secret", 7*24*time.Hour)
	authSvc.EnsureAgentAccounts()
	toolAudit := &tools.MemAuditor{}
	app := &App{
		Auth:      authSvc,
		Books:     wordbook.NewService(wordbook.NewMemRepo()),
		Cfg:       cfgStore,
		Sched:     sched,
		Judge:     judge.New(judge.DefaultSynonyms()),
		FB:        fb,
		Mem:       mem,
		Bank:      bank,
		Teacher:   agent.NewTeacherAgent(bank),
		System:    agent.NewSystemAgent(bank, mem, fb),
		Tools:     tools.New(toolAudit),
		ToolAudit: toolAudit,
		Log:       obs.Nop(),
		Clock:     clock,
		tickets:   map[string]*Ticket{},
	}
	app.Rooms = NewHub(app)
	app.registerTools()
	return app
}

// ---- 学生-词库桥接（FR-C）----

// EnableBook 选库：为该生该库建立记忆卡（未摸底）。
func (a *App) EnableBook(studentID, bookID int64) error {
	b, err := a.Books.GetBook(bookID)
	if err != nil {
		return err
	}
	if b.Status != wordbook.StatusPublished {
		return errBookNotPublished
	}
	entries, _, err := a.Books.ListEntries(bookID, 1, int(b.WordCount)+1)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := a.Sched.SeedCard(studentID, bookID, e.ID); err != nil {
			return err
		}
	}
	return nil
}

// PlacementSubmit 摸底提交（known=已会词 entryID；finish=true 触发首课）。
func (a *App) PlacementSubmit(studentID, bookID int64, known []int64, finish bool) error {
	if err := a.Sched.MarkPlacement(studentID, bookID, known); err != nil {
		return err
	}
	if finish {
		_, err := a.Sched.PlanNext(studentID, bookID)
		return err
	}
	return nil
}

// SetLogger 注入生产日志器（main/测试用；默认 Nop）。
func (a *App) SetLogger(l *obs.Logger) {
	if l != nil {
		a.Log = l
	}
}

// CronOverdue 定时巡检（S-10/S-11）：scheduler 重排 + 逾期通知学生。
func (a *App) CronOverdue() ([]int64, error) {
	replanned, err := a.Sched.HandleOverdue()
	if err != nil {
		return nil, err
	}
	for _, id := range replanned {
		l, err := a.Sched.Lesson(id)
		if err != nil {
			continue
		}
		old, _ := a.Sched.Lessons(l.StudentID, l.BookID)
		var origAt string
		for _, o := range old {
			if o.ID != id && o.Status == "expired" {
				origAt = o.OpenAt.Format("1月2日")
			}
		}
		a.Notify(l.StudentID, "课程已改期",
			"你缺席了 "+origAt+" 的课，已改到 "+l.OpenAt.Format("1月2日 15:04")+"，别再错过啦")
	}
	return replanned, nil
}

// ---- Ticket（AR-01/R2-AR-03）----

// IssueTicket enter 签发（同 lesson 复用同一 ticket）。
func (a *App) IssueTicket(lessonID, studentID int64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for t, tk := range a.tickets {
		if tk.LessonID == lessonID && tk.StudentID == studentID {
			return t
		}
	}
	t := newToken()
	a.tickets[t] = &Ticket{LessonID: lessonID, StudentID: studentID, CreatedAt: a.Clock.Now()}
	return t
}

// CheckTicket 校验并返回绑定。
func (a *App) CheckTicket(t string) (*Ticket, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	tk, ok := a.tickets[t]
	return tk, ok
}

// RevokeTicket 课堂结束销毁。
func (a *App) RevokeTicket(lessonID, studentID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for t, tk := range a.tickets {
		if tk.LessonID == lessonID && tk.StudentID == studentID {
			delete(a.tickets, t)
		}
	}
}

// ---- 课报存档（D-13，课程详情端点用）----

// LessonReportView 课程详情返回结构。
type LessonReportView struct {
	LessonID int64              `json:"lesson_id"`
	Report   classroom.LessonReport `json:"report"`
	Correct  map[int64]bool     `json:"correct"`
}

func (a *App) saveReport(lessonID int64, res *classroom.Result) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reports == nil {
		a.reports = map[int64]*classroom.Result{}
	}
	a.reports[lessonID] = res
}

// Report 取课报。
func (a *App) Report(lessonID int64) (*classroom.Result, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.reports[lessonID]
	return r, ok
}

// ---- 课堂状态持久化（IT-12：房间丢失后恢复）----

func (a *App) saveLessonState(lessonID int64, st classroom.State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lessonStates == nil {
		a.lessonStates = map[int64]*classroom.State{}
	}
	cp := st
	a.lessonStates[lessonID] = &cp
}

func (a *App) lessonState(lessonID int64) *classroom.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lessonStates[lessonID]
}

// ---- 通知中心（G-6/SN 系列）----

// Notification 通知。
type Notification struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Read      bool   `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// Notify 推送通知。
func (a *App) Notify(userID int64, title, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextNotifID++
	a.notifications = append(a.notifications, Notification{
		ID: a.nextNotifID, UserID: userID, Type: "info",
		Title: title, Body: body, CreatedAt: a.Clock.Now(),
	})
}

// Notifications 用户通知列表。
func (a *App) Notifications(userID int64) []Notification {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Notification
	for _, n := range a.notifications {
		if n.UserID == userID {
			out = append(out, n)
		}
	}
	return out
}

// ---- 工具注册（memory 域 D-16 演示 + config.set 管理通道）----

func (a *App) registerTools() {
	// memory.append（owner 由 caller 角色决定）
	_ = a.Tools.Register(tools.Tool{
		Name: "memory.append", Domain: "memory", Risk: tools.RiskMid,
		Scope: []tools.Role{tools.RoleTeacherAgent, tools.RoleSystemAgent},
		InputSchema: mustSchema(`{"type":"object","properties":{
			"scope":{"type":"string"},"student_id":{"type":"integer"},"book_id":{"type":"integer"},
			"kind":{"type":"string"},"content":{"type":"string"},"importance":{"type":"integer"},
			"source_lesson_id":{"type":"integer"}},"required":["scope","student_id","kind","content"]}`),
		Handler: func(ctx tools.Ctx, caller tools.Caller, args tools.Args) (any, error) {
			var in struct {
				Scope         string `json:"scope"`
				StudentID     int64  `json:"student_id"`
				BookID        int64  `json:"book_id"`
				Kind          string `json:"kind"`
				Content       string `json:"content"`
				Importance    int    `json:"importance"`
				SourceLesson  int64  `json:"source_lesson_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			owner := memory.OwnerTeacher
			if caller.Role == tools.RoleSystemAgent {
				owner = memory.OwnerSystem
			}
			return a.Mem.Append(owner, memory.Scope(in.Scope), in.StudentID, in.BookID,
				in.Kind, in.Content, in.Importance, in.SourceLesson)
		},
	})
	// config.set（high 风险 reason 双保险演示；IT-10 越权用）
	_ = a.Tools.Register(tools.Tool{
		Name: "config.set", Domain: "config", Risk: tools.RiskHigh,
		Scope: []tools.Role{tools.RoleSystemAgent, tools.RoleAdmin},
		InputSchema: mustSchema(`{"type":"object","properties":{
			"key":{"type":"string"},"value":{},"reason":{"type":"string"}},
			"required":["key","value","reason"]}`),
		Handler: func(ctx tools.Ctx, caller tools.Caller, args tools.Args) (any, error) {
			var in struct {
				Key    string `json:"key"`
				Value  any    `json:"value"`
				Reason string `json:"reason"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			who := string(caller.Role)
			if err := a.Cfg.SetGlobal(who, in.Key, in.Value, in.Reason); err != nil {
				return nil, err
			}
			return "ok", nil
		},
	})
}

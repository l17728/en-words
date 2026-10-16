// Package api 装配全部领域服务（内存实现版；GORM 版后补），
// 提供 REST 路由与课堂 WS hub。装配关系对应 SDD §1.1/§2。
package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"enwords/internal/agent"
	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/feedback"
	"enwords/internal/judge"
	"enwords/internal/llmzhipu"
	"enwords/internal/localmodel"
	"enwords/internal/memory"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/scripts"
	"enwords/internal/store"
	"enwords/internal/tools"
	"enwords/internal/wordbook"

	"gorm.io/gorm"
)

// Clock 可注入时钟（测试快进；生产 RealClock）。
type Clock interface {
	Now() time.Time
}

// App 全量装配。
type App struct {
	agentWriteMu      sync.Mutex
	completionMu      sync.Mutex
	feedbackState     *feedbackMemoryState
	overdueTasks      map[string]store.OverdueTask
	completionTargets map[int64]int
	completionTasks   map[int64]*lessonCompletion
	StartupError      error
	Local             *localmodel.Client
	DuplexPreflight   bool // Explicit isolated device preflight only; never enables teaching.
	Auth              *auth.Service
	Books             *wordbook.Service
	Cfg               *config.Store
	Sched             *scheduler.Service
	Judge             *judge.Judge
	FB                *feedback.Parser
	Mem               *memory.Service
	Bank              *scripts.Bank
	Teacher           *agent.TeacherAgent
	System            *agent.SystemAgent
	Tools             *tools.Registry
	ToolAudit         *tools.MemAuditor
	Log               *obs.Logger
	Clock             Clock

	Rooms          *Hub
	tickets        map[string]*Ticket          // ticket → 绑定
	reports        map[int64]*classroom.Result // lessonID → 课报
	lessonStates   map[int64]*classroom.State  // lessonID → 课堂快照（IT-12）
	notifications  []Notification
	nextNotifID    int64
	feedbackRecs   []FeedbackRecord
	nextFeedbackID int64
	TestMode       bool     // -testmode：跳过课程开放时间校验（E2E 用）
	DB             *gorm.DB // SQLite 持久化句柄（nil=纯内存模式）
	mu             sync.Mutex
}

// SetTestMode 开启测试模式。
func (a *App) SetTestMode() { a.TestMode = true }

// persistUser 持久化模式落库（router/extra 共用；nil DB 时 no-op）。
func (a *App) persistUser(username string) {
	if a.DB == nil {
		return
	}
	if err := store.NewAuthStore(a.DB).SaveUser(a.Auth, username); err != nil {
		a.Log.Warn("store", "user_save_failed", map[string]any{
			"username": username, "error": err.Error()})
	}
}

// EnableRealProviders 接入真实大模型 Provider（智谱 GLM：判分 L2 + 反馈解析；
// 规则 L1 保留为兜底，LLM 失败自动降级——课堂永不因外部 API 阻塞）。
// 密钥从 secrets/dashscope.local.yaml 的 zhipu: 段读取（严禁入库出日志）。
func (a *App) EnableRealProviders() error {
	key, baseURL, model, err := loadZhipuSecrets()
	if err != nil {
		return err
	}
	zc := llmzhipu.New(key, baseURL, model)
	a.Judge.LLM = judge.NewZhipu(zc)         // L2 真实判分
	a.FB.SetLLM(feedback.NewZhipuParser(zc)) // LLM 意图解析
	a.Log.Info("providers", "real_providers_enabled", map[string]any{
		"llm": model, "judge": "zhipu_l2", "feedback": "zhipu_llm"})
	return nil
}

// loadZhipuSecrets 读 secrets 的 zhipu 段（多段 yaml 手工切分，不引依赖）。
func loadZhipuSecrets() (key, baseURL, model string, err error) {
	for _, p := range []string{
		filepath.Join("secrets", "dashscope.local.yaml"),
		filepath.Join("..", "secrets", "dashscope.local.yaml"),
		filepath.Join("..", "..", "secrets", "dashscope.local.yaml"),
	} {
		raw, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		c := string(raw)
		i := strings.Index(c, "zhipu:")
		if i < 0 {
			continue
		}
		seg := c[i:]
		if m := regexpZhipuKey.FindStringSubmatch(seg); m != nil && m[1] != "" {
			key = m[1]
		}
		if m := regexpZhipuURL.FindStringSubmatch(seg); m != nil && m[1] != "" {
			baseURL = m[1]
		}
		if m := regexpZhipuModel.FindStringSubmatch(seg); m != nil && m[1] != "" {
			model = m[1]
		}
		if key != "" && baseURL != "" {
			if model == "" {
				model = "glm-4-flash"
			}
			return key, baseURL, model, nil
		}
	}
	return "", "", "", fmt.Errorf("secrets zhipu 段未配置")
}

var (
	regexpZhipuKey   = regexp.MustCompile(`api_key:\s*"([^"]+)"`)
	regexpZhipuURL   = regexp.MustCompile(`base_url:\s*"([^"]+)"`)
	regexpZhipuModel = regexp.MustCompile(`model:\s*"([^"]+)"`)
)

// Ticket 课堂连接票据（AR-01/R2：绑定 lesson+student，课堂生命周期内可复用）。
type Ticket struct {
	LessonID  int64
	StudentID int64
	CreatedAt time.Time
}

// NewApp 装配（默认真实时钟；测试传 FakeClock）。
// db 非 nil 时启用 SQLite 持久化（wordbook/scheduler/auth + 通知/反馈/课报/快照落库）。
func NewApp(clock Clock, db *gorm.DB) *App {
	if clock == nil {
		clock = scheduler.RealClock{}
	}
	var bookRepo wordbook.Repo = wordbook.NewMemRepo()
	var schedRepo scheduler.Repo = scheduler.NewMemRepo()
	if db != nil {
		bookRepo = store.NewWordbookRepo(db)
		schedRepo = store.NewSchedulerRepo(db)
	}
	var cfgPersist config.Persister
	var memRepo memory.Repo = memory.NewMemRepo()
	if db != nil {
		cfgPersist = store.NewConfigPersister(db)
		memRepo = store.NewMemoryRepo(db)
	}
	cfgStore, cfgErr := config.NewStore(cfgPersist, nil)
	if cfgErr != nil {
		panic(fmt.Sprintf("load configuration: %v", cfgErr))
	}
	sched := scheduler.NewService(schedRepo, cfgStore, clock)
	bank := scripts.NewBank()
	bank.SeedAll()
	mem := memory.NewService(memRepo)
	fb := feedback.NewParser(clock.Now)
	authSvc := auth.NewService("enwords-dev-secret", 7*24*time.Hour)
	restoredUsers := 0
	if db != nil {
		err := db.Transaction(func(tx *gorm.DB) error {
			// Serialize concurrent initializers before reading the persisted IDs.
			if err := tx.Exec("UPDATE users SET id=id WHERE 1=0").Error; err != nil {
				return err
			}
			p := store.NewAuthStore(tx)
			n, err := p.LoadAll(authSvc)
			if err != nil {
				return err
			}
			restoredUsers = n
			missing := []string{}
			for _, name := range []string{"teacher-agent", "system-agent"} {
				if !authSvc.HasUser(name) {
					missing = append(missing, name)
				}
			}
			authSvc.EnsureAgentAccounts()
			for _, name := range missing {
				if err := p.SaveUser(authSvc, name); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			panic("load or initialize persisted identities failed")
		}
	} else {
		authSvc.EnsureAgentAccounts()
	}
	toolAudit := &tools.MemAuditor{}
	app := &App{
		Auth:      authSvc,
		Books:     wordbook.NewService(bookRepo),
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
	app.Judge.OnDecision = func(ctx context.Context, decision judge.Decision) {
		app.Log.InfoCtx(ctx, "judge", "judge_decision", map[string]any{
			"source": decision.Source, "reason": decision.Reason, "result": decision.Result,
		})
	}
	if restoredUsers > 0 {
		app.Log.Info("store", "users_restored", map[string]any{"count": restoredUsers})
	}
	// §14 排课日志钩子（logger 在 SetLogger 注入后生效；这里闭包引用 app.Log）
	sched.OnPlanned = func(studentID, bookID int64, l *scheduler.Lesson) {
		app.Log.Info("scheduler", "lesson_planned", map[string]any{
			"student_id": studentID, "book_id": bookID,
			"lesson_id": l.ID, "open_at": l.OpenAt.Format(time.RFC3339),
			"batch": len(l.BatchWordIDs)})
	}
	sched.OnPlacementSaved = func(ctx context.Context, studentID, bookID int64, inputCount, changed int) {
		app.Log.InfoCtx(ctx, "scheduler", "placement_saved", map[string]any{"student_id": studentID, "book_id": bookID, "input_count": inputCount, "changed_count": changed})
	}
	// R4-backend#3：config.ladder 变更 → scheduler 截断记忆卡 step（SDD §6.2 事务联动）
	cfgStore.OnLadderChanged = func(newLadder []int) error {
		return sched.TruncateLadderSteps(len(newLadder))
	}
	app.Rooms = NewHub(app)
	app.registerTools()
	app.registerStudentTools()
	app.registerTeacherTools()
	app.DB = db
	app.restoreRuntime()
	if err := app.migrateLegacyFeedback(); err != nil {
		panic(fmt.Sprintf("migrate feedback: %v", err))
	}
	return app
}

// ---- 学生-词库桥接（FR-C）----

// MyBooks 某生已启用词库列表（Dashboard"我的词库"；enablement=记忆卡存在）。
func (a *App) MyBooks(studentID int64) []*wordbook.Book {
	ids, err := a.Sched.StudentBooks(studentID)
	if err != nil {
		a.Log.Warn("api", "mybooks_query_failed", map[string]any{
			"student_id": studentID, "err": err.Error()})
		return []*wordbook.Book{}
	}
	out := make([]*wordbook.Book, 0, len(ids))
	for _, id := range ids {
		b, err := a.Books.GetBook(id)
		if err != nil {
			continue // 词库已被管理员下架/删除：跳过不炸整页
		}
		out = append(out, b)
	}
	return out
}

// EnableBook 选库：为该生该库建立记忆卡（未摸底）。
func (a *App) EnableBook(studentID, bookID int64) error {
	return a.EnableBookContext(context.Background(), studentID, bookID)
}

func (a *App) EnableBookContext(ctx context.Context, studentID, bookID int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return a.Sched.SeedCards(ctx, studentID, bookID, ids)
}

// PlacementSubmit 摸底提交（known=已会词 entryID；finish=true 触发首课）。
// 用户定案（2026-08-23）：重新摸底完成 = 覆盖旧摸底结果并重排学习计划——
// 未开始的课作废+批次卡重置（ResetForReplacement），再以新摸底结果排新课。
// R6-sec：knownWordIds 逐个校验归属该词库（防跨库 ID 注入打乱记忆卡）。
func (a *App) PlacementSubmit(studentID, bookID int64, known []int64, finish bool) error {
	return a.PlacementSubmitContext(context.Background(), studentID, bookID, known, finish)
}

type placementPlanError struct{ cause error }

func (e *placementPlanError) Error() string { return "placement saved; plan incomplete" }
func (e *placementPlanError) Unwrap() error { return e.cause }

func (a *App) PlacementSubmitContext(ctx context.Context, studentID, bookID int64, known []int64, finish bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(known) > 0 {
		valid := map[int64]bool{}
		entries, _, err := a.Books.ListEntries(bookID, 1, 1<<30)
		if err != nil {
			return err
		}
		for _, e := range entries {
			valid[e.ID] = true
		}
		for _, id := range known {
			if !valid[id] {
				return fmt.Errorf("api: word entry %d not in book %d", id, bookID)
			}
		}
	}
	if err := a.Sched.MarkPlacementContext(ctx, studentID, bookID, known); err != nil {
		a.Log.ErrorCtx(ctx, "scheduler", "placement_save_failed", map[string]any{"student_id": studentID, "book_id": bookID, "reason": "transaction_failed"})
		return err
	}
	if finish {
		if err := a.Sched.ResetForReplacement(studentID, bookID); err != nil {
			a.Log.ErrorCtx(ctx, "scheduler", "placement_plan_failed", map[string]any{"student_id": studentID, "book_id": bookID, "draft_committed": true, "reason": "reset_failed"})
			return &placementPlanError{cause: err}
		}
		l, err := a.Sched.PlanNext(studentID, bookID)
		if err != nil {
			a.Log.ErrorCtx(ctx, "scheduler", "placement_plan_failed", map[string]any{"student_id": studentID, "book_id": bookID, "draft_committed": true, "reason": "plan_failed"})
			return &placementPlanError{cause: err}
		}
		a.openNowIfTestMode(l)
	}
	return nil
}

// openNowIfTestMode testmode 下把课拨到当前时刻（E2E：前端"还没到时间"禁用态自然放行）。
func (a *App) openNowIfTestMode(l *scheduler.Lesson) {
	if !a.TestMode || l == nil || l.Status != scheduler.LessonScheduledStr {
		return
	}
	l.OpenAt = a.Clock.Now()
	if err := a.Sched.SaveLesson(l); err != nil {
		a.Log.Error("scheduler", "test_open_failed", map[string]any{"lesson_id": l.ID})
		return
	}
	if err := a.Sched.ReserveCards(l.StudentID, l.BookID, l.BatchWordIDs, l.OpenAt); err != nil {
		a.Log.Error("scheduler", "test_reserve_failed", map[string]any{"lesson_id": l.ID})
	}
}

// SetLogger 注入生产日志器（main/测试用；默认 Nop）。
func (a *App) SetLogger(l *obs.Logger) {
	if l != nil {
		a.Log = l
		if a.FB != nil {
			a.FB.SetLogger(l)
		}
	}
}

// CronOverdue 定时巡检（S-10/S-11）：scheduler 重排 + 逾期通知学生（§14 逾期日志）。
func (a *App) CronOverdue() ([]int64, error) {
	before, err := a.Sched.AllLessons()
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	for _, l := range before {
		seen[l.ID] = true
	}
	if err := a.RunOverdue(context.Background()); err != nil {
		return nil, err
	}
	after, err := a.Sched.AllLessons()
	if err != nil {
		return nil, err
	}
	a.agentWriteMu.Lock()
	completed := map[int64]bool{}
	if a.DB != nil {
		var rows []store.OverdueTask
		err = a.DB.Where("status=?", "completed").Find(&rows).Error
		for _, row := range rows {
			completed[row.NewLessonID] = true
		}
	} else {
		for _, row := range a.overdueTasks {
			if row.Status == "completed" {
				completed[row.NewLessonID] = true
			}
		}
	}
	a.agentWriteMu.Unlock()
	if err != nil {
		return nil, err
	}
	replanned := []int64{}
	for _, l := range after {
		if !seen[l.ID] && completed[l.ID] {
			replanned = append(replanned, l.ID)
		}
	}
	return replanned, nil
}

// ---- Ticket（AR-01/R2-AR-03）----

// IssueTicket enter 签发（同 lesson 复用同一 ticket；R4-arch#3：含过期时间，判定时校验）。
func (a *App) IssueTicket(lessonID, studentID int64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for t, tk := range a.tickets {
		if tk.LessonID == lessonID && tk.StudentID == studentID && !tk.expired(a) {
			return t
		}
		if tk.LessonID == lessonID && tk.StudentID == studentID {
			delete(a.tickets, t) // 过期清理
		}
	}
	t := newToken()
	a.tickets[t] = &Ticket{LessonID: lessonID, StudentID: studentID, CreatedAt: a.Clock.Now()}
	return t
}

// expired ticket 过期判定：创建时间 + open_at 窗口 + overdueHours + 1h（R2-AR-03）。
func (tk *Ticket) expired(a *App) bool {
	lesson, err := a.Sched.Lesson(tk.LessonID)
	if err != nil {
		return true // 课不存在即失效
	}
	deadline := lesson.OpenAt.Add(time.Duration(a.Cfg.Int(tk.StudentID, "overdueHours")+1) * time.Hour)
	return a.Clock.Now().After(deadline)
}

// CheckTicket 校验并返回绑定（含过期检查，R4-arch#3）。
func (a *App) CheckTicket(t string) (*Ticket, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	tk, ok := a.tickets[t]
	if !ok {
		return nil, false
	}
	if tk.expired(a) {
		delete(a.tickets, t)
		return nil, false
	}
	return tk, true
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
	LessonID int64                  `json:"lesson_id"`
	Report   classroom.LessonReport `json:"report"`
	Correct  map[int64]bool         `json:"correct"`
}

func (a *App) saveReport(lessonID int64, res *classroom.Result) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.saveRuntimeLocked(fmt.Sprintf("report/%d", lessonID), res) {
		return
	}
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
	if !ok && a.DB != nil {
		var saved classroom.Result
		if a.readRuntime(fmt.Sprintf("report/%d", lessonID), &saved) {
			return &saved, true
		}
	}
	return r, ok
}

// ---- 课堂状态持久化（IT-12：房间丢失后恢复）----

func (a *App) saveLessonState(lessonID int64, st classroom.State) {
	a.saveLessonStateContext(context.Background(), lessonID, st)
}
func (a *App) saveLessonStateContext(ctx context.Context, lessonID int64, st classroom.State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.saveRuntimeContextLocked(ctx, fmt.Sprintf("snapshot/%d", lessonID), st) {
		return
	}
	if a.lessonStates == nil {
		a.lessonStates = map[int64]*classroom.State{}
	}
	cp := st
	a.lessonStates[lessonID] = &cp
}

func (a *App) lessonState(lessonID int64) *classroom.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.DB != nil {
		var saved classroom.State
		if a.readRuntime(fmt.Sprintf("snapshot/%d", lessonID), &saved) {
			return &saved
		}
	}
	return a.lessonStates[lessonID]
}

// ---- 通知中心（G-6/SN 系列）----

// Notification 通知。
type Notification struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Read      bool      `json:"read"`
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
	a.saveRuntimeLocked("notifications", a.notifications)
}

// Notifications 用户通知列表。
func (a *App) Notifications(userID int64) []Notification {
	out, _ := a.NotificationsContext(context.Background(), userID)
	return out
}

// ---- 工具注册（memory 域 D-16 演示 + config.set 管理通道）----

func (a *App) registerTools() {
	// config.set（high 风险 reason 双保险演示；IT-10 越权用）
	_ = a.Tools.Register(tools.Tool{
		Name: "config.set", Domain: "config", Risk: tools.RiskHigh,
		Scope: []tools.Role{tools.RoleAdmin},
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

// REST 路由（SDD §10）：认证/词库/学生/课堂/反馈/配置/Agent 工具。
package api

import (
	"net/http"
	"strconv"
	"strings"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/tools"
	"enwords/internal/wordbook"

	"github.com/gin-gonic/gin"
)

// Router 构建 gin engine。
func (a *App) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	v := r.Group("/api/v1")

	// ---- 首次启动向导（无 admin 时开放；SDD §17）----
	a.registerBootstrap(v)

	// ---- 认证 ----
	v.POST("/auth/login", func(c *gin.Context) {
		var body map[string]string
		_ = c.ShouldBindJSON(&body)
		u := body["username"]
		p := body["password"]
		tok, user, err := a.Auth.Login(u, p)
		if err != nil {
			a.Log.Warn("auth", "login_failed", map[string]any{"username": u})
			c.JSON(http.StatusOK, fail(errUnauthorized, "用户名或密码错误"))
			return
		}
		a.Log.Info("auth", "login_ok", map[string]any{
			"username": u, "role": string(user.Role), "user_id": user.ID})
		c.JSON(http.StatusOK, ok(gin.H{"token": tok, "role": string(user.Role),
			"nickname": user.Nickname, "user_id": user.ID}))
	})

	// ---- 学生/管理员共用（需登录）----
	authed := v.Group("", a.authMW())
	authed.POST("/auth/change_password", func(c *gin.Context) {
		var in struct{ OldPassword, NewPassword string }
		_ = c.ShouldBindJSON(&in)
		if err := a.Auth.ChangePassword(claimsOf(c).Username, in.OldPassword, in.NewPassword); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "原密码错误或新密码不合规"))
			return
		}
		a.Log.Info("auth", "password_changed", map[string]any{
			"username": claimsOf(c).Username})
		c.JSON(http.StatusOK, ok("changed"))
	})
	authed.GET("/me", func(c *gin.Context) {
		cl := claimsOf(c)
		u, err := a.Auth.GetUser(cl.Username)
		if err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, "user not found"))
			return
		}
		c.JSON(http.StatusOK, ok(gin.H{"username": u.Username, "nickname": u.Nickname,
			"role": string(u.Role), "user_id": u.ID}))
	})
	// 修改昵称（含编码防御）
	authed.POST("/me/nickname", func(c *gin.Context) {
		var in struct{ Nickname string `json:"nickname"` }
		if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Nickname) == "" {
			c.JSON(http.StatusOK, fail(errBadRequest, "nickname required"))
			return
		}
		if err := a.Auth.UpdateNickname(claimsOf(c).Username, in.Nickname); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		a.Log.Info("auth", "nickname_updated", map[string]any{
			"username": claimsOf(c).Username})
		c.JSON(http.StatusOK, ok("updated"))
	})

	// 词库：学生看已发布；管理员全量 CRUD
	authed.GET("/wordbooks", func(c *gin.Context) {
		cl := claimsOf(c)
		if cl.Role == auth.RoleAdmin {
			books, _ := a.Books.ListBooks()
			c.JSON(http.StatusOK, ok(books))
			return
		}
		books, _ := a.Books.ListPublished()
		c.JSON(http.StatusOK, ok(books))
	})
	adminOnly := authed.Group("/wordbooks", a.roleMW(auth.RoleAdmin))
	adminOnly.POST("", func(c *gin.Context) {
		var in struct{ Name, Grade, Description string }
		_ = c.ShouldBindJSON(&in)
		b, err := a.Books.CreateBook(in.Name, in.Grade, in.Description)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(b))
	})
	// 词库 PUT/DELETE（§10.2 完整 CRUD）
	adminOnly.PUT("/:id", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		b, err := a.Books.GetBook(id)
		if err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, "book not found"))
			return
		}
		var in struct{ Name, Grade, Description string }
		_ = c.ShouldBindJSON(&in)
		if in.Name != "" {
			b.Name = in.Name
		}
		if in.Grade != "" {
			b.Grade = in.Grade
		}
		b.Description = in.Description
		if err := a.Books.UpdateBook(b); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(b))
	})
	adminOnly.DELETE("/:id", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		if err := a.Books.DeleteBook(id); err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, "book not found"))
			return
		}
		c.JSON(http.StatusOK, ok("deleted"))
	})
	adminOnly.POST("/:id/import/commit", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		var in struct{ Entries []wordbook.EntryInput }
		_ = c.ShouldBindJSON(&in)
		res, err := a.Books.ImportBatch(id, in.Entries)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(res))
	})
	adminOnly.POST("/:id/publish", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		if err := a.Books.Publish(id); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok("published"))
	})
	// 词条 CRUD/导入解析/导出（FR-B2/B3/B6，总验收门补齐）
	a.registerEntryRoutes(adminOnly.Group(""))
	authed.GET("/wordbooks/:id/words", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		if _, err := a.Books.GetBook(id); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "book not found"))
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("size", "100"))
		es, total, err := a.Books.ListEntries(id, page, size)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(gin.H{"entries": es, "total": total}))
	})

	// ---- 学生学习 ----
	student := authed.Group("/student", a.roleMW(auth.RoleStudent))
	student.POST("/books/:bookId/enable", func(c *gin.Context) {
		bid, _ := strconv.ParseInt(c.Param("bookId"), 10, 64)
		if err := a.EnableBook(claimsOf(c).UID, bid); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok("enabled"))
	})
	student.GET("/books/:bookId/placement", func(c *gin.Context) {
		bid, _ := strconv.ParseInt(c.Param("bookId"), 10, 64)
		es, _, err := a.Books.ListEntries(bid, 1, 10000)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		// 摸底仅英文（C2）+ 已保存草稿勾选（FR-C4 分次完成：不清空可续选）
		type w struct {
			ID int64  `json:"id"`
			W  string `json:"word"`
		}
		out := make([]w, 0, len(es))
		for _, e := range es {
			out = append(out, w{ID: e.ID, W: e.Word})
		}
		draft := a.Sched.PlacementDraft(claimsOf(c).UID, bid)
		if draft == nil {
			draft = []int64{}
		}
		c.JSON(http.StatusOK, ok(gin.H{"words": out, "knownWordIds": draft}))
	})
	student.POST("/books/:bookId/placement", func(c *gin.Context) {
		bid, _ := strconv.ParseInt(c.Param("bookId"), 10, 64)
		var in struct {
			KnownWordIDs []int64 `json:"knownWordIds"`
			Finish       bool    `json:"finish"`
		}
		_ = c.ShouldBindJSON(&in)
		if err := a.PlacementSubmit(claimsOf(c).UID, bid, in.KnownWordIDs, in.Finish); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(gin.H{"finished": in.Finish}))
	})
	student.GET("/lessons", func(c *gin.Context) {
		bid, _ := strconv.ParseInt(c.Query("bookId"), 10, 64)
		ls, err := a.Sched.Lessons(claimsOf(c).UID, bid)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(ls))
	})
	student.GET("/lessons/:id/report", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		res, okk := a.Report(id)
		if !okk {
			c.JSON(http.StatusOK, fail(errNotFound, "report not found"))
			return
		}
		c.JSON(http.StatusOK, ok(LessonReportView{LessonID: id, Report: res.Report, Correct: res.Correct}))
	})

	student.GET("/notifications", func(c *gin.Context) {
		c.JSON(http.StatusOK, ok(a.Notifications(claimsOf(c).UID)))
	})
	student.POST("/notifications/read", func(c *gin.Context) {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i := range a.notifications {
			if a.notifications[i].UserID == claimsOf(c).UID {
				a.notifications[i].Read = true
			}
		}
		c.JSON(http.StatusOK, ok("read"))
	})

	// ---- 反馈（学生；文字/语音同管线 G-2）----
	fb := authed.Group("/feedbacks", a.roleMW(auth.RoleStudent))
	fb.POST("", func(c *gin.Context) {
		var in struct{ Text, Channel string }
		_ = c.ShouldBindJSON(&in)
		if strings.TrimSpace(in.Text) == "" {
			c.JSON(http.StatusOK, fail(errBadRequest, "text required"))
			return
		}
		c.JSON(http.StatusOK, ok(a.HandleFeedback(claimsOf(c).UID, in.Text)))
	})
	fb.GET("/mine", func(c *gin.Context) {
		c.JSON(http.StatusOK, ok(a.Feedbacks(claimsOf(c).UID)))
	})

	// ---- 课堂（学生）----
	cr := authed.Group("/classroom", a.roleMW(auth.RoleStudent))
	cr.POST("/lessons/:id/enter", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		// 开放时间校验（D1；-testmode 跳过：E2E 可立即上课）
		lesson, err := a.Sched.Lesson(id)
		if err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, "lesson not found"))
			return
		}
		if !a.TestMode && a.Clock.Now().Before(lesson.OpenAt) {
			c.JSON(http.StatusOK, fail(errForbidden, "lesson not open yet"))
			return
		}
		room, snap, err := a.Rooms.Enter(id, claimsOf(c).UID)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		ticket := a.IssueTicket(id, claimsOf(c).UID)
		c.JSON(http.StatusOK, ok(gin.H{"ticket": ticket, "snapshot": snap, "status": room.Status()}))
	})
	// REST 事件通道（WS 不可用/测试/文本兜底；与 WS 同一 Room 管线）
	// R4-sec#6：event 白名单校验（防任意字符串触发未定义行为）
	cr.POST("/lessons/:id/event", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		room, okk := a.Rooms.Room(id)
		if !okk {
			c.JSON(http.StatusOK, fail(errNotFound, "room not found"))
			return
		}
		var in struct {
			Event string `json:"event"`
			Text  string `json:"text"`
		}
		_ = c.ShouldBindJSON(&in)
		if !validClassroomEvent(in.Event) {
			c.JSON(http.StatusOK, fail(errBadRequest, "unknown event"))
			return
		}
		msgs := room.HandleEvent(classroom.Event{Type: in.Event, Text: sanitizeText(in.Text)})
		c.JSON(http.StatusOK, ok(msgs))
	})
	cr.POST("/lessons/:id/voice", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		var in struct{ Text string }
		_ = c.ShouldBindJSON(&in)
		room, okk := a.Rooms.Room(id)
		if !okk {
			c.JSON(http.StatusOK, fail(errNotFound, "room not found"))
			return
		}
		// R4-sec#3：长度限制+控制字符清洗（防注入下游管线）
		in.Text = sanitizeText(in.Text)
		if in.Text == "" {
			c.JSON(http.StatusOK, fail(errBadRequest, "empty text"))
			return
		}
		cmd, msgs := room.HandleVoiceText(in.Text)
		out := gin.H{"command": cmd, "frames": msgs}
		if cmd == "feedback" { // 语音反馈闭环（与 WS 同管线）
			select {
			case fb := <-room.FeedbackCh:
				rec := a.HandleFeedback(claimsOf(c).UID, fb)
				out["feedback"] = rec
			default:
			}
		}
		c.JSON(http.StatusOK, ok(out))
	})
	cr.POST("/lessons/:id/resume", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		room, okk := a.Rooms.Room(id)
		if !okk {
			c.JSON(http.StatusOK, fail(errNotFound, "room not found"))
			return
		}
		c.JSON(http.StatusOK, ok(room.Resume()))
	})

	// ---- 配置（admin；Agent 走工具）----
	cfgg := authed.Group("/config", a.roleMW(auth.RoleAdmin))
	cfgg.GET("", func(c *gin.Context) {
		out := gin.H{}
		for _, k := range []string{"wordsPerLesson", "groupSize", "ttsEnRepeat", "repeatTimes",
			"quizOldWords", "overdueHours", "goalMode", "ladder", "sessionTimeoutMin"} {
			switch k {
			case "goalMode":
				out[k] = a.Cfg.String(0, k)
			case "ladder":
				out[k] = a.Cfg.Ladder(0)
			default:
				out[k] = a.Cfg.Int(0, k)
			}
		}
		c.JSON(http.StatusOK, ok(out))
	})
	cfgg.POST("/set", func(c *gin.Context) {
		var in struct {
			Scope     string `json:"scope"`
			StudentID int64  `json:"studentId"`
			Key       string `json:"key"`
			Value     any    `json:"value"`
			Reason    string `json:"reason"`
		}
		_ = c.ShouldBindJSON(&in)
		who := claimsOf(c).Username
		var err error
		if in.Scope == "student" {
			err = a.Cfg.SetStudent(who, in.StudentID, in.Key, in.Value, in.Reason)
		} else {
			err = a.Cfg.SetGlobal(who, in.Key, in.Value, in.Reason)
		}
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		a.Log.Info("config", "config_set", map[string]any{
			"actor": who, "scope": in.Scope, "student_id": in.StudentID,
			"key": in.Key, "value": in.Value})
		c.JSON(http.StatusOK, ok("set"))
	})
	// 覆盖删除（BE-08，总验收门补齐）
	a.registerConfigRoutes(cfgg.Group(""))

	// ---- 管理员：用户/审计 ----
	admin := authed.Group("/admin", a.roleMW(auth.RoleAdmin))
	admin.POST("/users", func(c *gin.Context) {
		var in struct{ Username, Password, Nickname string; Role string }
		_ = c.ShouldBindJSON(&in)
		u, err := a.Auth.CreateUser(in.Username, in.Password, in.Nickname, auth.Role(in.Role))
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(u))
	})
	admin.GET("/audit", func(c *gin.Context) {
		c.JSON(http.StatusOK, ok(a.ToolAudit.Entries))
	})
	// 用户管理 + 待办（TX-01；总验收门补齐）
	a.registerUserRoutes(admin.Group(""))

	// ---- Agent 工具通道（I-3）----
	agent := authed.Group("/agent", a.roleMW(auth.RoleTeacherAgent, auth.RoleSystemAgent))
	agent.GET("/tools", func(c *gin.Context) {
		c.JSON(http.StatusOK, ok(a.Tools.ToolsFor(tools.Role(string(claimsOf(c).Role)))))
	})
	agent.POST("/tools/call", func(c *gin.Context) {
		var in struct {
			Name string      `json:"name"`
			Args tools.Args `json:"args"`
		}
		_ = c.ShouldBindJSON(&in)
		caller := tools.Caller{Role: tools.Role(string(claimsOf(c).Role)), ID: claimsOf(c).Username}
		res, err := a.Tools.Call(c.Request.Context(), caller, in.Name, in.Args)
		if err != nil {
			a.Log.Warn("tools", "tool_call_denied", map[string]any{ // §14 工具调用（含失败）
				"caller": caller.ID, "role": string(caller.Role),
				"tool": in.Name, "error": err.Error()})
			c.JSON(http.StatusOK, fail(errForbidden, err.Error()))
			return
		}
		a.Log.Info("tools", "tool_called", map[string]any{
			"caller": caller.ID, "role": string(caller.Role), "tool": in.Name})
		c.JSON(http.StatusOK, ok(res))
	})

	// ---- WS ----
	r.GET("/ws/classroom", a.handleWS)
	r.GET("/api/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	return r
}

// ---- 中间件 ----

const claimsKey = "claims"

func (a *App) authMW() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		tok := strings.TrimPrefix(h, "Bearer ")
		cl, err := a.Auth.Verify(tok)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusOK, fail(errUnauthorized, "unauthorized"))
			return
		}
		c.Set(claimsKey, cl)
	}
}

func (a *App) roleMW(roles ...auth.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		cl := claimsOf(c)
		for _, r := range roles {
			if cl.Role == r {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusOK, fail(errForbidden, "forbidden"))
	}
}

func claimsOf(c *gin.Context) *auth.Claims {
	if v, ok := c.Get(claimsKey); ok {
		return v.(*auth.Claims)
	}
	return &auth.Claims{}
}

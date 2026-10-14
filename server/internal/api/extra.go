// api 扩展端点（总验收门审计补齐）：
// FR-B2 词条 CRUD、B3 导入解析（Mock LLM）、B6 CSV 导出、
// TX-01 用户管理（列表/重置/停用/启用）、BE-8 覆盖删除、管理员待办（escalated 反馈）。
// 并在关键节点埋 SDD §14 必记日志（登录/判分/相位/配置/反馈/逾期/WS 连接）。
package api

import (
	"context"
	"net/http"
	"strconv"

	"enwords/internal/auth"
	"enwords/internal/wordbook"

	"github.com/gin-gonic/gin"
)

// registerEntryRoutes 词条 CRUD + 导入解析 + 导出（挂在 admin 词库组）。
func (a *App) registerEntryRoutes(g *gin.RouterGroup) {
	g.POST("/:id/entries", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		var in wordbook.EntryInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		e, err := a.Books.AddEntry(id, in)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(e))
	})
	g.PUT("/:id/entries/:entryId", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		eid, _ := strconv.ParseInt(c.Param("entryId"), 10, 64)
		var in wordbook.EntryInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		e, err := a.Books.GetEntry(eid)
		if err != nil || e.BookID != id {
			c.JSON(http.StatusOK, fail(errNotFound, "entry not found"))
			return
		}
		e.Word, e.Phonetic, e.Unit = in.Word, in.Phonetic, in.Unit
		e.Meanings = in.Meanings
		if err := a.Books.UpdateEntry(e); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(e))
	})
	g.DELETE("/:id/entries/:entryId", func(c *gin.Context) {
		eid, _ := strconv.ParseInt(c.Param("entryId"), 10, 64)
		if err := a.Books.DeleteEntry(eid); err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok("deleted"))
	})
	// B3 导入解析（Mock LLM；预览不入库，commit 走既有 import/commit）
	g.POST("/:id/import/parse", func(c *gin.Context) {
		var in struct {
			RawText string `json:"rawText"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		entries, warnings := a.Books.ImportParse(0, in.RawText)
		c.JSON(http.StatusOK, ok(gin.H{"entries": entries, "warnings": warnings}))
	})
	// B6 导出 CSV（UTF-8 BOM）
	g.GET("/:id/export", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		csv, err := a.Books.ExportCSV(id)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.Header("Content-Disposition", "attachment; filename=wordbook.csv")
		c.Data(http.StatusOK, "text/csv; charset=utf-8", csv)
	})
}

// registerUserRoutes 用户管理（TX-01：FR-A4）+ 待办。
func (a *App) registerUserRoutes(g *gin.RouterGroup) {
	g.GET("/users", func(c *gin.Context) {
		c.JSON(http.StatusOK, ok(a.Auth.ListUsers()))
	})
	g.POST("/users/:username/reset_password", func(c *gin.Context) {
		var in struct {
			Password string `json:"password"`
		}
		_ = c.ShouldBindJSON(&in)
		if err := a.Auth.ResetPassword(c.Param("username"), in.Password); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		a.persistUser(c.Param("username")) // 持久化（R18：重置后落库）
		a.Log.Info("auth", "password_reset", map[string]any{
			"operator": claimsOf(c).Username, "target": c.Param("username")})
		c.JSON(http.StatusOK, ok("reset"))
	})
	g.POST("/users/:username/disable", func(c *gin.Context) {
		if err := a.Auth.Disable(c.Param("username")); err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, err.Error()))
			return
		}
		a.persistUser(c.Param("username"))
		a.Log.Warn("auth", "user_disabled", map[string]any{
			"operator": claimsOf(c).Username, "target": c.Param("username")})
		c.JSON(http.StatusOK, ok("disabled"))
	})
	g.POST("/users/:username/enable", func(c *gin.Context) {
		if err := a.Auth.Enable(c.Param("username")); err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, err.Error()))
			return
		}
		a.persistUser(c.Param("username"))
		c.JSON(http.StatusOK, ok("enabled"))
	})
	// 管理员待办：已升级（escalated）的反馈（FR-G5/G6 闭环视图）
	a.registerNotificationRoutes(g)
	g.GET("/todos", func(c *gin.Context) {
		rows, err := a.FeedbackAdminTodosContext(c.Request.Context())
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(rows))
	})
	g.GET("/todos/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(200, fail(400, "invalid todo id"))
			return
		}
		row, err := a.FeedbackAdminTodoContext(c.Request.Context(), id)
		if err != nil {
			a.respondFeedbackError(c, err)
			return
		}
		c.JSON(200, ok(row))
	})
}

// registerConfigRoutes 配置覆盖删除（BE-08）。
func (a *App) registerConfigRoutes(g *gin.RouterGroup) {
	g.DELETE("/override", func(c *gin.Context) {
		var in struct {
			StudentID int64  `json:"studentId"`
			Key       string `json:"key"`
		}
		_ = c.ShouldBindJSON(&in)
		if err := a.Cfg.DeleteOverride(claimsOf(c).Username, in.StudentID, in.Key); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		a.Log.Info("config", "override_deleted", map[string]any{
			"student_id": in.StudentID, "key": in.Key})
		c.JSON(http.StatusOK, ok("deleted"))
	})
}

// Todos 公开只读（ws/外部装配用）。
func (a *App) Todos() []FeedbackRecord {
	rows, err := a.FeedbackAdminTodosContext(context.Background())
	if err != nil {
		return nil
	}
	out := make([]FeedbackRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, FeedbackRecord{ID: row.ID, TaskID: row.TaskID, StudentID: row.StudentID,
			Text: row.Text, Status: "escalated", Reply: row.Reply, CreatedAt: row.CreatedAt})
	}
	return out
}

var _ = auth.RoleAdmin // 保持 auth 引用（日志字段用）

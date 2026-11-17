package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"enwords/internal/auth"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
)

// EnablePilotPersistence explicitly migrates and audits the pilot schema on
// this App's SQLite pool. It does not change existing lesson routing.
func (a *App) EnablePilotPersistence(ctx context.Context) error {
	if a == nil || a.DB == nil {
		return store.ErrInitializationConflict
	}
	pilot, err := store.OpenPilotStore(ctx, a.DB, func(ctx context.Context, event store.PilotStoreEvent) {
		a.Log.InfoCtx(ctx, "pilot", "pilot_store_event", map[string]any{
			"event": event.Event, "outcome": event.Outcome, "reason": event.Reason,
			"student_id": event.StudentID, "book_id": event.BookID, "lesson_id": event.LessonID,
		})
	})
	if err != nil {
		return err
	}
	a.Pilot = pilot
	return nil
}

// CreatePilotLesson is the trusted application adapter for the isolated
// creation contract. It freezes the plan before entering PilotStore; callers
// cannot provide a lesson ID, state, or arbitrary configuration.
func (a *App) CreatePilotLesson(ctx context.Context, taskID string, studentID, bookID int64, openAt time.Time, wordIDs []int64) (store.PilotCreationReceipt, error) {
	if a == nil || a.Pilot == nil || a.DB == nil {
		return store.PilotCreationReceipt{}, store.ErrInitializationConflict
	}
	principal, err := store.BindPilotSystemPrincipal(studentID, strings.TrimSpace(taskID))
	if err != nil {
		return store.PilotCreationReceipt{}, err
	}
	plan, err := store.PreparePilotCreationPlan(ctx, a.DB, studentID, bookID, openAt, wordIDs)
	if err != nil {
		return store.PilotCreationReceipt{}, err
	}
	return a.Pilot.CreateIsolatedPilotLesson(ctx, principal, plan)
}

func (a *App) registerPilotRoutes(g *gin.RouterGroup) {
	admin := g.Group("/admin", a.roleMW(auth.RoleAdmin))
	admin.POST("/pilot/enable", func(c *gin.Context) {
		if err := a.EnablePilotPersistence(c.Request.Context()); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "pilot persistence unavailable"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "pilot", "pilot_persistence_enabled", map[string]any{"operator": claimsOf(c).UID})
		c.JSON(http.StatusOK, ok(gin.H{"enabled": true, "legacy_routing": true}))
	})
	admin.POST("/pilot/lessons", func(c *gin.Context) {
		var in struct {
			TaskID    string  `json:"task_id"`
			StudentID int64   `json:"student_id"`
			BookID    int64   `json:"book_id"`
			OpenAt    string  `json:"open_at"`
			WordIDs   []int64 `json:"word_ids"`
		}
		if c.ShouldBindJSON(&in) != nil || strings.TrimSpace(in.TaskID) == "" || in.StudentID <= 0 || in.BookID <= 0 || len(in.WordIDs) == 0 || len(in.WordIDs) > 100 {
			c.JSON(http.StatusOK, fail(errBadRequest, "invalid pilot lesson request"))
			return
		}
		openAt, err := time.Parse(time.RFC3339, in.OpenAt)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "invalid pilot lesson time"))
			return
		}
		receipt, err := a.CreatePilotLesson(c.Request.Context(), in.TaskID, in.StudentID, in.BookID, openAt, in.WordIDs)
		if err != nil {
			code := http.StatusConflict
			if strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "invalid") {
				code = errBadRequest
			}
			c.JSON(http.StatusOK, fail(code, "pilot lesson creation failed"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "pilot", "pilot_lesson_created", map[string]any{"lesson_id": receipt.LessonID, "student_id": receipt.StudentID, "book_id": receipt.BookID, "replayed": receipt.Replayed, "operator": claimsOf(c).UID})
		c.JSON(http.StatusOK, ok(receipt))
	})
}

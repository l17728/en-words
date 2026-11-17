package api

import (
	"context"
	"net/http"

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
}

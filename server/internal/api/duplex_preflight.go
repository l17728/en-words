package api

import (
	"strconv"

	"enwords/internal/auth"
	"github.com/gin-gonic/gin"
)

// Preflight grants no audio upload, model access, grade mutation or native
// duplex capability. It only exposes bounded local device diagnostics.
func (a *App) registerDuplexPreflight(g *gin.RouterGroup) {
	g.GET("/classroom/lessons/:id/duplex-capabilities", a.roleMW(auth.RoleStudent), func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(400, fail(400, "课堂编号无效"))
			return
		}
		if !a.ownLesson(c, id) {
			return
		}
		reasons := []string{"native_capability_unverified", "teaching_integration_pending", "acoustic_acceptance_pending"}
		if !a.DuplexPreflight {
			reasons = append([]string{"pilot_disabled"}, reasons...)
		}
		c.JSON(200, ok(gin.H{"pilot_enabled": a.DuplexPreflight, "mode": "preflight", "native_duplex": false, "automatic_teaching": false,
			"reasons": reasons, "limits": gin.H{"sample_rate": 16000, "frame_ms": 20, "batch_ms": 80, "preroll_ms": 300, "tail_ms": 200, "endpoint_ms": 600, "max_utterance_ms": 15000, "confirmation_ms": 8000}}))
	})
}

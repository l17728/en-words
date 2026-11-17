package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"enwords/internal/auth"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerTeacherAnalysisRoutes exposes the human review boundary. Agent output
// is never applied through the student read route.
func (a *App) registerTeacherAnalysisRoutes(g *gin.RouterGroup) {
	student := g.Group("/student", a.roleMW(auth.RoleStudent))
	student.GET("/lessons/:id/teacher-analysis", func(c *gin.Context) {
		if a.DB == nil {
			c.JSON(http.StatusOK, ok([]any{}))
			return
		}
		lessonID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		var rows []store.TeacherFailureAnalysis
		q := a.DB.Where("lesson_id = ? AND student_id = ?", lessonID, claimsOf(c).UID).Order("created_at DESC").Find(&rows)
		if q.Error != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "analysis unavailable"))
			return
		}
		c.JSON(http.StatusOK, ok(rows))
	})

	admin := g.Group("/admin", a.roleMW(auth.RoleAdmin))
	admin.GET("/teacher-analysis", func(c *gin.Context) {
		if a.DB == nil {
			c.JSON(http.StatusOK, ok([]any{}))
			return
		}
		var rows []store.TeacherFailureAnalysis
		q := a.DB.Order("created_at DESC").Limit(200).Find(&rows)
		if q.Error != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "analysis unavailable"))
			return
		}
		c.JSON(http.StatusOK, ok(rows))
	})
	admin.POST("/teacher-analysis/:id/evaluate", func(c *gin.Context) {
		if a.DB == nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "persistence unavailable"))
			return
		}
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		var in struct {
			Action  string `json:"action"`
			Variant string `json:"variant"`
		}
		if c.ShouldBindJSON(&in) != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "invalid evaluation"))
			return
		}
		allowed := map[string]bool{"accept_suggestion": true, "accept_transcript": true, "reject_suggestion": true, "edit_and_apply": true, "mark_asr_error": true, "defer": true}
		if !allowed[in.Action] || (in.Action == "edit_and_apply" && strings.TrimSpace(in.Variant) == "") {
			c.JSON(http.StatusOK, fail(errBadRequest, "unsupported evaluation"))
			return
		}
		var row store.TeacherFailureAnalysis
		if err := a.DB.First(&row, id).Error; err != nil {
			c.JSON(http.StatusOK, fail(errNotFound, "analysis not found"))
			return
		}
		now := time.Now()
		status := "rejected"
		if in.Action == "defer" {
			status = "pending"
		}
		if in.Action == "accept_suggestion" || in.Action == "accept_transcript" || in.Action == "edit_and_apply" {
			status = "approved"
		}
		variant := strings.TrimSpace(in.Variant)
		if variant == "" {
			variant = strings.TrimSpace(row.Transcript)
		}
		argsDigest := sha256.Sum256([]byte(strings.Join([]string{strconv.FormatInt(row.EntryID, 10), variant, in.Action}, "\x00")))
		argsHash := hex.EncodeToString(argsDigest[:])
		var nonce [16]byte
		_, _ = rand.Read(nonce[:])
		receipt := store.TeacherAuthorizationReceipt{ID: hex.EncodeToString(nonce[:]), AnalysisID: row.ID, OperatorID: claimsOf(c).UID, ToolName: "wordbook.variant.add", ArgsSHA256: argsHash, Nonce: hex.EncodeToString(nonce[:]), ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now}
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&store.TeacherFailureAnalysis{}).Where("id = ? AND implementation_status = ?", row.ID, "pending").Updates(map[string]any{"implementation_status": status, "resolved_at": &now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil
			}
			if err := tx.Create(&receipt).Error; err != nil {
				return err
			}
			if status == "approved" && variant != "" {
				if err := tx.Where("entry_id = ? AND variant = ?", row.EntryID, variant).FirstOrCreate(&store.WordPronunciationVariant{EntryID: row.EntryID, Variant: variant, Kind: "teacher_approved", Source: "teacher_agent", Confidence: row.Confidence, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
					return err
				}
			}
			return tx.Model(&store.TeacherAuthorizationReceipt{}).Where("id = ? AND consumed_at IS NULL", receipt.ID).Updates(map[string]any{"consumed_at": now}).Error
		})
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "evaluation failed"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "teacher_analysis", "teacher_analysis_evaluated", map[string]any{"analysis_id": row.ID, "action": in.Action, "receipt_id": receipt.ID, "variant_applied": status == "approved"})
		c.JSON(http.StatusOK, ok(gin.H{"analysis_id": row.ID, "status": status, "receipt_id": receipt.ID}))
	})
}

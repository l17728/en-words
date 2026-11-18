package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"enwords/internal/auth"
	"enwords/internal/speechnormalize"
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
	admin.GET("/wordbooks/:id/words/search", func(c *gin.Context) {
		bookID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		query := strings.ToLower(strings.TrimSpace(c.Query("q")))
		if query == "" {
			c.JSON(http.StatusOK, ok([]any{}))
			return
		}
		entries, _, err := a.Books.ListEntries(bookID, 1, 5000)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, "word search failed"))
			return
		}
		matches := make([]any, 0)
		for _, entry := range entries {
			if strings.Contains(strings.ToLower(entry.Word), query) {
				variants := []store.WordPronunciationVariant{}
				speechText := speechnormalize.NormalizeForSpeech(entry.Word)
				cache := map[string]bool{"en": false, "zh": false}
				if a.DB != nil {
					_ = a.DB.Where("entry_id = ?", entry.ID).Order("version DESC").Find(&variants).Error
					var assets []store.SpeechAsset
					_ = a.DB.Where("speech_text = ?", speechText).Find(&assets).Error
					for _, asset := range assets {
						if asset.Status == "ready" {
							cache[asset.Language] = true
						}
					}
				}
				matches = append(matches, gin.H{"entry": entry, "variants": variants, "speech_text": speechText, "cache_ready": cache})
			}
		}
		c.JSON(http.StatusOK, ok(matches))
	})
	admin.POST("/speech/refresh", func(c *gin.Context) {
		var in struct {
			Text string `json:"text"`
			Lang string `json:"lang"`
		}
		if c.ShouldBindJSON(&in) != nil || strings.TrimSpace(in.Text) == "" || (in.Lang != "en" && in.Lang != "zh") {
			c.JSON(http.StatusOK, fail(errBadRequest, "invalid speech refresh request"))
			return
		}
		if a.Local == nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "local speech unavailable"))
			return
		}
		if _, err := a.refreshSpeechCache(c.Request.Context(), strings.TrimSpace(in.Text), in.Lang); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusBadGateway, "speech refresh failed"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "speech", "speech_cache_refreshed", map[string]any{"operator": claimsOf(c).UID, "language": in.Lang, "text_length": len([]rune(in.Text))})
		c.JSON(http.StatusOK, ok(gin.H{"status": "ready"}))
	})
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
	admin.POST("/teacher-analysis/process-next", func(c *gin.Context) {
		if a.DB == nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "persistence unavailable"))
			return
		}
		owner := "admin-teacher-analysis-" + strconv.FormatInt(claimsOf(c).UID, 10)
		if err := a.ProcessNextTeacherAnalysis(c.Request.Context(), owner); err != nil {
			if errors.Is(err, store.ErrTeacherJobUnavailable) {
				c.JSON(http.StatusOK, ok(gin.H{"processed": false, "reason": "no_due_job"}))
				return
			}
			c.JSON(http.StatusOK, fail(http.StatusBadRequest, "analysis processing failed"))
			return
		}
		a.Log.InfoCtx(c.Request.Context(), "teacher_analysis", "teacher_analysis_processed", map[string]any{"operator": claimsOf(c).UID})
		c.JSON(http.StatusOK, ok(gin.H{"processed": true}))
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

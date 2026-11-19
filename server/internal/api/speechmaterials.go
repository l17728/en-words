package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"enwords/internal/auth"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type speechMaterialItem struct{ Key, Language, Text string }

func speechManifestRevision(items []speechMaterialItem) string {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Key != items[j].Key {
			return items[i].Key < items[j].Key
		}
		return items[i].Language < items[j].Language
	})
	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *App) speechMaterialsForLesson(id int64) ([]speechMaterialItem, string, error) {
	lesson, err := a.Sched.Lesson(id)
	if err != nil {
		return nil, "", err
	}
	ids := lesson.BatchWordIDs
	entries, err := a.Books.EntriesByIDs(lesson.BookID, ids)
	if err != nil {
		return nil, "", err
	}
	items := make([]speechMaterialItem, 0, len(entries)*2)
	for _, e := range entries {
		text := strings.TrimSpace(e.Word)
		if text == "" {
			continue
		}
		items = append(items, speechMaterialItem{Key: a.speechCacheKey(text, "en"), Language: "en", Text: text}, speechMaterialItem{Key: a.speechCacheKey(text, "zh"), Language: "zh", Text: text})
	}
	return items, speechManifestRevision(items), nil
}

func (a *App) registerSpeechMaterialRoutes(g *gin.RouterGroup) {
	student := g.Group("/classroom", a.roleMW(auth.RoleStudent))
	student.POST("/lessons/:id/speech-materials/scan", func(c *gin.Context) { a.speechMaterialScan(c, false) })
	student.POST("/lessons/:id/speech-materials/ensure", func(c *gin.Context) { a.speechMaterialScan(c, true) })
	student.GET("/lessons/:id/speech-materials/progress", a.speechMaterialProgress)
	student.POST("/lessons/:id/speech-materials/cancel", a.speechMaterialCancel)
}

func (a *App) speechMaterialScan(c *gin.Context, ensure bool) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if !a.ownLesson(c, id) {
		return
	}
	items, rev, err := a.speechMaterialsForLesson(id)
	if err != nil {
		c.JSON(http.StatusOK, fail(errNotFound, "lesson not found"))
		return
	}
	if a.DB != nil {
		if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "speech material store unavailable"))
			return
		}
	}
	a.Log.InfoCtx(c.Request.Context(), "speech", "speech_material_scan_started", map[string]any{"lesson_id": id, "manifest_revision": rev, "total": len(items)})
	ready := 0
	missing := 0
	if a.DB != nil {
		for _, item := range items {
			var n int64
			a.DB.Model(&store.SpeechAsset{}).Where("speech_text = ? AND language = ? AND status = ?", item.Text, item.Language, "ready").Count(&n)
			if n > 0 {
				ready++
				prefix := item.Key
				if len(prefix) > 12 {
					prefix = prefix[:12]
				}
				a.Log.InfoCtx(c.Request.Context(), "speech", "speech_material_cache_hit", map[string]any{"lesson_id": id, "cache_key_prefix": prefix, "language": item.Language})
			} else {
				missing++
			}
		}
	} else {
		missing = len(items)
	}
	if ensure && a.DB != nil {
		for _, item := range items {
			var n int64
			a.DB.Model(&store.SpeechAsset{}).Where("speech_text = ? AND language = ? AND status = ?", item.Text, item.Language, "ready").Count(&n)
			if n == 0 {
				job := &store.SpeechMaterialJob{CacheKey: item.Key, ManifestRevision: rev, LessonID: id, Language: item.Language, SpeechText: item.Text, Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if a.DB.Where("cache_key = ? AND manifest_revision = ?", item.Key, rev).FirstOrCreate(job).Error == nil && (job.Status == "failed" || job.Status == "canceled") {
					a.DB.Model(job).Updates(map[string]any{"status": "pending", "error_code": "", "error_summary": "", "updated_at": time.Now()})
				}
				if job.ID != 0 {
					if job.Status == "pending" || (job.Status == "running" && job.LeaseUntil != nil && job.LeaseUntil.Before(time.Now())) {
						go a.runSpeechMaterialJob(job.ID)
					}
				}
			}
		}
	}
	c.JSON(http.StatusOK, ok(gin.H{"lesson_id": id, "manifest_revision": rev, "total": len(items), "ready": ready, "missing": missing, "can_start": missing == 0, "ensure": ensure}))
}

func (a *App) runSpeechMaterialJob(id int64) {
	if a.DB == nil {
		return
	}
	var job store.SpeechMaterialJob
	if a.DB.First(&job, id).Error != nil {
		return
	}
	now := time.Now()
	token := fmt.Sprintf("speech-%d-%d", id, now.UnixNano())
	lease := now.Add(2 * time.Minute)
	claim := a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND (status = ? OR (status = ? AND lease_until IS NOT NULL AND lease_until < ?))", id, "pending", "running", now).Updates(map[string]any{"status": "running", "attempt_token": token, "lease_until": lease, "attempt": gorm.Expr("attempt + 1"), "updated_at": now})
	if claim.RowsAffected != 1 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if a.Local == nil {
		a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND attempt_token = ?", id, "running", token).Updates(map[string]any{"status": "failed", "error_code": "tts_unavailable", "error_summary": "local TTS provider unavailable", "updated_at": time.Now()})
		return
	}
	_, _, err := a.synthesizeCached(ctx, job.SpeechText, job.Language)
	status := "ready"
	values := map[string]any{"status": status, "updated_at": time.Now(), "error_code": "", "error_summary": ""}
	if err != nil {
		values["status"] = "failed"
		values["error_code"] = "synthesis_failed"
		values["error_summary"] = err.Error()
	}
	a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND attempt_token = ?", id, "running", token).Updates(values)
	if err != nil {
		a.Log.WarnCtx(ctx, "speech", "speech_material_failed", map[string]any{"job_id": id, "lesson_id": job.LessonID, "language": job.Language})
	} else {
		a.Log.InfoCtx(ctx, "speech", "speech_material_ready", map[string]any{"job_id": id, "lesson_id": job.LessonID, "language": job.Language})
	}
}

func (a *App) speechMaterialProgress(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if !a.ownLesson(c, id) {
		return
	}
	items, rev, err := a.speechMaterialsForLesson(id)
	if err != nil {
		c.JSON(http.StatusOK, fail(errNotFound, "lesson not found"))
		return
	}
	if a.DB != nil {
		var stale []store.SpeechMaterialJob
		now := time.Now()
		a.DB.Where("lesson_id = ? AND manifest_revision = ? AND status = ? AND lease_until IS NOT NULL AND lease_until < ?", id, rev, "running", now).Find(&stale)
		for _, job := range stale {
			a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND lease_until < ?", job.ID, "running", now).Updates(map[string]any{"status": "pending", "updated_at": now})
			go a.runSpeechMaterialJob(job.ID)
		}
	}
	if a.DB != nil {
		if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "speech material store unavailable"))
			return
		}
	}
	total := int64(len(items))
	var ready, failed, pending int64
	if a.DB != nil {
		for _, item := range items {
			var n int64
			a.DB.Model(&store.SpeechAsset{}).Where("speech_text = ? AND language = ? AND status = ?", item.Text, item.Language, "ready").Count(&n)
			if n > 0 {
				ready++
				continue
			}
			var job store.SpeechMaterialJob
			if a.DB.Where("cache_key = ? AND manifest_revision = ?", item.Key, rev).First(&job).Error != nil {
				pending++
				continue
			}
			switch job.Status {
			case "ready":
				ready++
			case "failed":
				failed++
			default:
				pending++
			}
		}
	}
	c.JSON(http.StatusOK, ok(gin.H{"lesson_id": id, "manifest_revision": rev, "total": total, "ready": ready, "failed": failed, "pending": pending, "can_start": total > 0 && ready == total}))
}

func (a *App) speechMaterialCancel(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if !a.ownLesson(c, id) {
		return
	}
	_, rev, err := a.speechMaterialsForLesson(id)
	if err != nil {
		c.JSON(http.StatusOK, fail(errNotFound, "lesson not found"))
		return
	}
	if a.DB != nil {
		if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "speech material store unavailable"))
			return
		}
	}
	if a.DB != nil {
		a.DB.Model(&store.SpeechMaterialJob{}).Where("lesson_id = ? AND manifest_revision = ? AND status IN ?", id, rev, []string{"pending", "running"}).Updates(map[string]any{"status": "canceled", "updated_at": time.Now()})
	}
	c.JSON(http.StatusOK, ok(gin.H{"lesson_id": id, "manifest_revision": rev, "canceled": true}))
}

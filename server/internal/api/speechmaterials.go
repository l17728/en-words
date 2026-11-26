package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"enwords/internal/auth"
	"enwords/internal/obs"
	"enwords/internal/speechnormalize"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type speechMaterialItem struct{ Key, Language, Text, ServiceID, Model, Voice string }

func (a *App) speechMaterialCached(item speechMaterialItem) bool {
	if a.DB == nil {
		return false
	}
	var asset store.SpeechAsset
	if a.DB.Where("cache_key = ? AND status = ?", item.Key, "ready").First(&asset).Error != nil {
		return false
	}
	if !validSpeechCachePath(item.Key, asset.SHA256, asset.FilePath) {
		a.DB.Model(&asset).Where("status = ?", "ready").Update("status", "failed")
		return false
	}
	raw, err := os.ReadFile(asset.FilePath)
	if err == nil && validSpeechAudio(raw, asset.SizeBytes, asset.SHA256) {
		return true
	}
	a.DB.Model(&asset).Where("status = ?", "ready").Update("status", "failed")
	return false
}

func (a *App) retrySpeechMaterialJob(job *store.SpeechMaterialJob) bool {
	if a.DB == nil || job == nil || (job.Status != "failed" && job.Status != "canceled" && job.Status != "ready") {
		return false
	}
	result := a.DB.Model(job).Where("status IN ?", []string{"failed", "canceled", "ready"}).Updates(map[string]any{"status": "pending", "error_code": "", "error_summary": "", "attempt_token": "", "lease_until": nil, "updated_at": time.Now().UTC()})
	if result.Error != nil || result.RowsAffected != 1 {
		return false
	}
	job.Status = "pending"
	job.ErrorCode = ""
	job.ErrorSummary = ""
	job.AttemptToken = ""
	job.LeaseUntil = nil
	return true
}

func (a *App) startSpeechMaterialJob(id int64) {
	a.speechMaterialMu.Lock()
	if a.speechMaterialClose {
		a.speechMaterialMu.Unlock()
		return
	}
	a.speechMaterialWG.Add(1)
	a.speechMaterialMu.Unlock()
	go func() {
		defer a.speechMaterialWG.Done()
		a.runSpeechMaterialJob(id)
	}()
}

// StopSpeechMaterialJobs prevents new workers and cancels queued/active work
// before the server closes its database pool.
func (a *App) StopSpeechMaterialJobs(ctx context.Context) error {
	a.speechMaterialMu.Lock()
	a.speechMaterialClose = true
	if a.speechMaterialStop != nil {
		a.speechMaterialStop()
	}
	a.speechMaterialMu.Unlock()
	done := make(chan struct{})
	go func() {
		a.speechMaterialWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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

func (a *App) activateSpeechManifest(lessonID int64, revision string) error {
	if a.DB == nil || lessonID <= 0 || revision == "" { return nil }
	now := time.Now().UTC()
	return a.DB.Transaction(func(tx *gorm.DB) error {
		manifest := store.SpeechMaterialManifest{LessonID:lessonID,Revision:revision,UpdatedAt:now}
		if err := tx.Clauses(clause.OnConflict{Columns:[]clause.Column{{Name:"lesson_id"}},DoUpdates:clause.AssignmentColumns([]string{"revision","updated_at"})}).Create(&manifest).Error; err != nil { return err }
		var staleIDs []int64
		if err := tx.Model(&store.SpeechMaterialJob{}).Where("lesson_id = ? AND manifest_revision <> ? AND status IN ?",lessonID,revision,[]string{"pending","running"}).Pluck("id",&staleIDs).Error; err != nil { return err }
		if len(staleIDs)==0 { return nil }
		if err := tx.Model(&store.SpeechMaterialJob{}).Where("id IN ?",staleIDs).Updates(map[string]any{"status":"canceled","agent_id":"","attempt_token":"","lease_until":nil,"updated_at":now}).Error; err != nil { return err }
		if tx.Migrator().HasTable(&store.SpeechUploadReservation{}) {
			return tx.Model(&store.SpeechUploadReservation{}).Where("job_id IN ? AND status = ?", staleIDs, "active").Update("status", "rejected").Error
		}
		return nil
	})
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
	enService, enModel, enVoice := a.speechTTSSelection("en")
	zhService, zhModel, zhVoice := a.speechTTSSelection("zh")
	if enModel == "" || enVoice == "" || zhModel == "" || zhVoice == "" {
		return nil, "", fmt.Errorf("the active TTS service does not resolve to one approved model and voice for both languages")
	}
	for _, e := range entries {
		english, chinese := lessonSpeechTexts(e.Word, e.Meanings)
		if english == "" {
			continue
		}
		items = append(items,
			speechMaterialItem{Key: speechCacheKeyWith(english, "en", enService, enModel, enVoice), Language: "en", Text: english, ServiceID: enService, Model: enModel, Voice: enVoice},
			speechMaterialItem{Key: speechCacheKeyWith(chinese, "zh", zhService, zhModel, zhVoice), Language: "zh", Text: chinese, ServiceID: zhService, Model: zhModel, Voice: zhVoice},
		)
	}
	return items, speechManifestRevision(items), nil
}

func lessonSpeechTexts(word string, meanings []string) (english, chinese string) {
	word = strings.TrimSpace(word)
	english = speechnormalize.NormalizeForSpeech(word)
	if len(meanings) > 0 {
		chinese = strings.TrimSpace(meanings[0])
		for _, prefix := range []string{"n.", "v.", "adj.", "adv.", "prep.", "conj.", "pron.", "art.", "num.", "int.", "vt.", "vi.", "abbr.", "aux."} {
			if strings.HasPrefix(chinese, prefix+" ") {
				chinese = strings.TrimSpace(strings.TrimPrefix(chinese, prefix+" "))
				break
			}
		}
	}
	if chinese == "" {
		chinese = word
	}
	return english, chinese
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
		if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}, &store.SpeechMaterialManifest{}); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "speech material store unavailable"))
			return
		}
		if err := a.activateSpeechManifest(id, rev); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "speech material manifest unavailable"))
			return
		}
	}
	a.Log.InfoCtx(c.Request.Context(), "speech", "speech_material_scan_started", map[string]any{"lesson_id": id, "manifest_revision": rev, "total": len(items)})
	ready := 0
	missing := 0
	failed := 0
	if a.DB != nil {
		for _, item := range items {
			if a.speechMaterialCached(item) {
				ready++
				prefix := item.Key
				if len(prefix) > 12 {
					prefix = prefix[:12]
				}
				a.Log.InfoCtx(c.Request.Context(), "speech", "speech_material_cache_hit", map[string]any{"lesson_id": id, "cache_key_prefix": prefix, "language": item.Language})
			} else {
				missing++
				var n int64
				a.DB.Model(&store.SpeechMaterialJob{}).Where("cache_key = ? AND manifest_revision = ? AND status = ?", item.Key, rev, "failed").Count(&n)
				if n > 0 {
					failed++
				}
			}
		}
	} else {
		missing = len(items)
	}
	if ensure && a.DB != nil {
		for _, item := range items {
			if !a.speechMaterialCached(item) {
				job := &store.SpeechMaterialJob{CacheKey: item.Key, ManifestRevision: rev, LessonID: id, Language: item.Language, SpeechText: item.Text, ServiceID: item.ServiceID, Model: item.Model, Voice: item.Voice, Status: "pending", TraceID: obs.TraceID(c.Request.Context()), CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if a.DB.Where("cache_key = ? AND manifest_revision = ?", item.Key, rev).FirstOrCreate(job).Error == nil {
					a.retrySpeechMaterialJob(job)
				}
				if job.ID != 0 {
					if a.Local != nil && (job.Status == "pending" || (job.Status == "running" && job.AgentID == "" && job.LeaseUntil != nil && job.LeaseUntil.Before(time.Now().UTC()))) {
						a.startSpeechMaterialJob(job.ID)
					}
				}
			}
		}
	}
	c.JSON(http.StatusOK, ok(gin.H{"lesson_id": id, "manifest_revision": rev, "total": len(items), "ready": ready, "missing": missing, "failed": failed, "can_start": missing == 0, "ensure": ensure}))
}

func (a *App) runSpeechMaterialJob(id int64) {
	if a.DB == nil {
		return
	}
	ctxBase := a.speechMaterialCtx
	if ctxBase == nil {
		ctxBase = context.Background()
	}
	if a.speechMaterialSlots != nil {
		select {
		case a.speechMaterialSlots <- struct{}{}:
		case <-ctxBase.Done():
			return
		}
		defer func() { <-a.speechMaterialSlots }()
	}
	var job store.SpeechMaterialJob
	if a.DB.First(&job, id).Error != nil {
		return
	}
	selectedService, selectedModel, selectedVoice := a.speechTTSSelection(job.Language)
	legacyUnpinned := job.ServiceID == "" && job.Model == "" && job.Voice == ""
	if (a.Local == nil && !legacyUnpinned) || (a.Local != nil && !legacyUnpinned && (job.ServiceID != selectedService || job.Model != selectedModel || job.Voice != selectedVoice)) {
		// A process-local provider may only synthesize the profile/model pinned
		// into the durable manifest. Remote-only or stale-profile jobs stay
		// pending for a matching resident agent; they must never be silently
		// generated by whichever provider happens to be the server default.
		return
	}
	now := time.Now().UTC()
	token := fmt.Sprintf("speech-%d-%d", id, now.UnixNano())
	lease := now.Add(2 * time.Minute)
	claim := a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND (status = ? OR (status = ? AND lease_until IS NOT NULL AND lease_until < ?))", id, "pending", "running", now).Updates(map[string]any{"status": "running", "attempt_token": token, "lease_until": lease, "attempt": gorm.Expr("attempt + 1"), "updated_at": now})
	if claim.RowsAffected != 1 {
		return
	}
	base := obs.WithTrace(ctxBase, job.TraceID)
	ctx, cancel := context.WithTimeout(base, 90*time.Second)
	defer cancel()
	a.Log.InfoCtx(ctx, "speech", "speech_material_job_started", map[string]any{"job_id": id, "lesson_id": job.LessonID, "language": job.Language, "attempt": job.Attempt + 1})
	if a.Local == nil {
		a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND attempt_token = ?", id, "running", token).Updates(map[string]any{"status": "failed", "error_code": "tts_unavailable", "error_summary": "local TTS provider unavailable", "updated_at": time.Now().UTC()})
		a.Log.WarnCtx(ctx, "speech", "speech_material_failed", map[string]any{"job_id": id, "lesson_id": job.LessonID, "language": job.Language, "error_code": "tts_unavailable"})
		return
	}
	_, _, err := a.synthesizeCached(ctx, job.SpeechText, job.Language)
	status := "ready"
	values := map[string]any{"status": status, "updated_at": time.Now().UTC(), "error_code": "", "error_summary": ""}
	if err != nil {
		values["status"] = "failed"
		values["error_code"] = "synthesis_failed"
		values["error_summary"] = "speech provider request failed"
	}
	a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND attempt_token = ?", id, "running", token).Updates(values)
	if err != nil {
		a.Log.WarnCtx(ctx, "speech", "speech_material_failed", map[string]any{"job_id": id, "lesson_id": job.LessonID, "language": job.Language, "error_code": "synthesis_failed"})
	} else {
		a.Log.InfoCtx(ctx, "speech", "speech_material_ready", map[string]any{"job_id": id, "lesson_id": job.LessonID, "language": job.Language})
	}
}

func (a *App) recoverExpiredSpeechMaterialJobs(lessonID int64, revision string) {
	if a.DB == nil || a.DB.AutoMigrate(&store.SpeechMaterialJob{}) != nil {
		return
	}
	now := time.Now().UTC()
	var stale []store.SpeechMaterialJob
	if a.DB.Where("lesson_id = ? AND manifest_revision = ? AND status = ? AND lease_until IS NOT NULL AND lease_until < ?", lessonID, revision, "running", now).Find(&stale).Error != nil {
		return
	}
	for _, job := range stale {
		result := a.DB.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND lease_until < ?", job.ID, "running", now).Updates(map[string]any{"status": "pending", "updated_at": now})
		if result.Error == nil && result.RowsAffected == 1 {
			a.startSpeechMaterialJob(job.ID)
		}
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
	a.recoverExpiredSpeechMaterialJobs(id, rev)
	if a.DB != nil {
		if err := a.DB.AutoMigrate(&store.SpeechMaterialJob{}); err != nil {
			c.JSON(http.StatusOK, fail(http.StatusServiceUnavailable, "speech material store unavailable"))
			return
		}
	}
	total := int64(len(items))
	var ready, failed, pending, canceled, attempts int64
	failureCodes := map[string]bool{}
	if a.DB != nil {
		for _, item := range items {
			var job store.SpeechMaterialJob
			jobFound := a.DB.Where("cache_key = ? AND manifest_revision = ?", item.Key, rev).First(&job).Error == nil
			if jobFound {
				attempts += int64(job.Attempt)
				if job.Status == "canceled" {
					canceled++
				}
				if job.Status == "failed" {
					failed++
					if job.ErrorCode != "" {
						failureCodes[job.ErrorCode] = true
					}
				}
			}
			if a.speechMaterialCached(item) {
				ready++
				continue
			}
			if !jobFound {
				pending++
				continue
			}
			switch job.Status {
			case "ready":
				pending++ // Ready metadata without a valid cache file must be regenerated.
			case "failed", "canceled":
				// Terminal states are surfaced separately and require an explicit retry.
			default:
				pending++
			}
		}
	}
	failedKinds := make([]string, 0, len(failureCodes))
	for code := range failureCodes {
		failedKinds = append(failedKinds, code)
	}
	sort.Strings(failedKinds)
	etagPayload := fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d:%s", rev, total, ready, failed, pending, canceled, attempts, strings.Join(failedKinds, ","))
	etagHash := sha256.Sum256([]byte(etagPayload))
	etag := `"` + hex.EncodeToString(etagHash[:]) + `"`
	c.Header("ETag", etag)
	if strings.TrimSpace(c.GetHeader("If-None-Match")) == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.JSON(http.StatusOK, ok(gin.H{"lesson_id": id, "manifest_revision": rev, "total": total, "ready": ready, "failed": failed, "failure_codes": failedKinds, "pending": pending, "canceled": canceled, "attempts": attempts, "can_start": total > 0 && ready == total}))
}

func (a *App) speechMaterialsReady(id int64) (bool, string, error) {
	items, rev, err := a.speechMaterialsForLesson(id)
	if err != nil {
		return false, "", err
	}
	if len(items) == 0 || a.DB == nil {
		return len(items) == 0, rev, nil
	}
	if !a.DB.Migrator().HasTable(&store.SpeechMaterialJob{}) {
		return false, rev, nil
	}
	for _, item := range items {
		if a.speechMaterialCached(item) {
			continue
		}
		return false, rev, nil
	}
	return true, rev, nil
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
		result := a.DB.Model(&store.SpeechMaterialJob{}).Where("lesson_id = ? AND manifest_revision = ? AND status IN ?", id, rev, []string{"pending", "running"}).Updates(map[string]any{"status": "canceled", "updated_at": time.Now()})
		a.Log.InfoCtx(c.Request.Context(), "speech", "speech_material_cancelled", map[string]any{"lesson_id": id, "manifest_revision": rev, "cancelled_jobs": result.RowsAffected, "operator": claimsOf(c).UID})
	}
	c.JSON(http.StatusOK, ok(gin.H{"lesson_id": id, "manifest_revision": rev, "canceled": true}))
}

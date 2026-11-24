package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"enwords/internal/speechagent"
	"enwords/internal/store"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	speechUploadLimit      = int64(10 << 20)
	speechUploadGlobalByte = int64(1 << 30)
	// Upload handlers hold one bounded WAV in memory. A shared reservation cap
	// keeps that memory budget bounded across all agents and server processes.
	speechUploadInFlightByteLimit = int64(32 << 20)
	speechUploadLease             = 2 * time.Minute
	speechUploadRateLimit         = 30
	speechUploadConcurrent        = 2
)

func newSpeechReservationID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (a *App) speechAgentCreateUploadReservation(c *gin.Context) {
	limitSpeechAgentJSON(c, 1<<10)
	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var in struct {
		AttemptToken string `json:"attempt_token"`
	}
	if err != nil || jobID <= 0 || c.ShouldBindJSON(&in) != nil || in.AttemptToken == "" || len(in.AttemptToken) > 256 {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid upload reservation request"))
		return
	}
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	agent := speechAgentFrom(c)
	now := a.Clock.Now().UTC()
	id, err := newSpeechReservationID()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "upload reservation unavailable"))
		return
	}
	err = svc.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := speechagent.CleanupExpiredMetadata(tx, now); err != nil {
			return err
		}
		// This no-op update takes SQLite's writer lock, making rate, slot and
		// byte-quota decisions atomic across server processes.
		if err := tx.Model(&store.SpeechAgent{}).Where("id = ? AND token_hash = ? AND enabled = ? AND revoked_at IS NULL", agent.ID, agent.TokenHash, true).Update("last_seen_at", now).Error; err != nil {
			return err
		}
		var active store.SpeechAgent
		if err := tx.Where("id = ? AND token_hash = ? AND enabled = ? AND revoked_at IS NULL", agent.ID, agent.TokenHash, true).First(&active).Error; err != nil {
			return errors.New("unauthorized")
		}
		var job store.SpeechMaterialJob
		if err := tx.Where("id = ? AND status = ? AND agent_id = ? AND attempt_token = ? AND lease_until > ?", jobID, "running", agent.ID, in.AttemptToken, now).First(&job).Error; err != nil {
			return errors.New("stale attempt")
		}
		if !speechagent.ManifestCurrent(tx, job) {
			return errors.New("stale attempt")
		}
		window := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, time.UTC)
		rate := store.SpeechAgentRateWindow{AgentID: agent.ID, WindowAt: window, Count: 1, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}, {Name: "window_at"}}, DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr("count + 1"), "updated_at": now})}).Create(&rate).Error; err != nil {
			return err
		}
		if err := tx.Where("agent_id = ? AND window_at = ?", agent.ID, window).First(&rate).Error; err != nil {
			return err
		}
		if rate.Count > speechUploadRateLimit {
			return errors.New("rate limit")
		}
		var activeSlots int64
		if err := tx.Model(&store.SpeechUploadReservation{}).Where("agent_id = ? AND status IN ? AND expires_at > ?", agent.ID, []string{"active", "uploading"}, now).Count(&activeSlots).Error; err != nil {
			return err
		}
		if activeSlots >= speechUploadConcurrent {
			return errors.New("upload slots full")
		}
		var indexed int64
		if err := tx.Model(&store.SpeechAsset{}).Where("status = ?", "ready").Select("COALESCE(SUM(size_bytes),0)").Scan(&indexed).Error; err != nil {
			return err
		}
		var reserved int64
		if err := tx.Model(&store.SpeechUploadReservation{}).Where("status IN ? AND expires_at > ?", []string{"active", "uploading"}, now).Select("COALESCE(SUM(reserved_bytes),0)").Scan(&reserved).Error; err != nil {
			return err
		}
		if reserved+speechUploadLimit > speechUploadInFlightByteLimit {
			return errors.New("global upload slots full")
		}
		orphanBytes, err := speechCacheUnindexedBytes(tx, speechCacheRoot())
		if err != nil {
			return err
		}
		if err := tx.Where("window_at < ?", now.Add(-48*time.Hour)).Delete(&store.SpeechAgentRateWindow{}).Error; err != nil {
			return err
		}
		if indexed+reserved+orphanBytes+speechUploadLimit > speechUploadGlobalByte {
			return errors.New("quota exceeded")
		}
		return tx.Create(&store.SpeechUploadReservation{ID: id, AgentID: agent.ID, JobID: job.ID, AttemptToken: in.AttemptToken, ReservedBytes: speechUploadLimit, ExpiresAt: now.Add(speechUploadLease), Status: "active", CreatedAt: now}).Error
	})
	if err != nil {
		status, code := http.StatusServiceUnavailable, "upload reservation unavailable"
		if errors.Is(err, speechagent.ErrAgentUnauthorized) || err.Error() == "unauthorized" {
			status = http.StatusUnauthorized
			code = "speech agent unauthorized"
		}
		if err.Error() == "stale attempt" {
			status, code = http.StatusConflict, "upload reservation rejected"
		}
		if err.Error() == "quota exceeded" {
			status = http.StatusInsufficientStorage
			code = "speech upload quota unavailable"
		}
		if err.Error() == "rate limit" || err.Error() == "upload slots full" || err.Error() == "global upload slots full" {
			status = http.StatusTooManyRequests
			code = "speech upload temporarily limited"
		}
		c.JSON(status, fail(status, code))
		return
	}
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "upload_reserved", map[string]any{"agent_id": agent.ID, "job_id": jobID, "reservation_id": id, "reserved_bytes": speechUploadLimit})
	c.JSON(http.StatusCreated, ok(gin.H{"reservation_id": id, "max_bytes": speechUploadLimit, "expires_at": now.Add(speechUploadLease)}))
}

// speechCacheUnindexedBytes accounts for crash leftovers and older immutable
// blobs while the caller holds SQLite's writer transaction. Symlinks and
// subdirectories are ignored so the quota cannot follow paths outside cache.
func speechCacheUnindexedBytes(tx *gorm.DB, root string) (int64, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var indexed []string
	if err := tx.Model(&store.SpeechAsset{}).Pluck("file_path", &indexed).Error; err != nil {
		return 0, err
	}
	known := make(map[string]struct{}, len(indexed))
	for _, path := range indexed {
		known[filepath.Clean(path)] = struct{}{}
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, ok := known[filepath.Clean(path)]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func (a *App) speechAgentUploadResult(c *gin.Context) {
	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	reservationID := c.GetHeader("X-Speech-Upload-Reservation")
	attempt := c.GetHeader("X-Speech-Attempt-Token")
	if err != nil || jobID <= 0 || reservationID == "" || len(reservationID) != 64 || attempt == "" || len(attempt) > 256 {
		c.JSON(http.StatusBadRequest, fail(errBadRequest, "invalid speech result request"))
		return
	}
	if ct := strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("Content-Type"), ";")[0])); ct != "audio/wav" && ct != "audio/x-wav" {
		c.JSON(http.StatusUnsupportedMediaType, fail(http.StatusUnsupportedMediaType, "speech result must be WAV"))
		return
	}
	svc, err := a.speechAgentService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech agent service unavailable"))
		return
	}
	agent := speechAgentFrom(c)
	var reservation store.SpeechUploadReservation
	now := a.Clock.Now().UTC()
	err = svc.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// This conditional write is the cross-process admission lock for every
		// body-consuming PUT, including retries with a previously released ID.
		identity := tx.Model(&store.SpeechAgent{}).Where("id = ? AND token_hash = ? AND enabled = ? AND revoked_at IS NULL", agent.ID, agent.TokenHash, true).Update("last_seen_at", now)
		if identity.Error != nil {
			return identity.Error
		}
		if identity.RowsAffected != 1 {
			return errors.New("unauthorized")
		}
		if err := tx.Where("id = ? AND agent_id = ? AND job_id = ? AND attempt_token = ?", reservationID, agent.ID, jobID, attempt).First(&reservation).Error; err != nil {
			return errors.New("reservation expired")
		}
		priorReplay := reservation.Status == "released"
		if priorReplay {
			var receipt store.SpeechAgentResultReceipt
			if err := tx.Where("job_id = ? AND attempt_token = ? AND agent_id = ? AND accepted = ?", jobID, attempt, agent.ID, true).First(&receipt).Error; err != nil {
				return errors.New("reservation expired")
			}
		} else if reservation.Status != "active" || !now.Before(reservation.ExpiresAt) {
			return errors.New("reservation expired")
		} else {
			var job store.SpeechMaterialJob
			if err := tx.Where("id = ? AND status = ? AND agent_id = ? AND attempt_token = ? AND lease_until > ?", jobID, "running", agent.ID, attempt, now).First(&job).Error; err != nil || !speechagent.ManifestCurrent(tx, job) {
				return errors.New("stale attempt")
			}
		}
		var reserved int64
		if err := tx.Model(&store.SpeechUploadReservation{}).Where("status IN ? AND expires_at > ?", []string{"active", "uploading"}, now).Select("COALESCE(SUM(reserved_bytes),0)").Scan(&reserved).Error; err != nil {
			return err
		}
		if priorReplay && reserved+reservation.ReservedBytes > speechUploadInFlightByteLimit {
			return errors.New("global upload slots full")
		}
		result := tx.Model(&store.SpeechUploadReservation{}).Where("id = ? AND status = ?", reservation.ID, reservation.Status).Updates(map[string]any{"status": "uploading", "expires_at": now.Add(speechUploadLease)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reservation expired")
		}
		reservation.Status = "uploading"
		reservation.ExpiresAt = now.Add(speechUploadLease)
		return nil
	})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, speechagent.ErrAgentUnauthorized) || err.Error() == "unauthorized" {
			status = http.StatusUnauthorized
		}
		if err.Error() == "reservation expired" || err.Error() == "stale attempt" {
			status = http.StatusConflict
		}
		if err.Error() == "global upload slots full" {
			status = http.StatusTooManyRequests
		}
		c.JSON(status, fail(status, "speech upload reservation unavailable"))
		return
	}
	reservationAccepted := false
	defer func() {
		if !reservationAccepted {
			_ = svc.DB.Model(&store.SpeechUploadReservation{}).Where("id = ? AND status = ?", reservationID, "uploading").Update("status", "rejected").Error
		}
	}()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, speechUploadLimit+1)
	_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Now().Add(30 * time.Second))
	body, err := io.ReadAll(c.Request.Body)
	_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Time{})
	if err != nil || int64(len(body)) > speechUploadLimit || int64(len(body)) > reservation.ReservedBytes {
		_ = svc.DB.Model(&reservation).Update("status", "rejected").Error
		c.JSON(http.StatusRequestEntityTooLarge, fail(http.StatusRequestEntityTooLarge, "speech result exceeds upload limit"))
		return
	}
	if !validSpeechAudio(body, 0, "") {
		_ = svc.DB.Model(&reservation).Update("status", "rejected").Error
		c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "speech result is not a valid WAV"))
		return
	}
	digest := audioSHA(body)
	commitNow := a.Clock.Now().UTC()
	var prior store.SpeechAgentResultReceipt
	if err := svc.DB.Where("job_id = ? AND attempt_token = ? AND accepted = ?", jobID, attempt, true).First(&prior).Error; err == nil {
		if prior.AgentID != agent.ID || prior.Digest != digest {
			c.JSON(http.StatusConflict, fail(http.StatusConflict, "speech result conflicts with accepted receipt"))
			return
		}
		err := svc.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if err := speechagent.VerifyAgentIdentity(tx, agent); err != nil {
				return err
			}
			var receipt store.SpeechAgentResultReceipt
			if err := tx.Where("job_id = ? AND attempt_token = ? AND agent_id = ? AND digest = ? AND accepted = ?", jobID, attempt, agent.ID, digest, true).First(&receipt).Error; err != nil {
				return errors.New("result conflict")
			}
			result := tx.Model(&store.SpeechUploadReservation{}).Where("id = ? AND agent_id = ? AND status = ?", reservationID, agent.ID, "uploading").Update("status", "released")
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("reservation unavailable")
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, speechagent.ErrAgentUnauthorized) || err.Error() == "unauthorized" {
				c.JSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "speech agent unauthorized"))
			} else if err.Error() == "result conflict" || err.Error() == "reservation unavailable" {
				c.JSON(http.StatusConflict, fail(http.StatusConflict, "speech result receipt unavailable"))
			} else {
				c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "speech result receipt unavailable"))
			}
			return
		}
		reservationAccepted = true
		c.JSON(http.StatusOK, ok(gin.H{"accepted": true, "job_id": jobID, "sha256": digest}))
		return
	}
	var job store.SpeechMaterialJob
	if err := svc.DB.Where("id = ? AND status = ? AND agent_id = ? AND attempt_token = ? AND lease_until > ?", jobID, "running", agent.ID, attempt, commitNow).First(&job).Error; err != nil {
		c.JSON(http.StatusConflict, fail(http.StatusConflict, "speech task attempt expired"))
		return
	}
	if !speechagent.ManifestCurrent(svc.DB, job) {
		c.JSON(http.StatusConflict, fail(http.StatusConflict, "speech task attempt expired"))
		return
	}
	root := speechCacheRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, fail(http.StatusInternalServerError, "speech cache unavailable"))
		return
	}
	finalPath := speechCacheBlobPath(job.CacheKey, digest)
	if err := publishSpeechBlob(root, job.CacheKey, finalPath, body); err != nil {
		c.JSON(http.StatusInternalServerError, fail(http.StatusInternalServerError, "speech cache write failed"))
		return
	}
	err = svc.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := speechagent.VerifyAgentIdentity(tx, agent); err != nil {
			return err
		}
		var existing store.SpeechAgentResultReceipt
		lookup := tx.Where("job_id = ? AND attempt_token = ?", jobID, attempt).First(&existing).Error
		if lookup == nil {
			if existing.Digest != digest {
				return errors.New("result conflict")
			}
			released := tx.Model(&store.SpeechUploadReservation{}).Where("id = ? AND agent_id = ? AND status = ?", reservationID, agent.ID, "uploading").Update("status", "released")
			if released.Error != nil {
				return released.Error
			}
			if released.RowsAffected != 1 {
				return errors.New("reservation unavailable")
			}
			return nil
		}
		if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		var activeReservation store.SpeechUploadReservation
		if err := tx.Where("id = ? AND agent_id = ? AND job_id = ? AND attempt_token = ? AND status = ? AND expires_at > ?", reservationID, agent.ID, jobID, attempt, "uploading", commitNow).First(&activeReservation).Error; err != nil {
			return errors.New("reservation expired")
		}
		if !speechagent.ManifestCurrent(tx, job) {
			return errors.New("stale attempt")
		}
		result := tx.Model(&store.SpeechMaterialJob{}).Where("id = ? AND status = ? AND agent_id = ? AND attempt_token = ? AND lease_until > ?", jobID, "running", agent.ID, attempt, commitNow).Updates(map[string]any{"status": "ready", "updated_at": commitNow, "error_code": "", "error_summary": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("stale attempt")
		}
		asset := store.SpeechAsset{CacheKey: job.CacheKey, Language: job.Language, SpeechText: job.SpeechText, Provider: job.ServiceID, Model: job.Model, Voice: job.Voice, RuleVersion: speechRuleVersion, FilePath: finalPath, SHA256: digest, SizeBytes: int64(len(body)), Status: "ready", CreatedAt: commitNow, LastUsedAt: commitNow}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "cache_key"}}, DoUpdates: clause.AssignmentColumns([]string{"language", "speech_text", "provider", "model", "voice", "rule_version", "file_path", "sha256", "size_bytes", "status", "last_used_at"})}).Create(&asset).Error; err != nil {
			return err
		}
		if err := tx.Create(&store.SpeechAgentResultReceipt{JobID: jobID, AttemptToken: attempt, AgentID: agent.ID, Digest: digest, Accepted: true, CreatedAt: commitNow}).Error; err != nil {
			return err
		}
		return tx.Model(&activeReservation).Update("status", "released").Error
	})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, speechagent.ErrAgentUnauthorized) || err.Error() == "unauthorized" {
			status = http.StatusUnauthorized
		}
		if err.Error() == "stale attempt" || err.Error() == "reservation expired" || err.Error() == "result conflict" || err.Error() == "reservation unavailable" {
			status = http.StatusConflict
		}
		c.JSON(status, fail(status, "speech result was not accepted"))
		return
	}
	reservationAccepted = true
	a.Log.InfoCtx(c.Request.Context(), "speech_agent", "speech_result_accepted", map[string]any{"agent_id": agent.ID, "job_id": jobID, "attempt": job.Attempt, "digest_prefix": digest[:12], "bytes": len(body)})
	c.JSON(http.StatusOK, ok(gin.H{"accepted": true, "job_id": jobID, "sha256": digest}))
}

func publishSpeechBlob(root, cacheKey, finalPath string, body []byte) error {
	if filepath.Dir(filepath.Clean(finalPath)) != filepath.Clean(root) {
		return fmt.Errorf("invalid cache destination")
	}
	if existing, err := os.ReadFile(finalPath); err == nil {
		if validSpeechAudio(existing, int64(len(body)), audioSHA(body)) {
			return nil
		}
		if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(root, "."+cacheKey+".upload-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write(body); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		if existing, readErr := os.ReadFile(finalPath); readErr == nil && validSpeechAudio(existing, int64(len(body)), audioSHA(body)) {
			return nil
		}
		return err
	}
	if dir, err := os.Open(root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"enwords/internal/judge"
	"enwords/internal/speechnormalize"
	"enwords/internal/store"
)

// classifyTeacherFailure is the deterministic fallback used by the worker
// when no remote teacher model is available. It never changes classroom state.
func classifyTeacherFailure(failureType, target, transcript string) (string, string, float64) {
	if strings.TrimSpace(transcript) == "" {
		return "未收到可用转写", "请确认麦克风权限并重试；也可以使用文字输入。", 0.99
	}
	if strings.Contains(strings.ToLower(failureType), "timeout") {
		return "录音或识别超时", "缩短朗读片段后重试，必要时切换文字输入。", 0.95
	}
	if strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(transcript)) {
		return "转写与目标一致，可能是提交链路延迟", "保留本次转写并检查提交状态，避免重复判分。", 0.92
	}
	return "转写未与目标文本匹配", "检查发音、环境噪声和麦克风距离；允许使用已批准的朗读变体。", 0.86
}

// ProcessNextTeacherAnalysis claims and completes one queued analysis. The
// worker is deliberately separate from enqueueTeacherFailure so the classroom
// response never waits for analysis or a model call.
func (a *App) ProcessNextTeacherAnalysis(ctx context.Context, owner string) error {
	if a == nil || a.DB == nil || strings.TrimSpace(owner) == "" {
		return store.ErrTeacherJobUnavailable
	}
	now := time.Now()
	repo := store.NewTeacherAnalysisRepo(a.DB)
	job, err := repo.ClaimJob(now, owner)
	if err != nil {
		return err
	}
	analysis, err := repo.GetAnalysis(job.AnalysisID)
	if err != nil {
		_ = repo.FailJob(job.ID, owner, "analysis_not_found", now, now.Add(time.Minute), true)
		return err
	}
	diagnosis, suggestion, confidence := classifyTeacherFailure(analysis.FailureType, analysis.TargetText, analysis.Transcript)
	updateErr := a.DB.WithContext(ctx).Model(&store.TeacherFailureAnalysis{}).
		Where("id = ? AND analysis_status = ?", analysis.ID, "pending").
		Updates(map[string]any{"diagnosis": diagnosis, "suggestion": suggestion, "confidence": confidence, "analysis_status": "succeeded"}).Error
	if updateErr != nil {
		_ = repo.FailJob(job.ID, owner, "analysis_update_failed", now, now.Add(time.Minute), false)
		return updateErr
	}
	return repo.CompleteJob(job.ID, owner, time.Now())
}

// enqueueTeacherFailure records a deterministic failure receipt and schedules
// the asynchronous teacher analysis. It is intentionally best-effort so the
// classroom fast path never waits for persistence or an LLM.
func (a *App) enqueueTeacherFailure(lessonID, entryID, studentID int64, target, transcript, failureType, epoch string) {
	if a.DB == nil {
		return
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(transcript), " "))
	keyMaterial := strings.Join([]string{strconv.FormatInt(lessonID, 10), strconv.FormatInt(entryID, 10), failureType, normalized, epoch}, "/")
	digest := sha256.Sum256([]byte(keyMaterial))
	key := hex.EncodeToString(digest[:])
	now := time.Now()
	row := &store.TeacherFailureAnalysis{LessonID: lessonID, EntryID: entryID, StudentID: studentID, FailureType: failureType, TargetText: target, Transcript: transcript, Diagnosis: "语音已转写，但未与目标词条匹配", Suggestion: transcript, Confidence: 0, AnalysisStatus: "pending", ImplementationStatus: "pending", AnalysisKey: key, CreatedAt: now}
	if err := a.DB.Create(row).Error; err != nil {
		return // unique analysis_key makes duplicate/late events harmless
	}
	job := &store.TeacherAnalysisJob{ID: key[:32], AnalysisID: row.ID, AnalysisKey: key, Status: "queued", NextRunAt: now, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Create(job).Error; err != nil {
		return
	}
	a.Log.Info("teacher_analysis", "failure_analysis_queued", map[string]any{"analysis_id": row.ID, "lesson_id": lessonID, "entry_id": entryID, "failure_type": failureType})
}

func (a *App) evaluateRepeatWithVariants(entryID int64, target, transcript string) string {
	result := judge.EvaluateRepeat(speechnormalize.NormalizeForSpeech(target), transcript)
	if result == "matched" || a.DB == nil {
		return result
	}
	var variants []store.WordPronunciationVariant
	if err := a.DB.Where("entry_id = ? AND enabled = ?", entryID, true).Find(&variants).Error; err == nil {
		for _, variant := range variants {
			if judge.EvaluateRepeat(speechnormalize.NormalizeForSpeech(variant.Variant), transcript) == "matched" {
				return "matched"
			}
		}
	}
	return result
}

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"enwords/internal/judge"
	"enwords/internal/store"
)

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
	result := judge.EvaluateRepeat(target, transcript)
	if result == "matched" || a.DB == nil {
		return result
	}
	var variants []store.WordPronunciationVariant
	if err := a.DB.Where("entry_id = ? AND enabled = ?", entryID, true).Find(&variants).Error; err == nil {
		for _, variant := range variants {
			if judge.EvaluateRepeat(variant.Variant, transcript) == "matched" {
				return "matched"
			}
		}
	}
	return result
}

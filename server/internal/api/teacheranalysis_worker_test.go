package api

import (
	"context"
	"testing"
	"time"

	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClassifyTeacherFailureDeterministicFallback(t *testing.T) {
	tests := []struct {
		name, kind, target, transcript, diagnosis string
	}{
		{"empty", "asr", "cat", "", "未收到可用转写"},
		{"timeout", "asr_timeout", "cat", "ca", "录音或识别超时"},
		{"matched", "asr", "cat", "cat", "转写与目标一致，可能是提交链路延迟"},
		{"mismatch", "asr", "cat", "cut", "转写未与目标文本匹配"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diagnosis, suggestion, confidence := classifyTeacherFailure(tc.kind, tc.target, tc.transcript)
			if diagnosis != tc.diagnosis || suggestion == "" || confidence <= 0 || confidence > 1 {
				t.Fatalf("unexpected fallback diagnosis=%q suggestion=%q confidence=%v", diagnosis, suggestion, confidence)
			}
		})
	}
}

func TestProcessNextTeacherAnalysisClaimsAndCompletesDurableJob(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/teacher-analysis.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	now := time.Now().UTC()
	app := NewApp(&fakeClock{t: now}, db)
	analysis := store.TeacherFailureAnalysis{
		LessonID: 3, EntryID: 8, StudentID: 13, FailureType: "asr_timeout",
		TargetText: "arrangement", Transcript: "arrange", AnalysisStatus: "pending",
		ImplementationStatus: "pending", AnalysisKey: "timeout-analysis", CreatedAt: now,
	}
	require.NoError(t, db.Create(&analysis).Error)
	job := store.TeacherAnalysisJob{ID: "timeout-job", AnalysisID: analysis.ID, AnalysisKey: analysis.AnalysisKey, Status: "queued", NextRunAt: now.Add(-time.Second), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&job).Error)

	require.NoError(t, app.ProcessNextTeacherAnalysis(context.Background(), "worker-1"))
	var got store.TeacherFailureAnalysis
	require.NoError(t, db.First(&got, analysis.ID).Error)
	require.Equal(t, "succeeded", got.AnalysisStatus)
	require.Equal(t, "录音或识别超时", got.Diagnosis)
	require.NotEmpty(t, got.Suggestion)
	require.Greater(t, got.Confidence, 0.0)
	var completed store.TeacherAnalysisJob
	require.NoError(t, db.First(&completed, "id = ?", job.ID).Error)
	require.Equal(t, "succeeded", completed.Status)
	require.Empty(t, completed.LeaseOwner)
}

func TestProcessNextTeacherAnalysisRejectsInvalidWorkerAndMissingAnalysis(t *testing.T) {
	err := (*App)(nil).ProcessNextTeacherAnalysis(context.Background(), "worker")
	require.ErrorIs(t, err, store.ErrTeacherJobUnavailable)

	db, err := store.Open(t.TempDir() + "/missing-analysis.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	app := NewApp(&fakeClock{t: time.Now()}, db)
	require.ErrorIs(t, app.ProcessNextTeacherAnalysis(context.Background(), " "), store.ErrTeacherJobUnavailable)
	require.ErrorIs(t, app.ProcessNextTeacherAnalysis(context.Background(), "worker"), store.ErrTeacherJobUnavailable)

	now := time.Now().UTC()
	job := store.TeacherAnalysisJob{ID: "orphan-job", AnalysisID: 999, AnalysisKey: "orphan-analysis", Status: "queued", NextRunAt: now.Add(-time.Second), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&job).Error)
	require.Error(t, app.ProcessNextTeacherAnalysis(context.Background(), "worker"))
	var failed store.TeacherAnalysisJob
	require.NoError(t, db.First(&failed, "id = ?", job.ID).Error)
	require.Equal(t, "failed", failed.Status)
	require.Equal(t, "analysis_not_found", failed.LastError)
}

func TestEnqueueTeacherFailureIsIdempotentAndVariantCanMatch(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/teacher-enqueue.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	app := NewApp(&fakeClock{t: time.Now()}, db)
	app.enqueueTeacherFailure(1, 2, 3, "ASAP", "as soon as possible", "repeat_mismatch", "epoch-1")
	app.enqueueTeacherFailure(1, 2, 3, "ASAP", "as soon as possible", "repeat_mismatch", "epoch-1")
	var analyses int64
	require.NoError(t, db.Model(&store.TeacherFailureAnalysis{}).Count(&analyses).Error)
	require.EqualValues(t, 1, analyses)
	var jobs int64
	require.NoError(t, db.Model(&store.TeacherAnalysisJob{}).Count(&jobs).Error)
	require.EqualValues(t, 1, jobs)

	entryID := int64(17)
	require.NoError(t, db.Create(&store.WordPronunciationVariant{EntryID: entryID, Variant: "as soon as possible", Kind: "rule_normalized", Source: "test", Confidence: 1, Enabled: true, Version: 1}).Error)
	require.Equal(t, "matched", app.evaluateRepeatWithVariants(entryID, "ASAP", "as soon as possible"))
	require.Equal(t, "no_match", app.evaluateRepeatWithVariants(entryID, "ASAP", "banana"))

}

func TestScanPronunciationVariantsIsIdempotent(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/variants.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	app := NewApp(&fakeClock{t: time.Now()}, db)
	book := store.WordBook{ID: 44, Name: "variant-book", Grade: "g7", Status: "published"}
	require.NoError(t, db.Create(&book).Error)
	require.NoError(t, db.Create(&store.WordEntry{ID: 441, BookID: book.ID, Word: "sth.", NormKey: "sth", Meanings: `["某事"]`}).Error)
	created, err := app.ScanPronunciationVariants(book.ID)
	require.NoError(t, err)
	require.Equal(t, 1, created)
	created, err = app.ScanPronunciationVariants(book.ID)
	require.NoError(t, err)
	require.Equal(t, 0, created)
	var variants []store.WordPronunciationVariant
	require.NoError(t, db.Where("entry_id=?", 441).Find(&variants).Error)
	require.Len(t, variants, 1)
	require.Equal(t, "something", variants[0].Variant)
}

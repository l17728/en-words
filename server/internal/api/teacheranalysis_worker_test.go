package api

import (
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

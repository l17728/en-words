package store

import (
	"enwords/internal/wordbook"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestClassroomBatchReadsOnlyRequestedWords(t *testing.T) {
	db, _ := openTest(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	repo := NewWordbookRepo(db)
	book := &wordbook.Book{ID: 1, Name: "large", Grade: "g7", Status: wordbook.StatusDraft}
	other := &wordbook.Book{ID: 2, Name: "other", Grade: "g8", Status: wordbook.StatusDraft}
	require.NoError(t, repo.CreateBook(book))
	require.NoError(t, repo.CreateBook(other))
	rows := make([]WordEntry, 1000)
	for i := range rows {
		rows[i] = WordEntry{ID: int64(i + 1), BookID: book.ID, Word: fmt.Sprintf("word%d", i), NormKey: fmt.Sprintf("word%d", i), Meanings: `["meaning"]`}
	}
	require.NoError(t, db.CreateInBatches(rows, 100).Error)
	require.NoError(t, db.Create(&WordEntry{ID: 1001, BookID: other.ID, Word: "foreign", NormKey: "foreign", Meanings: `["foreign"]`}).Error)
	var fetched int64
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:batch_rows", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]WordEntry); ok && tx.RowsAffected > 0 {
			fetched += tx.RowsAffected
		}
	}))
	svc := wordbook.NewService(repo)
	result, err := svc.EntriesByIDs(book.ID, []int64{999, 2, 999, 1001, 99999})
	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Equal(t, int64(999), result[0].ID)
	require.Equal(t, int64(2), result[1].ID)
	require.Equal(t, int64(999), result[2].ID)
	require.LessOrEqual(t, fetched, int64(2), "50-word classroom batches must not read the entire book")
	fetched = 0
	empty, err := svc.EntriesByIDs(book.ID, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Zero(t, fetched)
	ids := make([]int64, 1000)
	for i := range ids {
		ids[i] = int64(1000 - i)
	}
	result, err = svc.EntriesByIDs(book.ID, ids)
	require.NoError(t, err)
	require.Len(t, result, 1000)
	for i, entry := range result {
		require.Equal(t, ids[i], entry.ID)
	}
	require.EqualValues(t, 1000, fetched)
	var batches int
	failure := errors.New("batch read unavailable")
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:batch_failure", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]WordEntry); ok {
			batches++
			if batches == 2 {
				tx.AddError(failure)
			}
		}
	}))
	result, err = svc.EntriesByIDs(book.ID, ids)
	require.ErrorIs(t, err, failure)
	require.Nil(t, result, "partial batches must not escape on storage failure")
}

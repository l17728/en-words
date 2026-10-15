package wordbook

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

type importNonAtomicRepo struct{ Repo }

func TestImportAtomicMemoryBoundary(t *testing.T) {
	repo := NewMemRepo()
	svc := NewService(repo)
	book, err := svc.CreateBook("atomic", "g7", "")
	require.NoError(t, err)
	inputs := []EntryInput{{Word: "Apple", Meanings: []string{"苹果"}}, {Word: "apple", Meanings: []string{"重复"}}, {Word: "", Meanings: []string{"空"}}, {Word: "bee", Meanings: []string{"蜂"}}}
	result, err := svc.ImportBatchContext(nil, book.ID, inputs)
	require.NoError(t, err)
	require.Equal(t, 2, result.Inserted)
	require.Equal(t, []string{"apple", ""}, result.Skipped)
	stored, err := svc.GetBook(book.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, stored.WordCount)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = svc.ImportBatchContext(ctx, book.ID, []EntryInput{{Word: "cat", Meanings: []string{"猫"}}})
	require.Error(t, err)
	require.Nil(t, result)
	result, err = repo.ImportEntriesAtomic(ctx, book.ID, inputs)
	require.Error(t, err)
	require.Nil(t, result)
	result, err = svc.ImportBatchContext(context.Background(), 999, inputs)
	require.ErrorIs(t, err, ErrBookNotFound)
	require.Nil(t, result)
	legacy := NewService(importNonAtomicRepo{repo})
	result, err = legacy.ImportBatchContext(context.Background(), book.ID, inputs)
	require.Error(t, err)
	require.Nil(t, result)
	stored, err = svc.GetBook(book.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, stored.WordCount)
}

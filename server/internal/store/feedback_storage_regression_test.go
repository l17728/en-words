package store

import (
	"enwords/internal/scheduler"
	"enwords/internal/wordbook"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFeedbackStorageOutageIsNotMissingResource(t *testing.T) {
	db := reopenableFixture(t)()
	books := NewWordbookRepo(db)
	_, err := books.GetBook(999)
	require.ErrorIs(t, err, wordbook.ErrBookNotFound)
	_, err = books.EntryByKey(999, "cat")
	require.ErrorIs(t, err, wordbook.ErrEntryNotFound)
	_, err = books.GetEntry(999)
	require.ErrorIs(t, err, wordbook.ErrEntryNotFound)
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	_, err = books.GetBook(999)
	require.Error(t, err)
	require.NotErrorIs(t, err, wordbook.ErrBookNotFound)
	_, err = books.EntryByKey(999, "cat")
	require.Error(t, err)
	require.NotErrorIs(t, err, wordbook.ErrEntryNotFound)
	_, err = books.GetEntry(999)
	require.Error(t, err)
	require.NotErrorIs(t, err, wordbook.ErrEntryNotFound)
	require.Error(t, NewSchedulerRepo(db).SaveCard(&scheduler.Card{StudentID: 7, BookID: 3, WordID: 9, Status: scheduler.StatusLearning}))
	_, err = NewConfigPersister(db).Revisions(7, "wordsPerLesson")
	require.Error(t, err)
}

func TestFeedbackConfigRevisionsSurviveABADeletionAndReopen(t *testing.T) {
	open := reopenableFixture(t)
	db := open()
	p := NewConfigPersister(db)
	initial, err := p.Revisions(7, "wordsPerLesson")
	require.NoError(t, err)
	require.NoError(t, p.SaveGlobal("wordsPerLesson", 60))
	first, err := p.Revisions(7, "wordsPerLesson")
	require.NoError(t, err)
	require.Greater(t, first.Global, initial.Global)
	require.NoError(t, p.SaveGlobal("wordsPerLesson", 70))
	require.NoError(t, p.SaveGlobal("wordsPerLesson", 60))
	second, err := p.Revisions(7, "wordsPerLesson")
	require.NoError(t, err)
	require.Equal(t, first.Global+2, second.Global)
	require.NoError(t, p.SaveOverride(7, "wordsPerLesson", 60))
	third, err := p.Revisions(7, "wordsPerLesson")
	require.NoError(t, err)
	require.Greater(t, third.Student, second.Student)
	require.NoError(t, p.SaveOverride(7, "wordsPerLesson", 70))
	require.NoError(t, p.SaveOverride(7, "wordsPerLesson", 60))
	require.NoError(t, p.DeleteOverride(7, "wordsPerLesson"))
	final, err := NewConfigPersister(open()).Revisions(7, "wordsPerLesson")
	require.NoError(t, err)
	require.Equal(t, third.Student+3, final.Student)
	require.Equal(t, second.Global, final.Global)
	other, err := p.Revisions(8, "wordsPerLesson")
	require.NoError(t, err)
	require.Zero(t, other.Student)
	require.Equal(t, final.Global, other.Global)
}

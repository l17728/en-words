package scheduler

import (
	"enwords/internal/config"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMemoryTransactionIsolatesLessonAndCardPointers(t *testing.T) {
	repo := NewMemRepo()
	cfg, _ := config.NewStore(nil, nil)
	s := NewService(repo, cfg, RealClock{})
	at := time.Now()
	require.NoError(t, repo.CreateCard(&Card{StudentID: 1, BookID: 1, WordID: 1, NextDueAt: &at}))
	l := &Lesson{StudentID: 1, BookID: 1, Status: "scheduled", OpenAt: at, BatchWordIDs: []int64{1}}
	require.NoError(t, repo.CreateLesson(l))
	require.Error(t, s.WithMemoryTransaction(cfg, func(w *Service) error {
		require.NoError(t, w.PostponeLesson(l.ID, at.Add(24*time.Hour)))
		return errors.New("rollback")
	}))
	got, err := s.Lesson(l.ID)
	require.NoError(t, err)
	require.True(t, at.Equal(got.OpenAt))
	c, err := s.GetCard(1, 1, 1)
	require.NoError(t, err)
	require.True(t, c.NextDueAt.Equal(at))
	require.NoError(t, s.WithMemoryTransaction(cfg, func(w *Service) error { return w.PostponeLesson(l.ID, at.Add(24*time.Hour)) }))
	got, err = s.Lesson(l.ID)
	require.NoError(t, err)
	require.True(t, at.Add(24*time.Hour).Equal(got.OpenAt))
}

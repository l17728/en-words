package scheduler

import (
	"enwords/internal/config"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

type statsFailRepo struct {
	*MemRepo
	stage string
	err   error
}

func (r *statsFailRepo) StudentBooks(sid int64) ([]int64, error) {
	if r.stage == "books" {
		return nil, r.err
	}
	return r.MemRepo.StudentBooks(sid)
}
func (r *statsFailRepo) ListCards(sid, bid int64) ([]*Card, error) {
	if r.stage == "cards" {
		return nil, r.err
	}
	return r.MemRepo.ListCards(sid, bid)
}
func (r *statsFailRepo) ListLessons(sid, bid int64) ([]*Lesson, error) {
	if r.stage == "lessons" {
		return nil, r.err
	}
	return r.MemRepo.ListLessons(sid, bid)
}

func TestStatisticsDataPropagatesEveryRepositoryFailure(t *testing.T) {
	for _, stage := range []string{"books", "cards", "lessons"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("fixture repository unavailable")
			repo := &statsFailRepo{MemRepo: NewMemRepo(), stage: stage, err: failure}
			require.NoError(t, repo.CreateCard(&Card{StudentID: 7, BookID: 1, WordID: 1, Status: StatusNew}))
			cfg, err := config.NewStore(nil, nil)
			require.NoError(t, err)
			svc := NewService(repo, cfg, nil)
			cards, lessons, err := svc.StatisticsData(7)
			require.ErrorIs(t, err, failure)
			require.Nil(t, cards)
			require.Nil(t, lessons)
		})
	}
}

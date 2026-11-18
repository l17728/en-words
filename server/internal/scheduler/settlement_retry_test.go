package scheduler

import (
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type secondCardFailureRepo struct {
	*MemRepo
	writes int
	failed bool
}

func (r *secondCardFailureRepo) SaveCard(card *Card) error {
	r.writes++
	if r.writes == 2 && !r.failed {
		r.failed = true
		return errors.New("fixture second card failure")
	}
	return r.MemRepo.SaveCard(card)
}

func TestSettlementRetryDoesNotAdvanceAlreadySavedCardTwice(t *testing.T) {
	_, clock, cfg := newSched(t)
	repo := &secondCardFailureRepo{MemRepo: NewMemRepo()}
	service := NewService(repo, cfg, clock)
	for _, wid := range []int64{1, 2} {
		require.NoError(t, repo.CreateCard(&Card{StudentID: 7, BookID: 3, WordID: wid, Status: StatusLearning, LadderStep: 1}))
	}
	lesson := &Lesson{StudentID: 7, BookID: 3, Status: LessonScheduledStr, BatchWordIDs: []int64{1, 2}, OpenAt: clock.Now()}
	require.NoError(t, repo.CreateLesson(lesson))
	at := clock.Now()
	require.Error(t, service.SettleLesson(lesson.ID, map[int64]bool{1: true, 2: true}, at))
	first, err := repo.GetCard(7, 3, 1)
	require.NoError(t, err)
	require.Equal(t, 2, first.LadderStep)
	second, err := repo.GetCard(7, 3, 2)
	require.NoError(t, err)
	require.Equal(t, 1, second.LadderStep)
	current, err := repo.GetLesson(lesson.ID)
	require.NoError(t, err)
	require.Equal(t, LessonScheduledStr, current.Status)
	// A retry's later wall clock and changed payload cannot rewrite the frozen result.
	require.NoError(t, service.SettleLesson(lesson.ID, map[int64]bool{1: false, 2: false}, at.Add(time.Hour)))
	require.NoError(t, service.SettleLesson(lesson.ID, map[int64]bool{1: true, 2: true}, at.Add(2*time.Hour)))
	for _, wid := range []int64{1, 2} {
		card, err := repo.GetCard(7, 3, wid)
		require.NoError(t, err)
		require.Equal(t, 2, card.LadderStep)
		require.Equal(t, at, *card.LastReviewAt)
	}
	current, err = repo.GetLesson(lesson.ID)
	require.NoError(t, err)
	require.Equal(t, LessonFinishedStr, current.Status)
	require.Equal(t, at, *current.FinishedAt)
	require.Empty(t, service.pendingSettlements)
}

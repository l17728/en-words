package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchedulerTransactionDatesAndWordSetsAreIsolatedUntilCommit(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			service, clock, cfg := newSched(t)
			repo := service.repo.(*MemRepo)
			last, due, finished := clock.Now(), clock.Now().Add(24*time.Hour), clock.Now().Add(time.Hour)
			originalLast, originalDue, originalFinished := last, due, finished
			key := cardKey(7, 1, 9)
			require.NoError(t, repo.CreateCard(&Card{StudentID: 7, BookID: 1, WordID: 9, Status: StatusLearning, LastReviewAt: &last, NextDueAt: &due}))
			require.NoError(t, repo.CreateLesson(&Lesson{StudentID: 7, BookID: 1, Status: LessonFinishedStr, FinishedAt: &finished, BatchWordIDs: []int64{9, 10}}))
			originalNext := repo.nextLesson
			abort := errors.New("fixture rollback")
			err := service.WithMemoryTransaction(cfg, func(working *Service) error {
				cp := working.repo.(*MemRepo)
				*cp.cards[key].LastReviewAt = last.Add(48 * time.Hour)
				*cp.cards[key].NextDueAt = due.Add(48 * time.Hour)
				*cp.lessons[0].FinishedAt = finished.Add(48 * time.Hour)
				cp.lessons[0].BatchWordIDs[0] = 99
				require.NoError(t, cp.CreateLesson(&Lesson{StudentID: 7, BookID: 1, Status: LessonScheduledStr}))
				require.Equal(t, originalLast, *repo.cards[key].LastReviewAt)
				require.Equal(t, originalDue, *repo.cards[key].NextDueAt)
				require.Equal(t, originalFinished, *repo.lessons[0].FinishedAt)
				require.Equal(t, []int64{9, 10}, repo.lessons[0].BatchWordIDs)
				require.Len(t, repo.lessons, 1)
				require.Equal(t, originalNext, repo.nextLesson)
				if !commit {
					return abort
				}
				return nil
			})
			if commit {
				require.NoError(t, err)
				require.Equal(t, originalLast.Add(48*time.Hour), *repo.cards[key].LastReviewAt)
				require.Equal(t, originalDue.Add(48*time.Hour), *repo.cards[key].NextDueAt)
				require.Equal(t, originalFinished.Add(48*time.Hour), *repo.lessons[0].FinishedAt)
				require.Equal(t, []int64{99, 10}, repo.lessons[0].BatchWordIDs)
				require.Len(t, repo.lessons, 2)
				require.Equal(t, originalNext+1, repo.nextLesson)
			} else {
				require.ErrorIs(t, err, abort)
				require.Equal(t, originalLast, *repo.cards[key].LastReviewAt)
				require.Equal(t, originalDue, *repo.cards[key].NextDueAt)
				require.Equal(t, originalFinished, *repo.lessons[0].FinishedAt)
				require.Equal(t, []int64{9, 10}, repo.lessons[0].BatchWordIDs)
				require.Len(t, repo.lessons, 1)
				require.Equal(t, originalNext, repo.nextLesson)
			}
		})
	}
}

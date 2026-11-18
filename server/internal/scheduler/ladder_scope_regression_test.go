package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type basicRepo struct{ Repo }

func TestLadderScopeConfigurationWorkAndClamping(t *testing.T) {
	s, _, cfg := newSched(t)
	s.seedWords(t, 7, 1, 1, 2)
	s.seedWords(t, 8, 2, 3)
	for _, tc := range []struct {
		student, book, word int64
		step                int
	}{{7, 1, 1, 8}, {7, 1, 2, 2}, {8, 2, 3, 7}} {
		c, err := s.repo.GetCard(tc.student, tc.book, tc.word)
		require.NoError(t, err)
		c.LadderStep = tc.step
		require.NoError(t, s.repo.SaveCard(c))
	}

	ids, err := s.CardStudentIDs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int64{7, 8}, ids, "students with cards are unique and sorted")

	changed, err := s.ClampLadderSteps(context.Background(), 7, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 1, changed)
	seven, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 3, seven.LadderStep)
	eight, _ := s.repo.GetCard(8, 2, 3)
	assert.Equal(t, 7, eight.LadderStep, "clamping one student must not mutate another")
	changed, err = s.ClampLadderSteps(context.Background(), 7, 3)
	require.NoError(t, err)
	assert.Zero(t, changed, "reapplying the same limit is idempotent")

	_, err = s.ClampLadderSteps(context.Background(), 0, 3)
	assert.ErrorContains(t, err, "invalid ladder scope")
	_, err = s.ClampLadderSteps(context.Background(), 7, -1)
	assert.ErrorContains(t, err, "invalid ladder scope")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.CardStudentIDs(canceled)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.ClampLadderSteps(canceled, 7, 3)
	assert.ErrorIs(t, err, context.Canceled)

	// Normal services are guarded while a config work view owns the shared repo.
	s.RequireConfigurationWork()
	_, err = s.PlanNext(7, 1)
	assert.ErrorContains(t, err, "configuration_work_required")
	assert.ErrorContains(t, s.SetttleLessonFinishedForTest(7, 1), "configuration_work_required")
	assert.ErrorContains(t, s.MaintenanceCheck(7, 1, 1, true), "configuration_work_required")
	assert.ErrorContains(t, s.TruncateLadderSteps(2), "explicit configuration scope required")

	view := s.ConfigWorkView(cfg)
	assert.Same(t, s, view.workOwner)
	assert.NotSame(t, s, view)
	assert.NoError(t, view.SetttleLessonFinishedForTest(7, 1))
}

func (s *Service) SetttleLessonFinishedForTest(sid, bid int64) error {
	return s.SettleLessonFinished(sid, bid, nil, time.Now())
}

func TestLadderScopeUnavailableForRepositoriesWithoutAtomicCapability(t *testing.T) {
	s, _, _ := newSched(t)
	s.repo = basicRepo{Repo: s.repo}
	_, err := s.CardStudentIDs(context.Background())
	assert.ErrorContains(t, err, "atomic_configuration_unavailable")
	_, err = s.ClampLadderSteps(context.Background(), 7, 3)
	assert.ErrorContains(t, err, "atomic_configuration_unavailable")
}

func TestDueWordsAndProgressAreReadOnlyAndRespectClockBoundary(t *testing.T) {
	s, clk, _ := newSched(t)
	due := clk.Now()
	future := due.Add(time.Second)
	for _, c := range []*Card{
		{StudentID: 7, BookID: 1, WordID: 1, Status: StatusNew, NextDueAt: &due},
		{StudentID: 7, BookID: 1, WordID: 2, Status: StatusLearning, NextDueAt: &future},
		{StudentID: 7, BookID: 1, WordID: 3, Status: StatusMastered},
		{StudentID: 7, BookID: 1, WordID: 4, Status: StatusGraduated, NextDueAt: &due},
		{StudentID: 8, BookID: 1, WordID: 9, Status: StatusNew, NextDueAt: &due},
	} {
		require.NoError(t, s.repo.CreateCard(c))
	}
	got, err := s.DueWords(7, 1)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 4}, got)
	progress, err := s.Progress(7, 1)
	require.NoError(t, err)
	assert.Equal(t, ProgressSummary{Total: 4, New: 1, Learning: 1, Mastered: 1, Graduated: 1, Due: 2}, progress)
	lessons, err := s.Lessons(7, 1)
	require.NoError(t, err)
	assert.Empty(t, lessons, "read models do not create/reserve lessons")
}

func TestAllLessonsCopiesAndReplanExpiredValidatesTargetAndConflict(t *testing.T) {
	s, clk, _ := newSched(t)
	s.seedWords(t, 7, 1, 1, 2)
	original := &Lesson{StudentID: 7, BookID: 1, Status: LessonScheduledStr, OpenAt: clk.Now().Add(-day), BatchWordIDs: []int64{1, 2}}
	require.NoError(t, s.repo.CreateLesson(original))
	all, err := s.AllLessons()
	require.NoError(t, err)
	require.Len(t, all, 1)
	all[0].BatchWordIDs[0] = 999
	stored, _ := s.Lesson(original.ID)
	assert.Equal(t, []int64{1, 2}, stored.BatchWordIDs, "query returns detached lesson data")

	_, err = s.ReplanExpired(original.ID, original.OpenAt.Add(time.Second), clk.Now().Add(2*day))
	assert.ErrorIs(t, err, ErrOverdueStale)
	_, err = s.ReplanExpired(original.ID, original.OpenAt, clk.Now())
	assert.ErrorContains(t, err, "target must be future")
	next, err := s.ReplanExpired(original.ID, original.OpenAt, clk.Now().Add(3*day))
	require.NoError(t, err)
	assert.Equal(t, LessonScheduledStr, next.Status)
	assert.Equal(t, []int64{1, 2}, next.BatchWordIDs)
	expired, _ := s.Lesson(original.ID)
	assert.Equal(t, LessonExpiredStr, expired.Status)

	other := &Lesson{StudentID: 8, BookID: 2, Status: LessonScheduledStr, OpenAt: clk.Now().Add(-day)}
	require.NoError(t, s.repo.CreateLesson(other))
	_, err = s.ReplanExpired(other.ID, other.OpenAt, clk.Now().Add(day))
	assert.NoError(t, err, "different student/book plans do not conflict")

	conflicted := &Lesson{StudentID: 9, BookID: 3, Status: LessonScheduledStr, OpenAt: clk.Now().Add(-day), BatchWordIDs: []int64{8}}
	require.NoError(t, s.repo.CreateLesson(conflicted))
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 9, BookID: 3, Status: LessonScheduledStr, OpenAt: clk.Now().Add(2 * day)}))
	_, err = s.ReplanExpired(conflicted.ID, conflicted.OpenAt, clk.Now().Add(4*day))
	assert.ErrorIs(t, err, ErrOverdueConflict, "an existing scheduled lesson prevents a duplicate plan")
}

func TestMaintenanceAndTruncateRespectBusinessScope(t *testing.T) {
	s, clk, _ := newSched(t)
	s.seedWords(t, 7, 1, 1, 2)
	card, _ := s.repo.GetCard(7, 1, 1)
	card.Status, card.LadderStep = StatusGraduated, 8
	require.NoError(t, s.repo.SaveCard(card))
	require.NoError(t, s.MaintenanceCheck(7, 1, 1, true))
	card, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusGraduated, card.Status, "correct maintenance check preserves graduation")
	assert.Equal(t, clk.Now(), *card.LastReviewAt)
	require.NoError(t, s.MaintenanceCheck(7, 1, 1, false))
	card, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusLearning, card.Status)
	assert.Equal(t, 5, card.LadderStep)
	assert.Equal(t, clk.Now().Add(7*day), *card.NextDueAt, "step five uses the sixth default interval")

	other, _ := s.repo.GetCard(7, 1, 2)
	other.LadderStep = 9
	require.NoError(t, s.repo.SaveCard(other))
	require.NoError(t, s.TruncateLadderSteps(3))
	got, _ := s.repo.GetCard(7, 1, 2)
	assert.Equal(t, 2, got.LadderStep)
	assert.ErrorContains(t, s.TruncateLadderSteps(0), "explicit configuration scope required")
	wrapped := NewService(basicRepo{Repo: s.repo}, s.cfg, s.clock)
	assert.ErrorContains(t, wrapped.TruncateLadderSteps(2), "explicit configuration scope required")
}

func TestReplacementResetVoidsOnlyScheduledAndPreservesMastery(t *testing.T) {
	s, clk, _ := newSched(t)
	for _, c := range []*Card{
		{StudentID: 7, BookID: 1, WordID: 1, Status: StatusLearning, LadderStep: 2, BoostFlag: true, NextDueAt: ptrTime(clk.Now())},
		{StudentID: 7, BookID: 1, WordID: 2, Status: StatusMastered, LadderStep: 3, BoostFlag: true, NextDueAt: ptrTime(clk.Now())},
		{StudentID: 7, BookID: 1, WordID: 3, Status: StatusGraduated, LadderStep: 4, NextDueAt: ptrTime(clk.Now())},
	} {
		require.NoError(t, s.repo.CreateCard(c))
	}
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 7, BookID: 1, Status: LessonScheduledStr, BatchWordIDs: []int64{1, 2, 3}}))
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 7, BookID: 1, Status: LessonFinishedStr, BatchWordIDs: []int64{1}}))
	require.NoError(t, s.ResetForReplacement(7, 1))
	lessons, _ := s.Lessons(7, 1)
	assert.Equal(t, "voided_replacement", lessons[0].Status)
	assert.Equal(t, LessonFinishedStr, lessons[1].Status)
	learning, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusNew, learning.Status)
	assert.Equal(t, 2, learning.LadderStep, "new status excludes the old ladder position from planning")
	assert.False(t, learning.BoostFlag)
	assert.Nil(t, learning.NextDueAt)
	mastered, _ := s.repo.GetCard(7, 1, 2)
	assert.Equal(t, StatusMastered, mastered.Status)
	assert.Equal(t, 3, mastered.LadderStep)
	graduated, _ := s.repo.GetCard(7, 1, 3)
	assert.Equal(t, StatusGraduated, graduated.Status)
}

func ptrTime(t time.Time) *time.Time { return &t }

type failLessonsRepo struct {
	Repo
	err error
	all []*Lesson
}

func (r failLessonsRepo) ListLessonsAll() ([]*Lesson, error) { return r.all, r.err }

func TestOverdueAndReadFailuresAreExplicit(t *testing.T) {
	s, _, _ := newSched(t)
	_, err := s.ReplanExpired(999, time.Time{}, time.Now())
	assert.ErrorContains(t, err, "lesson not found")
	failing := NewService(failLessonsRepo{Repo: s.repo, err: errors.New("scan unavailable")}, s.cfg, s.clock)
	_, err = failing.AllLessons()
	assert.EqualError(t, err, "scan unavailable")
	assert.Empty(t, failing.repoLessonsAll(), "background scan failure is not treated as an empty successful scan")

	finished := time.Now()
	originalFinished := finished
	rows := []*Lesson{{ID: 4, Status: LessonFinishedStr, FinishedAt: &finished, BatchWordIDs: []int64{5}}, nil}
	throughExtendedRepo := NewService(failLessonsRepo{Repo: s.repo, all: rows}, s.cfg, s.clock)
	copy, err := throughExtendedRepo.AllLessons()
	require.NoError(t, err)
	require.Len(t, copy, 2)
	require.NotNil(t, copy[0])
	assert.Nil(t, copy[1], "nil repository rows remain nil")
	assert.Equal(t, originalFinished, *copy[0].FinishedAt)
	assert.Equal(t, []int64{5}, copy[0].BatchWordIDs, "extended repository results are returned")
	copy[0].Status = LessonExpiredStr
	copy[0].BatchWordIDs[0] = 99
	*copy[0].FinishedAt = copy[0].FinishedAt.Add(time.Hour)
	assert.Equal(t, LessonFinishedStr, rows[0].Status, "Lesson value is detached")
	assert.Equal(t, []int64{5}, rows[0].BatchWordIDs, "BatchWordIDs backing array is detached")
	assert.Equal(t, originalFinished, *rows[0].FinishedAt, "FinishedAt pointer is detached")

	unsupported := NewService(basicRepo{Repo: s.repo}, s.cfg, s.clock)
	_, err = unsupported.AllLessons()
	assert.ErrorContains(t, err, "all lessons query unavailable")
}

func TestReservationPlacementDraftAndBookRemovalBoundaries(t *testing.T) {
	s, clk, _ := newSched(t)
	s.seedWords(t, 7, 1, 1, 2)
	s.seedWords(t, 8, 1, 1)
	due := clk.Now().Add(2 * day)
	require.NoError(t, s.ReserveCards(7, 1, []int64{1, 2}, due))
	for _, id := range []int64{1, 2} {
		c, err := s.repo.GetCard(7, 1, id)
		require.NoError(t, err)
		assert.Equal(t, due, *c.NextDueAt)
	}
	assert.ErrorIs(t, s.ReserveCards(7, 1, []int64{99}, due), ErrCardNotFound)

	repo := s.repo.(*MemRepo)
	require.NoError(t, repo.SavePlacementDraft(7, 1, []int64{2, 1}))
	draft, err := repo.LoadPlacementDraft(7, 1)
	require.NoError(t, err)
	assert.Equal(t, []int64{2, 1}, draft)
	draft[0] = 999
	draftAgain, _ := repo.LoadPlacementDraft(7, 1)
	assert.Equal(t, []int64{2, 1}, draftAgain, "draft callers cannot mutate stored state")

	key := cardKey(7, 1, 1)
	replacement := &Card{StudentID: 7, BookID: 1, WordID: 99, Status: StatusLearning}
	require.NoError(t, repo.SaveCardDirect(key, replacement))
	replacement.Status = StatusGraduated
	saved, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusLearning, saved.Status, "direct memory persistence copies caller data")

	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 7, BookID: 1, Status: LessonScheduledStr}))
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 8, BookID: 1, Status: LessonScheduledStr}))
	require.NoError(t, s.RemoveBook(7, 1))
	books, err := s.StudentBooks(7)
	require.NoError(t, err)
	assert.Empty(t, books)
	remaining, _ := s.StudentLessons(8)
	assert.Len(t, remaining, 1, "removing one student's book preserves other users' data")
}

func TestIntervalClampsEmptyNegativeAndOversizedSteps(t *testing.T) {
	assert.Equal(t, 1, interval(nil, 0), "empty ladder has a safe one-day interval")
	ladder := []int{2, 5, 9}
	assert.Equal(t, 2, interval(ladder, -1))
	assert.Equal(t, 9, interval(ladder, 99))
}

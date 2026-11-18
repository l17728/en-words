package scheduler

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedPolicyCards(t *testing.T, s *Service, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		s.seedWords(t, 7, 1, int64(i))
	}
}

func policyHistory(t *testing.T, s *Service) {
	t.Helper()
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 7, BookID: 1, Status: LessonExpiredStr}))
}

func TestPlanPolicyDefaultAndFirstLesson(t *testing.T) {
	for _, ratio := range []float64{0, .4, 1} {
		s, _, _ := newSched(t)
		seedPolicyCards(t, s, 100)
		// Placement knowledge alone is not previous teaching.
		c, err := s.repo.GetCard(7, 1, 100)
		require.NoError(t, err)
		c.Status = StatusMastered
		require.NoError(t, s.repo.SaveCard(c))
		l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: ratio})
		require.NoError(t, err)
		require.Len(t, l.BatchWordIDs, 60)
		require.Equal(t, int64(1), l.BatchWordIDs[0])
	}
	s, _, _ := newSched(t)
	other, _, _ := newSched(t)
	seedPolicyCards(t, s, 100)
	seedPolicyCards(t, other, 100)
	policyHistory(t, s)
	policyHistory(t, other)
	a, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	b, err := other.PlanNextWithPolicy(7, 1, PlanPolicy{})
	require.NoError(t, err)
	require.Equal(t, a, b)
}

func TestPlanPolicyRejectsInvalidWithoutWriting(t *testing.T) {
	for _, ratio := range []float64{-1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		s, _, _ := newSched(t)
		seedPolicyCards(t, s, 2)
		_, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: ratio})
		require.ErrorIs(t, err, ErrInvalidPlanPolicy)
		ls, err := s.repo.ListLessons(7, 1)
		require.NoError(t, err)
		require.Empty(t, ls)
		c, err := s.repo.GetCard(7, 1, 1)
		require.NoError(t, err)
		require.Nil(t, c.NextDueAt)
	}
}

func TestPlanPolicyNoDueDoesNotWaiveLimitOrPersistPolicy(t *testing.T) {
	s, _, cfg := newSched(t)
	seedPolicyCards(t, s, 100)
	policyHistory(t, s)
	l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Len(t, l.BatchWordIDs, 24)
	require.Equal(t, 60, cfg.Int(7, "wordsPerLesson"))
	l.Status = LessonExpiredStr
	require.NoError(t, s.repo.SaveLesson(l))
	next, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	require.Len(t, next.BatchWordIDs, 60)
}

func TestPlanPolicyLearningEvidencePreventsFirstLessonException(t *testing.T) {
	for _, evidence := range []string{"learning", "graduated", "review", "step"} {
		t.Run(evidence, func(t *testing.T) {
			s, clk, _ := newSched(t)
			seedPolicyCards(t, s, 100)
			c, err := s.repo.GetCard(7, 1, 100)
			require.NoError(t, err)
			switch evidence {
			case "learning":
				c.Status = StatusLearning
			case "graduated":
				c.Status = StatusGraduated
			case "review":
				at := clk.Now()
				c.LastReviewAt = &at
			case "step":
				c.LadderStep = 1
			}
			require.NoError(t, s.repo.SaveCard(c))
			l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
			require.NoError(t, err)
			require.Len(t, l.BatchWordIDs, 24)
		})
	}
}

func TestPlanPolicyDuePriorityAndBoostedNewCount(t *testing.T) {
	s, clk, _ := newSched(t)
	seedPolicyCards(t, s, 100)
	for i := int64(1); i <= 50; i++ {
		c, err := s.repo.GetCard(7, 1, i)
		require.NoError(t, err)
		c.BoostFlag = true
		require.NoError(t, s.repo.SaveCard(c))
	}
	for i := int64(51); i <= 85; i++ {
		c, err := s.repo.GetCard(7, 1, i)
		require.NoError(t, err)
		at := clk.Now()
		c.Status, c.NextDueAt = StatusLearning, &at
		require.NoError(t, s.repo.SaveCard(c))
	}
	l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Len(t, l.BatchWordIDs, 59) // 35 due + 24 new; do not fill with a 25th new.
	for i := 0; i < 35; i++ {
		require.Equal(t, int64(51+i), l.BatchWordIDs[i])
	}
	for i := 0; i < 24; i++ {
		require.Equal(t, int64(1+i), l.BatchWordIDs[35+i])
	}
}

func TestPlanPolicyDueOverflowAndSmallBatch(t *testing.T) {
	s, clk, _ := newSched(t)
	seedPolicyCards(t, s, 100)
	for i := int64(1); i <= 70; i++ {
		c, err := s.repo.GetCard(7, 1, i)
		require.NoError(t, err)
		at := clk.Now()
		c.Status, c.NextDueAt = StatusLearning, &at
		require.NoError(t, s.repo.SaveCard(c))
	}
	l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Len(t, l.BatchWordIDs, 60)
	require.Equal(t, int64(60), l.BatchWordIDs[59])
	empty, _, _ := newSched(t)
	seedPolicyCards(t, empty, 10)
	policyHistory(t, empty)
	_, err = empty.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .001})
	require.ErrorIs(t, err, ErrNoWords)
	l, err = empty.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Len(t, l.BatchWordIDs, 10)
}

func TestPlanPolicySortsEarlierDueBeforeWordOrder(t *testing.T) {
	s, clk, _ := newSched(t)
	seedPolicyCards(t, s, 2)
	for i := int64(1); i <= 2; i++ {
		c, err := s.repo.GetCard(7, 1, i)
		require.NoError(t, err)
		at := clk.Now().Add(-day * time.Duration(i))
		c.Status, c.NextDueAt = StatusLearning, &at
		require.NoError(t, s.repo.SaveCard(c))
	}
	l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Equal(t, []int64{2, 1}, l.BatchWordIDs)
}

type policyHistoryFailure struct {
	Repo
	err error
}

func (r policyHistoryFailure) ListLessons(int64, int64) ([]*Lesson, error) { return nil, r.err }

func TestPlanPolicyHistoryFailurePropagatesForBothPaths(t *testing.T) {
	for _, ratio := range []float64{0, .4} {
		s, _, _ := newSched(t)
		seedPolicyCards(t, s, 10)
		base := s.repo
		failure := errors.New("history unavailable")
		s.repo = policyHistoryFailure{Repo: base, err: failure}
		_, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: ratio})
		require.ErrorIs(t, err, failure)
		ls, err := base.ListLessons(7, 1)
		require.NoError(t, err)
		require.Empty(t, ls)
	}
}

type policyBadSize struct {
	LadderSource
	size int
}

func (c policyBadSize) Int(sid int64, key string) int {
	if key == "wordsPerLesson" {
		return c.size
	}
	return c.LadderSource.Int(sid, key)
}

func TestPlanPolicyInvalidLessonSizeDoesNotPanic(t *testing.T) {
	for _, size := range []int{-1, 0, 49, 101} {
		s, _, _ := newSched(t)
		s.cfg = policyBadSize{LadderSource: s.cfg, size: size}
		_, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
		require.ErrorIs(t, err, ErrInvalidLessonSize)
	}
}

func TestPlanPolicyBoostedReviewAndTaskIsolation(t *testing.T) {
	s, clk, _ := newSched(t)
	seedPolicyCards(t, s, 100)
	// Other students/books cannot remove this student's first-lesson exception.
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 8, BookID: 1, Status: LessonFinishedStr}))
	require.NoError(t, s.repo.CreateLesson(&Lesson{StudentID: 7, BookID: 2, Status: LessonFinishedStr}))
	l, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Len(t, l.BatchWordIDs, 60)
	l.Status = LessonExpiredStr
	require.NoError(t, s.repo.SaveLesson(l))
	c, err := s.repo.GetCard(7, 1, 100)
	require.NoError(t, err)
	later := clk.Now().Add(10 * day)
	c.Status, c.NextDueAt, c.BoostFlag = StatusLearning, &later, true
	require.NoError(t, s.repo.SaveCard(c))
	calls := 0
	s.OnPlanned = func(sid, bid int64, lesson *Lesson) {
		require.Equal(t, int64(7), sid)
		require.Equal(t, int64(1), bid)
		require.NotNil(t, lesson)
		calls++
	}
	next, err := s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.NoError(t, err)
	require.Len(t, next.BatchWordIDs, 25) // boosted learning + 24 fresh
	require.Equal(t, int64(100), next.BatchWordIDs[0])
	require.Equal(t, 1, calls)
	_, err = s.PlanNextWithPolicy(7, 1, PlanPolicy{MaxNewRatio: .4})
	require.ErrorIs(t, err, ErrAlreadyPlanned)
	require.Equal(t, 1, calls)
}

package api

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func statsFixture(t *testing.T) (*App, *scheduler.MemRepo, *fakeClock) {
	t.Helper()
	a, clock := newTestApp(t)
	clock.t = time.Date(2026, 10, 13, 16, 30, 0, 0, time.UTC)
	repo := scheduler.NewMemRepo()
	a.Sched = scheduler.NewService(repo, a.Cfg, clock)
	a.reports = map[int64]*classroom.Result{}
	return a, repo, clock
}

func statsLesson(t *testing.T, a *App, repo scheduler.Repo, sid, bid int64, at time.Time) *scheduler.Lesson {
	t.Helper()
	l := &scheduler.Lesson{StudentID: sid, BookID: bid, Status: scheduler.LessonFinishedStr, OpenAt: at.Add(-time.Hour), FinishedAt: &at, BatchWordIDs: []int64{1, 2}}
	require.NoError(t, repo.CreateLesson(l))
	a.reports[l.ID] = &classroom.Result{TotalWords: 2, Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: .5}}
	return l
}

func TestStatisticsCategoriesIsolationAndRemoval(t *testing.T) {
	a, repo, clock := statsFixture(t)
	for i, status := range []scheduler.Status{scheduler.StatusNew, scheduler.StatusLearning, scheduler.StatusGraduated, scheduler.StatusMastered} {
		require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 1, WordID: int64(i + 1), Status: status}))
	}
	require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 2, WordID: 1, Status: scheduler.StatusLearning}))
	require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 8, BookID: 1, WordID: 1, Status: scheduler.StatusNew}))
	statsLesson(t, a, repo, 7, 1, clock.Now())
	statsLesson(t, a, repo, 8, 1, clock.Now())
	// Orphan history is excluded because this book is no longer enrolled.
	statsLesson(t, a, repo, 7, 99, clock.Now())
	got, err := a.StudentStatistics(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 3, got.Learned)
	require.Equal(t, 1, got.Graduated)
	require.Equal(t, 1, got.Remaining)
	require.Equal(t, 1, got.PlacementKnown)
	require.Equal(t, 1, got.FinishedLessons)
	other, err := a.StudentStatistics(context.Background(), 8)
	require.NoError(t, err)
	require.Zero(t, other.Learned)
	require.Equal(t, 1, other.Remaining)
	require.NoError(t, a.Sched.RemoveBook(7, 1))
	got, err = a.StudentStatistics(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 1, got.Learned)
	require.Zero(t, got.FinishedLessons)
	require.Empty(t, got.RecentLessons)
	empty, err := a.StudentStatistics(context.Background(), 9)
	require.NoError(t, err)
	require.NotNil(t, empty.RecentLessons)
	require.Empty(t, empty.RecentLessons)
	require.Zero(t, empty.StreakDays)
}

func TestStatisticsLongStreakRecentOrderAndFutureExcluded(t *testing.T) {
	a, repo, clock := statsFixture(t)
	require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 1, WordID: 1, Status: scheduler.StatusLearning}))
	var oldest int64
	for day := 0; day < 25; day++ {
		l := statsLesson(t, a, repo, 7, 1, clock.Now().AddDate(0, 0, -day))
		oldest = l.ID
	}
	delete(a.reports, oldest) // Reports outside the recent 20 are not required.
	newest := statsLesson(t, a, repo, 7, 1, clock.Now())
	future := statsLesson(t, a, repo, 7, 1, clock.Now().Add(time.Hour))
	delete(a.reports, future.ID)
	pending := statsLesson(t, a, repo, 7, 1, clock.Now())
	pending.Status = scheduler.LessonScheduledStr
	require.NoError(t, repo.SaveLesson(pending))
	delete(a.reports, pending.ID)
	got, err := a.StudentStatistics(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 26, got.FinishedLessons)
	require.Equal(t, 25, got.StreakDays)
	require.Len(t, got.RecentLessons, 20)
	require.Equal(t, newest.ID, got.RecentLessons[0].LessonID)
	require.Equal(t, .5, got.RecentLessons[0].FirstTryRate)
	for _, l := range got.RecentLessons {
		require.NotEqual(t, future.ID, l.LessonID)
	}
}

func TestStatisticsShanghaiBoundaryYesterdayAndGap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offsets []time.Duration
		want    int
	}{
		{"both_sides_of_midnight", []time.Duration{-40 * time.Minute, 0}, 2},
		{"yesterday_anchor", []time.Duration{-40 * time.Minute, -24*time.Hour - 40*time.Minute}, 2},
		{"gap", []time.Duration{-48 * time.Hour}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, repo, clock := statsFixture(t)
			require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 1, WordID: 1, Status: scheduler.StatusLearning}))
			for _, offset := range tc.offsets {
				statsLesson(t, a, repo, 7, 1, clock.Now().Add(offset))
			}
			got, err := a.StudentStatistics(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.StreakDays)
			require.Equal(t, "Asia/Shanghai", got.Timezone)
		})
	}
}

func TestStatisticsInvalidReportsFailExplicitly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report *classroom.Result
	}{
		{"missing", nil}, {"zero_structure", &classroom.Result{}},
		{"negative_total", &classroom.Result{Report: classroom.LessonReport{TotalWords: -1}}},
		{"negative_rate", &classroom.Result{Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: -.1}}},
		{"over_rate", &classroom.Result{Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: 1.1}}},
		{"nan", &classroom.Result{Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: math.NaN()}}},
		{"infinity", &classroom.Result{Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: math.Inf(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, repo, clock := statsFixture(t)
			require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 1, WordID: 1, Status: scheduler.StatusLearning}))
			l := statsLesson(t, a, repo, 7, 1, clock.Now())
			a.reports[l.ID] = tc.report
			got, err := a.StudentStatistics(context.Background(), 7)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestStatisticsSQLiteReopenCorruptionAndClosedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statistics.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	clock := &fakeClock{t: time.Date(2026, 10, 13, 16, 30, 0, 0, time.UTC)}
	a := NewApp(clock, db)
	a.reports = map[int64]*classroom.Result{}
	repo := store.NewSchedulerRepo(db)
	require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 1, WordID: 1, Status: scheduler.StatusLearning}))
	l := statsLesson(t, a, repo, 7, 1, clock.Now())
	raw, err := json.Marshal(a.reports[l.ID])
	require.NoError(t, err)
	key := "report/1"
	require.EqualValues(t, 1, l.ID)
	require.NoError(t, db.Create(&store.RuntimeRecord{Key: key, Value: string(raw)}).Error)
	before, err := a.StudentStatistics(context.Background(), 7)
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sql.Close())
	reopened, err := store.Open(path)
	require.NoError(t, err)
	a = NewApp(clock, reopened)
	handle, err := reopened.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = handle.Close() })
	after, err := a.StudentStatistics(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, value := range []string{"{", `{}`, `{"report":{"total_words":-2}}`} {
		require.NoError(t, reopened.Model(&store.RuntimeRecord{}).Where("key=?", key).Update("value", value).Error)
		got, err := a.StudentStatistics(context.Background(), 7)
		require.Error(t, err)
		require.Nil(t, got)
	}
	require.NoError(t, reopened.Where("key=?", key).Delete(&store.RuntimeRecord{}).Error)
	_, err = a.StudentStatistics(context.Background(), 7)
	require.Error(t, err)
	require.NoError(t, handle.Close())
	_, err = a.StudentStatistics(context.Background(), 7)
	require.Error(t, err)
}

func TestStatisticsRouteRejectsIdentityOverrideAndNonStudents(t *testing.T) {
	a, _ := newTestApp(t)
	_, err := a.Auth.CreateUser("stats-student", "fixturepw", "Student", auth.RoleStudent)
	require.NoError(t, err)
	student, _ := loginGet(t, a, "stats-student", "fixturepw")
	admin, _ := loginGet(t, a, "admin", "adminpw")
	service, err := a.Auth.SignService("system-agent")
	require.NoError(t, err)
	for _, token := range []string{"", admin, service} {
		resp := req2(t, a.Router(), "GET", "/api/v1/student/stats", token, nil)
		require.NotEqual(t, 0, resp.Code)
	}
	for _, query := range []string{"?student_id=999", "?book_id=2", "?unknown=value"} {
		resp := req2(t, a.Router(), "GET", "/api/v1/student/stats"+query, student, nil)
		require.Equal(t, errBadRequest, resp.Code)
	}
	resp := req2(t, a.Router(), "GET", "/api/v1/student/stats", student, nil)
	require.Equal(t, 0, resp.Code)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.StudentStatistics(canceled, 7)
	require.ErrorIs(t, err, context.Canceled)
}

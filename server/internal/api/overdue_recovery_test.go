package api

import (
	"context"
	"encoding/json"
	"enwords/internal/agent"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestOverdueExpiredPreparedPlanKeepsHistory(t *testing.T) {
	a, _, _, lid, _ := durableSettlementFixture(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now().Add(-25 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER pause_overdue BEFORE INSERT ON feedback_notices WHEN NEW.kind='overdue' BEGIN SELECT RAISE(FAIL,'fixture'); END`).Error)
	require.Error(t, a.RunOverdue(context.Background()))
	var old store.OverdueTask
	require.NoError(t, a.DB.First(&old).Error)
	a.Clock.(*fakeClock).t = a.Clock.Now().Add(96 * time.Hour)
	require.NoError(t, a.DB.Exec(`DROP TRIGGER pause_overdue`).Error)
	require.NoError(t, a.RunOverdue(context.Background()))
	var task store.OverdueTask
	require.NoError(t, a.DB.First(&task).Error)
	require.Equal(t, 2, task.Version)
	require.Equal(t, "completed", task.Status)
	var plan agent.OverduePlan
	require.NoError(t, json.Unmarshal([]byte(task.PlanJSON), &plan))
	require.True(t, plan.Target.After(a.Clock.Now()))
	var history []struct {
		Version int               `json:"version"`
		Plan    agent.OverduePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(task.PlanHistoryJSON), &history))
	require.Len(t, history, 1)
	require.Equal(t, 1, history[0].Version)
	var prior agent.OverduePlan
	require.NoError(t, json.Unmarshal([]byte(old.PlanJSON), &prior))
	require.Equal(t, prior, history[0].Plan)
}

func TestOverdueTwoInstancesCommitOneReplan(t *testing.T) {
	a, sid, bid, lid, _ := durableSettlementFixture(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now().Add(-25 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	b := NewApp(a.Clock, a.DB)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, app := range []*App{a, b} {
		wg.Add(1)
		go func(app *App) { defer wg.Done(); <-start; _ = app.RunOverdue(context.Background()) }(app)
	}
	close(start)
	wg.Wait()
	// A SQLite busy response may be retried, but no second business effect may commit.
	require.NoError(t, a.RunOverdue(context.Background()))
	rows, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	var notices int64
	require.NoError(t, a.DB.Model(&store.FeedbackNotice{}).Where("recipient=? AND kind=?", sid, "overdue").Count(&notices).Error)
	require.EqualValues(t, 1, notices)
	var done int64
	require.NoError(t, a.DB.Model(&store.OverdueTask{}).Where("status=?", "completed").Count(&done).Error)
	require.EqualValues(t, 1, done)
}

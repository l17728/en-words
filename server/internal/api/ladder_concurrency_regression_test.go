package api

import (
	"context"
	"enwords/internal/config"
	"enwords/internal/store"
	"fmt"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestLadderSecondStudentFailureRollsBackFirstStudent(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	for _, id := range []int64{sid, sid + 100} {
		require.NoError(t, a.Sched.SeedCard(id, 999, 999))
		c, e := a.Sched.GetCard(id, 999, 999)
		require.NoError(t, e)
		c.LadderStep = 6
		require.NoError(t, store.NewSchedulerRepo(a.DB).SaveCard(c))
	}
	sql := fmt.Sprintf("CREATE TRIGGER fail_second_student BEFORE UPDATE OF ladder_step ON memory_cards WHEN NEW.student_id=%d BEGIN SELECT RAISE(ABORT,'second student'); END", sid+100)
	require.NoError(t, a.DB.Exec(sql).Error)
	require.Error(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "all or nothing"))
	for _, id := range []int64{sid, sid + 100} {
		c, e := a.Sched.GetCard(id, 999, 999)
		require.NoError(t, e)
		require.Equal(t, 6, c.LadderStep)
	}
	view, e := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, e)
	require.Equal(t, config.DefaultLadder, view.Ladder.Effective)
	require.Zero(t, view.Ladder.Revision.Global)
}

func TestLadderTwoAppsConfigurationWritesRemainSerializable(t *testing.T) {
	a, b, sid := feedbackTwoApps(t)
	require.NoError(t, a.Sched.SeedCard(sid, 999, 999))
	c, e := a.Sched.GetCard(sid, 999, 999)
	require.NoError(t, e)
	c.LadderStep = 6
	require.NoError(t, store.NewSchedulerRepo(a.DB).SaveCard(c))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs <- a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "global shorten")
	}()
	go func() {
		defer wg.Done()
		<-start
		err := b.Cfg.SetStudent("admin", sid, "ladder", config.DefaultLadder, "student override")
		if err == nil {
			err = b.Cfg.DeleteOverride("admin", sid, "ladder")
		}
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	view, e := b.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, e)
	require.Equal(t, []int{1, 2, 3}, view.Ladder.Effective)
	c, e = a.Sched.GetCard(sid, 999, 999)
	require.NoError(t, e)
	require.Equal(t, 2, c.LadderStep)
}

func TestLadderGoalModesRespectExplicitLadderAndDueTimes(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	require.NoError(t, a.Sched.SeedCard(sid, 999, 999))
	before, e := a.Sched.GetCard(sid, 999, 999)
	require.NoError(t, e)
	require.NoError(t, a.Cfg.SetStudent("admin", sid, "goalMode", "exam_sprint", "goal"))
	view, e := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, e)
	require.Equal(t, config.SprintLadder, view.Ladder.Effective)
	require.NoError(t, a.Cfg.SetGlobal("admin", "ladder", []int{2, 3, 4}, "explicit"))
	view, e = a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, e)
	require.Equal(t, []int{2, 3, 4}, view.Ladder.Effective)
	require.Equal(t, "global", view.Ladder.Source)
	require.NoError(t, a.Cfg.DeleteOverride("admin", sid, "goalMode"))
	after, e := a.Sched.GetCard(sid, 999, 999)
	require.NoError(t, e)
	require.Equal(t, before.NextDueAt, after.NextDueAt)
	require.Equal(t, before.Status, after.Status)
	require.Equal(t, before.LastReviewAt, after.LastReviewAt)
}

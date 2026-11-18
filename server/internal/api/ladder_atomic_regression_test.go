package api

import (
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"testing"
)

// These regressions are authored before the implementation. Execution is deferred
// until the parent grants the isolated test window; no red/green is claimed yet.
func TestLadderAtomicGlobalHonorsStudentOverride(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	other := sid + 100
	require.NoError(t, a.Cfg.SetStudent("admin", other, "ladder", []int{1, 1, 2, 2, 4, 7, 14, 30}, "fixture"))
	for _, id := range []int64{sid, other} {
		require.NoError(t, a.Sched.SeedCard(id, 1, 1))
		c, e := a.Sched.GetCard(id, 1, 1)
		require.NoError(t, e)
		c.LadderStep = 6
		require.NoError(t, store.NewSchedulerRepo(a.DB).SaveCard(c))
	}
	require.NoError(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "shorten"))
	first, e := a.Sched.GetCard(sid, 1, 1)
	require.NoError(t, e)
	require.Equal(t, 2, first.LadderStep)
	second, e := a.Sched.GetCard(other, 1, 1)
	require.NoError(t, e)
	require.Equal(t, 6, second.LadderStep)
	require.NoError(t, a.Cfg.DeleteOverride("admin", other, "ladder"))
	second, e = a.Sched.GetCard(other, 1, 1)
	require.NoError(t, e)
	require.Equal(t, 2, second.LadderStep)
}

func TestLadderAtomicCardFailureRollsBackConfiguration(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	require.NoError(t, a.Sched.SeedCard(sid, 1, 1))
	c, e := a.Sched.GetCard(sid, 1, 1)
	require.NoError(t, e)
	c.LadderStep = 6
	require.NoError(t, store.NewSchedulerRepo(a.DB).SaveCard(c))
	rev, e := a.Cfg.Revisions(0, "ladder")
	require.NoError(t, e)
	require.NoError(t, a.DB.Exec("CREATE TRIGGER reject_ladder_update BEFORE UPDATE OF ladder_step ON memory_cards BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error)
	require.Error(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "failure"))
	require.Len(t, a.Cfg.Ladder(sid), 8)
	after, e := a.Cfg.Revisions(0, "ladder")
	require.NoError(t, e)
	require.Equal(t, rev, after)
	c, e = a.Sched.GetCard(sid, 1, 1)
	require.NoError(t, e)
	require.Equal(t, 6, c.LadderStep)
}

func TestLadderAtomicMemoryStudentIsolation(t *testing.T) {
	a, _, _, room := setupRoom(t)
	sid := room.studentID
	other := sid + 100
	for _, id := range []int64{sid, other} {
		require.NoError(t, a.Sched.SeedCard(id, 999, 999))
	}
	// Settle public cards to a nonzero step without depending on private repository access.
	for _, id := range []int64{sid, other} {
		for i := 0; i < 4; i++ {
			require.NoError(t, a.Sched.ConfigWorkView(a.Cfg).SettleLessonFinished(id, 999, map[int64]bool{999: true}, a.Clock.Now()))
		}
	}
	before, e := a.Sched.GetCard(other, 999, 999)
	require.NoError(t, e)
	require.NoError(t, a.Cfg.SetStudent("admin", sid, "ladder", []int{1, 2, 3}, "only this student"))
	after, e := a.Sched.GetCard(other, 999, 999)
	require.NoError(t, e)
	require.Equal(t, before.LadderStep, after.LadderStep)
}

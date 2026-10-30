package api

import (
	"context"
	"enwords/internal/config"
	"enwords/internal/scheduler"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLadderPairedSchedulerRejectsUncoordinatedConsumers(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	_, err := a.Sched.PlanNext(room.studentID, room.bookID)
	require.ErrorContains(t, err, "configuration_work_required")
	require.ErrorContains(t, a.Sched.SettleLesson(lid, nil, a.Clock.Now()), "configuration_work_required")
	require.ErrorContains(t, a.Sched.SettleLessonFinished(room.studentID, room.bookID, nil, a.Clock.Now()), "configuration_work_required")
	require.ErrorContains(t, a.Sched.MaintenanceCheck(room.studentID, room.bookID, 1, true), "configuration_work_required")
	require.Error(t, a.Sched.TruncateLadderSteps(3))
}

func TestLadderMemoryWorkingChangesDiscardedTogether(t *testing.T) {
	a, _, _, room := setupRoom(t)
	sid := room.studentID
	require.NoError(t, a.Sched.SeedCard(sid, 999, 999))
	for i := 0; i < 5; i++ {
		require.NoError(t, a.Sched.ConfigWorkView(a.Cfg).SettleLessonFinished(sid, 999, map[int64]bool{999: true}, a.Clock.Now()))
	}
	before, err := a.Sched.GetCard(sid, 999, 999)
	require.NoError(t, err)
	revision, err := a.Cfg.Revisions(sid, "ladder")
	require.NoError(t, err)
	err = a.Cfg.WithMemoryTransaction(func(cfg *config.Store) error {
		return a.Sched.WithMemoryTransaction(cfg, func(s *scheduler.Service) error {
			_, e := applyConfigMutationInWork(context.Background(), &configMutationWork{cfg: cfg, sched: s}, config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeStudent, StudentID: sid, Key: "ladder", Value: []int{1, 2, 3}, Reason: "rollback fixture"})
			if e != nil {
				return e
			}
			return errors.New("after private card and config changes")
		})
	})
	require.Error(t, err)
	after, err := a.Sched.GetCard(sid, 999, 999)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Len(t, a.Cfg.Ladder(sid), 8)
	actual, err := a.Cfg.Revisions(sid, "ladder")
	require.NoError(t, err)
	require.Equal(t, revision, actual)
}

package scheduler

import (
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBoostFIFOActualConsumptionReactivationAndRollback(t *testing.T) {
	s, clock, cfg := newSched(t)
	for _, wid := range []int64{1, 2} {
		require.NoError(t, s.SeedCard(7, 3, wid))
		_, err := s.BoostWordWithCapacity(7, 3, wid, 2)
		require.NoError(t, err)
	}
	before, err := s.GetCard(7, 3, 1)
	require.NoError(t, err)
	oldSequence := before.BoostSequence
	l := &Lesson{StudentID: 7, BookID: 3, Status: LessonScheduledStr, OpenAt: clock.Now(), BatchWordIDs: []int64{1}}
	require.NoError(t, s.repo.CreateLesson(l))
	require.NoError(t, s.SettleLesson(l.ID, map[int64]bool{1: true}, clock.Now()))
	consumed, err := s.GetCard(7, 3, 1)
	require.NoError(t, err)
	require.False(t, consumed.BoostFlag)
	require.Equal(t, oldSequence, consumed.BoostSequence)
	_, err = s.BoostWordWithCapacity(7, 3, 1, 2)
	require.NoError(t, err)
	active, err := s.GetCard(7, 3, 1)
	require.NoError(t, err)
	require.True(t, active.BoostFlag)
	require.Greater(t, active.BoostSequence, oldSequence)
	other, err := s.GetCard(7, 3, 2)
	require.NoError(t, err)
	beforeOther := *other
	beforeActive := *active
	require.NoError(t, s.SeedCard(7, 3, 3))
	require.Error(t, s.WithMemoryTransaction(cfg, func(tx *Service) error {
		evicted, err := tx.BoostWordWithCapacity(7, 3, 3, 2)
		require.Equal(t, []int64{2}, evicted)
		if err != nil {
			return err
		}
		return errors.New("abort after mutation")
	}))
	active, _ = s.GetCard(7, 3, 1)
	other, _ = s.GetCard(7, 3, 2)
	require.Equal(t, beforeActive, *active)
	require.Equal(t, beforeOther, *other)
	third, err := s.GetCard(7, 3, 3)
	require.NoError(t, err)
	require.False(t, third.BoostFlag)
	require.Zero(t, third.BoostSequence)
}

func TestBoostFIFORepeatedActivationAndConsumption(t *testing.T) {
	s, _, cfg := newSched(t)
	for wid := int64(1); wid <= 22; wid++ {
		require.NoError(t, s.SeedCard(7, 3, wid))
	}
	boost := func(wid int64) []int64 {
		var evicted []int64
		require.NoError(t, s.WithMemoryTransaction(cfg, func(tx *Service) error {
			var err error
			evicted, err = tx.BoostWordWithCapacity(7, 3, wid, 20)
			return err
		}))
		return evicted
	}
	for wid := int64(1); wid <= 20; wid++ {
		require.Empty(t, boost(wid))
	}
	first, err := s.GetCard(7, 3, 1)
	require.NoError(t, err)
	require.Empty(t, boost(1))
	again, err := s.GetCard(7, 3, 1)
	require.NoError(t, err)
	require.Equal(t, first.BoostSequence, again.BoostSequence)
	require.Equal(t, []int64{1}, boost(21))
	require.Equal(t, []int64{2}, boost(22))
	require.Equal(t, []int64{3}, boost(1))
	again, err = s.GetCard(7, 3, 1)
	require.NoError(t, err)
	require.Greater(t, again.BoostSequence, first.BoostSequence)
	_, err = s.BoostWordWithCapacity(99, 3, 1, 20)
	require.Error(t, err)
}

package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEffectiveViewTracksDerivedGoalAndDeletionFallback(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetStudent("admin", 7, "goalMode", "exam_sprint", "fixture"))
	v, err := s.EffectiveView(7)
	require.NoError(t, err)
	require.Equal(t, "default", v.Ladder.Source)
	require.Equal(t, "student", v.Ladder.GoalModeSource)
	require.Equal(t, SprintLadder, v.Ladder.Effective)
	require.EqualValues(t, 1, v.GoalMode.Revision.Student)
	require.NoError(t, s.SetStudent("admin", 7, "ladder", []int{1, 2, 3}, "fixture"))
	v, err = s.EffectiveView(7)
	require.NoError(t, err)
	require.Equal(t, "student", v.Ladder.Source)
	require.Empty(t, v.Ladder.DerivedFrom)
	require.Equal(t, SprintLadder, v.Ladder.Fallback)
	require.Equal(t, "student", v.Ladder.FallbackGoalModeSource)
	require.NoError(t, s.SetGlobal("admin", "ladder", []int{2, 3, 4, 5}, "fixture"))
	v, err = s.EffectiveView(7)
	require.NoError(t, err)
	require.Equal(t, "global", v.Ladder.FallbackSource)
	require.Equal(t, []int{2, 3, 4, 5}, v.Ladder.Fallback)
	require.NoError(t, s.DeleteOverride("admin", 7, "ladder"))
	v, err = s.EffectiveView(7)
	require.NoError(t, err)
	require.Equal(t, "global", v.Ladder.Source)
	require.False(t, v.Ladder.HasOverride)
	require.EqualValues(t, 2, v.Ladder.Revision.Student)
}

func TestPairedConfigurationWithoutCoordinatorRejectsSensitiveWrites(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	s.RequireAtomicMutationHandler()
	require.ErrorContains(t, s.SetGlobal("admin", "ladder", []int{1, 2, 3}, "missing assembly"), "atomic_configuration_unavailable")
	require.Error(t, s.SetStudent("admin", 7, "goalMode", "exam_sprint", "missing assembly"))
	require.Error(t, s.DeleteOverride("admin", 7, "ladder"))
	require.Equal(t, DefaultLadder, s.Ladder(7))
}

func TestEffectiveGoalInheritanceMatrix(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	check := func(sid int64, want []int, source, goalSource string) {
		t.Helper()
		v, e := s.EffectiveView(sid)
		require.NoError(t, e)
		require.Equal(t, want, v.Ladder.Effective)
		require.Equal(t, source, v.Ladder.Source)
		require.Equal(t, goalSource, v.GoalMode.Source)
	}
	check(7, DefaultLadder, "default", "default")
	require.NoError(t, s.SetGlobal("admin", "goalMode", "exam_sprint", "global goal"))
	check(7, SprintLadder, "default", "global")
	require.NoError(t, s.SetStudent("admin", 7, "goalMode", "longterm", "student goal"))
	check(7, DefaultLadder, "default", "student")
	check(8, SprintLadder, "default", "global")
	require.NoError(t, s.SetStudent("admin", 7, "ladder", []int{2, 4, 6}, "explicit student"))
	require.NoError(t, s.SetGlobal("admin", "ladder", []int{3, 5, 7}, "explicit global"))
	check(7, []int{2, 4, 6}, "student", "student")
	check(8, []int{3, 5, 7}, "global", "global")
	require.NoError(t, s.DeleteOverride("admin", 7, "goalMode"))
	check(7, []int{2, 4, 6}, "student", "global")
	require.NoError(t, s.DeleteOverride("admin", 7, "ladder"))
	check(7, []int{3, 5, 7}, "global", "global")
}

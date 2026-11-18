package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlacementMemoryTransactionKeepsDraftAndCardsTogether(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			s, _, cfg := newSched(t)
			require.NoError(t, s.SeedCards(nil, 7, 1, []int64{1, 2}))
			require.NoError(t, s.MarkPlacementContext(nil, 7, 1, []int64{1}))
			aborted := errors.New("test rollback")
			err := s.WithMemoryTransaction(cfg, func(w *Service) error {
				require.NoError(t, w.MarkPlacement(7, 1, []int64{2, 2}))
				require.Equal(t, []int64{2, 2}, w.PlacementDraft(7, 1))
				if !commit {
					return aborted
				}
				return nil
			})
			want := []int64{1}
			if commit {
				require.NoError(t, err)
				want = []int64{2, 2}
			} else {
				require.ErrorIs(t, err, aborted)
			}
			got := s.PlacementDraft(7, 1)
			require.Equal(t, want, got)
			got[0] = 999
			require.Equal(t, want, s.PlacementDraft(7, 1))
			cards, err := s.repo.ListCards(7, 1)
			require.NoError(t, err)
			for _, c := range cards {
				if c.WordID == want[0] {
					require.Equal(t, StatusMastered, c.Status)
				} else {
					require.Equal(t, StatusNew, c.Status)
				}
			}
		})
	}
}

func TestPlacementMemoryCancellationUnsupportedAndIsolation(t *testing.T) {
	repo := NewMemRepo()
	s := NewService(repo, nil, nil)
	require.NoError(t, s.SeedCards(nil, 1, 1, []int64{1, 2}))
	require.NoError(t, s.SeedCards(nil, 2, 1, []int64{1}))
	require.NoError(t, s.SeedCards(nil, 1, 2, []int64{1}))
	require.NoError(t, s.MarkPlacement(1, 1, []int64{1}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, s.MarkPlacementContext(ctx, 1, 1, []int64{2}), context.Canceled)
	require.Equal(t, []int64{1}, s.PlacementDraft(1, 1))
	for _, scope := range [][2]int64{{2, 1}, {1, 2}} {
		c, err := repo.GetCard(scope[0], scope[1], 1)
		require.NoError(t, err)
		require.Equal(t, StatusNew, c.Status)
		require.Empty(t, s.PlacementDraft(scope[0], scope[1]))
	}
	unsupported := NewService(seedNonAtomicRepo{repo}, nil, nil)
	require.Error(t, unsupported.MarkPlacement(1, 1, []int64{2}))
	require.Equal(t, []int64{1}, s.PlacementDraft(1, 1))
}

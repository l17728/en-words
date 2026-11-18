package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type seedNonAtomicRepo struct{ Repo }

func TestSeedCardsAtomicBoundaryAndIdempotency(t *testing.T) {
	repo := NewMemRepo()
	svc := NewService(repo, nil, nil)
	require.NoError(t, svc.SeedCards(nil, 1, 2, []int64{3, 3, 4}))
	got, err := repo.GetCard(1, 2, 3)
	require.NoError(t, err)
	got.Status = StatusMastered
	require.NoError(t, repo.SaveCard(got))
	require.NoError(t, svc.SeedCards(context.Background(), 1, 2, []int64{3, 4}))
	got, err = repo.GetCard(1, 2, 3)
	require.NoError(t, err)
	require.Equal(t, StatusMastered, got.Status)
	for _, bad := range []int64{0, -1} {
		require.Error(t, svc.SeedCards(context.Background(), 1, 2, []int64{5, bad}))
		_, err = repo.GetCard(1, 2, 5)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, svc.SeedCards(ctx, 1, 2, []int64{6}))
	_, err = repo.GetCard(1, 2, 6)
	require.Error(t, err)
	legacy := NewMemRepo()
	unsupported := NewService(seedNonAtomicRepo{legacy}, nil, nil)
	require.Error(t, unsupported.SeedCards(context.Background(), 1, 2, []int64{7}))
	_, err = legacy.GetCard(1, 2, 7)
	require.Error(t, err)
}

func TestAtomicMemoryCardsRejectNilWithoutPartialWrites(t *testing.T) {
	repo := NewMemRepo()
	valid := &Card{StudentID: 1, BookID: 2, WordID: 3, Status: StatusNew}
	require.Error(t, repo.CreateCardsIfAbsent(context.Background(), []*Card{valid, nil}))
	_, err := repo.GetCard(1, 2, 3)
	require.Error(t, err)
	require.NoError(t, repo.CreateCardsIfAbsent(context.Background(), []*Card{valid, {StudentID: 1, BookID: 2, WordID: 3, Status: StatusMastered}}))
	got, err := repo.GetCard(1, 2, 3)
	require.NoError(t, err)
	require.Equal(t, StatusNew, got.Status)
	valid.Status = StatusMastered
	got, err = repo.GetCard(1, 2, 3)
	require.NoError(t, err)
	require.Equal(t, StatusNew, got.Status, "caller mutation cannot alter stored card")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, repo.CreateCardsIfAbsent(ctx, []*Card{{StudentID: 2, BookID: 2, WordID: 3}}))
	_, err = repo.GetCard(2, 2, 3)
	require.Error(t, err)
}

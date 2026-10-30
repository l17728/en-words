package scheduler

import (
	"context"
	"fmt"
	"sort"
)

type ladderScopeRepo interface {
	CardStudentIDs(context.Context) ([]int64, error)
	ClampLadderSteps(context.Context, int64, int) (int64, error)
}

// Assembly-only guard. Production callers borrow a work view under their owner lock.
func (s *Service) RequireConfigurationWork() { s.pairedLive = true }
func (s *Service) ConfigWorkView(cfg LadderSource) *Service {
	view := NewService(s.repo, cfg, s.clock)
	view.workOwner = s
	view.OnPlanned = s.OnPlanned
	view.OnPlacementSaved = s.OnPlacementSaved
	return view
}
func (s *Service) CardStudentIDs(ctx context.Context) ([]int64, error) {
	r, ok := s.repo.(ladderScopeRepo)
	if !ok {
		return nil, fmt.Errorf("atomic_configuration_unavailable")
	}
	return r.CardStudentIDs(ctx)
}
func (s *Service) ClampLadderSteps(ctx context.Context, sid int64, max int) (int64, error) {
	if sid <= 0 || max < 0 {
		return 0, fmt.Errorf("invalid ladder scope")
	}
	r, ok := s.repo.(ladderScopeRepo)
	if !ok {
		return 0, fmt.Errorf("atomic_configuration_unavailable")
	}
	return r.ClampLadderSteps(ctx, sid, max)
}
func (m *MemRepo) CardStudentIDs(ctx context.Context) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := map[int64]bool{}
	for _, c := range m.cards {
		ids[c.StudentID] = true
	}
	out := make([]int64, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
func (m *MemRepo) ClampLadderSteps(ctx context.Context, sid int64, max int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, c := range m.cards {
		if c.StudentID == sid && c.LadderStep > max {
			c.LadderStep = max
			n++
		}
	}
	return n, nil
}

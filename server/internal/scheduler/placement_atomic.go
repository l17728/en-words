package scheduler

import (
	"context"
	"fmt"
)

func (s *Service) MarkPlacementContext(ctx context.Context, studentID, bookID int64, known []int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	repo, ok := s.repo.(interface {
		MarkPlacementAtomic(context.Context, int64, int64, []int64) (int, error)
	})
	if !ok {
		return fmt.Errorf("scheduler: atomic placement unavailable")
	}
	changed, err := repo.MarkPlacementAtomic(ctx, studentID, bookID, known)
	if err == nil && s.OnPlacementSaved != nil {
		s.OnPlacementSaved(ctx, studentID, bookID, len(known), changed)
	}
	return err
}

func (m *MemRepo) MarkPlacementAtomic(ctx context.Context, studentID, bookID int64, known []int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	selected := map[int64]bool{}
	for _, id := range known {
		selected[id] = true
	}
	changed := map[[3]int64]*Card{}
	for key, card := range m.cards {
		if card.StudentID != studentID || card.BookID != bookID {
			continue
		}
		status := card.Status
		if status == StatusNew && selected[card.WordID] {
			status = StatusMastered
		}
		if status == StatusMastered && !selected[card.WordID] {
			status = StatusNew
		}
		if status != card.Status {
			cp := *card
			cp.Status = status
			changed[key] = &cp
		}
	}
	draft := append([]int64(nil), known...)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for key, card := range changed {
		m.cards[key] = card
	}
	if m.drafts == nil {
		m.drafts = map[placementKey][]int64{}
	}
	m.drafts[placementKey{studentID, bookID}] = draft
	return len(changed), nil
}

func (m *MemRepo) SavePlacementDraft(studentID, bookID int64, known []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drafts == nil {
		m.drafts = map[placementKey][]int64{}
	}
	m.drafts[placementKey{studentID, bookID}] = append([]int64(nil), known...)
	return nil
}

func (m *MemRepo) LoadPlacementDraft(studentID, bookID int64) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int64(nil), m.drafts[placementKey{studentID, bookID}]...), nil
}

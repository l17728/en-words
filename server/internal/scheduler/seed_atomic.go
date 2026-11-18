package scheduler

import (
	"context"
	"fmt"
)

func (s *Service) SeedCards(ctx context.Context, studentID, bookID int64, wordIDs []int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cards := make([]*Card, 0, len(wordIDs))
	seen := map[int64]bool{}
	for _, id := range wordIDs {
		if id <= 0 {
			return fmt.Errorf("scheduler: invalid word ID")
		}
		if !seen[id] {
			seen[id] = true
			cards = append(cards, &Card{StudentID: studentID, BookID: bookID, WordID: id, Status: StatusNew})
		}
	}
	repo, ok := s.repo.(interface {
		CreateCardsIfAbsent(context.Context, []*Card) error
	})
	if !ok {
		return fmt.Errorf("scheduler: atomic card creation unsupported")
	}
	return repo.CreateCardsIfAbsent(ctx, cards)
}

func (m *MemRepo) CreateCardsIfAbsent(ctx context.Context, cards []*Card) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	prepared := make(map[[3]int64]*Card, len(cards))
	for _, c := range cards {
		if c == nil {
			return fmt.Errorf("scheduler: nil card")
		}
		k := cardKey(c.StudentID, c.BookID, c.WordID)
		if _, exists := m.cards[k]; !exists {
			if _, duplicate := prepared[k]; duplicate {
				continue
			}
			cp := *c
			prepared[k] = &cp
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for key, card := range prepared {
		m.cards[key] = card
	}
	return nil
}

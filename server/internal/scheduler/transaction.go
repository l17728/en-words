package scheduler

import "fmt"

func (s *Service) SupportsMemoryTransactions() bool { _, ok := s.repo.(*MemRepo); return ok }

func (s *Service) WithMemoryTransaction(cfg LadderSource, fn func(*Service) error) error {
	original, ok := s.repo.(*MemRepo)
	if !ok {
		return fmt.Errorf("memory transaction unavailable")
	}
	original.mu.Lock()
	defer original.mu.Unlock()
	cp := NewMemRepo()
	cp.nextLesson = original.nextLesson
	for k, ids := range original.drafts {
		cp.drafts[k] = append([]int64(nil), ids...)
	}
	for k, c := range original.cards {
		v := *c
		if c.LastReviewAt != nil {
			t := *c.LastReviewAt
			v.LastReviewAt = &t
		}
		if c.NextDueAt != nil {
			t := *c.NextDueAt
			v.NextDueAt = &t
		}
		cp.cards[k] = &v
	}
	for _, l := range original.lessons {
		v := *l
		v.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
		if l.FinishedAt != nil {
			t := *l.FinishedAt
			v.FinishedAt = &t
		}
		cp.lessons = append(cp.lessons, &v)
	}
	working := NewService(cp, cfg, s.clock)
	if err := fn(working); err != nil {
		return err
	}
	original.cards, original.lessons, original.nextLesson = cp.cards, cp.lessons, cp.nextLesson
	original.drafts = cp.drafts
	return nil
}

package memory

import "fmt"

func (s *Service) SupportsMemoryTransactions() bool { _, ok := s.repo.(*MemRepo); return ok }

func (s *Service) WithMemoryTransaction(fn func(*Service) error) error {
	original, ok := s.repo.(*MemRepo)
	if !ok {
		return fmt.Errorf("memory transaction unavailable")
	}
	original.mu.Lock()
	defer original.mu.Unlock()
	cp := NewMemRepo()
	cp.next = original.next
	for k, m := range original.ms {
		v := *m
		cp.ms[k] = &v
	}
	working := NewService(cp)
	working.now = s.now
	if err := fn(working); err != nil {
		return err
	}
	original.ms, original.next = cp.ms, cp.next
	return nil
}

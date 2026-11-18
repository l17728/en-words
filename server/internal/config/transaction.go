package config

import "fmt"

type Revision struct {
	Global  int64 `json:"global"`
	Student int64 `json:"student"`
}
type revisionSource interface {
	Revisions(int64, string) (Revision, error)
}

func (s *Store) SupportsMemoryTransactions() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.persist == nil
}

func (s *Store) bumpRevision(sid int64, key string) {
	if s.revisions == nil {
		s.revisions = map[int64]map[string]int64{}
	}
	if s.revisions[sid] == nil {
		s.revisions[sid] = map[string]int64{}
	}
	s.revisions[sid][key]++
}
func (s *Store) Revisions(sid int64, key string) (Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.persist != nil {
		if p, ok := s.persist.(revisionSource); ok {
			return p.Revisions(sid, key)
		}
		return Revision{}, fmt.Errorf("configuration revisions unavailable")
	}
	return Revision{Global: s.revisions[0][key], Student: s.revisions[sid][key]}, nil
}
func cloneValue(v any) any {
	if xs, ok := v.([]int); ok {
		return append([]int(nil), xs...)
	}
	return v
}

// The original lock stays held throughout callback and publication. The callback
// must use only the provided Store, never the original or its external hooks.
func (s *Store) WithMemoryTransaction(fn func(*Store) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persist != nil {
		return fmt.Errorf("memory transaction unavailable")
	}
	n, _ := NewStore(nil, nil)
	for k, v := range s.globals {
		n.globals[k] = cloneValue(v)
	}
	for sid, vals := range s.overrides {
		n.overrides[sid] = map[string]any{}
		for k, v := range vals {
			n.overrides[sid][k] = cloneValue(v)
		}
	}
	for sid, vals := range s.revisions {
		n.revisions[sid] = map[string]int64{}
		for k, v := range vals {
			n.revisions[sid][k] = v
		}
	}
	if err := fn(n); err != nil {
		return err
	}
	s.globals, s.overrides, s.revisions = n.globals, n.overrides, n.revisions
	return nil
}

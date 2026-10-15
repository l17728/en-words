package wordbook

import (
	"context"
	"fmt"
)

// PrepareImport preserves input order; only invalid input and duplicate keys skip.
func PrepareImport(ins []EntryInput, existing map[string]bool) ([]*Entry, *ImportResult) {
	result := &ImportResult{Skipped: []string{}}
	entries := make([]*Entry, 0, len(ins))
	for _, in := range ins {
		entry, err := validateEntry(in)
		if err != nil {
			result.Skipped = append(result.Skipped, in.Word)
			continue
		}
		if existing[entry.NormKey] {
			result.Skipped = append(result.Skipped, in.Word)
			continue
		}
		existing[entry.NormKey] = true
		entries = append(entries, entry)
	}
	result.Inserted = len(entries)
	return entries, result
}

func (s *Service) ImportBatchContext(ctx context.Context, bookID int64, ins []EntryInput) (*ImportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(interface {
		ImportEntriesAtomic(context.Context, int64, []EntryInput) (*ImportResult, error)
	})
	if !ok {
		return nil, fmt.Errorf("wordbook: atomic import unsupported")
	}
	return repo.ImportEntriesAtomic(ctx, bookID, ins)
}

func (m *MemRepo) ImportEntriesAtomic(ctx context.Context, bookID int64, ins []EntryInput) (*ImportResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	book, ok := m.books[bookID]
	if !ok {
		return nil, ErrBookNotFound
	}
	keys := map[string]bool{}
	var count int64
	for _, e := range m.entries {
		if e.BookID == bookID {
			keys[e.NormKey] = true
			count++
		}
	}
	entries, result := PrepareImport(ins, keys)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, e := range entries {
		m.nextE++
		e.ID = m.nextE
		e.BookID = bookID
		m.entries[e.ID] = cloneEntry(e)
	}
	book.WordCount = count + int64(len(entries))
	return result, nil
}

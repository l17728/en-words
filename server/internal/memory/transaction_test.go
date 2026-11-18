package memory

import (
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMemoryTransactionAppendRollback(t *testing.T) {
	s := NewService(NewMemRepo())
	var first int64
	require.Error(t, s.WithMemoryTransaction(func(w *Service) error {
		m, err := w.Append(OwnerSystem, ScopeStudentProfile, 1, 0, "promise", "fixture", 4, 0)
		require.NoError(t, err)
		first = m.ID
		return errors.New("rollback")
	}))
	_, err := s.repo.Get(first)
	require.Error(t, err)
	require.NoError(t, s.WithMemoryTransaction(func(w *Service) error {
		_, err := w.Append(OwnerSystem, ScopeStudentProfile, 1, 0, "promise", "fixture", 4, 0)
		return err
	}))
	_, err = s.repo.Get(first)
	require.NoError(t, err)
}

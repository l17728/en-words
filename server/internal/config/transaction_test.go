package config

import (
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMemoryTransactionRollbackAndRevisionABA(t *testing.T) {
	s, _ := NewStore(nil, nil)
	require.NoError(t, s.SetStudent("admin", 1, "wordsPerLesson", 70, "test"))
	before, err := s.Revisions(1, "wordsPerLesson")
	require.NoError(t, err)
	require.Error(t, s.WithMemoryTransaction(func(w *Store) error {
		require.NoError(t, w.SetStudent("agent", 1, "wordsPerLesson", 60, "test"))
		return errors.New("rollback")
	}))
	require.Equal(t, 70, s.Int(1, "wordsPerLesson"))
	r, _ := s.Revisions(1, "wordsPerLesson")
	require.Equal(t, before, r)
	require.NoError(t, s.WithMemoryTransaction(func(w *Store) error { return w.SetStudent("agent", 1, "wordsPerLesson", 60, "test") }))
	require.Equal(t, 60, s.Int(1, "wordsPerLesson"))
	require.NoError(t, s.SetStudent("admin", 1, "wordsPerLesson", 70, "test"))
	r, _ = s.Revisions(1, "wordsPerLesson")
	require.Equal(t, before.Student+2, r.Student)
	require.NoError(t, s.DeleteOverride("admin", 1, "wordsPerLesson"))
	r, _ = s.Revisions(1, "wordsPerLesson")
	require.Equal(t, before.Student+3, r.Student)
}

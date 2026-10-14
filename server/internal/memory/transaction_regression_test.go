package memory

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryTransactionExistingRecordsAreIsolatedUntilCommit(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			repo := NewMemRepo()
			service := NewService(repo)
			original := &Memory{StudentID: 7, Kind: "preference", Content: "original", Importance: 3}
			require.NoError(t, repo.Append(original))
			originalNext := repo.next
			abort := errors.New("fixture rollback")
			err := service.WithMemoryTransaction(func(working *Service) error {
				copyRepo := working.repo.(*MemRepo)
				require.NotSame(t, repo.ms[original.ID], copyRepo.ms[original.ID])
				copyRepo.ms[original.ID].Content = "changed"
				require.NoError(t, copyRepo.Append(&Memory{StudentID: 7, Content: "new"}))
				require.Equal(t, "original", repo.ms[original.ID].Content)
				require.Len(t, repo.ms, 1)
				require.Equal(t, originalNext, repo.next)
				if !commit {
					return abort
				}
				return nil
			})
			if commit {
				require.NoError(t, err)
				require.Equal(t, "changed", repo.ms[original.ID].Content)
				require.Len(t, repo.ms, 2)
				require.Equal(t, originalNext+1, repo.next)
			} else {
				require.ErrorIs(t, err, abort)
				require.Equal(t, "original", repo.ms[original.ID].Content)
				require.Len(t, repo.ms, 1)
				require.Equal(t, originalNext, repo.next)
			}
		})
	}
}

package config

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func validMutation() Mutation {
	return Mutation{Operation: "set", Actor: "admin", Scope: ScopeGlobal, Key: "ladder", Value: []int{1, 2, 3}, Reason: "test"}
}

func TestBindAtomicMutationHandlerAndApplyContext(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	require.Error(t, s.BindAtomicMutationHandler(nil))

	calls := 0
	want := MutationResult{Committed: true, ChangedCards: 2}
	require.NoError(t, s.BindAtomicMutationHandler(func(ctx context.Context, got Mutation) (MutationResult, error) {
		calls++
		require.NoError(t, ctx.Err())
		require.Equal(t, validMutation(), got)
		return want, nil
	}))
	require.Error(t, s.BindAtomicMutationHandler(func(context.Context, Mutation) (MutationResult, error) { return MutationResult{}, nil }))

	got, err := s.ApplyContext(context.Background(), validMutation())
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, 1, calls)
	// Paired sensitive writes must use the coordinator; the callback owns mutation.
	require.Equal(t, DefaultLadder, s.Ladder(0))
}

func TestApplyContextCancellationAndPairedRejection(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	calls := 0
	require.NoError(t, s.BindAtomicMutationHandler(func(context.Context, Mutation) (MutationResult, error) {
		calls++
		return MutationResult{}, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.ApplyContext(ctx, validMutation())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, MutationResult{}, result)
	require.Zero(t, calls)

	paired, err := NewStore(nil, nil)
	require.NoError(t, err)
	paired.RequireAtomicMutationHandler()
	_, err = paired.ApplyContext(context.Background(), validMutation())
	require.EqualError(t, err, "atomic_configuration_unavailable")
	// Non-sensitive settings remain on the legacy path when only sensitive config
	// has been paired with an atomic coordinator.
	result, err = paired.ApplyContext(context.Background(), Mutation{Operation: "set", Actor: "admin", Scope: ScopeGlobal, Key: "wordsPerLesson", Value: 70, Reason: "test"})
	require.NoError(t, err)
	require.False(t, result.Noop)
	require.Equal(t, 70, paired.Int(0, "wordsPerLesson"))
}

func TestApplyContextLegacyMutationAndDeleteNoop(t *testing.T) {
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	result, err := s.ApplyContext(context.Background(), Mutation{Operation: "delete_override", Actor: "admin", Scope: ScopeStudent, StudentID: 9, Key: "goalMode"})
	require.NoError(t, err)
	require.True(t, result.Noop)
	require.Equal(t, "longterm", s.String(9, "goalMode"))

	result, err = s.ApplyContext(context.Background(), Mutation{Operation: "set", Actor: "admin", Scope: ScopeStudent, StudentID: 9, Key: "goalMode", Value: "exam_sprint", Reason: "lesson plan"})
	require.NoError(t, err)
	require.False(t, result.Noop)
	require.Equal(t, "exam_sprint", s.String(9, "goalMode"))
	require.True(t, s.HasOverride(9, "goalMode"))
	require.False(t, s.HasOverride(10, "goalMode"))

	result, err = s.ApplyContext(context.Background(), Mutation{Operation: "delete_override", Actor: "admin", Scope: ScopeStudent, StudentID: 9, Key: "goalMode"})
	require.NoError(t, err)
	require.False(t, result.Noop)
	require.False(t, s.HasOverride(9, "goalMode"))
	require.Equal(t, "longterm", s.String(9, "goalMode"))
}

func TestValidateMutationRejectsInvalidBoundaries(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Mutation)
	}{
		{"unknown scope", func(m *Mutation) { m.Scope = "tenant" }},
		{"global student id", func(m *Mutation) { m.StudentID = 4 }},
		{"student missing id", func(m *Mutation) { m.Scope = ScopeStudent }},
		{"negative student id", func(m *Mutation) { m.Scope, m.StudentID = ScopeStudent, -1 }},
		{"unknown operation", func(m *Mutation) { m.Operation = "merge" }},
		{"empty reason", func(m *Mutation) { m.Reason = " \t\n" }},
		{"unknown key", func(m *Mutation) { m.Key = "notAConfigKey" }},
		{"invalid value", func(m *Mutation) { m.Value = "not an integer array" }},
		{"delete global override", func(m *Mutation) { m.Operation = "delete_override" }},
		{"delete unknown key", func(m *Mutation) {
			m.Operation, m.Scope, m.StudentID, m.Key = "delete_override", ScopeStudent, 7, "missing"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validMutation()
			tt.edit(&m)
			require.Error(t, ValidateMutation(m))
		})
	}
	require.NoError(t, ValidateMutation(Mutation{Operation: "delete_override", Scope: ScopeStudent, StudentID: 7, Key: "ladder"}))
	require.NoError(t, ValidateMutation(validMutation()))
}

type snapshotPersister struct {
	memPersister
	globals         map[string]json.RawMessage
	overrides       map[int64]map[string]json.RawMessage
	err             error
	snapshotCalls   int
	legacyLoadCalls int
}

func (p *snapshotPersister) LoadSnapshot() (map[string]json.RawMessage, map[int64]map[string]json.RawMessage, error) {
	p.snapshotCalls++
	return p.globals, p.overrides, p.err
}
func (p *snapshotPersister) LoadGlobals() (map[string]json.RawMessage, error) {
	p.legacyLoadCalls++
	return p.memPersister.LoadGlobals()
}
func (p *snapshotPersister) LoadOverrides() (map[int64]map[string]json.RawMessage, error) {
	p.legacyLoadCalls++
	return p.memPersister.LoadOverrides()
}

func TestNewStoreUsesAtomicLoadSnapshot(t *testing.T) {
	global, _ := json.Marshal(72)
	studentGoal, _ := json.Marshal("exam_sprint")
	p := &snapshotPersister{
		memPersister: *newMemPersister(),
		globals:      map[string]json.RawMessage{"wordsPerLesson": global},
		overrides:    map[int64]map[string]json.RawMessage{12: {"goalMode": studentGoal}},
	}
	s, err := NewStore(p, nil)
	require.NoError(t, err)
	require.Equal(t, 1, p.snapshotCalls)
	require.Zero(t, p.legacyLoadCalls)
	require.Equal(t, 72, s.Int(0, "wordsPerLesson"))
	require.Equal(t, "exam_sprint", s.String(12, "goalMode"))

	p.err = errors.New("snapshot unavailable")
	s, err = NewStore(p, nil)
	require.Nil(t, s)
	require.ErrorContains(t, err, "snapshot unavailable")
	require.Equal(t, 2, p.snapshotCalls)
	require.Zero(t, p.legacyLoadCalls)
}

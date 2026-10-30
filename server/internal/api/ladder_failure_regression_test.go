package api

import (
	"context"
	"encoding/json"
	"enwords/internal/config"
	"enwords/internal/store"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

type ladderReloadFault struct {
	config.Persister
	armed bool
}

func (p *ladderReloadFault) LoadGlobals() (map[string]json.RawMessage, error) {
	if p.armed {
		return nil, errors.New("fixture reload")
	}
	return p.Persister.LoadGlobals()
}

func TestLadderCommittedRefreshFailureUsesFreshRead(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	p := &ladderReloadFault{Persister: store.NewConfigPersister(a.DB)}
	cfg, err := config.NewStore(p, nil)
	require.NoError(t, err)
	a.Cfg = cfg
	p.armed = true
	result, err := a.ChangeConfig(context.Background(), config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{1, 2, 3}, Reason: "fixture"})
	require.NoError(t, err)
	require.True(t, result.Committed)
	require.True(t, result.RefreshPending)
	require.Len(t, a.Cfg.Ladder(sid), 8)
	snapshot, err := a.FreshConfigSnapshot(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, snapshot.Ladder)
	view, err := a.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, view.Ladder.Effective)
	require.EqualValues(t, 1, view.Ladder.Revision.Global)
}

func TestLadderScopeWithoutCardsFailsOnlyWhenEffectiveValueChanges(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	require.NoError(t, a.DB.Exec("CREATE TABLE classroom_scope_reservations(student_id INTEGER, book_id INTEGER, mode TEXT, schema_version INTEGER, reserved_at INTEGER)").Error)
	require.NoError(t, a.DB.Exec("INSERT INTO classroom_scope_reservations VALUES(?, 1, 'pilot', 2, 0)", sid).Error)
	require.Error(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "must refuse protected"))
	require.Len(t, a.Cfg.Ladder(sid), 8)
	// An explicit identical override is not an effective ladder change. Its normal
	// revision semantics remain, and unrelated students can be changed safely.
	require.NoError(t, a.Cfg.SetStudent("admin", sid, "ladder", config.DefaultLadder, "same effective value"))
	require.NoError(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "protected student inherits nothing"))
	require.Equal(t, config.DefaultLadder, a.Cfg.Ladder(sid))
	require.Error(t, a.Cfg.DeleteOverride("admin", sid, "ladder"))
}

func TestLadderDeferredCommitFailureLeavesOriginalAndNewConnectionsClean(t *testing.T) {
	a, b, sid := feedbackTwoApps(t)
	for _, sql := range []string{"CREATE TABLE ladder_parent(id INTEGER PRIMARY KEY)", "CREATE TABLE ladder_child(id INTEGER REFERENCES ladder_parent(id) DEFERRABLE INITIALLY DEFERRED)", "CREATE TRIGGER ladder_commit_fault AFTER INSERT ON config_globals WHEN NEW.key='ladder' BEGIN INSERT INTO ladder_child VALUES(999); END"} {
		require.NoError(t, a.DB.Exec(sql).Error)
	}
	require.Error(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "deferred failure"))
	for _, app := range []*App{a, b} {
		v, err := app.EffectiveConfiguration(context.Background(), sid)
		require.NoError(t, err)
		require.Equal(t, config.DefaultLadder, v.Ladder.Effective)
		require.Zero(t, v.Ladder.Revision.Global)
	}
	require.NoError(t, a.DB.Exec("DROP TRIGGER ladder_commit_fault").Error)
	require.NoError(t, a.Cfg.SetGlobal("admin", "ladder", []int{1, 2, 3}, "retry after rollback"))
	v, err := b.EffectiveConfiguration(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, v.Ladder.Effective)
}

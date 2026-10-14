package api

import (
	"enwords/internal/store"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"sync"
	"testing"
)

func TestIdentityTwoAppsFirstStartupAndRestartKeepAgentIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.db")
	first, err := store.Open(path)
	require.NoError(t, err)
	second, err := store.Open(path)
	require.NoError(t, err)
	for _, db := range []*gorm.DB{first, second} {
		sql, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sql.Close() })
	}
	var wg sync.WaitGroup
	wg.Add(2)
	apps := make([]*App, 2)
	for i, db := range []*gorm.DB{first, second} {
		go func(i int, db *gorm.DB) { defer wg.Done(); apps[i] = NewApp(nil, db) }(i, db)
	}
	wg.Wait()
	restarted := NewApp(nil, first)
	for _, name := range []string{"teacher-agent", "system-agent"} {
		original, err := apps[0].Auth.GetUser(name)
		require.NoError(t, err)
		for _, a := range []*App{apps[1], restarted} {
			got, err := a.Auth.GetUser(name)
			require.NoError(t, err)
			require.Equal(t, original.ID, got.ID)
			require.Equal(t, original.Role, got.Role)
		}
		require.EqualValues(t, 1, submissionCount(t, apps[0], "users", "username=?", name))
	}
}

func TestIdentityDatabaseReadFailureNeverUsesCachedAuthorization(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	token, _ := loginGet(t, a, "feedback-student", "fixturepw")
	require.NoError(t, a.DB.Callback().Query().Before("gorm:query").Register("fixture_identity_unavailable", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("private identity read error"))
		}
	}))
	got := req2(t, a.Router(), "POST", "/api/v1/feedbacks", token, map[string]any{"submission_id": "identity-failure", "text": "每次词太多了"})
	require.Equal(t, 401, got.Code)
	require.Zero(t, submissionCount(t, a, "feedback_tasks", "student_id=?", sid))
	require.PanicsWithValue(t, "load or initialize persisted identities failed", func() { NewApp(a.Clock, a.DB) })
}

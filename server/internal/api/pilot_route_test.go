package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enwords/internal/auth"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAdminCanExplicitlyEnablePilotPersistence(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pilot-api.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	app := NewApp(&fakeClock{}, db)
	_, err = app.Auth.CreateUser("admin", "adminpw", "管理员", auth.RoleAdmin)
	require.NoError(t, err)
	app.persistUser("admin")
	token, _ := loginGet(t, app, "admin", "adminpw")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pilot/enable", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)
	require.NotNil(t, app.Pilot)

	// Repeating the explicit operation is idempotent after the schema audit.
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	require.Equal(t, 0, doJSON(t, w).Code)
}

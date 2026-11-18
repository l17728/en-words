package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	student, err := app.Auth.CreateUser("student-7", "studentpw", "Student", auth.RoleStudent)
	require.NoError(t, err)
	app.persistUser("student-7")
	require.NoError(t, db.Create(&store.WordBook{ID: 9, Name: "pilot-book", Status: "published"}).Error)
	require.NoError(t, db.Create(&store.WordEntry{ID: 91, BookID: 9, Word: "cat", NormKey: "cat", Meanings: `["cat"]`}).Error)
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

	body, err := json.Marshal(map[string]any{"task_id": "admin-pilot-task", "student_id": student.ID, "book_id": 9, "open_at": time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339), "word_ids": []int64{91}})
	require.NoError(t, err)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pilot/lessons", strings.NewReader(string(body)))
	create.Header.Set("Authorization", "Bearer "+token)
	create.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, create)
	created := doJSON(t, w)
	require.Equal(t, 0, created.Code, created.Msg)
	studentToken, _ := loginGet(t, app, "student-7", "studentpw")
	enter := httptest.NewRequest(http.MethodGet, "/api/v1/student/pilot/lessons/1/enter?task_id=student-room-task", nil)
	enter.Header.Set("Authorization", "Bearer "+studentToken)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, enter)
	entered := doJSON(t, w)
	require.Equal(t, 0, entered.Code, entered.Msg)
	commandBody := `{"task_id":"student-room-task","command_id":"cmd-http-1","kind":"lesson.start","payload":"{\"start\":true}"}`
	command := httptest.NewRequest(http.MethodPost, "/api/v1/student/pilot/lessons/1/commands", strings.NewReader(commandBody))
	command.Header.Set("Authorization", "Bearer "+studentToken)
	command.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, command)
	commandResp := doJSON(t, w)
	require.Equal(t, 0, commandResp.Code, commandResp.Msg)

}

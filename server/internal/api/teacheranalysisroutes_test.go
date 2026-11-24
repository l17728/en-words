package api

import (
	"net/http"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func TestTeacherAnalysisAdminAndStudentRoutes(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/teacher-routes.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	app, _ := newTestApp(t)
	app.DB = db
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "admin"))
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	_, err = app.Auth.CreateUser("analysis-student", "pw", "学生", auth.RoleStudent)
	require.NoError(t, err)
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "analysis-student"))
	studentToken, studentID := loginGet(t, app, "analysis-student", "pw")

	resp := req2(t, app.Router(), http.MethodGet, "/api/v1/admin/teacher-analysis", adminToken, nil)
	require.Equal(t, 0, resp.Code, resp.Msg)
	require.Empty(t, resp.Data)
	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/student/lessons/31/teacher-analysis", studentToken, nil)
	require.Equal(t, 0, resp.Code)
	require.Empty(t, resp.Data)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/teacher-analysis/process-next", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/admin/wordbooks/1/words/search", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	require.Empty(t, resp.Data)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/speech/refresh", adminToken, map[string]string{"text": "cat", "lang": "en"})
	require.Equal(t, http.StatusServiceUnavailable, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/speech/refresh", adminToken, map[string]string{"text": " ", "lang": "en"})
	require.Equal(t, errBadRequest, resp.Code)

	bookID := seedBook(t, app, adminToken, "analysis-variants", "g7", []string{"sth.", "cat"})
	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/admin/wordbooks/"+itoa64(bookID)+"/words/search?q=sth", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	searchResults, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, searchResults, 1)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/wordbooks/"+itoa64(bookID)+"/pronunciation-scan", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/wordbooks/"+itoa64(bookID)+"/pronunciation-scan", studentToken, nil)
	require.Equal(t, errForbidden, resp.Code)

	now := time.Now().UTC()
	analysis := store.TeacherFailureAnalysis{LessonID: 31, EntryID: 41, StudentID: studentID, FailureType: "repeat_mismatch", TargetText: "ASAP", Transcript: "as soon as possible", Diagnosis: "pending", Suggestion: "pending", AnalysisStatus: "pending", ImplementationStatus: "pending", AnalysisKey: "route-analysis", CreatedAt: now}
	require.NoError(t, db.Create(&analysis).Error)
	job := store.TeacherAnalysisJob{ID: "route-job", AnalysisID: analysis.ID, AnalysisKey: analysis.AnalysisKey, Status: "queued", NextRunAt: now.Add(-time.Second), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&job).Error)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/teacher-analysis/process-next", adminToken, nil)
	require.Equal(t, 0, resp.Code)

	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/admin/teacher-analysis", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	rows, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/student/lessons/31/teacher-analysis", studentToken, nil)
	require.Equal(t, 0, resp.Code)
	rows, ok = resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)

	path := "/api/v1/admin/teacher-analysis/" + itoa64(analysis.ID) + "/evaluate"
	resp = req2(t, app.Router(), http.MethodPost, path, adminToken, map[string]string{"action": "edit_and_apply"})
	require.Equal(t, errBadRequest, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, path, adminToken, map[string]string{"action": "run_anything"})
	require.Equal(t, errBadRequest, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, path, adminToken, map[string]string{"action": "accept_suggestion"})
	require.Equal(t, 0, resp.Code)
	var applied store.TeacherFailureAnalysis
	require.NoError(t, db.First(&applied, analysis.ID).Error)
	require.Equal(t, "approved", applied.ImplementationStatus)
	var variants []store.WordPronunciationVariant
	require.NoError(t, db.Where("entry_id = ?", analysis.EntryID).Find(&variants).Error)
	require.Len(t, variants, 1)
	require.Equal(t, "as soon as possible", variants[0].Variant)
	var receipts []store.TeacherAuthorizationReceipt
	require.NoError(t, db.Where("analysis_id = ?", analysis.ID).Find(&receipts).Error)
	require.Len(t, receipts, 1)
	require.NotNil(t, receipts[0].ConsumedAt)

	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/teacher-analysis/999999/evaluate", adminToken, map[string]string{"action": "accept_suggestion"})
	require.Equal(t, errNotFound, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/teacher-analysis/"+itoa64(analysis.ID)+"/evaluate", studentToken, map[string]string{"action": "accept_suggestion"})
	require.Equal(t, errForbidden, resp.Code)

	withoutDB, _ := newTestApp(t)
	noDBAdmin, _ := loginGet(t, withoutDB, "admin", "adminpw")
	resp = req2(t, withoutDB.Router(), http.MethodGet, "/api/v1/admin/teacher-analysis", noDBAdmin, nil)
	require.Equal(t, 0, resp.Code)
	require.Empty(t, resp.Data)
	resp = req2(t, withoutDB.Router(), http.MethodPost, "/api/v1/admin/teacher-analysis/process-next", noDBAdmin, nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.Code)
}

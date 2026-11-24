package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSpeechMaterialRoutesScanEnsureProgressCancelAndOwnership(t *testing.T) {
	t.Setenv("ENWORDS_TTS_CACHE_DIR", t.TempDir())
	app, _, lessonID, _ := setupRoom(t)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-routes.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	app.DB = db
	require.NoError(t, db.AutoMigrate(&store.User{}, &store.SpeechAsset{}, &store.SpeechMaterialJob{}))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "admin"))
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "sfin"))
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.StopSpeechMaterialJobs(ctx)
		_ = pool.Close()
	})
	studentToken, _ := loginGet(t, app, "sfin", "pw")
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	path := "/api/v1/classroom/lessons/" + itoa64(lessonID) + "/speech-materials/"

	scan := req2(t, app.Router(), http.MethodPost, path+"scan", studentToken, nil)
	require.Equal(t, 0, scan.Code, scan.Msg)
	scanData := scan.Data.(map[string]any)
	require.Greater(t, scanData["total"].(float64), float64(0))
	require.Equal(t, false, scanData["can_start"])

	_, err = app.Auth.CreateUser("speech-other", "pw", "other", auth.RoleStudent)
	require.NoError(t, err)
	require.NoError(t, store.NewAuthStore(db).SaveUser(app.Auth, "speech-other"))
	otherToken, _ := loginGet(t, app, "speech-other", "pw")
	denied := req2(t, app.Router(), http.MethodPost, path+"scan", otherToken, nil)
	require.Equal(t, errForbidden, denied.Code)
	adminDenied := req2(t, app.Router(), http.MethodPost, path+"scan", adminToken, nil)
	require.NotEqual(t, 0, adminDenied.Code)
	missing := req2(t, app.Router(), http.MethodPost, "/api/v1/classroom/lessons/999999/speech-materials/scan", studentToken, nil)
	require.Equal(t, errForbidden, missing.Code)

	ensure := req2(t, app.Router(), http.MethodPost, path+"ensure", studentToken, nil)
	require.Equal(t, 0, ensure.Code, ensure.Msg)
	deadline := time.Now().Add(3 * time.Second)
	var progress Resp
	for time.Now().Before(deadline) {
		progress = req2(t, app.Router(), http.MethodGet, path+"progress", studentToken, nil)
		data := progress.Data.(map[string]any)
		if data["pending"].(float64) == data["total"].(float64) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, 0, progress.Code, progress.Msg)
	progressData := progress.Data.(map[string]any)
	require.Equal(t, float64(0), progressData["failed"])
	require.Equal(t, scanData["total"], progressData["pending"])
	require.Equal(t, false, progressData["can_start"])

	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path+"progress", nil)
	request.Header.Set("Authorization", "Bearer "+studentToken)
	app.Router().ServeHTTP(first, request)
	require.Equal(t, http.StatusOK, first.Code)
	require.NotEmpty(t, first.Header().Get("ETag"))
	conditional := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, path+"progress", nil)
	request.Header.Set("Authorization", "Bearer "+studentToken)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	app.Router().ServeHTTP(conditional, request)
	require.Equal(t, http.StatusNotModified, conditional.Code)

	canceled := req2(t, app.Router(), http.MethodPost, path+"cancel", studentToken, nil)
	require.Equal(t, 0, canceled.Code, canceled.Msg)
	require.Equal(t, true, canceled.Data.(map[string]any)["canceled"])
}

func TestSpeechMaterialsReadyRequiresEveryCurrentProfileWAV(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ENWORDS_TTS_CACHE_DIR", root)
	app, _, lessonID, _ := setupRoom(t)
	ready, revision, err := app.speechMaterialsReady(lessonID)
	require.NoError(t, err)
	require.False(t, ready, "in-memory apps have no persistent speech cache")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "speech-ready.db")), &gorm.Config{})
	require.NoError(t, err)
	app.DB = db
	ready, _, err = app.speechMaterialsReady(lessonID)
	require.NoError(t, err)
	require.False(t, ready, "unmigrated asset tables cannot authorize lesson entry")
	require.NoError(t, db.AutoMigrate(&store.SpeechAsset{}, &store.SpeechMaterialJob{}))
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	items, revision, err := app.speechMaterialsForLesson(lessonID)
	require.NoError(t, err)
	require.NotEmpty(t, revision)
	seeded := make(map[string]bool, len(items))
	for _, item := range items {
		if seeded[item.Key] {
			continue
		}
		seeded[item.Key] = true
		path := speechCacheAssetPath(item.Key)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		raw := minimalTestWAV()
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		require.NoError(t, db.Create(&store.SpeechAsset{CacheKey: item.Key, Language: item.Language, SpeechText: item.Text, FilePath: path, SHA256: audioSHA(raw), SizeBytes: int64(len(raw)), Status: "ready"}).Error)
	}
	ready, gotRevision, err := app.speechMaterialsReady(lessonID)
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, revision, gotRevision)
	if len(items) > 0 {
		require.NoError(t, os.Remove(speechCacheAssetPath(items[0].Key)))
		ready, _, err = app.speechMaterialsReady(lessonID)
		require.NoError(t, err)
		require.False(t, ready, "missing WAV must keep classroom entry gated")
	}
}

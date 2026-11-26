package api

import (
	"net/http"
	"testing"
	"time"

	"enwords/internal/localmodel"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEnsureDefaultModelProfileRepairsWithoutOverridingActiveChoice(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/profiles.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	app := NewApp(&fakeClock{t: time.Now()}, db)

	defaultRow := store.ModelProfile{Name: "default", Active: false}
	fastRow := store.ModelProfile{Name: "fast", TextService: "text-fast", ASRService: "asr", TTSZhService: "tts-zh", TTSEnService: "tts-en", Active: true}
	require.NoError(t, db.Create(&defaultRow).Error)
	require.NoError(t, db.Create(&fastRow).Error)

	selected, err := app.ensureDefaultModelProfile()
	require.NoError(t, err)
	require.Equal(t, "fast", selected.Name)

	var repaired store.ModelProfile
	require.NoError(t, db.First(&repaired, defaultRow.ID).Error)
	require.Equal(t, "vl", repaired.TextService)
	require.Equal(t, "asr", repaired.ASRService)
	require.False(t, repaired.Active, "repair must not steal active selection")
	var activeCount int64
	require.NoError(t, db.Model(&store.ModelProfile{}).Where("active = ?", true).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount)
}

func TestModelProfileRoutesCoverFallbackValidationAndUnavailableCases(t *testing.T) {
	app, _ := newTestApp(t)
	adminToken, _ := loginGet(t, app, "admin", "adminpw")
	resp := req2(t, app.Router(), http.MethodGet, "/api/v1/admin/model-services", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	require.Empty(t, resp.Data)
	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/admin/model-profiles", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	profiles, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, profiles, 1)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/model-profiles", adminToken, map[string]string{"name": " "})
	require.Equal(t, errBadRequest, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/model-profiles", adminToken, map[string]string{"name": "memory-only"})
	require.Equal(t, 0, resp.Code)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/model-profiles/1/apply", adminToken, nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.Code)

	app.Local = &localmodel.Client{Services: map[string]localmodel.Service{
		"text": {ID: "text", Model: "text-1", Capabilities: []string{"text"}},
	}}
	resp = req2(t, app.Router(), http.MethodGet, "/api/v1/admin/model-services", adminToken, nil)
	require.Equal(t, 0, resp.Code)
	services, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, services, 1)
	resp = req2(t, app.Router(), http.MethodPost, "/api/v1/admin/model-profiles", adminToken, map[string]string{
		"name": "bad-service", "text_service": "missing", "asr_service": "missing", "tts_zh_service": "missing", "tts_en_service": "missing",
	})
	require.Equal(t, errBadRequest, resp.Code)
}

func TestEnsureDefaultModelProfileActivatesFallbackWhenNoProfileIsActive(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/profiles-no-active.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	app := NewApp(&fakeClock{t: time.Now()}, db)
	row := store.ModelProfile{Name: "default", TextService: "vl", ASRService: "asr", TTSZhService: "tts", TTSEnService: "tts-en", Active: false}
	require.NoError(t, db.Create(&row).Error)

	selected, err := app.ensureDefaultModelProfile()
	require.NoError(t, err)
	require.Equal(t, "default", selected.Name)
	require.True(t, selected.Active)
	var activeCount int64
	require.NoError(t, db.Model(&store.ModelProfile{}).Where("active = ?", true).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount)
}

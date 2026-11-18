package api

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"enwords/internal/auth"
	"enwords/internal/localmodel"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
)

func modelProfileWAVFixture() []byte {
	wav := make([]byte, 52)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], 44)
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 8)
	copy(wav[44:], []byte{0, 0, 100, 0, 0, 0, 156, 255})
	return wav
}

func TestModelProfilesAndSpeechRefreshPersistAndApply(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tts/speech" || r.URL.Path == "/api/tts-en/speech" {
			_, _ = w.Write(modelProfileWAVFixture())
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer provider.Close()

	db, err := store.Open(t.TempDir() + "/model-profile.db")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	app := NewApp(&fakeClock{}, db)
	_, err = app.Auth.CreateUser("admin", "adminpw", "管理员", auth.RoleAdmin)
	require.NoError(t, err)
	app.persistUser("admin")
	client, err := localmodel.New(provider.URL, app.Log)
	require.NoError(t, err)
	client.Services = map[string]localmodel.Service{
		"vl":     {ID: "vl", Model: "text-v1", Capabilities: []string{"text"}},
		"asr":    {ID: "asr", Model: "asr-v1", Capabilities: []string{"asr"}},
		"tts":    {ID: "tts", Model: "zh-v1", Capabilities: []string{"tts"}},
		"tts-en": {ID: "tts-en", Model: "en-v1", Capabilities: []string{"tts"}},
		"text2":  {ID: "text2", Model: "text-v2", Capabilities: []string{"text"}},
	}
	app.Local = client
	token, _ := loginGet(t, app, "admin", "adminpw")

	get := httptest.NewRequest(http.MethodGet, "/api/v1/admin/model-profiles", nil)
	get.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	app.Router().ServeHTTP(got, get)
	require.Equal(t, http.StatusOK, got.Code)
	t.Logf("profiles response: %s", got.Body.String())
	var profilesResp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &profilesResp))
	var profiles []store.ModelProfile
	require.NoError(t, json.Unmarshal(profilesResp.Data, &profiles))
	require.Len(t, profiles, 1)
	require.Equal(t, "default", profiles[0].Name)

	body := `{"name":"fast","text_service":"text2","asr_service":"asr","tts_zh_service":"tts","tts_en_service":"tts-en","active":false}`
	post := httptest.NewRequest(http.MethodPost, "/api/v1/admin/model-profiles", strings.NewReader(body))
	post.Header.Set("Authorization", "Bearer "+token)
	post.Header.Set("Content-Type", "application/json")
	saved := httptest.NewRecorder()
	app.Router().ServeHTTP(saved, post)
	require.Equal(t, http.StatusOK, saved.Code)
	var savedResp struct {
		Code int                `json:"code"`
		Data store.ModelProfile `json:"data"`
	}
	require.NoError(t, json.Unmarshal(saved.Body.Bytes(), &savedResp))
	require.Equal(t, 0, savedResp.Code)
	require.NotZero(t, savedResp.Data.ID)

	apply := httptest.NewRequest(http.MethodPost, "/api/v1/admin/model-profiles/"+itoa64(savedResp.Data.ID)+"/apply", nil)
	apply.Header.Set("Authorization", "Bearer "+token)
	applied := httptest.NewRecorder()
	app.Router().ServeHTTP(applied, apply)
	require.Equal(t, http.StatusOK, applied.Code)
	require.Equal(t, "text2", client.Profile().Text)

	refresh := httptest.NewRequest(http.MethodPost, "/api/v1/admin/speech/refresh", strings.NewReader(`{"text":"cat","lang":"en"}`))
	refresh.Header.Set("Authorization", "Bearer "+token)
	refresh.Header.Set("Content-Type", "application/json")
	refreshed := httptest.NewRecorder()
	app.Router().ServeHTTP(refreshed, refresh)
	require.Equal(t, http.StatusOK, refreshed.Code)
	var assets []store.SpeechAsset
	require.NoError(t, db.Where("speech_text = ? AND language = ?", "cat", "en").Find(&assets).Error)
	require.Len(t, assets, 1)
	require.Equal(t, "ready", assets[0].Status)
}

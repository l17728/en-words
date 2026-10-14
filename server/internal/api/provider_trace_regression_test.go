package api

import (
	"bytes"
	"enwords/internal/auth"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClassroomSynthesisOwnershipAndProviderTrace(t *testing.T) {
	a, _, lid, _ := setupRoom(t)
	owner, _ := loginGet(t, a, "sfin", "pw")
	_, err := a.Auth.CreateUser("speech-outsider", "pw", "other", auth.RoleStudent)
	require.NoError(t, err)
	outsider, _ := loginGet(t, a, "speech-outsider", "pw")
	wav, err := os.ReadFile("../../../review/audio-fixtures/en/01.wav")
	require.NoError(t, err)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, fmt.Sprintf("lesson-%d", lid), r.Header.Get("X-Trace-ID"))
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav)
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	a.SetLogger(obs.NewWithSink(&logs))
	client, err := localmodel.New(upstream.URL, a.Log)
	require.NoError(t, err)
	client.Services["tts"] = localmodel.Service{ID: "tts", Model: "fixture", Capabilities: []string{"tts"}}
	a.Local = client
	router := a.Router()
	for _, token := range []string{outsider, owner} {
		req := httptest.NewRequest("POST", "/api/v1/speech/synthesize", strings.NewReader(fmt.Sprintf(`{"text":"测试朗读","lang":"zh","lesson_id":%d}`, lid)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Trace-ID", "speech-request-marker")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if token == outsider {
			require.Equal(t, errForbidden, doJSON(t, response).Code)
			require.Zero(t, calls.Load())
			continue
		}
		require.Equal(t, 200, response.Code)
		require.Equal(t, wav, response.Body.Bytes())
		require.EqualValues(t, 1, calls.Load())
	}
	require.Contains(t, logs.String(), fmt.Sprintf(`"trace_id":"lesson-%d"`, lid))
	require.Contains(t, logs.String(), `"event":"synthesis_requested"`)
	require.Contains(t, logs.String(), `"request_trace_id":`)
	require.NotContains(t, logs.String(), "测试朗读")
}

func TestClassroomASRForwardsBrowserPCMWithOwnedLessonTrace(t *testing.T) {
	a, _, lid, _ := setupRoom(t)
	owner, _ := loginGet(t, a, "sfin", "pw")
	wav, err := os.ReadFile("../../../review/audio-fixtures/en/01.wav")
	require.NoError(t, err)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/api/asr/transcriptions", r.URL.Path)
		require.Equal(t, fmt.Sprintf("lesson-%d", lid), r.Header.Get("X-Trace-ID"))
		require.NoError(t, r.ParseMultipartForm(1<<20))
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		require.NoError(t, err)
		defer file.Close()
		require.Equal(t, "recording.wav", header.Filename)
		require.Equal(t, "application/octet-stream", header.Header.Get("Content-Type"), "upstream multipart compatibility remains unchanged")
		data, err := io.ReadAll(file)
		require.NoError(t, err)
		require.Equal(t, wav, data)
		require.Equal(t, "en", r.FormValue("language"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"apple"}`))
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	a.SetLogger(obs.NewWithSink(&logs))
	client, err := localmodel.New(upstream.URL, a.Log)
	require.NoError(t, err)
	client.Services["asr"] = localmodel.Service{ID: "asr", Model: "fixture", Capabilities: []string{"asr"}}
	a.Local = client
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="untrusted-name.wav"`)
	header.Set("Content-Type", "audio/wav")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(wav)
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("language", "en"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/classroom/lessons/%d/transcribe", lid), &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+owner)
	response := httptest.NewRecorder()
	a.Router().ServeHTTP(response, req)
	require.Equal(t, 200, response.Code)
	require.Equal(t, 0, doJSON(t, response).Code)
	require.EqualValues(t, 1, calls.Load())
	require.Contains(t, logs.String(), `"event":"audio_received"`)
	require.Contains(t, logs.String(), fmt.Sprintf(`"trace_id":"lesson-%d"`, lid))
	require.Contains(t, logs.String(), `"request_trace_id":`)
	require.Contains(t, logs.String(), `"format":"wav"`)
	require.NotContains(t, logs.String(), "untrusted-name")
}

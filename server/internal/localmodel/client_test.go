package localmodel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsoleDiscoveryAndChatContract(t *testing.T) {
	var chatCalls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			if r.Method != http.MethodGet {
				t.Error("discovery must GET")
			}
			_, _ = w.Write([]byte(`{"services":[{"id":"vl","model":"fixture-text","capabilities":["text"],"proxyRoute":"http://invalid.example/ignored"}]}`))
		case "/api/vl/chat":
			chatCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["model"] != "fixture-text" || body["stream"] != false || body["temperature"] != float64(0) {
				t.Errorf("unexpected contract: %#v", body)
			}
			if r.Header.Get("X-Trace-ID") == "" {
				t.Error("missing trace")
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"correct\":true}"}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, nil)
	require.NoError(t, err)
	require.NoError(t, c.Discover(context.Background()))
	var out struct {
		Correct bool `json:"correct"`
	}
	require.NoError(t, c.ChatJSON(context.Background(), "fixture system", "fixture answer", &out))
	require.True(t, out.Correct)
	require.EqualValues(t, 1, chatCalls.Load())
}

func TestConsoleRejectsMalformedResponsesAndRedirect(t *testing.T) {
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1); w.WriteHeader(200) }))
	defer destination.Close()
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"bad envelope", "not json", 200},
		{"missing choices", `{}`, 200},
		{"bad structured output", `{"choices":[{"message":{"content":"not json"}}]}`, 200},
		{"unavailable", `{"error":"fixture"}`, 503},
		{"redirect", "", 302},
		{"oversized", strings.Repeat("x", (1<<20)+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			c, err := New(s.URL, nil)
			require.NoError(t, err)
			c.Services["vl"] = Service{Model: "fixture", Capabilities: []string{"text"}}
			var out map[string]any
			require.Error(t, c.ChatJSON(context.Background(), "", "", &out))
		})
	}
	require.Zero(t, followed.Load(), "redirect must never contact another origin")
}

func TestConsoleCancellationAndInvalidOrigin(t *testing.T) {
	for _, origin := range []string{"file:///tmp/model", "http://user:secret@localhost", "http://localhost/path", "http://localhost?key=secret"} {
		_, err := New(origin, nil)
		require.Error(t, err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request reached server") }))
	defer s.Close()
	c, err := New(s.URL, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, c.Discover(ctx))
}

func TestConsoleMediaContractsAndLimits(t *testing.T) {
	wav := pcmWAVFixture()
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/api/asr/transcriptions":
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("model") != "fixture-asr" || r.FormValue("language") != "en" {
				t.Error("ASR model/language contract")
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			raw, err := io.ReadAll(file)
			if err != nil || string(raw) != "fixture audio" || header.Filename != "recording.wav" {
				t.Error("ASR upload contract")
			}
			_, _ = w.Write([]byte(`{"text":" cat "}`))
		case "/api/tts/speech":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["model"] != "fixture-tts" || body["input"] != "猫" || body["response_format"] != "wav" {
				t.Error("TTS request contract")
			}
			_, _ = w.Write(wav)
		default:
			t.Error("unexpected media path")
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, nil)
	require.NoError(t, err)
	c.Services["asr"] = Service{Model: "fixture-asr", Capabilities: []string{"asr"}}
	c.Services["tts"] = Service{Model: "fixture-tts", Capabilities: []string{"tts"}}
	transcript, err := c.Transcribe(context.Background(), []byte("fixture audio"), "recording.wav", "en")
	require.NoError(t, err)
	require.Equal(t, "cat", transcript)
	result, err := c.Synthesize(context.Background(), "猫", "zh")
	require.NoError(t, err)
	require.Equal(t, wav, result)
	_, err = c.Transcribe(context.Background(), make([]byte, MaxAudio+1), "recording.wav", "en")
	require.Error(t, err)
	_, err = c.Synthesize(context.Background(), "cat", "en")
	require.Error(t, err)
	_, err = c.Synthesize(context.Background(), strings.Repeat("猫", 301), "zh")
	require.Error(t, err)
	require.EqualValues(t, 2, calls.Load(), "invalid media must not reach provider")
}

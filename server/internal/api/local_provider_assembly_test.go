package api

import (
	"context"
	"encoding/json"
	"enwords/internal/judge"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalProviderDiscoveryAndStructuredHealthGate(t *testing.T) {
	for _, tc := range []struct {
		name, discovery, completion string
		chatStatus                  int
		wantErr, wantLLM            bool
	}{
		{"text healthy", `{"services":[{"id":"vl","model":"fixture","capabilities":["text"]}]}`, `{"ok":true}`, 200, false, true},
		{"empty registry", `{"services":[]}`, `{"ok":true}`, 200, true, false},
		{"bad discovery", `bad json`, `{"ok":true}`, 200, true, false},
		{"text unhealthy", `{"services":[{"id":"vl","model":"fixture","capabilities":["text"]}]}`, `{"ok":false}`, 200, false, false},
		{"text malformed", `{"services":[{"id":"vl","model":"fixture","capabilities":["text"]}]}`, `not json`, 200, false, false},
		{"speech survives text outage", `{"services":[{"id":"vl","model":"fixture","capabilities":["text"]},{"id":"asr","model":"fixture-asr","capabilities":["asr"]},{"id":"tts","model":"fixture-tts","capabilities":["tts"]}]}`, ``, 502, false, false},
		{"speech without text", `{"services":[{"id":"asr","model":"fixture-asr","capabilities":["asr"]}]}`, ``, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/models" {
					_, _ = w.Write([]byte(tc.discovery))
					return
				}
				if r.URL.Path != "/api/vl/chat" {
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if tc.chatStatus == 0 {
					t.Error("text probe must not run without text capability")
				}
				if tc.chatStatus != 200 {
					w.WriteHeader(tc.chatStatus)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.completion}}}})
			}))
			defer server.Close()
			t.Setenv("ENWORDS_MODEL_CONSOLE", server.URL)
			app, _ := newTestApp(t)
			err := app.EnableLocalProviders(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, app.Local)
			} else {
				require.NoError(t, err)
				require.NotNil(t, app.Local)
			}
			if tc.wantLLM {
				require.IsType(t, &judge.ZhipuLLM{}, app.Judge.LLM)
			} else {
				require.Nil(t, app.Judge.LLM)
			}
		})
	}
	t.Run("invalid origin", func(t *testing.T) {
		t.Setenv("ENWORDS_MODEL_CONSOLE", "file:///fixture")
		a, _ := newTestApp(t)
		require.Error(t, a.EnableLocalProviders(context.Background()))
	})
}

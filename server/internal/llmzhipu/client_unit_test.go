package llmzhipu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfflineClientRequestAndStructuredReply(t *testing.T) {
	for _, structured := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[structured], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "/v4/chat/completions", r.URL.Path)
				require.Equal(t, "Bearer fixture-only-key", r.Header.Get("Authorization"))
				require.Equal(t, "application/json", r.Header.Get("Content-Type"))
				var input struct {
					Model    string `json:"model"`
					Messages []struct{ Role, Content string }
					Format   map[string]string `json:"response_format"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				require.Equal(t, "glm-4-flash", input.Model)
				require.Len(t, input.Messages, 2)
				require.Equal(t, "system", input.Messages[0].Role)
				require.Equal(t, "fixture policy", input.Messages[0].Content)
				require.Equal(t, "user", input.Messages[1].Role)
				require.Equal(t, "fixture question", input.Messages[1].Content)
				if structured {
					require.Equal(t, "json_object", input.Format["type"])
				} else {
					require.Nil(t, input.Format)
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
			}))
			defer server.Close()
			client := New("fixture-only-key", server.URL+"/v4", "")
			if structured {
				var result struct {
					OK bool `json:"ok"`
				}
				require.NoError(t, client.ChatJSON(context.Background(), "fixture policy", "fixture question", &result))
				require.True(t, result.OK)
			} else {
				result, err := client.Chat(context.Background(), "fixture policy", "fixture question")
				require.NoError(t, err)
				require.Equal(t, `{"ok":true}`, result)
			}
		})
	}
}

func TestOfflineClientRejectsUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{"error":"fixture unauthorized"}`},
		{"unavailable", 503, `unavailable`},
		{"malformed_response", 200, `not JSON`},
		{"empty_choices", 200, `{"choices":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := New("fixture", server.URL, "fixture-model")
			_, err := client.Chat(context.Background(), "policy", "question")
			require.Error(t, err)
			var out map[string]any
			require.Error(t, client.ChatJSON(context.Background(), "policy", "question", &out))
		})
	}
}

func TestOfflineClientCancellationAndInvalidEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		canceled  bool
	}{
		{"malformed_url", ":invalid", false},
		{"unsupported_protocol", "fixture://local", false},
		{"canceled", "http://127.0.0.1:1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New("fixture", tc.url, "fixture-model")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			_, err := client.Chat(ctx, "policy", "question")
			require.Error(t, err)
			var out map[string]any
			err = client.ChatJSON(ctx, "policy", "question", &out)
			require.Error(t, err)
			if tc.canceled {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestOfflineClientStructuredContentMustBeJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not JSON"}}]}`))
	}))
	defer server.Close()
	var out map[string]any
	require.Error(t, New("fixture", server.URL, "fixture").ChatJSON(context.Background(), "policy", "question", &out))
}

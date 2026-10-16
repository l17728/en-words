package remoteprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/obs"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidationLoggingIsSafeAndNotTransportSuccess(t *testing.T) {
	var logs bytes.Buffer
	c := configured(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, completionBody(`{"known":null,"private":"fixture-private-body"}`))
	})
	c.log = obs.New(&logs)
	ctx := obs.WithTrace(context.Background(), "trace-provider-fixture")
	var target struct {
		Known bool `json:"known"`
	}
	require.Error(t, c.ChatJSON(ctx, "private-prompt", "private-answer", &target))
	got := logs.String()
	require.Contains(t, got, "response_received")
	require.Contains(t, got, "validation_failed")
	require.Contains(t, got, "trace-provider-fixture")
	validationEvents := 0
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event["event"] == "validation_failed" {
			validationEvents++
		}
	}
	require.Equal(t, 1, validationEvents)
	for _, secret := range []string{"request_completed", "fixture-key", "fixture-private-body", "private-prompt", "private-answer"} {
		require.NotContains(t, got, secret)
	}
}

func configured(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "fixture-key", AllowLoopbackHTTP: true}, nil)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}
func completionBody(content string) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
	return string(raw)
}

func TestConfigurationBoundaries(t *testing.T) {
	good := Config{BaseURL: "https://example.test/v1", Model: "org/model", APIKey: "fixture"}
	c, e := New(good, nil)
	require.NoError(t, e)
	c.Close()
	for _, origin := range []string{"http://example.test/v1", "http://localhost/v1", "https://user:pass@example.test/v1", "https://example.test/v1?key=x", "https://example.test/v1#x", "https://example.test/v2", "https://example.test/%76%31", "bad%url", " https://example.test/v1", "ftp://127.0.0.1/v1"} {
		t.Run(origin, func(t *testing.T) {
			cfg := good
			cfg.BaseURL = origin
			cfg.AllowLoopbackHTTP = true
			_, err := New(cfg, nil)
			require.Error(t, err)
		})
	}
	for _, origin := range []string{"http://127.0.0.1/v1/", "http://[::1]/v1"} {
		cfg := good
		cfg.BaseURL = origin
		_, e = New(cfg, nil)
		require.Error(t, e)
		cfg.AllowLoopbackHTTP = true
		c, e = New(cfg, nil)
		require.NoError(t, e)
		c.Close()
	}
	for _, key := range []string{"", "a b", "a\nb", strings.Repeat("k", 4097)} {
		cfg := good
		cfg.APIKey = key
		_, e = New(cfg, nil)
		require.Error(t, e)
	}
	for _, model := range []string{"", "bad model", "bad\nmodel", strings.Repeat("m", 257)} {
		cfg := good
		cfg.Model = model
		_, e = New(cfg, nil)
		require.Error(t, e)
	}
}

func TestRedirectAndErrorDoNotLeak(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, status := range []int{302, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := configured(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, "secret-upstream-body")
			})
			_, err := c.Models(context.Background())
			var remote *Error
			require.ErrorAs(t, err, &remote)
			require.Equal(t, status, remote.Status)
			require.Equal(t, "http_status", remote.Code)
			require.NotContains(t, err.Error(), "secret")
			require.NotContains(t, err.Error(), "fixture-key")
		})
	}
	require.Zero(t, targetCalls.Load())
}

func TestModelsSchema(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"data":null}`, `{"data":{}}`, `{"data":[{}]}`, `{"data":[{"id":"x"},{"id":"x"}]}`, `{"data":[{"id":"x","id":"y"}]}`, `{"data":[],"data":[]}`, `{"data":[{"id":"bad id"}]}`, `{"data":[]} {}`} {
		t.Run(body, func(t *testing.T) {
			c := configured(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			_, err := c.Models(context.Background())
			require.Error(t, err)
		})
	}
	c := configured(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		fmt.Fprint(w, `{"object":"list","data":[{"id":"one"},{"id":"two"}]}`)
	})
	ids, err := c.Models(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, ids)
}

func TestChatContract(t *testing.T) {
	c := configured(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, false, req["stream"])
		require.Equal(t, "test-model", req["model"])
		fmt.Fprint(w, completionBody("hello"))
	})
	out, err := c.Chat(context.Background(), "system", "user")
	require.NoError(t, err)
	require.Equal(t, "hello", out)
	for _, body := range []string{`{}`, `{"choices":[]}`, strings.Replace(completionBody("x"), `"stop"`, `"length"`, 1), strings.Replace(completionBody("x"), `"assistant"`, `"user"`, 1), strings.Replace(completionBody("x"), `"content":"x"`, `"content":"x","tool_calls":[{}]`, 1), strings.Replace(completionBody("x"), `"content":"x"`, `"content":"x","refusal":"no"`, 1), completionBody(" "), completionBody(strings.Repeat("x", 65537)), `{"choices":null}`, `{"choices":[{"message":{"content":7}}]}`} {
		t.Run(fmt.Sprint(len(body))+body[:min(len(body), 50)], func(t *testing.T) {
			x := configured(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			_, e := x.Chat(context.Background(), "", "question")
			require.Error(t, e)
		})
	}
	for _, user := range []string{" ", strings.Repeat("u", 32769)} {
		_, err = c.Chat(context.Background(), "", user)
		require.Error(t, err)
	}
	_, err = c.Chat(context.Background(), strings.Repeat("s", 8193), "u")
	require.Error(t, err)
}

func TestStructuredSchema(t *testing.T) {
	for _, content := range []string{`null`, `[]`, `{"known":null}`, `{"known":true,"extra":1}`, `{"known":true,"known":false}`, `{"known":true} {}`, `{"known":"true"}`, `{"nested":{"x":1,"x":2}}`, strings.Repeat(`{"x":`, 34) + `0` + strings.Repeat(`}`, 34)} {
		t.Run(content[:min(len(content), 60)], func(t *testing.T) {
			c := configured(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, completionBody(content)) })
			var target struct {
				Known bool `json:"known"`
			}
			require.Error(t, c.ChatJSON(context.Background(), "", "question", &target))
		})
	}
	c := configured(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, map[string]any{"type": "json_object"}, req["response_format"])
		fmt.Fprint(w, completionBody(`{"known":true}`))
	})
	var target struct {
		Known bool `json:"known"`
	}
	require.NoError(t, c.ChatJSON(context.Background(), "", "question", &target))
	require.True(t, target.Known)
	require.Error(t, c.ChatJSON(context.Background(), "", "question", nil))
}

// This transport adapter does not infer required fields from arbitrary Go structs.
// A decision-specific adapter must validate required fields, enums and ranges.
func TestStructuredRequiredFieldsRemainCallerResponsibility(t *testing.T) {
	c := configured(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, completionBody(`{}`)) })
	var target struct {
		Known bool `json:"known"`
	}
	require.NoError(t, c.ChatJSON(context.Background(), "", "question", &target))
	require.False(t, target.Known)
}

func TestStructuredNestedNullAndCaseAlias(t *testing.T) {
	for _, content := range []string{`{"known":true,"Known":false}`, `{"nested":{"value":null}}`, `{"nested":{"value":true,"VALUE":false}}`, `{"items":[null]}`} {
		t.Run(content, func(t *testing.T) {
			c := configured(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, completionBody(content)) })
			type nested struct {
				Value bool `json:"value"`
			}
			target := struct {
				Known  bool   `json:"known"`
				Nested nested `json:"nested"`
				Items  []bool `json:"items"`
			}{Known: true, Nested: nested{Value: true}, Items: []bool{true}}
			before, err := json.Marshal(target)
			require.NoError(t, err)
			require.Error(t, c.ChatJSON(context.Background(), "", "question", &target))
			after, err := json.Marshal(target)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestDeadlineBodyCancelAndReadLimit(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			c := configured(t, func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := c.Models(ctx)
			require.Error(t, err)
			require.Less(t, time.Since(start), time.Second)
		})
	}
	c := configured(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", MaxResponse+1)) })
	_, err := c.Models(context.Background())
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.Chat(ctx, "", "question")
	require.Error(t, err)
	_, err = c.Models(nil)
	require.Error(t, err)
}

func TestUniqueJSONBoundaries(t *testing.T) {
	for _, raw := range []string{``, `{"a":1`, `]`, `{"a":[1,`, `{"a":1,"a":2}`, `{} true`} {
		require.Error(t, uniqueJSON([]byte(raw)))
	}
	for _, raw := range []string{`{"a":[1,true,null,{"b":"x"}]}`, `[]`, `42`} {
		require.NoError(t, uniqueJSON([]byte(raw)))
	}
}

func TestStructuredFailureDoesNotMutateTarget(t *testing.T) {
	c := configured(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, completionBody(`{"known":false,"unknown":7}`))
	})
	target := struct {
		Known bool `json:"known"`
	}{Known: true}
	require.Error(t, c.ChatJSON(context.Background(), "", "question", &target))
	require.True(t, target.Known)
	require.Error(t, c.ChatJSON(context.Background(), "", "question", target))
	require.Error(t, c.ChatJSON(context.Background(), "", "question", new(map[string]any)))
	require.Error(t, uniqueJSON([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}))
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalEvaluationFrozenCandidateDatasetValidation(t *testing.T) {
	raw, err := os.ReadFile("../../../review/feedback-candidate-holdout.json")
	require.NoError(t, err)
	const frozen = "480f46a27696b5f331d27888dc075b7e86ca9a7c6d5f3f3ff488adc8aea98b2e"
	data, err := decodeFrozenDataset(raw, frozen)
	require.NoError(t, err)
	require.Len(t, data.Cases, 32)
	require.Equal(t, 2, data.Repeats)
	_, err = decodeFrozenDataset(append(raw, ' '), frozen)
	require.ErrorContains(t, err, "hash")
	for _, mode := range []string{"count", "repeats", "status", "duplicate", "anchor"} {
		t.Run(mode, func(t *testing.T) {
			var changed dataset
			require.NoError(t, json.Unmarshal(raw, &changed))
			switch mode {
			case "count":
				changed.Cases = changed.Cases[:31]
			case "repeats":
				changed.Repeats = 1
			case "status":
				changed.Status = "draft"
			case "duplicate":
				changed.Cases[1].ID = changed.Cases[0].ID
			case "anchor":
				changed.Cases[0].Anchor = "tomorrow"
			}
			b, err := json.Marshal(changed)
			require.NoError(t, err)
			_, err = decodeFrozenDataset(b, hash(b))
			require.Error(t, err)
		})
	}
}

func TestLocalEvaluationStrictSchemaRejectsZeroValueCoercion(t *testing.T) {
	valid := `{"intents":[{"kind":"C_frequency","target_words":0,"lessons_per_day":2,"date":"","word":"","confidence":0.9,"needs_confirm":false}]}`
	_, ok := schema(valid)
	require.True(t, ok)
	for _, bad := range []string{strings.Replace(valid, `"needs_confirm":false`, `"needs_confirm":null`, 1), strings.Replace(valid, `"lessons_per_day":2`, `"lessons_per_day":"2"`, 1), strings.Replace(valid, `"confidence":0.9`, `"confidence":2`, 1), strings.Replace(valid, `"kind":"C_frequency"`, `"kind":"delete_all"`, 1), strings.Replace(valid, `"word":"",`, "", 1), valid + " explanation", `{"intents":[]}`} {
		_, ok := schema(bad)
		require.False(t, ok, bad)
	}
}
func TestLocalEvaluationSemanticSetsAndCriticalSlots(t *testing.T) {
	one := map[string]any{"kind": "C_frequency", "lessons_per_day": float64(2)}
	want := []map[string]any{{"kind": "C_frequency", "lessons_per_day": 2}}
	require.True(t, matches([]map[string]any{one, one}, want), "model duplicate intents count once semantically")
	require.False(t, len([]map[string]any{one, one}) == len(want), "product output additionally checks exact count")
	require.False(t, matches([]map[string]any{{"kind": "C_frequency", "lessons_per_day": 1}}, want))
	require.False(t, matches([]map[string]any{{"kind": "D_postpone", "needs_confirm": false}}, []map[string]any{{"kind": "D_postpone", "needs_confirm": true}}))
}
func TestLocalEvaluationRejectsNonLoopbackOrigins(t *testing.T) {
	for _, base := range []string{"https://example.com", "http://localhost:8090", "http://127.0.0.1:8090/path", "http://127.0.0.1:8090?token=x", "http://user:pass@127.0.0.1:8090", "http://192.168.1.1:8090"} {
		require.Error(t, loopbackOrigin(base))
	}
	require.NoError(t, loopbackOrigin("http://127.0.0.1:8090"))
	require.NoError(t, loopbackOrigin("http://[::1]:8090"))
}

type evalRoundTrip func(*http.Request) (*http.Response, error)

func (fn evalRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestLocalEvaluationRecordsSameRawResponseWithoutSecondCall(t *testing.T) {
	calls := 0
	content := `{"intents":[]}`
	envelope, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	r := &recorder{input: "synthetic input", base: evalRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(req.Body)
		require.Equal(t, `{"model":"fixture"}`, string(body))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(envelope))}, nil
	})}
	req, _ := http.NewRequest("POST", "http://127.0.0.1:8090/api/vl/chat", strings.NewReader(`{"model":"fixture"}`))
	resp, err := r.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, envelope, raw)
	require.Equal(t, 1, calls)
	require.Len(t, r.calls, 1)
	require.Equal(t, content, r.calls[0].Content)
	require.Equal(t, hash([]byte(`{"model":"fixture"}`)), r.calls[0].Fingerprint)
}

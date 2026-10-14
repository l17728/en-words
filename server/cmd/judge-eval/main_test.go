package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func syntheticSample(accept bool) sample {
	return sample{ID: "synthetic", Word: "cat", Meanings: []string{"猫"}, Answer: "这是一只猫。", Expected: &accept, Allowed: map[bool][]string{true: {"correct", "near"}, false: {"wrong"}}[accept]}
}
func offlineClient(t *testing.T, body string, failure bool) (*localmodel.Client, *recorder, *int) {
	t.Helper()
	calls := new(int)
	c, err := localmodel.New("http://127.0.0.1:8090", obs.Nop())
	require.NoError(t, err)
	c.Services["vl"] = localmodel.Service{ID: "vl", Model: "offline-model", Capabilities: []string{"text"}}
	transport := &recorder{base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		*calls++
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 8*time.Second)
		require.Greater(t, time.Until(deadline), 7*time.Second)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		require.EqualValues(t, 512, payload["max_tokens"])
		require.EqualValues(t, 0, payload["temperature"])
		require.Equal(t, false, payload["stream"])
		require.Equal(t, "json_object", payload["response_format"].(map[string]any)["type"])
		if failure {
			return nil, errors.New("offline transport failure")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	c.HTTP.Transport = transport
	return c, transport, calls
}
func envelope(content string) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	return string(raw)
}

func TestJudgeEvalSameResponseProductionReplayAndContract(t *testing.T) {
	c, r, calls := offlineClient(t, envelope(`{"judgment":"near"}`), false)
	got, err := evaluate(context.Background(), syntheticSample(true), 1, c, "frozen-prompt", r)
	require.NoError(t, err)
	require.Equal(t, 1, *calls)
	require.True(t, got.RawOK)
	require.True(t, got.ProductOK)
	require.Equal(t, "l2", got.Decision.Source)
	require.Equal(t, 1, got.ReplayCalls)
	var request struct {
		Messages []struct{ Role, Content string }
	}
	require.NoError(t, json.Unmarshal(got.Wire.Request, &request))
	require.Equal(t, "frozen-prompt", request.Messages[0].Content)
	var user map[string]any
	require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &user))
	require.Equal(t, syntheticSample(true).Answer, user["student_answer"])
	require.Equal(t, []any{"猫"}, user["standard_meanings"])
	require.Equal(t, "cat", user["word"])
	require.Equal(t, digest(got.Wire.Request), got.Wire.RequestSHA)
}
func TestJudgeEvalL1DoesNotBecomeModelCredit(t *testing.T) {
	c, r, calls := offlineClient(t, envelope(`{"judgment":"wrong"}`), false)
	s := syntheticSample(true)
	s.Answer = "cat"
	got, err := evaluate(context.Background(), s, 1, c, "frozen-prompt", r)
	require.NoError(t, err)
	require.Equal(t, 1, *calls)
	require.False(t, got.RawOK)
	require.True(t, got.ProductOK)
	require.Equal(t, "l1", got.Decision.Source)
	require.Zero(t, got.ReplayCalls)
}
func TestJudgeEvalFailuresDoNotEarnNegativeSemanticCredit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		transport  bool
	}{{"invalid-enum", envelope(`{"judgment":"yes"}`), false}, {"missing", envelope(`{}`), false}, {"json", envelope(`not json`), false}, {"transport", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			c, r, calls := offlineClient(t, tc.body, tc.transport)
			s := syntheticSample(false)
			got, err := evaluate(context.Background(), s, 1, c, "frozen", r)
			require.NoError(t, err)
			require.Equal(t, 1, *calls)
			require.False(t, got.SchemaOK)
			require.False(t, got.RawOK)
			require.False(t, got.ProductOK)
			require.True(t, got.ConservativeReject)
			require.False(t, got.ProductUnsafe)
		})
	}
}
func TestJudgeEvalReplayRejectsDifferentSyntheticRequest(t *testing.T) {
	p := &promptClient{user: "original", result: []byte(`{"judgment":"correct"}`)}
	r := &replayClient{record: p}
	var out any
	require.Error(t, r.ChatJSON(context.Background(), "wrong-prompt", "original", &out))
	require.Error(t, r.ChatJSON(context.Background(), "wrong-prompt", "changed", &out))
}
func TestJudgeEvalFrozenDatasetsAndPrompts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "review", "judge-semantic-holdout40.json"))
	require.NoError(t, err)
	sha := "509423198f2045c91f82ed384e36a9b271db5db221cb05f58b8ab7590c5fed45"
	d, err := frozenData(raw, sha)
	require.NoError(t, err)
	require.Len(t, d.Cases, 40)
	_, err = frozenData(raw, strings.Repeat("0", 64))
	require.Error(t, err)
	for _, mutate := range []func(*dataset){func(d *dataset) { d.Cases = d.Cases[:39] }, func(d *dataset) { d.Cases[1].ID = d.Cases[0].ID }, func(d *dataset) { d.Status = "draft" }, func(d *dataset) { d.Cases[0].Expected = nil }, func(d *dataset) { d.Cases[0].Allowed = []string{"wrong", "correct"} }} {
		var changed dataset
		require.NoError(t, json.Unmarshal(raw, &changed))
		mutate(&changed)
		b, _ := json.Marshal(changed)
		_, err = frozenData(b, digest(b))
		require.Error(t, err)
	}
	for name, sha := range map[string]string{"judge-prompt-baseline-20261014.txt": baselineSHA, "judge-prompt-candidate-20261014.txt": candidateSHA} {
		b, e := os.ReadFile(filepath.Join("..", "..", "..", "review", name))
		require.NoError(t, e)
		require.Equal(t, sha, digest(b))
	}
}
func TestJudgeEvalSourceHashTracksCodeNotArtifacts(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("one"), 0600))
	a, e := sourceDigest(root)
	require.NoError(t, e)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".artifacts"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".artifacts", "scratch.go"), []byte("ignore"), 0600))
	b, e := sourceDigest(root)
	require.NoError(t, e)
	require.Equal(t, a, b)
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("two"), 0600))
	b, e = sourceDigest(root)
	require.NoError(t, e)
	require.NotEqual(t, a, b)
}

type brokenReport struct{ closed, synced bool }

func (*brokenReport) Write([]byte) (int, error) { return 0, errors.New("fixture disk write failed") }
func (b *brokenReport) Sync() error             { b.synced = true; return errors.New("fixture sync failed") }
func (b *brokenReport) Close() error            { b.closed = true; return errors.New("fixture close failed") }
func TestJudgeEvalReportErrorsAndSourceChangeCannotPass(t *testing.T) {
	w := &brokenReport{}
	err := finishReport(w, map[string]any{"expected_cases": 80})
	require.ErrorContains(t, err, "write failed")
	require.ErrorContains(t, err, "sync failed")
	require.ErrorContains(t, err, "close failed")
	require.True(t, w.closed)
	require.True(t, w.synced)
	r := map[string]any{"quality_gate_passed": true, "dataset_gate_passed": true}
	require.Error(t, sealSource(r, "before", "after", nil))
	require.Equal(t, false, r["quality_gate_passed"])
	require.Equal(t, false, r["dataset_gate_passed"])
}
func TestJudgeEvalNonregressionPairsSameRoundAndRetainsMissingCases(t *testing.T) {
	prior := []row{{ID: "one", Round: 1, Expected: true, RawOK: true, ProductOK: true}, {ID: "one", Round: 2, Expected: true, RawOK: true, ProductOK: true}}
	current := []row{{ID: "one", Round: 1, Expected: true, RawOK: false, ProductOK: true}}
	got, err := regressions(prior, current)
	require.NoError(t, err)
	require.Equal(t, []string{"one/1:raw", "one/2:unattempted"}, got)
	current[0].Expected = false
	_, err = regressions(prior, current)
	require.Error(t, err)
}

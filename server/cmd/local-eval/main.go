// local-eval is an explicit opt-in quality probe using production adapters.
// It never changes model configuration or executes Agent business actions.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"enwords/internal/agent"
	"enwords/internal/feedback"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
)

type evalCase struct {
	ID        string           `json:"case_id"`
	Text      string           `json:"text"`
	Anchor    string           `json:"anchor"`
	Expected  []map[string]any `json:"expected_intents"`
	Actions   []string         `json:"expected_actions"`
	Forbidden []string         `json:"forbidden_actions"`
}
type dataset struct {
	Version int        `json:"schema_version"`
	Status  string     `json:"status"`
	Repeats int        `json:"repeats"`
	Cases   []evalCase `json:"cases"`
}
type capture struct {
	Fingerprint string `json:"request_fingerprint"`
	Input       string `json:"synthetic_model_input"`
	Model       string `json:"model"`
	Content     string `json:"raw_synthetic_response"`
	DurationMS  int64  `json:"duration_ms"`
	Status      int    `json:"http_status"`
	Error       string `json:"error,omitempty"`
	SchemaOK    bool   `json:"schema_ok"`
	SemanticOK  bool   `json:"semantic_ok"`
}
type recorder struct {
	base  http.RoundTripper
	calls []*capture
	input string
}

func hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/api/vl/chat" {
		return r.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	c := &capture{Fingerprint: hash(body), Input: r.input, Model: fmt.Sprint(request["model"])}
	r.calls = append(r.calls, c)
	start := time.Now()
	resp, err := r.base.RoundTrip(req)
	c.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		c.Error = "transport_or_timeout"
		return nil, err
	}
	c.Status = resp.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	c.DurationMS = time.Since(start).Milliseconds()
	if readErr != nil {
		c.Error = "response_read_failed"
		return nil, readErr
	}
	if len(raw) > 1<<20 {
		c.Error = "response_oversized"
		return resp, nil
	}
	if resp.StatusCode != 200 {
		c.Error = "http_error"
		return resp, nil
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) != 1 {
		c.Error = "invalid_completion"
		return resp, nil
	}
	c.Content = envelope.Choices[0].Message.Content
	return resp, nil
}

type recordedParser struct {
	adapter  *feedback.ZhipuParser
	recorder *recorder
	fallback bool
}

func (p *recordedParser) ParseIntents(ctx context.Context, text string, at time.Time) ([]feedback.Intent, error) {
	p.recorder.input = text
	intents, err := p.adapter.ParseIntents(ctx, text, at)
	if err != nil || len(intents) == 0 {
		p.fallback = true
	}
	return intents, err
}
func schema(content string) ([]map[string]any, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &root) != nil || len(root) != 1 {
		return nil, false
	}
	var intents []map[string]any
	if json.Unmarshal(root["intents"], &intents) != nil || len(intents) == 0 {
		return nil, false
	}
	for _, it := range intents {
		if len(it) != 7 {
			return intents, false
		}
		kind, ok := it["kind"].(string)
		if !ok || !strings.Contains("|A_reduce|B_increase|C_frequency|D_postpone|E_hard_word|F_complaint|G_other|", "|"+kind+"|") {
			return intents, false
		}
		for _, key := range []string{"target_words", "lessons_per_day"} {
			n, ok := it[key].(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
				return intents, false
			}
		}
		for _, key := range []string{"date", "word"} {
			if _, ok := it[key].(string); !ok {
				return intents, false
			}
		}
		n, ok := it["confidence"].(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1 {
			return intents, false
		}
		if _, ok := it["needs_confirm"].(bool); !ok {
			return intents, false
		}
	}
	return intents, true
}

// Only necessary semantic slots are labeled. Unspecified slots are still schema-checked.
func matches(actual, expected []map[string]any) bool {
	unique := []map[string]any{}
	seen := map[string]bool{}
	for _, it := range actual {
		semantic := map[string]any{}
		for _, key := range []string{"kind", "target_words", "lessons_per_day", "date", "word"} {
			semantic[key] = it[key]
		}
		b, _ := json.Marshal(semantic)
		if !seen[string(b)] {
			unique = append(unique, it)
			seen[string(b)] = true
		}
	}
	if len(unique) != len(expected) {
		return false
	}
	used := make([]bool, len(unique))
	for _, want := range expected {
		found := false
		for i, got := range unique {
			if used[i] {
				continue
			}
			equal := true
			for k, v := range want {
				if fmt.Sprint(got[k]) != fmt.Sprint(v) {
					equal = false
					break
				}
			}
			if equal {
				used[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func productIntents(parsed feedback.ParseResult) []map[string]any {
	out := []map[string]any{}
	for _, it := range parsed.Intents {
		date := ""
		if it.Date != nil {
			date = it.Date.Format("2006-01-02")
		}
		out = append(out, map[string]any{"kind": string(it.Kind), "target_words": it.TargetWords, "lessons_per_day": it.LessonsPerDay, "word": it.Word, "date": date, "needs_confirm": it.NeedsConfirm})
	}
	return out
}
func sameStrings(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "|") == strings.Join(b, "|")
}
func loopbackOrigin(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("explicit HTTP loopback origin required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return errors.New("explicit numeric loopback host and port required")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func decodeFrozenDataset(raw []byte, expectedSHA string) (dataset, error) {
	if hash(raw) != strings.ToLower(expectedSHA) {
		return dataset{}, errors.New("dataset hash differs from frozen labels")
	}
	var data dataset
	if json.Unmarshal(raw, &data) != nil || data.Version != 1 || (len(data.Cases) != 64 && len(data.Cases) != 28 && len(data.Cases) != 32) || data.Repeats != 2 {
		return dataset{}, errors.New("expected version 1, 28/32/64 cases, two repetitions")
	}
	if data.Status != "frozen_reviewed" {
		return dataset{}, errors.New("dataset labels are not frozen_reviewed")
	}
	seen := map[string]bool{}
	for _, c := range data.Cases {
		if c.ID == "" || seen[c.ID] || len(c.Expected) == 0 || len(c.Actions) == 0 {
			return dataset{}, errors.New("invalid or duplicate labeled case")
		}
		seen[c.ID] = true
		if _, e := time.Parse(time.RFC3339, c.Anchor); e != nil {
			return dataset{}, e
		}
	}
	return data, nil
}

func run() (returnErr error) {
	base := flag.String("base", "http://127.0.0.1:8090", "explicit loopback Model Console origin")
	casesPath := flag.String("cases", "", "reviewed JSON dataset path")
	sha := flag.String("expected-sha256", "", "independently frozen dataset SHA-256")
	output := flag.String("output", "", "new report path; existing file is never overwritten")
	flag.Parse()
	if os.Getenv("ENWORDS_LOCAL_MODEL_EVAL") != "1" {
		return errors.New("set ENWORDS_LOCAL_MODEL_EVAL=1 explicitly")
	}
	if err := loopbackOrigin(*base); err != nil {
		return err
	}
	if *casesPath == "" || *sha == "" || *output == "" {
		return errors.New("cases, expected-sha256 and output are required")
	}
	raw, err := os.ReadFile(*casesPath)
	if err != nil {
		return err
	}
	data, err := decodeFrozenDataset(raw, *sha)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	report := map[string]any{"scope": "frozen curated synthetic feedback inputs; production Parser and Agent plan, no business writes; not a population accuracy estimate", "dataset_sha256": hash(raw), "started_at": time.Now().Format(time.RFC3339), "dataset_cases": len(data.Cases), "repeats": data.Repeats, "business_execution": false, "human_listening": "not_applicable"}
	defer func() { returnErr = errors.Join(returnErr, json.NewEncoder(file).Encode(report), file.Close()) }()
	journal, err := os.OpenFile(*output+".jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, journal.Close()) }()
	client, err := localmodel.New(*base, obs.Nop())
	if err != nil {
		return err
	}
	transport := &recorder{base: http.DefaultTransport}
	client.HTTP.Transport = transport
	if err = client.Discover(context.Background()); err != nil {
		report["fatal_error"] = err.Error()
		report["completed_cases"] = 0
		return err
	}
	report["models"] = client.Services
	rows := []map[string]any{}
	durations := []int64{}
	modelPass, modelTotal, structurePass, productPass, bypasses, fallbacks, forbidden := 0, 0, 0, 0, 0, 0, 0
	consecutiveUnavailable := 0
evaluation:
	for repetition := 1; repetition <= data.Repeats; repetition++ {
		for _, c := range data.Cases {
			transport.calls = nil
			anchor, _ := time.Parse(time.RFC3339, c.Anchor)
			wrapper := &recordedParser{adapter: feedback.NewZhipuParser(client), recorder: transport}
			parser := feedback.NewParser(func() time.Time { return anchor })
			parser.SetLLM(wrapper)
			ctx := obs.WithTrace(context.Background(), fmt.Sprintf("eval-%s-%d", c.ID, repetition))
			parsed, parseErr := parser.ParseContextAt(ctx, c.Text, anchor)
			plans := parser.PlanAt(parsed, anchor)
			actualActions := []string{}
			for _, p := range plans {
				actualActions = append(actualActions, string(p.Action))
			}
			agentActions := agent.NewSystemAgent(nil, nil, parser).Decide(agent.SystemInput{Trigger: "feedback_submitted", StudentID: 1, FeedbackText: c.Text, ParsedFeedback: &parsed, FeedbackAnchor: anchor})
			unsafe := []string{}
			for _, act := range agentActions {
				for _, blocked := range c.Forbidden {
					if act.Kind == blocked {
						unsafe = append(unsafe, act.Kind)
					}
				}
			}
			forbidden += len(unsafe)
			actualIntents := productIntents(parsed)
			productUnique := len(actualIntents) == len(c.Expected) && matches(actualIntents, c.Expected)
			productOK := parseErr == nil && productUnique && sameStrings(actualActions, c.Actions) && len(unsafe) == 0
			if productOK {
				productPass++
			}
			if wrapper.fallback {
				fallbacks++
			}
			if len(transport.calls) == 0 {
				bypasses++
			}
			for _, call := range transport.calls {
				if call.Status == 0 || call.Status >= 500 {
					consecutiveUnavailable++
				} else {
					consecutiveUnavailable = 0
				}
				modelTotal++
				durations = append(durations, call.DurationMS)
				expected := c.Expected
				if call.Input != c.Text {
					expected = nil
					for _, it := range c.Expected {
						if it["kind"] != "F_complaint" {
							expected = append(expected, it)
						}
					}
				}
				intents, valid := schema(call.Content)
				call.SchemaOK = valid && call.Error == ""
				call.SemanticOK = call.SchemaOK && matches(intents, expected)
				if call.SchemaOK {
					structurePass++
				}
				if call.SemanticOK {
					modelPass++
				}
			}
			row := map[string]any{"case_id": c.ID, "repeat": repetition, "policy_bypass": len(transport.calls) == 0, "fallback": wrapper.fallback, "model_calls": transport.calls, "product_passed": productOK, "product_unique_and_matches": productUnique, "product_intent_count": len(actualIntents), "product_intents": actualIntents, "plan_actions": actualActions, "agent_actions": agentActions, "forbidden_planned_actions": unsafe}
			if parseErr != nil {
				row["parse_error"] = parseErr.Error()
			}
			rows = append(rows, row)
			report["cases"], report["completed_cases"] = rows, len(rows)
			if err := json.NewEncoder(journal).Encode(row); err != nil {
				return err
			}
			if err := journal.Sync(); err != nil {
				return err
			}
			fmt.Printf("%s repeat=%d calls=%d fallback=%t product_pass=%t\n", c.ID, repetition, len(transport.calls), wrapper.fallback, productOK)
			if consecutiveUnavailable >= 5 {
				report["stopped_reason"] = "five_consecutive_provider_unavailable"
				break evaluation
			}
		}
	}
	report["cases"] = rows
	report["completed_cases"] = len(rows)
	report["unattempted_cases"] = len(data.Cases)*data.Repeats - len(rows)
	report["model_calls"] = modelTotal
	report["model_semantic_pass"] = modelPass
	report["model_schema_pass"] = structurePass
	report["product_pass"] = productPass
	report["policy_bypass"] = bypasses
	report["fallback"] = fallbacks
	report["forbidden_planned_actions"] = forbidden
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		report["latency_ms"] = map[string]any{"p50": durations[int(math.Ceil(float64(len(durations))*.5))-1], "p95": durations[int(math.Ceil(float64(len(durations))*.95))-1], "max": durations[len(durations)-1]}
	}
	passed := len(rows) == len(data.Cases)*data.Repeats && modelTotal > 0 && structurePass == modelTotal && modelPass*100 >= 90*modelTotal && productPass == len(rows) && forbidden == 0
	report["gate_passed"] = passed
	report["finished_at"] = time.Now().Format(time.RFC3339)
	if !passed {
		return errors.New("quality gate not met; report preserves every attempted case")
	}
	return nil
}

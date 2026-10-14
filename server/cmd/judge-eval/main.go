// judge-eval is an opt-in, synthetic-only probe of the production judge adapters.
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
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"enwords/internal/judge"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
)

const baselineSHA = "a2d6952e823e0eacc14e5d113685c787d2bba26ba529e3cd737c131c671f4b98"
const candidateSHA = "317647ba9d320a5af9337ed011b16be45f038b5dc3ac9365d9bdaf192a1c5b7f"

type sample struct {
	ID       string   `json:"case_id"`
	Group    string   `json:"group"`
	Word     string   `json:"word"`
	Meanings []string `json:"standard_meanings"`
	Answer   string   `json:"student_answer"`
	Expected *bool    `json:"expected_accept"`
	Allowed  []string `json:"allowed_judgments"`
}
type dataset struct {
	Version int      `json:"schema_version"`
	Status  string   `json:"status"`
	Scope   string   `json:"scope"`
	Cases   []sample `json:"cases"`
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func frozenData(raw []byte, sha string) (dataset, error) {
	var d dataset
	if len(sha) != 64 || digest(raw) != sha {
		return d, errors.New("dataset SHA mismatch")
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	if d.Version != 1 || d.Status != "labels_frozen_independent_review_approved_before_inference" || (len(d.Cases) != 1 && len(d.Cases) != 40 && len(d.Cases) != 100) {
		return d, errors.New("dataset must be reviewed frozen 1, 40 or 100 cases")
	}
	seen := map[string]bool{}
	for _, c := range d.Cases {
		if c.ID == "" || seen[c.ID] || strings.TrimSpace(c.Word) == "" || len(c.Meanings) == 0 || c.Expected == nil || len(c.Allowed) == 0 {
			return d, errors.New("incomplete or duplicate label")
		}
		seen[c.ID] = true
		for _, m := range c.Meanings {
			if strings.TrimSpace(m) == "" {
				return d, errors.New("empty meaning")
			}
		}
		labels := map[string]bool{}
		for _, v := range c.Allowed {
			if labels[v] || !(v == "wrong" || v == "near" || v == "correct") || (v != "wrong") != *c.Expected {
				return d, errors.New("inconsistent semantic labels")
			}
			labels[v] = true
		}
	}
	return d, nil
}

type wire struct {
	RequestSHA string          `json:"request_sha256"`
	Request    json.RawMessage `json:"synthetic_request"`
	Response   json.RawMessage `json:"synthetic_response,omitempty"`
	Status     int             `json:"http_status"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}
type recorder struct {
	base  http.RoundTripper
	calls []wire
}

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
	w := wire{RequestSHA: digest(body), Request: append([]byte(nil), body...)}
	start := time.Now()
	defer func() { w.DurationMS = time.Since(start).Milliseconds(); r.calls = append(r.calls, w) }()
	response, err := r.base.RoundTrip(req)
	if err != nil {
		w.Error = "transport_or_timeout"
		return nil, err
	}
	w.Status = response.StatusCode
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		w.Error = "response_read_failed"
		return nil, err
	}
	if len(raw) > 1<<20 {
		w.Error = "response_oversized"
	} else if json.Valid(raw) {
		w.Response = append([]byte(nil), raw...)
	} else {
		w.Error = "response_not_json"
	}
	return response, nil
}

// The production adapter constructs user JSON. Only the frozen system prompt is
// replaced; the production Client owns all HTTP/timeout/token parameters.
type promptClient struct {
	client judge.ZhipuChat
	prompt string
	user   string
	result []byte
	err    error
	calls  int
}

func (p *promptClient) ChatJSON(ctx context.Context, system, user string, out any) error {
	p.calls++
	if p.calls != 1 || system != judge.SystemPrompt {
		return errors.New("unexpected adapter request")
	}
	p.user = user
	p.err = p.client.ChatJSON(ctx, p.prompt, user, out)
	if p.err == nil {
		p.result, p.err = json.Marshal(out)
	}
	return p.err
}

type replayClient struct {
	record *promptClient
	calls  int
}

func (r *replayClient) ChatJSON(_ context.Context, system, user string, out any) error {
	r.calls++
	if r.calls > 1 || system != judge.SystemPrompt || user != r.record.user {
		return errors.New("replay request fingerprint mismatch")
	}
	if r.record.err != nil {
		return r.record.err
	}
	return json.Unmarshal(r.record.result, out)
}

type row struct {
	ID                 string         `json:"case_id"`
	Round              int            `json:"round"`
	Expected           bool           `json:"expected_accept"`
	Wire               wire           `json:"wire"`
	Raw                string         `json:"raw_judgment"`
	RawError           string         `json:"raw_error,omitempty"`
	SchemaOK           bool           `json:"schema_ok"`
	RawOK              bool           `json:"raw_semantic_ok"`
	RawUnsafe          bool           `json:"raw_unsafe_accept"`
	Product            judge.Judgment `json:"product_judgment"`
	Decision           judge.Decision `json:"product_source"`
	ProductOK          bool           `json:"product_semantic_ok"`
	ConservativeReject bool           `json:"fallback_conservative_reject"`
	ProductUnsafe      bool           `json:"product_unsafe_accept"`
	ReplayCalls        int            `json:"replay_adapter_calls"`
}

func contains(v []string, x string) bool {
	for _, s := range v {
		if s == x {
			return true
		}
	}
	return false
}

type baselineReport struct {
	DatasetSHA      string                        `json:"dataset_sha256"`
	PromptSHA       string                        `json:"prompt_sha256"`
	SourceSHA       string                        `json:"source_sha256"`
	SourceUnchanged bool                          `json:"source_unchanged"`
	Expected        int                           `json:"expected_cases"`
	Completed       int                           `json:"completed_cases"`
	Models          map[string]localmodel.Service `json:"models"`
	Cases           []row                         `json:"cases"`
}

func loadBaseline(raw []byte, datasetSHA, sourceSHA string, count int) (*baselineReport, error) {
	var b baselineReport
	if json.Unmarshal(raw, &b) != nil || b.DatasetSHA != datasetSHA || b.PromptSHA != baselineSHA || b.SourceSHA != sourceSHA || !b.SourceUnchanged || b.Expected != count || b.Completed != count || len(b.Cases) != count {
		return nil, errors.New("baseline must be complete on the identical dataset and source tree")
	}
	keys := map[string]bool{}
	for _, r := range b.Cases {
		key := fmt.Sprintf("%s/%d", r.ID, r.Round)
		if r.ID == "" || r.Round < 1 || r.Round > 2 || keys[key] {
			return nil, errors.New("invalid baseline rows")
		}
		keys[key] = true
	}
	return &b, nil
}
func regressions(prior []row, current []row) ([]string, error) {
	rows := map[string]row{}
	for _, r := range prior {
		rows[fmt.Sprintf("%s/%d", r.ID, r.Round)] = r
	}
	var failures []string
	for _, r := range current {
		key := fmt.Sprintf("%s/%d", r.ID, r.Round)
		old, ok := rows[key]
		if !ok || old.Expected != r.Expected {
			return nil, errors.New("baseline label pairing mismatch")
		}
		delete(rows, key)
		if old.RawOK && !r.RawOK {
			failures = append(failures, key+":raw")
		}
		if old.ProductOK && !r.ProductOK {
			failures = append(failures, key+":product")
		}
	}
	for key := range rows {
		failures = append(failures, key+":unattempted")
	}
	sort.Strings(failures)
	return failures, nil
}

type syncWriter interface {
	io.Writer
	Sync() error
	Close() error
}

func finishReport(w syncWriter, report any) error {
	return errors.Join(json.NewEncoder(w).Encode(report), w.Sync(), w.Close())
}
func sealSource(report map[string]any, before, after string, err error) error {
	report["source_unchanged"] = err == nil && before == after
	if err != nil || before != after {
		report["quality_gate_passed"] = false
		report["dataset_gate_passed"] = false
		return errors.New("evaluation source changed or became unreadable")
	}
	return nil
}
func evaluate(ctx context.Context, c sample, round int, client judge.ZhipuChat, prompt string, transport *recorder) (row, error) {
	transport.calls = nil
	p := &promptClient{client: client, prompt: prompt}
	raw, err := judge.NewZhipu(p).JudgeContext(ctx, c.Word, c.Meanings, c.Answer)
	if len(transport.calls) != 1 || p.calls != 1 {
		return row{}, errors.New("sample must issue exactly one actual L2 request")
	}
	r := row{ID: c.ID, Round: round, Expected: *c.Expected, Wire: transport.calls[0], Raw: raw, SchemaOK: err == nil}
	if err != nil {
		if errors.Is(err, judge.ErrInvalidJudgment) {
			r.RawError = "invalid_judgment"
		} else {
			r.RawError = "provider_or_protocol_error"
		}
	}
	r.RawOK = r.SchemaOK && contains(c.Allowed, raw)
	r.RawUnsafe = r.SchemaOK && !*c.Expected && raw != "wrong"
	replay := &replayClient{record: p}
	product := judge.New(judge.DefaultSynonyms())
	product.LLM = judge.NewZhipu(replay)
	product.OnDecision = func(_ context.Context, d judge.Decision) { r.Decision = d }
	// Replay has no network and no elapsed request deadline: retain actual provider
	// errors, but do not invent another timeout after a successfully captured call.
	r.Product = product.JudgeContext(context.Background(), c.Word, c.Meanings, c.Answer)
	r.ReplayCalls = replay.calls
	r.ProductOK = contains(c.Allowed, string(r.Product)) && (r.Decision.Source == "l1" || r.SchemaOK)
	r.ConservativeReject = r.Decision.Source == "fallback" && r.Product == judge.JudgmentWrong
	r.ProductUnsafe = !*c.Expected && r.Product != judge.JudgmentWrong
	if len(transport.calls) != 1 {
		return r, errors.New("replay issued a second request")
	}
	return r, nil
}
func sourceDigest(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") || d.Name() == "go.mod" || d.Name() == "go.sum" {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", errors.New("source tree empty")
	}
	sort.Strings(paths)
	var b bytes.Buffer
	for _, p := range paths {
		raw, e := os.ReadFile(p)
		if e != nil {
			return "", e
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s %s\n", filepath.ToSlash(rel), digest(raw))
	}
	return digest(b.Bytes()), nil
}
func run() (returnErr error) {
	base := flag.String("base", "http://127.0.0.1:8090", "fixed local platform")
	path := flag.String("cases", "", "frozen labeled JSON")
	sha := flag.String("expected-sha256", "", "frozen input SHA")
	promptPath := flag.String("prompt", "", "frozen baseline or candidate prompt")
	output := flag.String("output", "", "new exclusive report")
	baselinePath := flag.String("baseline-report", "", "complete same-source baseline; required for candidate old100")
	source := flag.String("source-root", ".", "server Go tree")
	flag.Parse()
	if os.Getenv("ENWORDS_JUDGE_EVAL") != "1" || *base != "http://127.0.0.1:8090" {
		return errors.New("explicit opt-in and fixed local platform required")
	}
	if *path == "" || *promptPath == "" || *output == "" {
		return errors.New("cases, prompt and output required")
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	data, err := frozenData(raw, *sha)
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(*promptPath)
	if err != nil {
		return err
	}
	ph := digest(prompt)
	if ph != baselineSHA && ph != candidateSHA {
		return errors.New("prompt is not a reviewed frozen version")
	}
	sourceSHA, err := sourceDigest(*source)
	if err != nil {
		return err
	}
	var baseline *baselineReport
	var baselineFileSHA string
	if *baselinePath != "" {
		if ph != candidateSHA {
			return errors.New("baseline comparison requires candidate prompt")
		}
		b, e := os.ReadFile(*baselinePath)
		if e != nil {
			return e
		}
		baseline, e = loadBaseline(b, digest(raw), sourceSHA, 2*len(data.Cases))
		if e != nil {
			return e
		}
		baselineFileSHA = digest(b)
	}
	if ph == candidateSHA && len(data.Cases) == 100 && baseline == nil {
		return errors.New("candidate old100 requires its same-source baseline report")
	}
	f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	report := map[string]any{"scope": data.Scope, "dataset_sha256": digest(raw), "prompt_sha256": ph, "source_sha256": sourceSHA, "repeats": 2, "expected_cases": 2 * len(data.Cases), "completed_cases": 0, "unattempted_cases": 2 * len(data.Cases), "forced_l2_diagnostic": true, "second_network_request": false, "started_at": time.Now().Format(time.RFC3339)}
	defer func() {
		after, e := sourceDigest(*source)
		returnErr = errors.Join(returnErr, sealSource(report, sourceSHA, after, e), finishReport(f, report))
	}()
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
		report["fatal_error"] = "discovery_failed"
		return err
	}
	report["models"] = client.Services
	if baseline != nil && baseline.Models["vl"].Model != client.Services["vl"].Model {
		return errors.New("baseline model identity mismatch")
	}
	rows := []row{}
	rawPass, productPass, schemaPass, unsafeRaw, unsafeProduct, unavailable := 0, 0, 0, 0, 0, 0
outer:
	for repeat := 1; repeat <= 2; repeat++ {
		for _, c := range data.Cases {
			r, e := evaluate(context.Background(), c, repeat, client, string(prompt), transport)
			if e != nil {
				return e
			}
			rows = append(rows, r)
			report["cases"] = rows
			report["completed_cases"] = len(rows)
			report["unattempted_cases"] = 2*len(data.Cases) - len(rows)
			if e = json.NewEncoder(journal).Encode(r); e != nil {
				return e
			}
			if e = journal.Sync(); e != nil {
				return e
			}
			if r.RawOK {
				rawPass++
			}
			if r.ProductOK {
				productPass++
			}
			if r.SchemaOK {
				schemaPass++
			}
			if r.RawUnsafe {
				unsafeRaw++
			}
			if r.ProductUnsafe {
				unsafeProduct++
			}
			if r.Wire.Error == "transport_or_timeout" || r.Wire.Error == "response_read_failed" {
				unavailable++
			} else {
				unavailable = 0
			}
			if unavailable >= 5 {
				report["stopped_reason"] = "five_consecutive_transport_failures"
				break outer
			}
		}
	}
	total := 2 * len(data.Cases)
	report["raw_passed"] = rawPass
	report["product_passed"] = productPass
	report["schema_passed"] = schemaPass
	report["raw_unsafe_accepts"] = unsafeRaw
	report["product_unsafe_accepts"] = unsafeProduct
	report["raw_accuracy"] = float64(rawPass) / float64(total)
	report["product_accuracy"] = float64(productPass) / float64(total)
	report["quality_gate_passed"] = len(rows) == total && schemaPass == total && float64(rawPass)/float64(total) >= .95 && float64(productPass)/float64(total) >= .95 && unsafeRaw == 0 && unsafeProduct == 0
	report["dataset_gate_passed"] = report["quality_gate_passed"]
	report["final_adoption_gate"] = "not_decided_by_single_dataset_report"
	report["old100_nonregression"] = "requires_separate_baseline_comparison"
	if baseline != nil {
		regressed, e := regressions(baseline.Cases, rows)
		if e != nil {
			return e
		}
		report["regressions"] = regressed
		report["nonregression_passed"] = len(regressed) == 0
		report["baseline_report_sha256"] = baselineFileSHA
		if len(data.Cases) == 100 {
			report["old100_nonregression"] = len(regressed) == 0
			report["quality_gate_passed"] = report["quality_gate_passed"] == true && len(regressed) == 0
			report["dataset_gate_passed"] = report["quality_gate_passed"]
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/feedback"
	"enwords/internal/localmodel"
	"enwords/internal/obs"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

type diagnosticTransport struct {
	body     []byte
	status   int
	response string
}

func (d *diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/api/vl/chat" {
		return http.DefaultTransport.RoundTrip(r)
	}
	d.body, _ = io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(d.body))
	response, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		d.status = response.StatusCode
		d.response = string(raw)
		response.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return response, err
}
func TestOptInProductionFB001Diagnostic(t *testing.T) {
	if os.Getenv("ENWORDS_LOCAL_DIAGNOSTIC") != "1" {
		t.Skip("explicit local diagnostic only")
	}
	raw, err := os.ReadFile("../../../review/feedback-local-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var data dataset
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	c, err := localmodel.New("http://127.0.0.1:8090", obs.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	capture := &diagnosticTransport{}
	c.HTTP.Transport = capture
	anchor, _ := time.Parse(time.RFC3339, data.Cases[0].Anchor)
	start := time.Now()
	_, err = feedback.NewZhipuParser(c).ParseIntents(context.Background(), data.Cases[0].Text, anchor)
	t.Logf("production_8s status=%d latency_ms=%d request_sha=%s error=%v response=%s", capture.status, time.Since(start).Milliseconds(), hash(capture.body), err, capture.response)
	if err != nil && capture.status == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:8090/api/vl/chat", bytes.NewReader(capture.body))
		req.Header.Set("Content-Type", "application/json")
		start = time.Now()
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Logf("diagnostic_only_30s latency_ms=%d error=%v", time.Since(start).Milliseconds(), e)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		t.Logf("diagnostic_only_30s status=%d latency_ms=%d response=%s", resp.StatusCode, time.Since(start).Milliseconds(), body)
	}
}

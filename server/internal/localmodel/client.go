// Package localmodel accesses only the fixed, same-origin Model Console proxies.
package localmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"enwords/internal/obs"
)

const MaxAudio = 8 << 20

// TranscribeTimeout bounds one classroom turn.
const TranscribeTimeout = 15 * time.Second

type Service struct {
	ID           string   `json:"id"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
}

type Client struct {
	base      string
	HTTP      *http.Client
	Log       *obs.Logger
	Services  map[string]Service
	statusMu  sync.Mutex
	statusAt  time.Time
	status    map[string]bool
	profileMu sync.RWMutex
	profile   Profile
	// The local ASR model is single-GPU and can return empty/partial results
	// when several multipart requests are decoded at once. Keep a cancellable
	// one-at-a-time gate; callers still retain their request deadline.
	asrGate chan struct{}
	ttsGate chan struct{}
}

// Profile assigns a model-console service to each product capability. The
// service IDs are deliberately data, so an administrator can switch models
// without rebuilding the server.
type Profile struct {
	Name  string `json:"name"`
	Text  string `json:"text_service"`
	ASR   string `json:"asr_service"`
	TTSZh string `json:"tts_zh_service"`
	TTSEn string `json:"tts_en_service"`
}

func New(base string, log *obs.Logger) (*Client, error) {
	if base == "" {
		base = "http://127.0.0.1:8090"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("model console: invalid origin")
	}
	if log == nil {
		log = obs.Nop()
	}
	return &Client{base: strings.TrimRight(base, "/"), Log: log, Services: map[string]Service{}, profile: Profile{Name: "default", Text: "vl", ASR: "asr", TTSZh: "tts", TTSEn: "tts-en"}, HTTP: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, asrGate: make(chan struct{}, 1), ttsGate: make(chan struct{}, 1)}, nil
}

func (c *Client) acquireASR(ctx context.Context) error {
	select {
	case c.asrGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) releaseASR() { <-c.asrGate }

func (c *Client) acquireTTS(ctx context.Context) error {
	select {
	case c.ttsGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) releaseTTS() { <-c.ttsGate }

func (c *Client) request(ctx context.Context, path, contentType string, body io.Reader, limit int64) ([]byte, error) {
	start := time.Now()
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("model console: invalid request")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Trace-ID", obs.TraceID(ctx))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.Log.ErrorCtx(ctx, "provider", "model_request_failed", map[string]any{"endpoint": path, "reason": "transport_or_timeout", "duration_ms": time.Since(start).Milliseconds()})
		return nil, fmt.Errorf("model console: transport or timeout")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Log.ErrorCtx(ctx, "provider", "model_request_failed", map[string]any{"endpoint": path, "status": resp.StatusCode, "duration_ms": time.Since(start).Milliseconds()})
		return nil, fmt.Errorf("model console: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		c.Log.ErrorCtx(ctx, "provider", "model_request_failed", map[string]any{"endpoint": path, "reason": "invalid_or_oversized_response", "duration_ms": time.Since(start).Milliseconds()})
		return nil, fmt.Errorf("model console: invalid or oversized response")
	}
	c.Log.InfoCtx(ctx, "provider", "model_http_completed", map[string]any{"endpoint": path, "bytes": len(raw), "duration_ms": time.Since(start).Milliseconds()})
	return raw, nil
}

func (c *Client) Discover(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := c.request(ctx, "/api/models", "", nil, 1<<20)
	if err != nil {
		return err
	}
	var data struct {
		Services []Service `json:"services"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return fmt.Errorf("model console: invalid discovery JSON")
	}
	for _, s := range data.Services {
		if strings.TrimSpace(s.ID) != "" {
			c.Services[s.ID] = s
		}
	}
	if !c.Has("vl", "text") && !c.Has("asr", "asr") && !c.Has("tts", "tts") && !c.Has("tts-en", "tts") {
		return fmt.Errorf("model console: no supported capability")
	}
	c.Log.InfoCtx(ctx, "provider", "provider_discovered", map[string]any{"text": c.Has("vl", "text"), "asr": c.Has("asr", "asr"), "tts": c.Has("tts", "tts"), "tts_en": c.Has("tts-en", "tts")})
	return nil
}

func (c *Client) SetProfile(p Profile) error {
	if p.Text == "" {
		p.Text = "vl"
	}
	if p.ASR == "" {
		p.ASR = "asr"
	}
	if p.TTSZh == "" {
		p.TTSZh = "tts"
	}
	if p.TTSEn == "" {
		p.TTSEn = "tts-en"
	}
	for id, cap := range map[string]string{p.Text: "text", p.ASR: "asr", p.TTSZh: "tts", p.TTSEn: "tts"} {
		if !c.Has(id, cap) {
			return fmt.Errorf("model console: service %q lacks %s capability", id, cap)
		}
	}
	c.profileMu.Lock()
	c.profile = p
	c.profileMu.Unlock()
	return nil
}

func (c *Client) Profile() Profile {
	c.profileMu.RLock()
	defer c.profileMu.RUnlock()
	return c.profile
}
func (c *Client) serviceID(role string) string {
	p := c.Profile()
	if p.Text == "" {
		p = Profile{Name: "default", Text: "vl", ASR: "asr", TTSZh: "tts", TTSEn: "tts-en"}
	}
	switch role {
	case "text":
		return p.Text
	case "asr":
		return p.ASR
	case "tts_zh":
		return p.TTSZh
	case "tts_en":
		return p.TTSEn
	}
	return ""
}

// Ready returns a short-lived, fail-closed snapshot of the manager's live
// capabilities. The mutex coalesces concurrent refreshes from multiple users.
func (c *Client) Ready(ctx context.Context) map[string]bool {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if time.Now().Before(c.statusAt) {
		return map[string]bool{"vl": c.status["vl"], "asr": c.status["asr"], "tts": c.status["tts"], "tts-en": c.status["tts-en"]}
	}
	ready := map[string]bool{"vl": false, "asr": false, "tts": false, "tts-en": false}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := c.request(queryCtx, "/api/status", "", nil, 1<<20)
	statusError := err != nil
	if err == nil {
		var data struct {
			Services []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"services"`
		}
		if json.Unmarshal(raw, &data) == nil {
			for _, service := range data.Services {
				if _, known := ready[service.ID]; known {
					capability := map[string]string{"vl": "text", "asr": "asr", "tts": "tts", "tts-en": "tts"}[service.ID]
					ready[service.ID] = service.Status == "ready" && c.Has(service.ID, capability)
				}
			}
		} else {
			statusError = true
		}
	}
	if ctx.Err() != nil {
		return ready // A canceled caller must not poison another student's cache.
	}
	c.status = ready
	c.statusAt = time.Now().Add(2 * time.Second)
	c.Log.InfoCtx(ctx, "provider", "model_status_observed", map[string]any{"text": ready["vl"], "asr": ready["asr"], "tts": ready["tts"], "tts_en": ready["tts-en"], "status_error": statusError})
	return map[string]bool{"vl": ready["vl"], "asr": ready["asr"], "tts": ready["tts"], "tts-en": ready["tts-en"]}
}

func (c *Client) Has(id, capability string) bool {
	s := c.Services[id]
	if s.Model == "" {
		return false
	}
	for _, v := range s.Capabilities {
		if v == capability {
			return true
		}
	}
	return false
}

func (c *Client) ChatJSON(ctx context.Context, system, user string, out any) (err error) {
	ctx = obs.WithChild(ctx, "model")
	start := time.Now()
	defer func() {
		if err != nil {
			c.Log.ErrorCtx(ctx, "provider", "model_request_failed", map[string]any{"task": "text", "model": c.Services[c.serviceID("text")].Model, "reason": err.Error(), "duration_ms": time.Since(start).Milliseconds()})
		} else {
			c.Log.InfoCtx(ctx, "provider", "model_request_completed", map[string]any{"task": "text", "model": c.Services[c.serviceID("text")].Model, "duration_ms": time.Since(start).Milliseconds()})
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	id := c.serviceID("text")
	body, _ := json.Marshal(map[string]any{"model": c.Services[id].Model, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}, "temperature": 0, "max_tokens": 512, "stream": false, "response_format": map[string]string{"type": "json_object"}})
	raw, err := c.request(ctx, "/api/vl/chat", "application/json", bytes.NewReader(body), 1<<20)
	if err != nil {
		c.Log.InfoCtx(ctx, "provider", "provider_fallback", map[string]any{"task": "text", "reason": "request_failed"})
		return err
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &data) != nil || len(data.Choices) == 0 {
		return fmt.Errorf("model console: invalid completion")
	}
	if err = json.Unmarshal([]byte(data.Choices[0].Message.Content), out); err != nil {
		c.Log.InfoCtx(ctx, "provider", "provider_fallback", map[string]any{"task": "text", "reason": "invalid_json"})
		return fmt.Errorf("model console: invalid structured output")
	}
	return nil
}

func (c *Client) Transcribe(ctx context.Context, audio []byte, filename, language string) (text string, err error) {
	ctx = obs.WithChild(ctx, "asr")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	defer func() { c.mediaResult(ctx, "asr", language, len(audio), start, err) }()
	id := c.serviceID("asr")
	if !c.Has(id, "asr") || len(audio) == 0 || len(audio) > MaxAudio {
		return "", fmt.Errorf("model console: audio unavailable or invalid")
	}
	if err := c.acquireASR(ctx); err != nil {
		return "", fmt.Errorf("model console: ASR queue canceled")
	}
	defer c.releaseASR()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	f, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err = f.Write(audio); err != nil {
		return "", err
	}
	_ = w.WriteField("model", c.Services[id].Model)
	_ = w.WriteField("language", language)
	_ = w.Close()
	raw, err := c.request(ctx, "/api/asr/transcriptions", w.FormDataContentType(), &body, 1<<20)
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &out) != nil || strings.TrimSpace(out.Text) == "" {
		return "", fmt.Errorf("model console: empty transcript")
	}
	if i := strings.Index(out.Text, "<asr_text>"); i >= 0 {
		out.Text = out.Text[i+len("<asr_text>"):]
	}
	out.Text = strings.TrimSpace(strings.TrimSuffix(out.Text, "<|endoftext|>"))
	if out.Text == "" {
		return "", fmt.Errorf("model console: empty transcript")
	}
	return strings.TrimSpace(out.Text), nil
}

func (c *Client) Synthesize(ctx context.Context, text, lang string) (audio []byte, err error) {
	ctx = obs.WithChild(ctx, "tts")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	defer func() { c.mediaResult(ctx, "tts", lang, len(audio), start, err) }()
	id, path := c.serviceID("tts_zh"), "/api/tts/speech"
	if lang == "en" {
		id, path = c.serviceID("tts_en"), "/api/tts-en/speech"
	}
	if (lang != "en" && lang != "zh") || !c.Has(id, "tts") || len([]rune(text)) > 300 || strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("model console: unsupported speech request")
	}
	if err := c.acquireTTS(ctx); err != nil {
		return nil, fmt.Errorf("model console: TTS queue canceled")
	}
	defer c.releaseTTS()
	body, _ := json.Marshal(map[string]any{"model": c.Services[id].Model, "input": text, "voice": "default", "response_format": "wav"})
	raw, err := c.request(ctx, path, "application/json", bytes.NewReader(body), MaxAudio)
	if err != nil {
		return nil, err
	}
	if err := validateWAV(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) mediaResult(ctx context.Context, task, lang string, bytes int, start time.Time, err error) {
	modelID := task
	if task == "tts" {
		if lang == "en" {
			modelID = c.serviceID("tts_en")
		} else {
			modelID = c.serviceID("tts_zh")
		}
	}
	fields := map[string]any{"task": task, "model": c.Services[modelID].Model, "language": lang, "bytes": bytes, "duration_ms": time.Since(start).Milliseconds()}
	if err != nil {
		fields["reason"] = "invalid_media_or_provider_failure"
		if ctx.Err() != nil {
			fields["reason"] = "canceled_or_timeout"
		}
		c.Log.ErrorCtx(ctx, "provider", "model_request_failed", fields)
	} else {
		c.Log.InfoCtx(ctx, "provider", "model_request_completed", fields)
	}
}

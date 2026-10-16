// Package remoteprovider implements the verified subset of OpenAI-compatible
// HTTP. Chat/Models support never implies realtime audio or duplex capability.
package remoteprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"enwords/internal/obs"
)

const MaxResponse = 1 << 20

type Config struct {
	BaseURL string
	Model   string
	APIKey  string
	// Only a literal loopback IP is accepted; the caller must own and verify
	// the entire SSH tunnel, including any upstream hop.
	AllowLoopbackHTTP bool
}

type Client struct {
	base, model, key string
	http             *http.Client
	log              *obs.Logger
}

type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string  { return "remote provider: " + e.Code }
func failure(code string) error { return &Error{Code: code} }

func New(cfg Config, log *obs.Logger) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(cfg.BaseURL, "\r\n\t ") {
		return nil, failure("invalid_origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !cfg.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return nil, failure("protected_transport_required")
		}
	}
	if u.Path != "/v1" && u.Path != "/v1/" {
		return nil, failure("invalid_api_path")
	}
	if !identifier(cfg.Model) {
		return nil, failure("invalid_model")
	}
	if len(cfg.APIKey) < 1 || len(cfg.APIKey) > 4096 || strings.IndexFunc(cfg.APIKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, failure("invalid_key")
	}
	if log == nil {
		log = obs.Nop()
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{base: strings.TrimRight(cfg.BaseURL, "/"), model: cfg.Model, key: cfg.APIKey, log: log, http: &http.Client{
		Timeout: 8 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func identifier(s string) bool {
	return s != "" && len(s) <= 256 && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, path string, body []byte) ([]byte, error) {
	started := time.Now()
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, failure("invalid_request")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Trace-ID", obs.TraceID(ctx))
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.ErrorCtx(ctx, "remote_provider", "request_failed", map[string]any{"endpoint": path, "reason": "transport_or_timeout", "duration_ms": time.Since(started).Milliseconds()})
		return nil, failure("transport_or_timeout")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.log.ErrorCtx(ctx, "remote_provider", "request_failed", map[string]any{"endpoint": path, "status": resp.StatusCode, "reason": "http_status"})
		return nil, &Error{Code: "http_status", Status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil || len(raw) > MaxResponse {
		return nil, failure("invalid_or_oversized_response")
	}
	if ctx.Err() != nil {
		return nil, failure("canceled")
	}
	c.log.InfoCtx(ctx, "remote_provider", "response_received", map[string]any{"endpoint": path, "bytes": len(raw), "duration_ms": time.Since(started).Milliseconds()})
	return raw, nil
}

func (c *Client) Models(ctx context.Context) (models []string, resultErr error) {
	defer func() { c.logValidation(ctx, "/models", resultErr) }()
	raw, err := c.request(ctx, "/models", nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = uniqueJSON(raw); err != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Data == nil || len(envelope.Data) > 1024 {
		return nil, failure("invalid_models")
	}
	result := make([]string, 0, len(envelope.Data))
	seen := map[string]bool{}
	for _, m := range envelope.Data {
		if !identifier(m.ID) || seen[m.ID] {
			return nil, failure("invalid_models")
		}
		seen[m.ID] = true
		result = append(result, m.ID)
	}
	return result, nil
}

func (c *Client) completion(ctx context.Context, system, user string, structured bool) (string, error) {
	if strings.TrimSpace(user) == "" || len(user) > 32768 || len(system) > 8192 {
		return "", failure("invalid_prompt")
	}
	payload := map[string]any{"model": c.model, "stream": false, "temperature": 0, "max_tokens": 1024,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}}
	if structured {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", failure("invalid_request")
	}
	raw, err := c.request(ctx, "/chat/completions", body)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string            `json:"role"`
				Content   string            `json:"content"`
				Refusal   *string           `json:"refusal"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if uniqueJSON(raw) != nil || json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) != 1 {
		return "", failure("invalid_completion")
	}
	x := envelope.Choices[0]
	if x.FinishReason != "stop" || x.Message.Role != "assistant" || x.Message.Refusal != nil || len(x.Message.ToolCalls) != 0 || strings.TrimSpace(x.Message.Content) == "" || len(x.Message.Content) > 65536 {
		return "", failure("incomplete_or_unsupported_completion")
	}
	return x.Message.Content, nil
}

func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	text, err := c.completion(ctx, system, user, false)
	c.logValidation(ctx, "/chat/completions", err)
	return text, err
}

func (c *Client) ChatJSON(ctx context.Context, system, user string, target any) (resultErr error) {
	defer func() { c.logValidation(ctx, "/chat/completions", resultErr) }()
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return failure("invalid_structured_target")
	}
	content, err := c.completion(ctx, system, user, true)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 || trimmed[0] != '{' || validateJSON([]byte(trimmed), true) != nil {
		return failure("invalid_structured_response")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	candidate := reflect.New(value.Elem().Type())
	if decoder.Decode(candidate.Interface()) != nil {
		return failure("invalid_structured_response")
	}
	value.Elem().Set(candidate.Elem())
	return nil
}

func (c *Client) logValidation(ctx context.Context, endpoint string, err error) {
	if e, ok := err.(*Error); ok {
		switch e.Code {
		case "invalid_models", "invalid_completion", "incomplete_or_unsupported_completion", "invalid_structured_response":
			c.log.ErrorCtx(ctx, "remote_provider", "validation_failed", map[string]any{"endpoint": endpoint, "reason": e.Code})
		}
	}
}

// JSON objects with duplicate keys are ambiguous at a decision boundary. Reject
// them at every depth instead of accepting encoding/json's last-value behavior.
func uniqueJSON(raw []byte) error {
	return validateJSON(raw, false)
}

// Structured decision payloads cannot use null as a scalar zero value. Nullable
// provider envelope metadata remains allowed outside the decision payload.
func validateJSON(raw []byte, rejectNull bool) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("depth")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if t == nil && rejectNull {
			return fmt.Errorf("null")
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				key = strings.ToLower(key)
				if !ok || keys[key] {
					return fmt.Errorf("duplicate")
				}
				keys[key] = true
				if err = visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing")
	}
	return nil
}

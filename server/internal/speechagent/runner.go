package speechagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enwords/internal/localmodel"
)

type RunnerConfig struct {
	ServerOrigin    string
	ConsoleOrigin   string
	SpoolDir        string
	CAFile          string
	EnrollmentToken string
	AgentToken      string
	DisplayName     string
}

type runnerCredential struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}
type runnerJournal struct {
	RequestID            string    `json:"request_id"`
	Claim                Claim     `json:"claim"`
	ReservationID        string    `json:"reservation_id,omitempty"`
	ReservationExpiresAt time.Time `json:"reservation_expires_at,omitempty"`
}
type runnerEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}
type runnerHTTPError struct{ Status int }

func (e runnerHTTPError) Error() string { return fmt.Sprintf("speech agent HTTP %d", e.Status) }

type runnerReservation struct {
	ReservationID string    `json:"reservation_id"`
	MaxBytes      int64     `json:"max_bytes"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type Runner struct {
	cfg          RunnerConfig
	http         *http.Client
	console      *localmodel.Client
	credential   runnerCredential
	capabilities []Capability
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.SpoolDir == "" {
		return nil, errors.New("speech agent spool directory required")
	}
	if err := validateHTTPSOrigin(cfg.ServerOrigin); err != nil {
		return nil, err
	}
	consoleURL, err := url.Parse(cfg.ConsoleOrigin)
	if err != nil || consoleURL.Host == "" || consoleURL.User != nil || consoleURL.RawQuery != "" || consoleURL.Fragment != "" || (consoleURL.Scheme != "http" && consoleURL.Scheme != "https") || (consoleURL.Path != "" && consoleURL.Path != "/") {
		return nil, errors.New("invalid model console origin")
	}
	host := consoleURL.Hostname()
	ip := net.ParseIP(host)
	if !(ip != nil && ip.IsLoopback()) && !strings.EqualFold(host, "localhost") {
		return nil, errors.New("model console must be loopback")
	}
	console, err := localmodel.New(strings.TrimRight(cfg.ConsoleOrigin, "/"), nil)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pemBytes, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, errors.New("speech agent CA bundle unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return nil, errors.New("speech agent CA bundle invalid")
		}
		tlsConfig.RootCAs = roots
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &Runner{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, console: console}, nil
}

func validateHTTPSOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("speech agent server must be an HTTPS origin")
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.cfg.SpoolDir, 0o700); err != nil {
		return fmt.Errorf("speech agent spool unavailable")
	}
	if err := secureSpeechAgentPath(r.cfg.SpoolDir, true); err != nil {
		return errors.New("speech agent spool permissions could not be secured")
	}
	if err := r.loadOrEnroll(ctx); err != nil {
		return err
	}
	if err := r.console.Discover(ctx); err != nil {
		return errors.New("local model console unavailable")
	}
	if err := r.loadCapabilities(); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan error, 1)
	go func() {
		err := r.heartbeatLoop(runCtx)
		heartbeatDone <- err
		if err != nil {
			cancel()
		}
	}()
	for runCtx.Err() == nil {
		if err := r.processJournal(runCtx); err != nil {
			if runCtx.Err() != nil {
				break
			}
			slog.Warn("speech-agent task cycle failed", "event", "speech_agent_cycle_retry", "category", safeRunnerErrorCategory(err))
			select {
			case <-runCtx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
	}
	cancel()
	select {
	case err := <-heartbeatDone:
		if err != nil {
			return err
		}
	default:
		<-heartbeatDone
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("speech agent stopped")
}

func (r *Runner) credentialPath() string {
	return filepath.Join(r.cfg.SpoolDir, "agent-credential.json")
}
func (r *Runner) journalPath() string { return filepath.Join(r.cfg.SpoolDir, "active-claim.json") }
func (r *Runner) pendingPath() string { return filepath.Join(r.cfg.SpoolDir, "pending-request.json") }
func (r *Runner) audioPath() string   { return filepath.Join(r.cfg.SpoolDir, "active-result.wav") }

func (r *Runner) loadOrEnroll(ctx context.Context) error {
	if r.cfg.AgentToken != "" {
		r.credential.AgentToken = r.cfg.AgentToken
	}
	if r.credential.AgentToken == "" {
		if raw, err := os.ReadFile(r.credentialPath()); err == nil {
			_ = json.Unmarshal(raw, &r.credential)
		}
	}
	if r.credential.AgentToken != "" {
		return nil
	}
	if r.cfg.EnrollmentToken == "" {
		return errors.New("speech agent enrollment token required")
	}
	var result struct {
		AgentID      string       `json:"agent_id"`
		AgentToken   string       `json:"agent_token"`
		Capabilities []Capability `json:"capabilities"`
	}
	body, _ := json.Marshal(map[string]string{"enrollment_token": r.cfg.EnrollmentToken, "display_name": r.cfg.DisplayName})
	if err := r.call(ctx, http.MethodPost, "/api/v1/speech-agent/enroll", "", body, &result); err != nil {
		return errors.New("speech agent enrollment rejected")
	}
	if result.AgentID == "" || result.AgentToken == "" {
		return errors.New("invalid enrollment response")
	}
	r.credential = runnerCredential{AgentID: result.AgentID, AgentToken: result.AgentToken}
	r.capabilities = result.Capabilities
	if err := r.writeCapabilities(result.Capabilities); err != nil {
		return errors.New("speech agent capability storage failed")
	}
	if err := r.writeAtomic(r.credentialPath(), r.credential); err != nil {
		return errors.New("speech agent credential storage failed")
	}
	return nil
}

func (r *Runner) loadCapabilities() error {
	if len(r.capabilities) > 0 {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(r.cfg.SpoolDir, "approved-capabilities.json"))
	if err != nil {
		return errors.New("approved speech capabilities unavailable; reenroll the agent")
	}
	var caps []Capability
	if json.Unmarshal(raw, &caps) != nil {
		return errors.New("invalid approved speech capabilities")
	}
	canonical, err := NormalizeCapabilities(caps)
	if err != nil {
		return errors.New("invalid approved speech capabilities")
	}
	r.capabilities = canonical
	return nil
}

func (r *Runner) writeCapabilities(caps []Capability) error {
	return r.writeAtomic(filepath.Join(r.cfg.SpoolDir, "approved-capabilities.json"), caps)
}

func (r *Runner) writeAtomic(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.cfg.SpoolDir, ".spool-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = secureSpeechAgentPath(tmp.Name(), false)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if dir, err := os.Open(r.cfg.SpoolDir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (r *Runner) heartbeatLoop(ctx context.Context) error {
	for {
		if err := r.call(ctx, http.MethodPost, "/api/v1/speech-agent/heartbeat", r.credential.AgentToken, []byte(`{}`), nil); err != nil {
			var status runnerHTTPError
			if errors.As(err, &status) && (status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden) {
				return errors.New("speech agent credential revoked or disabled")
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(15 * time.Second):
		}
	}
}

func (r *Runner) processJournal(ctx context.Context) error {
	var j runnerJournal
	if raw, err := os.ReadFile(r.journalPath()); err == nil {
		if json.Unmarshal(raw, &j) != nil || j.Claim.JobID <= 0 || j.Claim.AttemptToken == "" {
			return errors.New("invalid active speech journal")
		}
		if pendingRaw, pendingErr := os.ReadFile(r.pendingPath()); pendingErr == nil {
			var pending struct {
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(pendingRaw, &pending) == nil && pending.RequestID == j.RequestID {
				_ = os.Remove(r.pendingPath())
			}
		}
		return r.processClaim(ctx, j)
	} else if !os.IsNotExist(err) {
		return err
	}
	requestID := ""
	if raw, err := os.ReadFile(r.pendingPath()); err == nil {
		var pending struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(raw, &pending) == nil {
			requestID = pending.RequestID
		}
	}
	if requestID == "" {
		token, err := NewToken()
		if err != nil {
			return err
		}
		requestID = token
		if err := r.writeAtomic(r.pendingPath(), map[string]string{"request_id": requestID}); err != nil {
			return err
		}
	}
	var claim Claim
	err := r.call(ctx, http.MethodPost, "/api/v1/speech-agent/next", r.credential.AgentToken, mustJSON(map[string]string{"request_id": requestID}), &claim)
	if err != nil {
		var statusErr runnerHTTPError
		if errors.As(err, &statusErr) && statusErr.Status == http.StatusConflict {
			// The server intentionally expires request IDs after their claim
			// lease. Retire that durable ID and poll with a fresh one.
			_ = os.Remove(r.pendingPath())
			return nil
		}
		return err
	}
	if claim.JobID == 0 {
		_ = os.Remove(r.pendingPath())
		time.Sleep(1500 * time.Millisecond)
		return nil
	}
	j = runnerJournal{RequestID: requestID, Claim: claim}
	if err := r.writeAtomic(r.journalPath(), j); err != nil {
		return err
	}
	_ = os.Remove(r.pendingPath())
	return r.processClaim(ctx, j)
}

func (r *Runner) processClaim(ctx context.Context, j runnerJournal) error {
	c := j.Claim
	slog.Info("speech-agent processing task", "event", "speech_agent_task_started", "job_id", c.JobID, "manifest_revision", c.ManifestRevision, "language", c.Language)
	if !r.hasCapability(c) || !r.console.Has(c.ServiceID, "tts") || r.console.Services[c.ServiceID].Model != c.Model {
		return errors.New("claimed capability is not available locally")
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var initialLease map[string]time.Time
	if err := r.call(jobCtx, http.MethodPost, fmt.Sprintf("/api/v1/speech-agent/jobs/%d/lease", c.JobID), r.credential.AgentToken, mustJSON(map[string]string{"attempt_token": c.AttemptToken}), &initialLease); err != nil {
		var status runnerHTTPError
		if errors.As(err, &status) && status.Status == http.StatusConflict {
			_ = os.Remove(r.audioPath())
			_ = os.Remove(r.journalPath())
			return nil
		}
		return err
	}
	leaseDone := make(chan struct{})
	go func() {
		defer close(leaseDone)
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-tick.C:
				var ack map[string]time.Time
				if err := r.call(jobCtx, http.MethodPost, fmt.Sprintf("/api/v1/speech-agent/jobs/%d/lease", c.JobID), r.credential.AgentToken, mustJSON(map[string]string{"attempt_token": c.AttemptToken}), &ack); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-leaseDone }()
	if _, err := os.Stat(r.audioPath()); os.IsNotExist(err) {
		audio, err := r.console.SynthesizeFor(jobCtx, c.ServiceID, c.Model, c.Voice, c.SpeechText, c.Language)
		if err != nil {
			return errors.New("local speech synthesis failed")
		}
		tmp, err := os.CreateTemp(r.cfg.SpoolDir, ".audio-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if err = tmp.Chmod(0o600); err == nil {
			err = secureSpeechAgentPath(tmp.Name(), false)
		}
		if err == nil {
			_, err = tmp.Write(audio)
		}
		if err == nil {
			err = tmp.Sync()
		}
		if ce := tmp.Close(); err == nil {
			err = ce
		}
		if err != nil {
			return errors.New("speech spool write failed")
		}
		if err = os.Rename(tmpName, r.audioPath()); err != nil {
			return errors.New("speech spool publish failed")
		}
		if d, e := os.Open(r.cfg.SpoolDir); e == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	audio, err := os.ReadFile(r.audioPath())
	if err != nil {
		return err
	}
	if len(audio) > 10<<20 {
		return errors.New("spooled speech result exceeds limit")
	}
	if j.ReservationID == "" || !time.Now().Before(j.ReservationExpiresAt.Add(-30*time.Second)) {
		var reservation runnerReservation
		if err := r.call(jobCtx, http.MethodPost, fmt.Sprintf("/api/v1/speech-agent/jobs/%d/reservations", c.JobID), r.credential.AgentToken, mustJSON(map[string]string{"attempt_token": c.AttemptToken}), &reservation); err != nil {
			var status runnerHTTPError
			if errors.As(err, &status) && status.Status == http.StatusConflict {
				// A conflict here means this claim is no longer current (the job
				// completed, its lease expired, or a newer manifest fenced it).
				// Retire durable local work so the next cycle can claim fresh work.
				_ = os.Remove(r.audioPath())
				_ = os.Remove(r.journalPath())
				return nil
			}
			return err
		}
		if reservation.ReservationID == "" || len(audio) > int(reservation.MaxBytes) {
			return errors.New("invalid upload reservation response")
		}
		j.ReservationID, j.ReservationExpiresAt = reservation.ReservationID, reservation.ExpiresAt
		if err := r.writeAtomic(r.journalPath(), j); err != nil {
			return errors.New("speech upload journal unavailable")
		}
	}
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPut, r.cfg.ServerOrigin+fmt.Sprintf("/api/v1/speech-agent/jobs/%d/result", c.JobID), bytes.NewReader(audio))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.credential.AgentToken)
	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("X-Speech-Upload-Reservation", j.ReservationID)
	req.Header.Set("X-Speech-Attempt-Token", c.AttemptToken)
	resp, err := r.http.Do(req)
	if err != nil {
		return errors.New("speech result upload unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.New("speech result response unavailable")
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusConflict {
			j.ReservationID, j.ReservationExpiresAt = "", time.Time{}
			_ = r.writeAtomic(r.journalPath(), j)
		}
		return fmt.Errorf("speech result not accepted (HTTP %d)", resp.StatusCode)
	}
	var env runnerEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Code != 0 {
		return errors.New("speech result receipt invalid")
	}
	_ = os.Remove(r.audioPath())
	_ = os.Remove(r.journalPath())
	slog.Info("speech-agent task accepted", "event", "speech_agent_task_accepted", "job_id", c.JobID, "trace_id", resp.Header.Get("X-Trace-ID"), "audio_bytes", len(audio))
	return nil
}

func safeRunnerErrorCategory(err error) string {
	if err == nil {
		return "none"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout") || strings.Contains(message, "unavailable"):
		return "unavailable_or_timeout"
	case strings.Contains(message, "rejected") || strings.Contains(message, "expired"):
		return "server_rejected"
	case strings.Contains(message, "invalid") || strings.Contains(message, "unsupported"):
		return "invalid_task_or_media"
	default:
		return "retryable_task_error"
	}
}

func (r *Runner) hasCapability(c Claim) bool {
	for _, cap := range r.capabilities {
		if cap.ServiceID == c.ServiceID && cap.Model == c.Model && cap.Language == c.Language && cap.Voice == c.Voice {
			return true
		}
	}
	return false
}

func (r *Runner) call(ctx context.Context, method, path, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, r.cfg.ServerOrigin+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		slog.Debug("speech-agent request completed", "event", "speech_agent_http_completed", "route", path, "status", resp.StatusCode, "trace_id", resp.Header.Get("X-Trace-ID"))
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("speech-agent request rejected", "event", "speech_agent_http_rejected", "route", path, "status", resp.StatusCode, "trace_id", resp.Header.Get("X-Trace-ID"))
		return runnerHTTPError{Status: resp.StatusCode}
	}
	slog.Debug("speech-agent request completed", "event", "speech_agent_http_completed", "route", path, "status", resp.StatusCode, "trace_id", resp.Header.Get("X-Trace-ID"))
	var env runnerEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Code != 0 {
		return errors.New("speech agent API rejected request")
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Package duplex contains opt-in protocol components; it does not run a Room.
package duplex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrProtocol = errors.New("duplex: invalid protocol")
	ErrStale    = errors.New("duplex: stale identity or revision")
	ErrState    = errors.New("duplex: invalid state")
	ErrConflict = errors.New("duplex: confirmation required")
	ErrTimeout  = errors.New("duplex: deadline exceeded")
)

type Identity struct {
	Session       string `json:"session_id"`
	Epoch         string `json:"epoch"`
	InputRevision uint64 `json:"input_revision"`
	Utterance     string `json:"utterance_id"`
}

func validID(s string) bool {
	return len(s) > 0 && len(s) <= 128 && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
func (i Identity) Valid() bool {
	return validID(i.Session) && validID(i.Epoch) && validID(i.Utterance) && i.InputRevision > 0
}

type Frame struct {
	Protocol string `json:"protocol"`
	Type     string `json:"type"`
	Identity
	EventID  string `json:"event_id"`
	Revision uint64 `json:"revision,omitempty"`
	Seq      uint64 `json:"seq,omitempty"`
	Text     string `json:"text,omitempty"`
}

// DecodeControl rejects duplicate keys, unknown fields, trailing objects and
// unbounded text. Audio has a separate binary transport; never JSON/base64 here.
func DecodeControl(raw []byte) (Frame, error) {
	var f Frame
	if len(raw) == 0 || len(raw) > 16<<10 || !utf8.Valid(raw) {
		return f, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return f, ErrProtocol
	}
	keys := map[string]bool{}
	allowed := map[string]bool{"protocol": true, "type": true, "session_id": true, "epoch": true, "input_revision": true, "utterance_id": true, "event_id": true, "revision": true, "seq": true, "text": true}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return f, ErrProtocol
		}
		k, ok := key.(string)
		if !ok || !allowed[k] || keys[k] {
			return f, ErrProtocol
		}
		keys[k] = true
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return f, ErrProtocol
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return f, ErrProtocol
		}
	}
	if _, err = d.Token(); err != nil {
		return f, ErrProtocol
	}
	if _, err = d.Token(); err != io.EOF {
		return f, ErrProtocol
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil {
		return f, ErrProtocol
	}
	if f.Protocol != "duplex.v1" || !f.Identity.Valid() || !validID(f.EventID) || len([]rune(f.Text)) > 2048 {
		return Frame{}, ErrProtocol
	}
	switch f.Type {
	case "audio.start", "audio.end", "state.sync", "heartbeat", "response.interrupt":
		if f.Text != "" || f.Revision != 0 || f.Seq != 0 {
			return Frame{}, ErrProtocol
		}
	case "transcript.preview", "transcript.final":
		if f.Revision == 0 || strings.TrimSpace(f.Text) == "" || f.Seq != 0 {
			return Frame{}, ErrProtocol
		}
	default:
		return Frame{}, ErrProtocol
	}
	return f, nil
}

func Normalize(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(norm.NFC.String(s))), " ")
	// Remove only one sentence period, never question/exclamation/ellipsis.
	if strings.HasSuffix(s, "。") && !strings.HasSuffix(s, "。。") && !strings.HasSuffix(s, ".。") {
		return strings.TrimSuffix(s, "。")
	}
	if strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "..") && !strings.HasSuffix(s, "。.") {
		return strings.TrimSuffix(s, ".")
	}
	return s
}
func Agree(a, b string) bool {
	x, y := Normalize(a), Normalize(b)
	return x == y && strings.IndexFunc(x, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// Session is bounded single-utterance state. Callers supply a monotonic clock;
// session generation and input identity are server-issued, not authorization.
type Session struct {
	mu                sync.Mutex
	id                Identity
	state             string
	revision          uint64
	text              string
	nextSeq           uint64
	samples           int
	started, deadline time.Time
	leaseUsed         bool
	response, lease   string
	outputCanceled    bool
}

func New(id Identity) (*Session, error) {
	if !id.Valid() {
		return nil, ErrProtocol
	}
	return &Session{id: id, state: "idle"}, nil
}
func (s *Session) Start(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "idle" || s.leaseUsed {
		return ErrState
	}
	s.state = "capturing"
	s.started = now
	s.leaseUsed = true
	return nil
}
func (s *Session) Chunk(id Identity, seq uint64, pcm []byte, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id {
		return ErrStale
	}
	if s.state != "capturing" {
		return ErrState
	}
	if now.Sub(s.started) >= 15*time.Second {
		s.state = "timed_out"
		return ErrTimeout
	}
	if seq != s.nextSeq || len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > 2560 || s.samples+len(pcm)/2 > 240000 {
		s.state = "rejected"
		return ErrProtocol
	}
	s.nextSeq++
	s.samples += len(pcm) / 2
	return nil
}
func (s *Session) Preview(id Identity, revision uint64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id || revision <= s.revision {
		return ErrStale
	}
	if s.state != "capturing" {
		return ErrState
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len([]rune(text)) > 2048 {
		return ErrProtocol
	}
	s.revision = revision
	s.text = text
	return nil
}
func (s *Session) End(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "capturing" || s.samples == 0 {
		return ErrState
	}
	if now.Sub(s.started) >= 15*time.Second {
		s.state = "timed_out"
		return ErrTimeout
	}
	s.state = "confirming"
	s.deadline = now.Add(8 * time.Second)
	return nil
}
func (s *Session) Confirm(id Identity, revision uint64, final string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id || revision != s.revision {
		return ErrStale
	}
	if s.state != "confirming" {
		return ErrState
	}
	if !now.Before(s.deadline) {
		s.state = "timed_out"
		return ErrTimeout
	}
	if !utf8.ValidString(final) || len([]rune(final)) > 2048 || strings.TrimSpace(final) == "" {
		return ErrProtocol
	}
	if !Agree(s.text, final) {
		s.state = "needs_confirmation"
		return ErrConflict
	}
	s.state = "verified"
	return nil
}
func (s *Session) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "committed" && s.state != "committing" {
		s.state = "canceled"
	}
	s.response = ""
	s.lease = ""
	s.outputCanceled = true
}
func (s *Session) MarkCommitted(id Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id {
		return ErrStale
	}
	if s.state == "committed" {
		return nil
	}
	if s.state != "committing" {
		return ErrState
	}
	s.state = "committed"
	return nil
}

// BeginCommit is the last calculation deadline check. The coordinator must
// finish Judge and confirmation before this call; DB commit time is separate.
func (s *Session) BeginCommit(id Identity, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id {
		return ErrStale
	}
	if s.state != "verified" {
		return ErrState
	}
	if !now.Before(s.deadline) {
		s.state = "timed_out"
		return ErrTimeout
	}
	s.state = "committing"
	return nil
}

// CommitFailed is only for a confirmed rollback. An unknown database outcome
// must stay committing until the authorized coordinator looks up the receipt.
func (s *Session) CommitFailed(id Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id {
		return ErrStale
	}
	if s.state != "committing" {
		return ErrState
	}
	s.state = "rejected"
	if s.outputCanceled {
		s.state = "canceled"
	}
	return nil
}
func (s *Session) GrantOutput(response, lease string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "committed" || s.outputCanceled || !validID(response) || !validID(lease) {
		return ErrState
	}
	if s.lease != "" {
		return ErrState
	}
	s.response = response
	s.lease = lease
	return nil
}
func (s *Session) OutputValid(response, lease string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lease != "" && s.lease == lease && s.response == response
}
func (s *Session) State() string { s.mu.Lock(); defer s.mu.Unlock(); return s.state }

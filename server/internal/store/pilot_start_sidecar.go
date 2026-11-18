//go:build pilot_e2e

package store

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const pilotStartSidecarLimit = 1 << 20

var errPilotStartSidecar = errors.New("pilot start sidecar integrity error")

type pilotSidecarFile interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Sync() error
	Seek(int64, int) (int64, error)
	Stat() (os.FileInfo, error)
	Close() error
}

type pilotStartEvent struct {
	SchemaVersion  int     `json:"schema_version"`
	EventSeq       int64   `json:"event_seq"`
	EventType      string  `json:"event_type"`
	CommandKeyHMAC string  `json:"command_key_hmac"`
	CommandHMAC    string  `json:"command_hmac"`
	PlanHMAC       *string `json:"plan_hmac"`
	BundleHMAC     *string `json:"bundle_hmac"`
	AttemptID      string  `json:"attempt_id"`
	AttemptNo      int     `json:"attempt_no"`
	ToolName       *string `json:"tool_name"`
	ResultClass    *string `json:"result_class"`
	ActorRole      string  `json:"actor_role"`
	Trigger        string  `json:"trigger"`
	Scope          string  `json:"scope"`
	Risk           string  `json:"risk"`
	TaskHMAC       string  `json:"task_hmac"`
	TraceHMAC      string  `json:"trace_hmac"`
	TemplateID     *string `json:"template_id"`
	Source         *string `json:"source"`
	EventTimeMS    int64   `json:"event_time_ms"`
	PrevEventHMAC  *string `json:"prev_event_hmac"`
	EventHMAC      string  `json:"event_hmac,omitempty"`
}

type pilotStartSidecar struct {
	mu             sync.Mutex
	file           pilotSidecarFile
	key            []byte
	seq            int64
	prev           string
	phase          int
	attemptID      string
	commandKeyHMAC string
	commandHMAC    string
	planHMAC       string
	bundleHMAC     string
	sizeLimit      int64
	closed         bool
	degraded       bool
}

func openPilotStartSidecar(path string, key []byte) (*pilotStartSidecar, error) {
	return loadPilotStartSidecar(path, key, false, nil)
}

// createPilotStartSidecar is for a supervisor-verified new case only. Recovery
// paths must use openPilotStartSidecar and therefore fail if the file vanished.
func createPilotStartSidecar(path string, key []byte) (*pilotStartSidecar, error) {
	return createPilotStartSidecarWithParentSync(path, key, syncPilotSidecarParent)
}

func createPilotStartSidecarWithParentSync(path string, key []byte, syncParent func(string) error) (*pilotStartSidecar, error) {
	return loadPilotStartSidecar(path, key, true, syncParent)
}

func loadPilotStartSidecar(path string, key []byte, create bool, syncParent func(string) error) (*pilotStartSidecar, error) {
	if len(key) != 32 || path == "" {
		return nil, errPilotStartSidecar
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, errPilotStartSidecar
	}
	if info, statErr := os.Lstat(resolved); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !pilotSidecarModePrivate(info) {
			return nil, errPilotStartSidecar
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, errPilotStartSidecar
	}
	flags := os.O_RDWR | os.O_APPEND
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(resolved, flags, 0600)
	if err != nil {
		return nil, fmt.Errorf("%w", errPilotStartSidecar)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !pilotSidecarModePrivate(info) {
		_ = f.Close()
		return nil, errPilotStartSidecar
	}
	if create {
		if syncParent == nil || syncParent(resolved) != nil {
			_ = f.Close()
			return nil, errPilotStartSidecar
		}
	}
	if !create && info.Size() == 0 {
		_ = f.Close()
		return nil, errPilotStartSidecar
	}
	s := &pilotStartSidecar{file: f, key: append([]byte(nil), key...), sizeLimit: pilotStartSidecarLimit}
	if err := s.replay(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// Windows FileMode does not report the effective DACL. The harness must open
// this test-only file only after its OpenVerified ACL check; this helper does
// not claim to validate Windows ACLs itself.
func pilotSidecarModePrivate(info os.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm()&0077 == 0
}

// POSIX directory fsync makes the newly created directory entry durable. The
// Windows supervisor must provide equivalent verified-handle durability before
// this test-only constructor is eligible for integration.
func syncPilotSidecarParent(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *pilotStartSidecar) Append(event pilotStartEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.degraded || s.file == nil {
		return errPilotStartSidecar
	}
	event.SchemaVersion = 1
	event.EventSeq = s.seq + 1
	event.PrevEventHMAC = optionalPilotString(s.prev)
	event.EventHMAC = ""
	if err := validatePilotStartEvent(event, s.seq); err != nil {
		return err
	}
	nextPhase, err := nextPilotStartPhase(s.phase, event)
	if err != nil {
		return err
	}
	if err := s.validateContinuity(event); err != nil {
		return err
	}
	unsigned, err := json.Marshal(event)
	if err != nil {
		return errPilotStartSidecar
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(unsigned)
	event.EventHMAC = hex.EncodeToString(mac.Sum(nil))
	line, err := json.Marshal(event)
	if err != nil || len(line) > 8192 {
		return errPilotStartSidecar
	}
	info, err := s.file.Stat()
	if err != nil || info.Size()+int64(len(line)+1) > s.sizeLimit {
		s.degraded = true
		return errPilotStartSidecar
	}
	line = append(line, '\n')
	n, writeErr := s.file.Write(line)
	if writeErr != nil || n != len(line) {
		s.degraded = true
		return errPilotStartSidecar
	}
	if err = s.file.Sync(); err != nil {
		s.degraded = true
		return errPilotStartSidecar
	}
	s.commitContinuity(event)
	s.seq, s.prev, s.phase = event.EventSeq, event.EventHMAC, nextPhase
	return nil
}

func (s *pilotStartSidecar) replay() error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return errPilotStartSidecar
	}
	reader := bufio.NewReader(io.LimitReader(s.file, pilotStartSidecarLimit+1))
	var total int64
	var prior int64
	phase := 0
	var attemptID, commandKeyHMAC, commandHMAC, planHMAC, bundleHMAC string
	prev := ""
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			total += int64(len(line))
			if total > pilotStartSidecarLimit || len(line) > 8192 || line[len(line)-1] != '\n' {
				return errPilotStartSidecar
			}
			line = bytesTrimNewline(line)
			var event pilotStartEvent
			if err := pilotAuditJSON(line); err != nil {
				return errPilotStartSidecar
			}
			dec := json.NewDecoder(strings.NewReader(string(line)))
			dec.DisallowUnknownFields()
			if dec.Decode(&event) != nil || dec.Decode(new(any)) != io.EOF {
				return errPilotStartSidecar
			}
			canonicalLine, marshalErr := json.Marshal(event)
			if marshalErr != nil || !hmac.Equal(canonicalLine, line) || !validSHA256Hex(event.EventHMAC) {
				return errPilotStartSidecar
			}
			if err := validatePilotStartEvent(event, prior); err != nil || pilotStringValue(event.PrevEventHMAC) != prev {
				return errPilotStartSidecar
			}
			nextPhase, phaseErr := nextPilotStartPhase(phase, event)
			if phaseErr != nil {
				return errPilotStartSidecar
			}
			state := &pilotStartSidecar{attemptID: attemptID, commandKeyHMAC: commandKeyHMAC, commandHMAC: commandHMAC, planHMAC: planHMAC, bundleHMAC: bundleHMAC}
			if err := state.validateContinuity(event); err != nil {
				return errPilotStartSidecar
			}
			state.commitContinuity(event)
			attemptID, commandKeyHMAC, commandHMAC, planHMAC, bundleHMAC = state.attemptID, state.commandKeyHMAC, state.commandHMAC, state.planHMAC, state.bundleHMAC
			saved := event.EventHMAC
			event.EventHMAC = ""
			unsigned, _ := json.Marshal(event)
			mac := hmac.New(sha256.New, s.key)
			_, _ = mac.Write(unsigned)
			want := hex.EncodeToString(mac.Sum(nil))
			got, e1 := hex.DecodeString(saved)
			wantBytes, e2 := hex.DecodeString(want)
			if e1 != nil || e2 != nil || !hmac.Equal(got, wantBytes) {
				return errPilotStartSidecar
			}
			prior, prev, phase = event.EventSeq, saved, nextPhase
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errPilotStartSidecar
		}
	}
	s.seq, s.prev, s.phase = prior, prev, phase
	s.attemptID, s.commandKeyHMAC, s.commandHMAC, s.planHMAC, s.bundleHMAC = attemptID, commandKeyHMAC, commandHMAC, planHMAC, bundleHMAC
	if info, statErr := s.file.Stat(); statErr != nil {
		return errPilotStartSidecar
	} else if info.Size() >= pilotStartSidecarLimit {
		s.degraded = true
	}
	_, err := s.file.Seek(0, io.SeekEnd)
	if err != nil {
		return errPilotStartSidecar
	}
	return nil
}

func (s *pilotStartSidecar) validateContinuity(e pilotStartEvent) error {
	if s.attemptID != "" && (e.AttemptID != s.attemptID || e.CommandKeyHMAC != s.commandKeyHMAC || e.CommandHMAC != s.commandHMAC) {
		return errPilotStartSidecar
	}
	if s.planHMAC != "" && e.PlanHMAC != nil && *e.PlanHMAC != s.planHMAC {
		return errPilotStartSidecar
	}
	if s.planHMAC != "" && e.PlanHMAC == nil {
		return errPilotStartSidecar
	}
	if s.bundleHMAC != "" && e.BundleHMAC != nil && *e.BundleHMAC != s.bundleHMAC {
		return errPilotStartSidecar
	}
	if s.bundleHMAC != "" && (e.BundleHMAC == nil || *e.BundleHMAC != s.bundleHMAC) {
		return errPilotStartSidecar
	}
	return nil
}

func (s *pilotStartSidecar) commitContinuity(e pilotStartEvent) {
	if s.attemptID == "" {
		s.attemptID, s.commandKeyHMAC, s.commandHMAC = e.AttemptID, e.CommandKeyHMAC, e.CommandHMAC
	}
	if e.EventType == "plan_ready" && e.PlanHMAC != nil {
		s.planHMAC = *e.PlanHMAC
	}
	if e.EventType == "bundle_ready" && e.BundleHMAC != nil {
		s.bundleHMAC = *e.BundleHMAC
	}
}

func validatePilotStartEvent(e pilotStartEvent, prior int64) error {
	if e.SchemaVersion != 1 || e.EventSeq != prior+1 || e.EventTimeMS <= 0 || e.AttemptNo < 0 || e.AttemptNo > 2 || len(e.AttemptID) != 32 || !isLowerHex(e.AttemptID) || !validSHA256Hex(e.CommandKeyHMAC) || !validSHA256Hex(e.CommandHMAC) || !validSHA256Hex(e.TaskHMAC) || !validSHA256Hex(e.TraceHMAC) || e.ActorRole != "teacher_agent" || e.Trigger != "classroom_event" || e.Scope != "teacher_agent" || e.Risk != "low" {
		return errPilotStartSidecar
	}
	switch e.EventType {
	case "attempt_started":
		if prior != 0 || e.AttemptNo != 0 || e.PlanHMAC != nil || e.BundleHMAC != nil || e.ToolName != nil || e.ResultClass != nil || e.TemplateID != nil || e.Source != nil {
			return errPilotStartSidecar
		}
	case "plan_ready":
		if prior < 1 || e.AttemptNo != 0 || !validOptionalPilotHash(e.PlanHMAC) || e.PlanHMAC == nil || e.BundleHMAC != nil || e.ToolName != nil || e.ResultClass != nil || (pilotStringValue(e.Source) != "template" && pilotStringValue(e.Source) != "fallback") || (pilotStringValue(e.Source) == "template" && pilotStringValue(e.TemplateID) != "OP-1") || (pilotStringValue(e.Source) == "fallback" && e.TemplateID != nil) {
			return errPilotStartSidecar
		}
	case "tool_started", "tool_result":
		if prior < 2 || e.AttemptNo != 0 || !validOptionalPilotHash(e.PlanHMAC) || e.PlanHMAC == nil || e.BundleHMAC != nil || (pilotStringValue(e.ToolName) != "classroom.say" && pilotStringValue(e.ToolName) != "classroom.show_word") || e.TemplateID != nil || e.Source != nil || (e.EventType == "tool_result" && !validPilotResultClass(pilotStringValue(e.ResultClass))) || (e.EventType == "tool_started" && e.ResultClass != nil) {
			return errPilotStartSidecar
		}
	case "bundle_ready":
		if prior < 2 || e.AttemptNo != 0 || !validOptionalPilotHash(e.PlanHMAC) || !validOptionalPilotHash(e.BundleHMAC) || e.PlanHMAC == nil || e.BundleHMAC == nil || e.ToolName != nil || e.ResultClass != nil || e.TemplateID != nil || e.Source != nil {
			return errPilotStartSidecar
		}
	case "commit_attempt_started":
		if prior < 1 || (e.AttemptNo != 1 && e.AttemptNo != 2) || e.PlanHMAC == nil || e.BundleHMAC == nil || e.ToolName != nil || e.ResultClass != nil || e.TemplateID != nil || e.Source != nil {
			return errPilotStartSidecar
		}
	case "committed", "commit_unknown", "terminal_failed", "rollback_proven":
		resultClass := pilotStringValue(e.ResultClass)
		if prior < 1 || (e.EventType != "terminal_failed" && e.AttemptNo != 1 && e.AttemptNo != 2) || e.ToolName != nil || e.TemplateID != nil || e.Source != nil || !validPilotResultClass(resultClass) || (e.EventType == "committed" && resultClass != "ok") || (e.EventType == "commit_unknown" && resultClass != "commit_unknown") || (e.EventType == "rollback_proven" && resultClass != "rollback_proven") || (e.EventType == "terminal_failed" && (resultClass == "ok" || resultClass == "commit_unknown" || resultClass == "rollback_proven")) {
			return errPilotStartSidecar
		}
	default:
		return errPilotStartSidecar
	}
	return nil
}

func validPilotResultClass(value string) bool {
	switch value {
	case "ok", "schema_rejected", "budget_denied", "handler_error", "handler_panic", "planner_error", "output_invalid", "unauthorized", "not_supported", "schema_invalid", "authorization_rejected", "stale_checkpoint", "already_started", "command_conflict", "write_error", "rollback_proven", "commit_unknown":
		return true
	default:
		return false
	}
}

func nextPilotStartPhase(phase int, e pilotStartEvent) (int, error) {
	switch {
	case phase == 0 && e.EventType == "attempt_started":
		return 1, nil
	case phase == 1 && e.EventType == "plan_ready":
		return 2, nil
	case phase == 2 && e.EventType == "tool_started" && pilotStringValue(e.ToolName) == "classroom.say":
		return 3, nil
	case phase == 3 && e.EventType == "tool_result" && pilotStringValue(e.ToolName) == "classroom.say" && pilotStringValue(e.ResultClass) == "ok":
		return 4, nil
	case phase == 3 && e.EventType == "tool_result" && pilotStringValue(e.ToolName) == "classroom.say":
		return 13, nil
	case phase == 4 && e.EventType == "tool_started" && pilotStringValue(e.ToolName) == "classroom.show_word":
		return 5, nil
	case phase == 5 && e.EventType == "tool_result" && pilotStringValue(e.ToolName) == "classroom.show_word" && pilotStringValue(e.ResultClass) == "ok":
		return 6, nil
	case phase == 5 && e.EventType == "tool_result" && pilotStringValue(e.ToolName) == "classroom.show_word":
		return 14, nil
	case phase == 6 && e.EventType == "bundle_ready":
		return 7, nil
	case phase == 7 && e.EventType == "commit_attempt_started" && e.AttemptNo == 1:
		return 8, nil
	case phase == 8 && e.AttemptNo == 1 && (e.EventType == "committed" || e.EventType == "commit_unknown"):
		return 9, nil
	case phase == 8 && e.EventType == "rollback_proven" && e.AttemptNo == 1:
		return 10, nil
	case phase == 10 && e.EventType == "commit_attempt_started" && e.AttemptNo == 2:
		return 11, nil
	case phase == 11 && e.AttemptNo == 2 && (e.EventType == "committed" || e.EventType == "commit_unknown"):
		return 9, nil
	case phase == 11 && e.EventType == "rollback_proven" && e.AttemptNo == 2:
		return 12, nil
	case (phase == 1 || phase == 2 || phase == 4 || phase == 6 || phase == 7 || phase == 13 || phase == 14) && e.EventType == "terminal_failed" && e.AttemptNo == 0:
		return 9, nil
	case phase == 8 && e.EventType == "terminal_failed" && e.AttemptNo == 1:
		return 9, nil
	case (phase == 11 || phase == 12) && e.EventType == "terminal_failed" && e.AttemptNo == 2:
		return 9, nil
	default:
		return phase, errPilotStartSidecar
	}
}

func isLowerHex(value string) bool {
	if value == "" || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func optionalPilotString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func pilotStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validOptionalPilotHash(value *string) bool {
	return value == nil || validSHA256Hex(*value)
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	b, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(b) == value
}

func bytesTrimNewline(b []byte) []byte {
	if len(b) != 0 && b[len(b)-1] == '\n' {
		return b[:len(b)-1]
	}
	return b
}

func (s *pilotStartSidecar) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for i := range s.key {
		s.key[i] = 0
	}
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	if err != nil {
		return fmt.Errorf("%w", errPilotStartSidecar)
	}
	return nil
}

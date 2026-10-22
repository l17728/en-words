//go:build pilot_e2e

package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type pilotSidecarFaultFile struct {
	*os.File
	writeMode string
	syncErr   error
}

type pilotReplayFaultFile struct {
	*os.File
	readErr     error
	seekCall    int
	seekErrCall int
	statErr     error
}

func (f *pilotReplayFaultFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.Read(p)
}
func (f *pilotReplayFaultFile) Seek(offset int64, whence int) (int64, error) {
	f.seekCall++
	if f.seekCall == f.seekErrCall {
		return 0, errors.New("injected seek failure")
	}
	return f.File.Seek(offset, whence)
}
func (f *pilotReplayFaultFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func signedPilotReplayLine(key []byte, event pilotStartEvent, priorHMAC string) []byte {
	event.SchemaVersion = 1
	event.PrevEventHMAC = optionalPilotString(priorHMAC)
	event.EventHMAC = ""
	unsigned, _ := json.Marshal(event)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(unsigned)
	event.EventHMAC = hex.EncodeToString(mac.Sum(nil))
	line, _ := json.Marshal(event)
	return append(line, '\n')
}

func TestPilotStartSidecarReplayFailureBranches(t *testing.T) {
	key := bytesFill(32, 0x92)
	newFile := func(t *testing.T) *os.File {
		t.Helper()
		path := filepath.Join(t.TempDir(), "replay.jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	base := pilotStartEvent{EventSeq: 1, EventType: "attempt_started", AttemptID: hex.EncodeToString(bytesFill(16, 0x93)),
		CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x94)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x95)),
		ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
		TaskHMAC: hex.EncodeToString(bytesFill(32, 0x96)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x97)), EventTimeMS: 1700000000000}
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *os.File) *pilotReplayFaultFile
	}{
		{"seek start failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			return &pilotReplayFaultFile{File: f, seekErrCall: 1}
		}},
		{"read failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			return &pilotReplayFaultFile{File: f, readErr: errors.New("injected read failure")}
		}},
		{"stat failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			return &pilotReplayFaultFile{File: f, statErr: errors.New("injected stat failure")}
		}},
		{"seek end failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			return &pilotReplayFaultFile{File: f, seekErrCall: 2}
		}},
		{"event validation failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			e := base
			e.ActorRole = "untrusted"
			return writeReplayBytes(t, f, signedPilotReplayLine(key, e, ""))
		}},
		{"phase transition failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			plan := hex.EncodeToString(bytesFill(32, 0x98))
			bundle := hex.EncodeToString(bytesFill(32, 0x99))
			lines := [][]byte{signedPilotReplayLine(key, base, "")}
			previous := func() string {
				var e pilotStartEvent
				_ = json.Unmarshal(lines[len(lines)-1][:len(lines[len(lines)-1])-1], &e)
				return e.EventHMAC
			}
			e := base
			e.EventSeq = 2
			e.EventType = "plan_ready"
			e.PlanHMAC = &plan
			e.TemplateID = optionalPilotString("OP-1")
			e.Source = optionalPilotString("template")
			lines = append(lines, signedPilotReplayLine(key, e, previous()))
			e = base
			e.EventSeq = 3
			e.EventType = "tool_started"
			e.PlanHMAC = &plan
			e.ToolName = optionalPilotString("classroom.say")
			lines = append(lines, signedPilotReplayLine(key, e, previous()))
			e = base
			e.EventSeq = 4
			e.EventType = "tool_result"
			e.PlanHMAC = &plan
			e.ToolName = optionalPilotString("classroom.say")
			e.ResultClass = optionalPilotString("ok")
			lines = append(lines, signedPilotReplayLine(key, e, previous()))
			// bundle_ready has a valid schema and hashes, but is forbidden before phase 6.
			e = base
			e.EventSeq = 5
			e.EventType = "bundle_ready"
			e.PlanHMAC = &plan
			e.BundleHMAC = &bundle
			lines = append(lines, signedPilotReplayLine(key, e, previous()))
			var data []byte
			for _, line := range lines {
				data = append(data, line...)
			}
			return writeReplayBytes(t, f, data)
		}},
		{"continuity failure", func(t *testing.T, f *os.File) *pilotReplayFaultFile {
			first := signedPilotReplayLine(key, base, "")
			var previous pilotStartEvent
			_ = json.Unmarshal(first[:len(first)-1], &previous)
			plan := hex.EncodeToString(bytesFill(32, 0x9a))
			e := base
			e.EventSeq = 2
			e.AttemptID = hex.EncodeToString(bytesFill(16, 0x9b))
			e.EventType = "plan_ready"
			e.PlanHMAC = &plan
			e.TemplateID = optionalPilotString("OP-1")
			e.Source = optionalPilotString("template")
			return writeReplayBytes(t, f, append(first, signedPilotReplayLine(key, e, previous.EventHMAC)...))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFile(t)
			defer f.Close()
			fault := tc.setup(t, f)
			s := &pilotStartSidecar{file: fault, key: key}
			if err := s.replay(); !errors.Is(err, errPilotStartSidecar) {
				t.Fatalf("replay error=%v, want integrity failure", err)
			}
			_ = f.Close()
		})
	}
}

func writeReplayBytes(t *testing.T, f *os.File, data []byte) *pilotReplayFaultFile {
	t.Helper()
	if err := f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	return &pilotReplayFaultFile{File: f}
}

func (f *pilotSidecarFaultFile) Write(data []byte) (int, error) {
	switch f.writeMode {
	case "zero":
		return 0, errors.New("injected zero-byte write")
	case "partial":
		n, _ := f.File.Write(data[:len(data)/2])
		return n, errors.New("injected partial write")
	default:
		return f.File.Write(data)
	}
}

func (f *pilotSidecarFaultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func TestPilotStartSidecarAttemptGoldenAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar.jsonl")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := createPilotStartSidecar(path, key)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Append(pilotStartEvent{
		EventType: "attempt_started", AttemptNo: 0, CommandKeyHMAC: "0000000000000000000000000000000000000000000000000000000000000000",
		CommandHMAC: "1111111111111111111111111111111111111111111111111111111111111111",
		AttemptID:   "22222222222222222222222222222222", ActorRole: "teacher_agent", Trigger: "classroom_event",
		Scope: "teacher_agent", Risk: "low", TaskHMAC: "3333333333333333333333333333333333333333333333333333333333333333",
		TraceHMAC: "4444444444444444444444444444444444444444444444444444444444444444", EventTimeMS: 1700000000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.prev != "dbeb47c57bd4b24259ac6befa98e7fba2926d9106cbeb000a19dff02595ac50f" {
		t.Fatalf("golden HMAC mismatch: %s", s.prev)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openPilotStartSidecar(path, key)
	if err != nil {
		t.Fatalf("reopen valid chain: %v", err)
	}
	if s.seq != 1 || s.prev != "dbeb47c57bd4b24259ac6befa98e7fba2926d9106cbeb000a19dff02595ac50f" {
		t.Fatalf("unexpected recovered state: seq=%d prev=%s", s.seq, s.prev)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPilotStartSidecarRejectsTamperTruncationAndWrongKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
		key    []byte
	}{
		{name: "tampered", mutate: func(b []byte) []byte { b[20] ^= 1; return b }},
		{name: "truncated", mutate: func(b []byte) []byte { return b[:len(b)-1] }},
		{name: "wrong key", mutate: func(b []byte) []byte { return b }, key: make([]byte, 32)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sidecar.jsonl")
			key := make([]byte, 32)
			for i := range key {
				key[i] = byte(i)
			}
			s, err := createPilotStartSidecar(path, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Append(pilotStartEvent{EventType: "attempt_started", AttemptNo: 0, CommandKeyHMAC: hex.EncodeToString(make([]byte, 32)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x11)), AttemptID: hex.EncodeToString(bytesFill(16, 0x22)), ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low", TaskHMAC: hex.EncodeToString(bytesFill(32, 0x33)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x44)), EventTimeMS: 1700000000000}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				data = tc.mutate(data)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			openKey := key
			if tc.key != nil {
				openKey = tc.key
			}
			opened, err := openPilotStartSidecar(path, openKey)
			if err == nil {
				_ = opened.Close()
				t.Fatal("expected integrity rejection")
			}
			if !errors.Is(err, errPilotStartSidecar) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPilotStartSidecarRejectsOutOfOrderAndCrossAttemptEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar.jsonl")
	key := bytesFill(32, 0x5a)
	s, err := createPilotStartSidecar(path, key)
	if err != nil {
		t.Fatal(err)
	}
	base := pilotStartEvent{
		EventType: "attempt_started", CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x01)),
		CommandHMAC: hex.EncodeToString(bytesFill(32, 0x02)), AttemptID: hex.EncodeToString(bytesFill(16, 0x03)),
		ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
		TaskHMAC: hex.EncodeToString(bytesFill(32, 0x04)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x05)), EventTimeMS: 1700000000000,
	}
	if err := s.Append(pilotStartEvent{EventType: "tool_started"}); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("out-of-order tool event should fail before writing: %v", err)
	}
	if s.seq != 0 {
		t.Fatalf("invalid event advanced sequence: %d", s.seq)
	}
	if err := s.Append(base); err != nil {
		t.Fatal(err)
	}
	planHash := hex.EncodeToString(bytesFill(32, 0x06))
	if err := s.Append(pilotStartEvent{
		EventType: "plan_ready", CommandKeyHMAC: base.CommandKeyHMAC, CommandHMAC: base.CommandHMAC,
		AttemptID: base.AttemptID, ActorRole: base.ActorRole, Trigger: base.Trigger, Scope: base.Scope, Risk: base.Risk,
		TaskHMAC: base.TaskHMAC, TraceHMAC: base.TraceHMAC, PlanHMAC: &planHash, TemplateID: optionalPilotString("OP-1"),
		Source: optionalPilotString("template"), EventTimeMS: base.EventTimeMS,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(pilotStartEvent{
		EventType: "tool_started", CommandKeyHMAC: base.CommandKeyHMAC, CommandHMAC: base.CommandHMAC,
		AttemptID: hex.EncodeToString(bytesFill(16, 0x09)), ActorRole: base.ActorRole, Trigger: base.Trigger, Scope: base.Scope, Risk: base.Risk,
		TaskHMAC: base.TaskHMAC, TraceHMAC: base.TraceHMAC, PlanHMAC: &planHash, ToolName: optionalPilotString("classroom.say"),
		EventTimeMS: base.EventTimeMS,
	}); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("cross-attempt event should be rejected: %v", err)
	}
	if s.seq != 2 {
		t.Fatalf("invalid event advanced sequence: %d", s.seq)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPilotStartSidecarCommitRetryIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase int
		event pilotStartEvent
		want  int
		ok    bool
	}{
		{name: "first commit", phase: 7, event: pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 1}, want: 8, ok: true},
		{name: "cannot skip first commit", phase: 7, event: pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 2}, want: 7},
		{name: "first rollback allows one retry", phase: 8, event: pilotStartEvent{EventType: "rollback_proven", AttemptNo: 1}, want: 10, ok: true},
		{name: "retry is second attempt", phase: 10, event: pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 2}, want: 11, ok: true},
		{name: "second rollback requires terminal failure", phase: 11, event: pilotStartEvent{EventType: "rollback_proven", AttemptNo: 2}, want: 12, ok: true},
		{name: "attempt two terminal failure", phase: 11, event: pilotStartEvent{EventType: "terminal_failed", AttemptNo: 2}, want: 9, ok: true},
		{name: "third commit forbidden", phase: 12, event: pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 3}, want: 12},
		{name: "terminal failure closes retry", phase: 12, event: pilotStartEvent{EventType: "terminal_failed", AttemptNo: 2}, want: 9, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextPilotStartPhase(tc.phase, tc.event)
			if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
				t.Fatalf("next phase = %d, err = %v; want phase %d, ok=%v", got, err, tc.want, tc.ok)
			}
		})
	}
}

func TestPilotStartSidecarToolFailuresMustTerminateBeforeNextTool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase int
		event pilotStartEvent
		want  int
		ok    bool
	}{
		{name: "say success permits show", phase: 3, event: pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("ok")}, want: 4, ok: true},
		{name: "say failure closes tool sequence", phase: 3, event: pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("handler_error")}, want: 13, ok: true},
		{name: "show forbidden after say failure", phase: 13, event: pilotStartEvent{EventType: "tool_started", ToolName: optionalPilotString("classroom.show_word")}, want: 13},
		{name: "say failure can become terminal", phase: 13, event: pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, want: 9, ok: true},
		{name: "show failure closes bundle path", phase: 5, event: pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.show_word"), ResultClass: optionalPilotString("handler_panic")}, want: 14, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextPilotStartPhase(tc.phase, tc.event)
			if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
				t.Fatalf("next phase = %d, err = %v; want phase %d, ok=%v", got, err, tc.want, tc.ok)
			}
		})
	}
}

func TestPilotStartEventSchemaAcceptsAndRejectsEachEventShape(t *testing.T) {
	plan := hex.EncodeToString(bytesFill(32, 0x61))
	bundle := hex.EncodeToString(bytesFill(32, 0x62))
	base := pilotStartEvent{SchemaVersion: 1, EventSeq: 2, AttemptID: hex.EncodeToString(bytesFill(16, 0x63)),
		CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x64)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x65)),
		ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
		TaskHMAC: hex.EncodeToString(bytesFill(32, 0x66)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x67)), EventTimeMS: 1700000000000}
	for _, tc := range []struct {
		name  string
		event pilotStartEvent
		prior int64
	}{
		{"attempt start", func() pilotStartEvent { e := base; e.EventType = "attempt_started"; e.EventSeq = 1; return e }(), 0},
		{"template plan", func() pilotStartEvent {
			e := base
			e.EventType = "plan_ready"
			e.PlanHMAC = &plan
			e.TemplateID = optionalPilotString("OP-1")
			e.Source = optionalPilotString("template")
			return e
		}(), 1},
		{"fallback plan", func() pilotStartEvent {
			e := base
			e.EventType = "plan_ready"
			e.PlanHMAC = &plan
			e.Source = optionalPilotString("fallback")
			return e
		}(), 1},
		{"tool started", func() pilotStartEvent {
			e := base
			e.EventType = "tool_started"
			e.PlanHMAC = &plan
			e.ToolName = optionalPilotString("classroom.say")
			return e
		}(), 2},
		{"tool result", func() pilotStartEvent {
			e := base
			e.EventType = "tool_result"
			e.PlanHMAC = &plan
			e.ToolName = optionalPilotString("classroom.show_word")
			e.ResultClass = optionalPilotString("ok")
			return e
		}(), 2},
		{"bundle ready", func() pilotStartEvent {
			e := base
			e.EventType = "bundle_ready"
			e.PlanHMAC = &plan
			e.BundleHMAC = &bundle
			return e
		}(), 2},
		{"commit started", func() pilotStartEvent {
			e := base
			e.EventType = "commit_attempt_started"
			e.AttemptNo = 1
			e.PlanHMAC = &plan
			e.BundleHMAC = &bundle
			return e
		}(), 1},
		{"committed", func() pilotStartEvent {
			e := base
			e.EventType = "committed"
			e.AttemptNo = 1
			e.PlanHMAC = &plan
			e.BundleHMAC = &bundle
			e.ResultClass = optionalPilotString("ok")
			return e
		}(), 1},
		{"commit unknown", func() pilotStartEvent {
			e := base
			e.EventType = "commit_unknown"
			e.AttemptNo = 1
			e.PlanHMAC = &plan
			e.BundleHMAC = &bundle
			e.ResultClass = optionalPilotString("commit_unknown")
			return e
		}(), 1},
		{"rollback", func() pilotStartEvent {
			e := base
			e.EventType = "rollback_proven"
			e.AttemptNo = 1
			e.PlanHMAC = &plan
			e.BundleHMAC = &bundle
			e.ResultClass = optionalPilotString("rollback_proven")
			return e
		}(), 1},
		{"terminal failure", func() pilotStartEvent {
			e := base
			e.EventType = "terminal_failed"
			e.PlanHMAC = &plan
			e.ResultClass = optionalPilotString("planner_error")
			return e
		}(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.event.EventSeq = tc.prior + 1
			if err := validatePilotStartEvent(tc.event, tc.prior); err != nil {
				t.Fatalf("valid event rejected: %v", err)
			}
		})
	}
	invalid := []func(*pilotStartEvent){
		func(e *pilotStartEvent) { e.SchemaVersion = 2 }, func(e *pilotStartEvent) { e.EventSeq = 0 },
		func(e *pilotStartEvent) { e.EventTimeMS = 0 }, func(e *pilotStartEvent) { e.AttemptNo = 3 },
		func(e *pilotStartEvent) { e.AttemptID = "ABC" }, func(e *pilotStartEvent) { e.CommandKeyHMAC = "bad" },
		func(e *pilotStartEvent) { e.ActorRole = "student" }, func(e *pilotStartEvent) { e.Trigger = "http" },
		func(e *pilotStartEvent) { e.Scope = "system_agent" }, func(e *pilotStartEvent) { e.Risk = "high" },
		func(e *pilotStartEvent) { e.TaskHMAC = "bad" }, func(e *pilotStartEvent) { e.TraceHMAC = "bad" },
	}
	for i, mutate := range invalid {
		e := base
		e.EventType = "attempt_started"
		e.EventSeq = 1
		mutate(&e)
		if err := validatePilotStartEvent(e, 0); !errors.Is(err, errPilotStartSidecar) {
			t.Errorf("invalid common field %d accepted: %v", i, err)
		}
	}
	invalidShapes := []pilotStartEvent{
		{EventType: "unknown"},
		{EventType: "attempt_started", AttemptNo: 1},
		{EventType: "plan_ready", AttemptNo: 0, Source: optionalPilotString("unknown"), PlanHMAC: &plan},
		{EventType: "plan_ready", AttemptNo: 0, Source: optionalPilotString("fallback"), TemplateID: optionalPilotString("OP-1"), PlanHMAC: &plan},
		{EventType: "tool_started", AttemptNo: 0, PlanHMAC: &plan, ToolName: optionalPilotString("classroom.bad")},
		{EventType: "tool_started", AttemptNo: 0, PlanHMAC: &plan, ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("ok")},
		{EventType: "tool_result", AttemptNo: 0, PlanHMAC: &plan, ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("bad")},
		{EventType: "bundle_ready", AttemptNo: 0, PlanHMAC: &plan},
		{EventType: "commit_attempt_started", AttemptNo: 3, PlanHMAC: &plan, BundleHMAC: &bundle},
		{EventType: "committed", AttemptNo: 1, ResultClass: optionalPilotString("write_error")},
		{EventType: "terminal_failed", AttemptNo: 0, ResultClass: optionalPilotString("ok")},
		{EventType: "terminal_failed", AttemptNo: 1, ResultClass: optionalPilotString("commit_unknown")},
		{EventType: "terminal_failed", AttemptNo: 1, ResultClass: optionalPilotString("rollback_proven")},
	}
	for i, shape := range invalidShapes {
		e := base
		e.EventType, e.AttemptNo = shape.EventType, shape.AttemptNo
		e.PlanHMAC, e.BundleHMAC, e.ToolName, e.ResultClass, e.TemplateID, e.Source = shape.PlanHMAC, shape.BundleHMAC, shape.ToolName, shape.ResultClass, shape.TemplateID, shape.Source
		if err := validatePilotStartEvent(e, 1); !errors.Is(err, errPilotStartSidecar) {
			t.Errorf("invalid event shape %d accepted: %v", i, err)
		}
	}
}

func TestPilotStartPhaseTableCoversSuccessfulFailureAndForbiddenTransitions(t *testing.T) {
	for _, tc := range []struct {
		phase int
		event pilotStartEvent
		want  int
		ok    bool
	}{
		{0, pilotStartEvent{EventType: "attempt_started"}, 1, true}, {1, pilotStartEvent{EventType: "plan_ready"}, 2, true},
		{2, pilotStartEvent{EventType: "tool_started", ToolName: optionalPilotString("classroom.say")}, 3, true},
		{3, pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("ok")}, 4, true},
		{3, pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("handler_error")}, 13, true},
		{4, pilotStartEvent{EventType: "tool_started", ToolName: optionalPilotString("classroom.show_word")}, 5, true},
		{5, pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.show_word"), ResultClass: optionalPilotString("ok")}, 6, true},
		{5, pilotStartEvent{EventType: "tool_result", ToolName: optionalPilotString("classroom.show_word"), ResultClass: optionalPilotString("handler_error")}, 14, true},
		{6, pilotStartEvent{EventType: "bundle_ready"}, 7, true}, {7, pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 1}, 8, true},
		{8, pilotStartEvent{EventType: "committed", AttemptNo: 1}, 9, true}, {8, pilotStartEvent{EventType: "commit_unknown", AttemptNo: 1}, 9, true},
		{8, pilotStartEvent{EventType: "rollback_proven", AttemptNo: 1}, 10, true}, {10, pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 2}, 11, true},
		{11, pilotStartEvent{EventType: "committed", AttemptNo: 2}, 9, true}, {11, pilotStartEvent{EventType: "commit_unknown", AttemptNo: 2}, 9, true},
		{11, pilotStartEvent{EventType: "rollback_proven", AttemptNo: 2}, 12, true},
		{1, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, 9, true}, {7, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, 9, true},
		{8, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 1}, 9, true}, {12, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 2}, 9, true},
		{13, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, 9, true}, {14, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, 9, true},
		{0, pilotStartEvent{EventType: "plan_ready"}, 0, false}, {9, pilotStartEvent{EventType: "attempt_started"}, 9, false},
		{2, pilotStartEvent{EventType: "tool_started", ToolName: optionalPilotString("classroom.show_word")}, 2, false},
		{3, pilotStartEvent{EventType: "tool_started", ToolName: optionalPilotString("classroom.show_word")}, 3, false},
		{3, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, 3, false},
		{5, pilotStartEvent{EventType: "terminal_failed", AttemptNo: 0}, 5, false},
		{7, pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 2}, 7, false},
		{12, pilotStartEvent{EventType: "commit_attempt_started", AttemptNo: 3}, 12, false},
	} {
		got, err := nextPilotStartPhase(tc.phase, tc.event)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("phase %d + %s => %d, %v; want %d ok=%v", tc.phase, tc.event.EventType, got, err, tc.want, tc.ok)
		}
	}
}

func TestPilotStartToolFailureCannotCloseUnknownAndReplaysKnownFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool-failure.jsonl")
	key := bytesFill(32, 0x71)
	s, err := createPilotStartSidecar(path, key)
	if err != nil {
		t.Fatal(err)
	}
	base := pilotStartEvent{CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x72)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x73)),
		AttemptID: hex.EncodeToString(bytesFill(16, 0x74)), ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
		TaskHMAC: hex.EncodeToString(bytesFill(32, 0x75)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x76)), EventTimeMS: 1700000000000}
	appendEvent := func(event pilotStartEvent) {
		event.CommandKeyHMAC, event.CommandHMAC, event.AttemptID = base.CommandKeyHMAC, base.CommandHMAC, base.AttemptID
		event.ActorRole, event.Trigger, event.Scope, event.Risk = base.ActorRole, base.Trigger, base.Scope, base.Risk
		event.TaskHMAC, event.TraceHMAC, event.EventTimeMS = base.TaskHMAC, base.TraceHMAC, base.EventTimeMS
		if err := s.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.EventType, err)
		}
	}
	appendEvent(pilotStartEvent{EventType: "attempt_started"})
	plan := hex.EncodeToString(bytesFill(32, 0x77))
	appendEvent(pilotStartEvent{EventType: "plan_ready", PlanHMAC: &plan, TemplateID: optionalPilotString("OP-1"), Source: optionalPilotString("template")})
	appendEvent(pilotStartEvent{EventType: "tool_started", PlanHMAC: &plan, ToolName: optionalPilotString("classroom.say")})
	unknown := pilotStartEvent{EventType: "terminal_failed", PlanHMAC: &plan, ResultClass: optionalPilotString("write_error")}
	if err := s.Append(unknown); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("unresolved tool call was terminalized: %v", err)
	}
	appendEvent(pilotStartEvent{EventType: "tool_result", PlanHMAC: &plan, ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("handler_error")})
	appendEvent(pilotStartEvent{EventType: "terminal_failed", PlanHMAC: &plan, ResultClass: optionalPilotString("handler_error")})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openPilotStartSidecar(path, key)
	if err != nil {
		t.Fatalf("known tool failure replay failed: %v", err)
	}
	defer reopened.Close()
	if reopened.phase != 9 {
		t.Fatalf("failure chain recovered phase %d, want terminal phase 9", reopened.phase)
	}
}

func TestPilotResultClassAndHexValidationBoundaries(t *testing.T) {
	for _, value := range []string{"ok", "schema_rejected", "budget_denied", "handler_error", "handler_panic", "planner_error", "output_invalid", "unauthorized", "not_supported", "schema_invalid", "authorization_rejected", "stale_checkpoint", "already_started", "command_conflict", "write_error", "rollback_proven", "commit_unknown"} {
		if !validPilotResultClass(value) {
			t.Errorf("known result class %q rejected", value)
		}
	}
	for _, value := range []string{"", "OK", "unknown"} {
		if validPilotResultClass(value) {
			t.Errorf("unknown result class %q accepted", value)
		}
	}
	for _, value := range []string{"", "ABC", "gg", "ab-cd"} {
		if isLowerHex(value) {
			t.Errorf("invalid lowercase hex %q accepted", value)
		}
	}
	for _, value := range []string{"", "00", "GG"} {
		if validSHA256Hex(value) {
			t.Errorf("invalid SHA256 %q accepted", value)
		}
	}
}

func TestPilotStartSidecarRecoveryRejectsMissingOrEmptyFile(t *testing.T) {
	key := bytesFill(32, 0x7b)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	if _, err := openPilotStartSidecar(missing, key); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("missing recovery sidecar must fail closed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	s, err := createPilotStartSidecar(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openPilotStartSidecar(path, key); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("empty recovery sidecar must fail closed: %v", err)
	}
}

func TestPilotStartSidecarTerminalEventsStayBoundToKnownHashes(t *testing.T) {
	planHash := hex.EncodeToString(bytesFill(32, 0x11))
	bundleHash := hex.EncodeToString(bytesFill(32, 0x22))
	base := pilotStartEvent{EventType: "terminal_failed", PlanHMAC: &planHash, ResultClass: optionalPilotString("planner_error")}
	if err := (&pilotStartSidecar{planHMAC: planHash}).validateContinuity(base); err != nil {
		t.Fatalf("same plan HMAC should be accepted: %v", err)
	}
	base.PlanHMAC = nil
	if err := (&pilotStartSidecar{planHMAC: planHash}).validateContinuity(base); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("terminal event missing plan HMAC should fail: %v", err)
	}
	base.PlanHMAC = &planHash
	if err := (&pilotStartSidecar{planHMAC: planHash, bundleHMAC: bundleHash}).validateContinuity(base); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("terminal event missing bundle HMAC should fail: %v", err)
	}
	base.BundleHMAC = &bundleHash
	if err := (&pilotStartSidecar{planHMAC: planHash, bundleHMAC: bundleHash}).validateContinuity(base); err != nil {
		t.Fatalf("same plan and bundle HMAC should be accepted: %v", err)
	}
}

func TestPilotStartSidecarContinuityRejectsEveryIdentityHashMismatch(t *testing.T) {
	key := hex.EncodeToString(bytesFill(32, 0x81))
	cmd := hex.EncodeToString(bytesFill(32, 0x82))
	plan := hex.EncodeToString(bytesFill(32, 0x83))
	bundle := hex.EncodeToString(bytesFill(32, 0x84))
	other := hex.EncodeToString(bytesFill(32, 0x85))
	baseState := &pilotStartSidecar{attemptID: "attempt", commandKeyHMAC: key, commandHMAC: cmd, planHMAC: plan, bundleHMAC: bundle}
	baseEvent := pilotStartEvent{AttemptID: "attempt", CommandKeyHMAC: key, CommandHMAC: cmd, PlanHMAC: &plan, BundleHMAC: &bundle}
	for _, tc := range []struct {
		name  string
		state *pilotStartSidecar
		event pilotStartEvent
		ok    bool
	}{
		{"matching identity and hashes", baseState, baseEvent, true},
		{"empty state initializes", &pilotStartSidecar{}, baseEvent, true},
		{"attempt id mismatch", baseState, func() pilotStartEvent { e := baseEvent; e.AttemptID = "other"; return e }(), false},
		{"command key mismatch", baseState, func() pilotStartEvent { e := baseEvent; e.CommandKeyHMAC = other; return e }(), false},
		{"command mismatch", baseState, func() pilotStartEvent { e := baseEvent; e.CommandHMAC = other; return e }(), false},
		{"plan mismatch", baseState, func() pilotStartEvent { e := baseEvent; e.PlanHMAC = &other; return e }(), false},
		{"plan absent", baseState, func() pilotStartEvent { e := baseEvent; e.PlanHMAC = nil; return e }(), false},
		{"bundle mismatch", baseState, func() pilotStartEvent { e := baseEvent; e.BundleHMAC = &other; return e }(), false},
		{"bundle absent", baseState, func() pilotStartEvent { e := baseEvent; e.BundleHMAC = nil; return e }(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.state.validateContinuity(tc.event)
			if (err == nil) != tc.ok {
				t.Fatalf("continuity err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestPilotStartSidecarReplayRejectsMalformedRecordsAndOversizedLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"invalid JSON", []byte("{\n")},
		{"duplicate key", []byte("{\"schema_version\":1,\"schema_version\":1}\n")},
		{"unknown field", []byte("{\"unknown\":1}\n")},
		{"trailing JSON value", []byte("{} {}\n")},
		{"noncanonical JSON", []byte("{ }\n")},
		{"oversized line", append(bytesFill(8192, 'x'), '\n')},
		{"unterminated line", []byte("{}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "malformed.jsonl")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			s := &pilotStartSidecar{file: f, key: bytesFill(32, 0x91)}
			if err := s.replay(); !errors.Is(err, errPilotStartSidecar) {
				t.Fatalf("malformed sidecar accepted: %v", err)
			}
			_ = f.Close()
		})
	}
}

func TestPilotStartSidecarFullAttemptAndRollbackRetryReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full-attempt.jsonl")
	key := bytesFill(32, 0x6d)
	s, err := createPilotStartSidecar(path, key)
	if err != nil {
		t.Fatal(err)
	}
	planHash := hex.EncodeToString(bytesFill(32, 0x31))
	bundleHash := hex.EncodeToString(bytesFill(32, 0x32))
	base := pilotStartEvent{
		CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x33)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x34)),
		AttemptID: hex.EncodeToString(bytesFill(16, 0x35)), ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
		TaskHMAC: hex.EncodeToString(bytesFill(32, 0x36)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x37)), EventTimeMS: 1700000000000,
	}
	events := []pilotStartEvent{
		{EventType: "attempt_started"},
		{EventType: "plan_ready", PlanHMAC: &planHash, TemplateID: optionalPilotString("OP-1"), Source: optionalPilotString("template")},
		{EventType: "tool_started", PlanHMAC: &planHash, ToolName: optionalPilotString("classroom.say")},
		{EventType: "tool_result", PlanHMAC: &planHash, ToolName: optionalPilotString("classroom.say"), ResultClass: optionalPilotString("ok")},
		{EventType: "tool_started", PlanHMAC: &planHash, ToolName: optionalPilotString("classroom.show_word")},
		{EventType: "tool_result", PlanHMAC: &planHash, ToolName: optionalPilotString("classroom.show_word"), ResultClass: optionalPilotString("ok")},
		{EventType: "bundle_ready", PlanHMAC: &planHash, BundleHMAC: &bundleHash},
		{EventType: "commit_attempt_started", AttemptNo: 1, PlanHMAC: &planHash, BundleHMAC: &bundleHash},
		{EventType: "rollback_proven", AttemptNo: 1, PlanHMAC: &planHash, BundleHMAC: &bundleHash, ResultClass: optionalPilotString("rollback_proven")},
		{EventType: "commit_attempt_started", AttemptNo: 2, PlanHMAC: &planHash, BundleHMAC: &bundleHash},
		{EventType: "terminal_failed", AttemptNo: 2, PlanHMAC: &planHash, BundleHMAC: &bundleHash, ResultClass: optionalPilotString("write_error")},
	}
	for _, event := range events {
		event.CommandKeyHMAC, event.CommandHMAC, event.AttemptID = base.CommandKeyHMAC, base.CommandHMAC, base.AttemptID
		event.ActorRole, event.Trigger, event.Scope, event.Risk = base.ActorRole, base.Trigger, base.Scope, base.Risk
		event.TaskHMAC, event.TraceHMAC, event.EventTimeMS = base.TaskHMAC, base.TraceHMAC, base.EventTimeMS
		if err := s.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.EventType, err)
		}
	}
	if s.phase != 9 || s.seq != int64(len(events)) {
		t.Fatalf("unexpected terminal state phase=%d seq=%d", s.phase, s.seq)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openPilotStartSidecar(path, key)
	if err != nil {
		t.Fatalf("replay valid full attempt chain: %v", err)
	}
	defer reopened.Close()
	if reopened.phase != 9 || reopened.seq != int64(len(events)) || reopened.planHMAC != planHash || reopened.bundleHMAC != bundleHash {
		t.Fatalf("recovered wrong terminal state: phase=%d seq=%d", reopened.phase, reopened.seq)
	}
}

func TestPilotStartSidecarRejectsAppendBeyondReplayLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full.jsonl")
	s, err := createPilotStartSidecar(path, bytesFill(32, 0x4c))
	if err != nil {
		t.Fatal(err)
	}
	s.sizeLimit = 0
	event := pilotStartEvent{
		EventType: "attempt_started", AttemptNo: 0,
		CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x01)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x02)),
		AttemptID: hex.EncodeToString(bytesFill(16, 0x03)), ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
		TaskHMAC: hex.EncodeToString(bytesFill(32, 0x04)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x05)), EventTimeMS: 1700000000000,
	}
	if err := s.Append(event); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("append beyond replay limit should fail: %v", err)
	}
	info, err := s.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("failed append changed file size: %d", info.Size())
	}
	if !s.degraded {
		t.Fatal("sidecar should fail closed after reaching its configured size limit")
	}
	_ = s.Close()
}

func TestPilotStartSidecarWriteAndSyncFaultsFailClosed(t *testing.T) {
	for _, mode := range []string{"zero", "partial", "sync"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.jsonl")
			s, err := createPilotStartSidecar(path, bytesFill(32, 0x55))
			if err != nil {
				t.Fatal(err)
			}
			f, ok := s.file.(*os.File)
			if !ok {
				t.Fatalf("unexpected underlying file type: %T", s.file)
			}
			fault := &pilotSidecarFaultFile{File: f}
			if mode == "sync" {
				fault.syncErr = errors.New("injected sync failure")
			} else {
				fault.writeMode = mode
			}
			s.file = fault
			event := pilotStartEvent{
				EventType: "attempt_started", AttemptNo: 0,
				CommandKeyHMAC: hex.EncodeToString(bytesFill(32, 0x01)), CommandHMAC: hex.EncodeToString(bytesFill(32, 0x02)),
				AttemptID: hex.EncodeToString(bytesFill(16, 0x03)), ActorRole: "teacher_agent", Trigger: "classroom_event", Scope: "teacher_agent", Risk: "low",
				TaskHMAC: hex.EncodeToString(bytesFill(32, 0x04)), TraceHMAC: hex.EncodeToString(bytesFill(32, 0x05)), EventTimeMS: 1700000000000,
			}
			if err := s.Append(event); !errors.Is(err, errPilotStartSidecar) {
				t.Fatalf("injected %s failure should fail append: %v", mode, err)
			}
			if !s.degraded || s.seq != 0 {
				t.Fatalf("uncertain write did not fail closed: degraded=%v seq=%d", s.degraded, s.seq)
			}
			if err := s.Append(event); !errors.Is(err, errPilotStartSidecar) {
				t.Fatalf("degraded writer accepted retry: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPilotStartSidecarParentSyncFailureFailsCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent-sync.jsonl")
	s, err := createPilotStartSidecarWithParentSync(path, bytesFill(32, 0x56), func(string) error {
		return errors.New("injected parent sync failure")
	})
	if s != nil || !errors.Is(err, errPilotStartSidecar) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("parent sync failure must reject creation: sidecar=%v err=%v", s, err)
	}
	if _, err := openPilotStartSidecar(path, bytesFill(32, 0x56)); !errors.Is(err, errPilotStartSidecar) {
		t.Fatalf("incomplete new sidecar must fail recovery: %v", err)
	}
}

func bytesFill(n int, value byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = value
	}
	return out
}

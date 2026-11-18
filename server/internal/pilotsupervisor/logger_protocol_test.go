//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var runHostLoggerPlatformTests func(*testing.T)

func testHostReaperEvent() hostReaperEvent {
	return hostReaperEvent{
		TraceID: strings.Repeat("1", 32), ProcessID: strings.Repeat("2", 32),
		OperationID: strings.Repeat("3", 32), Module: "pilot_host_reaper",
		Event: "run_root_dispatch_intent", LockWaitMS: 0, IOMS: 4,
		Outcome: "ok", Reason: "none",
	}
}

func TestRunRootBootstrap(t *testing.T) {
	t.Run("logger_event_process_id_sha256_vector", func(t *testing.T) {
		got := hostLoggerEventProcessID(strings.Repeat("0", 32), 1)
		const want = "9b283a5c8362f1e694ae1101e0a022b8"
		if got != want {
			t.Fatalf("event process ID vector mismatch: got %s want %s", got, want)
		}
		if hostLoggerEventProcessID(strings.Repeat("0", 31), 1) != "" || hostLoggerEventProcessID(strings.Repeat("0", 32), 0) != "" {
			t.Fatal("invalid event process ID input accepted")
		}
	})

	t.Run("logger_channel_ready_golden_and_reject_matrix", func(t *testing.T) {
		contextID := strings.Repeat("0", 32)
		frame, err := encodeHostLoggerChannelReady(contextID, 1, "ok")
		if err != nil {
			t.Fatal(err)
		}
		const golden = `{"version":1,"type":"LOGGER_CHANNEL_READY","context_id":"00000000000000000000000000000000","control_generation":1,"result":"ok"}`
		if string(frame) != golden || validateHostLoggerChannelReady(frame, contextID, 1) != nil {
			t.Fatalf("readiness wire vector mismatch: %s", frame)
		}
		assertLoggerJSONKeyOrder(t, frame, "version", "type", "context_id", "control_generation", "result")
		for name, bad := range map[string][]byte{
			"duplicate":        []byte(strings.Replace(golden, `,"result":"ok"`, `,"result":"ok","result":"ok"`, 1)),
			"unknown":          []byte(strings.Replace(golden, `,"result":"ok"`, `,"extra":0,"result":"ok"`, 1)),
			"wrong_version":    []byte(strings.Replace(golden, `"version":1`, `"version":2`, 1)),
			"wrong_type":       []byte(strings.Replace(golden, `LOGGER_CHANNEL_READY`, `LOGGER_REGISTER_ACK`, 1)),
			"wrong_context":    []byte(strings.Replace(golden, contextID, strings.Repeat("1", 32), 1)),
			"wrong_generation": []byte(strings.Replace(golden, `"control_generation":1`, `"control_generation":2`, 1)),
			"restart_required": []byte(strings.Replace(golden, `"result":"ok"`, `"result":"restart_required"`, 1)),
			"trailing":         append([]byte(golden), ' '),
		} {
			t.Run(name, func(t *testing.T) {
				if validateHostLoggerChannelReady(bad, contextID, 1) == nil {
					t.Fatalf("invalid readiness accepted: %s", bad)
				}
			})
		}
		if _, err := encodeHostLoggerChannelReady(contextID, 1, "rejected"); err == nil {
			t.Fatal("unsupported readiness result encoded")
		}
	})

	t.Run("logger_runroot_event_allowlist", func(t *testing.T) {
		for _, name := range []string{"bootstrap.start", "bootstrap.finish", "barrier", "run_root_dispatch_intent", "run_root_dispatch_committed"} {
			event := testHostReaperEvent()
			event.Event = name
			if name == "barrier" {
				barrier := "after_prepared"
				event.BarrierName = &barrier
			}
			if _, _, err := encodeHostLoggerEnvelope(event); err != nil {
				t.Fatalf("allowlisted event %q rejected: %v", name, err)
			}
		}
	})

	t.Run("logger_unknown_event_reject", func(t *testing.T) {
		unknown := testHostReaperEvent()
		unknown.Event = "run_root_dispatch_unknown"
		if _, _, err := encodeHostLoggerEnvelope(unknown); err == nil {
			t.Fatal("unknown run-root event accepted")
		}
	})

	t.Run("logger_ack_envelope_and_exact_ack_golden", func(t *testing.T) {
		event := testHostReaperEvent()
		frame, expected, err := encodeHostLoggerEnvelope(event)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(frame, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope) != 4 || !bytes.HasPrefix(frame, []byte(`{"version":1,"delivery_nonce":"`)) {
			t.Fatalf("noncanonical envelope fields/order: %s", frame)
		}
		assertLoggerJSONKeyOrder(t, frame, "version", "delivery_nonce", "event_sha256", "event")
		gotEvent, gotACK, err := decodeHostLoggerEnvelope(frame)
		if err != nil || gotEvent != event || gotACK != expected {
			t.Fatalf("decode envelope: event=%+v ack=%+v err=%v", gotEvent, gotACK, err)
		}
		canonicalEvent, _ := json.Marshal(event)
		digest := sha256.Sum256(canonicalEvent)
		if expected.EventSHA256 != hex.EncodeToString(digest[:]) || !hostLogHex32.MatchString(expected.DeliveryNonce) {
			t.Fatalf("bad nonce/digest: %+v", expected)
		}
		ackFrame, err := encodeHostLoggerACK(hostLoggerACK{
			Version: 1, TraceID: expected.TraceID, ProcessID: expected.ProcessID,
			OperationID: expected.OperationID, Event: expected.Event,
			DeliveryNonce: expected.DeliveryNonce, EventSHA256: expected.EventSHA256,
			Status: "accepted",
		})
		if err != nil || validateHostLoggerACK(ackFrame, expected) != nil {
			t.Fatalf("matching ACK rejected: %s err=%v", ackFrame, err)
		}
		assertLoggerJSONKeyOrder(t, ackFrame, "version", "trace_id", "process_id", "operation_id", "event", "delivery_nonce", "event_sha256", "status")
		_, second, err := encodeHostLoggerEnvelope(event)
		if err != nil || second.DeliveryNonce == expected.DeliveryNonce {
			t.Fatalf("delivery nonce reused: first=%s second=%s err=%v", expected.DeliveryNonce, second.DeliveryNonce, err)
		}
	})

	t.Run("logger_ack_stale_same_tuple_nonce", func(t *testing.T) {
		_, first, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		_, second, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		oldACK, _ := encodeHostLoggerACK(hostLoggerACK{Version: 1, TraceID: first.TraceID, ProcessID: first.ProcessID,
			OperationID: first.OperationID, Event: first.Event, DeliveryNonce: first.DeliveryNonce,
			EventSHA256: first.EventSHA256, Status: "accepted"})
		if validateHostLoggerACK(oldACK, second) == nil {
			t.Fatal("stale ACK with matching event tuple was accepted for a fresh delivery")
		}
	})

	t.Run("logger_ack_event_digest_mismatch", func(t *testing.T) {
		_, expected, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		bad := expected
		bad.EventSHA256 = strings.Repeat("f", 64)
		frame, _ := encodeHostLoggerACK(hostLoggerACK{Version: 1, TraceID: bad.TraceID, ProcessID: bad.ProcessID,
			OperationID: bad.OperationID, Event: bad.Event, DeliveryNonce: bad.DeliveryNonce,
			EventSHA256: bad.EventSHA256, Status: "accepted"})
		if validateHostLoggerACK(frame, expected) == nil {
			t.Fatal("ACK with wrong event digest was accepted")
		}
		eventFrame, _, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		tampered := bytes.Replace(eventFrame, []byte("run_root_dispatch_intent"), []byte("run_root_dispatch_commit"), 1)
		if _, _, err := decodeHostLoggerEnvelope(tampered); err == nil {
			t.Fatal("envelope with tampered event digest accepted")
		}
	})

	t.Run("logger_ack_strict_parser_reject_matrix", func(t *testing.T) {
		_, expected, err := encodeHostLoggerEnvelope(testHostReaperEvent())
		if err != nil {
			t.Fatal(err)
		}
		valid, err := encodeHostLoggerACK(hostLoggerACK{Version: 1, TraceID: expected.TraceID,
			ProcessID: expected.ProcessID, OperationID: expected.OperationID, Event: expected.Event,
			DeliveryNonce: expected.DeliveryNonce, EventSHA256: expected.EventSHA256, Status: "accepted"})
		if err != nil {
			t.Fatal(err)
		}
		cases := map[string][]byte{
			"missing":         bytes.Replace(valid, []byte(`,"status":"accepted"`), nil, 1),
			"duplicate":       bytes.Replace(valid, []byte(`,"status":"accepted"`), []byte(`,"status":"accepted","status":"accepted"`), 1),
			"unknown":         bytes.Replace(valid, []byte(`,"status":"accepted"`), []byte(`,"extra":0,"status":"accepted"`), 1),
			"reordered":       bytes.Replace(valid, []byte(`{"version":1,"trace_id":"`+expected.TraceID+`",`), []byte(`{"trace_id":"`+expected.TraceID+`","version":1,`), 1),
			"wrong_version":   bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":2`), 1),
			"invalid_utf8":    append(append([]byte(nil), valid...), 0xff),
			"trailing_bytes":  append(append([]byte(nil), valid...), ' '),
			"length_prefix":   append([]byte{0, 0, 0, byte(len(valid))}, valid...),
			"bad_nonce":       bytes.Replace(valid, []byte(expected.DeliveryNonce), []byte("BAD"), 1),
			"bad_digest":      bytes.Replace(valid, []byte(expected.EventSHA256), []byte("BAD"), 1),
			"unknown_status":  bytes.Replace(valid, []byte(`"accepted"`), []byte(`"maybe"`), 1),
			"rejected_status": bytes.Replace(valid, []byte(`"accepted"`), []byte(`"rejected"`), 1),
		}
		for name, frame := range cases {
			t.Run(name, func(t *testing.T) {
				if validateHostLoggerACK(frame, expected) == nil {
					t.Fatalf("malformed ACK accepted: %s", frame)
				}
			})
		}
	})

	if runHostLoggerPlatformTests != nil {
		runHostLoggerPlatformTests(t)
	}
}

func TestHostLoggerEncoderAndEventInvariantRejections(t *testing.T) {
	valid := testHostReaperEvent()
	badBarrier := "not-a-barrier"
	validCases := []struct {
		name string
		edit func(*hostReaperEvent)
	}{
		{name: "unknown reason", edit: func(e *hostReaperEvent) { e.Outcome = "error"; e.Reason = "not-allowlisted" }},
		{name: "reason none with error", edit: func(e *hostReaperEvent) { e.Outcome = "error" }},
		{name: "reason required on blocked outcome", edit: func(e *hostReaperEvent) { e.Outcome = "blocked"; e.Reason = "none" }},
		{name: "case digest forbidden", edit: func(e *hostReaperEvent) { value := strings.Repeat("a", 64); e.CaseHash = &value }},
		{name: "state fields forbidden", edit: func(e *hostReaperEvent) { value := "prepared"; e.StateBefore = &value }},
		{name: "barrier name required", edit: func(e *hostReaperEvent) { e.Event = "barrier" }},
		{name: "barrier name allowlist", edit: func(e *hostReaperEvent) { e.Event = "barrier"; e.BarrierName = &badBarrier }},
		{name: "non-barrier cannot carry barrier name", edit: func(e *hostReaperEvent) { e.BarrierName = &badBarrier }},
	}
	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			event := valid
			tc.edit(&event)
			if _, _, err := encodeHostLoggerEnvelope(event); !errors.Is(err, errHostLoggerProtocol) {
				t.Fatalf("invalid event was encoded: err=%v event=%+v", err, event)
			}
		})
	}
	if validHostLogReason("unlisted_reason") || validHostLogBarrier("unlisted_barrier") {
		t.Fatal("unknown reason/barrier was allowlisted")
	}
}

func TestEncodeHostLoggerACKRejectsInvalidClaims(t *testing.T) {
	valid := hostLoggerACK{Version: 1, TraceID: strings.Repeat("1", 32), ProcessID: strings.Repeat("2", 32),
		OperationID: strings.Repeat("3", 32), Event: "run_root_dispatch_intent", DeliveryNonce: strings.Repeat("4", 32),
		EventSHA256: strings.Repeat("5", 64), Status: "accepted"}
	cases := []struct {
		name string
		edit func(*hostLoggerACK)
	}{
		{name: "version", edit: func(a *hostLoggerACK) { a.Version = 2 }},
		{name: "trace id", edit: func(a *hostLoggerACK) { a.TraceID = "bad" }},
		{name: "process id", edit: func(a *hostLoggerACK) { a.ProcessID = "bad" }},
		{name: "operation id", edit: func(a *hostLoggerACK) { a.OperationID = "bad" }},
		{name: "event", edit: func(a *hostLoggerACK) { a.Event = "unknown" }},
		{name: "nonce", edit: func(a *hostLoggerACK) { a.DeliveryNonce = "bad" }},
		{name: "digest", edit: func(a *hostLoggerACK) { a.EventSHA256 = "bad" }},
		{name: "status", edit: func(a *hostLoggerACK) { a.Status = "unknown" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ack := valid
			tc.edit(&ack)
			if _, err := encodeHostLoggerACK(ack); !errors.Is(err, errHostLoggerProtocol) {
				t.Fatalf("invalid acknowledgement encoded: %+v err=%v", ack, err)
			}
		})
	}
}

func assertLoggerJSONKeyOrder(t *testing.T, frame []byte, keys ...string) {
	t.Helper()
	last := -1
	for _, key := range keys {
		index := bytes.Index(frame, []byte(`"`+key+`":`))
		if index <= last {
			t.Fatalf("JSON key %q missing or out of order in %s", key, frame)
		}
		last = index
	}
}

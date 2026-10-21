//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const hostLoggerMaxRecord = 16 << 10

var (
	errHostLoggerProtocol = errors.New("pilot host logger protocol rejected")
	hostLogHex32          = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hostLogHex64          = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// hostReaperEvent has the fixed docs/42 JSON field order. Pointer fields
// preserve the required JSON null values; BarrierName is the sole optional
// field and is present only for barrier events.
type hostReaperEvent struct {
	TraceID     string  `json:"trace_id"`
	ProcessID   string  `json:"process_id"`
	OperationID string  `json:"operation_id"`
	Module      string  `json:"module"`
	Event       string  `json:"event"`
	CaseHash    *string `json:"case_hash"`
	StateBefore *string `json:"state_before"`
	StateAfter  *string `json:"state_after"`
	LockWaitMS  uint64  `json:"lock_wait_ms"`
	IOMS        uint64  `json:"io_ms"`
	Outcome     string  `json:"outcome"`
	Reason      string  `json:"reason"`
	BarrierName *string `json:"barrier_name,omitempty"`
}

type hostLoggerEnvelope struct {
	Version       int             `json:"version"`
	DeliveryNonce string          `json:"delivery_nonce"`
	EventSHA256   string          `json:"event_sha256"`
	Event         json.RawMessage `json:"event"`
}

type hostLoggerACK struct {
	Version       int    `json:"version"`
	TraceID       string `json:"trace_id"`
	ProcessID     string `json:"process_id"`
	OperationID   string `json:"operation_id"`
	Event         string `json:"event"`
	DeliveryNonce string `json:"delivery_nonce"`
	EventSHA256   string `json:"event_sha256"`
	Status        string `json:"status"`
}

// hostLoggerChannelReady is the first data-channel frame. It is sent only
// after Logger has authenticated SO_PEERCRED and enabled SO_PASSCRED on the
// accepted socket, closing the connect/first-packet credential race.
type hostLoggerChannelReady struct {
	Version           int    `json:"version"`
	Type              string `json:"type"`
	ContextID         string `json:"context_id"`
	ControlGeneration uint64 `json:"control_generation"`
	Result            string `json:"result"`
}

func encodeHostLoggerChannelReady(contextID string, generation uint64, result string) ([]byte, error) {
	ready := hostLoggerChannelReady{Version: 1, Type: "LOGGER_CHANNEL_READY", ContextID: contextID,
		ControlGeneration: generation, Result: result}
	if !hostLogHex32.MatchString(contextID) || generation == 0 ||
		(result != "ok" && result != "restart_required") {
		return nil, errHostLoggerProtocol
	}
	frame, err := json.Marshal(ready)
	if err != nil || len(frame) == 0 || len(frame) > 1024 {
		return nil, errHostLoggerProtocol
	}
	return frame, nil
}

func validateHostLoggerChannelReady(frame []byte, contextID string, generation uint64) error {
	var got hostLoggerChannelReady
	if len(frame) == 0 || len(frame) > 1024 || !hostLogHex32.MatchString(contextID) || generation == 0 ||
		decodeCanonicalJSON(frame, &got) != nil || got.Version != 1 || got.Type != "LOGGER_CHANNEL_READY" ||
		got.ContextID != contextID || got.ControlGeneration != generation || got.Result != "ok" {
		return errHostLoggerProtocol
	}
	return nil
}

func hostLoggerEventProcessID(runEpoch string, generation uint64) string {
	epoch, err := hex.DecodeString(runEpoch)
	if err != nil || len(epoch) != 16 || generation == 0 {
		return ""
	}
	input := append([]byte("en-words-pilot-log-process-v1"), 0)
	input = append(input, epoch...)
	var counter [8]byte
	for i := 7; i >= 0; i-- {
		counter[i] = byte(generation)
		generation >>= 8
	}
	input = append(input, counter[:]...)
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:16])
}

func encodeHostLoggerEnvelope(event hostReaperEvent) ([]byte, hostLoggerACK, error) {
	if validateHostReaperEvent(event) != nil {
		return nil, hostLoggerACK{}, errHostLoggerProtocol
	}
	eventBytes, err := json.Marshal(event)
	if err != nil || len(eventBytes) == 0 || len(eventBytes) > hostLoggerMaxRecord {
		return nil, hostLoggerACK{}, errHostLoggerProtocol
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, hostLoggerACK{}, errHostLoggerProtocol
	}
	ack := hostLoggerACK{
		Version: 1, TraceID: event.TraceID, ProcessID: event.ProcessID,
		OperationID: event.OperationID, Event: event.Event,
		DeliveryNonce: hex.EncodeToString(nonce[:]),
		EventSHA256:   hex.EncodeToString(sha256Sum(eventBytes)),
	}
	envelope := hostLoggerEnvelope{Version: 1, DeliveryNonce: ack.DeliveryNonce, EventSHA256: ack.EventSHA256, Event: eventBytes}
	frame, err := json.Marshal(envelope)
	if err != nil || len(frame) == 0 || len(frame) > hostLoggerMaxRecord {
		return nil, hostLoggerACK{}, errHostLoggerProtocol
	}
	return frame, ack, nil
}

func decodeHostLoggerEnvelope(frame []byte) (hostReaperEvent, hostLoggerACK, error) {
	var envelope hostLoggerEnvelope
	if decodeCanonicalJSON(frame, &envelope) != nil || envelope.Version != 1 ||
		!hostLogHex32.MatchString(envelope.DeliveryNonce) || !hostLogHex64.MatchString(envelope.EventSHA256) ||
		len(envelope.Event) == 0 || len(envelope.Event) > hostLoggerMaxRecord ||
		!bytes.Equal(bytes.TrimSpace(envelope.Event), envelope.Event) {
		return hostReaperEvent{}, hostLoggerACK{}, errHostLoggerProtocol
	}
	var event hostReaperEvent
	if decodeCanonicalJSON(envelope.Event, &event) != nil || validateHostReaperEvent(event) != nil ||
		hex.EncodeToString(sha256Sum(envelope.Event)) != envelope.EventSHA256 {
		return hostReaperEvent{}, hostLoggerACK{}, errHostLoggerProtocol
	}
	ack := hostLoggerACK{Version: 1, TraceID: event.TraceID, ProcessID: event.ProcessID,
		OperationID: event.OperationID, Event: event.Event, DeliveryNonce: envelope.DeliveryNonce,
		EventSHA256: envelope.EventSHA256}
	return event, ack, nil
}

func encodeHostLoggerACK(ack hostLoggerACK) ([]byte, error) {
	if ack.Version != 1 || !hostLogHex32.MatchString(ack.TraceID) || !hostLogHex32.MatchString(ack.ProcessID) ||
		!hostLogHex32.MatchString(ack.OperationID) || !validHostLogEventName(ack.Event) ||
		!hostLogHex32.MatchString(ack.DeliveryNonce) || !hostLogHex64.MatchString(ack.EventSHA256) ||
		(ack.Status != "accepted" && ack.Status != "rejected") {
		return nil, errHostLoggerProtocol
	}
	frame, err := json.Marshal(ack)
	if err != nil || len(frame) == 0 || len(frame) > hostLoggerMaxRecord {
		return nil, errHostLoggerProtocol
	}
	return frame, nil
}

func validateHostLoggerACK(frame []byte, expected hostLoggerACK) error {
	var got hostLoggerACK
	if decodeCanonicalJSON(frame, &got) != nil || got.Version != 1 ||
		got.TraceID != expected.TraceID || got.ProcessID != expected.ProcessID ||
		got.OperationID != expected.OperationID || got.Event != expected.Event ||
		got.DeliveryNonce != expected.DeliveryNonce || got.EventSHA256 != expected.EventSHA256 ||
		(got.Status != "accepted" && got.Status != "rejected") || got.Status != "accepted" {
		return errHostLoggerProtocol
	}
	return nil
}

func decodeCanonicalJSON(frame []byte, dst any) error {
	if len(frame) == 0 || len(frame) > hostLoggerMaxRecord || !json.Valid(frame) {
		return errHostLoggerProtocol
	}
	d := json.NewDecoder(bytes.NewReader(frame))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errHostLoggerProtocol
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errHostLoggerProtocol
	}
	canonical, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(canonical, frame) {
		return errHostLoggerProtocol
	}
	return nil
}

func validateHostReaperEvent(e hostReaperEvent) error {
	if !hostLogHex32.MatchString(e.TraceID) || !hostLogHex32.MatchString(e.ProcessID) ||
		!hostLogHex32.MatchString(e.OperationID) || e.Module != "pilot_host_reaper" ||
		!validHostLogEventName(e.Event) || !validHostLogOutcome(e.Outcome) || !validHostLogReason(e.Reason) {
		return errHostLoggerProtocol
	}
	if e.CaseHash != nil || e.StateBefore != nil || e.StateAfter != nil {
		return errHostLoggerProtocol
	}
	if e.Event == "barrier" {
		if e.BarrierName == nil || !validHostLogBarrier(*e.BarrierName) {
			return errHostLoggerProtocol
		}
	} else if e.BarrierName != nil {
		return errHostLoggerProtocol
	}
	if e.Outcome == "ok" && e.Reason != "none" || e.Outcome != "ok" && e.Reason == "none" {
		return errHostLoggerProtocol
	}
	return nil
}

func validHostLogEventName(v string) bool {
	switch v {
	case "bootstrap.start", "bootstrap.finish", "barrier", "run_root_dispatch_intent", "run_root_dispatch_committed":
		return true
	default:
		return false
	}
}

func validHostLogOutcome(v string) bool {
	return v == "ok" || v == "error" || v == "indeterminate" || v == "blocked"
}

func validHostLogReason(v string) bool {
	if v == "none" {
		return true
	}
	switch v {
	case "ipc_bootstrap_invalid", "ipc_cancelled", "ipc_launcher_failed", "ipc_listener_failed", "ipc_operation_abandoned", "ipc_parent_gone", "ipc_peer_rejected", "ipc_registry_corrupt", "ipc_registry_full", "ipc_request_conflict", "ipc_result_indeterminate", "ipc_timeout", "operation_canceled", "io_uncertain", "restart_required", "verified_anchor_invalid", "verified_close_unknown", "verified_configuration_missing", "verified_identity_changed", "verified_io_failed", "verified_member_invalid", "verified_path_invalid", "verified_policy_mismatch", "verified_timeout":
		return true
	default:
		return false
	}
}

func validHostLogBarrier(v string) bool {
	switch v {
	case "after_prepared", "after_executing", "after_mkdirat", "after_fchmod_fchown", "after_child_fsync", "after_anchor_fsync", "before_terminal_commit", "after_terminal_commit_before_finish_ack":
		return true
	default:
		return false
	}
}

func sha256Sum(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

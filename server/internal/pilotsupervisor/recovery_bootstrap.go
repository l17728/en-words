//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

const recoveryBootstrapMaxPacket = 768

var errRecoveryBootstrapInvalid = errors.New("pilot recovery bootstrap invalid")

type recoveryTargetIdentity struct {
	Generation  uint64
	LaunchNonce string
	RunEpoch    string
}

type recoveryTargetSnapshot struct {
	Identity        recoveryTargetIdentity
	Found           bool
	Terminal        bool
	RunState        string
	EvidencePresent bool
	// EvidenceRows is the exact durable row count for this target; only one
	// immutable matching row can authorize terminal finalization.
	EvidenceRows            int
	EvidenceMatches         bool
	EvidenceDigest          string
	OperationsTerminal      bool
	RequiredReceiptsPresent bool
	RequiredReceiptsMatch   bool
	CleanupState            string
}

// recoveryBootstrapLedger methods are durable transaction boundaries and must
// honor ctx cancellation while holding database/ledger transactions. A
// false result from ConsumeRecoveryControl means the generation is stale and
// was not consumed. FinalizeRunTerminal must recheck every predicate in one
// transaction; false means no target-ledger write occurred. An error is an
// uncertain persistence outcome and forces the caller to restart the Reaper.
type recoveryBootstrapLedger interface {
	ConsumeRecoveryControl(context.Context, uint64, string) (bool, error)
	LoadRecoveryTarget(context.Context, recoveryTargetIdentity) (recoveryTargetSnapshot, error)
	FinalizeRunTerminal(context.Context, recoveryTargetIdentity, string) (bool, error)
}

type recoveryBootstrapRequest struct {
	Version           uint8  `json:"version"`
	Type              string `json:"type"`
	Generation        uint64 `json:"generation"`
	LaunchNonce       string `json:"launch_nonce"`
	TargetGeneration  uint64 `json:"target_generation"`
	TargetLaunchNonce string `json:"target_launch_nonce"`
	TargetRunEpoch    string `json:"target_run_epoch"`
}

type recoveryBootstrapAck struct {
	Version     uint8  `json:"version"`
	Type        string `json:"type"`
	Generation  uint64 `json:"generation"`
	LaunchNonce string `json:"launch_nonce"`
	Result      string `json:"result"`
}

func encodeRecoveryBootstrap(request recoveryBootstrapRequest) ([]byte, error) {
	if !validRecoveryBootstrap(request) {
		return nil, errRecoveryBootstrapInvalid
	}
	packet, err := json.Marshal(request)
	if err != nil || len(packet) > recoveryBootstrapMaxPacket {
		return nil, errRecoveryBootstrapInvalid
	}
	return packet, nil
}

func decodeRecoveryBootstrap(packet []byte) (recoveryBootstrapRequest, error) {
	var request recoveryBootstrapRequest
	if len(packet) == 0 || len(packet) > recoveryBootstrapMaxPacket {
		return request, errRecoveryBootstrapInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(packet))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return recoveryBootstrapRequest{}, errRecoveryBootstrapInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return recoveryBootstrapRequest{}, errRecoveryBootstrapInvalid
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, packet) || !validRecoveryBootstrap(request) {
		return recoveryBootstrapRequest{}, errRecoveryBootstrapInvalid
	}
	return request, nil
}

func encodeRecoveryBootstrapAck(ack recoveryBootstrapAck) ([]byte, error) {
	if !validRecoveryBootstrapAck(ack) {
		return nil, errRecoveryBootstrapInvalid
	}
	packet, err := json.Marshal(ack)
	if err != nil || len(packet) > recoveryBootstrapMaxPacket {
		return nil, errRecoveryBootstrapInvalid
	}
	return packet, nil
}

func decodeRecoveryBootstrapAck(packet []byte) (recoveryBootstrapAck, error) {
	var ack recoveryBootstrapAck
	if len(packet) == 0 || len(packet) > recoveryBootstrapMaxPacket {
		return ack, errRecoveryBootstrapInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(packet))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ack) != nil {
		return recoveryBootstrapAck{}, errRecoveryBootstrapInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return recoveryBootstrapAck{}, errRecoveryBootstrapInvalid
	}
	canonical, err := json.Marshal(ack)
	if err != nil || !bytes.Equal(canonical, packet) || !validRecoveryBootstrapAck(ack) {
		return recoveryBootstrapAck{}, errRecoveryBootstrapInvalid
	}
	return ack, nil
}

func validRecoveryBootstrap(request recoveryBootstrapRequest) bool {
	return request.Version == 1 && request.Type == "RECOVERY_BOOTSTRAP" &&
		request.Generation > 0 && isLowerHexString(request.LaunchNonce, 32) &&
		request.TargetGeneration > 0 &&
		isLowerHexString(request.TargetLaunchNonce, 32) && isLowerHexString(request.TargetRunEpoch, 32)
}

func validRecoveryBootstrapAck(ack recoveryBootstrapAck) bool {
	if ack.Version != 1 || ack.Type != "RECOVERY_BOOTSTRAP_ACK" || ack.Generation == 0 || !isLowerHexString(ack.LaunchNonce, 32) {
		return false
	}
	switch ack.Result {
	case "already_terminal", "finalized", "recovery_proof_missing", "recovery_not_ready", "rejected", "restart_required":
		return true
	default:
		return false
	}
}

func recoveryBootstrapAckMatches(request recoveryBootstrapRequest, ack recoveryBootstrapAck) bool {
	return validRecoveryBootstrap(request) && validRecoveryBootstrapAck(ack) &&
		request.Generation == ack.Generation && request.LaunchNonce == ack.LaunchNonce
}

// handleRecoveryBootstrap enforces the control-ledger consume-before-read
// ordering. Transport authentication and SOCK_SEQPACKET ancillary validation
// are performed by the Linux transport adapter before calling this function.
func handleRecoveryBootstrap(ctx context.Context, request recoveryBootstrapRequest, ledger recoveryBootstrapLedger) recoveryBootstrapAck {
	ack := recoveryBootstrapAck{
		Version: 1, Type: "RECOVERY_BOOTSTRAP_ACK",
		Generation: request.Generation, LaunchNonce: request.LaunchNonce,
		Result: "rejected",
	}
	if ctx == nil || ledger == nil || !validRecoveryBootstrap(request) {
		return ack
	}
	consumed, err := ledger.ConsumeRecoveryControl(ctx, request.Generation, request.LaunchNonce)
	if err != nil {
		ack.Result = "restart_required"
		return ack
	}
	if !consumed {
		return ack
	}
	target := recoveryTargetIdentity{
		Generation:  request.TargetGeneration,
		LaunchNonce: request.TargetLaunchNonce,
		RunEpoch:    request.TargetRunEpoch,
	}
	snapshot, err := ledger.LoadRecoveryTarget(ctx, target)
	if err != nil {
		ack.Result = "restart_required"
		return ack
	}
	if !snapshot.Found || snapshot.Identity != target {
		return ack
	}
	if snapshot.Terminal {
		ack.Result = "already_terminal"
		return ack
	}
	if !snapshot.EvidencePresent {
		ack.Result = "recovery_proof_missing"
		return ack
	}
	if snapshot.EvidenceRows != 1 {
		return ack
	}
	if !snapshot.EvidenceMatches || !isLowerHexString(snapshot.EvidenceDigest, 64) {
		return ack
	}
	if snapshot.RunState != "containment_empty" || !snapshot.OperationsTerminal ||
		!snapshot.RequiredReceiptsPresent || !snapshot.RequiredReceiptsMatch ||
		(snapshot.CleanupState != "complete" && snapshot.CleanupState != "retained") {
		ack.Result = "recovery_not_ready"
		return ack
	}
	committed, err := ledger.FinalizeRunTerminal(ctx, target, snapshot.EvidenceDigest)
	if err != nil {
		ack.Result = "restart_required"
		return ack
	}
	if !committed {
		ack.Result = "recovery_not_ready"
		return ack
	}
	ack.Result = "finalized"
	return ack
}

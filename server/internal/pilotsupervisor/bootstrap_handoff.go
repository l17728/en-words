//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const bootstrapHandoffMaxPacket = 768

var errBootstrapHandoffInvalid = errors.New("pilot bootstrap handoff invalid")

// bootstrapHandoffRequest is the exact docs/47 BOOTSTRAP/v1 wire schema. Field
// order is part of the canonical JSON encoding; identity values are attested
// by the trusted launcher and are never accepted from a Gateway request.
type bootstrapHandoffRequest struct {
	Version        uint8  `json:"version"`
	Type           string `json:"type"`
	Generation     uint64 `json:"generation"`
	LaunchNonce    string `json:"launch_nonce"`
	RunEpoch       string `json:"run_epoch"`
	PID            uint32 `json:"pid"`
	UID            uint32 `json:"uid"`
	StartTimeTicks uint64 `json:"start_time_ticks"`
	PIDNSDevice    uint64 `json:"pidns_device"`
	PIDNSInode     uint64 `json:"pidns_inode"`
	CgroupMountID  uint64 `json:"cgroup_mount_id"`
	CgroupDevice   uint64 `json:"cgroup_device"`
	CgroupInode    uint64 `json:"cgroup_inode"`
}

type bootstrapHandoffAck struct {
	Version     uint8  `json:"version"`
	Type        string `json:"type"`
	Generation  uint64 `json:"generation"`
	LaunchNonce string `json:"launch_nonce"`
	Result      string `json:"result"`
}

func encodeBootstrapHandoff(request bootstrapHandoffRequest) ([]byte, error) {
	if !validBootstrapHandoff(request) {
		return nil, errBootstrapHandoffInvalid
	}
	packet, err := json.Marshal(request)
	if err != nil || len(packet) > bootstrapHandoffMaxPacket {
		return nil, errBootstrapHandoffInvalid
	}
	return packet, nil
}

func decodeBootstrapHandoff(packet []byte) (bootstrapHandoffRequest, error) {
	var request bootstrapHandoffRequest
	if len(packet) == 0 || len(packet) > bootstrapHandoffMaxPacket {
		return request, errBootstrapHandoffInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(packet))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return bootstrapHandoffRequest{}, errBootstrapHandoffInvalid
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, packet) || !validBootstrapHandoff(request) {
		return bootstrapHandoffRequest{}, errBootstrapHandoffInvalid
	}
	return request, nil
}

func validBootstrapHandoff(request bootstrapHandoffRequest) bool {
	return request.Version == 1 && request.Type == "BOOTSTRAP" &&
		request.Generation > 0 && isLowerHexString(request.LaunchNonce, 32) &&
		isLowerHexString(request.RunEpoch, 32) && request.PID > 0 &&
		request.PID <= 1<<31-1 && request.UID > 0 &&
		request.StartTimeTicks > 0 && request.PIDNSDevice > 0 &&
		request.PIDNSInode > 0 && request.CgroupMountID > 0 &&
		request.CgroupDevice > 0 && request.CgroupInode > 0
}

func encodeBootstrapHandoffAck(ack bootstrapHandoffAck) ([]byte, error) {
	if !validBootstrapHandoffAck(ack) {
		return nil, errBootstrapHandoffInvalid
	}
	packet, err := json.Marshal(ack)
	if err != nil || len(packet) > bootstrapHandoffMaxPacket {
		return nil, errBootstrapHandoffInvalid
	}
	return packet, nil
}

func decodeBootstrapHandoffAck(packet []byte) (bootstrapHandoffAck, error) {
	var ack bootstrapHandoffAck
	if len(packet) == 0 || len(packet) > bootstrapHandoffMaxPacket {
		return ack, errBootstrapHandoffInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(packet))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ack) != nil || decoder.Decode(new(any)) != io.EOF {
		return bootstrapHandoffAck{}, errBootstrapHandoffInvalid
	}
	canonical, err := json.Marshal(ack)
	if err != nil || !bytes.Equal(canonical, packet) || !validBootstrapHandoffAck(ack) {
		return bootstrapHandoffAck{}, errBootstrapHandoffInvalid
	}
	return ack, nil
}

func validBootstrapHandoffAck(ack bootstrapHandoffAck) bool {
	return ack.Version == 1 && ack.Type == "BOOTSTRAP_ACK" && ack.Generation > 0 &&
		isLowerHexString(ack.LaunchNonce, 32) &&
		(ack.Result == "ok" || ack.Result == "rejected" || ack.Result == "restart_required")
}

func bootstrapHandoffAckMatches(request bootstrapHandoffRequest, ack bootstrapHandoffAck) bool {
	return validBootstrapHandoff(request) && validBootstrapHandoffAck(ack) &&
		request.Generation == ack.Generation && request.LaunchNonce == ack.LaunchNonce
}

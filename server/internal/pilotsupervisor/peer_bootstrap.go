//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const (
	peerBootstrapVersion  = 1
	peerBootstrapMaxFrame = 4096
	peerBootstrapMaxBody  = peerBootstrapMaxFrame - 4
)

var (
	errPeerBootstrapInvalid = errors.New("ipc_bootstrap_invalid")
	workerGenerationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	noncePattern            = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// peerBootstrap is a fixed-order wire schema. It is delivered over a
// one-way inherited OS pipe and never accepted from command-line or network
// input. GatewayPublicKey is lowercase hexadecimal Ed25519 public-key bytes.
type peerBootstrap struct {
	Version          int    `json:"version"`
	RunEpoch         string `json:"run_epoch"`
	WorkerGeneration string `json:"worker_generation"`
	EndpointNonce    string `json:"endpoint_nonce"`
	GatewayPID       uint32 `json:"gateway_pid"`
	GatewayUID       uint32 `json:"gateway_uid"`
	GatewaySID       string `json:"gateway_sid"`
	GatewayCreatedAt uint64 `json:"gateway_created_at"`
	GatewayPublicKey string `json:"gateway_public_key"`
}

func encodePeerBootstrap(b peerBootstrap) ([]byte, error) {
	if !validPeerBootstrap(b) {
		return nil, errPeerBootstrapInvalid
	}
	body, err := json.Marshal(b)
	if err != nil || len(body) == 0 || len(body) > peerBootstrapMaxBody {
		return nil, errPeerBootstrapInvalid
	}
	frame := make([]byte, 4+len(body))
	frame[0] = byte(len(body) >> 24)
	frame[1] = byte(len(body) >> 16)
	frame[2] = byte(len(body) >> 8)
	frame[3] = byte(len(body))
	copy(frame[4:], body)
	return frame, nil
}

func decodePeerBootstrapBody(body []byte) (peerBootstrap, error) {
	if len(body) == 0 || len(body) > peerBootstrapMaxBody {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	var b peerBootstrap
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	canonical, err := json.Marshal(b)
	if err != nil || !bytes.Equal(canonical, body) || !validPeerBootstrap(b) {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	return b, nil
}

func readPeerBootstrapFrame(r io.Reader) (peerBootstrap, error) {
	var prefix [4]byte
	if err := readPeerBootstrapBytes(r, prefix[:]); err != nil {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	n := int(prefix[0])<<24 | int(prefix[1])<<16 | int(prefix[2])<<8 | int(prefix[3])
	if n <= 0 || n > peerBootstrapMaxBody {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	body := make([]byte, n)
	if err := readPeerBootstrapBytes(r, body); err != nil {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	var trailing [1]byte
	for noProgress := 0; ; {
		count, err := r.Read(trailing[:])
		if count != 0 {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		noProgress++
		if noProgress >= 100 {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
	}
	return decodePeerBootstrapBody(body)
}

func readPeerBootstrapBytes(r io.Reader, dst []byte) error {
	noProgress := 0
	for len(dst) > 0 {
		n, err := r.Read(dst)
		if n < 0 || n > len(dst) {
			return io.ErrNoProgress
		}
		if n > 0 {
			dst = dst[n:]
			noProgress = 0
		}
		if err != nil {
			if len(dst) == 0 {
				return nil
			}
			return err
		}
		if n == 0 {
			noProgress++
			if noProgress >= 100 {
				return io.ErrNoProgress
			}
		}
	}
	return nil
}

func decodePeerBootstrapFrame(frame []byte) (peerBootstrap, error) {
	if len(frame) < 5 || len(frame) > peerBootstrapMaxFrame {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	n := int(frame[0])<<24 | int(frame[1])<<16 | int(frame[2])<<8 | int(frame[3])
	if n != len(frame)-4 || n > peerBootstrapMaxBody {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	return decodePeerBootstrapBody(frame[4:])
}

func validPeerBootstrap(b peerBootstrap) bool {
	if b.Version != peerBootstrapVersion || !epochPattern.MatchString(b.RunEpoch) ||
		!workerGenerationPattern.MatchString(b.WorkerGeneration) || !noncePattern.MatchString(b.EndpointNonce) ||
		b.GatewayPID == 0 || len(b.GatewayPublicKey) != 64 || b.GatewayPublicKey != string(bytes.ToLower([]byte(b.GatewayPublicKey))) {
		return false
	}
	if _, err := hex.DecodeString(b.GatewayPublicKey); err != nil {
		return false
	}
	return true
}

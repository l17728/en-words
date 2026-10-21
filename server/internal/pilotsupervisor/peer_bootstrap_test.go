//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validPeerBootstrapForTest() peerBootstrap {
	return peerBootstrap{
		Version: peerBootstrapVersion, RunEpoch: testEpoch,
		WorkerGeneration: "abcdef0123456789abcdef0123456789",
		EndpointNonce:    "fedcba9876543210fedcba9876543210",
		GatewayPID:       42, GatewayUID: 1000,
		GatewaySID: "S-1-5-21-1-2-3-1001", GatewayCreatedAt: 123456789,
		GatewayPublicKey: strings.Repeat("ab", 32),
	}
}

func TestPeerIPC_BootstrapAcceptsValidParent(t *testing.T) {
	want := validPeerBootstrapForTest()
	frame, err := encodePeerBootstrap(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePeerBootstrapFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %#v want %#v", got, want)
	}
	if len(frame) > peerBootstrapMaxFrame {
		t.Fatalf("frame length=%d", len(frame))
	}
	if binary.BigEndian.Uint32(frame[:4]) != uint32(len(frame)-4) {
		t.Fatal("invalid length prefix")
	}
}

func TestPeerIPC_BootstrapRejectsInvalidFrame(t *testing.T) {
	base, err := encodePeerBootstrap(validPeerBootstrapForTest())
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(nil), base[4:]...)
	badVersion := validPeerBootstrapForTest()
	badVersion.Version++
	versionBody, err := json.Marshal(badVersion)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append([]byte(`{"version":1,`), body[1:]...)
	unknownBody := append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"extra":true}`)...)
	tooLarge := make([]byte, peerBootstrapMaxBody+1)
	tests := map[string][]byte{
		"missing":       nil,
		"truncated":     base[:len(base)-1],
		"zero":          {0, 0, 0, 0},
		"oversize":      {0, 0, 0x10, 0x00},
		"duplicate":     frameWithBody(duplicate),
		"unknown":       frameWithBody(unknownBody),
		"noncanonical":  append([]byte(nil), base...),
		"version":       frameWithBody(versionBody),
		"epoch":         frameWithBody(replaceBootstrapField(body, `"run_epoch":"`+testEpoch+`"`, `"run_epoch":"not-an-epoch"`)),
		"trailing":      append(append([]byte(nil), base...), 0x41),
		"second":        append(append([]byte(nil), base...), base...),
		"oversize body": frameWithBody(tooLarge),
	}
	tests["noncanonical"] = append([]byte{' '}, base...)
	for name, frame := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodePeerBootstrapFrame(frame); !errors.Is(err, errPeerBootstrapInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func frameWithBody(body []byte) []byte {
	if len(body) > peerBootstrapMaxBody {
		return []byte{0xff, 0xff, 0xff, 0xff}
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	return frame
}

func replaceBootstrapField(body []byte, old, replacement string) []byte {
	return bytes.Replace(body, []byte(old), []byte(replacement), 1)
}

//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func validBootstrapHandoffRequest() bootstrapHandoffRequest {
	return bootstrapHandoffRequest{
		Version: 1, Type: "BOOTSTRAP", Generation: 8,
		LaunchNonce: "0123456789abcdef0123456789abcdef",
		RunEpoch:    "abcdef0123456789abcdef0123456789",
		PID:         4321, UID: 1201, StartTimeTicks: 987654,
		PIDNSDevice: 10, PIDNSInode: 11, CgroupMountID: 12,
		CgroupDevice: 13, CgroupInode: 14,
	}
}

func TestBootstrapHandoffCanonicalRoundTrip(t *testing.T) {
	request := validBootstrapHandoffRequest()
	packet, err := encodeBootstrapHandoff(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"type":"BOOTSTRAP","generation":8,"launch_nonce":"0123456789abcdef0123456789abcdef","run_epoch":"abcdef0123456789abcdef0123456789","pid":4321,"uid":1201,"start_time_ticks":987654,"pidns_device":10,"pidns_inode":11,"cgroup_mount_id":12,"cgroup_device":13,"cgroup_inode":14}`
	if string(packet) != want {
		t.Fatalf("canonical BOOTSTRAP frame mismatch: %s", packet)
	}
	decoded, err := decodeBootstrapHandoff(packet)
	if err != nil || decoded != request {
		t.Fatalf("decode result = %#v, %v", decoded, err)
	}
}

func TestBootstrapHandoffRejectsInvalidIdentityFields(t *testing.T) {
	base := validBootstrapHandoffRequest()
	tests := []struct {
		name string
		edit func(*bootstrapHandoffRequest)
	}{
		{"version", func(r *bootstrapHandoffRequest) { r.Version = 2 }},
		{"type", func(r *bootstrapHandoffRequest) { r.Type = "bootstrap" }},
		{"generation", func(r *bootstrapHandoffRequest) { r.Generation = 0 }},
		{"nonce", func(r *bootstrapHandoffRequest) { r.LaunchNonce = "ABCDEF0123456789ABCDEF0123456789" }},
		{"run-epoch", func(r *bootstrapHandoffRequest) { r.RunEpoch = "../etc/passwd" }},
		{"zero-pid", func(r *bootstrapHandoffRequest) { r.PID = 0 }},
		{"pid-overflow", func(r *bootstrapHandoffRequest) { r.PID = 1 << 31 }},
		{"zero-uid", func(r *bootstrapHandoffRequest) { r.UID = 0 }},
		{"zero-start-time", func(r *bootstrapHandoffRequest) { r.StartTimeTicks = 0 }},
		{"zero-pidns-device", func(r *bootstrapHandoffRequest) { r.PIDNSDevice = 0 }},
		{"zero-pidns-inode", func(r *bootstrapHandoffRequest) { r.PIDNSInode = 0 }},
		{"zero-cgroup-mount", func(r *bootstrapHandoffRequest) { r.CgroupMountID = 0 }},
		{"zero-cgroup-device", func(r *bootstrapHandoffRequest) { r.CgroupDevice = 0 }},
		{"zero-cgroup-inode", func(r *bootstrapHandoffRequest) { r.CgroupInode = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := base
			tc.edit(&request)
			if _, err := encodeBootstrapHandoff(request); !errors.Is(err, errBootstrapHandoffInvalid) {
				t.Fatalf("encode invalid request error = %v", err)
			}
		})
	}
}

func TestBootstrapHandoffDecodeRejectsNoncanonicalAndMalformedFrames(t *testing.T) {
	canonical, err := encodeBootstrapHandoff(validBootstrapHandoffRequest())
	if err != nil {
		t.Fatal(err)
	}
	reordered := bytes.Replace(canonical, []byte(`"type":"BOOTSTRAP","generation":8`), []byte(`"generation":8,"type":"BOOTSTRAP"`), 1)
	duplicate := append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"version":1}`)...)
	unknown := append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"extra":true}`)...)
	tests := map[string][]byte{
		"empty":                 nil,
		"oversized":             bytes.Repeat([]byte{'x'}, bootstrapHandoffMaxPacket+1),
		"reordered":             reordered,
		"duplicate-key":         duplicate,
		"unknown-key":           unknown,
		"leading-space":         append([]byte{' '}, canonical...),
		"trailing-data":         append(append([]byte(nil), canonical...), []byte(` {}`)...),
		"fractional-generation": bytes.Replace(canonical, []byte(`"generation":8`), []byte(`"generation":8.0`), 1),
		"negative-generation":   bytes.Replace(canonical, []byte(`"generation":8`), []byte(`"generation":-8`), 1),
	}
	for name, packet := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBootstrapHandoff(packet); !errors.Is(err, errBootstrapHandoffInvalid) {
				t.Fatalf("decode malformed frame error = %v", err)
			}
		})
	}
	if len(canonical) > bootstrapHandoffMaxPacket {
		t.Fatal(fmt.Sprintf("fixture unexpectedly exceeds limit: %d", len(canonical)))
	}
}

func TestBootstrapHandoffAckValidationAndCorrelation(t *testing.T) {
	request := validBootstrapHandoffRequest()
	ack := bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK", Generation: request.Generation,
		LaunchNonce: request.LaunchNonce, Result: "ok"}
	packet, err := encodeBootstrapHandoffAck(ack)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBootstrapHandoffAck(packet)
	if err != nil || decoded != ack || !bootstrapHandoffAckMatches(request, decoded) {
		t.Fatalf("valid ACK did not round-trip/correlate: %#v, %v", decoded, err)
	}
	for _, result := range []string{"rejected", "restart_required"} {
		ack.Result = result
		if _, err := encodeBootstrapHandoffAck(ack); err != nil {
			t.Fatalf("encode result %q: %v", result, err)
		}
	}
	for _, edit := range []func(*bootstrapHandoffAck){
		func(a *bootstrapHandoffAck) { a.Result = "anything" },
		func(a *bootstrapHandoffAck) { a.Type = "RECOVERY_BOOTSTRAP_ACK" },
		func(a *bootstrapHandoffAck) { a.Generation++ },
		func(a *bootstrapHandoffAck) { a.LaunchNonce = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
	} {
		mutated := bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK", Generation: request.Generation,
			LaunchNonce: request.LaunchNonce, Result: "ok"}
		edit(&mutated)
		if bootstrapHandoffAckMatches(request, mutated) {
			t.Fatalf("mutated ACK unexpectedly matched: %#v", mutated)
		}
	}
}

func TestBootstrapHandoffAckDecoderRejectsMalformedCanonicalFrames(t *testing.T) {
	ack := bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK", Generation: 8,
		LaunchNonce: "0123456789abcdef0123456789abcdef", Result: "ok"}
	canonical, err := encodeBootstrapHandoffAck(ack)
	if err != nil {
		t.Fatal(err)
	}
	reordered := bytes.Replace(canonical,
		[]byte(`{"version":1,"type":"BOOTSTRAP_ACK",`),
		[]byte(`{"type":"BOOTSTRAP_ACK","version":1,`), 1)
	duplicate := append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"version":1}`)...)
	unknown := append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"extra":true}`)...)
	tests := []struct {
		name   string
		packet []byte
	}{
		{name: "empty"},
		{name: "oversized", packet: bytes.Repeat([]byte{'x'}, bootstrapHandoffMaxPacket+1)},
		{name: "malformed", packet: []byte("{")},
		{name: "trailing", packet: append(append([]byte(nil), canonical...), []byte(" {}")...)},
		{name: "reordered", packet: reordered},
		{name: "duplicate", packet: duplicate},
		{name: "unknown", packet: unknown},
		{name: "invalid result", packet: bytes.Replace(canonical, []byte(`"result":"ok"`), []byte(`"result":"bad"`), 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeBootstrapHandoffAck(tc.packet); !errors.Is(err, errBootstrapHandoffInvalid) {
				t.Fatalf("malformed ACK accepted or misclassified: %v", err)
			}
		})
	}
	if _, err := encodeBootstrapHandoffAck(bootstrapHandoffAck{}); !errors.Is(err, errBootstrapHandoffInvalid) {
		t.Fatalf("invalid ACK encoded: %v", err)
	}
}

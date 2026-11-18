package pilotquota

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegalMixedChainOffsetsAndLengths(t *testing.T) {
	type expectedRecord struct {
		Type   string `json:"type"`
		Offset int64  `json:"offset"`
		Length int    `json:"length"`
	}
	var fixture struct {
		TotalBytes int              `json:"total_bytes"`
		Records    []expectedRecord `json:"records"`
	}
	path := filepath.Join("testdata", "mixed-chain-v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mixed-chain layout: %v", err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode mixed-chain layout: %v", err)
	}

	key := bytesRepeat(0x11, 32)
	var journal []byte
	var previous [32]byte
	var nextSequence uint64 = 1
	appendRecord := func(r Record) {
		t.Helper()
		r.Sequence = nextSequence
		nextSequence++
		r.Previous = previous
		frame, err := EncodeFrame(r, key)
		if err != nil {
			t.Fatalf("encode %v: %v", r.Type, err)
		}
		journal = append(journal, frame...)
		previous = FrameDigest(frame)
	}
	makeRequest := func(id byte) ([16]byte, [16]byte, [32]byte) {
		var operation, request [16]byte
		var principal [32]byte
		operation[0], request[0], principal[0] = id, id+10, 3
		return operation, request, principal
	}
	makeIssued := func(operation, request [16]byte, principal [32]byte, issuedSequence, issuedAt, expiresAt uint64) [32]byte {
		ciphertext := []byte("abc")
		hash := sha256.Sum256(ciphertext)
		r := testRecord(Issued, issuedSequence+1, issuedAt*1_000_000_000, operation, request, previous)
		r.PrincipalRef = principal
		r.Suffix = make([]byte, 61)
		binary.BigEndian.PutUint64(r.Suffix[:8], issuedSequence)
		copy(r.Suffix[8:40], hash[:])
		binary.BigEndian.PutUint64(r.Suffix[40:48], issuedAt)
		binary.BigEndian.PutUint64(r.Suffix[48:56], expiresAt)
		binary.BigEndian.PutUint16(r.Suffix[56:58], uint16(len(ciphertext)))
		copy(r.Suffix[58:], ciphertext)
		appendRecord(r)
		return hash
	}
	settle := func(typ RecordType, op, request [16]byte, principal [32]byte, issuedSequence uint64, hash [32]byte, issuedAt, expiresAt, eventNS, appendNS uint64) {
		r := testRecord(typ, 1, appendNS, op, request, previous)
		r.PrincipalRef = principal
		if typ == Bound {
			r.Suffix = make([]byte, 64)
			binary.BigEndian.PutUint64(r.Suffix[:8], issuedSequence)
			copy(r.Suffix[8:40], hash[:])
			for i := 40; i < 56; i++ {
				r.Suffix[i] = 0x33
			}
			binary.BigEndian.PutUint64(r.Suffix[56:64], eventNS)
		} else {
			r.Suffix = make([]byte, 64)
			binary.BigEndian.PutUint64(r.Suffix[:8], issuedSequence)
			copy(r.Suffix[8:40], hash[:])
			binary.BigEndian.PutUint64(r.Suffix[40:48], issuedAt)
			binary.BigEndian.PutUint64(r.Suffix[48:56], expiresAt)
			binary.BigEndian.PutUint64(r.Suffix[56:64], eventNS)
		}
		appendRecord(r)
	}

	operationA, requestA, principalA := makeRequest(1)
	permitA := testRecord(Permit, 1, 100_000_000_000, operationA, requestA, previous)
	permitA.PrincipalRef = principalA
	appendRecord(permitA)
	hashA := makeIssued(operationA, requestA, principalA, 1, 100, 200)
	var settleOpA [16]byte
	settleOpA[0] = 51
	settle(Bound, settleOpA, requestA, principalA, 2, hashA, 100, 200, 150_000_000_000, 205_000_000_000)

	operationB, requestB, principalB := makeRequest(2)
	permitB := testRecord(Permit, 4, 205_000_000_000, operationB, requestB, previous)
	permitB.PrincipalRef = principalB
	appendRecord(permitB)
	hashB := makeIssued(operationB, requestB, principalB, 4, 206, 300)
	var settleOpB [16]byte
	settleOpB[0] = 52
	settle(Expired, settleOpB, requestB, principalB, 5, hashB, 206, 300, 300_000_000_000, 301_000_000_000)

	if len(journal) != fixture.TotalBytes {
		t.Fatalf("mixed chain bytes = %d, want %d", len(journal), fixture.TotalBytes)
	}
	result, err := Replay(journal, map[[16]byte][]byte{{1}: key})
	if err != nil {
		t.Fatalf("replay legal mixed chain: %v", err)
	}
	if len(result.Records) != len(fixture.Records) {
		t.Fatalf("record count = %d, want %d", len(result.Records), len(fixture.Records))
	}
	for i, want := range fixture.Records {
		got := result.Records[i]
		if got.JournalOffset != want.Offset || int(got.FrameLength) != want.Length || got.Type.String() != want.Type {
			t.Errorf("record %d = (%s, offset=%d, length=%d), want (%s, offset=%d, length=%d)", i, got.Type, got.JournalOffset, got.FrameLength, want.Type, want.Offset, want.Length)
		}
	}
}

func (t RecordType) String() string {
	switch t {
	case Permit:
		return "PERMIT"
	case Issued:
		return "ISSUED"
	case Bound:
		return "BOUND"
	case Expired:
		return "EXPIRED"
	default:
		return "UNKNOWN"
	}
}

package pilotquota

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckedInFrameVectorsRoundTrip(t *testing.T) {
	path := filepath.Join("..", "..", "..", "review", "pilot-intent-quota-frame-v1-vectors.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checked-in frame vectors: %v", err)
	}
	var fixture struct {
		Vectors []struct {
			Type         string `json:"type"`
			Sequence     uint64 `json:"sequence"`
			RecordTimeNS uint64 `json:"record_time_ns"`
			FrameHex     string `json:"frame_hex"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode checked-in frame vectors: %v", err)
	}
	if len(fixture.Vectors) != 4 {
		t.Fatalf("vector count = %d, want all four record types", len(fixture.Vectors))
	}
	key := bytesRepeat(0x11, 32)
	seen := map[RecordType]bool{}
	for _, vector := range fixture.Vectors {
		frame, err := hex.DecodeString(vector.FrameHex)
		if err != nil {
			t.Fatalf("%s vector hex: %v", vector.Type, err)
		}
		record, err := DecodeFrame(frame, key)
		if err != nil {
			t.Fatalf("decode %s vector: %v", vector.Type, err)
		}
		encoded, err := EncodeFrame(record, key)
		if err != nil {
			t.Fatalf("encode %s vector: %v", vector.Type, err)
		}
		if !bytes.Equal(encoded, frame) {
			t.Fatalf("%s vector did not round-trip byte-for-byte", vector.Type)
		}
		if record.Type.String() != vector.Type || record.Sequence != vector.Sequence || record.RecordTimeNS != vector.RecordTimeNS {
			t.Fatalf("%s vector metadata disagrees with frame: decoded type=%s sequence=%d time=%d", vector.Type, record.Type, record.Sequence, record.RecordTimeNS)
		}
		if int(record.Type) < int(Permit) || int(record.Type) > int(Expired) || seen[record.Type] {
			t.Fatalf("unexpected or duplicate vector type %s", vector.Type)
		}
		seen[record.Type] = true
	}
	for _, typ := range []RecordType{Permit, Issued, Bound, Expired} {
		if !seen[typ] {
			t.Fatalf("missing vector type %d", typ)
		}
	}
}

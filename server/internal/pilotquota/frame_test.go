package pilotquota

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func testRecord(typ RecordType, sequence, at uint64, op [16]byte, request [16]byte, previous [32]byte) Record {
	r := Record{Type: typ, Sequence: sequence, RecordTimeNS: at, OperationID: op, RequestID: request, Previous: previous}
	r.KeyID[0] = 1
	r.PrincipalRef[0] = 2
	r.BindingDigest[0] = 3
	return r
}

func TestEncodeDecodePermitAndRejectTampering(t *testing.T) {
	var op, request [16]byte
	op[0], request[0] = 4, 5
	r := testRecord(Permit, 1, 10, op, request, [32]byte{})
	frame, err := EncodeFrame(r, testKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFrame(frame, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatalf("decoded record differs: %#v", got)
	}
	bad := append([]byte(nil), frame...)
	bad[frameHeaderSize+48] ^= 1
	if _, err := DecodeFrame(bad, testKey); err == nil {
		t.Fatal("tampered frame accepted")
	}
	if _, err := DecodeFrame(frame[:len(frame)-1], testKey); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestRecordValidationRejectsInvalidLayouts(t *testing.T) {
	valid := testRecord(Permit, 1, 10, [16]byte{1}, [16]byte{2}, [32]byte{})
	issuedRecord := func() Record {
		r := testRecord(Issued, 2, 100_000_000_000, [16]byte{1}, [16]byte{2}, [32]byte{})
		r.Suffix = make([]byte, 59)
		binary.BigEndian.PutUint64(r.Suffix[40:48], 100)
		binary.BigEndian.PutUint64(r.Suffix[48:56], 200)
		binary.BigEndian.PutUint16(r.Suffix[56:58], 1)
		r.Suffix[58] = 0x44
		h := sha256.Sum256(r.Suffix[58:])
		copy(r.Suffix[8:40], h[:])
		return r
	}
	bound := testRecord(Bound, 1, 10, [16]byte{1}, [16]byte{2}, [32]byte{})
	bound.Suffix = make([]byte, 64)
	binary.BigEndian.PutUint64(bound.Suffix[56:64], 10)
	expired := testRecord(Expired, 1, 200_000_000_000, [16]byte{1}, [16]byte{2}, [32]byte{})
	expired.Suffix = make([]byte, 64)
	binary.BigEndian.PutUint64(expired.Suffix[40:48], 100)
	binary.BigEndian.PutUint64(expired.Suffix[48:56], 200)
	binary.BigEndian.PutUint64(expired.Suffix[56:64], 200_000_000_000)

	cases := []struct {
		name   string
		record Record
	}{
		{"permit suffix", func() Record { r := valid; r.Suffix = []byte{1}; return r }()},
		{"issued short", func() Record { r := issuedRecord(); r.Suffix = r.Suffix[:58]; return r }()},
		{"issued overlong", func() Record { r := issuedRecord(); r.Suffix = append(r.Suffix, 1); return r }()},
		{"issued length mismatch", func() Record { r := issuedRecord(); binary.BigEndian.PutUint16(r.Suffix[56:58], 2); return r }()},
		{"issued hash mismatch", func() Record { r := issuedRecord(); r.Suffix[8] ^= 1; return r }()},
		{"issued time order", func() Record { r := issuedRecord(); binary.BigEndian.PutUint64(r.Suffix[48:56], 100); return r }()},
		{"issued time overflow", func() Record { r := issuedRecord(); binary.BigEndian.PutUint64(r.Suffix[48:56], ^uint64(0)); return r }()},
		{"issued before issued-at", func() Record { r := issuedRecord(); r.RecordTimeNS = 99_999_999_999; return r }()},
		{"issued at expiry", func() Record { r := issuedRecord(); r.RecordTimeNS = 200_000_000_000; return r }()},
		{"bound suffix", func() Record { r := bound; r.Suffix = r.Suffix[:63]; return r }()},
		{"bound append precedes event", func() Record { r := bound; binary.BigEndian.PutUint64(r.Suffix[56:64], 11); return r }()},
		{"expired suffix", func() Record { r := expired; r.Suffix = r.Suffix[:63]; return r }()},
		{"expired time order", func() Record { r := expired; binary.BigEndian.PutUint64(r.Suffix[48:56], 100); return r }()},
		{"expired event before expiry", func() Record {
			r := expired
			binary.BigEndian.PutUint64(r.Suffix[56:64], 199_999_999_999)
			r.RecordTimeNS = 199_999_999_999
			return r
		}()},
		{"unknown type", func() Record { r := valid; r.Type = 99; return r }()},
		{"zero sequence", func() Record { r := valid; r.Sequence = 0; return r }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.record.validate(); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}

func TestZeroUnixNanosecondTimestampIsValid(t *testing.T) {
	r := testRecord(Permit, 1, 0, [16]byte{1}, [16]byte{2}, [32]byte{})
	if _, err := EncodeFrame(r, testKey); err != nil {
		t.Fatalf("zero Unix timestamp rejected: %v", err)
	}
}

func TestJournalByteCapDeterminesEffectiveRowCap(t *testing.T) {
	if minFrameSize != 208 {
		t.Fatalf("minimum frame size = %d, want 208", minFrameSize)
	}
	if got := maxJournalBytes / minFrameSize; got != 322_638 {
		t.Fatalf("effective rows from byte cap = %d, want 322638", got)
	}
	if got := maxJournalBytes % minFrameSize; got != 160 {
		t.Fatalf("byte-cap remainder = %d, want 160", got)
	}
	if maxJournalRows <= maxJournalBytes/minFrameSize {
		t.Fatalf("explicit row guard %d is expected to be looser than the byte-derived cap", maxJournalRows)
	}
}

func TestTimestampConversionLimits(t *testing.T) {
	const maxSeconds = uint64(math.MaxInt64 / 1_000_000_000)
	got, ok := secondsToNS(maxSeconds)
	if !ok || got != 9_223_372_036_000_000_000 {
		t.Fatalf("secondsToNS(max) = (%d, %t)", got, ok)
	}
	if _, ok := secondsToNS(maxSeconds + 1); ok {
		t.Fatal("seconds above checked nanosecond range accepted")
	}
	r := testRecord(Permit, 1, math.MaxInt64, [16]byte{1}, [16]byte{2}, [32]byte{})
	if err := r.validate(); err != nil {
		t.Fatalf("MaxInt64 nanosecond timestamp rejected: %v", err)
	}
	r.RecordTimeNS = uint64(math.MaxInt64) + 1
	if err := r.validate(); err == nil {
		t.Fatal("nanosecond timestamp above MaxInt64 accepted")
	}
}

func TestSettlementEventTimestampInt64Bounds(t *testing.T) {
	bound := testRecord(Bound, 1, math.MaxInt64, [16]byte{1}, [16]byte{2}, [32]byte{})
	bound.Suffix = make([]byte, 64)
	binary.BigEndian.PutUint64(bound.Suffix[56:64], math.MaxInt64)
	if err := bound.validate(); err != nil {
		t.Fatalf("BOUND MaxInt64 event rejected: %v", err)
	}
	binary.BigEndian.PutUint64(bound.Suffix[56:64], uint64(math.MaxInt64)+1)
	if err := bound.validate(); err == nil {
		t.Fatal("BOUND event above MaxInt64 accepted")
	}

	expired := testRecord(Expired, 1, math.MaxInt64, [16]byte{1}, [16]byte{2}, [32]byte{})
	expired.Suffix = make([]byte, 64)
	binary.BigEndian.PutUint64(expired.Suffix[40:48], 1)
	binary.BigEndian.PutUint64(expired.Suffix[48:56], 2)
	binary.BigEndian.PutUint64(expired.Suffix[56:64], math.MaxInt64)
	if err := expired.validate(); err != nil {
		t.Fatalf("EXPIRED MaxInt64 event rejected: %v", err)
	}
	binary.BigEndian.PutUint64(expired.Suffix[56:64], uint64(math.MaxInt64)+1)
	if err := expired.validate(); err == nil {
		t.Fatal("EXPIRED event above MaxInt64 accepted")
	}
}

func TestDecodeRejectsMalformedEnvelopeAndAuthenticatedInvalidRecord(t *testing.T) {
	r := testRecord(Permit, 1, 10, [16]byte{1}, [16]byte{2}, [32]byte{})
	frame, err := EncodeFrame(r, testKey)
	if err != nil {
		t.Fatal(err)
	}
	badMAC := func(in []byte) []byte {
		copyFrame := append([]byte(nil), in...)
		mac := hmac.New(sha256.New, testKey)
		_, _ = mac.Write([]byte("en-words-intent-quota-v1\x00"))
		_, _ = mac.Write(copyFrame[:len(copyFrame)-macSize])
		copy(copyFrame[len(copyFrame)-macSize:], mac.Sum(nil))
		return copyFrame
	}
	cases := []struct {
		name  string
		frame []byte
		key   []byte
	}{
		{"short key", frame, testKey[:31]},
		{"short frame", frame[:frameHeaderSize+commonSize+macSize-1], testKey},
		{"oversized frame", append(append([]byte(nil), frame...), bytesRepeat(0, maxFrameSize+1)...), testKey},
		{"bad magic", func() []byte { x := append([]byte(nil), frame...); x[0] ^= 1; return x }(), testKey},
		{"bad version", func() []byte { x := append([]byte(nil), frame...); x[9]++; return x }(), testKey},
		{"bad flags", func() []byte { x := append([]byte(nil), frame...); x[11] = 1; return x }(), testKey},
		{"bad declared length", func() []byte { x := append([]byte(nil), frame...); x[15]--; return x }(), testKey},
		{"unknown type", func() []byte { x := append([]byte(nil), frame...); x[10] = 99; return x }(), testKey},
		{"bad mac", func() []byte { x := append([]byte(nil), frame...); x[len(x)-1] ^= 1; return x }(), testKey},
		{"authenticated invalid record", func() []byte {
			x := append([]byte(nil), frame...)
			binary.BigEndian.PutUint64(x[frameHeaderSize+32:frameHeaderSize+40], 0)
			return badMAC(x)
		}(), testKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeFrame(tc.frame, tc.key); err == nil {
				t.Fatal("malformed frame accepted")
			}
		})
	}
	if _, err := EncodeFrame(r, testKey[:31]); err == nil {
		t.Fatal("short HMAC key accepted")
	}
}

func TestReplayRejectsMalformedAndDuplicateRecords(t *testing.T) {
	base := testRecord(Permit, 1, 10, [16]byte{1}, [16]byte{2}, [32]byte{})
	frame, err := EncodeFrame(base, testKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[[16]byte][]byte{base.KeyID: testKey}
	if _, err := Replay([]byte{1}, keys); err == nil {
		t.Fatal("truncated header accepted")
	}
	if _, err := Replay(make([]byte, frameHeaderSize), keys); err == nil {
		t.Fatal("invalid length accepted")
	}
	if _, err := Replay(frame, nil); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := Replay(bytesRepeat(0, maxJournalBytes+1), keys); err == nil {
		t.Fatal("journal byte cap was not enforced")
	}
	if _, err := Replay(frame, keys); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	badSeq := testRecord(Permit, 2, 10, [16]byte{3}, [16]byte{4}, [32]byte{})
	badSeqFrame, err := EncodeFrame(badSeq, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(badSeqFrame, keys); err == nil {
		t.Fatal("initial sequence gap accepted")
	}
	second := testRecord(Permit, 2, 10, [16]byte{3}, base.RequestID, FrameDigest(frame))
	secondFrame, err := EncodeFrame(second, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(append(frame, secondFrame...), keys); err == nil {
		t.Fatal("duplicate request ID accepted")
	}
	badPrevious := testRecord(Permit, 2, 11, [16]byte{3}, [16]byte{5}, [32]byte{1})
	badPreviousFrame, err := EncodeFrame(badPrevious, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(append(frame, badPreviousFrame...), keys); err == nil {
		t.Fatal("broken chain accepted")
	}
	regressedTime := testRecord(Permit, 2, 9, [16]byte{3}, [16]byte{5}, FrameDigest(frame))
	regressedFrame, err := EncodeFrame(regressedTime, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(append(frame, regressedFrame...), keys); err == nil {
		t.Fatal("time regression accepted")
	}
	if _, err := Replay(frame, map[[16]byte][]byte{base.KeyID: testKey[:31]}); err == nil {
		t.Fatal("short replay key accepted")
	}
	badMAC := append([]byte(nil), frame...)
	badMAC[len(badMAC)-1] ^= 1
	if _, err := Replay(badMAC, keys); err == nil {
		t.Fatal("bad frame MAC accepted during replay")
	}
	settlement := testRecord(Bound, 1, 10, [16]byte{9}, [16]byte{2}, [32]byte{})
	settlement.Suffix = make([]byte, 64)
	binary.BigEndian.PutUint64(settlement.Suffix[:8], 99)
	binary.BigEndian.PutUint64(settlement.Suffix[56:64], 10)
	settlementFrame, err := EncodeFrame(settlement, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(settlementFrame, keys); err == nil {
		t.Fatal("settlement without ISSUE accepted")
	}
}

func TestReplayRejectsDuplicateOperationAndMismatchedIssue(t *testing.T) {
	var operation, firstRequest, secondRequest [16]byte
	operation[0], firstRequest[0], secondRequest[0] = 1, 2, 3
	permit := testRecord(Permit, 1, 100_000_000_000, operation, firstRequest, [32]byte{})
	permitFrame, err := EncodeFrame(permit, testKey)
	if err != nil {
		t.Fatal(err)
	}
	duplicatePermit := testRecord(Permit, 2, 100_000_000_000, operation, secondRequest, FrameDigest(permitFrame))
	duplicateFrame, err := EncodeFrame(duplicatePermit, testKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[[16]byte][]byte{permit.KeyID: testKey}
	if _, err := Replay(append(permitFrame, duplicateFrame...), keys); err == nil {
		t.Fatal("duplicate operation ID accepted")
	}

	issued := testRecord(Issued, 2, 100_000_000_000, operation, firstRequest, FrameDigest(permitFrame))
	issued.PrincipalRef[0]++
	issued.Suffix = make([]byte, 59)
	binary.BigEndian.PutUint64(issued.Suffix[:8], permit.Sequence)
	issued.Suffix[58] = 0x44
	hash := sha256.Sum256(issued.Suffix[58:])
	copy(issued.Suffix[8:40], hash[:])
	binary.BigEndian.PutUint64(issued.Suffix[40:48], 100)
	binary.BigEndian.PutUint64(issued.Suffix[48:56], 200)
	binary.BigEndian.PutUint16(issued.Suffix[56:58], 1)
	issuedFrame, err := EncodeFrame(issued, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(append(permitFrame, issuedFrame...), keys); err == nil {
		t.Fatal("ISSUED with changed principal accepted")
	}

	orphan := testRecord(Issued, 1, 100_000_000_000, operation, firstRequest, [32]byte{})
	orphan.Suffix = append([]byte(nil), issued.Suffix...)
	orphanFrame, err := EncodeFrame(orphan, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(orphanFrame, keys); err == nil {
		t.Fatal("ISSUED without PERMIT accepted")
	}
}

func TestIssuedAcceptsMaximumCiphertextAndRejectsOversize(t *testing.T) {
	r := testRecord(Issued, 2, 100_000_000_000, [16]byte{1}, [16]byte{2}, [32]byte{})
	r.Suffix = make([]byte, 58+512)
	binary.BigEndian.PutUint64(r.Suffix[:8], 1)
	ciphertext := bytesRepeat(0x7f, 512)
	hash := sha256.Sum256(ciphertext)
	copy(r.Suffix[8:40], hash[:])
	binary.BigEndian.PutUint64(r.Suffix[40:48], 100)
	binary.BigEndian.PutUint64(r.Suffix[48:56], 200)
	binary.BigEndian.PutUint16(r.Suffix[56:58], 512)
	copy(r.Suffix[58:], ciphertext)
	if _, err := EncodeFrame(r, testKey); err != nil {
		t.Fatalf("maximum legal ciphertext rejected: %v", err)
	}
	r.Suffix = append(r.Suffix, 0)
	binary.BigEndian.PutUint16(r.Suffix[56:58], 513)
	if _, err := EncodeFrame(r, testKey); err == nil {
		t.Fatal("513-byte ciphertext accepted")
	}
}

func TestReplayExpiredSettlementAndBindExpiryBoundary(t *testing.T) {
	makeJournal := func(settlement RecordType, appendNS, eventNS uint64) []byte {
		var op, request [16]byte
		op[0], request[0] = 7, 8
		permit := testRecord(Permit, 1, 100_000_000_000, op, request, [32]byte{})
		permitFrame, err := EncodeFrame(permit, testKey)
		if err != nil {
			t.Fatal(err)
		}
		issued := testRecord(Issued, 2, 100_000_000_000, op, request, FrameDigest(permitFrame))
		issued.Suffix = make([]byte, 59)
		binary.BigEndian.PutUint64(issued.Suffix[:8], 1)
		cipherHash := sha256.Sum256([]byte{0x55})
		copy(issued.Suffix[8:40], cipherHash[:])
		binary.BigEndian.PutUint64(issued.Suffix[40:48], 100)
		binary.BigEndian.PutUint64(issued.Suffix[48:56], 200)
		binary.BigEndian.PutUint16(issued.Suffix[56:58], 1)
		issued.Suffix[58] = 0x55
		issuedFrame, err := EncodeFrame(issued, testKey)
		if err != nil {
			t.Fatal(err)
		}
		settle := testRecord(settlement, 3, appendNS, [16]byte{9}, request, FrameDigest(issuedFrame))
		settle.Suffix = make([]byte, 64)
		binary.BigEndian.PutUint64(settle.Suffix[:8], 2)
		copy(settle.Suffix[8:40], issued.Suffix[8:40])
		if settlement == Expired {
			binary.BigEndian.PutUint64(settle.Suffix[40:48], 100)
			binary.BigEndian.PutUint64(settle.Suffix[48:56], 200)
		}
		binary.BigEndian.PutUint64(settle.Suffix[56:64], eventNS)
		settleFrame, err := EncodeFrame(settle, testKey)
		if err != nil {
			t.Fatal(err)
		}
		return append(append(permitFrame, issuedFrame...), settleFrame...)
	}
	keys := map[[16]byte][]byte{{1}: testKey}
	if _, err := Replay(makeJournal(Expired, 200_000_000_000, 200_000_000_000), keys); err != nil {
		t.Fatalf("valid EXPIRED rejected: %v", err)
	}
	if _, err := Replay(makeJournal(Bound, 205_000_000_000, 199_999_999_999), keys); err != nil {
		t.Fatalf("pre-expiry BOUND appended late rejected: %v", err)
	}
	if _, err := Replay(makeJournal(Bound, 200_000_000_001, 200_000_000_001), keys); err == nil {
		t.Fatal("BOUND event after expiry accepted")
	}
}

func TestReplayRejectsSettlementTupleAndExpiryMismatch(t *testing.T) {
	for _, typ := range []RecordType{Bound, Expired} {
		t.Run(typ.String(), func(t *testing.T) {
			var operation, request [16]byte
			var principal, binding [32]byte
			operation[0], request[0], principal[0], binding[0] = 1, 2, 3, 4
			permit := testRecord(Permit, 1, 100_000_000_000, operation, request, [32]byte{})
			permit.PrincipalRef, permit.BindingDigest = principal, binding
			permitFrame, err := EncodeFrame(permit, testKey)
			if err != nil {
				t.Fatal(err)
			}
			ciphertext := []byte("x")
			handleHash := sha256.Sum256(ciphertext)
			issued := testRecord(Issued, 2, 100_000_000_000, operation, request, FrameDigest(permitFrame))
			issued.PrincipalRef, issued.BindingDigest = principal, binding
			issued.Suffix = make([]byte, 59)
			binary.BigEndian.PutUint64(issued.Suffix[:8], permit.Sequence)
			copy(issued.Suffix[8:40], handleHash[:])
			binary.BigEndian.PutUint64(issued.Suffix[40:48], 100)
			binary.BigEndian.PutUint64(issued.Suffix[48:56], 200)
			binary.BigEndian.PutUint16(issued.Suffix[56:58], uint16(len(ciphertext)))
			copy(issued.Suffix[58:], ciphertext)
			issuedFrame, err := EncodeFrame(issued, testKey)
			if err != nil {
				t.Fatal(err)
			}
			appendNS, eventNS := uint64(150_000_000_000), uint64(150_000_000_000)
			if typ == Expired {
				appendNS, eventNS = 200_000_000_000, 200_000_000_000
			}
			base := testRecord(typ, 3, appendNS, [16]byte{9}, request, FrameDigest(issuedFrame))
			base.PrincipalRef, base.BindingDigest = principal, binding
			base.Suffix = make([]byte, 64)
			binary.BigEndian.PutUint64(base.Suffix[:8], issued.Sequence)
			copy(base.Suffix[8:40], handleHash[:])
			if typ == Bound {
				copy(base.Suffix[40:56], bytesRepeat(0x33, 16))
			} else {
				binary.BigEndian.PutUint64(base.Suffix[40:48], 100)
				binary.BigEndian.PutUint64(base.Suffix[48:56], 200)
			}
			binary.BigEndian.PutUint64(base.Suffix[56:64], eventNS)
			keys := map[[16]byte][]byte{permit.KeyID: testKey}
			validFrame, err := EncodeFrame(base, testKey)
			if err != nil {
				t.Fatal(err)
			}
			prefix := append(append([]byte(nil), permitFrame...), issuedFrame...)
			if _, err := Replay(append(append([]byte(nil), prefix...), validFrame...), keys); err != nil {
				t.Fatalf("valid settlement rejected: %v", err)
			}

			mutations := []struct {
				name string
				edit func(*Record)
			}{
				{"request id", func(r *Record) { r.RequestID[0]++ }},
				{"principal reference", func(r *Record) { r.PrincipalRef[0]++ }},
				{"binding digest", func(r *Record) { r.BindingDigest[0]++ }},
				{"handle hash", func(r *Record) { r.Suffix[8]++ }},
			}
			if typ == Expired {
				mutations = append(mutations,
					struct {
						name string
						edit func(*Record)
					}{"issued at", func(r *Record) { binary.BigEndian.PutUint64(r.Suffix[40:48], 101) }},
					struct {
						name string
						edit func(*Record)
					}{"expiry", func(r *Record) { binary.BigEndian.PutUint64(r.Suffix[48:56], 199) }},
				)
			}
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					changed := base
					changed.Suffix = append([]byte(nil), base.Suffix...)
					mutation.edit(&changed)
					frame, err := EncodeFrame(changed, testKey) // Re-sign to exercise replay linkage checks.
					if err != nil {
						t.Fatalf("encode mutated settlement: %v", err)
					}
					if _, err := Replay(append(append([]byte(nil), prefix...), frame...), keys); err == nil {
						t.Fatal("mismatched settlement accepted")
					}
				})
			}
		})
	}
}

func TestReplayHandlesManyIssuancesBeforeSettlement(t *testing.T) {
	const count = 1200
	keys := map[[16]byte][]byte{{1}: testKey}
	var journal []byte
	previous := [32]byte{}
	requests := make([][16]byte, count)
	issuedHashes := make([][32]byte, count)
	issuedSeqs := make([]uint64, count)
	for i := 0; i < count; i++ {
		var operation, requestID [16]byte
		binary.BigEndian.PutUint64(operation[8:], uint64(i+1))
		binary.BigEndian.PutUint64(requestID[8:], uint64(i+1))
		requests[i] = requestID
		seq := uint64(i*2 + 1)
		permit := testRecord(Permit, seq, 100_000_000_000, operation, requestID, previous)
		permitFrame, err := EncodeFrame(permit, testKey)
		if err != nil {
			t.Fatal(err)
		}
		journal = append(journal, permitFrame...)
		previous = FrameDigest(permitFrame)
		issued := testRecord(Issued, seq+1, 100_000_000_000, operation, requestID, previous)
		issued.Suffix = make([]byte, 59)
		binary.BigEndian.PutUint64(issued.Suffix[:8], seq)
		cipher := []byte{byte(i % 251)}
		issuedHashes[i] = sha256.Sum256(cipher)
		copy(issued.Suffix[8:40], issuedHashes[i][:])
		binary.BigEndian.PutUint64(issued.Suffix[40:48], 100)
		binary.BigEndian.PutUint64(issued.Suffix[48:56], 200)
		binary.BigEndian.PutUint16(issued.Suffix[56:58], 1)
		issued.Suffix[58] = cipher[0]
		issuedSeqs[i] = seq + 1
		issuedFrame, err := EncodeFrame(issued, testKey)
		if err != nil {
			t.Fatal(err)
		}
		journal = append(journal, issuedFrame...)
		previous = FrameDigest(issuedFrame)
	}
	for i := 0; i < count; i++ {
		var settlementOperation [16]byte
		settlementOperation[0] = 0xff
		binary.BigEndian.PutUint64(settlementOperation[8:], uint64(i+1))
		settle := testRecord(Bound, uint64(count*2+i+1), 150_000_000_000, settlementOperation, requests[i], previous)
		settle.Suffix = make([]byte, 64)
		binary.BigEndian.PutUint64(settle.Suffix[:8], issuedSeqs[i])
		copy(settle.Suffix[8:40], issuedHashes[i][:])
		binary.BigEndian.PutUint64(settle.Suffix[56:64], 150_000_000_000)
		settleFrame, err := EncodeFrame(settle, testKey)
		if err != nil {
			t.Fatal(err)
		}
		journal = append(journal, settleFrame...)
		previous = FrameDigest(settleFrame)
	}
	result, err := Replay(journal, keys)
	if err != nil || len(result.Records) != count*3 || result.Burned != 0 {
		t.Fatalf("large replay = records:%d burned:%d err:%v", len(result.Records), result.Burned, err)
	}
}

func bytesRepeat(value byte, count int) []byte {
	data := make([]byte, count)
	for i := range data {
		data[i] = value
	}
	return data
}

func TestReplayPermitOnlyIsBurnedAndIssueCanSettleOnce(t *testing.T) {
	var op, request [16]byte
	op[0], request[0] = 4, 5
	permit := testRecord(Permit, 1, 100_000_000_000, op, request, [32]byte{})
	permitFrame, err := EncodeFrame(permit, testKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[[16]byte][]byte{permit.KeyID: testKey}
	result, err := Replay(permitFrame, keys)
	if err != nil || result.Burned != 1 {
		t.Fatalf("PERMIT replay = (%+v, %v), want burned", result, err)
	}

	issued := testRecord(Issued, 2, 100_000_000_000, op, request, FrameDigest(permitFrame))
	issued.Suffix = make([]byte, 59)
	binary.BigEndian.PutUint64(issued.Suffix[:8], permit.Sequence)
	handleHash := sha256.Sum256([]byte{0x7f})
	copy(issued.Suffix[8:40], handleHash[:])
	binary.BigEndian.PutUint64(issued.Suffix[40:48], 100)
	binary.BigEndian.PutUint64(issued.Suffix[48:56], 200)
	binary.BigEndian.PutUint16(issued.Suffix[56:58], 1)
	issued.Suffix[58] = 0x7f
	issuedFrame, err := EncodeFrame(issued, testKey)
	if err != nil {
		t.Fatal(err)
	}

	bound := testRecord(Bound, 3, 150_000_000_000, [16]byte{9}, request, FrameDigest(issuedFrame))
	bound.Suffix = make([]byte, 64)
	binary.BigEndian.PutUint64(bound.Suffix[:8], issued.Sequence)
	copy(bound.Suffix[8:40], issued.Suffix[8:40])
	binary.BigEndian.PutUint64(bound.Suffix[56:64], 150_000_000_000)
	frame3, err := EncodeFrame(bound, testKey)
	if err != nil {
		t.Fatal(err)
	}

	result, err = Replay(append(append(permitFrame, issuedFrame...), frame3...), keys)
	if err != nil || result.Burned != 0 || len(result.Records) != 3 {
		t.Fatalf("settled replay = (%+v, %v)", result, err)
	}
	for i, record := range result.Records {
		if len(record.Suffix) != 0 {
			t.Fatalf("replay metadata record %d retained suffix bytes", i)
		}
	}
	if result.Records[1].JournalOffset != int64(len(permitFrame)) || result.Records[1].FrameLength != uint16(len(issuedFrame)) {
		t.Fatalf("ISSUED replay location = (%d, %d), want (%d, %d)", result.Records[1].JournalOffset, result.Records[1].FrameLength, len(permitFrame), len(issuedFrame))
	}
	if _, err := Replay(append(append(append(permitFrame, issuedFrame...), frame3...), frame3...), keys); err == nil {
		t.Fatal("duplicate settlement accepted")
	}
}

func TestReplayRejectsBrokenChainAndIssueWithoutMatchingPermit(t *testing.T) {
	var op, request [16]byte
	op[0], request[0] = 1, 2
	permit := testRecord(Permit, 1, 10, op, request, [32]byte{})
	first, err := EncodeFrame(permit, testKey)
	if err != nil {
		t.Fatal(err)
	}
	issued := testRecord(Issued, 2, 1_000_000_000, op, request, [32]byte{1})
	issued.Suffix = make([]byte, 59)
	binary.BigEndian.PutUint64(issued.Suffix[:8], 1)
	binary.BigEndian.PutUint64(issued.Suffix[40:48], 1)
	binary.BigEndian.PutUint64(issued.Suffix[48:56], 2)
	binary.BigEndian.PutUint16(issued.Suffix[56:58], 1)
	issued.Suffix[58] = 1
	secondHandleHash := sha256.Sum256([]byte{1})
	copy(issued.Suffix[8:40], secondHandleHash[:])
	second, err := EncodeFrame(issued, testKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[[16]byte][]byte{permit.KeyID: testKey}
	if _, err := Replay(append(first, second...), keys); err == nil {
		t.Fatal("broken chain accepted")
	}
	issued.Previous = FrameDigest(first)
	second, err = EncodeFrame(issued, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(second, keys); err == nil {
		t.Fatal("ISSUED without PERMIT accepted")
	}
}

// Package pilotquota implements the on-disk quota-journal frame format.
// Production minting must remain disabled until an approved monotonic anchor
// provider and the resident IPC coordinator are available.
package pilotquota

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	frameHeaderSize = 16
	commonSize      = 160
	macSize         = sha256.Size
	maxFrameSize    = 4096
	frameVersion    = 1
)

var (
	frameMagic = [8]byte{'E', 'N', 'W', 'Q', 'J', 'N', 'L', '1'}
	errFrame   = errors.New("invalid quota journal frame")
)

type RecordType uint8

const (
	Permit RecordType = iota + 1
	Issued
	Bound
	Expired
)

// Record is the authenticated fixed-prefix metadata plus a type-specific
// suffix. Key bytes are supplied separately and are never retained here.
type Record struct {
	Type          RecordType
	KeyID         [16]byte
	OperationID   [16]byte
	Sequence      uint64
	RecordTimeNS  uint64
	PrincipalRef  [32]byte
	RequestID     [16]byte
	BindingDigest [32]byte
	Previous      [32]byte
	Suffix        []byte
	// JournalOffset and FrameLength are replay metadata, not serialized fields.
	JournalOffset int64
	FrameLength   uint16
}

func (r Record) validate() error {
	switch r.Type {
	case Permit:
		if len(r.Suffix) != 0 {
			return fmt.Errorf("%w: PERMIT suffix must be empty", errFrame)
		}
	case Issued:
		// permit sequence, handle hash, issued/expiry seconds, u16 length,
		// and 1..512 bytes of ciphertext.
		if len(r.Suffix) < 59 || len(r.Suffix) > 570 {
			return fmt.Errorf("%w: ISSUED suffix length", errFrame)
		}
		sealedLen := int(binary.BigEndian.Uint16(r.Suffix[56:58]))
		if sealedLen < 1 || sealedLen > 512 || len(r.Suffix) != 58+sealedLen {
			return fmt.Errorf("%w: ISSUED ciphertext length", errFrame)
		}
		sealedHash := sha256.Sum256(r.Suffix[58:])
		if !hmac.Equal(sealedHash[:], r.Suffix[8:40]) {
			return fmt.Errorf("%w: ISSUED ciphertext hash", errFrame)
		}
		issuedAtNS, issuedOK := secondsToNS(binary.BigEndian.Uint64(r.Suffix[40:48]))
		expiresAtNS, expiresOK := secondsToNS(binary.BigEndian.Uint64(r.Suffix[48:56]))
		if !issuedOK || !expiresOK || expiresAtNS <= issuedAtNS {
			return fmt.Errorf("%w: ISSUED timestamp order", errFrame)
		}
		if r.RecordTimeNS < issuedAtNS || r.RecordTimeNS >= expiresAtNS {
			return fmt.Errorf("%w: ISSUED record time outside lifetime", errFrame)
		}
	case Bound:
		if len(r.Suffix) != 64 {
			return fmt.Errorf("%w: BOUND suffix length", errFrame)
		}
		if binary.BigEndian.Uint64(r.Suffix[56:64]) > math.MaxInt64 {
			return fmt.Errorf("%w: BOUND event timestamp out of range", errFrame)
		}
		if r.RecordTimeNS < binary.BigEndian.Uint64(r.Suffix[56:64]) {
			return fmt.Errorf("%w: BOUND append precedes event", errFrame)
		}
	case Expired:
		if len(r.Suffix) != 64 {
			return fmt.Errorf("%w: EXPIRED suffix length", errFrame)
		}
		issuedAtNS, issuedOK := secondsToNS(binary.BigEndian.Uint64(r.Suffix[40:48]))
		expiresAtNS, expiresOK := secondsToNS(binary.BigEndian.Uint64(r.Suffix[48:56]))
		eventTimeNS := binary.BigEndian.Uint64(r.Suffix[56:64])
		if !issuedOK || !expiresOK || expiresAtNS <= issuedAtNS || eventTimeNS > math.MaxInt64 {
			return fmt.Errorf("%w: EXPIRED timestamp order", errFrame)
		}
		if eventTimeNS < expiresAtNS || r.RecordTimeNS < eventTimeNS {
			return fmt.Errorf("%w: EXPIRED event precedes expiry or append", errFrame)
		}
	default:
		return fmt.Errorf("%w: unknown record type %d", errFrame, r.Type)
	}
	if r.Sequence == 0 || r.RecordTimeNS > math.MaxInt64 {
		return fmt.Errorf("%w: sequence or timestamp out of range", errFrame)
	}
	return nil
}

func secondsToNS(seconds uint64) (uint64, bool) {
	const maxUnixSeconds = uint64(math.MaxInt64 / 1_000_000_000)
	if seconds > maxUnixSeconds {
		return 0, false
	}
	return seconds * 1_000_000_000, true
}

// EncodeFrame serializes a record and authenticates the complete header and
// body (excluding only the trailing MAC) with the selected journal key.
func EncodeFrame(r Record, key []byte) ([]byte, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("%w: journal key too short", errFrame)
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	total := frameHeaderSize + commonSize + len(r.Suffix) + macSize
	if total > maxFrameSize {
		return nil, fmt.Errorf("%w: frame exceeds limit", errFrame)
	}
	out := make([]byte, total)
	copy(out[:8], frameMagic[:])
	binary.BigEndian.PutUint16(out[8:10], frameVersion)
	out[10] = byte(r.Type)
	binary.BigEndian.PutUint32(out[12:16], uint32(total))
	b := out[frameHeaderSize : total-macSize]
	copy(b[0:16], r.KeyID[:])
	copy(b[16:32], r.OperationID[:])
	binary.BigEndian.PutUint64(b[32:40], r.Sequence)
	binary.BigEndian.PutUint64(b[40:48], r.RecordTimeNS)
	copy(b[48:80], r.PrincipalRef[:])
	copy(b[80:96], r.RequestID[:])
	copy(b[96:128], r.BindingDigest[:])
	copy(b[128:160], r.Previous[:])
	copy(b[commonSize:], r.Suffix)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("en-words-intent-quota-v1\x00"))
	_, _ = mac.Write(out[:total-macSize])
	copy(out[total-macSize:], mac.Sum(nil))
	return out, nil
}

// DecodeFrame validates a complete single frame. Chain and state-transition
// validation are performed by Replay, which is intentionally separate.
func DecodeFrame(frame, key []byte) (Record, error) {
	var r Record
	if len(key) < 32 || len(frame) < frameHeaderSize+commonSize+macSize || len(frame) > maxFrameSize {
		return r, errFrame
	}
	if string(frame[:8]) != string(frameMagic[:]) || binary.BigEndian.Uint16(frame[8:10]) != frameVersion || frame[11] != 0 || int(binary.BigEndian.Uint32(frame[12:16])) != len(frame) {
		return r, errFrame
	}
	typ := RecordType(frame[10])
	if typ < Permit || typ > Expired {
		return r, errFrame
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("en-words-intent-quota-v1\x00"))
	_, _ = mac.Write(frame[:len(frame)-macSize])
	if !hmac.Equal(frame[len(frame)-macSize:], mac.Sum(nil)) {
		return r, fmt.Errorf("%w: MAC mismatch", errFrame)
	}
	b := frame[frameHeaderSize : len(frame)-macSize]
	r.Type = typ
	copy(r.KeyID[:], b[0:16])
	copy(r.OperationID[:], b[16:32])
	r.Sequence = binary.BigEndian.Uint64(b[32:40])
	r.RecordTimeNS = binary.BigEndian.Uint64(b[40:48])
	copy(r.PrincipalRef[:], b[48:80])
	copy(r.RequestID[:], b[80:96])
	copy(r.BindingDigest[:], b[96:128])
	copy(r.Previous[:], b[128:160])
	r.Suffix = append([]byte(nil), b[commonSize:]...)
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}

// FrameDigest returns the chain digest for an already authenticated frame.
func FrameDigest(frame []byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("en-words-intent-quota-frame-v1\x00"))
	_, _ = h.Write(frame)
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

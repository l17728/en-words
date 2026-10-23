package pilotquota

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	maxJournalBytes = 64 << 20
	maxJournalRows  = 1_000_000
	minFrameSize    = frameHeaderSize + commonSize + macSize
)

var errReplay = errors.New("invalid quota journal replay")

type ReplayResult struct {
	Records []Record
	Head    [32]byte
	Bytes   int
	// BURNED counts permits without an ISSUED record. These remain spent.
	Burned int
}

type replayRequest struct {
	permit         Record
	issued         bool
	settled        bool
	handleHash     [32]byte
	issuedSequence uint64
	issuedAt       uint64
	expiresAt      uint64
}

// Replay validates framing, HMACs, the hash chain, monotonically increasing
// sequence/time, and the core PERMIT -> ISSUED -> settlement relationships.
// It never repairs or truncates a malformed journal.
func Replay(data []byte, keys map[[16]byte][]byte) (ReplayResult, error) {
	var result ReplayResult
	if len(data) > maxJournalBytes {
		return result, fmt.Errorf("%w: journal exceeds byte cap", errReplay)
	}
	requests := make(map[[16]byte]*replayRequest)
	issuedBySequence := make(map[uint64]*replayRequest)
	operations := make(map[[16]byte]RecordType)
	var previous [32]byte
	var lastTime uint64
	for offset := 0; offset < len(data); {
		if len(result.Records) >= maxJournalRows || len(data)-offset < frameHeaderSize {
			return ReplayResult{}, fmt.Errorf("%w: truncated header or row cap", errReplay)
		}
		header := data[offset : offset+frameHeaderSize]
		length := int(binary.BigEndian.Uint32(header[12:16]))
		if length < frameHeaderSize+commonSize+macSize || length > maxFrameSize || length > len(data)-offset {
			return ReplayResult{}, fmt.Errorf("%w: invalid frame length", errReplay)
		}
		frame := data[offset : offset+length]
		// Read key ID only after validating the minimum header/body envelope.
		var keyID [16]byte
		copy(keyID[:], frame[frameHeaderSize:frameHeaderSize+16])
		key, ok := keys[keyID]
		if !ok {
			return ReplayResult{}, fmt.Errorf("%w: unknown key id", errReplay)
		}
		record, err := DecodeFrame(frame, key)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("%w at byte %d: %v", errReplay, offset, err)
		}
		wantSequence := uint64(len(result.Records) + 1)
		if record.Sequence != wantSequence || record.RecordTimeNS < lastTime || !bytes.Equal(record.Previous[:], previous[:]) {
			return ReplayResult{}, fmt.Errorf("%w: sequence, time, or chain mismatch", errReplay)
		}
		if err := validateTransition(record, requests, issuedBySequence, operations); err != nil {
			return ReplayResult{}, err
		}
		previous = FrameDigest(frame)
		lastTime = record.RecordTimeNS
		// Keep only bounded location metadata. Callers that need an anchored
		// replayed handle must reread and reverify the exact frame at this offset.
		record.JournalOffset = int64(offset)
		record.FrameLength = uint16(length)
		record.Suffix = nil
		result.Records = append(result.Records, record)
		offset += length
	}
	for _, request := range requests {
		if !request.issued {
			result.Burned++
		}
	}
	result.Head = previous
	result.Bytes = len(data)
	return result, nil
}

func validateTransition(r Record, requests map[[16]byte]*replayRequest, issuedBySequence map[uint64]*replayRequest, operations map[[16]byte]RecordType) error {
	priorType, opExists := operations[r.OperationID]
	if opExists {
		// PERMIT and its ISSUE are the only legal pair sharing an operation ID.
		if !(priorType == Permit && r.Type == Issued) {
			return fmt.Errorf("%w: duplicate operation id", errReplay)
		}
	}
	var request *replayRequest
	switch r.Type {
	case Permit:
		if _, exists := requests[r.RequestID]; exists || opExists {
			return fmt.Errorf("%w: duplicate permit", errReplay)
		}
		requests[r.RequestID] = &replayRequest{permit: r}
		operations[r.OperationID] = r.Type
	case Issued:
		request = requests[r.RequestID]
		if request == nil || request.issued || request.settled || !opExists || priorType != Permit ||
			request.permit.OperationID != r.OperationID || request.permit.PrincipalRef != r.PrincipalRef ||
			request.permit.BindingDigest != r.BindingDigest || binary.BigEndian.Uint64(r.Suffix[:8]) != request.permit.Sequence {
			return fmt.Errorf("%w: ISSUE has no exact PERMIT", errReplay)
		}
		copy(request.handleHash[:], r.Suffix[8:40])
		request.issuedSequence = r.Sequence
		request.issuedAt = binary.BigEndian.Uint64(r.Suffix[40:48])
		request.expiresAt = binary.BigEndian.Uint64(r.Suffix[48:56])
		request.issued = true
		issuedBySequence[request.issuedSequence] = request
		operations[r.OperationID] = r.Type
	case Bound, Expired:
		issuedSequence := binary.BigEndian.Uint64(r.Suffix[:8])
		matched := issuedBySequence[issuedSequence]
		if matched == nil || matched.settled || matched.permit.RequestID != r.RequestID || matched.permit.PrincipalRef != r.PrincipalRef || matched.permit.BindingDigest != r.BindingDigest || !bytes.Equal(matched.handleHash[:], r.Suffix[8:40]) {
			return fmt.Errorf("%w: settlement has no eligible ISSUE", errReplay)
		}
		issuedNS, issuedOK := secondsToNS(matched.issuedAt)
		expiresNS, expiresOK := secondsToNS(matched.expiresAt)
		eventTimeNS := binary.BigEndian.Uint64(r.Suffix[56:64])
		if !issuedOK || !expiresOK || r.RecordTimeNS > math.MaxInt64 || eventTimeNS > math.MaxInt64 {
			return fmt.Errorf("%w: settlement timestamp out of range", errReplay)
		}
		if r.Type == Bound && (eventTimeNS < issuedNS || eventTimeNS > expiresNS || r.RecordTimeNS < eventTimeNS) {
			return fmt.Errorf("%w: BOUND event outside issue lifetime", errReplay)
		}
		if r.Type == Expired && (binary.BigEndian.Uint64(r.Suffix[40:48]) != matched.issuedAt || binary.BigEndian.Uint64(r.Suffix[48:56]) != matched.expiresAt || eventTimeNS < expiresNS || r.RecordTimeNS < eventTimeNS) {
			return fmt.Errorf("%w: EXPIRED timestamps do not match ISSUE", errReplay)
		}
		matched.settled = true
		operations[r.OperationID] = r.Type
	}
	return nil
}

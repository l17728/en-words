//go:build pilot_e2e

package store

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"sync"
)

const (
	pilotCatalogSchemaVersion = 1
	pilotCatalogMaxPayload    = 256
	pilotCatalogMaxBytes      = 16 << 20
	pilotCatalogMaxFrames     = 1 << 16
	minPilotCatalogFrameSize  = 20
)

var (
	errPilotCatalogIntegrity  = errors.New("pilot catalog integrity error")
	errPilotCatalogLimit      = errors.New("pilot catalog limit exceeded")
	errPilotCatalogTransition = errors.New("pilot catalog transition rejected")
	errPilotCatalogPoisoned   = errors.New("pilot catalog supervisor poisoned")
)

var (
	pilotCatalogMagic    = [4]byte{'D', 'W', 'C', '1'}
	pilotCatalogTrailer  = [4]byte{'1', 'C', 'W', 'D'}
	pilotCatalogCRCTable = crc32.MakeTable(crc32.Castagnoli)
)

type pilotCatalogState string

const (
	pilotCatalogProvisioning         pilotCatalogState = "provisioning"
	pilotCatalogReady                pilotCatalogState = "ready"
	pilotCatalogOpenRecoveryRequired pilotCatalogState = "open_recovery_required"
	pilotCatalogCommitted            pilotCatalogState = "committed"
	pilotCatalogTerminalFailed       pilotCatalogState = "terminal_failed"
	pilotCatalogQuarantined          pilotCatalogState = "quarantined"
	pilotCatalogCleanupQuarantined   pilotCatalogState = "cleanup_quarantined"
	pilotCatalogDeleted              pilotCatalogState = "deleted"
)

type pilotCatalogRecord struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    uint64            `json:"generation"`
	CaseID        string            `json:"case_id"`
	State         pilotCatalogState `json:"state"`
}

// The supervisor supplies OpenVerified handles and OS locks. This layer never
// opens a path and must not be treated as a filesystem security boundary.
type pilotCatalogLock interface {
	Lock() error
	Unlock() error
}

type pilotCatalogJournal struct {
	mu         sync.Mutex
	file       pilotSidecarFile
	lock       pilotCatalogLock
	syncParent func() error
	health     *pilotCatalogHealth
}

// pilotCatalogHealth is owned by one supervisor and shared by all journal
// handles it issues. Any ambiguous persistence failure closes every handle.
type pilotCatalogHealth struct {
	operation sync.Mutex
	mu        sync.Mutex
	poisoned  bool
}

func (health *pilotCatalogHealth) isPoisoned() bool {
	if health == nil {
		return true
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	return health.poisoned
}

func (health *pilotCatalogHealth) poison() {
	if health == nil {
		return
	}
	health.mu.Lock()
	health.poisoned = true
	health.mu.Unlock()
}

// openPilotCatalogFromVerifiedHandle is called only after the supervisor has
// validated the catalog path, owner, ACL/mode and file identity. The caller
// holds the bootstrap lock. Startup replays and syncs while holding the global
// catalog lock before returning an authorized journal handle.
func openPilotCatalogFromVerifiedHandle(file pilotSidecarFile, lock pilotCatalogLock, syncParent func() error, health *pilotCatalogHealth) (*pilotCatalogJournal, error) {
	if file == nil || lock == nil || syncParent == nil || health == nil {
		return nil, errPilotCatalogIntegrity
	}
	health.operation.Lock()
	defer health.operation.Unlock()
	if health.isPoisoned() {
		return nil, errPilotCatalogPoisoned
	}
	journal := &pilotCatalogJournal{file: file, lock: lock, syncParent: syncParent, health: health}
	if err := lock.Lock(); err != nil {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	if health.isPoisoned() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			health.poison()
			return nil, errPilotCatalogIntegrity
		}
		return nil, errPilotCatalogPoisoned
	}
	_, err := journal.readLocked()
	if err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = syncParent()
	}
	if err != nil {
		health.poison()
	}
	if unlockErr := lock.Unlock(); unlockErr != nil {
		err = errPilotCatalogIntegrity
		health.poison()
	}
	if err != nil {
		return nil, errPilotCatalogIntegrity
	}
	return journal, nil
}

// createPilotCatalogWithFirstCase is used only by the exclusive bootstrap
// winner. The supplied handle must refer to a file opened with CREATE_NEW.
// The first durable frame reserves the case before any case-specific artifact
// is created; callers must retain the generated case ID privately.
func createPilotCatalogWithFirstCase(file pilotSidecarFile, lock pilotCatalogLock, firstCaseID string, syncParent func() error, health *pilotCatalogHealth) (result *pilotCatalogJournal, resultErr error) {
	if file == nil || lock == nil || syncParent == nil || health == nil || !validPilotCatalogCaseID(firstCaseID) {
		return nil, errPilotCatalogIntegrity
	}
	health.operation.Lock()
	defer health.operation.Unlock()
	if health.isPoisoned() {
		return nil, errPilotCatalogPoisoned
	}
	if err := lock.Lock(); err != nil {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	if health.isPoisoned() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			health.poison()
			return nil, errPilotCatalogIntegrity
		}
		return nil, errPilotCatalogPoisoned
	}
	defer func() {
		if err := lock.Unlock(); err != nil {
			result = nil
			resultErr = errPilotCatalogIntegrity
			health.poison()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	frame, err := encodePilotCatalogFrame(pilotCatalogRecord{
		SchemaVersion: pilotCatalogSchemaVersion,
		Generation:    1,
		CaseID:        firstCaseID,
		State:         pilotCatalogProvisioning,
	})
	if err != nil {
		health.poison()
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	n, err := file.Write(frame)
	if err != nil || n != len(frame) {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	if err = file.Sync(); err != nil {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	if err = syncParent(); err != nil {
		health.poison()
		return nil, errPilotCatalogIntegrity
	}
	return &pilotCatalogJournal{file: file, lock: lock, syncParent: syncParent, health: health}, nil
}

func (journal *pilotCatalogJournal) Snapshot() ([]pilotCatalogRecord, error) {
	if journal == nil || journal.health == nil {
		return nil, errPilotCatalogIntegrity
	}
	journal.health.operation.Lock()
	defer journal.health.operation.Unlock()
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.health.isPoisoned() || journal.file == nil || journal.lock == nil {
		return nil, errPilotCatalogPoisoned
	}
	if err := journal.lock.Lock(); err != nil {
		journal.health.poison()
		return nil, errPilotCatalogIntegrity
	}
	if journal.health.isPoisoned() {
		if unlockErr := journal.lock.Unlock(); unlockErr != nil {
			journal.health.poison()
			return nil, errPilotCatalogIntegrity
		}
		return nil, errPilotCatalogPoisoned
	}
	records, err := journal.readLocked()
	if err != nil {
		journal.health.poison()
	}
	if unlockErr := journal.lock.Unlock(); unlockErr != nil {
		err = errPilotCatalogIntegrity
		journal.health.poison()
	}
	if err != nil {
		return nil, errPilotCatalogIntegrity
	}
	return append([]pilotCatalogRecord(nil), records...), nil
}

func (journal *pilotCatalogJournal) Transition(caseID string, next pilotCatalogState) (resultErr error) {
	if journal == nil || journal.health == nil || !validPilotCatalogCaseID(caseID) || !validPilotCatalogState(next) {
		return errPilotCatalogIntegrity
	}
	journal.health.operation.Lock()
	defer journal.health.operation.Unlock()
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.health.isPoisoned() || journal.file == nil || journal.lock == nil {
		return errPilotCatalogPoisoned
	}
	if err := journal.lock.Lock(); err != nil {
		journal.health.poison()
		return errPilotCatalogIntegrity
	}
	defer func() {
		if err := journal.lock.Unlock(); err != nil {
			journal.health.poison()
			resultErr = errPilotCatalogIntegrity
		}
	}()
	if journal.health.isPoisoned() {
		return errPilotCatalogPoisoned
	}
	records, err := journal.readLocked()
	if err != nil {
		journal.health.poison()
		return errPilotCatalogIntegrity
	}
	if len(records) >= pilotCatalogMaxFrames {
		journal.health.poison()
		return errPilotCatalogLimit
	}
	info, err := journal.file.Stat()
	if err != nil {
		journal.health.poison()
		return errPilotCatalogIntegrity
	}
	prior := pilotCatalogState("")
	caseWasUsed := false
	for _, record := range records {
		if record.CaseID == caseID {
			prior = record.State
			caseWasUsed = true
		}
	}
	if (!caseWasUsed && next != pilotCatalogProvisioning) ||
		(caseWasUsed && !nextPilotCatalogState(prior, next)) {
		return errPilotCatalogTransition
	}
	nextRecord := pilotCatalogRecord{
		SchemaVersion: pilotCatalogSchemaVersion,
		Generation:    uint64(len(records) + 1),
		CaseID:        caseID,
		State:         next,
	}
	frame, err := encodePilotCatalogFrame(nextRecord)
	if err != nil {
		return err
	}
	if info.Size()+int64(len(frame)) > pilotCatalogMaxBytes {
		journal.health.poison()
		return errPilotCatalogLimit
	}
	if _, err = journal.file.Seek(0, io.SeekEnd); err != nil {
		journal.health.poison()
		return errPilotCatalogIntegrity
	}
	n, writeErr := journal.file.Write(frame)
	if writeErr != nil || n != len(frame) {
		journal.health.poison()
		return errPilotCatalogIntegrity
	}
	if err = journal.file.Sync(); err != nil {
		journal.health.poison()
		return errPilotCatalogIntegrity
	}
	return nil
}
func (journal *pilotCatalogJournal) readLocked() ([]pilotCatalogRecord, error) {
	info, err := journal.file.Stat()
	if err != nil || !info.Mode().IsRegular() || validatePilotCatalogByteSize(info.Size()) != nil {
		return nil, errPilotCatalogIntegrity
	}
	if _, err := journal.file.Seek(0, io.SeekStart); err != nil {
		return nil, errPilotCatalogIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(journal.file, int64(pilotCatalogMaxBytes)+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errPilotCatalogIntegrity
	}
	return replayPilotCatalog(data)
}

func validPilotCatalogCaseID(id string) bool {
	if len(id) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 {
		return false
	}
	return hex.EncodeToString(decoded) == id
}

func validPilotCatalogState(state pilotCatalogState) bool {
	switch state {
	case pilotCatalogProvisioning, pilotCatalogReady, pilotCatalogOpenRecoveryRequired,
		pilotCatalogCommitted, pilotCatalogTerminalFailed, pilotCatalogQuarantined,
		pilotCatalogCleanupQuarantined, pilotCatalogDeleted:
		return true
	default:
		return false
	}
}

func validatePilotCatalogRecord(record pilotCatalogRecord) error {
	if record.SchemaVersion != pilotCatalogSchemaVersion || record.Generation == 0 ||
		!validPilotCatalogCaseID(record.CaseID) || !validPilotCatalogState(record.State) {
		return errPilotCatalogIntegrity
	}
	return nil
}

func encodePilotCatalogRecord(record pilotCatalogRecord) ([]byte, error) {
	if err := validatePilotCatalogRecord(record); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(record)
	if err != nil || len(payload) == 0 || len(payload) > pilotCatalogMaxPayload {
		return nil, errPilotCatalogIntegrity
	}
	return payload, nil
}

func encodePilotCatalogFrame(record pilotCatalogRecord) ([]byte, error) {
	payload, err := encodePilotCatalogRecord(record)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, 20+len(payload))
	frame = append(frame, pilotCatalogMagic[:]...)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	frame = append(frame, length[:]...)
	frame = append(frame, payload...)
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.Checksum(payload, pilotCatalogCRCTable))
	frame = append(frame, checksum[:]...)
	frame = append(frame, length[:]...)
	frame = append(frame, pilotCatalogTrailer[:]...)
	return frame, nil
}

func decodePilotCatalogFrame(data []byte) (pilotCatalogRecord, int, error) {
	var zero pilotCatalogRecord
	if len(data) < minPilotCatalogFrameSize || !bytes.Equal(data[:4], pilotCatalogMagic[:]) {
		return zero, 0, errPilotCatalogIntegrity
	}
	payloadLength := binary.BigEndian.Uint32(data[4:8])
	if payloadLength == 0 || payloadLength > pilotCatalogMaxPayload {
		return zero, 0, errPilotCatalogIntegrity
	}
	frameLength := 20 + int(payloadLength)
	if len(data) < frameLength {
		return zero, 0, errPilotCatalogIntegrity
	}
	payloadEnd := 8 + int(payloadLength)
	payload := data[8:payloadEnd]
	checksum := binary.BigEndian.Uint32(data[payloadEnd : payloadEnd+4])
	if checksum != crc32.Checksum(payload, pilotCatalogCRCTable) ||
		binary.BigEndian.Uint32(data[payloadEnd+4:payloadEnd+8]) != payloadLength ||
		!bytes.Equal(data[payloadEnd+8:payloadEnd+12], pilotCatalogTrailer[:]) {
		return zero, 0, errPilotCatalogIntegrity
	}
	if pilotAuditJSON(payload) != nil {
		return zero, 0, errPilotCatalogIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&zero); err != nil {
		return pilotCatalogRecord{}, 0, errPilotCatalogIntegrity
	}
	canonical, err := encodePilotCatalogRecord(zero)
	if err != nil || !bytes.Equal(canonical, payload) {
		return pilotCatalogRecord{}, 0, errPilotCatalogIntegrity
	}
	return zero, frameLength, nil
}

func nextPilotCatalogState(previous pilotCatalogState, next pilotCatalogState) bool {
	switch previous {
	case "":
		return next == pilotCatalogProvisioning
	case pilotCatalogProvisioning:
		return next == pilotCatalogReady || next == pilotCatalogQuarantined
	case pilotCatalogReady:
		return next == pilotCatalogOpenRecoveryRequired || next == pilotCatalogCleanupQuarantined
	case pilotCatalogOpenRecoveryRequired:
		return next == pilotCatalogCommitted || next == pilotCatalogTerminalFailed ||
			next == pilotCatalogQuarantined || next == pilotCatalogCleanupQuarantined
	case pilotCatalogCommitted, pilotCatalogTerminalFailed, pilotCatalogQuarantined:
		return next == pilotCatalogCleanupQuarantined
	case pilotCatalogCleanupQuarantined:
		return next == pilotCatalogDeleted
	default:
		return false
	}
}

func replayPilotCatalog(data []byte) ([]pilotCatalogRecord, error) {
	if err := validatePilotCatalogByteSize(int64(len(data))); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errPilotCatalogIntegrity
	}
	records := make([]pilotCatalogRecord, 0, 32)
	states := make(map[string]pilotCatalogState)
	usedIDs := make(map[string]struct{})
	for offset := 0; offset < len(data); {
		if len(records) >= pilotCatalogMaxFrames {
			return nil, errPilotCatalogLimit
		}
		record, size, err := decodePilotCatalogFrame(data[offset:])
		if err != nil {
			return nil, errPilotCatalogIntegrity
		}
		if record.Generation != uint64(len(records)+1) {
			return nil, errPilotCatalogIntegrity
		}
		prior, exists := states[record.CaseID]
		if exists {
			if _, used := usedIDs[record.CaseID]; !used || !nextPilotCatalogState(prior, record.State) {
				return nil, errPilotCatalogIntegrity
			}
		} else {
			if _, used := usedIDs[record.CaseID]; used || !nextPilotCatalogState("", record.State) {
				return nil, errPilotCatalogIntegrity
			}
			usedIDs[record.CaseID] = struct{}{}
		}
		states[record.CaseID] = record.State
		records = append(records, record)
		offset += size
	}
	return records, nil
}

func validatePilotCatalogByteSize(size int64) error {
	if size < 0 || size == 0 {
		return errPilotCatalogIntegrity
	}
	if size > pilotCatalogMaxBytes {
		return errPilotCatalogLimit
	}
	return nil
}

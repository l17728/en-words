//go:build pilot_e2e

package store

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const catalogTestCaseA = "00112233445566778899aabbccddeeff"

func mustCatalogFrame(t *testing.T, generation uint64, caseID string, state pilotCatalogState) []byte {
	t.Helper()
	frame, err := encodePilotCatalogFrame(pilotCatalogRecord{
		SchemaVersion: pilotCatalogSchemaVersion,
		Generation:    generation,
		CaseID:        caseID,
		State:         state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestPilotCatalogFrameGoldenAndStrictDecode(t *testing.T) {
	record := pilotCatalogRecord{
		SchemaVersion: pilotCatalogSchemaVersion,
		Generation:    1,
		CaseID:        catalogTestCaseA,
		State:         pilotCatalogProvisioning,
	}
	frame, err := encodePilotCatalogFrame(record)
	if err != nil {
		t.Fatal(err)
	}
	const want = "4457433100000067" +
		"7b22736368656d615f76657273696f6e223a312c2267656e65726174696f6e223a312c22636173655f6964223a223030313132323333343435353636373738383939616162626363646465656666222c227374617465223a2270726f766973696f6e696e67227d" +
		"a02388590000006731435744"
	if got := hex.EncodeToString(frame); got != want {
		t.Fatalf("golden frame mismatch\n got %s\nwant %s", got, want)
	}
	decoded, size, err := decodePilotCatalogFrame(frame)
	if err != nil || size != len(frame) || decoded != record {
		t.Fatalf("decode = (%+v, %d, %v), want (%+v, %d, nil)", decoded, size, err, record, len(frame))
	}
}

func TestPilotCatalogFrameRejectsEveryTruncationAndCorruption(t *testing.T) {
	frame := mustCatalogFrame(t, 1, catalogTestCaseA, pilotCatalogProvisioning)
	for cut := 0; cut < len(frame); cut++ {
		if _, _, err := decodePilotCatalogFrame(frame[:cut]); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("prefix length %d accepted: %v", cut, err)
		}
	}
	for _, index := range []int{0, 4, 7, 8, len(frame) - 12, len(frame) - 8, len(frame) - 4, len(frame) - 1} {
		corrupt := append([]byte(nil), frame...)
		corrupt[index] ^= 0x01
		if _, _, err := decodePilotCatalogFrame(corrupt); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("corruption at byte %d accepted: %v", index, err)
		}
	}
}

func TestPilotCatalogReplayTransitionsAndInterleavedCases(t *testing.T) {
	caseB := "ffeeddccbbaa99887766554433221100"
	states := []struct {
		id    string
		state pilotCatalogState
	}{
		{catalogTestCaseA, pilotCatalogProvisioning},
		{caseB, pilotCatalogProvisioning},
		{catalogTestCaseA, pilotCatalogReady},
		{caseB, pilotCatalogQuarantined},
		{catalogTestCaseA, pilotCatalogOpenRecoveryRequired},
		{caseB, pilotCatalogCleanupQuarantined},
		{catalogTestCaseA, pilotCatalogCommitted},
		{caseB, pilotCatalogDeleted},
		{catalogTestCaseA, pilotCatalogCleanupQuarantined},
		{catalogTestCaseA, pilotCatalogDeleted},
	}
	var journal []byte
	for index, item := range states {
		journal = append(journal, mustCatalogFrame(t, uint64(index+1), item.id, item.state)...)
	}
	records, err := replayPilotCatalog(journal)
	if err != nil || len(records) != len(states) {
		t.Fatalf("replay = %d records, %v; want %d", len(records), err, len(states))
	}
}

func TestPilotCatalogReplayRejectsIllegalTransitionsAndGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []pilotCatalogState
	}{
		{"must begin provisioning", []pilotCatalogState{pilotCatalogReady}},
		{"provisioning cannot commit", []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogCommitted}},
		{"ready cannot skip open", []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogReady, pilotCatalogCommitted}},
		{"cannot reopen terminal", []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogReady, pilotCatalogOpenRecoveryRequired, pilotCatalogCommitted, pilotCatalogReady}},
		{"deleted is final", []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogQuarantined, pilotCatalogCleanupQuarantined, pilotCatalogDeleted, pilotCatalogProvisioning}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var journal []byte
			for index, state := range tc.states {
				journal = append(journal, mustCatalogFrame(t, uint64(index+1), catalogTestCaseA, state)...)
			}
			if _, err := replayPilotCatalog(journal); !errors.Is(err, errPilotCatalogIntegrity) {
				t.Fatalf("invalid transition accepted: %v", err)
			}
		})
	}

	gap := append(mustCatalogFrame(t, 1, catalogTestCaseA, pilotCatalogProvisioning),
		mustCatalogFrame(t, 3, catalogTestCaseA, pilotCatalogReady)...)
	if _, err := replayPilotCatalog(gap); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("generation gap accepted: %v", err)
	}
	duplicateID := append(mustCatalogFrame(t, 1, catalogTestCaseA, pilotCatalogProvisioning),
		mustCatalogFrame(t, 2, catalogTestCaseA, pilotCatalogQuarantined)...)
	duplicateID = append(duplicateID, mustCatalogFrame(t, 3, catalogTestCaseA, pilotCatalogProvisioning)...)
	if _, err := replayPilotCatalog(duplicateID); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("case ID reuse accepted: %v", err)
	}
}

func TestPilotCatalogFramePayloadLengthAndRecordValidation(t *testing.T) {
	for _, length := range []int{1, pilotCatalogMaxPayload} {
		payload := make([]byte, length)
		frame := frameWithPilotCatalogPayload(payload)
		if _, _, err := decodePilotCatalogFrame(frame); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("invalid %d-byte record payload accepted: %v", length, err)
		}
	}
	for _, length := range []int{0, pilotCatalogMaxPayload + 1} {
		frame := frameWithPilotCatalogPayload(make([]byte, length))
		if _, _, err := decodePilotCatalogFrame(frame); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("out-of-range %d-byte frame payload accepted: %v", length, err)
		}
	}
	if _, err := encodePilotCatalogFrame(pilotCatalogRecord{SchemaVersion: 1, Generation: 1, CaseID: "ABCDEF0123456789ABCDEF0123456789", State: pilotCatalogProvisioning}); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("uppercase case ID accepted: %v", err)
	}
}

func TestPilotCatalogRejectsNonCanonicalJSONEvenWithValidChecksum(t *testing.T) {
	valid := `{"schema_version":1,"generation":1,"case_id":"00112233445566778899aabbccddeeff","state":"provisioning"}`
	for name, payload := range map[string]string{
		"reordered fields": `{"generation":1,"schema_version":1,"case_id":"00112233445566778899aabbccddeeff","state":"provisioning"}`,
		"whitespace":       valid + " ",
		"unknown field":    `{"schema_version":1,"generation":1,"case_id":"00112233445566778899aabbccddeeff","state":"provisioning","extra":true}`,
		"duplicate field":  `{"schema_version":1,"generation":1,"generation":1,"case_id":"00112233445566778899aabbccddeeff","state":"provisioning"}`,
		"trailing object":  valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodePilotCatalogFrame(frameWithPilotCatalogPayload([]byte(payload))); !errors.Is(err, errPilotCatalogIntegrity) {
				t.Fatalf("noncanonical payload accepted: %v", err)
			}
		})
	}
}

func frameWithPilotCatalogPayload(payload []byte) []byte {
	frame := make([]byte, 8, 16+len(payload))
	copy(frame[:4], pilotCatalogMagic[:])
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	frame = append(frame, payload...)
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.Checksum(payload, pilotCatalogCRCTable))
	frame = append(frame, checksum[:]...)
	frame = append(frame, frame[4:8]...)
	frame = append(frame, pilotCatalogTrailer[:]...)
	return frame
}

func TestPilotCatalogReplayFrameCountLimit(t *testing.T) {
	const cases = pilotCatalogMaxFrames / 4
	var journal []byte
	var generation uint64
	for i := 0; i < cases; i++ {
		id := fmt.Sprintf("%032x", i+1)
		for _, state := range []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogQuarantined, pilotCatalogCleanupQuarantined, pilotCatalogDeleted} {
			generation++
			journal = append(journal, mustCatalogFrame(t, generation, id, state)...)
		}
	}
	if len(journal) > pilotCatalogMaxBytes {
		t.Fatalf("fixture unexpectedly exceeds byte limit: %d", len(journal))
	}
	if records, err := replayPilotCatalog(journal); err != nil || len(records) != pilotCatalogMaxFrames {
		t.Fatalf("max frame replay = %d records, %v", len(records), err)
	}
	tooMany := append(append([]byte(nil), journal...), mustCatalogFrame(t, generation+1, "ffffffffffffffffffffffffffffffff", pilotCatalogProvisioning)...)
	if _, err := replayPilotCatalog(tooMany); !errors.Is(err, errPilotCatalogLimit) {
		t.Fatalf("journal with frame %d accepted: %v", pilotCatalogMaxFrames+1, err)
	}
}

func TestPilotCatalogFrameLimitPoisonsSupervisor(t *testing.T) {
	const cases = pilotCatalogMaxFrames / 4
	var journalBytes []byte
	var generation uint64
	for i := 0; i < cases; i++ {
		id := fmt.Sprintf("%032x", i+1)
		for _, state := range []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogQuarantined, pilotCatalogCleanupQuarantined, pilotCatalogDeleted} {
			generation++
			journalBytes = append(journalBytes, mustCatalogFrame(t, generation, id, state)...)
		}
	}
	file := newCatalogTestFile(t, nil)
	if n, err := file.Write(journalBytes); err != nil || n != len(journalBytes) {
		t.Fatalf("fixture write = %d, %v", n, err)
	}
	health := &pilotCatalogHealth{}
	lock := &catalogTestLock{}
	journal, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition("ffffffffffffffffffffffffffffffff", pilotCatalogProvisioning); !errors.Is(err, errPilotCatalogLimit) {
		t.Fatalf("limit transition = %v", err)
	}
	if _, err := journal.Snapshot(); !errors.Is(err, errPilotCatalogPoisoned) {
		t.Fatalf("snapshot after limit = %v", err)
	}
}

func TestPilotCatalogByteSizeLimitBoundaries(t *testing.T) {
	if err := validatePilotCatalogByteSize(pilotCatalogMaxBytes); err != nil {
		t.Fatalf("exact byte cap rejected: %v", err)
	}
	if err := validatePilotCatalogByteSize(pilotCatalogMaxBytes + 1); !errors.Is(err, errPilotCatalogLimit) {
		t.Fatalf("byte cap + 1 not rejected as limit: %v", err)
	}
	if err := validatePilotCatalogByteSize(0); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("empty catalog accepted: %v", err)
	}
}

type catalogTestLock struct {
	gate           sync.Mutex
	mu             sync.Mutex
	held           bool
	failLock       bool
	failUnlock     bool
	events         *[]string
	beforeGate     func()
	blockUnlock    bool
	unlockReleased chan struct{}
	unlockWait     chan struct{}
	unlockWindow   func()
}

func (l *catalogTestLock) Lock() error {
	if l.failLock {
		return errors.New("lock failed")
	}
	if l.beforeGate != nil {
		l.beforeGate()
	}
	l.gate.Lock()
	l.mu.Lock()
	l.held = true
	if l.events != nil {
		*l.events = append(*l.events, "lock")
	}
	l.mu.Unlock()
	return nil
}

func (l *catalogTestLock) Unlock() error {
	l.mu.Lock()
	l.held = false
	if l.events != nil {
		*l.events = append(*l.events, "unlock")
	}
	fail := l.failUnlock
	block := l.blockUnlock
	l.blockUnlock = false
	l.mu.Unlock()
	l.gate.Unlock()
	if block {
		close(l.unlockReleased)
		if l.unlockWindow != nil {
			l.unlockWindow()
		}
		<-l.unlockWait
	}
	if fail {
		return errors.New("unlock failed")
	}
	return nil
}

type catalogTestFile struct {
	*os.File
	syncErr          error
	readErr          error
	writeErr         error
	seekErr          error
	statErr          error
	readLimit        int
	readBytes        int
	syncBlockEntered chan struct{}
	syncBlockRelease chan struct{}
	writeLimit       int
	events           *[]string
}

func (f *catalogTestFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		err := f.readErr
		f.readErr = nil
		return 0, err
	}
	if f.readLimit >= 0 {
		if f.readBytes >= f.readLimit {
			return 0, io.EOF
		}
		if remaining := f.readLimit - f.readBytes; len(p) > remaining {
			p = p[:remaining]
		}
		n, err := f.File.Read(p)
		f.readBytes += n
		return n, err
	}
	return f.File.Read(p)
}

func (f *catalogTestFile) Write(p []byte) (int, error) {
	if f.events != nil {
		*f.events = append(*f.events, "write")
	}
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.writeLimit >= 0 && len(p) > f.writeLimit {
		return f.File.Write(p[:f.writeLimit])
	}
	return f.File.Write(p)
}

func (f *catalogTestFile) Seek(offset int64, whence int) (int64, error) {
	if f.seekErr != nil {
		return 0, f.seekErr
	}
	position, err := f.File.Seek(offset, whence)
	if err == nil && whence == io.SeekStart && offset == 0 {
		f.readBytes = 0
	}
	return position, err
}

func (f *catalogTestFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func (f *catalogTestFile) Sync() error {
	if f.events != nil {
		*f.events = append(*f.events, "sync")
	}
	if f.syncBlockEntered != nil {
		close(f.syncBlockEntered)
		<-f.syncBlockRelease
	}
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func newCatalogTestFile(t *testing.T, events *[]string) *catalogTestFile {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "catalog-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return &catalogTestFile{File: file, writeLimit: -1, readLimit: -1, events: events}
}

func TestPilotCatalogCreateAndStartupSyncBeforeAuthorization(t *testing.T) {
	var events []string
	health := &pilotCatalogHealth{}
	lock := &catalogTestLock{events: &events}
	file := newCatalogTestFile(t, &events)
	journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error {
		events = append(events, "parent-sync")
		return nil
	}, health)
	if err != nil || journal == nil || journal.lock != lock {
		t.Fatalf("create = (%v, %v), want usable journal", journal, err)
	}
	want := []string{"lock", "write", "sync", "parent-sync", "unlock"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("create order = %v, want %v", events, want)
	}

	events = nil
	opened, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error {
		events = append(events, "parent-sync")
		return nil
	}, health)
	if err != nil || opened == nil {
		t.Fatalf("open = (%v, %v)", opened, err)
	}
	want = []string{"lock", "sync", "parent-sync", "unlock"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("startup order = %v, want %v", events, want)
	}
}

func TestPilotCatalogTransitionRejectsAndAdvancesUnderGlobalLock(t *testing.T) {
	health := &pilotCatalogHealth{}
	lock := &catalogTestLock{}
	file := newCatalogTestFile(t, nil)
	journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := file.Stat()
	if err := journal.Transition(catalogTestCaseA, pilotCatalogCommitted); !errors.Is(err, errPilotCatalogTransition) {
		t.Fatalf("illegal transition = %v", err)
	}
	after, _ := file.Stat()
	if before.Size() != after.Size() {
		t.Fatalf("illegal transition appended bytes: %d -> %d", before.Size(), after.Size())
	}
	for _, state := range []pilotCatalogState{pilotCatalogReady, pilotCatalogOpenRecoveryRequired, pilotCatalogCommitted} {
		if err := journal.Transition(catalogTestCaseA, state); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	records, err := journal.Snapshot()
	if err != nil || len(records) != 4 || records[3].Generation != 4 || records[3].State != pilotCatalogCommitted {
		t.Fatalf("snapshot = (%+v, %v)", records, err)
	}
}

func TestPilotCatalogWriteAndUnlockFailuresPoisonJournal(t *testing.T) {
	t.Run("short write", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if err != nil {
			t.Fatal(err)
		}
		file.writeLimit = 1
		if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("transition = %v", err)
		}
		if !journal.health.isPoisoned() {
			t.Fatal("short write did not poison journal")
		}
		if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("poisoned retry = %v", err)
		}
	})
	t.Run("unlock error", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if err != nil {
			t.Fatal(err)
		}
		lock.failUnlock = true
		if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("transition = %v", err)
		}
		if !journal.health.isPoisoned() {
			t.Fatal("unlock failure did not poison journal")
		}
	})
}

func TestPilotCatalogConcurrentJournalsKeepOneGlobalGenerationChain(t *testing.T) {
	health := &pilotCatalogHealth{}
	lock := &catalogTestLock{}
	file := newCatalogTestFile(t, nil)
	journalA, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	journalB, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	caseB := "ffeeddccbbaa99887766554433221100"
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, item := range []struct {
		journal *pilotCatalogJournal
		id      string
	}{{journalA, catalogTestCaseA}, {journalB, caseB}} {
		item := item
		go func() {
			<-start
			states := []pilotCatalogState{pilotCatalogProvisioning, pilotCatalogQuarantined, pilotCatalogCleanupQuarantined, pilotCatalogDeleted}
			if item.id == catalogTestCaseA {
				states = []pilotCatalogState{pilotCatalogReady, pilotCatalogOpenRecoveryRequired, pilotCatalogCommitted, pilotCatalogCleanupQuarantined, pilotCatalogDeleted}
			}
			for _, state := range states {
				if err := item.journal.Transition(item.id, state); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	close(start)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	records, err := journalA.Snapshot()
	if err != nil || len(records) != 10 {
		t.Fatalf("records = %d, %v", len(records), err)
	}
	for i, record := range records {
		if record.Generation != uint64(i+1) {
			t.Fatalf("generation[%d]=%d", i, record.Generation)
		}
	}
}

func TestPilotCatalogPersistenceFailurePoisonsAllSupervisorHandles(t *testing.T) {
	health := &pilotCatalogHealth{}
	lock := &catalogTestLock{}
	file := newCatalogTestFile(t, nil)
	journalA, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	journalB, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	file.syncErr = errors.New("sync outcome uncertain")
	if err := journalA.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("uncertain write = %v", err)
	}
	before, _ := file.Stat()
	if _, err := journalB.Snapshot(); !errors.Is(err, errPilotCatalogPoisoned) {
		t.Fatalf("sibling snapshot = %v", err)
	}
	if err := journalB.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogPoisoned) {
		t.Fatalf("sibling transition = %v", err)
	}
	after, _ := file.Stat()
	if before.Size() != after.Size() {
		t.Fatalf("poisoned supervisor appended bytes: %d -> %d", before.Size(), after.Size())
	}
}

func TestPilotCatalogSerializedWaiterObservesSharedPoison(t *testing.T) {
	health := &pilotCatalogHealth{}
	lock := &catalogTestLock{}
	file := newCatalogTestFile(t, nil)
	journalA, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	journalB, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
	if err != nil {
		t.Fatal(err)
	}
	file.syncErr = errors.New("sync outcome uncertain")
	file.syncBlockEntered = make(chan struct{})
	file.syncBlockRelease = make(chan struct{})
	aResult := make(chan error, 1)
	go func() { aResult <- journalA.Transition(catalogTestCaseA, pilotCatalogReady) }()
	<-file.syncBlockEntered
	bStarted := make(chan struct{})
	bResult := make(chan error, 1)
	go func() {
		close(bStarted)
		bResult <- journalB.Transition(catalogTestCaseA, pilotCatalogReady)
	}()
	<-bStarted
	if health.operation.TryLock() {
		health.operation.Unlock()
		close(file.syncBlockRelease)
		<-aResult
		t.Fatal("supervisor operation gate was not held during catalog Sync")
	}
	beforeB, _ := file.Stat() // A has written its complete frame and is blocked in Sync.
	close(file.syncBlockRelease)
	if err := <-aResult; !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("A sync failure = %v", err)
	}
	if err := <-bResult; !errors.Is(err, errPilotCatalogPoisoned) {
		t.Fatalf("B after lock = %v", err)
	}
	afterB, _ := file.Stat()
	if afterB.Size() != beforeB.Size() {
		t.Fatalf("waiting sibling appended after global poison: %d -> %d", beforeB.Size(), afterB.Size())
	}
	if _, err := journalB.Snapshot(); !errors.Is(err, errPilotCatalogPoisoned) {
		t.Fatalf("B snapshot = %v", err)
	}
}

func TestPilotCatalogReadOrStartupFailurePoisonsBeforeUnlock(t *testing.T) {
	for _, mode := range []string{"startup sync", "snapshot read"} {
		t.Run(mode, func(t *testing.T) {
			health := &pilotCatalogHealth{}
			lock := &catalogTestLock{}
			file := newCatalogTestFile(t, nil)
			journalA, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
			if err != nil {
				t.Fatal(err)
			}
			journalB, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
			if err != nil {
				t.Fatal(err)
			}
			lock.blockUnlock = true
			lock.unlockReleased = make(chan struct{})
			lock.unlockWait = make(chan struct{})
			poisonObserved := make(chan bool, 1)
			lock.unlockWindow = func() { poisonObserved <- health.isPoisoned() }
			operationDone := make(chan error, 1)
			if mode == "startup sync" {
				file.syncErr = errors.New("startup sync failed")
				go func() {
					_, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
					operationDone <- err
				}()
			} else {
				file.readErr = errors.New("catalog read failed")
				go func() {
					_, err := journalA.Snapshot()
					operationDone <- err
				}()
			}
			<-lock.unlockReleased // Failed operation released the OS lock but has not returned.
			before, _ := file.Stat()
			if poisoned := <-poisonObserved; !poisoned {
				close(lock.unlockWait)
				<-operationDone
				t.Fatalf("%s failure was not published before OS unlock returned", mode)
			}
			after, _ := file.Stat()
			if before.Size() != after.Size() {
				close(lock.unlockWait)
				<-operationDone
				t.Fatalf("sibling appended during %s unlock window: %d -> %d", mode, before.Size(), after.Size())
			}
			close(lock.unlockWait)
			if err := <-operationDone; !errors.Is(err, errPilotCatalogIntegrity) {
				t.Fatalf("failed %s operation = %v", mode, err)
			}
			if err := journalB.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogPoisoned) {
				t.Fatalf("sibling transition after %s failure = %v", mode, err)
			}
		})
	}
}

func TestPilotCatalogCreateAndStartupFailClosedOnSyncErrors(t *testing.T) {
	t.Run("first frame sync", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		file.syncErr = errors.New("file sync failed")
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if journal != nil || !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("create = (%v, %v)", journal, err)
		}
		if lock.held {
			t.Fatal("bootstrap failure leaked global lock")
		}
	})
	t.Run("first parent sync", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return errors.New("parent sync failed") }, health)
		if journal != nil || !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("create = (%v, %v)", journal, err)
		}
		if lock.held {
			t.Fatal("bootstrap failure leaked global lock")
		}
	})
	t.Run("startup sync", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); err != nil {
			t.Fatal(err)
		}
		file.syncErr = errors.New("startup sync failed")
		journal, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health)
		if journal != nil || !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("open = (%v, %v)", journal, err)
		}
		if lock.held {
			t.Fatal("startup failure leaked global lock")
		}
	})
}

func TestPilotCatalogInvalidInputsAndPoisonedLockWindow(t *testing.T) {
	var nilHealth *pilotCatalogHealth
	if !nilHealth.isPoisoned() {
		t.Fatal("nil health must fail closed")
	}
	nilHealth.poison()
	var nilJournal *pilotCatalogJournal
	if _, err := nilJournal.Snapshot(); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("nil snapshot = %v", err)
	}
	if err := nilJournal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("nil transition = %v", err)
	}
	if _, err := replayPilotCatalog(nil); !errors.Is(err, errPilotCatalogIntegrity) {
		t.Fatalf("empty replay = %v", err)
	}
	for _, id := range []string{"", "not-hex-not-hex-not-hex-not-hex", "gggggggggggggggggggggggggggggggg", "ABCDEF0123456789ABCDEF0123456789"} {
		if validPilotCatalogCaseID(id) {
			t.Fatalf("invalid case ID accepted: %q", id)
		}
	}
	if validPilotCatalogState("unknown") {
		t.Fatal("unknown state accepted")
	}
	if _, err := encodePilotCatalogRecord(pilotCatalogRecord{SchemaVersion: 1, Generation: 1, CaseID: catalogTestCaseA, State: pilotCatalogProvisioning}); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	t.Run("argument validation", func(t *testing.T) {
		syncParent := func() error { return nil }
		for name, args := range map[string]struct {
			file   pilotSidecarFile
			lock   pilotCatalogLock
			sync   func() error
			health *pilotCatalogHealth
		}{
			"open nil file":   {nil, &catalogTestLock{}, syncParent, &pilotCatalogHealth{}},
			"open nil lock":   {newCatalogTestFile(t, nil), nil, syncParent, &pilotCatalogHealth{}},
			"open nil sync":   {newCatalogTestFile(t, nil), &catalogTestLock{}, nil, &pilotCatalogHealth{}},
			"open nil health": {newCatalogTestFile(t, nil), &catalogTestLock{}, syncParent, nil},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := openPilotCatalogFromVerifiedHandle(args.file, args.lock, args.sync, args.health); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("open = %v", err)
				}
			})
		}
		for name, args := range map[string]struct {
			file   pilotSidecarFile
			lock   pilotCatalogLock
			id     string
			sync   func() error
			health *pilotCatalogHealth
		}{
			"create nil file":   {nil, &catalogTestLock{}, catalogTestCaseA, syncParent, &pilotCatalogHealth{}},
			"create nil lock":   {newCatalogTestFile(t, nil), nil, catalogTestCaseA, syncParent, &pilotCatalogHealth{}},
			"create nil sync":   {newCatalogTestFile(t, nil), &catalogTestLock{}, catalogTestCaseA, nil, &pilotCatalogHealth{}},
			"create nil health": {newCatalogTestFile(t, nil), &catalogTestLock{}, catalogTestCaseA, syncParent, nil},
			"invalid case id":   {newCatalogTestFile(t, nil), &catalogTestLock{}, "invalid", syncParent, &pilotCatalogHealth{}},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := createPilotCatalogWithFirstCase(args.file, args.lock, args.id, args.sync, args.health); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("create = %v", err)
				}
			})
		}
		journal, _, _, _ := func() (*pilotCatalogJournal, *catalogTestFile, *catalogTestLock, *pilotCatalogHealth) {
			health := &pilotCatalogHealth{}
			lock := &catalogTestLock{}
			file := newCatalogTestFile(t, nil)
			journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, syncParent, health)
			if err != nil {
				t.Fatal(err)
			}
			return journal, file, lock, health
		}()
		for name, transition := range map[string]struct {
			id    string
			state pilotCatalogState
		}{
			"invalid case id": {"bad", pilotCatalogReady},
			"invalid state":   {catalogTestCaseA, "unknown"},
		} {
			t.Run(name, func(t *testing.T) {
				if err := journal.Transition(transition.id, transition.state); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("transition = %v", err)
				}
			})
		}
	})

	t.Run("poisoned before lock", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); err != nil {
			t.Fatal(err)
		}
		health.poison()
		if _, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("open on poisoned supervisor = %v", err)
		}
		if lock.held {
			t.Fatal("open acquired lock on poisoned supervisor")
		}
	})
	t.Run("nil journal health", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if err != nil {
			t.Fatal(err)
		}
		journal.health = nil
		if _, err := journal.Snapshot(); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("snapshot without health = %v", err)
		}
		if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("transition without health = %v", err)
		}
	})

	t.Run("open detects poison during lock acquisition", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); err != nil {
			t.Fatal(err)
		}
		lock.beforeGate = health.poison
		if _, err := openPilotCatalogFromVerifiedHandle(file, lock, func() error { return nil }, health); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("open after lock-window poison = %v", err)
		}
		if lock.held {
			t.Fatal("open leaked lock after poison")
		}
	})
	t.Run("create detects poison during lock acquisition", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{beforeGate: health.poison}
		file := newCatalogTestFile(t, nil)
		if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("create after lock-window poison = %v", err)
		}
		if lock.held {
			t.Fatal("create leaked lock after poison")
		}
	})
	t.Run("create fails before lock when already poisoned", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		health.poison()
		lock := &catalogTestLock{}
		if _, err := createPilotCatalogWithFirstCase(newCatalogTestFile(t, nil), lock, catalogTestCaseA, func() error { return nil }, health); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("create on poisoned supervisor = %v", err)
		}
		if lock.held {
			t.Fatal("create acquired lock on poisoned supervisor")
		}
	})
	t.Run("snapshot detects poison during lock acquisition", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if err != nil {
			t.Fatal(err)
		}
		lock.beforeGate = health.poison
		if _, err := journal.Snapshot(); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("snapshot after lock-window poison = %v", err)
		}
	})
	t.Run("transition detects poison during lock acquisition", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if err != nil {
			t.Fatal(err)
		}
		lock.beforeGate = health.poison
		if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogPoisoned) {
			t.Fatalf("transition after lock-window poison = %v", err)
		}
	})
}

func TestPilotCatalogFileOperationFailuresPoisonBeforeAcknowledgement(t *testing.T) {
	newJournal := func(t *testing.T) (*pilotCatalogJournal, *catalogTestFile, *catalogTestLock, *pilotCatalogHealth) {
		t.Helper()
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health)
		if err != nil {
			t.Fatal(err)
		}
		return journal, file, lock, health
	}
	t.Run("create file operation failures", func(t *testing.T) {
		for name, inject := range map[string]func(*catalogTestFile){
			"stat":        func(file *catalogTestFile) { file.statErr = errors.New("stat failed") },
			"seek":        func(file *catalogTestFile) { file.seekErr = errors.New("seek failed") },
			"write":       func(file *catalogTestFile) { file.writeErr = errors.New("write failed") },
			"short write": func(file *catalogTestFile) { file.writeLimit = 1 },
		} {
			t.Run(name, func(t *testing.T) {
				health := &pilotCatalogHealth{}
				lock := &catalogTestLock{}
				file := newCatalogTestFile(t, nil)
				inject(file)
				if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("create = %v", err)
				}
				if !health.isPoisoned() || lock.held {
					t.Fatal("create failure did not poison and release")
				}
			})
		}
	})
	t.Run("create rejects directory handle", func(t *testing.T) {
		directory, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		health := &pilotCatalogHealth{}
		if _, err := createPilotCatalogWithFirstCase(&catalogTestFile{File: directory, writeLimit: -1, readLimit: -1}, &catalogTestLock{}, catalogTestCaseA, func() error { return nil }, health); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("directory handle create = %v", err)
		}
	})
	t.Run("create rejects nonempty file and lock failure", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{}
		file := newCatalogTestFile(t, nil)
		if _, err := file.File.Write([]byte("unexpected")); err != nil {
			t.Fatal(err)
		}
		if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("nonempty create = %v", err)
		}
		health = &pilotCatalogHealth{}
		lock = &catalogTestLock{failLock: true}
		file = newCatalogTestFile(t, nil)
		if _, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("lock failure create = %v", err)
		}
		if !health.isPoisoned() {
			t.Fatal("lock failure did not poison supervisor")
		}
	})
	t.Run("create unlock failure poisons result", func(t *testing.T) {
		health := &pilotCatalogHealth{}
		lock := &catalogTestLock{failUnlock: true}
		file := newCatalogTestFile(t, nil)
		if journal, err := createPilotCatalogWithFirstCase(file, lock, catalogTestCaseA, func() error { return nil }, health); journal != nil || !errors.Is(err, errPilotCatalogIntegrity) {
			t.Fatalf("create on unlock failure = (%v, %v)", journal, err)
		}
		if !health.isPoisoned() {
			t.Fatal("create unlock failure did not poison")
		}
	})
	t.Run("open failures", func(t *testing.T) {
		for name, inject := range map[string]func(*catalogTestFile, *catalogTestLock, *pilotCatalogHealth) func() error{
			"lock": func(_ *catalogTestFile, lock *catalogTestLock, _ *pilotCatalogHealth) func() error {
				lock.failLock = true
				return func() error { return nil }
			},
			"read": func(file *catalogTestFile, _ *catalogTestLock, _ *pilotCatalogHealth) func() error {
				file.readErr = errors.New("read failed")
				return func() error { return nil }
			},
			"parent sync": func(_ *catalogTestFile, _ *catalogTestLock, _ *pilotCatalogHealth) func() error {
				return func() error { return errors.New("parent sync failed") }
			},
			"unlock": func(_ *catalogTestFile, lock *catalogTestLock, _ *pilotCatalogHealth) func() error {
				lock.failUnlock = true
				return func() error { return nil }
			},
		} {
			t.Run(name, func(t *testing.T) {
				journal, file, lock, health := newJournal(t)
				_ = journal
				syncParent := inject(file, lock, health)
				opened, err := openPilotCatalogFromVerifiedHandle(file, lock, syncParent, health)
				if opened != nil || !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("open = (%v, %v)", opened, err)
				}
				if !health.isPoisoned() {
					t.Fatal("open failure did not poison")
				}
			})
		}
	})
	t.Run("snapshot and transition operation failures", func(t *testing.T) {
		for name, inject := range map[string]func(*catalogTestFile, *catalogTestLock){
			"lock":       func(_ *catalogTestFile, lock *catalogTestLock) { lock.failLock = true },
			"stat":       func(file *catalogTestFile, _ *catalogTestLock) { file.statErr = errors.New("stat failed") },
			"seek":       func(file *catalogTestFile, _ *catalogTestLock) { file.seekErr = errors.New("seek failed") },
			"read":       func(file *catalogTestFile, _ *catalogTestLock) { file.readErr = errors.New("read failed") },
			"short read": func(file *catalogTestFile, _ *catalogTestLock) { file.readLimit = 1 },
			"unlock":     func(_ *catalogTestFile, lock *catalogTestLock) { lock.failUnlock = true },
		} {
			t.Run(name+" snapshot", func(t *testing.T) {
				journal, file, lock, health := newJournal(t)
				inject(file, lock)
				if _, err := journal.Snapshot(); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("snapshot = %v", err)
				}
				if !health.isPoisoned() {
					t.Fatal("snapshot failure did not poison")
				}
			})
			t.Run(name+" transition", func(t *testing.T) {
				journal, file, lock, health := newJournal(t)
				inject(file, lock)
				if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("transition = %v", err)
				}
				if !health.isPoisoned() {
					t.Fatal("transition failure did not poison")
				}
			})
		}
		for name, inject := range map[string]func(*catalogTestFile, *catalogTestLock){
			"write": func(file *catalogTestFile, _ *catalogTestLock) { file.writeErr = errors.New("write failed") },
			"sync":  func(file *catalogTestFile, _ *catalogTestLock) { file.syncErr = errors.New("sync failed") },
		} {
			t.Run(name+" transition", func(t *testing.T) {
				journal, file, lock, health := newJournal(t)
				inject(file, lock)
				if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogIntegrity) {
					t.Fatalf("transition = %v", err)
				}
				if !health.isPoisoned() {
					t.Fatal("transition failure did not poison")
				}
			})
		}
		for _, operation := range []string{"snapshot", "transition"} {
			t.Run(operation+" missing file", func(t *testing.T) {
				journal, _, _, _ := newJournal(t)
				journal.file = nil
				if operation == "snapshot" {
					if _, err := journal.Snapshot(); !errors.Is(err, errPilotCatalogPoisoned) {
						t.Fatalf("snapshot = %v", err)
					}
				} else if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogPoisoned) {
					t.Fatalf("transition = %v", err)
				}
			})
			t.Run(operation+" missing lock", func(t *testing.T) {
				journal, _, _, _ := newJournal(t)
				journal.lock = nil
				if operation == "snapshot" {
					if _, err := journal.Snapshot(); !errors.Is(err, errPilotCatalogPoisoned) {
						t.Fatalf("snapshot = %v", err)
					}
				} else if err := journal.Transition(catalogTestCaseA, pilotCatalogReady); !errors.Is(err, errPilotCatalogPoisoned) {
					t.Fatalf("transition = %v", err)
				}
			})
		}
	})
}

func TestPilotCatalogPlatformFileLockCanBeReacquired(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "catalog-lock-*.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := newPilotCatalogFileLock(file)
	for i := 0; i < 2; i++ {
		if err := lock.Lock(); err != nil {
			t.Fatalf("lock %d: %v", i, err)
		}
		if err := lock.Unlock(); err != nil {
			t.Fatalf("unlock %d: %v", i, err)
		}
	}
}

func TestPilotCatalogFileLockRejectsMissingOrClosedHandle(t *testing.T) {
	var nilLock *pilotCatalogFileLock
	if err := nilLock.Lock(); err == nil {
		t.Fatal("nil Lock unexpectedly succeeded")
	}
	if acquired, err := nilLock.TryLock(); err == nil || acquired {
		t.Fatalf("nil TryLock = (%v, %v)", acquired, err)
	}
	if err := nilLock.Unlock(); err == nil {
		t.Fatal("nil Unlock unexpectedly succeeded")
	}
	if err := newPilotCatalogFileLock(nil).Lock(); err == nil {
		t.Fatal("nil file Lock unexpectedly succeeded")
	}
	if acquired, err := newPilotCatalogFileLock(nil).TryLock(); err == nil || acquired {
		t.Fatalf("nil file TryLock = (%v, %v)", acquired, err)
	}
	file, err := os.CreateTemp(t.TempDir(), "closed-catalog-lock-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	closed := newPilotCatalogFileLock(file)
	if err := closed.Lock(); err == nil {
		t.Fatal("closed file Lock unexpectedly succeeded")
	}
	if acquired, err := closed.TryLock(); err == nil || acquired {
		t.Fatalf("closed file TryLock = (%v, %v)", acquired, err)
	}
}

func TestPilotCatalogFileLockSerializesChildProcess(t *testing.T) {
	if lockPath := os.Getenv("PILOT_CATALOG_LOCK_CHILD_PATH"); lockPath != "" {
		lockFile, err := os.OpenFile(lockPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lockFile.Close()
		control, err := net.Dial("tcp", os.Getenv("PILOT_CATALOG_LOCK_CHILD_CONTROL"))
		if err != nil {
			t.Fatal(err)
		}
		defer control.Close()
		lock := newPilotCatalogFileLock(lockFile)
		acquired, err := lock.TryLock()
		if err != nil {
			t.Fatal(err)
		}
		if acquired {
			if err := lock.Unlock(); err != nil {
				t.Fatal(err)
			}
			_, _ = control.Write([]byte{'A'})
			return
		}
		if _, err := control.Write([]byte{'B'}); err != nil {
			t.Fatal(err)
		}
		var release [1]byte
		if _, err := io.ReadFull(control, release[:]); err != nil || release[0] != 'R' {
			t.Fatalf("release barrier = %q, %v", release[0], err)
		}
		if err := lock.Lock(); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Write([]byte{'C'}); err != nil {
			t.Fatal(err)
		}
		if err := lock.Unlock(); err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.Create(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lockFile.Close()
	parentLock := newPilotCatalogFileLock(lockFile)
	if err := parentLock.Lock(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestPilotCatalogFileLockSerializesChildProcess$")
	command.Env = append(os.Environ(), "PILOT_CATALOG_LOCK_CHILD_PATH="+filepath.Join(dir, "lock"), "PILOT_CATALOG_LOCK_CHILD_CONTROL="+listener.Addr().String())
	if err := command.Start(); err != nil {
		_ = parentLock.Unlock()
		t.Fatal(err)
	}
	waitedAlready := false
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	defer func() {
		if !waitedAlready {
			_ = command.Process.Kill()
			select {
			case <-waited:
			case <-time.After(2 * time.Second):
				t.Error("lock child did not exit after forced termination")
			}
		}
	}()
	connection, err := listener.Accept()
	if err != nil {
		_ = parentLock.Unlock()
		_ = command.Process.Kill()
		t.Fatalf("accept child control: %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = parentLock.Unlock()
		t.Fatal(err)
	}
	var probe [1]byte
	if _, err := io.ReadFull(connection, probe[:]); err != nil {
		_ = parentLock.Unlock()
		_ = command.Process.Kill()
		t.Fatalf("child lock probe: %v", err)
	}
	if probe[0] != 'B' {
		_ = parentLock.Unlock()
		_ = command.Process.Kill()
		t.Fatal("child nonblocking probe acquired lock while parent held it")
	}
	if _, err := connection.Write([]byte{'R'}); err != nil {
		_ = parentLock.Unlock()
		t.Fatal(err)
	}
	if err := parentLock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(connection, probe[:]); err != nil || probe[0] != 'C' {
		_ = command.Process.Kill()
		t.Fatalf("child blocking acquisition = %q, %v", probe[0], err)
	}
	select {
	case err := <-waited:
		waitedAlready = true
		if err != nil {
			t.Fatalf("lock child: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		select {
		case <-waited:
			waitedAlready = true
			t.Fatal("lock child did not exit before deadline")
		case <-time.After(2 * time.Second):
			waitedAlready = true
			t.Fatal("lock child did not exit after forced termination")
		}
	}
}

func TestPilotCatalogFileLockTryLockIsNonBlockingForLocalHolder(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "pilot-catalog-lock-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := newPilotCatalogFileLock(file)
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	type lockResult struct {
		acquired bool
		err      error
	}
	tryResult := make(chan lockResult, 1)
	go func() {
		acquired, err := lock.TryLock()
		tryResult <- lockResult{acquired: acquired, err: err}
	}()
	select {
	case result := <-tryResult:
		if result.err != nil || result.acquired {
			_ = lock.Unlock()
			t.Fatalf("TryLock while locally held = (%v, %v), want (false, nil)", result.acquired, result.err)
		}
	case <-time.After(2 * time.Second):
		_ = lock.Unlock()
		select {
		case result := <-tryResult:
			if result.acquired {
				_ = lock.Unlock()
			}
		case <-time.After(2 * time.Second):
			t.Fatal("locally contended TryLock did not return after releasing the holder")
		}
		t.Fatal("TryLock blocked while the same lock object was held")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	acquired, err := lock.TryLock()
	if err != nil || !acquired {
		t.Fatalf("TryLock after release = (%v, %v), want (true, nil)", acquired, err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}


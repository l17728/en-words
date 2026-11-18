//go:build cgo && pilot_e2e && linux

package pilotcontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

type pilotGoldenPlatform struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	ParentVFS string `json:"parent_vfs"`
}

type pilotGoldenSQLite struct {
	Version       string `json:"version"`
	VersionNumber string `json:"version_number"`
	SourceID      string `json:"source_id"`
}

type pilotGoldenXOpen struct {
	ProcessRole    string `json:"process_role"`
	ConnectionID   string `json:"connection_id"`
	Role           string `json:"role"`
	RC             int    `json:"rc"`
	RequestedFlags int    `json:"requested_flags"`
	ObservedFlags  int    `json:"observed_flags"`
	POutFlags      int    `json:"p_out_flags"`
}

type pilotGoldenObserved struct {
	CallbackEventCount int                `json:"callback_event_count"`
	Callbacks          []string           `json:"callbacks"`
	XOpens             []pilotGoldenXOpen `json:"xopens"`
	Details            map[string]any     `json:"details"`
}

type pilotGoldenOracle struct {
	ExpectedOutcome string               `json:"expected_outcome"`
	ObservedOutcome *pilotGoldenObserved `json:"observed_outcome"`
	ExecutionStatus string               `json:"execution_status"`
	ReasonCode      *string              `json:"reason_code"`
	Details         map[string]any       `json:"details"`
	EventCount      int                  `json:"event_count"`
}

type pilotGoldenEvent struct {
	SourceID   string  `json:"source_id"`
	Seq        int     `json:"seq"`
	ConnID     string  `json:"connection_id"`
	Actor      string  `json:"actor"`
	Process    string  `json:"process_role"`
	Callback   string  `json:"callback"`
	HandleID   *string `json:"file_handle_id"`
	Name       string  `json:"name"`
	OutputName string  `json:"output_name"`
	Role       string  `json:"role"`
	Arg0       int     `json:"arg0"`
	Arg1       int     `json:"arg1"`
	Arg2       int     `json:"arg2"`
	RC         int     `json:"rc"`
	Out        int     `json:"out"`
}

type pilotGoldenChild struct {
	CommandTemplate       string  `json:"command_template"`
	ProcessRole           string  `json:"process_role"`
	Started               bool    `json:"started"`
	ExecutionStatus       string  `json:"execution_status"`
	ExitCode              *int    `json:"exit_code"`
	TerminationSignal     string  `json:"termination_signal"`
	TerminalOutcome       *string `json:"terminal_outcome"`
	CleanupStatus         string  `json:"cleanup_status"`
	ChildControlFDClosed  bool    `json:"child_control_fd_closed"`
	ParentControlFDClosed *bool   `json:"parent_control_fd_closed"`
	TracePersisted        bool    `json:"trace_persisted"`
	TimedOut              bool    `json:"timed_out"`
	Killed                bool    `json:"killed"`
	Reaped                bool    `json:"reaped"`
	ErrorSummaryCode      *string `json:"error_summary_code"`
}

type pilotGoldenScenario struct {
	ID                string                    `json:"id"`
	Status            string                    `json:"status"`
	ReasonCode        *string                   `json:"reason_code"`
	Oracle            pilotGoldenOracle         `json:"oracle"`
	Events            []pilotGoldenEvent        `json:"events"`
	ChildExecutions   []pilotGoldenChild        `json:"child_executions"`
	EvidenceArtifacts []pilotTraceChildArtifact `json:"evidence_artifacts,omitempty"`
}

type pilotGoldenFile struct {
	FormatVersion int                   `json:"format_version"`
	FixtureGroup  string                `json:"fixture_group"`
	Platform      pilotGoldenPlatform   `json:"platform"`
	SQLite        pilotGoldenSQLite     `json:"sqlite"`
	Scenarios     []pilotGoldenScenario `json:"scenarios"`
}

var pilotGoldenScenarioOrder = map[string][]string{
	"sqlite3451-vfs-flags":          {"VFSFLAGS-FAILED-OPEN", "VFSFLAGS-MAIN-OPEN", "VFSFLAGS-WAL-CREATE", "VFSFLAGS-SIDECAR-EXISTING", "VFSFLAGS-JOURNAL-CREATE", "VFSFLAGS-SIDECAR-EEXIST-REOPEN"},
	"sqlite3451-unix-lock-trace":    {"LOCK-LOCAL-CONTEND", "LOCK-CROSS-PROCESS-CONTEND", "LOCK-PROMOTION-BUSY", "LOCK-DOWNGRADE"},
	"sqlite3451-shm-lock-trace":     {"SHM-LOCAL-LOCK-UNLOCK", "SHM-CROSS-PROCESS-CONTEND", "SHM-FINAL-UNMAP-REOPEN"},
	"sqlite3451-pager-delete-trace": {"PAGER-EMPTY-OBSOLETE-RESERVED", "PAGER-HOT-ROLLBACK-EXCLUSIVE", "PAGER-COMMIT-EXCLUSIVE"},
}

var pilotGoldenDetailKeys = map[string]string{
	"LOCK-LOCAL-CONTEND":            "a_busy_lock_seq,b_reserved_lock_seq,b_unlock_seq,callbacks_observed,participant_a_read_value,participant_a_update,participant_b_begin_immediate,scope,xCheckReservedLock_observed",
	"LOCK-CROSS-PROCESS-CONTEND":    "a_busy_seq,b_reserved_seq,b_unlock_seq,callback_sequence_edges,child_begin_immediate,control_messages,parent_snapshot,parent_update,parent_update_result,scope",
	"LOCK-PROMOTION-BUSY":           "a_reserved_xlock_seq,b_busy_xlock_seq,b_shared_xlock_seq,participant_a_snapshot,participant_a_update,participant_b_after_busy,participant_b_snapshot,participant_b_update,scope,xCheckReservedLock_observed",
	"LOCK-DOWNGRADE":                "committed_value,exclusive_seq,lock_order,none_unlock_seq,reserved_seq,scope,shared_unlock_seq,transaction_rc",
	"SHM-LOCAL-LOCK-UNLOCK":         "callbacks_observed,reader_ready_after_value,scope,writer_commit",
	"SHM-CROSS-PROCESS-CONTEND":     "callback_barrier_edges,callbacks_observed,child_committed_value,child_pre_ready_last_seq,child_shm_unmap_seq,child_writer_lock_acquire,child_writer_lock_release,control_messages,parent_snapshot_after_child_commit,parent_snapshot_before,scope",
	"SHM-FINAL-UNMAP-REOPEN":        "barrier,control_messages,observed_callback_order,participant_a_file_handle,participant_a_map_seq,participant_a_unmap_seq,participant_a_value,participant_b_file_handle,participant_b_map_handle_id,participant_b_map_seq,participant_b_value,scope",
	"PAGER-EMPTY-OBSOLETE-RESERVED": "main_journal_xOpen,obsolete_journal_xDelete,schema_version,scope,sqlite_rc",
	"PAGER-HOT-ROLLBACK-EXCLUSIVE":  "crash_db_snapshot,crash_journal_snapshot,durable_trace,durable_trace_stage,journal_present_before_kill,journal_sync_callback_seq,main_write_callback_returned,main_write_callback_seq,main_write_delegate_rc,main_write_handle_id,main_write_offset,main_write_size,old_committed_value,recovered_value,scope",
	"PAGER-COMMIT-EXCLUSIVE":        "final_value,journal_callback_order,journal_xDelete,main_journal_xOpen,scope,transaction_rc",
}

var pilotGoldenStringAllow = map[string]map[string]bool{
	"connection": setOf("child-B", "conn-A", "conn-B", "conn-failed", "conn-wal-witness", "crash-child", "eexist-child", "pager-commit", "pager-empty", "parent-A", "participant-A", "participant-B", "recovery-parent", "verify"),
	"process":    setOf("", "child", "child-A", "child-B", "crash-child", "eexist-child", "parent", "recovery-parent"),
	"role":       setOf("", "MAIN_DB", "MAIN_JOURNAL", "WAL"),
	"sqlite_rc":  setOf("SQLITE_OK", "SQLITE_BUSY", "SQLITE_ERROR", "SQLITE_IOERR", "SQLITE_CANTOPEN", "SQLITE_READONLY", "SQLITE_CORRUPT", "SQLITE_LOCKED", "SQLITE_MISUSE"),
}

func setOf(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func TestPinnedVFSCallbackGoldens(t *testing.T) {
	if !pilotTraceEntrypointEnabled(t, "golden") {
		return
	}
	if runtimeGOOS, runtimeGOARCH := pilotGoldenRuntime(); runtimeGOOS != "linux" || runtimeGOARCH != "amd64" {
		t.Fatalf("golden platform mismatch: %s/%s", runtimeGOOS, runtimeGOARCH)
	}
	pilotTraceGenerateCandidates(t)
	artifactDir := os.Getenv("PILOT_VFS_ARTIFACT_DIR")
	for _, group := range []string{"sqlite3451-vfs-flags", "sqlite3451-unix-lock-trace", "sqlite3451-shm-lock-trace", "sqlite3451-pager-delete-trace"} {
		candidatePath := filepath.Join(artifactDir, group+".candidate.json")
		candidate, err := pilotTraceReadArtifact(candidatePath)
		if err != nil {
			t.Fatalf("read candidate for %s: %v", group, err)
		}
		canonical, err := pilotNormalizeGoldenCandidate(candidate, group)
		if err != nil {
			t.Fatalf("normalize %s candidate: %v", group, err)
		}
		goldenPath := filepath.Join(pilotGoldenTestdataDir(t), group+".golden.json")
		golden, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read pinned golden for %s: %v", group, err)
		}
		if !bytes.Equal(canonical, golden) {
			t.Fatalf("pinned callback golden differs for %s (first differing field: %s)", group, pilotGoldenFirstDifference(canonical, golden))
		}
	}
}

func TestProposePinnedVFSCallbackGoldens(t *testing.T) {
	if !pilotTraceEntrypointEnabled(t, "propose-goldens") {
		return
	}
	pilotTraceGenerateCandidates(t)
	artifactDir := os.Getenv("PILOT_VFS_ARTIFACT_DIR")
	for group := range pilotGoldenScenarioOrder {
		candidate, err := pilotTraceReadArtifact(filepath.Join(artifactDir, group+".candidate.json"))
		if err != nil {
			t.Fatalf("read candidate for %s: %v", group, err)
		}
		canonical, err := pilotNormalizeGoldenCandidate(candidate, group)
		if err != nil {
			t.Fatalf("normalize %s candidate: %v", group, err)
		}
		if err := pilotGoldenWriteExclusive(artifactDir, group+".golden.proposal.json", canonical); err != nil {
			t.Fatalf("write proposal for %s: %v", group, err)
		}
	}
}

func pilotGoldenRuntime() (string, string) { return runtime.GOOS, runtime.GOARCH }

func pilotGoldenTestdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve golden test source")
	}
	return filepath.Join(filepath.Dir(file), "testdata")
}

func pilotGoldenWriteExclusive(dir, name string, data []byte) error {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".golden.proposal.json") {
		return errors.New("invalid proposal basename")
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var before, pathInfo unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return err
	}
	if err := unix.Lstat(dir, &pathInfo); err != nil {
		return err
	}
	if before.Dev != pathInfo.Dev || before.Ino != pathInfo.Ino || before.Uid != uint32(os.Geteuid()) || before.Mode&unix.S_IFMT != unix.S_IFDIR || before.Mode&0777 != 0700 {
		return errors.New("proposal directory identity, owner, or mode changed")
	}
	out, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(out), name)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func pilotNormalizeGoldenCandidate(data []byte, expectedGroup string) ([]byte, error) {
	if err := pilotGoldenRejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var candidate pilotTraceCandidate
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&candidate); err != nil {
		return nil, fmt.Errorf("candidate schema: %w", err)
	}
	if err := pilotGoldenRequireEOF(decoder); err != nil {
		return nil, err
	}
	ids, ok := pilotGoldenScenarioOrder[expectedGroup]
	if !ok || candidate.FixtureGroup != expectedGroup {
		return nil, errors.New("unknown or mismatched fixture group")
	}
	if candidate.Status != "CANDIDATE" || candidate.TraceOverflowed {
		return nil, errors.New("candidate run gate failed")
	}
	if len(candidate.SQLite) != 6 {
		return nil, errors.New("candidate SQLite identity key set mismatch")
	}
	if candidate.SQLite["version"] != pinnedSQLiteVersion || candidate.SQLite["version_number"] != fmt.Sprint(pinnedSQLiteVersionNum) || candidate.SQLite["source_id"] != pinnedSQLiteSourceID || candidate.SQLite["parent_vfs"] != "unix" || candidate.SQLite["vfs_name"] != pilotTraceVFSName {
		return nil, errors.New("pinned SQLite identity mismatch")
	}
	if len(candidate.Scenarios) != len(ids) {
		return nil, errors.New("scenario count mismatch")
	}
	version, number, sourceID := sqlite3.Version()
	if version != pinnedSQLiteVersion || number != pinnedSQLiteVersionNum || sourceID != pinnedSQLiteSourceID {
		return nil, errors.New("runtime SQLite identity mismatch")
	}
	file := pilotGoldenFile{FormatVersion: 1, FixtureGroup: expectedGroup, Platform: pilotGoldenPlatform{OS: "linux", Arch: "amd64", ParentVFS: "unix"}, SQLite: pilotGoldenSQLite{Version: version, VersionNumber: fmt.Sprint(number), SourceID: sourceID}, Scenarios: make([]pilotGoldenScenario, 0, len(ids))}
	for index, scenario := range candidate.Scenarios {
		if scenario.ID != ids[index] {
			return nil, fmt.Errorf("scenario order mismatch at index %d", index)
		}
		normalized, err := pilotGoldenNormalizeScenario(scenario)
		if err != nil {
			return nil, fmt.Errorf("scenario %s: %w", scenario.ID, err)
		}
		file.Scenarios = append(file.Scenarios, normalized)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(file); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func pilotGoldenNormalizeScenario(s pilotTraceScenario) (pilotGoldenScenario, error) {
	if s.Status != "CANDIDATE" && s.Status != "NOT RUN" {
		return pilotGoldenScenario{}, errors.New("invalid scenario status")
	}
	if len(s.Oracle) != 6 {
		return pilotGoldenScenario{}, errors.New("oracle key set mismatch")
	}
	for _, key := range []string{"expected_outcome", "observed_outcome", "execution_status", "reason", "details", "event_count"} {
		if _, ok := s.Oracle[key]; !ok {
			return pilotGoldenScenario{}, fmt.Errorf("missing oracle field %s", key)
		}
	}
	details := pilotTraceOracleDetails(s)
	if err := pilotGoldenValidateDetails(s.ID, s.Status, details); err != nil {
		return pilotGoldenScenario{}, err
	}
	reasonCode, err := pilotGoldenReasonCode(s)
	if err != nil {
		return pilotGoldenScenario{}, err
	}
	if err := pilotGoldenValidateEvidence(os.Getenv("PILOT_VFS_ARTIFACT_DIR"), s.ID, s.Status, s.EvidenceArtifacts, details); err != nil {
		return pilotGoldenScenario{}, err
	}
	events, handleMap, err := pilotGoldenNormalizeEvents(s.Events)
	if err != nil {
		return pilotGoldenScenario{}, err
	}
	canonicalDetails, err := pilotGoldenNormalizeDetails(s.ID, details, s.Events)
	if err != nil {
		return pilotGoldenScenario{}, err
	}
	if err := pilotGoldenNormalizeHandleRefs(canonicalDetails, handleMap); err != nil {
		return pilotGoldenScenario{}, err
	}
	children, err := pilotGoldenNormalizeChildren(s.ChildExecutions)
	if err != nil {
		return pilotGoldenScenario{}, err
	}
	oracleRaw, ok := s.Oracle["expected_outcome"].(string)
	if !ok || oracleRaw != pilotTraceExpectedOutcome(s.ID) {
		return pilotGoldenScenario{}, errors.New("expected outcome mismatch")
	}
	count, ok := pilotGoldenInt(s.Oracle["event_count"])
	if !ok || count != len(events) {
		return pilotGoldenScenario{}, errors.New("oracle event count mismatch")
	}
	oracleStatus, _ := s.Oracle["execution_status"].(string)
	if oracleStatus != s.Status {
		return pilotGoldenScenario{}, errors.New("oracle status mismatch")
	}
	observed := &pilotGoldenObserved{CallbackEventCount: len(events), Callbacks: pilotGoldenCallbacks(events), XOpens: pilotGoldenXOpens(events), Details: canonicalDetails}
	if s.Status == "NOT RUN" {
		observed = nil
	}
	evidence := s.EvidenceArtifacts
	if s.ID != "VFSFLAGS-SIDECAR-EEXIST-REOPEN" {
		evidence = nil // pager snapshot hashes are validated against candidate evidence, not pinned as volatile artifacts
	}
	return pilotGoldenScenario{ID: s.ID, Status: s.Status, ReasonCode: reasonCode, Oracle: pilotGoldenOracle{ExpectedOutcome: oracleRaw, ObservedOutcome: observed, ExecutionStatus: oracleStatus, ReasonCode: reasonCode, Details: canonicalDetails, EventCount: len(events)}, Events: events, ChildExecutions: children, EvidenceArtifacts: evidence}, nil
}

func pilotGoldenValidateEvidence(artifactDir, id, status string, artifacts []pilotTraceChildArtifact, details map[string]any) error {
	if id == "VFSFLAGS-SIDECAR-EEXIST-REOPEN" {
		if len(artifacts) > 1 {
			return errors.New("too many EEXIST evidence artifacts")
		}
		for _, artifact := range artifacts {
			if artifact.File != "libpilot-vfs-eexist.so" || artifact.Bytes <= 0 || !pilotGoldenHex64.MatchString(artifact.SHA256) {
				return errors.New("invalid EEXIST interposer evidence artifact")
			}
			if value, ok := details["interposer_sha256"].(string); ok && value != artifact.SHA256 {
				return errors.New("interposer hash differs from evidence artifact")
			}
		}
		return nil
	}
	if id != "PAGER-HOT-ROLLBACK-EXCLUSIVE" {
		if len(artifacts) != 0 {
			return errors.New("unexpected scenario evidence artifacts")
		}
		return nil
	}
	if status == "NOT RUN" {
		if len(artifacts) != 0 {
			return errors.New("NOT RUN pager scenario has unexpected evidence artifacts")
		}
		return nil
	}
	if len(artifacts) != 2 {
		return errors.New("pager snapshot evidence must contain database and journal artifacts")
	}
	want := map[string]string{"pager-hot-rollback-crash-db.bin": "crash_db_snapshot", "pager-hot-rollback-crash-journal.bin": "crash_journal_snapshot"}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		field, ok := want[artifact.File]
		if !ok || seen[artifact.File] || artifact.Bytes <= 0 || !pilotGoldenHex64.MatchString(artifact.SHA256) {
			return errors.New("invalid pager snapshot evidence artifact")
		}
		snapshot, ok := details[field].(map[string]any)
		if !ok || len(snapshot) != 3 {
			return errors.New("pager snapshot detail schema mismatch")
		}
		file, fileOK := snapshot["file"].(string)
		hash, hashOK := snapshot["sha256"].(string)
		bytes, bytesOK := pilotGoldenInt(snapshot["bytes"])
		if !fileOK || file != artifact.File || !hashOK || hash != artifact.SHA256 || !bytesOK || bytes != artifact.Bytes {
			return errors.New("pager snapshot detail differs from evidence artifact")
		}
		if err := pilotGoldenVerifyArtifactFile(artifactDir, artifact); err != nil {
			return err
		}
		if field == "crash_journal_snapshot" {
			content, err := pilotGoldenReadArtifactFile(artifactDir, artifact)
			if err != nil {
				return err
			}
			projectionHash, err := pilotGoldenProjectCrashJournal(content)
			if err != nil {
				return fmt.Errorf("invalid rollback journal evidence: %w", err)
			}
			snapshot["sha256"] = projectionHash
		}
		seen[artifact.File] = true
	}
	return nil
}

func pilotGoldenVerifyArtifactFile(dir string, artifact pilotTraceChildArtifact) error {
	_, err := pilotGoldenReadArtifactFile(dir, artifact)
	return err
}

func pilotGoldenReadArtifactFile(dir string, artifact pilotTraceChildArtifact) ([]byte, error) {
	if filepath.Base(artifact.File) != artifact.File || dir == "" {
		return nil, errors.New("invalid evidence artifact path")
	}
	dirFD, _, err := pilotGoldenOpenPinnedArtifactDir(dir)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	fileFD, err := unix.Openat(dirFD, artifact.File, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open evidence artifact: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), artifact.File)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size != int64(artifact.Bytes) {
		return nil, errors.New("evidence artifact type or size mismatch")
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != int64(artifact.Bytes) || hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errors.New("evidence artifact content hash mismatch")
	}
	return content, nil
}

func pilotGoldenProjectCrashJournal(raw []byte) (string, error) {
	const (
		pageSize    = 4096
		journalSize = 8720
		recordSize  = 4 + pageSize + 4
	)
	magic := []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}
	if len(raw) != journalSize || !bytes.Equal(raw[:8], magic) {
		return "", errors.New("journal length or magic mismatch")
	}
	nRec := binary.BigEndian.Uint32(raw[8:12])
	nonce := binary.BigEndian.Uint32(raw[12:16])
	dbPages := binary.BigEndian.Uint32(raw[16:20])
	sectorSize := binary.BigEndian.Uint32(raw[20:24])
	pageBytes := binary.BigEndian.Uint32(raw[24:28])
	if nRec != 2 || dbPages != 2 || sectorSize != 512 || pageBytes != pageSize {
		return "", errors.New("journal header fields differ from pinned shape")
	}
	if !bytes.Equal(raw[28:512], make([]byte, 512-28)) {
		return "", errors.New("journal header padding is nonzero")
	}
	projection := append([]byte(nil), raw...)
	for i := 0; i < 2; i++ {
		record := 512 + i*recordSize
		pageNo := binary.BigEndian.Uint32(raw[record : record+4])
		if pageNo < 1 || pageNo > 2 || (i == 1 && pageNo == binary.BigEndian.Uint32(raw[512:516])) {
			return "", errors.New("journal page number is invalid or duplicated")
		}
		page := raw[record+4 : record+4+pageSize]
		checksum := nonce
		for offset := pageSize - 200; offset >= 0; offset -= 200 {
			checksum += uint32(page[offset])
		}
		checksumOffset := record + 4 + pageSize
		if binary.BigEndian.Uint32(raw[checksumOffset:checksumOffset+4]) != checksum {
			return "", errors.New("journal page checksum mismatch")
		}
		clear(projection[checksumOffset : checksumOffset+4])
	}
	clear(projection[12:16])
	digest := sha256.Sum256(projection)
	return hex.EncodeToString(digest[:]), nil
}

func pilotGoldenTestJournal(nonce uint32) []byte {
	const pageSize, recordSize = 4096, 4104
	data := make([]byte, 8720)
	copy(data[:8], []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7})
	binary.BigEndian.PutUint32(data[8:12], 2)
	binary.BigEndian.PutUint32(data[12:16], nonce)
	binary.BigEndian.PutUint32(data[16:20], 2)
	binary.BigEndian.PutUint32(data[20:24], 512)
	binary.BigEndian.PutUint32(data[24:28], pageSize)
	for i := 0; i < 2; i++ {
		record := 512 + i*recordSize
		binary.BigEndian.PutUint32(data[record:record+4], uint32(i+1))
		page := data[record+4 : record+4+pageSize]
		for j := range page {
			page[j] = byte((j*31 + i*73) % 251)
		}
		checksum := nonce
		for offset := pageSize - 200; offset >= 0; offset -= 200 {
			checksum += uint32(page[offset])
		}
		checksumOffset := record + 4 + pageSize
		binary.BigEndian.PutUint32(data[checksumOffset:checksumOffset+4], checksum)
	}
	return data
}

func TestPilotGoldenProjectCrashJournalStableAndStrict(t *testing.T) {
	first := pilotGoldenTestJournal(0)
	second := pilotGoldenTestJournal(0x12345678)
	rawFirst, rawSecond := sha256.Sum256(first), sha256.Sum256(second)
	if rawFirst == rawSecond {
		t.Fatal("fixture raw journal hashes unexpectedly match")
	}
	projectedFirst, err := pilotGoldenProjectCrashJournal(first)
	if err != nil {
		t.Fatal(err)
	}
	projectedSecond, err := pilotGoldenProjectCrashJournal(second)
	if err != nil {
		t.Fatal(err)
	}
	if projectedFirst != projectedSecond {
		t.Fatalf("nonce changed stable journal projection: %s != %s", projectedFirst, projectedSecond)
	}
	changedPage := append([]byte(nil), first...)
	changedPage[516+2000] ^= 1
	checksum := uint32(binary.BigEndian.Uint32(changedPage[4612:4616]))
	checksum = 0
	for offset := 4096 - 200; offset >= 0; offset -= 200 {
		checksum += uint32(changedPage[516+offset])
	}
	binary.BigEndian.PutUint32(changedPage[4612:4616], checksum)
	changedProjection, err := pilotGoldenProjectCrashJournal(changedPage)
	if err != nil || changedProjection == projectedFirst {
		t.Fatalf("changed page not represented in projection: hash=%s err=%v", changedProjection, err)
	}
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"bad magic", func(b []byte) []byte { b[0] ^= 1; return b }},
		{"record count", func(b []byte) []byte { binary.BigEndian.PutUint32(b[8:12], 1); return b }},
		{"sentinel", func(b []byte) []byte { binary.BigEndian.PutUint32(b[8:12], ^uint32(0)); return b }},
		{"initial database pages", func(b []byte) []byte { binary.BigEndian.PutUint32(b[16:20], 3); return b }},
		{"page size", func(b []byte) []byte { binary.BigEndian.PutUint32(b[24:28], 2048); return b }},
		{"page size non power of two", func(b []byte) []byte { binary.BigEndian.PutUint32(b[24:28], 4095); return b }},
		{"page size out of range", func(b []byte) []byte { binary.BigEndian.PutUint32(b[24:28], ^uint32(0)); return b }},
		{"sector size", func(b []byte) []byte { binary.BigEndian.PutUint32(b[20:24], 513); return b }},
		{"sector size out of range", func(b []byte) []byte { binary.BigEndian.PutUint32(b[20:24], ^uint32(0)); return b }},
		{"header padding", func(b []byte) []byte { b[28] = 1; return b }},
		{"invalid page number", func(b []byte) []byte { binary.BigEndian.PutUint32(b[512:516], 3); return b }},
		{"page number out of range", func(b []byte) []byte { binary.BigEndian.PutUint32(b[512:516], ^uint32(0)); return b }},
		{"duplicate page number", func(b []byte) []byte { binary.BigEndian.PutUint32(b[512+4104:516+4104], 1); return b }},
		{"checksum", func(b []byte) []byte { b[4612] ^= 1; return b }},
		{"nonce with stale checksums", func(b []byte) []byte { binary.BigEndian.PutUint32(b[12:16], 1); return b }},
		{"extra header segment", func(b []byte) []byte { return append(b, make([]byte, 512)...) }},
		{"trailing bytes", func(b []byte) []byte { return append(b, 1) }},
		{"truncated header", func(b []byte) []byte { return b[:27] }},
		{"truncated header padding", func(b []byte) []byte { return b[:511] }},
		{"truncated first record", func(b []byte) []byte { return b[:512+4103] }},
		{"truncated second record", func(b []byte) []byte { return b[:512+4104+4103] }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.mutate(append([]byte(nil), first...))
			if _, err := pilotGoldenProjectCrashJournal(input); err == nil {
				t.Fatal("malformed journal accepted")
			}
		})
	}
}

func pilotGoldenOpenPinnedArtifactDir(dir string) (int, unix.Stat_t, error) {
	var result unix.Stat_t
	if os.Getenv("PILOT_VFS_ARTIFACT_DIR_DEVICE") == "" || os.Getenv("PILOT_VFS_ARTIFACT_DIR_INODE") == "" {
		return -1, result, errors.New("runner-pinned artifact identity missing")
	}
	return pilotTraceOpenArtifactDirectory(dir, true)
}

func pilotTraceOpenArtifactDirectory(dir string, requireRunnerIdentity bool) (int, unix.Stat_t, error) {
	var result unix.Stat_t
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return -1, result, err
	}
	clean := filepath.Clean(absolute)
	if clean != absolute || !filepath.IsAbs(clean) {
		return -1, result, errors.New("artifact directory path is not canonical absolute path")
	}
	deviceText, inodeText := os.Getenv("PILOT_VFS_ARTIFACT_DIR_DEVICE"), os.Getenv("PILOT_VFS_ARTIFACT_DIR_INODE")
	runnerDir := os.Getenv("PILOT_VFS_ARTIFACT_DIR")
	runnerDirAbs := ""
	if runnerDir != "" {
		runnerDirAbs, err = filepath.Abs(runnerDir)
		if err != nil {
			return -1, result, err
		}
	}
	usesRunnerIdentity := clean == runnerDirAbs && deviceText != "" && inodeText != ""
	if requireRunnerIdentity && !usesRunnerIdentity {
		return -1, result, errors.New("runner-pinned artifact directory identity missing")
	}
	var device, inode uint64
	if usesRunnerIdentity {
		device, err = strconv.ParseUint(deviceText, 10, 64)
		if err != nil {
			return -1, result, errors.New("invalid runner-pinned artifact device identity")
		}
		inode, err = strconv.ParseUint(inodeText, 10, 64)
		if err != nil {
			return -1, result, errors.New("invalid runner-pinned artifact inode identity")
		}
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, result, err
	}
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(fd)
			return -1, result, errors.New("invalid artifact directory path component")
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return -1, result, fmt.Errorf("open artifact directory component without following links: %w", openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	if err := unix.Fstat(fd, &result); err != nil {
		_ = unix.Close(fd)
		return -1, result, err
	}
	strictIdentity := requireRunnerIdentity || usesRunnerIdentity
	if (usesRunnerIdentity && (uint64(result.Dev) != device || result.Ino != inode)) || result.Mode&unix.S_IFMT != unix.S_IFDIR || (strictIdentity && (result.Uid != uint32(os.Geteuid()) || result.Mode&0777 != 0700)) {
		_ = unix.Close(fd)
		return -1, result, errors.New("artifact directory differs from runner-pinned identity, owner, or mode")
	}
	return fd, result, nil
}

var pilotGoldenHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var pilotGoldenCallbackName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
var pilotGoldenApprovedName = setOf("", "failed.sqlite", "operations.sqlite", "operations.sqlite-journal", "operations.sqlite-wal")

func pilotGoldenReasonCode(s pilotTraceScenario) (*string, error) {
	if s.Status == "CANDIDATE" {
		if s.Reason != "" || s.Oracle["reason"] != "" {
			return nil, errors.New("candidate carries a reason")
		}
		return nil, nil
	}
	reason, ok := s.Oracle["reason"].(string)
	if !ok || reason != s.Reason {
		return nil, errors.New("NOT RUN oracle reason mismatch")
	}
	code := ""
	switch s.ID {
	case "VFSFLAGS-FAILED-OPEN":
		if reason == "SQLite rejected the missing-parent open before invoking xOpen" {
			code = "SQLITE_REJECTED_BEFORE_XOPEN"
		}
	case "VFSFLAGS-SIDECAR-EXISTING":
		if reason == "second connection did not request xOpen for existing WAL sidecar" {
			code = "NO_EXISTING_WAL_XOPEN"
		}
	case "VFSFLAGS-SIDECAR-EEXIST-REOPEN":
		switch {
		case strings.HasPrefix(reason, "exact WAL path was observed, but no O_CREAT|O_EXCL attempt occurred"):
			code = "HOST_DID_NOT_ISSUE_EXCLUSIVE_CREATE"
		case strings.HasPrefix(reason, "child completed without an exact temporary WAL path attempt"):
			code = "HOST_DID_NOT_ISSUE_EXACT_WAL_PATH"
		case regexp.MustCompile(`^interposer failed closed before any injection \(errno=[0-9]+\)$`).MatchString(reason):
			if len(s.ChildExecutions) == 1 && s.ChildExecutions[0].TerminalOutcome != nil && *s.ChildExecutions[0].TerminalOutcome == "INTERPOSER_UNAVAILABLE_BEFORE_EXCLUSIVE_CREATE" {
				code = "INTERPOSER_FAILED_CLOSED"
			}
		case strings.HasPrefix(reason, "EEXIST child could not start:"):
			code = "CHILD_START_FAILED"
		case strings.HasPrefix(reason, "test interposer unavailable before child start:"):
			code = "INTERPOSER_UNAVAILABLE"
		}
	case "SHM-CROSS-PROCESS-CONTEND":
		if strings.Contains(reason, "READY missing") || strings.Contains(reason, "COMMIT_DONE missing") || strings.Contains(reason, "structured pre-READY SQLite error") || strings.Contains(reason, "structured terminal SQLite error") {
			code = "SHM_CHILD_BARRIER_NOT_OBSERVED"
		}
	case "SHM-FINAL-UNMAP-REOPEN":
		if strings.Contains(reason, "FINAL_CLOSE_READY barrier missing") || strings.Contains(reason, "UNMAP_DONE barrier missing") || strings.Contains(reason, "mapping lifetime was not observed") {
			code = "SHM_MAPPING_LIFETIME_NOT_ESTABLISHED"
		}
	case "PAGER-HOT-ROLLBACK-EXCLUSIVE":
		if reason == "child completed without a successful journal xSync barrier" || strings.Contains(reason, "without a qualifying main-file write") {
			code = "HOT_ROLLBACK_TRIGGER_NOT_REACHED"
		}
	case "PAGER-EMPTY-OBSOLETE-RESERVED":
		if strings.HasPrefix(reason, "PAGER-EMPTY-OBSOLETE-RESERVED setup failed before SQLite open:") {
			code = "PAGER_EMPTY_SETUP_FAILED"
		}
	}
	if code == "" {
		return nil, errors.New("NOT RUN is not eligible for this scenario or reason")
	}
	if _, ok := s.Oracle["observed_outcome"].(string); !ok || s.Oracle["observed_outcome"] != "NOT_OBSERVED" {
		return nil, errors.New("NOT RUN observed outcome mismatch")
	}
	return &code, nil
}

func pilotGoldenNormalizeEvents(input []pilotTraceEvent) ([]pilotGoldenEvent, map[string]map[int]string, error) {
	out := make([]pilotGoldenEvent, 0, len(input))
	handles := map[string]map[int]string{}
	for index, event := range input {
		actorAllow := map[string]bool{}
		for actor := range pilotGoldenStringAllow["connection"] {
			actorAllow[actor] = true
		}
		actorAllow["parent"], actorAllow["child"] = true, true
		if event.SourceID != pinnedSQLiteSourceID || !pilotGoldenCallbackName.MatchString(event.Callback) || !pilotGoldenStringAllow["connection"][event.ConnID] || !actorAllow[event.Actor] || !pilotGoldenStringAllow["process"][event.Process] || !pilotGoldenStringAllow["role"][event.Role] {
			return nil, nil, errors.New("event contains an unapproved callback/role/identity")
		}
		if !pilotGoldenApprovedName[event.Name] || !pilotGoldenApprovedName[event.OutputName] {
			return nil, nil, errors.New("event contains an unapproved path token")
		}
		var handle *string
		if event.HandleID != nil {
			if handles[event.Process] == nil {
				handles[event.Process] = map[int]string{}
			}
			value, ok := handles[event.Process][*event.HandleID]
			if !ok {
				value = fmt.Sprintf("h%d", len(handles[event.Process])+1)
				handles[event.Process][*event.HandleID] = value
			}
			handle = &value
		}
		out = append(out, pilotGoldenEvent{SourceID: event.SourceID, Seq: index + 1, ConnID: event.ConnID, Actor: event.Actor, Process: event.Process, Callback: event.Callback, HandleID: handle, Name: event.Name, OutputName: event.OutputName, Role: event.Role, Arg0: event.Arg0, Arg1: event.Arg1, Arg2: event.Arg2, RC: event.RC, Out: event.Out})
	}
	return out, handles, nil
}

func pilotGoldenCallbacks(events []pilotGoldenEvent) []string {
	set := map[string]bool{}
	for _, event := range events {
		set[event.Callback] = true
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func pilotGoldenXOpens(events []pilotGoldenEvent) []pilotGoldenXOpen {
	result := make([]pilotGoldenXOpen, 0)
	for _, event := range events {
		if event.Callback == "xOpen" {
			result = append(result, pilotGoldenXOpen{ProcessRole: event.Process, ConnectionID: event.ConnID, Role: event.Role, RC: event.RC, RequestedFlags: event.Arg0, ObservedFlags: event.Arg1, POutFlags: event.Out})
		}
	}
	return result
}

func pilotGoldenValidateDetails(id, status string, details map[string]any) error {
	expected, known := pilotGoldenDetailKeys[id]
	if strings.HasPrefix(id, "VFSFLAGS-") {
		expected, known = "", true
	}
	if id == "VFSFLAGS-SIDECAR-EEXIST-REOPEN" && status == "CANDIDATE" {
		expected, known = "exclusive_create_attempts,injected_eexist_count,interposer_sha256,retry_success,scope,target_token,wal_open_count", true
	}
	if !known {
		return errors.New("unknown scenario detail schema")
	}
	if status == "NOT RUN" {
		switch id {
		case "VFSFLAGS-FAILED-OPEN", "VFSFLAGS-SIDECAR-EXISTING", "VFSFLAGS-SIDECAR-EEXIST-REOPEN", "PAGER-EMPTY-OBSOLETE-RESERVED", "PAGER-HOT-ROLLBACK-EXCLUSIVE", "SHM-FINAL-UNMAP-REOPEN":
			expected = ""
		case "LOCK-CROSS-PROCESS-CONTEND", "SHM-CROSS-PROCESS-CONTEND":
			expected = "scope"
		}
	}
	keys := []string{}
	if expected != "" {
		keys = strings.Split(expected, ",")
	}
	if id == "SHM-CROSS-PROCESS-CONTEND" && status == "CANDIDATE" {
		base := strings.Split(expected, ",")
		if _, has := details["child_error"]; has {
			base = append(base, "child_terminal", "child_error", "child_error_control_seq", "child_error_control_message")
		}
		keys = base
	}
	if len(details) != len(keys) {
		return errors.New("scenario detail key set mismatch")
	}
	for _, key := range keys {
		if _, ok := details[key]; !ok {
			return fmt.Errorf("missing scenario detail key %s", key)
		}
	}
	if strings.HasPrefix(id, "VFSFLAGS-") && id != "VFSFLAGS-SIDECAR-EEXIST-REOPEN" && len(details) != 0 {
		return errors.New("VFS flag details must be empty")
	}
	return pilotGoldenValidateDetailValues(id, details)
}

func pilotGoldenValidateDetailValues(id string, details map[string]any) error {
	for key, value := range details {
		if key == "scope" {
			if scope, ok := value.(string); !ok || strings.TrimSpace(scope) == "" || pilotGoldenHasPath(scope) {
				return errors.New("scope must be a fixed sanitized string")
			}
		}
		if strings.Contains(strings.ToLower(key), "snapshot") {
			if snapshot, ok := value.(map[string]any); ok {
				for field := range snapshot {
					if field != "file" && field != "bytes" && field != "sha256" {
						return errors.New("unknown snapshot field")
					}
				}
				if file, ok := snapshot["file"].(string); ok && !strings.HasSuffix(file, ".bin") {
					return errors.New("invalid snapshot artifact token")
				}
			}
		}
		if key != "scope" && pilotGoldenContainsPath(value) {
			return errors.New("detail contains path-like host data")
		}
	}
	return nil
}

func pilotGoldenContainsPath(value any) bool {
	switch item := value.(type) {
	case string:
		return pilotGoldenHasPath(item)
	case map[string]any:
		for _, child := range item {
			if pilotGoldenContainsPath(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if pilotGoldenContainsPath(child) {
				return true
			}
		}
	}
	return false
}

func pilotGoldenHasPath(value string) bool {
	return strings.Contains(value, "/") || strings.Contains(value, `:\`) || strings.Contains(value, "\\")
}

func TestGoldenSHMBarrierDescriptionUsesPathFreeLanguage(t *testing.T) {
	details := map[string]any{"callback_barrier_edges": []any{
		"child-B BEGIN IMMEDIATE and xShmLock before READY",
		"parent-A START_WRITE after READY",
		"child-B update and commit after START_WRITE",
		"child-B terminal callbacks before COMMIT_DONE",
	}}
	if pilotGoldenContainsPath(details) {
		t.Fatal("fixed barrier description was misclassified as host path data")
	}
}

func pilotGoldenNormalizeDetails(scenarioID string, input map[string]any, events []pilotTraceEvent) (map[string]any, error) {
	copyMap := make(map[string]any, len(input))
	for key, value := range input {
		copyMap[key] = value
	}
	for key, value := range copyMap {
		if key == "durable_trace" {
			if nested, ok := value.(map[string]any); ok {
				delete(nested, "artifact_ref")
				delete(nested, "sha256")
			}
		}
		if key == "crash_db_snapshot" || key == "crash_journal_snapshot" {
			if nested, ok := value.(map[string]any); ok {
				delete(nested, "file")
			}
		}
	}
	if token, ok := copyMap["target_token"].(string); ok {
		if !pilotGoldenHex64.MatchString(token) {
			return nil, errors.New("invalid EEXIST target token")
		}
		copyMap["target_token"] = "TARGET_TOKEN_SHA256_PRESENT"
	}
	resolveSequence := func(old uint64, detailPath string) (int, bool) {
		process, actor := pilotGoldenReferenceParticipant(scenarioID, detailPath)
		found, position := false, 0
		for index, event := range events {
			if event.Seq != old || (process != "" && event.Process != process) || (actor != "" && event.Actor != actor && event.ConnID != actor) {
				continue
			}
			if found {
				return 0, false
			}
			found, position = true, index+1
		}
		return position, found
	}
	var normalize func(any, string, string) error
	normalize = func(value any, parent, keyPath string) error {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				if key == "file" && (parent == "crash_db_snapshot" || parent == "crash_journal_snapshot") {
					delete(item, key)
					continue
				}
				if (key == "artifact_ref" || key == "sha256") && parent == "durable_trace" {
					delete(item, key)
					continue
				}
				if key == "sequence" || strings.HasSuffix(key, "_seq") || (key == "seq" && (parent == "child_writer_lock_acquire" || parent == "child_writer_lock_release")) {
					if key == "after_message_seq" || key == "before_message_seq" || key == "child_error_control_seq" {
						continue
					}
					old, ok := pilotGoldenUint(child)
					if !ok {
						return fmt.Errorf("invalid event sequence field %s", key)
					}
					position, ok := resolveSequence(old, keyPath+"."+key)
					if !ok {
						return fmt.Errorf("unresolved event sequence field %s", key)
					}
					item[key] = position
					continue
				}
				if err := normalize(child, key, keyPath+"."+key); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := normalize(child, parent, keyPath); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := normalize(copyMap, "", ""); err != nil {
		return nil, err
	}
	return copyMap, nil
}

func pilotGoldenReferenceParticipant(scenarioID, detailPath string) (string, string) {
	key := detailPath[strings.LastIndex(detailPath, ".")+1:]
	switch scenarioID {
	case "LOCK-LOCAL-CONTEND":
		if strings.HasPrefix(key, "a_") {
			return "", "participant-A"
		}
		if strings.HasPrefix(key, "b_") {
			return "", "participant-B"
		}
	case "LOCK-CROSS-PROCESS-CONTEND":
		if strings.HasPrefix(key, "a_") {
			return "", "parent-A"
		}
		if strings.HasPrefix(key, "b_") {
			return "", "child-B"
		}
	case "LOCK-PROMOTION-BUSY":
		if strings.HasPrefix(key, "a_") {
			return "", "participant-A"
		}
		if strings.HasPrefix(key, "b_") {
			return "", "participant-B"
		}
	case "LOCK-DOWNGRADE":
		return "", "participant-A"
	case "SHM-CROSS-PROCESS-CONTEND":
		return "", "child-B"
	case "SHM-FINAL-UNMAP-REOPEN":
		if strings.HasPrefix(key, "participant_a") {
			return "child", "participant-A"
		}
		if strings.HasPrefix(key, "participant_b") {
			return "parent", "participant-B"
		}
	case "PAGER-HOT-ROLLBACK-EXCLUSIVE":
		return "crash-child", "crash-child"
	}
	return "", ""
}

func pilotGoldenNormalizeHandleRefs(details map[string]any, handles map[string]map[int]string) error {
	var visit func(any, string) error
	visit = func(value any, process string) error {
		switch item := value.(type) {
		case map[string]any:
			if role, ok := item["process_role"].(string); ok {
				process = role
			}
			for key, child := range item {
				if strings.Contains(key, "handle_id") && child != nil {
					if key == "participant_b_map_handle_id" {
						process = "parent"
					}
					if key == "main_write_handle_id" {
						process = "crash-child"
					}
					old, ok := pilotGoldenInt(child)
					if !ok {
						return errors.New("invalid handle reference")
					}
					mapped, ok := handles[process][old]
					if !ok {
						return errors.New("unresolved file handle reference")
					}
					item[key] = mapped
				} else if err := visit(child, process); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := visit(child, process); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(details, "")
}

func pilotGoldenNormalizeChildren(input []pilotTraceChildExecution) ([]pilotGoldenChild, error) {
	out := make([]pilotGoldenChild, 0, len(input))
	terminalAllow := setOf("CHILD_EXIT_NO_QUALIFYING_EXCLUSIVE_CREATE", "CHILD_EXIT_NO_EXACT_PATH_ATTEMPT", "COMMIT_DONE", "EEXIST_INJECTED_AND_WAL_OPEN_RETRIED", "FINAL_CLOSE_READY", "INTERPOSER_UNAVAILABLE_BEFORE_EXCLUSIVE_CREATE", "RELEASED", "RESERVED_ACQUIRED", "SIGKILL_AFTER_MAIN_WRITE_DELEGATED_OK", "SQLITE_ERROR", "UNMAP_DONE", "WRITER_READY")
	for _, child := range input {
		commandAllow := setOf("go test binary -test.run=^TestPilotVFSChildEEXIST$", "go test binary -test.run=^TestPilotVFSChildLockContention$", "go test binary -test.run=^TestPilotVFSChildPagerCrash$", "go test binary -test.run=^TestPilotVFSChildSHMContention$", "go test binary -test.run=^TestPilotVFSChildSHMFinalUnmapReopen$")
		processAllow := setOf("child", "child-A", "child-B", "crash-child", "eexist-child", "parent", "recovery-parent")
		if !commandAllow[child.CommandTemplate] || !processAllow[child.ProcessRole] {
			return nil, errors.New("unapproved child command or process role")
		}
		if child.ExecutionStatus != "CANDIDATE" && child.ExecutionStatus != "NOT RUN" {
			return nil, errors.New("invalid child status")
		}
		if child.TerminalOutcome != nil && !terminalAllow[*child.TerminalOutcome] {
			return nil, errors.New("invalid child terminal outcome")
		}
		var errorCode *string
		if child.ErrorSummary != "" {
			value := ""
			switch {
			case child.ErrorSummary == "structured SQLite error; see allowlisted oracle details":
				value = "STRUCTURED_SQLITE_ERROR"
			case regexp.MustCompile(`^interposer unavailable \(errno=[0-9]+\)$`).MatchString(child.ErrorSummary):
				value = "INTERPOSER_UNAVAILABLE"
			default:
				return nil, errors.New("unapproved child error summary")
			}
			errorCode = &value
		}
		if child.CleanupStatus != "CLEAN" && child.CleanupStatus != "INCOMPLETE" && child.CleanupStatus != "NOT_APPLICABLE" {
			return nil, errors.New("invalid child cleanup status")
		}
		out = append(out, pilotGoldenChild{CommandTemplate: child.CommandTemplate, ProcessRole: child.ProcessRole, Started: child.Started, ExecutionStatus: child.ExecutionStatus, ExitCode: child.ExitCode, TerminationSignal: child.TerminationSignal, TerminalOutcome: child.TerminalOutcome, CleanupStatus: child.CleanupStatus, ChildControlFDClosed: child.ChildControlFDClosed, ParentControlFDClosed: child.ParentControlFDClosed, TracePersisted: child.TracePersisted, TimedOut: child.TimedOut, Killed: child.Killed, Reaped: child.Reaped, ErrorSummaryCode: errorCode})
	}
	return out, nil
}

func pilotGoldenInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), int64(int(number)) == number
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	}
	return 0, false
}

func pilotGoldenUint(value any) (uint64, bool) {
	switch number := value.(type) {
	case uint64:
		return number, true
	case int:
		if number >= 0 {
			return uint64(number), true
		}
	case int64:
		if number >= 0 {
			return uint64(number), true
		}
	case json.Number:
		parsed, err := number.Int64()
		return uint64(parsed), err == nil && parsed >= 0
	}
	return 0, false
}

func pilotGoldenRejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := pilotGoldenScanJSON(decoder); err != nil {
		return err
	}
	return pilotGoldenRequireEOF(decoder)
}

func pilotGoldenScanJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %s", key)
			}
			seen[key] = true
			if err := pilotGoldenScanJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := pilotGoldenScanJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func pilotGoldenRequireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func pilotGoldenFirstDifference(a, b []byte) string {
	var left, right any
	_ = json.Unmarshal(a, &left)
	_ = json.Unmarshal(b, &right)
	return pilotGoldenDiffValue(left, right, "$")
}

func pilotGoldenDiffValue(left, right any, path string) string {
	if reflect.DeepEqual(left, right) {
		return "none"
	}
	lm, lok := left.(map[string]any)
	rm, rok := right.(map[string]any)
	if lok && rok {
		keys := map[string]bool{}
		for k := range lm {
			keys[k] = true
		}
		for k := range rm {
			keys[k] = true
		}
		ordered := make([]string, 0, len(keys))
		for k := range keys {
			ordered = append(ordered, k)
		}
		sort.Strings(ordered)
		for _, k := range ordered {
			if !reflect.DeepEqual(lm[k], rm[k]) {
				return pilotGoldenDiffValue(lm[k], rm[k], path+"."+k)
			}
		}
	}
	return path
}

func TestVFSCallbackGoldenNormalizer(t *testing.T) {
	if err := pilotGoldenRejectDuplicateJSONKeys([]byte(`{"x":1,"x":2}`)); err == nil {
		t.Fatal("duplicate object keys accepted")
	}
	if err := pilotGoldenRejectDuplicateJSONKeys([]byte(`{"x":{"y":1,"y":2}}`)); err == nil {
		t.Fatal("nested duplicate object keys accepted")
	}
	if _, ok := pilotGoldenInt(json.Number("1.0")); ok {
		t.Fatal("decimal number accepted as integer")
	}
	if _, ok := pilotGoldenInt(json.Number("9223372036854775808")); ok {
		t.Fatal("out-of-range integer accepted")
	}
	base := pilotGoldenTestCandidate("sqlite3451-vfs-flags")
	baseline := pilotGoldenNormalizeTestCandidate(t, base)

	volatile := base
	volatile.RunID = "ffffffffffffffffffffffffffffffff"
	volatile.Execution.Kernel = "new-kernel-release"
	if got := pilotGoldenNormalizeTestCandidate(t, volatile); !bytes.Equal(got, baseline) {
		t.Fatal("volatile run metadata changed canonical fixture")
	}

	mutations := []struct {
		name  string
		apply func(*pilotTraceCandidate)
	}{
		{"callback", func(c *pilotTraceCandidate) { c.Scenarios[1].Events[0].Callback = "xDelete" }},
		{"event order", func(c *pilotTraceCandidate) {
			c.Scenarios[1].Events[0], c.Scenarios[1].Events[1] = c.Scenarios[1].Events[1], c.Scenarios[1].Events[0]
		}},
		{"flags", func(c *pilotTraceCandidate) { c.Scenarios[1].Events[0].Arg0++ }},
		{"result", func(c *pilotTraceCandidate) { c.Scenarios[1].Events[0].RC++ }},
		{"handle", func(c *pilotTraceCandidate) { *c.Scenarios[1].Events[0].HandleID++ }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := pilotGoldenTestCandidate("sqlite3451-vfs-flags")
			mutation.apply(&candidate)
			got := pilotGoldenNormalizeTestCandidate(t, candidate)
			if bytes.Equal(got, baseline) {
				t.Fatalf("%s mutation did not change normalized fixture", mutation.name)
			}
		})
	}
	wrongOrder := pilotGoldenTestCandidate("sqlite3451-vfs-flags")
	wrongOrder.Scenarios[0], wrongOrder.Scenarios[1] = wrongOrder.Scenarios[1], wrongOrder.Scenarios[0]
	if _, err := pilotNormalizeGoldenCandidate(pilotGoldenMarshalTestCandidate(t, wrongOrder), wrongOrder.FixtureGroup); err == nil {
		t.Fatal("scenario order mutation accepted")
	}
	wrongCount := pilotGoldenTestCandidate("sqlite3451-vfs-flags")
	wrongCount.Scenarios = wrongCount.Scenarios[:len(wrongCount.Scenarios)-1]
	if _, err := pilotNormalizeGoldenCandidate(pilotGoldenMarshalTestCandidate(t, wrongCount), wrongCount.FixtureGroup); err == nil {
		t.Fatal("scenario count mutation accepted")
	}
	wrongSource := pilotGoldenTestCandidate("sqlite3451-vfs-flags")
	wrongSource.Scenarios[1].Events[0].SourceID = "unreviewed-source"
	if _, err := pilotNormalizeGoldenCandidate(pilotGoldenMarshalTestCandidate(t, wrongSource), wrongSource.FixtureGroup); err == nil {
		t.Fatal("source ID mutation accepted")
	}
	badReason := pilotGoldenTestCandidate("sqlite3451-vfs-flags")
	badReason.Scenarios[5].Reason = "unreviewed host text"
	badReason.Scenarios[5].Oracle["reason"] = badReason.Scenarios[5].Reason
	if _, err := pilotNormalizeGoldenCandidate(pilotGoldenMarshalTestCandidate(t, badReason), badReason.FixtureGroup); err == nil {
		t.Fatal("unknown NOT RUN reason accepted")
	}
	unknown := strings.Replace(string(pilotGoldenMarshalTestCandidate(t, base)), `"id":"VFSFLAGS-FAILED-OPEN"`, `"id":"VFSFLAGS-FAILED-OPEN","host_path":"/tmp/secret"`, 1)
	if _, err := pilotNormalizeGoldenCandidate([]byte(unknown), base.FixtureGroup); err == nil {
		t.Fatal("unknown scenario field accepted")
	}
}

func TestPilotGoldenPagerEvidenceVerification(t *testing.T) {
	makeFixture := func(t *testing.T) (string, []pilotTraceChildArtifact, map[string]any) {
		t.Helper()
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		var info unix.Stat_t
		if err := unix.Stat(dir, &info); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PILOT_VFS_ARTIFACT_DIR_DEVICE", strconv.FormatUint(info.Dev, 10))
		t.Setenv("PILOT_VFS_ARTIFACT_DIR_INODE", strconv.FormatUint(info.Ino, 10))
		t.Setenv("PILOT_VFS_ARTIFACT_DIR", dir)
		contents := map[string][]byte{
			"pager-hot-rollback-crash-db.bin":      []byte("stable database snapshot"),
			"pager-hot-rollback-crash-journal.bin": pilotGoldenTestJournal(0x7a19c321),
		}
		artifacts := make([]pilotTraceChildArtifact, 0, 2)
		details := map[string]any{}
		for name, body := range contents {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(body)
			artifacts = append(artifacts, pilotTraceChildArtifact{File: name, SHA256: hex.EncodeToString(hash[:]), Bytes: len(body)})
			field := "crash_db_snapshot"
			if strings.Contains(name, "journal") {
				field = "crash_journal_snapshot"
			}
			details[field] = map[string]any{"file": name, "sha256": hex.EncodeToString(hash[:]), "bytes": len(body)}
		}
		return dir, artifacts, details
	}

	t.Run("valid evidence", func(t *testing.T) {
		dir, artifacts, details := makeFixture(t)
		t.Setenv("PILOT_VFS_ARTIFACT_DIR", dir)
		details["durable_trace"] = map[string]any{"artifact_ref": "trace.jsonl", "persisted_before_main_write_ack": true, "sequence": 500, "sha256": strings.Repeat("a", 64)}
		details["durable_trace_stage"] = "fdatasync completed before MAIN_WRITE_DELEGATED_OK was sent"
		details["journal_present_before_kill"] = true
		details["journal_sync_callback_seq"] = 500
		details["main_write_callback_returned"] = false
		details["main_write_callback_seq"] = 500
		details["main_write_delegate_rc"] = 0
		details["main_write_handle_id"] = 7
		details["main_write_offset"] = 0
		details["main_write_size"] = 4096
		details["old_committed_value"] = "old"
		details["recovered_value"] = "old"
		details["scope"] = "host unix VFS callback observation and rollback recovery only; not private VFS acceptance"
		event := pilotTraceEvent{SourceID: pinnedSQLiteSourceID, Seq: 500, ConnID: "crash-child", Actor: "crash-child", Process: "crash-child", Callback: "xWrite", HandleID: pilotGoldenIntPointer(7), Name: "operations.sqlite", Role: "MAIN_DB", RC: 0}
		scenario := pilotTraceScenario{ID: "PAGER-HOT-ROLLBACK-EXCLUSIVE", Status: "CANDIDATE", Events: []pilotTraceEvent{event}, EvidenceArtifacts: artifacts, Oracle: map[string]any{"expected_outcome": pilotTraceExpectedOutcome("PAGER-HOT-ROLLBACK-EXCLUSIVE"), "observed_outcome": "observed", "execution_status": "CANDIDATE", "reason": "", "details": details, "event_count": 1}}
		normalized, err := pilotGoldenNormalizeScenario(scenario)
		if err != nil {
			t.Fatal(err)
		}
		if len(normalized.EvidenceArtifacts) != 0 {
			t.Fatal("pager input evidence artifacts leaked into normalized golden")
		}
		for _, key := range []string{"crash_db_snapshot", "crash_journal_snapshot"} {
			snapshot := normalized.Oracle.Details[key].(map[string]any)
			inputSnapshot := details[key].(map[string]any)
			wantHash := inputSnapshot["sha256"]
			if key == "crash_journal_snapshot" {
				var journal []byte
				for _, artifact := range artifacts {
					if artifact.File == "pager-hot-rollback-crash-journal.bin" {
						journal, err = pilotGoldenReadArtifactFile(dir, artifact)
						break
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				wantHash, err = pilotGoldenProjectCrashJournal(journal)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(snapshot) != 2 || snapshot["bytes"] != inputSnapshot["bytes"] || snapshot["sha256"] != wantHash {
				t.Fatalf("normalized snapshot %s lost stable evidence: %#v", key, snapshot)
			}
			if _, hasFilename := snapshot["file"]; hasFilename {
				t.Fatalf("normalized snapshot %s retained volatile filename", key)
			}
		}
	})
	t.Run("content hash and byte tamper", func(t *testing.T) {
		dir, artifacts, details := makeFixture(t)
		original, err := os.ReadFile(filepath.Join(dir, artifacts[0].File))
		if err != nil {
			t.Fatal(err)
		}
		tampered := bytes.Repeat([]byte{'x'}, len(original))
		if bytes.Equal(tampered, original) {
			tampered[0] ^= 1
		}
		if err := os.WriteFile(filepath.Join(dir, artifacts[0].File), tampered, 0600); err != nil {
			t.Fatal(err)
		}
		if err := pilotGoldenValidateEvidence(dir, "PAGER-HOT-ROLLBACK-EXCLUSIVE", "CANDIDATE", artifacts, details); err == nil {
			t.Fatal("tampered file accepted")
		}
	})
	t.Run("zero byte artifact", func(t *testing.T) {
		dir, artifacts, details := makeFixture(t)
		artifacts[0].Bytes = 0
		details["crash_db_snapshot"].(map[string]any)["bytes"] = 0
		if err := os.WriteFile(filepath.Join(dir, artifacts[0].File), nil, 0600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(nil)
		artifacts[0].SHA256 = hex.EncodeToString(hash[:])
		details["crash_db_snapshot"].(map[string]any)["sha256"] = artifacts[0].SHA256
		if err := pilotGoldenValidateEvidence(dir, "PAGER-HOT-ROLLBACK-EXCLUSIVE", "CANDIDATE", artifacts, details); err == nil {
			t.Fatal("zero-byte snapshot accepted")
		}
	})
	t.Run("detail hash mismatch", func(t *testing.T) {
		dir, artifacts, details := makeFixture(t)
		details["crash_db_snapshot"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
		if err := pilotGoldenValidateEvidence(dir, "PAGER-HOT-ROLLBACK-EXCLUSIVE", "CANDIDATE", artifacts, details); err == nil {
			t.Fatal("snapshot metadata mismatch accepted")
		}
	})
	t.Run("missing duplicate and extra artifact", func(t *testing.T) {
		for _, mutate := range []struct {
			name string
			fn   func([]pilotTraceChildArtifact) []pilotTraceChildArtifact
		}{
			{"missing", func(a []pilotTraceChildArtifact) []pilotTraceChildArtifact { return a[:1] }},
			{"duplicate", func(a []pilotTraceChildArtifact) []pilotTraceChildArtifact { return append(a, a[0]) }},
			{"extra basename", func(a []pilotTraceChildArtifact) []pilotTraceChildArtifact { a[0].File = "other.bin"; return a }},
		} {
			t.Run(mutate.name, func(t *testing.T) {
				dir, artifacts, details := makeFixture(t)
				if err := pilotGoldenValidateEvidence(dir, "PAGER-HOT-ROLLBACK-EXCLUSIVE", "CANDIDATE", mutate.fn(artifacts), details); err == nil {
					t.Fatal("invalid artifact set accepted")
				}
			})
		}
	})
	t.Run("symlink and non regular file", func(t *testing.T) {
		for _, nonRegular := range []bool{false, true} {
			t.Run(fmt.Sprintf("non_regular_%t", nonRegular), func(t *testing.T) {
				dir, artifacts, details := makeFixture(t)
				target := filepath.Join(dir, artifacts[0].File)
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if nonRegular {
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(filepath.Join(dir, artifacts[1].File), target); err != nil {
					t.Fatal(err)
				}
				if err := pilotGoldenValidateEvidence(dir, "PAGER-HOT-ROLLBACK-EXCLUSIVE", "CANDIDATE", artifacts, details); err == nil {
					t.Fatal("unsafe artifact file accepted")
				}
			})
		}
	})
	t.Run("symlinked ancestor", func(t *testing.T) {
		base := t.TempDir()
		realParent := filepath.Join(base, "real-parent")
		if err := os.Mkdir(realParent, 0700); err != nil {
			t.Fatal(err)
		}
		finalDir := filepath.Join(realParent, "run")
		if err := os.Mkdir(finalDir, 0700); err != nil {
			t.Fatal(err)
		}
		var info unix.Stat_t
		if err := unix.Stat(finalDir, &info); err != nil {
			t.Fatal(err)
		}
		aliasParent := filepath.Join(base, "link-parent")
		if err := os.Symlink(realParent, aliasParent); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(aliasParent, "run")
		t.Setenv("PILOT_VFS_ARTIFACT_DIR", alias)
		t.Setenv("PILOT_VFS_ARTIFACT_DIR_DEVICE", strconv.FormatUint(info.Dev, 10))
		t.Setenv("PILOT_VFS_ARTIFACT_DIR_INODE", strconv.FormatUint(info.Ino, 10))
		if _, _, err := pilotGoldenOpenPinnedArtifactDir(alias); err == nil {
			t.Fatal("symlinked artifact ancestor accepted")
		}
	})
	t.Run("runner identity mismatch", func(t *testing.T) {
		dir, _, _ := makeFixture(t)
		var info unix.Stat_t
		if err := unix.Stat(dir, &info); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PILOT_VFS_ARTIFACT_DIR_INODE", strconv.FormatUint(info.Ino+1, 10))
		if _, _, err := pilotGoldenOpenPinnedArtifactDir(dir); err == nil {
			t.Fatal("artifact directory differing from runner-pinned identity accepted")
		}
	})
}

func pilotGoldenTestCandidate(group string) pilotTraceCandidate {
	ids := pilotGoldenScenarioOrder[group]
	candidate := pilotTraceCandidate{FixtureGroup: group, Status: "CANDIDATE", RunID: "0123456789abcdef0123456789abcdef", SQLite: map[string]string{"version": pinnedSQLiteVersion, "version_number": fmt.Sprint(pinnedSQLiteVersionNum), "source_id": pinnedSQLiteSourceID, "vfs_name": pilotTraceVFSName, "parent_vfs": "unix", "scope": "host VFS behavior characterization only"}, Execution: pilotTraceExecution{Kernel: "test", OS: "linux", Arch: "amd64"}, Scenarios: make([]pilotTraceScenario, 0, len(ids))}
	for index, id := range ids {
		status, reason := "CANDIDATE", ""
		if id == "VFSFLAGS-SIDECAR-EEXIST-REOPEN" {
			status, reason = "NOT RUN", "child completed without an exact temporary WAL path attempt"
		}
		events := []pilotTraceEvent{{SourceID: pinnedSQLiteSourceID, Seq: uint64(100 + index*10), ConnID: "conn-A", Actor: "conn-A", Callback: "xOpen", HandleID: pilotGoldenIntPointer(index + 1), Name: "operations.sqlite", Role: "MAIN_DB", Arg0: 6, Arg1: 6, RC: 0, Out: 6}}
		if index == 1 {
			events = append(events, pilotTraceEvent{SourceID: pinnedSQLiteSourceID, Seq: uint64(101 + index*10), ConnID: "conn-A", Actor: "conn-A", Callback: "xClose", HandleID: pilotGoldenIntPointer(index + 1), Name: "operations.sqlite", Role: "MAIN_DB", RC: 0})
		}
		oracle := map[string]any{"expected_outcome": pilotTraceExpectedOutcome(id), "observed_outcome": "placeholder-observation", "execution_status": status, "reason": reason, "details": map[string]any{}, "event_count": len(events)}
		if status == "NOT RUN" {
			oracle["observed_outcome"] = "NOT_OBSERVED"
		}
		candidate.Scenarios = append(candidate.Scenarios, pilotTraceScenario{ID: id, Status: status, Reason: reason, Oracle: oracle, Events: events})
	}
	return candidate
}

func pilotGoldenIntPointer(value int) *int { return &value }

func pilotGoldenMarshalTestCandidate(t *testing.T, candidate pilotTraceCandidate) []byte {
	t.Helper()
	data, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pilotGoldenNormalizeTestCandidate(t *testing.T, candidate pilotTraceCandidate) []byte {
	t.Helper()
	normalized, err := pilotNormalizeGoldenCandidate(pilotGoldenMarshalTestCandidate(t, candidate), candidate.FixtureGroup)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestPilotGoldenReasonEligibilityMutations(t *testing.T) {
	mandatory := pilotTraceScenario{ID: "VFSFLAGS-JOURNAL-CREATE", Status: "NOT RUN", Reason: "transaction produced no main-journal xOpen", Oracle: map[string]any{"reason": "transaction produced no main-journal xOpen", "observed_outcome": "NOT_OBSERVED"}}
	if _, err := pilotGoldenReasonCode(mandatory); err == nil {
		t.Fatal("mandatory journal-create scenario was allowed to be relabeled NOT RUN")
	}
	postAttempt := pilotTraceScenario{ID: "VFSFLAGS-SIDECAR-EEXIST-REOPEN", Status: "NOT RUN", Reason: "interposer failed closed before any injection (errno=38)", Oracle: map[string]any{"reason": "interposer failed closed before any injection (errno=38)", "observed_outcome": "NOT_OBSERVED"}, ChildExecutions: []pilotTraceChildExecution{{TerminalOutcome: pilotTraceTerminal("INTERPOSER_UNAVAILABLE_BEFORE_EXCLUSIVE_CREATE")}}}
	if _, err := pilotGoldenReasonCode(postAttempt); err != nil {
		t.Fatalf("pre-request initial unavailable case must be eligible: %v", err)
	}
	postAttempt.ChildExecutions[0].TerminalOutcome = pilotTraceTerminal("EEXIST_INJECTED_AND_WAL_OPEN_RETRIED")
	if _, err := pilotGoldenReasonCode(postAttempt); err == nil {
		t.Fatal("post-request child outcome was accepted as NOT RUN")
	}
}

func pilotGoldenHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

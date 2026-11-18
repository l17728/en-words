//go:build cgo && pilot_e2e && linux

package pilotcontrol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

type pilotTraceEvent struct {
	RunID      string `json:"run_id"`
	ScenarioID string `json:"scenario_id"`
	CaseID     string `json:"case_id"`
	SourceID   string `json:"source_id"`
	Seq        uint64 `json:"seq"`
	ConnID     string `json:"connection_id"`
	Actor      string `json:"actor"`
	Process    string `json:"process_role,omitempty"`
	Callback   string `json:"callback"`
	HandleID   *int   `json:"file_handle_id"`
	Name       string `json:"name"`
	OutputName string `json:"output_name,omitempty"`
	Role       string `json:"role"`
	Arg0       int    `json:"arg0"`
	Arg1       int    `json:"arg1"`
	Arg2       int    `json:"arg2"`
	RC         int    `json:"rc"`
	Out        int    `json:"out"`
}

type pilotTraceScenario struct {
	ID                  string                     `json:"id"`
	Status              string                     `json:"status"`
	Reason              string                     `json:"reason,omitempty"`
	Oracle              map[string]any             `json:"oracle,omitempty"`
	OracleDetails       map[string]any             `json:"-"`
	OracleDetailsSHA256 string                     `json:"-"`
	ChildExecutions     []pilotTraceChildExecution `json:"child_executions,omitempty"`
	EvidenceArtifacts   []pilotTraceChildArtifact  `json:"evidence_artifacts,omitempty"`
	DurableTraceSeq     uint64                     `json:"-"`
	RawTraceFile        string                     `json:"raw_trace_file,omitempty"`
	RawTraceSHA256      string                     `json:"raw_trace_sha256,omitempty"`
	Events              []pilotTraceEvent          `json:"events,omitempty"`
}

type pilotTraceChildArtifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type pilotTraceChildExecution struct {
	CommandTemplate       string                   `json:"command_template"`
	ProcessRole           string                   `json:"process_role"`
	Started               bool                     `json:"started"`
	ExecutionStatus       string                   `json:"execution_status"`
	ExitCode              *int                     `json:"exit_code"`
	TerminationSignal     string                   `json:"termination_signal,omitempty"`
	Stdout                *pilotTraceChildArtifact `json:"stdout"`
	Stderr                *pilotTraceChildArtifact `json:"stderr"`
	ErrorSummary          string                   `json:"error_summary,omitempty"`
	TerminalOutcome       *string                  `json:"terminal_outcome"`
	CleanupStatus         string                   `json:"cleanup_status"`
	ChildControlFDClosed  bool                     `json:"child_control_fd_closed"`
	ParentControlFDClosed *bool                    `json:"parent_control_fd_closed,omitempty"`
	TracePersisted        bool                     `json:"trace_persisted"`
	TimedOut              bool                     `json:"timed_out"`
	Killed                bool                     `json:"killed"`
	Reaped                bool                     `json:"reaped"`
}

type pilotTraceSQLError struct {
	Version       int      `json:"version"`
	Kind          string   `json:"kind"`
	Phase         string   `json:"phase"`
	SQLiteCode    *int     `json:"sqlite_code,omitempty"`
	ExtendedCode  *int     `json:"extended_code,omitempty"`
	CleanupErrors []string `json:"cleanup_errors,omitempty"`
}

type pilotTraceCandidate struct {
	FixtureGroup    string               `json:"fixture_group"`
	Status          string               `json:"status"`
	TraceOverflowed bool                 `json:"trace_overflowed"`
	RunID           string               `json:"run_id"`
	SQLite          map[string]string    `json:"sqlite"`
	HarnessSHA      string               `json:"harness_sha256"`
	HarnessFiles    map[string]string    `json:"harness_files"`
	Execution       pilotTraceExecution  `json:"execution"`
	Scenarios       []pilotTraceScenario `json:"scenarios"`
}

type pilotTraceExecution struct {
	Command          string `json:"command"`
	ProcessRecordRef string `json:"process_record_ref"`
	LockTraceRef     string `json:"lock_syscall_trace_ref"`
	GoVersion        string `json:"go_version"`
	Compiler         string `json:"compiler"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	Kernel           string `json:"kernel"`
	CGOEnabled       bool   `json:"cgo_enabled"`
	CCompilerVersion string `json:"c_compiler_version"`
	Filesystem       string `json:"filesystem"`
	DriverRevision   string `json:"driver_revision"`
	ScenarioSeed     string `json:"scenario_seed"`
	TempRoot         string `json:"temp_root"`
}

var pilotTraceAbsolutePathPattern = regexp.MustCompile(`(?i)([a-z]:\\[^\s"']+|/(?:[^\s/"']+/)*[^\s"']+)`)

var pilotTraceAbsolutePathWithSpacesPattern = regexp.MustCompile(`(?i)\b[a-z]:\\|(?:^|[\s"'=(:,])/(?:[^/\s]+/)+`)

func pilotTraceExecutionMetadata(filesystemPath string) pilotTraceExecution {
	return pilotTraceExecution{
		Command:          "go test -exec <pilot-vfs-strace-wrapper> -tags=pilot_e2e -run '^TestPilotVFSRuntimeCandidateGeneration$' -count=1 -timeout=120s -v ./internal/pilotcontrol",
		ProcessRecordRef: "process_record.json",
		LockTraceRef:     "process_record.json#lock_syscall_trace",
		GoVersion:        runtime.Version(),
		Compiler:         runtime.Compiler,
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Kernel:           pilotTraceKernelRelease(),
		CGOEnabled:       true,
		CCompilerVersion: pilotTraceCCompilerVersion(),
		Filesystem:       pilotTraceSafeFilesystem(filesystemPath),
		DriverRevision:   pilotTraceDriverRevision(),
		ScenarioSeed:     "stable-scenario-id-v1; no randomized operations",
		TempRoot:         "<TEST_TEMP_ROOT>",
	}
}

func pilotTraceHandleID(event pilotTraceEvent) int {
	if event.HandleID == nil {
		return 0
	}
	return *event.HandleID
}

func pilotTraceKernelRelease() string {
	var info unix.Utsname
	if err := unix.Uname(&info); err != nil {
		return "unknown"
	}
	release := make([]byte, len(info.Release))
	for i, value := range info.Release {
		release[i] = byte(value)
	}
	return unix.ByteSliceToString(release)
}

func pilotTraceDriverRevision() string {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return "unavailable"
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "third_party", "go-sqlite3"))
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "unavailable"
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "unavailable"
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "unavailable"
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(rel)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
	}
	return "go-sqlite3-local-fork:" + hex.EncodeToString(hash.Sum(nil))
}

func pilotTraceCCompilerVersion() string {
	cc := strings.Fields(os.Getenv("CC"))
	if len(cc) == 0 {
		cc = []string{"cc"}
	}
	args := append(append([]string(nil), cc[1:]...), "--version")
	output, err := exec.Command(cc[0], args...).Output()
	if err != nil {
		return "unknown"
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "unknown"
	}
	return strings.TrimSpace(lines[0])
}

func pilotTraceSafeFilesystem(path string) string {
	output, err := exec.Command("findmnt", "--target", path, "--noheadings", "--output", "FSTYPE").Output()
	if err != nil {
		return "unknown"
	}
	value := strings.ToLower(strings.TrimSpace(string(output)))
	if value == "overlay" {
		value = "overlayfs"
	}
	switch value {
	case "ext4", "xfs", "btrfs", "tmpfs", "overlayfs":
		return value
	default:
		return "unknown"
	}
}

func pilotTraceExistingWALOpen(events []pilotTraceEvent, readerActor string) []pilotTraceEvent {
	return eventSubset(events, func(e pilotTraceEvent) bool {
		return e.Actor == readerActor && e.Callback == "xOpen" && e.Role == "WAL" && e.RC == 0
	})
}

func TestPilotVFSRuntimeCandidateGeneration(t *testing.T) {
	if !pilotTraceEntrypointEnabled(t, "candidate") {
		return
	}
	pilotTraceGenerateCandidates(t)
}

func pilotTraceEntrypointEnabled(t *testing.T, mode string) bool {
	t.Helper()
	active := os.Getenv("PILOT_VFS_MODE")
	if active == "" {
		t.Skip("Linux evidence entrypoint runs only through review/run_pilot_vfs_candidate.py")
	}
	if active != mode {
		t.Skipf("entrypoint mode %q is not selected by runner mode %q", mode, active)
	}
	return true
}

func pilotTraceGenerateCandidates(t *testing.T) {
	if version, number, sourceID := sqlite3.Version(); version != pinnedSQLiteVersion || number != pinnedSQLiteVersionNum || sourceID != pinnedSQLiteSourceID {
		t.Fatalf("runtime SQLite identity mismatch: %s/%d/%s; candidate generation NOT RUN", version, number, sourceID)
	}
	runID := os.Getenv("PILOT_VFS_RUN_ID")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(runID) {
		t.Fatal("PILOT_VFS_RUN_ID must be a unique 128-bit lowercase hex ID supplied by run_pilot_vfs_candidate.py; candidate generation NOT RUN")
	}
	artifactDir := os.Getenv("PILOT_VFS_ARTIFACT_DIR")
	if artifactDir == "" {
		t.Fatal("PILOT_VFS_ARTIFACT_DIR must name a persistent evidence directory; candidate generation NOT RUN")
	}
	if !filepath.IsAbs(artifactDir) {
		t.Fatalf("PILOT_VFS_ARTIFACT_DIR must be absolute; got %q; candidate generation NOT RUN", artifactDir)
	}
	sourcePath, err := currentPilotControlTestFile(t)
	if err != nil {
		t.Fatalf("resolve test source for artifact preflight: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
	if _, err := pilotVFSManifestArtifactDir(repoRoot, os.Getenv); err != nil {
		t.Fatalf("artifact directory isolation preflight: %v", err)
	}
	artifactFD, _, err := pilotGoldenOpenPinnedArtifactDir(artifactDir)
	if err != nil {
		t.Fatalf("open runner-pinned candidate artifact directory: %v", err)
	}
	artifactDirectory := os.NewFile(uintptr(artifactFD), "pilot-vfs-artifacts")
	entries, err := artifactDirectory.ReadDir(-1)
	closeErr := artifactDirectory.Close()
	if err != nil {
		t.Fatalf("inspect candidate artifact directory: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close candidate artifact directory: %v", closeErr)
	}
	if len(entries) != 0 {
		t.Fatalf("candidate artifact directory must be new and empty to preserve immutable evidence; found %d existing entries", len(entries))
	}
	root := t.TempDir()
	execution := pilotTraceExecutionMetadata(root)
	if execution.Kernel == "unknown" || execution.CCompilerVersion == "unknown" || execution.Filesystem == "unknown" || !strings.HasPrefix(execution.DriverRevision, "go-sqlite3-local-fork:") || execution.ScenarioSeed == "" {
		t.Fatalf("runtime provenance incomplete (kernel=%q filesystem=%q driver_revision=%q scenario_seed=%q); candidate generation NOT RUN", execution.Kernel, execution.Filesystem, execution.DriverRevision, execution.ScenarioSeed)
	}
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatal(err)
	}
	if parent := pilotTraceParentName(); parent != "unix" {
		t.Fatalf("trace VFS parent identity=%q, want unix", parent)
	}
	if !pilotTraceCapabilitiesPreserved() {
		t.Fatal("trace VFS changed the parent VFS version or hid an advertised extension method")
	}
	t.Cleanup(func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("unregister trace VFS: %v", err)
		}
	})

	dbPath := filepath.Join(root, "operations.sqlite")
	main, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	uri := (&urlForPilotDB{Path: filepath.ToSlash(dbPath)}).dsn()
	ctx := context.Background()
	version, number, sourceID := sqlite3.Version()
	rawTraces := make(map[string]string)
	rawTraceFiles := make(map[string]string)
	rawTraceHashes := make(map[string]string)
	var scenarios [4][]pilotTraceScenario

	// Failed xOpen must retain no synthetic file handle identity.
	failedURI := (&urlForPilotDB{Path: filepath.ToSlash(filepath.Join(root, "missing-parent", "failed.sqlite"))}).dsn()
	pilotTraceReset()
	pilotTraceSetContext(runID, "VFSFLAGS-FAILED-OPEN", "VFSFLAGS-FAILED-OPEN", sourceID)
	if failedConn, openErr := pilotTraceOpen(ctx, failedURI, "conn-failed"); openErr == nil {
		_ = pilotTraceClose(failedConn, "conn-failed")
		t.Fatal("open below a missing parent unexpectedly succeeded")
	}
	failedEvents := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, failedEvents, runID, "VFSFLAGS-FAILED-OPEN", sourceID)
	failedMainOpen := eventSubset(failedEvents, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "MAIN_DB" })
	if len(failedMainOpen) == 0 {
		if trace := pilotTraceDump(); trace != "" {
			rawTraces["VFSFLAGS-FAILED-OPEN"] = trace
		}
		scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-FAILED-OPEN", Status: "NOT RUN", Reason: "SQLite rejected the missing-parent open before invoking xOpen"})
	} else if len(failedMainOpen) != 1 || failedMainOpen[0].RC == 0 || failedMainOpen[0].HandleID != nil {
		t.Fatalf("failed xOpen must carry a nonzero result and null file_handle_id: %+v", failedMainOpen)
	} else {
		rawTraces["VFSFLAGS-FAILED-OPEN"] = pilotTraceDump()
		scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-FAILED-OPEN", Status: "CANDIDATE", Events: failedEvents})
	}

	// Main-file open uses the exact pinned URI and records the observed VFS request.
	pilotTraceReset()
	pilotTraceSetContext(runID, "VFSFLAGS-MAIN-OPEN", "VFSFLAGS-MAIN-OPEN", sourceID)
	connA, err := pilotTraceOpen(ctx, uri, "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	if err := pilotTraceQueryOne(ctx, connA, "conn-A", "SELECT 1"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("main SELECT 1: %v", err)
	}
	eventsMain := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, eventsMain, runID, "VFSFLAGS-MAIN-OPEN", sourceID)
	fullPath := eventSubset(eventsMain, func(e pilotTraceEvent) bool { return e.Callback == "xFullPathname" })
	if len(fullPath) == 0 {
		t.Fatal("main-open scenario trace omitted xFullPathname")
	}
	for _, event := range fullPath {
		if event.HandleID != nil || event.OutputName != "operations.sqlite" {
			t.Fatalf("xFullPathname evidence must expose a sanitized output token and nullable pre-open handle: %+v", event)
		}
	}
	mainOpen := eventSubset(eventsMain, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "MAIN_DB" })
	if len(mainOpen) != 1 || mainOpen[0].RC != 0 || mainOpen[0].HandleID == nil || *mainOpen[0].HandleID <= 0 {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("main xOpen trace invalid: %+v", mainOpen)
	}
	if err := pilotTraceClose(connA, "conn-A"); err != nil {
		t.Fatalf("close main-open connection: %v", err)
	}
	eventsMain = pilotTraceSnapshot(t)
	assertTraceCorrelation(t, eventsMain, runID, "VFSFLAGS-MAIN-OPEN", sourceID)
	if len(eventSubset(eventsMain, func(e pilotTraceEvent) bool { return e.Callback == "xClose" && e.Role == "MAIN_DB" })) == 0 {
		t.Fatal("main-open scenario trace omitted its database xClose")
	}
	rawTraces["VFSFLAGS-MAIN-OPEN"] = pilotTraceDump()
	scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-MAIN-OPEN", Status: "CANDIDATE", Events: eventsMain})

	// Generate a real WAL sidecar and transaction through the pinned SQLite build.
	pilotTraceReset()
	pilotTraceSetContext(runID, "VFSFLAGS-WAL-CREATE", "VFSFLAGS-WAL-CREATE", sourceID)
	connA, err = pilotTraceOpen(ctx, uri, "conn-A")
	if err != nil {
		t.Fatalf("reopen for WAL scenario: %v", err)
	}
	if err := pilotTraceExec(ctx, connA, "conn-A", "PRAGMA journal_mode=WAL"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("enable WAL: %v", err)
	}
	if err := pilotTraceExec(ctx, connA, "conn-A", "CREATE TABLE t(v INTEGER)"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("create table: %v", err)
	}
	if err := pilotTraceExec(ctx, connA, "conn-A", "BEGIN; INSERT INTO t VALUES(1); COMMIT"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("WAL transaction: %v", err)
	}
	walWitness, err := pilotTraceOpen(ctx, uri, "conn-wal-witness")
	if err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("open WAL witness: %v", err)
	}
	if err := pilotTraceClose(walWitness, "conn-wal-witness"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("close WAL witness: %v", err)
	}
	if err := pilotTraceClose(connA, "conn-A"); err != nil {
		t.Fatalf("close WAL creator: %v", err)
	}
	eventsWAL := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, eventsWAL, runID, "VFSFLAGS-WAL-CREATE", sourceID)
	if len(eventSubset(eventsWAL, func(e pilotTraceEvent) bool { return e.Callback == "xClose" && e.Role == "MAIN_DB" })) < 2 {
		t.Fatal("WAL scenario trace omitted the creator or witness database xClose")
	}
	rawTraces["VFSFLAGS-WAL-CREATE"] = pilotTraceDump()
	walOpen := eventSubset(eventsWAL, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "WAL" })
	if len(walOpen) == 0 {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatal("SQLite did not open WAL sidecar; VFSFLAGS-WAL-CREATE is NOT RUN")
	}
	scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-WAL-CREATE", Status: "CANDIDATE", Events: eventsWAL})

	// A second real connection exercises an existing WAL sidecar request when SQLite asks for it.
	pilotTraceReset()
	pilotTraceSetContext(runID, "VFSFLAGS-SIDECAR-EXISTING", "VFSFLAGS-SIDECAR-EXISTING", sourceID)
	connA, err = pilotTraceOpen(ctx, uri, "conn-A")
	if err != nil {
		t.Fatalf("reopen WAL owner for existing-sidecar scenario: %v", err)
	}
	if err := pilotTraceExec(ctx, connA, "conn-A", "PRAGMA journal_mode=WAL; INSERT INTO t VALUES(2)"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("prepare existing WAL sidecar: %v", err)
	}
	connB, err := pilotTraceOpen(ctx, uri, "conn-B")
	if err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("open second connection: %v", err)
	}
	if err := pilotTraceExec(ctx, connB, "conn-B", "SELECT v FROM t"); err != nil {
		_ = pilotTraceClose(connB, "conn-B")
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("second connection read: %v", err)
	}
	eventsExisting := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, eventsExisting, runID, "VFSFLAGS-SIDECAR-EXISTING", sourceID)
	if err := pilotTraceClose(connB, "conn-B"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("close existing-sidecar witness: %v", err)
	}
	if err := pilotTraceClose(connA, "conn-A"); err != nil {
		t.Fatalf("close existing-sidecar owner: %v", err)
	}
	eventsExisting = pilotTraceSnapshot(t)
	assertTraceCorrelation(t, eventsExisting, runID, "VFSFLAGS-SIDECAR-EXISTING", sourceID)
	if len(eventSubset(eventsExisting, func(e pilotTraceEvent) bool { return e.Callback == "xClose" && e.Role == "MAIN_DB" })) < 2 {
		t.Fatal("existing-sidecar scenario trace omitted owner or reader database xClose")
	}
	rawTraces["VFSFLAGS-SIDECAR-EXISTING"] = pilotTraceDump()
	if len(pilotTraceExistingWALOpen(eventsExisting, "conn-B")) > 0 {
		scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-SIDECAR-EXISTING", Status: "CANDIDATE", Events: eventsExisting})
	} else {
		scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-SIDECAR-EXISTING", Status: "NOT RUN", Reason: "second connection did not request xOpen for existing WAL sidecar"})
	}
	connA, err = pilotTraceOpen(ctx, uri, "conn-A")
	if err != nil {
		t.Fatalf("reopen for rollback-journal scenario: %v", err)
	}
	if err := pilotTraceExec(ctx, connA, "conn-A", "PRAGMA journal_mode=DELETE"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("switch to DELETE journal: %v", err)
	}

	// A committed rollback-journal transaction records the actual sidecar callback order.
	pilotTraceReset()
	pilotTraceSetContext(runID, "VFSFLAGS-JOURNAL-CREATE", "VFSFLAGS-JOURNAL-CREATE", sourceID)
	if err := pilotTraceExec(ctx, connA, "conn-A", "BEGIN; UPDATE t SET v=2; COMMIT"); err != nil {
		_ = pilotTraceClose(connA, "conn-A")
		t.Fatalf("rollback-journal transaction: %v", err)
	}
	if err := pilotTraceClose(connA, "conn-A"); err != nil {
		t.Fatalf("close rollback-journal connection: %v", err)
	}
	eventsJournal := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, eventsJournal, runID, "VFSFLAGS-JOURNAL-CREATE", sourceID)
	if len(eventSubset(eventsJournal, func(e pilotTraceEvent) bool { return e.Callback == "xClose" && e.Role == "MAIN_DB" })) == 0 {
		t.Fatal("rollback-journal scenario trace omitted its database xClose")
	}
	rawTraces["VFSFLAGS-JOURNAL-CREATE"] = pilotTraceDump()
	if len(eventSubset(eventsJournal, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "MAIN_JOURNAL" })) > 0 {
		scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-JOURNAL-CREATE", Status: "CANDIDATE", Events: eventsJournal})
	} else {
		scenarios[0] = append(scenarios[0], pilotTraceScenario{ID: "VFSFLAGS-JOURNAL-CREATE", Status: "NOT RUN", Reason: "transaction produced no main-journal xOpen"})
	}
	eexistScenario, eexistTrace, eexistObserved := pilotTraceSidecarEEXIST(t, root, runID, sourceID, artifactDir)
	if eexistObserved {
		scenarios[0] = append(scenarios[0], eexistScenario)
		if eexistTrace != "" {
			rawTraces[eexistScenario.ID] = eexistTrace
		}
	}

	for _, id := range []string{"LOCK-LOCAL-CONTEND", "LOCK-CROSS-PROCESS-CONTEND", "LOCK-PROMOTION-BUSY", "LOCK-DOWNGRADE"} {
		scenarios[1] = append(scenarios[1], pilotTraceScenario{ID: id, Status: "NOT RUN", Reason: "scenario protocol generator is not implemented; no lock outcome inferred"})
	}
	for _, id := range []string{"SHM-LOCAL-LOCK-UNLOCK", "SHM-CROSS-PROCESS-CONTEND", "SHM-FINAL-UNMAP-REOPEN"} {
		scenarios[2] = append(scenarios[2], pilotTraceScenario{ID: id, Status: "NOT RUN", Reason: "scenario protocol generator is not implemented; no SHM outcome inferred"})
	}
	scenarios[3] = pilotTraceInitialPagerScenarios()

	// These same-process cases produce reviewable callback candidates. They do
	// not claim cross-process syscall or private-VFS acceptance.
	if scenario, trace, ok := pilotTraceSHMLocal(t, root, runID, sourceID); ok {
		scenarios[2][0] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTraceLockLocal(t, root, runID, sourceID); ok {
		scenarios[1][0] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTraceLockCrossProcess(t, root, runID, sourceID, artifactDir); ok {
		scenarios[1][1] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTraceSHMCrossProcess(t, root, runID, sourceID, artifactDir); ok {
		scenarios[2][1] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTraceLockDowngrade(t, root, runID, sourceID); ok {
		scenarios[1][3] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTraceLockPromotionBusy(t, root, runID, sourceID); ok {
		scenarios[1][2] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTraceSHMFinalUnmapReopen(t, root, runID, sourceID, artifactDir); ok {
		scenarios[2][2] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTracePagerEmpty(t, root, runID, sourceID); ok {
		scenarios[3][0] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTracePagerCommit(t, root, runID, sourceID); ok {
		scenarios[3][2] = scenario
		rawTraces[scenario.ID] = trace
	}
	if scenario, trace, ok := pilotTracePagerHotRollback(t, root, runID, sourceID, artifactDir); ok {
		scenarios[3][1] = scenario
		rawTraces[scenario.ID] = trace
	}

	for scenarioID, trace := range rawTraces {
		name := strings.ToLower(scenarioID) + ".raw.jsonl"
		if err := pilotTraceWriteNewArtifact(filepath.Join(artifactDir, name), []byte(trace)); err != nil {
			t.Fatal(err)
		}
		rawTraceFiles[scenarioID] = name
		sum := sha256.Sum256([]byte(trace))
		rawTraceHashes[scenarioID] = hex.EncodeToString(sum[:])
	}
	for groupIndex := range scenarios {
		for scenarioIndex := range scenarios[groupIndex] {
			scenario := &scenarios[groupIndex][scenarioIndex]
			scenario.Reason = pilotTraceSanitizeReason(scenario.Reason)
			scenario.RawTraceFile = rawTraceFiles[scenario.ID]
			scenario.RawTraceSHA256 = rawTraceHashes[scenario.ID]
			scenario.OracleDetails = pilotTraceOracleDetails(*scenario)
			if scenario.DurableTraceSeq != 0 {
				scenario.OracleDetails["durable_trace"] = map[string]any{
					"sequence": scenario.DurableTraceSeq, "artifact_ref": scenario.RawTraceFile, "sha256": scenario.RawTraceSHA256,
					"persisted_before_main_write_ack": true,
				}
			}
			scenario.OracleDetailsSHA256, err = pilotTraceJSONSHA256(scenario.OracleDetails)
			if err != nil {
				t.Fatalf("hash scenario oracle details for %s: %v", scenario.ID, err)
			}
			scenario.Oracle = pilotTraceCompleteOracle(*scenario)
		}
	}
	harnessHashes, harnessHash, err := pilotTraceHarnessHashes()
	if err != nil {
		t.Fatal(err)
	}
	groupNames := []string{"sqlite3451-vfs-flags", "sqlite3451-unix-lock-trace", "sqlite3451-shm-lock-trace", "sqlite3451-pager-delete-trace"}
	for i, groupName := range groupNames {
		status := "NOT RUN"
		if pilotTraceOverflowed() {
			t.Fatalf("trace buffer overflowed before candidate assignment for %s; candidate invalidated", groupName)
		}
		for _, scenario := range scenarios[i] {
			if err := pilotTraceValidateScenario(scenario, artifactDir); err != nil {
				t.Fatalf("scenario evidence schema invalid for %s: %v", scenario.ID, err)
			}
			if scenario.Status == "CANDIDATE" {
				status = "CANDIDATE"
				break
			}
		}
		candidate := pilotTraceCandidate{
			FixtureGroup:    groupName,
			Status:          status,
			TraceOverflowed: false,
			RunID:           runID,
			SQLite:          map[string]string{"version": version, "version_number": fmt.Sprint(number), "source_id": sourceID, "vfs_name": pilotTraceVFSName, "parent_vfs": pilotTraceParentName(), "scope": "host VFS behavior characterization only"},
			HarnessSHA:      harnessHash,
			HarnessFiles:    harnessHashes,
			Execution:       execution,
			Scenarios:       scenarios[i],
		}
		data, err := json.MarshalIndent(candidate, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(artifactDir, groupName+".candidate.json")
		if err := pilotTraceWriteNewArtifact(path, append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden_candidate fixture_group=%s status=%s artifact=%s", groupName, status, filepath.Base(path))
	}
	t.Logf("raw_trace_files=%d harness_sha256=%s status=CANDIDATE_ONLY", len(rawTraceFiles), harnessHash)
}

func pilotTraceInitialPagerScenarios() []pilotTraceScenario {
	ids := []string{"PAGER-EMPTY-OBSOLETE-RESERVED", "PAGER-HOT-ROLLBACK-EXCLUSIVE", "PAGER-COMMIT-EXCLUSIVE"}
	scenarios := make([]pilotTraceScenario, 0, len(ids))
	for _, id := range ids {
		scenarios = append(scenarios, pilotTraceScenario{ID: id, Status: "NOT RUN", Reason: "pager scenario generator has not produced a candidate"})
	}
	return scenarios
}

func TestPilotPagerScenarioSlotOrder(t *testing.T) {
	got := pilotTraceInitialPagerScenarios()
	want := []string{"PAGER-EMPTY-OBSOLETE-RESERVED", "PAGER-HOT-ROLLBACK-EXCLUSIVE", "PAGER-COMMIT-EXCLUSIVE"}
	if len(got) != len(want) {
		t.Fatalf("pager scenario slots=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("pager scenario slot %d=%q want %q", i, got[i].ID, want[i])
		}
	}
}

func TestPilotVFSRuntimeEvidenceSchemaGuards(t *testing.T) {
	t.Run("artifact writer never overwrites evidence", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "immutable.json")
		original := []byte("reviewed evidence")
		if err := pilotTraceWriteNewArtifact(path, original); err != nil {
			t.Fatalf("write initial artifact: %v", err)
		}
		if err := pilotTraceWriteNewArtifact(path, []byte("replacement")); err == nil {
			t.Fatal("second artifact writer replaced an existing file")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("existing evidence changed: got %q, want %q", got, original)
		}
	})
	t.Run("oracle event count and expected outcome are enforced", func(t *testing.T) {
		scenario := pilotTraceScenario{ID: "VFSFLAGS-MAIN-OPEN", Status: "NOT RUN", Reason: "open precondition missing"}
		scenario.Oracle = pilotTraceCompleteOracle(scenario)
		scenario.OracleDetailsSHA256, _ = pilotTraceJSONSHA256(scenario.Oracle["details"])
		if err := pilotTraceValidateScenario(scenario, t.TempDir()); err != nil {
			t.Fatalf("valid NOT RUN row rejected: %v", err)
		}
		scenario.Oracle["event_count"] = 1
		if err := pilotTraceValidateScenario(scenario, t.TempDir()); err == nil {
			t.Fatal("mismatched event_count was accepted")
		}
		scenario.Oracle = pilotTraceCompleteOracle(pilotTraceScenario{ID: "VFSFLAGS-MAIN-OPEN", Status: "NOT RUN", Reason: "open precondition missing"})
		scenario.Oracle["expected_outcome"] = "invented expectation"
		if err := pilotTraceValidateScenario(scenario, t.TempDir()); err == nil {
			t.Fatal("expectation drift was accepted")
		}
		scenario.Oracle = pilotTraceCompleteOracle(pilotTraceScenario{ID: "VFSFLAGS-MAIN-OPEN", Status: "NOT RUN", Reason: "open precondition missing"})
		scenario.OracleDetailsSHA256, _ = pilotTraceJSONSHA256(scenario.Oracle["details"])
		scenario.Oracle["observed_outcome"] = "fabricated observation"
		if err := pilotTraceValidateScenario(scenario, t.TempDir()); err == nil {
			t.Fatal("fabricated observed outcome was accepted")
		}
		scenario.Oracle["observed_outcome"] = "NOT_OBSERVED"
		scenario.Oracle["details"].(map[string]any)["fabricated"] = true
		if err := pilotTraceValidateScenario(scenario, t.TempDir()); err == nil {
			t.Fatal("mutated oracle details were accepted")
		}
	})

	t.Run("not-started child terminal is JSON null", func(t *testing.T) {
		child := pilotTraceNotStartedChild("go test binary -test.run=<child>", "child")
		data, err := json.Marshal(child)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if value, exists := record["terminal_outcome"]; !exists || value != nil {
			t.Fatalf("terminal_outcome=%v exists=%t, want JSON null", value, exists)
		}
	})

	t.Run("candidate measured outcome is bound to its events and details", func(t *testing.T) {
		artifactDir := t.TempDir()
		raw := []byte("{}\n")
		if err := pilotTraceWriteNewArtifact(filepath.Join(artifactDir, "candidate.raw.jsonl"), raw); err != nil {
			t.Fatal(err)
		}
		scenario := pilotTraceScenario{
			ID: "VFSFLAGS-MAIN-OPEN", Status: "CANDIDATE",
			Events: []pilotTraceEvent{{Callback: "xOpen", Actor: "conn-A", Role: "MAIN_DB", RC: 0}},
			Oracle: map[string]any{"sqlite_rc": "SQLITE_OK"},
		}
		scenario.OracleDetails = pilotTraceOracleDetails(scenario)
		scenario.OracleDetailsSHA256, _ = pilotTraceJSONSHA256(scenario.OracleDetails)
		scenario.Oracle = pilotTraceCompleteOracle(scenario)
		scenario.RawTraceFile = "candidate.raw.jsonl"
		sum := sha256.Sum256(raw)
		scenario.RawTraceSHA256 = hex.EncodeToString(sum[:])
		if err := pilotTraceValidateScenario(scenario, artifactDir); err != nil {
			t.Fatalf("valid candidate rejected: %v", err)
		}
		scenario.Oracle["observed_outcome"] = "fabricated"
		if err := pilotTraceValidateScenario(scenario, artifactDir); err == nil {
			t.Fatal("fabricated candidate measurement was accepted")
		}
	})

	t.Run("paths containing spaces are removed from diagnostic text", func(t *testing.T) {
		for _, input := range []string{`open D:\evidence with spaces\child.json: denied`, `open /tmp/evidence with spaces/child.json: denied`} {
			if got := pilotTraceSanitizeReason(input); strings.Contains(got, "evidence with spaces") || strings.Contains(got, "child.json") {
				t.Fatalf("path leaked from diagnostic %q", got)
			}
		}
	})
}

func pilotTraceValidateScenario(s pilotTraceScenario, artifactDir string) error {
	if s.ID == "" || (s.Status != "CANDIDATE" && s.Status != "NOT RUN") {
		return fmt.Errorf("missing ID or unsupported status %q", s.Status)
	}
	for _, key := range []string{"expected_outcome", "observed_outcome", "execution_status", "reason"} {
		if _, ok := s.Oracle[key]; !ok {
			return fmt.Errorf("oracle missing required key %q", key)
		}
	}
	if s.Oracle["execution_status"] != s.Status || s.Oracle["expected_outcome"] == "" {
		return fmt.Errorf("oracle status/expectation does not match scenario")
	}
	if s.Oracle["expected_outcome"] != pilotTraceExpectedOutcome(s.ID) {
		return fmt.Errorf("oracle expectation does not match the scenario contract")
	}
	details, ok := s.Oracle["details"].(map[string]any)
	if !ok {
		return fmt.Errorf("oracle details must be an object")
	}
	if !reflect.DeepEqual(details, pilotTraceOracleDetails(s)) {
		return fmt.Errorf("oracle details do not match source scenario measurements")
	}
	detailsHash, err := pilotTraceJSONSHA256(details)
	if err != nil || s.OracleDetailsSHA256 == "" || detailsHash != s.OracleDetailsSHA256 {
		return fmt.Errorf("oracle details integrity hash mismatch")
	}
	eventCount, ok := s.Oracle["event_count"].(int)
	if !ok || eventCount != len(s.Events) {
		return fmt.Errorf("oracle event_count does not match normalized events")
	}
	if s.Status == "NOT RUN" && (s.Oracle["observed_outcome"] != "NOT_OBSERVED" || s.Oracle["reason"] == "" || strings.TrimSpace(s.Reason) == "") {
		return fmt.Errorf("NOT RUN oracle must have NOT_OBSERVED and a reason")
	}
	if s.Status == "CANDIDATE" && (s.Oracle["observed_outcome"] == "NOT_OBSERVED" || s.Oracle["reason"] != "") {
		return fmt.Errorf("candidate oracle must state an observation and have empty reason")
	}
	if s.Oracle["reason"] != s.Reason {
		return fmt.Errorf("oracle reason does not match scenario status record")
	}
	if s.Status == "CANDIDATE" {
		observed, ok := s.Oracle["observed_outcome"].(string)
		if !ok || observed == "" || observed == "TRACE_CAPTURED" || observed != pilotTraceMeasuredOutcome(s) {
			return fmt.Errorf("candidate oracle must contain a measured outcome")
		}
	}
	if s.Status == "CANDIDATE" && len(s.Events) == 0 {
		return fmt.Errorf("candidate status requires at least one captured callback event")
	}
	if s.Status == "CANDIDATE" && s.RawTraceFile == "" {
		return fmt.Errorf("candidate status requires a raw trace artifact reference")
	}
	if s.RawTraceFile != "" {
		if filepath.Base(s.RawTraceFile) != s.RawTraceFile || len(s.RawTraceSHA256) != 64 {
			return fmt.Errorf("invalid raw trace artifact reference/hash")
		}
		data, err := pilotTraceReadArtifact(filepath.Join(artifactDir, s.RawTraceFile))
		if err != nil {
			return fmt.Errorf("read raw trace artifact %s: %w", s.RawTraceFile, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != s.RawTraceSHA256 {
			return fmt.Errorf("raw trace artifact hash mismatch for %s", s.RawTraceFile)
		}
	} else if s.RawTraceSHA256 != "" {
		return fmt.Errorf("raw trace hash exists without artifact reference")
	}
	for _, child := range s.ChildExecutions {
		if child.CommandTemplate == "" || child.ProcessRole == "" {
			return fmt.Errorf("child execution missing command template or role")
		}
		if !child.Started {
			if child.ExecutionStatus != "NOT RUN" || child.CleanupStatus != "NOT_APPLICABLE" || child.ExitCode != nil || child.Stdout != nil || child.Stderr != nil || child.TerminalOutcome != nil || child.Reaped || child.TimedOut || child.Killed {
				return fmt.Errorf("not-started child record has observed execution fields")
			}
			continue
		}
		if child.ExitCode == nil || child.Stdout == nil || child.Stderr == nil || child.Stdout.File == "WRITE_FAILED" || child.Stderr.File == "WRITE_FAILED" {
			return fmt.Errorf("started child record lacks immutable output or exit evidence")
		}
		for _, artifact := range []*pilotTraceChildArtifact{child.Stdout, child.Stderr} {
			if artifact.Bytes < 0 || len(artifact.SHA256) != 64 || filepath.Base(artifact.File) != artifact.File {
				return fmt.Errorf("invalid child output artifact metadata")
			}
			data, err := pilotTraceReadArtifact(filepath.Join(artifactDir, artifact.File))
			if err != nil {
				return fmt.Errorf("read child output artifact %s: %w", artifact.File, err)
			}
			sum := sha256.Sum256(data)
			if len(data) != artifact.Bytes || hex.EncodeToString(sum[:]) != artifact.SHA256 {
				return fmt.Errorf("child output artifact hash/length mismatch for %s", artifact.File)
			}
		}
		if child.ParentControlFDClosed != nil && !*child.ParentControlFDClosed {
			return fmt.Errorf("parent control descriptor was not closed")
		}
		if !child.ChildControlFDClosed {
			return fmt.Errorf("child control descriptor was not closed")
		}
		if child.ExecutionStatus != "CANDIDATE" && child.ExecutionStatus != "NOT RUN" {
			return fmt.Errorf("unsupported child execution status %q", child.ExecutionStatus)
		}
		if child.CleanupStatus != "CLEAN" && child.CleanupStatus != "INCOMPLETE" {
			return fmt.Errorf("unsupported started-child cleanup status %q", child.CleanupStatus)
		}
		normalExit := child.ExitCode != nil && *child.ExitCode == 0 && !child.Killed && child.TerminationSignal == ""
		expectedCrashKill := child.ExitCode != nil && *child.ExitCode == -1 && child.Killed && child.TerminationSignal == "SIGKILL"
		if child.TerminalOutcome == nil || child.Reaped != (child.ExitCode != nil) || (child.ExecutionStatus == "CANDIDATE" && (!child.Reaped || (!normalExit && !expectedCrashKill) || *child.TerminalOutcome == "NOT_OBSERVED" || child.CleanupStatus != "CLEAN" || !child.TracePersisted || child.TimedOut || child.ParentControlFDClosed == nil || !*child.ParentControlFDClosed)) {
			return fmt.Errorf("child terminal/cleanup evidence is inconsistent")
		}
	}
	for _, artifact := range s.EvidenceArtifacts {
		if artifact.Bytes < 0 || len(artifact.SHA256) != 64 || filepath.Base(artifact.File) != artifact.File {
			return fmt.Errorf("invalid evidence artifact metadata")
		}
		data, err := pilotTraceReadArtifact(filepath.Join(artifactDir, artifact.File))
		if err != nil {
			return fmt.Errorf("read evidence artifact %s: %w", artifact.File, err)
		}
		sum := sha256.Sum256(data)
		if len(data) != artifact.Bytes || hex.EncodeToString(sum[:]) != artifact.SHA256 {
			return fmt.Errorf("evidence artifact hash/length mismatch for %s", artifact.File)
		}
	}
	return nil
}

func pilotTraceCompleteOracle(s pilotTraceScenario) map[string]any {
	details := pilotTraceOracleDetails(s)
	reason := ""
	observed := pilotTraceMeasuredOutcome(s)
	if s.Status == "NOT RUN" {
		reason = strings.TrimSpace(s.Reason)
		if reason == "" {
			reason = "scenario precondition was not met"
		}
		observed = "NOT_OBSERVED"
	}
	return map[string]any{
		"expected_outcome": pilotTraceExpectedOutcome(s.ID),
		"observed_outcome": observed,
		"execution_status": s.Status,
		"reason":           reason,
		"details":          details,
		"event_count":      len(s.Events),
	}
}

func pilotTraceOracleDetails(s pilotTraceScenario) map[string]any {
	source := s.OracleDetails
	if source == nil {
		if details, ok := s.Oracle["details"].(map[string]any); ok {
			source = details
		} else {
			source = s.Oracle
		}
	}
	details := make(map[string]any, len(source))
	for key, value := range source {
		details[key] = value
	}
	return details
}

func pilotTraceJSONSHA256(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func pilotTraceMeasuredOutcome(s pilotTraceScenario) string {
	if s.Status != "CANDIDATE" {
		return "NOT_OBSERVED"
	}
	callbackSet := make(map[string]struct{})
	xopens := make([]map[string]any, 0)
	for _, event := range s.Events {
		callbackSet[event.Callback] = struct{}{}
		if event.Callback == "xOpen" {
			xopens = append(xopens, map[string]any{"process_role": event.Process, "connection_id": event.ConnID, "role": event.Role, "rc": event.RC, "requested_flags": event.Arg0, "observed_flags": event.Arg1, "p_out_flags": event.Out})
		}
	}
	callbacks := make([]string, 0, len(callbackSet))
	for callback := range callbackSet {
		callbacks = append(callbacks, callback)
	}
	sort.Strings(callbacks)
	measured := map[string]any{"callback_event_count": len(s.Events), "callbacks": callbacks}
	if len(xopens) > 0 {
		measured["xopens"] = xopens
	}
	for key, value := range pilotTraceOracleDetails(s) {
		measured[key] = value
	}
	encoded, err := json.Marshal(measured)
	if err != nil {
		return fmt.Sprintf("callback_event_count=%d", len(s.Events))
	}
	return string(encoded)
}

func pilotTraceSanitizeReason(reason string) string {
	if pilotTraceAbsolutePathPattern.MatchString(reason) || pilotTraceAbsolutePathWithSpacesPattern.MatchString(reason) {
		return "scenario precondition failed; absolute path redacted"
	}
	return reason
}

func pilotTraceTerminal(value string) *string { return &value }

func pilotTraceExpectedOutcome(id string) string {
	expectations := map[string]string{
		"VFSFLAGS-MAIN-OPEN":             "one successful MAIN_DB xOpen with requested and output flags plus matching close",
		"VFSFLAGS-WAL-CREATE":            "successful main and WAL opens with WAL transaction callbacks and close ordering",
		"VFSFLAGS-SIDECAR-EXISTING":      "connection B successfully opens the existing WAL sidecar",
		"VFSFLAGS-JOURNAL-CREATE":        "a successful main-journal xOpen and observed sync/close/delete ordering",
		"VFSFLAGS-SIDECAR-EEXIST-REOPEN": "observe one injected exclusive-create EEXIST followed by SQLite retry and success",
		"LOCK-LOCAL-CONTEND":             "B acquires RESERVED and A competing UPDATE returns SQLITE_BUSY",
		"LOCK-CROSS-PROCESS-CONTEND":     "child B acquires RESERVED, parent A UPDATE returns SQLITE_BUSY, then child releases",
		"LOCK-PROMOTION-BUSY":            "A promotes to RESERVED, B promotion returns SQLITE_BUSY while B snapshot remains usable",
		"LOCK-DOWNGRADE":                 "record RESERVED/EXCLUSIVE/downgrade/unlock order through COMMIT",
		"SHM-LOCAL-LOCK-UNLOCK":          "record local xShmMap/Lock/Barrier/Unmap sequence and SQL results",
		"SHM-CROSS-PROCESS-CONTEND":      "child writer commits after START_WRITE while parent read snapshot remains stable",
		"SHM-FINAL-UNMAP-REOPEN":         "record final unmap before the subsequent reopen/map sequence",
		"PAGER-EMPTY-OBSOLETE-RESERVED":  "record schema query outcome and observed obsolete journal callbacks",
		"PAGER-HOT-ROLLBACK-EXCLUSIVE":   "kill after journal sync and main-page write; recovery returns old committed value",
		"PAGER-COMMIT-EXCLUSIVE":         "successful commit returns new value and records journal close/delete order",
	}
	if expected, ok := expectations[id]; ok {
		return expected
	}
	return "capture the documented scenario outcome"
}

func pilotTraceChildRecord(artifactDir, scenarioID, role, command string, cmd *exec.Cmd, stdout, stderr *bytes.Buffer, tracePersisted, timedOut, killed, reaped bool, terminal, errSummary string) pilotTraceChildExecution {
	started := cmd != nil && cmd.Process != nil
	status := "CANDIDATE"
	cleanup := "CLEAN"
	if terminal == "NOT_OBSERVED" || !reaped || !tracePersisted {
		status = "NOT RUN"
	}
	if !reaped {
		cleanup = "INCOMPLETE"
	}
	if !started {
		status, cleanup = "NOT RUN", "NOT_APPLICABLE"
	}
	pilotTraceLogChildCleanup(scenarioID, role, started, reaped, cleanup, timedOut)
	stdoutArtifact := pilotTracePersistedOutput(artifactDir, scenarioID, role, "stdout", stdout)
	stderrArtifact := pilotTracePersistedOutput(artifactDir, scenarioID, role, "stderr", stderr)
	if !started {
		stdoutArtifact, stderrArtifact = nil, nil
	}
	var exitCode *int
	terminationSignal := ""
	if started && reaped {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
			if waitStatus, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
				if waitStatus.Signal() == syscall.SIGKILL {
					terminationSignal = "SIGKILL"
				} else {
					terminationSignal = waitStatus.Signal().String()
				}
			}
		}
		exitCode = &code
	}
	var terminalOutcome *string
	if started {
		terminalOutcome = pilotTraceTerminal(terminal)
	}
	return pilotTraceChildExecution{
		CommandTemplate: command, ProcessRole: role, Started: started, ExecutionStatus: status, ExitCode: exitCode, TerminationSignal: terminationSignal,
		Stdout: stdoutArtifact, Stderr: stderrArtifact, ErrorSummary: errSummary,
		TerminalOutcome: terminalOutcome, CleanupStatus: cleanup, ChildControlFDClosed: !started || reaped,
		TracePersisted: tracePersisted, TimedOut: timedOut, Killed: killed, Reaped: reaped,
	}
}

func pilotTraceLogChildCleanup(scenarioID, role string, started, reaped bool, cleanup string, timedOut bool) {
	fmt.Printf("PILOT_CHILD_CLEANUP scenario=%s role=%s started=%t reaped=%t cleanup=%s timed_out=%t\n", scenarioID, role, started, reaped, cleanup, timedOut)
}

func pilotTracePersistedOutput(artifactDir, scenarioID, role, stream string, output *bytes.Buffer) *pilotTraceChildArtifact {
	data := []byte(pilotTraceSanitizeReason(string(output.Bytes())))
	sum := sha256.Sum256(data)
	name := strings.ToLower(strings.ReplaceAll(scenarioID, "_", "-")) + "-" + role + "-" + stream + ".log"
	if err := pilotTraceWriteNewArtifact(filepath.Join(artifactDir, name), data); err != nil {
		return &pilotTraceChildArtifact{File: "WRITE_FAILED", SHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}
	}
	return &pilotTraceChildArtifact{File: name, SHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}
}

func pilotTraceWriteNewArtifact(path string, data []byte) error {
	file, err := pilotTraceOpenNewArtifact(path)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// pilotTraceReplaceArtifact updates a child snapshot only after the caller's
// control protocol has serialized readers and writers. It validates an
// existing target before truncating it and never follows links or blocks on a
// FIFO.
func pilotTraceReplaceArtifact(path string, data []byte) error {
	return pilotTraceReplaceArtifactWithFault(path, data, "first")
}

func pilotTraceReplaceArtifactWithFault(path string, data []byte, phase string) error {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || filepath.Dir(path) == path {
		return errors.New("invalid artifact basename")
	}
	dirFD, _, err := pilotTraceOpenArtifactDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	failAt := os.Getenv("PILOT_VFS_TEST_PUBLISH_FAULT")
	fault := func(stage string) error {
		if failAt == phase+"-"+stage {
			return fmt.Errorf("injected %s publication %s failure", phase, stage)
		}
		return nil
	}
	openStage := "create"
	if phase == "repeat" {
		openStage = "reopen"
	}
	if err := fault(openStage); err != nil {
		return err
	}
	var fileFD int
	if phase == "first" {
		fileFD, err = unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	} else {
		fileFD, err = unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fileFD), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		_ = file.Close()
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		_ = file.Close()
		return errors.New("artifact target is not a singly-linked regular file owned by effective user")
	}
	if err := fault("truncate"); err != nil && phase == "repeat" {
		_ = file.Close()
		return err
	}
	if err := unix.Ftruncate(fileFD, 0); err != nil {
		_ = file.Close()
		return err
	}
	if err := fault("write"); err != nil {
		_ = file.Close()
		return err
	}
	if err := fault("short-write"); err != nil {
		partial := len(data) / 2
		if partial > 0 {
			if _, writeErr := unix.Write(fileFD, data[:partial]); writeErr != nil {
				_ = file.Close()
				return writeErr
			}
		}
		_ = file.Close()
		return errors.Join(err, io.ErrShortWrite)
	}
	for len(data) > 0 {
		n, writeErr := unix.Write(fileFD, data)
		if writeErr == unix.EINTR {
			continue
		}
		if writeErr != nil {
			_ = file.Close()
			return writeErr
		}
		if n == 0 {
			_ = file.Close()
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	if err := fault("file-sync"); err != nil {
		_ = file.Close()
		return err
	}
	if err := unix.Fdatasync(fileFD); err != nil {
		_ = file.Close()
		return err
	}
	if err := fault("directory-sync"); err != nil {
		_ = file.Close()
		return err
	}
	if err := unix.Fsync(dirFD); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func TestPilotTraceReplaceArtifactPublishesRepeatedSnapshotSafely(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "child.raw.jsonl")
	first := []byte("first snapshot\n")
	second := []byte("second complete snapshot\n")
	if err := pilotTraceReplaceArtifact(path, first); err != nil {
		t.Fatalf("publish first snapshot: %v", err)
	}
	if err := pilotTraceReplaceArtifactWithFault(path, second, "repeat"); err != nil {
		t.Fatalf("publish replacement snapshot: %v", err)
	}
	got, err := pilotTraceReadArtifact(path)
	if err != nil {
		t.Fatalf("read published snapshot: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("snapshot=%q, want %q", got, second)
	}
}

func TestPilotTraceReplaceArtifactEnforcesPublicationSequence(t *testing.T) {
	t.Run("first publication refuses existing basename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "child.raw.jsonl")
		original := []byte("existing evidence\n")
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := pilotTraceReplaceArtifactWithFault(path, []byte("must not truncate\n"), "first"); err == nil {
			t.Fatal("first publication accepted an existing basename")
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("existing evidence changed: got=%q err=%v", got, err)
		}
	})
	t.Run("repeat publication refuses missing basename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "child.raw.jsonl")
		if err := pilotTraceReplaceArtifactWithFault(path, []byte("must not create\n"), "repeat"); err == nil {
			t.Fatal("repeat publication created a missing basename")
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing evidence was created: lstat err=%v", err)
		}
	})
}

func TestPilotTraceReplaceArtifactRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "child.raw.jsonl")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := pilotTraceReplaceArtifactWithFault(symlink, []byte("must not replace"), "repeat"); err == nil {
		t.Fatal("symlink target unexpectedly accepted")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve me" {
		t.Fatalf("symlink target changed: contents=%q err=%v", contents, err)
	}
	if err := os.Remove(symlink); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "directory.raw.jsonl")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := pilotTraceReplaceArtifactWithFault(directory, []byte("must not replace"), "repeat"); err == nil {
		t.Fatal("directory target unexpectedly accepted")
	}
	fifo := filepath.Join(root, "child.raw.jsonl")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- pilotTraceReplaceArtifactWithFault(fifo, []byte("must not block"), "repeat") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO target unexpectedly accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO rejection blocked")
	}
}

func TestPilotTraceReplaceArtifactPublicationFaultsFailClosed(t *testing.T) {
	tests := []struct {
		phase string
		stage string
	}{
		{"first", "create"}, {"first", "write"}, {"first", "file-sync"}, {"first", "directory-sync"},
		{"repeat", "reopen"}, {"repeat", "truncate"}, {"repeat", "write"}, {"repeat", "short-write"}, {"repeat", "file-sync"}, {"repeat", "directory-sync"},
	}
	for _, tc := range tests {
		t.Run(tc.phase+"-"+tc.stage, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "child.raw.jsonl")
			if tc.phase == "repeat" {
				if err := pilotTraceReplaceArtifact(path, []byte("first durable snapshot\n")); err != nil {
					t.Fatalf("publish baseline: %v", err)
				}
			}
			t.Setenv("PILOT_VFS_TEST_PUBLISH_FAULT", tc.phase+"-"+tc.stage)
			err := pilotTraceReplaceArtifactWithFault(path, []byte("faulted snapshot\n"), tc.phase)
			if err == nil {
				t.Fatal("injected snapshot publication fault unexpectedly succeeded")
			}
		})
	}
}

func pilotTraceOpenNewArtifact(path string) (*os.File, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || filepath.Dir(path) == path {
		return nil, errors.New("invalid artifact basename")
	}
	dirFD, _, err := pilotTraceOpenArtifactDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	fileFD, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fileFD), name), nil
}

func pilotTraceOpenExistingArtifact(path string) (*os.File, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || filepath.Dir(path) == path {
		return nil, errors.New("invalid artifact basename")
	}
	dirFD, _, err := pilotTraceOpenArtifactDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	fileFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		_ = unix.Close(fileFD)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fileFD)
		return nil, errors.New("artifact is not a regular file")
	}
	return os.NewFile(uintptr(fileFD), name), nil
}

func pilotTraceReadArtifact(path string) ([]byte, error) {
	file, err := pilotTraceOpenExistingArtifact(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func pilotTraceNotStartedChild(command, role string) pilotTraceChildExecution {
	return pilotTraceChildExecution{CommandTemplate: command, ProcessRole: role, ExecutionStatus: "NOT RUN", TerminalOutcome: nil, CleanupStatus: "NOT_APPLICABLE"}
}

// TestPilotVFSChildLockContention is a child-process endpoint used only by the
// candidate generator. Its control protocol is bounded SOCK_SEQPACKET: READY,
// RELEASE, then RELEASED. It emits its callback trace to a private file so
// the parent can bind both process-local sequences into one candidate artifact.
func TestPilotVFSChildLockContention(t *testing.T) {
	if os.Getenv("PILOT_VFS_LOCK_CHILD") != "1" {
		return
	}
	fd, err := strconv.Atoi(os.Getenv("PILOT_VFS_LOCK_CONTROL_FD"))
	if err != nil || fd < 3 {
		t.Fatalf("invalid child control descriptor: %v", err)
	}
	tracePath := os.Getenv("PILOT_VFS_LOCK_TRACE_PATH")
	if !filepath.IsAbs(tracePath) {
		t.Fatal("child trace path must be absolute")
	}
	runID := os.Getenv("PILOT_VFS_LOCK_RUN_ID")
	scenarioID := "LOCK-CROSS-PROCESS-CONTEND"
	sourceID := os.Getenv("PILOT_VFS_LOCK_SOURCE_ID")
	control := os.NewFile(uintptr(fd), "pilot-lock-control")
	if control == nil {
		t.Fatal("child control descriptor unavailable")
	}
	defer control.Close()
	controlFD := int(control.Fd())
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatalf("child register trace VFS: %v", err)
	}
	defer func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("child unregister trace VFS: %v", err)
		}
	}()
	if pilotTraceParentName() != "unix" {
		t.Fatalf("child parent VFS=%q, want unix", pilotTraceParentName())
	}
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	uri := os.Getenv("PILOT_VFS_LOCK_DB_URI")
	if uri == "" {
		t.Fatal("missing child database URI")
	}
	ctx := context.Background()
	conn, err := pilotTraceOpen(ctx, uri, "child-B")
	if err != nil {
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("child open: %v", err)
	}
	if err := pilotTraceExec(ctx, conn, "child-B", "BEGIN IMMEDIATE"); err != nil {
		_ = pilotTraceClose(conn, "child-B")
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("child BEGIN IMMEDIATE: %v", err)
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		_ = pilotTraceExec(ctx, conn, "child-B", "ROLLBACK")
		_ = pilotTraceClose(conn, "child-B")
		t.Fatalf("persist child RESERVED trace before READY: %v", err)
	}
	pilotTraceTestCloseControlPeer("peer-eof-lock")
	if os.Getenv("PILOT_VFS_TEST_CONTROL_FAULT") == "timeout-lock" {
		time.Sleep(10 * time.Second)
	}
	if err := pilotTraceSendControl(controlFD, 1, 1); err != nil {
		_ = pilotTraceExec(ctx, conn, "child-B", "ROLLBACK")
		_ = pilotTraceClose(conn, "child-B")
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("child READY send: %v", err)
	}
	messageSeq, message, err := pilotTraceRecvControl(controlFD, 5000)
	if err != nil || messageSeq != 2 || message != 2 {
		_ = pilotTraceExec(ctx, conn, "child-B", "ROLLBACK")
		_ = pilotTraceClose(conn, "child-B")
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("child RELEASE receive: seq=%d message=%d err=%v", messageSeq, message, err)
	}
	if err := pilotTraceExec(ctx, conn, "child-B", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(conn, "child-B")
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("child rollback: %v", err)
	}
	if err := pilotTraceClose(conn, "child-B"); err != nil {
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("child close: %v", err)
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		t.Fatalf("persist final child trace: %v", err)
	}
	if err := pilotTraceSendControl(controlFD, 3, 3); err != nil {
		t.Fatalf("child RELEASED send: %v", err)
	}
}

func TestPilotVFSChildSHMContention(t *testing.T) {
	if os.Getenv("PILOT_VFS_SHM_CHILD") != "1" {
		return
	}
	fd, err := strconv.Atoi(os.Getenv("PILOT_VFS_LOCK_CONTROL_FD"))
	if err != nil || fd < 3 {
		t.Fatalf("invalid SHM child control descriptor: %v", err)
	}
	tracePath := os.Getenv("PILOT_VFS_LOCK_TRACE_PATH")
	if !filepath.IsAbs(tracePath) {
		t.Fatal("SHM child trace path must be absolute")
	}
	runID := os.Getenv("PILOT_VFS_LOCK_RUN_ID")
	const scenarioID = "SHM-CROSS-PROCESS-CONTEND"
	sourceID := os.Getenv("PILOT_VFS_LOCK_SOURCE_ID")
	control := os.NewFile(uintptr(fd), "pilot-shm-control")
	if control == nil {
		t.Fatal("SHM child control descriptor unavailable")
	}
	defer control.Close()
	controlFD := int(control.Fd())
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatalf("SHM child register trace VFS: %v", err)
	}
	defer func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("SHM child unregister trace VFS: %v", err)
		}
	}()
	if pilotTraceParentName() != "unix" {
		t.Fatalf("SHM child parent VFS=%q, want unix", pilotTraceParentName())
	}
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	uri := os.Getenv("PILOT_VFS_LOCK_DB_URI")
	if uri == "" {
		t.Fatal("missing SHM child database URI")
	}
	ctx := context.Background()
	conn, err := pilotTraceOpen(ctx, uri, "child-B")
	if err != nil {
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("SHM child open: %v", err)
	}
	sendSQLError := func(sequence uint64, phase string, cause error) {
		cleanupErrors := pilotTraceCleanupSQLError(phase,
			func() error { return pilotTraceExec(ctx, conn, "child-B", "ROLLBACK") },
			func() error { return pilotTraceClose(conn, "child-B") },
		)
		for _, operation := range cleanupErrors {
			t.Errorf("SHM child cleanup failed: operation=%s", operation)
		}
		if writeErr := pilotTraceWriteChildTrace(tracePath); writeErr != nil {
			t.Errorf("persist SHM child error trace: %v", writeErr)
			return
		}
		payload, encodeErr := pilotTraceEncodeSQLErrorWithCleanup(phase, cause, cleanupErrors)
		if encodeErr != nil {
			t.Errorf("encode SHM child SQLite error: %v", encodeErr)
			return
		}
		if sendErr := pilotTraceSendControlPayload(controlFD, sequence, 4, payload); sendErr != nil {
			t.Errorf("send structured SHM child SQLite error: %v", sendErr)
		}
	}
	if err := pilotTraceExec(ctx, conn, "child-B", "BEGIN IMMEDIATE"); err != nil {
		sendSQLError(1, "begin_immediate", err)
		return
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		_ = pilotTraceClose(conn, "child-B")
		t.Fatalf("persist SHM child trace before READY: %v", err)
	}
	if os.Getenv("PILOT_VFS_TEST_CONTROL_FAULT") == "bad-error-envelope" {
		payload := []byte(`{"version":2,"kind":"sqlite_error","phase":"commit"}`)
		if err := pilotTraceSendControlPayload(controlFD, 1, 4, payload); err != nil {
			t.Fatalf("send malformed structured error test frame: %v", err)
		}
		return
	}
	if err := pilotTraceSendControl(controlFD, 1, 1); err != nil {
		_ = pilotTraceClose(conn, "child-B")
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("SHM child READY send: %v", err)
	}
	seq, message, err := pilotTraceRecvControl(controlFD, 5000)
	if err != nil || seq != 2 || message != 2 {
		_ = pilotTraceClose(conn, "child-B")
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("SHM child START_WRITE receive: seq=%d message=%d err=%v", seq, message, err)
	}
	phase := "update"
	err = pilotTraceExec(ctx, conn, "child-B", "UPDATE t SET v=v+1")
	if err == nil {
		phase = "commit"
		err = pilotTraceExec(ctx, conn, "child-B", "COMMIT")
	}
	if err != nil {
		sendSQLError(3, phase, err)
		return
	}
	value, queryErr := pilotTraceQueryInt(ctx, conn, "child-B", "SELECT v FROM t")
	if queryErr != nil || value != 2 {
		if queryErr == nil {
			queryErr = fmt.Errorf("post-commit value mismatch: got %d", value)
		}
		sendSQLError(3, "post_commit_verify", queryErr)
		return
	}
	if err := pilotTraceClose(conn, "child-B"); err != nil {
		pilotTraceWriteChildTraceOrFail(t, tracePath)
		t.Fatalf("SHM child close: %v", err)
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		t.Fatalf("persist SHM child terminal trace: %v", err)
	}
	if err := pilotTraceSendControl(controlFD, 3, 3); err != nil {
		t.Fatalf("SHM child COMMIT_DONE send: %v", err)
	}
}

// TestPilotVFSChildSHMFinalUnmapReopen is a child-only endpoint for the
// sequential cross-process final-unmap/reopen candidate.
func TestPilotVFSChildSHMFinalUnmapReopen(t *testing.T) {
	if os.Getenv("PILOT_VFS_FINAL_CHILD") != "1" {
		return
	}
	fd, err := strconv.Atoi(os.Getenv("PILOT_VFS_FINAL_CONTROL_FD"))
	if err != nil || fd < 3 {
		t.Fatalf("invalid final-unmap child control descriptor: %v", err)
	}
	tracePath := os.Getenv("PILOT_VFS_FINAL_TRACE_PATH")
	if !filepath.IsAbs(tracePath) {
		t.Fatal("final-unmap child trace path must be absolute")
	}
	control := os.NewFile(uintptr(fd), "pilot-final-unmap-control")
	if control == nil {
		t.Fatal("final-unmap child control descriptor unavailable")
	}
	defer control.Close()
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatalf("final-unmap child register trace VFS: %v", err)
	}
	defer func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("final-unmap child unregister trace VFS: %v", err)
		}
	}()
	const scenarioID = "SHM-FINAL-UNMAP-REOPEN"
	version, number, sourceID := sqlite3.Version()
	if version != pinnedSQLiteVersion || number != pinnedSQLiteVersionNum || sourceID != pinnedSQLiteSourceID {
		t.Fatalf("final-unmap child SQLite identity mismatch: %s/%d/%s", version, number, sourceID)
	}
	if pilotTraceParentName() != "unix" {
		t.Fatalf("final-unmap child parent VFS=%q, want unix", pilotTraceParentName())
	}
	pilotTraceSetContext(os.Getenv("PILOT_VFS_FINAL_RUN_ID"), scenarioID, scenarioID, sourceID)
	uri := os.Getenv("PILOT_VFS_FINAL_DB_URI")
	if uri == "" {
		t.Fatal("missing final-unmap database URI")
	}
	ctx := context.Background()
	conn, err := pilotTraceOpen(ctx, uri, "participant-A")
	if err != nil {
		t.Fatalf("final-unmap participant A open: %v", err)
	}
	value, err := pilotTraceQueryInt(ctx, conn, "participant-A", "SELECT v FROM t")
	if err != nil || value != 7 {
		_ = pilotTraceClose(conn, "participant-A")
		t.Fatalf("final-unmap participant A read value=%d err=%v", value, err)
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		_ = pilotTraceClose(conn, "participant-A")
		t.Fatalf("persist final-unmap child trace before READY: %v", err)
	}
	controlFD := int(control.Fd())
	if err := pilotTraceSendControl(controlFD, 1, 5); err != nil {
		_ = pilotTraceClose(conn, "participant-A")
		t.Fatalf("send FINAL_CLOSE_READY: %v", err)
	}
	seq, message, err := pilotTraceRecvControl(controlFD, 5000)
	if err != nil || seq != 2 || message != 6 {
		_ = pilotTraceClose(conn, "participant-A")
		t.Fatalf("final-unmap CLOSE command: seq=%d message=%d err=%v", seq, message, err)
	}
	if err := pilotTraceClose(conn, "participant-A"); err != nil {
		t.Fatalf("final-unmap participant A close: %v", err)
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		t.Fatalf("persist final-unmap child trace after close: %v", err)
	}
	if err := pilotTraceSendControl(controlFD, 3, 7); err != nil {
		t.Fatalf("send UNMAP_DONE: %v", err)
	}
}

func TestPilotVFSChildTracePersistsBeforeTerminalBarrier(t *testing.T) {
	if version, number, sourceID := sqlite3.Version(); version != pinnedSQLiteVersion || number != pinnedSQLiteVersionNum || sourceID != pinnedSQLiteSourceID {
		t.Fatalf("runtime SQLite identity mismatch: %s/%d/%s", version, number, sourceID)
	}
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("unregister parent trace VFS: %v", err)
		}
	}()
	root := t.TempDir()
	_, uri := pilotTraceCreateDBFile(t, root, "LOCK-MISSING-TERMINAL-BARRIER")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if err := pilotTraceExec(ctx, setup, "setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "setup")
		t.Fatal(err)
	}
	if err := pilotTraceClose(setup, "setup"); err != nil {
		t.Fatal(err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parentFD, childFD := pair[0], pair[1]
	defer unix.Close(parentFD)
	tracePath := filepath.Join(t.TempDir(), "child-trace.jsonl")
	childFile := os.NewFile(uintptr(childFD), "pilot-vfs-missing-barrier-control")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPilotVFSChildLockContention$")
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.Env = pilotTraceChildEnvironment("pilot-vfs-missing-barrier-test", pinnedSQLiteSourceID, uri, tracePath)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		t.Fatal(err)
	}
	_ = childFile.Close()
	waitDone := make(chan error, 1)
	waitStarted, reaped := false, false
	waitChild := func(timeout time.Duration) (error, bool) {
		if !waitStarted {
			waitStarted = true
			go func() { waitDone <- cmd.Wait() }()
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case err := <-waitDone:
			return err, true
		case <-timer.C:
			return nil, false
		}
	}
	defer func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_, reaped = waitChild(5 * time.Second)
			if !reaped {
				t.Errorf("child process did not reap after forced termination")
			}
		}
	}()
	seq, typ, err := pilotTraceRecvControl(parentFD, 5000)
	if err != nil || seq != 1 || typ != 1 {
		t.Fatalf("child READY barrier: seq=%d type=%d err=%v output=%s", seq, typ, err, output.String())
	}
	firstSnapshot, err := pilotTraceReadArtifact(tracePath)
	if err != nil {
		t.Fatalf("child did not durably publish trace before READY: %v", err)
	}
	childEvents, err := pilotTraceDecodeEvents(firstSnapshot)
	if err != nil {
		t.Fatalf("decode pre-terminal child trace: %v", err)
	}
	reserved := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
		return e.Actor == "child-B" && e.Callback == "xLock" && e.Arg0 == 2 && e.RC == 0
	})
	if reserved == nil {
		t.Fatalf("pre-terminal child trace omitted RESERVED xLock: %s", firstSnapshot)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("terminate child before terminal barrier: %v", err)
	}
	if _, ok := waitChild(5 * time.Second); !ok {
		t.Fatal("child did not reap within bounded test deadline")
	}
	reaped = true
	if _, _, err := pilotTraceRecvControl(parentFD, 50); err == nil {
		t.Fatal("child unexpectedly emitted a terminal control message after forced termination")
	}
	afterKill, err := pilotTraceReadArtifact(tracePath)
	if err != nil || !bytes.Equal(firstSnapshot, afterKill) {
		t.Fatalf("pre-terminal child trace was not preserved after kill: read_err=%v before=%d after=%d", err, len(firstSnapshot), len(afterKill))
	}
}

func TestPilotVFSUnlockSelectionSkipsPreReservationUnlock(t *testing.T) {
	events := []pilotTraceEvent{
		{Seq: 21, Callback: "xUnlock", Actor: "child-B", Arg0: 0, RC: 0},
		{Seq: 27, Callback: "xLock", Actor: "child-B", Arg0: 2, RC: 0},
		{Seq: 31, Callback: "xUnlock", Actor: "child-B", Arg0: 0, RC: 0},
	}
	unlock := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xUnlock" && e.Actor == "child-B" && e.Arg0 == 0 && e.RC == 0 && e.Seq > 27
	})
	if unlock == nil || unlock.Seq != 31 {
		t.Fatalf("selected unlock=%+v, want post-RESERVED unlock at seq 31", unlock)
	}
}

func TestPilotTraceSQLErrorPayloadUsesStructuredRedactedFields(t *testing.T) {
	input := &sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusyRecovery}
	payload, err := pilotTraceEncodeSQLError("commit", input)
	if err != nil {
		t.Fatal(err)
	}
	var got pilotTraceSQLError
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Kind != "sqlite_error" || got.Phase != "commit" || got.SQLiteCode == nil || *got.SQLiteCode != int(sqlite3.ErrBusy) || got.ExtendedCode == nil || *got.ExtendedCode != int(sqlite3.ErrBusyRecovery) {
		t.Fatalf("structured SQLite error=%+v payload=%s", got, payload)
	}
	if bytes.Contains(payload, []byte("database is locked")) || bytes.Contains(payload, []byte("/tmp/")) {
		t.Fatalf("structured error leaked raw diagnostic text: %s", payload)
	}
}

func TestPilotTraceExecutionMetadataDoesNotCopyEnvironmentPaths(t *testing.T) {
	commandSecret := "C:\\private\\runner.exe --token=do-not-log"
	logSecret := "D:\\private\\run.log"
	filesystemSecret := "D:\\private\\volume"
	t.Setenv("PILOT_VFS_TEST_COMMAND", commandSecret)
	t.Setenv("PILOT_VFS_RUN_LOG", logSecret)
	t.Setenv("PILOT_VFS_FILESYSTEM", filesystemSecret)
	metadata := pilotTraceExecutionMetadata(t.TempDir())
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(commandSecret)) || bytes.Contains(encoded, []byte(logSecret)) || bytes.Contains(encoded, []byte(filesystemSecret)) {
		t.Fatalf("execution metadata leaked environment paths: %s", encoded)
	}
	if metadata.Filesystem == filesystemSecret {
		t.Fatalf("filesystem metadata copied untrusted environment value %q", metadata.Filesystem)
	}
}

func TestPilotTraceExistingSidecarRequiresReaderOpen(t *testing.T) {
	events := []pilotTraceEvent{
		{Actor: "conn-A", Callback: "xOpen", Role: "WAL", RC: 0},
		{Actor: "conn-B", Callback: "xOpen", Role: "MAIN_DB", RC: 0},
	}
	if got := pilotTraceExistingWALOpen(events, "conn-B"); len(got) != 0 {
		t.Fatalf("owner WAL creation was misattributed to reader: %+v", got)
	}
	events = append(events, pilotTraceEvent{Actor: "conn-B", Callback: "xOpen", Role: "WAL", RC: 0})
	if got := pilotTraceExistingWALOpen(events, "conn-B"); len(got) != 1 {
		t.Fatalf("reader WAL open not selected: %+v", got)
	}
}

func TestPilotTraceSQLErrorPayloadPreservesCleanupFailureWithoutRawText(t *testing.T) {
	input := &sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusyRecovery}
	payload, err := pilotTraceEncodeSQLErrorWithCleanup("commit", input, []string{"rollback", "close"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := pilotTraceDecodeSQLError(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.CleanupErrors, []string{"rollback", "close"}) {
		t.Fatalf("cleanup errors=%v payload=%s", got.CleanupErrors, payload)
	}
	if bytes.Contains(payload, []byte("database is locked")) || bytes.Contains(payload, []byte("/tmp/")) {
		t.Fatalf("structured cleanup error leaked raw diagnostics: %s", payload)
	}
	if _, err := pilotTraceEncodeSQLErrorWithCleanup("commit", input, []string{"unlink"}); err == nil {
		t.Fatal("unknown cleanup operation was accepted")
	}
}

func TestPilotTraceCleanupSQLErrorOnlyRollsBackActiveTransactions(t *testing.T) {
	for _, phase := range []string{"begin_immediate", "post_commit_verify"} {
		rollbackCalls, closeCalls := 0, 0
		failures := pilotTraceCleanupSQLError(phase, func() error {
			rollbackCalls++
			return nil
		}, func() error {
			closeCalls++
			return nil
		})
		if rollbackCalls != 0 || closeCalls != 1 || len(failures) != 0 {
			t.Fatalf("phase=%s cleanup calls rollback=%d close=%d failures=%v", phase, rollbackCalls, closeCalls, failures)
		}
	}
	rollbackCalls, closeCalls := 0, 0
	failures := pilotTraceCleanupSQLError("commit", func() error {
		rollbackCalls++
		return nil
	}, func() error {
		closeCalls++
		return nil
	})
	if rollbackCalls != 1 || closeCalls != 1 || len(failures) != 0 {
		t.Fatalf("commit cleanup calls rollback=%d close=%d failures=%v", rollbackCalls, closeCalls, failures)
	}
}

func TestPilotTraceCleanupFailureWithholdsCandidateStatus(t *testing.T) {
	rollbackFailure := pilotTraceCleanupSQLError("commit", func() error {
		return errors.New("rollback failed")
	}, func() error { return nil })
	closeFailure := pilotTraceCleanupSQLError("commit", func() error {
		return nil
	}, func() error { return errors.New("close failed") })
	for _, failures := range [][]string{rollbackFailure, closeFailure} {
		payload, err := pilotTraceEncodeSQLErrorWithCleanup("commit", &sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusyRecovery}, failures)
		if err != nil {
			t.Fatal(err)
		}
		record, err := pilotTraceDecodeSQLError(payload)
		if err != nil {
			t.Fatal(err)
		}
		if pilotTraceSQLErrorCandidateStatus(&record) != "NOT RUN" {
			t.Fatalf("cleanup failures %v incorrectly retained candidate status", failures)
		}
	}
	if pilotTraceSQLErrorCandidateStatus(&pilotTraceSQLError{}) != "CANDIDATE" {
		t.Fatal("clean structured SQL error should remain a candidate")
	}
}

func TestPilotTraceControlFrameCarriesStructuredSQLError(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])
	payload, err := pilotTraceEncodeSQLError("commit", &sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusyRecovery})
	if err != nil {
		t.Fatal(err)
	}
	if err := pilotTraceSendControlPayload(pair[0], 3, 4, payload); err != nil {
		t.Fatal(err)
	}
	seq, message, got, err := pilotTraceRecvControlPayload(pair[1], 500)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 3 || message != 4 || !bytes.Equal(got, payload) {
		t.Fatalf("control frame seq=%d message=%d payload=%s, want seq=3 message=4 payload=%s", seq, message, got, payload)
	}
}

func TestPilotTraceControlProtocolRejectsMalformedFramesAndSequences(t *testing.T) {
	t.Run("short packet is protocol failure", func(t *testing.T) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pair[0])
		defer unix.Close(pair[1])
		if _, err := unix.Write(pair[0], []byte{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
		_, _, err = pilotTraceRecvControl(pair[1], 500)
		if err == nil || !pilotTraceMalformedControl(err.Error()) {
			t.Fatalf("short frame error=%v, want protocol failure", err)
		}
	})
	t.Run("oversized packet is protocol failure", func(t *testing.T) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pair[0])
		defer unix.Close(pair[1])
		packet := make([]byte, 9+4097)
		if _, err := unix.SendmsgN(pair[0], packet, nil, nil, unix.MSG_NOSIGNAL); err != nil {
			t.Fatal(err)
		}
		_, _, _, err = pilotTraceRecvControlPayload(pair[1], 500)
		if err == nil || !pilotTraceMalformedControl(err.Error()) {
			t.Fatalf("oversized frame error=%v, want protocol failure", err)
		}
	})
	t.Run("peer eof is fail-closed because empty records are indistinguishable", func(t *testing.T) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pair[1])
		if err := unix.Close(pair[0]); err != nil {
			t.Fatal(err)
		}
		_, _, _, err = pilotTraceRecvControlPayload(pair[1], 500)
		if err == nil || !pilotTraceMalformedControl(err.Error()) {
			t.Fatalf("peer close error=%v, want fail-closed protocol failure", err)
		}
	})
	t.Run("invalid descriptor is fatal protocol failure", func(t *testing.T) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		fd := pair[1]
		if err := unix.Close(pair[1]); err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pair[0])
		_, _, _, err = pilotTraceRecvControlPayload(fd, 500)
		if err == nil || !pilotTraceMalformedControl(err.Error()) {
			t.Fatalf("invalid descriptor error=%v, want fatal protocol failure", err)
		}
	})
	t.Run("wrong sequence or type cannot be not run", func(t *testing.T) {
		for _, got := range []struct {
			seq uint64
			typ byte
		}{{2, 1}, {1, 9}} {
			err := pilotTraceUnexpectedControl(got.seq, got.typ, 1, 1, 0)
			if !pilotTraceMalformedControl(err.Error()) {
				t.Fatalf("unexpected frame error=%v was not marked fatal", err)
			}
		}
	})
	t.Run("invalid structured error cannot be not run", func(t *testing.T) {
		for _, payload := range [][]byte{[]byte(`{"version":2,"kind":"sqlite_error","phase":"commit"}`), []byte(`{"version":1,"kind":"sqlite_error","phase":"commit","unknown":true}`), []byte(`not-json`)} {
			if _, err := pilotTraceDecodeControlSQLError(payload); err == nil || !pilotTraceMalformedControl(err.Error()) {
				t.Fatalf("invalid error envelope %s returned %v, want protocol failure", payload, err)
			}
		}
	})
	t.Run("payload on empty control message is protocol failure", func(t *testing.T) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pair[0])
		defer unix.Close(pair[1])
		if err := pilotTraceSendControlPayload(pair[0], 1, 1, []byte("unexpected")); err != nil {
			t.Fatal(err)
		}
		_, _, err = pilotTraceRecvControl(pair[1], 500)
		if err == nil || !pilotTraceMalformedControl(err.Error()) {
			t.Fatalf("payload frame error=%v, want protocol failure", err)
		}
	})
}

func pilotTraceLockCrossProcess(t *testing.T, root, runID, sourceID, artifactDir string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "LOCK-CROSS-PROCESS-CONTEND")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "setup")
	if err != nil {
		t.Fatalf("cross-process lock setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "setup")
		t.Fatalf("cross-process lock setup SQL: %v", err)
	}
	if err := pilotTraceClose(setup, "setup"); err != nil {
		t.Fatalf("cross-process lock setup close: %v", err)
	}
	const scenarioID = "LOCK-CROSS-PROCESS-CONTEND"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	parent, err := pilotTraceOpen(ctx, uri, "parent-A")
	if err != nil {
		t.Fatalf("cross-process parent open: %v", err)
	}
	if err := pilotTraceExec(ctx, parent, "parent-A", "BEGIN"); err != nil {
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("cross-process parent BEGIN: %v", err)
	}
	value, err := pilotTraceQueryInt(ctx, parent, "parent-A", "SELECT v FROM t")
	if err != nil || value != 1 {
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("cross-process parent snapshot=%d err=%v", value, err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("create bounded child control socket: %v", err)
	}
	parentFD, childFD := pair[0], pair[1]
	tracePath := filepath.Join(t.TempDir(), "child-lock-trace.jsonl")
	childFile := os.NewFile(uintptr(childFD), "pilot-vfs-child-control")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPilotVFSChildLockContention$")
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.Env = pilotTraceChildEnvironment(runID, sourceID, uri, tracePath)
	var childStdout, childStderr bytes.Buffer
	cmd.Stdout = &childStdout
	cmd.Stderr = &childStderr
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		_ = unix.Close(parentFD)
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("start child SQLite lock participant: %v", err)
	}
	childControlFDClosed := childFile.Close() == nil
	childFinished := false
	childKilled := false
	childTimedOut := false
	childTerminal := "NOT_OBSERVED"
	var parentControlFDCloseObserved bool
	waitDone := make(chan error, 1)
	waitStarted := false
	waitChild := func(timeout time.Duration) (error, bool) {
		if !waitStarted {
			waitStarted = true
			go func() { waitDone <- cmd.Wait() }()
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case err := <-waitDone:
			return err, true
		case <-timer.C:
			childTimedOut = true
			return nil, false
		}
	}
	stopChild := func() bool {
		if childFinished {
			return true
		}
		if cmd.Process != nil {
			if cmd.Process.Kill() == nil {
				childKilled = true
			}
		}
		_, reaped := waitChild(5 * time.Second)
		if reaped {
			childFinished = true
		}
		cleanup := "CLEAN"
		if !reaped {
			cleanup = "INCOMPLETE"
		}
		pilotTraceLogChildCleanup(scenarioID, "child-B", true, reaped, cleanup, childTimedOut)
		return reaped
	}
	collectNotRun := func(reason string) (pilotTraceScenario, string, bool) {
		if !stopChild() {
			t.Fatalf("%s; child process could not be reaped within deadline", reason)
		}
		if err := pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK"); err != nil {
			_ = pilotTraceClose(parent, "parent-A")
			t.Fatalf("%s; parent rollback failed: %v", reason, err)
		}
		if err := pilotTraceClose(parent, "parent-A"); err != nil {
			t.Fatalf("%s; parent close failed: %v", reason, err)
		}
		if scenarioID == "LOCK-CROSS-PROCESS-CONTEND" {
			t.Fatalf("%s lock barrier unavailable; refusing NOT RUN: %s child_stdout=%s child_stderr=%s", pilotTraceControlProtocolFailure, reason, childStdout.String(), childStderr.String())
		}
		if pilotTraceMalformedControl(reason) {
			t.Fatalf("malformed child control protocol; refusing NOT RUN: %s child_stdout=%s child_stderr=%s", reason, childStdout.String(), childStderr.String())
		}
		if pilotTraceChildPublicationFailed(&childStdout, &childStderr) {
			t.Fatalf("child trace publication failed; refusing NOT RUN: %s", childStdout.String()+childStderr.String())
		}
		parentEvents := pilotTraceSnapshot(t)
		parentRaw := pilotTraceDump()
		for i := range parentEvents {
			parentEvents[i].Process = "parent"
		}
		events := parentEvents
		childRaw := ""
		if childBytes, readErr := pilotTraceReadArtifact(tracePath); readErr == nil {
			childRaw = string(childBytes)
			if childEvents, decodeErr := pilotTraceDecodeEvents(childBytes); decodeErr == nil {
				for i := range childEvents {
					childEvents[i].Process = "child"
				}
				events = append(events, childEvents...)
			}
		}
		trace := strings.TrimRight(parentRaw, "\n") + "\n"
		if childRaw != "" {
			trace += strings.TrimLeft(childRaw, "\n")
		}
		childTimedOut = childTimedOut || strings.Contains(strings.ToLower(reason), "timeout")
		terminal := childTerminal
		childExecution := pilotTraceChildRecord(artifactDir, scenarioID, "child-B", "go test binary -test.run=^TestPilotVFSChildLockContention$", cmd, &childStdout, &childStderr, childFinished && childRaw != "", childTimedOut, childKilled, childFinished, terminal, "")
		childExecution.ChildControlFDClosed = childControlFDClosed && childFinished
		childExecution.ParentControlFDClosed = &parentControlFDCloseObserved
		return pilotTraceScenario{ID: scenarioID, Status: "NOT RUN", Reason: reason,
			Oracle: map[string]any{"scope": "barrier incomplete; partial callbacks preserved, no cross-process outcome inferred"}, ChildExecutions: []pilotTraceChildExecution{childExecution}, Events: events}, trace, true
	}
	defer func() {
		if unix.Close(parentFD) == nil {
			parentControlFDCloseObserved = true
		}
		if !childFinished && cmd.Process != nil {
			_ = stopChild()
		}
	}()
	firstBarrierTimeout := 5000
	if os.Getenv("PILOT_VFS_TEST_CONTROL_FAULT") == "timeout-lock" {
		firstBarrierTimeout = 100
	}
	messageSeq, message, err := pilotTraceRecvControl(parentFD, firstBarrierTimeout)
	if err == nil && (messageSeq != 1 || message != 1) {
		return collectNotRun(pilotTraceUnexpectedControl(messageSeq, message, 1, 1, 0).Error())
	}
	if err != nil {
		return collectNotRun(fmt.Sprintf("child RESERVED_ACQUIRED barrier missing (seq=%d, message=%d, err=%v)", messageSeq, message, err))
	}
	childTerminal = "RESERVED_ACQUIRED"
	writeErr := pilotTraceExec(ctx, parent, "parent-A", "UPDATE t SET v=v+1")
	var sqliteErr sqlite3.Error
	if !errors.As(writeErr, &sqliteErr) || sqliteErr.Code != sqlite3.ErrBusy {
		_ = pilotTraceSendControl(parentFD, 2, 2)
		_ = stopChild()
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("parent competing write error=%v, want SQLITE_BUSY; child output=%s", writeErr, childStdout.String()+childStderr.String())
	}
	if err := pilotTraceSendControl(parentFD, 2, 2); err != nil {
		return collectNotRun(fmt.Sprintf("child release command failed: %v", err))
	}
	messageSeq, message, err = pilotTraceRecvControl(parentFD, 5000)
	if err == nil && (messageSeq != 3 || message != 3) {
		return collectNotRun(pilotTraceUnexpectedControl(messageSeq, message, 3, 3, 0).Error())
	}
	if err != nil {
		return collectNotRun(fmt.Sprintf("child RELEASED barrier missing (seq=%d, message=%d, err=%v)", messageSeq, message, err))
	}
	childTerminal = "RELEASED"
	if err, finished := waitChild(5 * time.Second); !finished {
		return collectNotRun("child did not exit after terminal barrier")
	} else if err != nil {
		childFinished = true
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("child SQLite process failed: %v: %s", err, childStdout.String()+childStderr.String())
	}
	childFinished = true
	if err := pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("parent rollback: %v", err)
	}
	if err := pilotTraceClose(parent, "parent-A"); err != nil {
		t.Fatalf("parent close: %v", err)
	}
	parentEvents := pilotTraceSnapshot(t)
	parentRaw := pilotTraceDump()
	assertTraceCorrelation(t, parentEvents, runID, scenarioID, sourceID)
	for i := range parentEvents {
		parentEvents[i].Process = "parent"
	}
	childBytes, err := pilotTraceReadArtifact(tracePath)
	if err != nil {
		t.Fatalf("read child trace artifact: %v", err)
	}
	if len(parentRaw) == 0 || !bytes.HasSuffix([]byte(parentRaw), []byte("\n")) {
		t.Fatalf("parent raw trace is empty or not newline-terminated")
	}
	if len(childBytes) == 0 || !bytes.HasSuffix(childBytes, []byte("\n")) {
		t.Fatalf("child raw trace is empty or not newline-terminated")
	}
	childEvents, err := pilotTraceDecodeEvents(childBytes)
	if err != nil {
		t.Fatalf("decode child trace: %v", err)
	}
	assertTraceCorrelation(t, childEvents, runID, scenarioID, sourceID)
	for i := range childEvents {
		childEvents[i].Process = "child"
	}
	events := append(parentEvents, childEvents...)
	aBusy := firstTraceEvent(parentEvents, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "parent-A" && e.Arg0 == 2 && e.RC == int(sqlite3.ErrBusy)
	})
	bReserved := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "child-B" && e.Arg0 == 2 && e.RC == 0
	})
	var bUnlock *pilotTraceEvent
	if bReserved != nil {
		bUnlock = firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
			return e.Callback == "xUnlock" && e.Actor == "child-B" && e.Arg0 == 0 && e.RC == 0 && e.Seq > bReserved.Seq
		})
	}
	if aBusy == nil || bReserved == nil || bUnlock == nil {
		t.Fatalf("cross-process lock callbacks missing: B_RESERVED=%+v A_BUSY=%+v B_UNLOCK=%+v", bReserved, aBusy, bUnlock)
	}
	trace := parentRaw + string(childBytes)
	childExecution := pilotTraceChildRecord(artifactDir, scenarioID, "child-B", "go test binary -test.run=^TestPilotVFSChildLockContention$", cmd, &childStdout, &childStderr, true, false, false, true, "RELEASED", "")
	childExecution.ChildControlFDClosed = childControlFDClosed
	childExecution.ParentControlFDClosed = &parentControlFDCloseObserved
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", ChildExecutions: []pilotTraceChildExecution{childExecution}, Oracle: map[string]any{
		"parent_snapshot": value, "parent_update": "SQLITE_BUSY", "child_begin_immediate": "SQLITE_OK",
		"control_messages": []map[string]any{
			{"seq": 1, "participant": "child-B", "type": "RESERVED_ACQUIRED"},
			{"seq": 2, "participant": "parent-A", "type": "RELEASE"},
			{"seq": 3, "participant": "child-B", "type": "RELEASED"},
		},
		"parent_update_result":    map[string]any{"participant": "parent-A", "type": "UPDATE_BUSY", "after_message_seq": 1, "before_message_seq": 2},
		"callback_sequence_edges": []string{"child-B xLock RESERVED before child_READY", "parent-A xLock RESERVED SQLITE_BUSY after child_READY", "child-B xUnlock NONE after parent_RELEASE"},
		"b_reserved_seq":          bReserved.Seq, "a_busy_seq": aBusy.Seq, "b_unlock_seq": bUnlock.Seq,
		"scope": "two processes on host unix VFS; callback traces retain process-local sequence and explicit process_role; passive syscall lock byte ranges, external identity policy and private-VFS behavior are not tested",
	}, Events: events}, trace, true
}

func pilotTraceSHMCrossProcess(t *testing.T, root, runID, sourceID, artifactDir string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "SHM-CROSS-PROCESS-CONTEND")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "shm-setup")
	if err != nil {
		t.Fatalf("cross-process SHM setup open: %v", err)
	}
	if err := pilotTraceExec(ctx, setup, "shm-setup", "PRAGMA journal_mode=WAL; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "shm-setup")
		t.Fatalf("cross-process SHM setup SQL: %v", err)
	}
	if err := pilotTraceClose(setup, "shm-setup"); err != nil {
		t.Fatalf("cross-process SHM setup close: %v", err)
	}
	const scenarioID = "SHM-CROSS-PROCESS-CONTEND"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	parent, err := pilotTraceOpen(ctx, uri, "parent-A")
	if err != nil {
		t.Fatalf("cross-process SHM parent open: %v", err)
	}
	if err := pilotTraceExec(ctx, parent, "parent-A", "BEGIN"); err != nil {
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("cross-process SHM parent BEGIN: %v", err)
	}
	oldValue, err := pilotTraceQueryInt(ctx, parent, "parent-A", "SELECT v FROM t")
	if err != nil || oldValue != 1 {
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("cross-process SHM parent snapshot=%d err=%v", oldValue, err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("create SHM child control socket: %v", err)
	}
	parentFD, childFD := pair[0], pair[1]
	tracePath := filepath.Join(t.TempDir(), "child-shm-trace.jsonl")
	childFile := os.NewFile(uintptr(childFD), "pilot-vfs-child-shm-control")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPilotVFSChildSHMContention$")
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.Env = pilotTraceSHMChildEnvironment(runID, sourceID, uri, tracePath)
	var childStdout, childStderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childStdout, &childStderr
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		_ = unix.Close(parentFD)
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("start cross-process SHM child: %v", err)
	}
	childControlFDClosed := childFile.Close() == nil
	childFinished := false
	childKilled := false
	childTimedOut := false
	var parentControlFDCloseObserved bool
	var childSQLError *pilotTraceSQLError
	var childSQLErrorSeq uint64
	childTerminal := "NOT_OBSERVED"
	waitDone := make(chan error, 1)
	waitStarted := false
	waitChild := func(timeout time.Duration) (error, bool) {
		if !waitStarted {
			waitStarted = true
			go func() { waitDone <- cmd.Wait() }()
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case err := <-waitDone:
			return err, true
		case <-timer.C:
			childTimedOut = true
			return nil, false
		}
	}
	stopChild := func() bool {
		if childFinished {
			return true
		}
		if cmd.Process != nil {
			if cmd.Process.Kill() == nil {
				childKilled = true
			}
		}
		_, reaped := waitChild(5 * time.Second)
		if reaped {
			childFinished = true
		}
		cleanup := "CLEAN"
		if !reaped {
			cleanup = "INCOMPLETE"
		}
		pilotTraceLogChildCleanup(scenarioID, "child-B", true, reaped, cleanup, childTimedOut)
		return reaped
	}
	collectNotRun := func(reason string) (pilotTraceScenario, string, bool) {
		if !stopChild() {
			t.Fatalf("%s; SHM child process could not be reaped", reason)
		}
		if err := pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK"); err != nil {
			_ = pilotTraceClose(parent, "parent-A")
			t.Fatalf("%s; parent snapshot rollback failed: %v", reason, err)
		}
		if err := pilotTraceClose(parent, "parent-A"); err != nil {
			t.Fatalf("%s; parent close failed: %v", reason, err)
		}
		if pilotTraceMalformedControl(reason) {
			t.Fatalf("malformed SHM child control protocol; refusing NOT RUN: %s child_stdout=%s child_stderr=%s", reason, childStdout.String(), childStderr.String())
		}
		if pilotTraceChildPublicationFailed(&childStdout, &childStderr) {
			t.Fatalf("SHM child trace publication failed; refusing NOT RUN: %s", childStdout.String()+childStderr.String())
		}
		parentEvents := pilotTraceSnapshot(t)
		parentRaw := pilotTraceDump()
		for i := range parentEvents {
			parentEvents[i].Process = "parent"
		}
		events := parentEvents
		childRaw := []byte(nil)
		if childBytes, readErr := pilotTraceReadArtifact(tracePath); readErr == nil {
			childRaw = childBytes
			if childEvents, decodeErr := pilotTraceDecodeEvents(childBytes); decodeErr == nil {
				for i := range childEvents {
					childEvents[i].Process = "child"
				}
				events = append(events, childEvents...)
			}
		}
		status := "NOT RUN"
		oracle := map[string]any{"scope": "SHM barriers incomplete; partial callbacks preserved, no cross-process commit inferred"}
		if childSQLError != nil {
			status = pilotTraceSQLErrorCandidateStatus(childSQLError)
		}
		if childSQLError != nil && status == "CANDIDATE" {
			oracle["child_terminal"] = "SQLITE_ERROR"
			oracle["child_error"] = childSQLError
			oracle["child_error_control_seq"] = childSQLErrorSeq
			oracle["child_error_control_message"] = "SQLITE_ERROR"
		} else if childSQLError != nil {
			oracle["child_error"] = childSQLError
			oracle["scope"] = "SHM child SQL error cleanup incomplete; callbacks preserved, candidate status withheld"
		}
		terminal := childTerminal
		errorSummary := ""
		if childSQLError != nil {
			terminal = "SQLITE_ERROR"
			errorSummary = "structured SQLite error; see allowlisted oracle details"
		}
		childTimedOut = childTimedOut || strings.Contains(strings.ToLower(reason), "timeout")
		childExecution := pilotTraceChildRecord(artifactDir, scenarioID, "child-B", "go test binary -test.run=^TestPilotVFSChildSHMContention$", cmd, &childStdout, &childStderr, childFinished && len(childRaw) != 0, childTimedOut, childKilled, childFinished, terminal, errorSummary)
		childExecution.ChildControlFDClosed = childControlFDClosed && childFinished
		childExecution.ParentControlFDClosed = &parentControlFDCloseObserved
		scenarioReason := reason
		if status == "CANDIDATE" {
			scenarioReason = ""
		}
		return pilotTraceScenario{ID: scenarioID, Status: status, Reason: scenarioReason, Oracle: oracle, ChildExecutions: []pilotTraceChildExecution{childExecution}, Events: events}, parentRaw + string(childRaw), true
	}
	defer func() {
		if unix.Close(parentFD) == nil {
			parentControlFDCloseObserved = true
		}
		if !childFinished && cmd.Process != nil {
			_ = stopChild()
		}
	}()
	seq, message, payload, err := pilotTraceRecvControlPayload(parentFD, 5000)
	if err == nil && seq == 1 && message == 4 {
		parsed, parseErr := pilotTraceDecodeControlSQLError(payload)
		if parseErr != nil {
			return collectNotRun(fmt.Sprintf("%s child pre-READY error frame invalid: %v", pilotTraceControlProtocolFailure, parseErr))
		}
		childSQLError = &parsed
		childSQLErrorSeq = seq
		childTerminal = "SQLITE_ERROR"
		if _, finished := waitChild(5 * time.Second); !finished {
			return collectNotRun("child did not exit after structured pre-READY SQLite error")
		}
		childFinished = true
		return collectNotRun("child reported structured SQLite error before WRITER_READY")
	}
	if err == nil && (seq != 1 || message != 1) {
		return collectNotRun(pilotTraceUnexpectedControl(seq, message, 1, 1, len(payload)).Error())
	}
	if err != nil {
		return collectNotRun(fmt.Sprintf("SHM child READY missing (seq=%d message=%d err=%v)", seq, message, err))
	}
	childTerminal = "WRITER_READY"
	if len(payload) != 0 {
		return collectNotRun(fmt.Sprintf("%s SHM child READY unexpectedly carried %d payload bytes", pilotTraceControlProtocolFailure, len(payload)))
	}
	childBefore, err := pilotTraceReadArtifact(tracePath)
	if err != nil {
		return collectNotRun(fmt.Sprintf("SHM child pre-READY trace unavailable: %v", err))
	}
	childPreEvents, err := pilotTraceDecodeEvents(childBefore)
	if err != nil {
		return collectNotRun(fmt.Sprintf("SHM child pre-READY trace invalid: %v", err))
	}
	if firstTraceEvent(childPreEvents, func(e pilotTraceEvent) bool { return e.Actor == "child-B" && e.Callback == "xShmLock" }) == nil {
		return collectNotRun("SHM child READY arrived without a preceding child xShmLock callback")
	}
	if err := pilotTraceSendControl(parentFD, 2, 2); err != nil {
		return collectNotRun(fmt.Sprintf("SHM START_WRITE send failed: %v", err))
	}
	seq, message, payload, err = pilotTraceRecvControlPayload(parentFD, 5000)
	if err == nil && seq == 3 && message == 4 {
		parsed, parseErr := pilotTraceDecodeControlSQLError(payload)
		if parseErr != nil {
			return collectNotRun(fmt.Sprintf("%s child terminal SQLite error frame invalid: %v", pilotTraceControlProtocolFailure, parseErr))
		}
		childSQLError = &parsed
		childSQLErrorSeq = seq
		childTerminal = "SQLITE_ERROR"
		if _, finished := waitChild(5 * time.Second); !finished {
			return collectNotRun("child did not exit after structured terminal SQLite error")
		}
		childFinished = true
		return collectNotRun("child reported structured SQLite error after START_WRITE")
	}
	if err == nil && (seq != 3 || message != 3) {
		return collectNotRun(pilotTraceUnexpectedControl(seq, message, 3, 3, len(payload)).Error())
	}
	if err != nil {
		return collectNotRun(fmt.Sprintf("SHM child COMMIT_DONE missing (seq=%d message=%d err=%v)", seq, message, err))
	}
	childTerminal = "COMMIT_DONE"
	if len(payload) != 0 {
		return collectNotRun(fmt.Sprintf("%s SHM COMMIT_DONE unexpectedly carried %d payload bytes", pilotTraceControlProtocolFailure, len(payload)))
	}
	if waitErr, finished := waitChild(5 * time.Second); !finished {
		return collectNotRun("SHM child did not exit after COMMIT_DONE")
	} else if waitErr != nil {
		childFinished = true
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("SHM child exited with error: %v: %s", waitErr, childStdout.String()+childStderr.String())
	}
	childFinished = true
	newSnapshotValue, err := pilotTraceQueryInt(ctx, parent, "parent-A", "SELECT v FROM t")
	if err != nil || newSnapshotValue != oldValue {
		_ = pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK")
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("SHM parent snapshot changed while transaction open: value=%d want=%d err=%v", newSnapshotValue, oldValue, err)
	}
	if err := pilotTraceExec(ctx, parent, "parent-A", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(parent, "parent-A")
		t.Fatalf("SHM parent snapshot rollback: %v", err)
	}
	if err := pilotTraceClose(parent, "parent-A"); err != nil {
		t.Fatalf("SHM parent close: %v", err)
	}
	verify, err := pilotTraceOpen(ctx, uri, "verify")
	if err != nil {
		t.Fatalf("SHM post-commit verifier open: %v", err)
	}
	committed, err := pilotTraceQueryInt(ctx, verify, "verify", "SELECT v FROM t")
	if err != nil || committed != 2 {
		_ = pilotTraceClose(verify, "verify")
		t.Fatalf("SHM committed value=%d want=2 err=%v", committed, err)
	}
	if err := pilotTraceClose(verify, "verify"); err != nil {
		t.Fatalf("SHM verifier close: %v", err)
	}
	parentEvents := pilotTraceSnapshot(t)
	parentRaw := pilotTraceDump()
	for i := range parentEvents {
		parentEvents[i].Process = "parent"
	}
	childRaw, err := pilotTraceReadArtifact(tracePath)
	if err != nil {
		t.Fatalf("read terminal SHM child trace: %v", err)
	}
	if len(parentRaw) == 0 || !bytes.HasSuffix([]byte(parentRaw), []byte("\n")) || len(childRaw) == 0 || !bytes.HasSuffix(childRaw, []byte("\n")) {
		t.Fatalf("parent/child SHM JSONL must both be nonempty and newline-terminated")
	}
	childEvents, err := pilotTraceDecodeEvents(childRaw)
	if err != nil {
		t.Fatalf("decode terminal SHM child trace: %v", err)
	}
	assertTraceCorrelation(t, parentEvents, runID, scenarioID, sourceID)
	assertTraceCorrelation(t, childEvents, runID, scenarioID, sourceID)
	for i := range childEvents {
		childEvents[i].Process = "child"
	}
	events := append(parentEvents, childEvents...)
	callbacks := []string{"xShmMap", "xShmLock", "xShmBarrier", "xShmUnmap"}
	for _, callback := range callbacks {
		if len(eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == callback })) == 0 {
			t.Fatalf("docs/56 SHM cross-process candidate missing callback %s", callback)
		}
	}
	childWriterAcquire := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
		return e.Actor == "child-B" && e.Callback == "xShmLock" && e.Arg0 == 0 && e.Arg1 == 1 && e.Arg2 == 10 && e.RC == 0
	})
	if childWriterAcquire == nil {
		t.Fatal("SHM child trace omitted successful exclusive writer-lock acquisition (offset=0,count=1,flags=LOCK|EXCLUSIVE)")
	}
	var lastPreReadySeq uint64
	for _, event := range childPreEvents {
		if event.Seq > lastPreReadySeq {
			lastPreReadySeq = event.Seq
		}
	}
	preReadyWriterAcquire := firstTraceEvent(childPreEvents, func(e pilotTraceEvent) bool {
		return e.Actor == "child-B" && e.Callback == "xShmLock" && e.Arg0 == 0 && e.Arg1 == 1 && e.Arg2 == 10 && e.RC == 0
	})
	if preReadyWriterAcquire == nil || preReadyWriterAcquire.Seq != childWriterAcquire.Seq || childWriterAcquire.Seq > lastPreReadySeq {
		t.Fatalf("successful writer lock must be durably present before READY: lock=%+v pre_ready=%+v tail=%d", childWriterAcquire, preReadyWriterAcquire, lastPreReadySeq)
	}
	childWriterRelease := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
		return e.Actor == "child-B" && e.Callback == "xShmLock" && e.Arg0 == 0 && e.Arg1 == 1 && e.Arg2 == 9 && e.RC == 0 && e.Seq > childWriterAcquire.Seq
	})
	childUnmap := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
		return e.Actor == "child-B" && e.Callback == "xShmUnmap" && e.RC == 0
	})
	if childWriterRelease == nil || childUnmap == nil || childUnmap.Seq <= childWriterRelease.Seq {
		t.Fatalf("SHM child writer release/unmap order missing: acquire=%+v release=%+v unmap=%+v", childWriterAcquire, childWriterRelease, childUnmap)
	}
	if pilotTraceOverflowed() {
		t.Fatal("cross-process SHM callback trace overflowed")
	}
	childExecution := pilotTraceChildRecord(artifactDir, scenarioID, "child-B", "go test binary -test.run=^TestPilotVFSChildSHMContention$", cmd, &childStdout, &childStderr, true, false, false, true, "COMMIT_DONE", "")
	childExecution.ChildControlFDClosed = childControlFDClosed
	childExecution.ParentControlFDClosed = &parentControlFDCloseObserved
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", ChildExecutions: []pilotTraceChildExecution{childExecution}, Oracle: map[string]any{
		"control_messages": []map[string]any{
			{"seq": 1, "participant": "child-B", "type": "WRITER_READY"},
			{"seq": 2, "participant": "parent-A", "type": "START_WRITE"},
			{"seq": 3, "participant": "child-B", "type": "COMMIT_DONE"},
		},
		"parent_snapshot_before": oldValue, "parent_snapshot_after_child_commit": newSnapshotValue,
		"child_committed_value": committed, "callbacks_observed": callbacks,
		"callback_barrier_edges":    []string{"child-B BEGIN IMMEDIATE and xShmLock before READY", "parent-A START_WRITE after READY", "child-B update and commit after START_WRITE", "child-B terminal callbacks before COMMIT_DONE"},
		"child_writer_lock_acquire": map[string]any{"seq": childWriterAcquire.Seq, "offset": childWriterAcquire.Arg0, "count": childWriterAcquire.Arg1, "flags": childWriterAcquire.Arg2, "rc": childWriterAcquire.RC},
		"child_writer_lock_release": map[string]any{"seq": childWriterRelease.Seq, "offset": childWriterRelease.Arg0, "count": childWriterRelease.Arg1, "flags": childWriterRelease.Arg2, "rc": childWriterRelease.RC},
		"child_shm_unmap_seq":       childUnmap.Seq,
		"child_pre_ready_last_seq":  lastPreReadySeq,
		"scope":                     "cross-process host unix VFS SHM callback candidate; no passive syscall or private-VFS acceptance",
	}, Events: events}, parentRaw + string(childRaw), true
}

func pilotTraceChildEnvironment(runID, sourceID, uri, tracePath string) []string {
	const prefix = "PILOT_VFS_LOCK_"
	filtered := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) && !strings.HasPrefix(entry, "PILOT_VFS_SHM_") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered,
		"PILOT_VFS_LOCK_CHILD=1",
		"PILOT_VFS_LOCK_CONTROL_FD=3",
		"PILOT_VFS_LOCK_RUN_ID="+runID,
		"PILOT_VFS_LOCK_SOURCE_ID="+sourceID,
		"PILOT_VFS_LOCK_DB_URI="+uri,
		"PILOT_VFS_LOCK_TRACE_PATH="+tracePath,
	)
}

func pilotTraceSHMChildEnvironment(runID, sourceID, uri, tracePath string) []string {
	env := pilotTraceChildEnvironment(runID, sourceID, uri, tracePath)
	return append(env, "PILOT_VFS_SHM_CHILD=1")
}

func pilotTraceFinalSHMChildEnvironment(runID, sourceID, uri, tracePath string) []string {
	filtered := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PILOT_VFS_FINAL_") && !strings.HasPrefix(entry, "PILOT_VFS_LOCK_") && !strings.HasPrefix(entry, "PILOT_VFS_SHM_") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered,
		"PILOT_VFS_FINAL_CHILD=1",
		"PILOT_VFS_FINAL_CONTROL_FD=3",
		"PILOT_VFS_FINAL_RUN_ID="+runID,
		"PILOT_VFS_FINAL_SOURCE_ID="+sourceID,
		"PILOT_VFS_FINAL_DB_URI="+uri,
		"PILOT_VFS_FINAL_TRACE_PATH="+tracePath,
	)
}

func pilotTraceSendControl(fd int, sequence uint64, message byte) error {
	return pilotTraceSendControlPayload(fd, sequence, message, nil)
}

func pilotTraceSendControlPayload(fd int, sequence uint64, message byte, payload []byte) error {
	if message == 1 {
		switch os.Getenv("PILOT_VFS_TEST_CONTROL_FAULT") {
		case "short-frame":
			packet := make([]byte, 8)
			binary.BigEndian.PutUint64(packet, sequence)
			_, err := unix.SendmsgN(fd, packet, nil, nil, unix.MSG_NOSIGNAL)
			return err
		case "oversized-frame":
			payload = bytes.Repeat([]byte{'x'}, 4097)
		case "wrong-sequence":
			sequence++
		case "wrong-type":
			message = 9
		case "payload-on-empty":
			payload = []byte("unexpected")
		}
	}
	if len(payload) > 4096 {
		if os.Getenv("PILOT_VFS_TEST_CONTROL_FAULT") != "oversized-frame" || message != 1 {
			return fmt.Errorf("control payload too large: %d", len(payload))
		}
	}
	packet := make([]byte, 9)
	binary.BigEndian.PutUint64(packet[:8], sequence)
	packet[8] = message
	packet = append(packet, payload...)
	n, err := unix.SendmsgN(fd, packet, nil, nil, unix.MSG_NOSIGNAL)
	if err != nil {
		return err
	}
	if n != len(packet) {
		return fmt.Errorf("%s control packet sent %d bytes, want %d", pilotTraceControlProtocolFailure, n, len(packet))
	}
	return nil
}

func pilotTraceTestCloseControlPeer(fault string) {
	if os.Getenv("PILOT_VFS_TEST_CONTROL_FAULT") == fault {
		os.Exit(0)
	}
}

func pilotTraceEncodeSQLError(phase string, cause error) ([]byte, error) {
	return pilotTraceEncodeSQLErrorWithCleanup(phase, cause, nil)
}

func pilotTraceCleanupSQLError(phase string, rollback, closeConn func() error) []string {
	var failures []string
	if phase == "update" || phase == "commit" {
		if err := rollback(); err != nil {
			failures = append(failures, "rollback")
		}
	}
	if err := closeConn(); err != nil {
		failures = append(failures, "close")
	}
	return failures
}

func pilotTraceSQLErrorCandidateStatus(record *pilotTraceSQLError) string {
	if record == nil || len(record.CleanupErrors) != 0 {
		return "NOT RUN"
	}
	return "CANDIDATE"
}

func pilotTraceEncodeSQLErrorWithCleanup(phase string, cause error, cleanupErrors []string) ([]byte, error) {
	record := pilotTraceSQLError{Version: 1, Kind: "operation_error", Phase: phase}
	for _, op := range cleanupErrors {
		if op != "rollback" && op != "close" {
			return nil, fmt.Errorf("invalid cleanup operation %q", op)
		}
		record.CleanupErrors = append(record.CleanupErrors, op)
	}
	var sqliteErr sqlite3.Error
	var code, extended int
	if errors.As(cause, &sqliteErr) {
		code, extended = int(sqliteErr.Code), int(sqliteErr.ExtendedCode)
		record.Kind = "sqlite_error"
	} else {
		var sqliteErrPtr *sqlite3.Error
		if errors.As(cause, &sqliteErrPtr) && sqliteErrPtr != nil {
			code, extended = int(sqliteErrPtr.Code), int(sqliteErrPtr.ExtendedCode)
			record.Kind = "sqlite_error"
		}
	}
	if record.Kind == "sqlite_error" {
		record.SQLiteCode = &code
		record.ExtendedCode = &extended
	}
	return json.Marshal(record)
}

func pilotTraceDecodeSQLError(payload []byte) (pilotTraceSQLError, error) {
	var record pilotTraceSQLError
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return record, errors.New("structured child error contains trailing JSON data")
	}
	if record.Version != 1 || (record.Phase != "begin_immediate" && record.Phase != "update" && record.Phase != "commit" && record.Phase != "post_commit_verify") || (record.Kind != "sqlite_error" && record.Kind != "operation_error") {
		return record, fmt.Errorf("invalid structured child error envelope")
	}
	if record.Kind == "sqlite_error" && (record.SQLiteCode == nil || record.ExtendedCode == nil) {
		return record, fmt.Errorf("structured SQLite error omitted result codes")
	}
	if record.Kind == "operation_error" && (record.SQLiteCode != nil || record.ExtendedCode != nil) {
		return record, fmt.Errorf("operation error unexpectedly carries SQLite result codes")
	}
	for _, op := range record.CleanupErrors {
		if op != "rollback" && op != "close" {
			return record, fmt.Errorf("invalid structured cleanup operation %q", op)
		}
	}
	return record, nil
}

func pilotTraceDecodeControlSQLError(payload []byte) (pilotTraceSQLError, error) {
	record, err := pilotTraceDecodeSQLError(payload)
	if err != nil {
		return record, fmt.Errorf("%s invalid structured SQLite error envelope: %w", pilotTraceControlProtocolFailure, err)
	}
	return record, nil
}

func pilotTraceWriteChildTrace(path string) error {
	if pilotTraceOverflowed() {
		return errors.New("PILOT_TRACE_PUBLICATION_FAILED: child callback trace overflowed")
	}
	data := []byte(pilotTraceDump())
	phase := "first"
	if pilotTraceChildSnapshotCount > 0 {
		phase = "repeat"
	}
	pilotTraceChildSnapshotCount++
	if err := pilotTraceReplaceArtifactWithFault(path, data, phase); err != nil {
		return fmt.Errorf("PILOT_TRACE_PUBLICATION_FAILED: %w", err)
	}
	return nil
}

func pilotTraceWriteChildTraceOrFail(t *testing.T, path string) {
	t.Helper()
	if err := pilotTraceWriteChildTrace(path); err != nil {
		t.Fatalf("PILOT_TRACE_PUBLICATION_FAILED: %v", err)
	}
}

var pilotTraceChildSnapshotCount int

func pilotTraceChildPublicationFailed(stdout, stderr *bytes.Buffer) bool {
	return (stdout != nil && bytes.Contains(stdout.Bytes(), []byte("PILOT_TRACE_PUBLICATION_FAILED"))) ||
		(stderr != nil && bytes.Contains(stderr.Bytes(), []byte("PILOT_TRACE_PUBLICATION_FAILED")))
}

const pilotTraceControlProtocolFailure = "PILOT_VFS_CONTROL_PROTOCOL_FAILURE:"

func pilotTraceMalformedControl(reason string) bool {
	return strings.Contains(reason, pilotTraceControlProtocolFailure)
}

func pilotTraceUnexpectedControl(seq uint64, message byte, wantSeq uint64, wantMessage byte, payloadLen int) error {
	return fmt.Errorf("%s got seq=%d message=%d payload_bytes=%d; want seq=%d message=%d", pilotTraceControlProtocolFailure, seq, message, payloadLen, wantSeq, wantMessage)
}

func pilotTraceRecvControl(fd int, timeoutMs int) (uint64, byte, error) {
	seq, message, payload, err := pilotTraceRecvControlPayload(fd, timeoutMs)
	if err != nil {
		return 0, 0, err
	}
	if len(payload) != 0 {
		return 0, 0, fmt.Errorf("%s control message %d unexpectedly carried %d payload bytes", pilotTraceControlProtocolFailure, message, len(payload))
	}
	return seq, message, nil
}

func pilotTraceRecvControlPayload(fd int, timeoutMs int) (uint64, byte, []byte, error) {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	var poll []unix.PollFd
	for {
		poll = []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		waitMs := int(remaining / time.Millisecond)
		if remaining > 0 && waitMs == 0 {
			waitMs = 1
		}
		n, err := unix.Poll(poll, waitMs)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, 0, nil, fmt.Errorf("%s poll control socket: %w", pilotTraceControlProtocolFailure, err)
		}
		if n == 0 {
			return 0, 0, nil, errors.New("control packet barrier timeout")
		}
		flags := poll[0].Revents
		if flags&(unix.POLLNVAL|unix.POLLERR) != 0 {
			return 0, 0, nil, fmt.Errorf("%s poll control socket returned revents=%#x", pilotTraceControlProtocolFailure, flags)
		}
		if flags&unix.POLLIN != 0 {
			break
		}
		if flags&unix.POLLHUP != 0 {
			return 0, 0, nil, fmt.Errorf("%s peer closed before a complete control frame", pilotTraceControlProtocolFailure)
		}
		return 0, 0, nil, fmt.Errorf("%s unexpected poll revents=%#x", pilotTraceControlProtocolFailure, flags)
	}
	var packet [9 + 4096]byte
	var read, flags int
	for {
		var err error
		read, _, flags, _, err = unix.Recvmsg(fd, packet[:], nil, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, 0, nil, fmt.Errorf("%s receive control packet: %w", pilotTraceControlProtocolFailure, err)
		}
		break
	}
	if read == 0 {
		if flags&unix.MSG_EOR != 0 {
			return 0, 0, nil, fmt.Errorf("%s zero-length SOCK_SEQPACKET control record", pilotTraceControlProtocolFailure)
		}
		return 0, 0, nil, fmt.Errorf("%s empty read is ambiguous with peer closure", pilotTraceControlProtocolFailure)
	}
	if flags&unix.MSG_TRUNC != 0 || read < 9 || read > len(packet) {
		return 0, 0, nil, fmt.Errorf("%s control packet length=%d flags=%#x outside bounded frame", pilotTraceControlProtocolFailure, read, flags)
	}
	return binary.BigEndian.Uint64(packet[:8]), packet[8], append([]byte(nil), packet[9:read]...), nil
}

func pilotTraceDecodeEvents(raw []byte) ([]pilotTraceEvent, error) {
	var events []pilotTraceEvent
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event pilotTraceEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, errors.New("empty process trace")
	}
	return events, nil
}

func TestPilotTraceBufferOverflowSignal(t *testing.T) {
	pilotTraceReset()
	if pilotTraceOverflowed() {
		t.Fatal("fresh trace buffer reports overflow")
	}
	pilotTraceSetContext("run", "scenario", "case", strings.Repeat("x", 200))
	if !pilotTraceOverflowed() {
		t.Fatal("truncated source context did not invalidate trace candidate")
	}
	pilotTraceSetContext("run", "scenario", "case", "source")
	pilotTraceReset()
	pilotTraceTestSetOverflowed(true)
	if !pilotTraceOverflowed() {
		t.Fatal("overflow signal was not observable; a truncated trace could be mislabeled candidate")
	}
	pilotTraceReset()
	if pilotTraceOverflowed() {
		t.Fatal("reset did not clear overflow state")
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func pilotTraceCreateDBFile(t *testing.T, root, base string) (string, string) {
	t.Helper()
	dir := filepath.Join(root, strings.ToLower(base))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operations.sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path, (&urlForPilotDB{Path: filepath.ToSlash(path)}).dsn()
}

func pilotTraceSHMLocal(t *testing.T, root, runID, sourceID string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "SHM-LOCAL-LOCK-UNLOCK")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "setup")
	if err != nil {
		t.Fatalf("SHM setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "setup", "PRAGMA journal_mode=WAL; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "setup")
		t.Fatalf("SHM setup SQL: %v", err)
	}
	if err = pilotTraceClose(setup, "setup"); err != nil {
		t.Fatalf("SHM setup close: %v", err)
	}

	const scenarioID = "SHM-LOCAL-LOCK-UNLOCK"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	a, err := pilotTraceOpen(ctx, uri, "conn-A")
	if err != nil {
		t.Fatalf("SHM reader open: %v", err)
	}
	if err = pilotTraceExec(ctx, a, "conn-A", "BEGIN"); err != nil {
		_ = pilotTraceClose(a, "conn-A")
		t.Fatalf("SHM reader begin: %v", err)
	}
	oldValue, err := pilotTraceQueryInt(ctx, a, "conn-A", "SELECT v FROM t")
	if err != nil || oldValue != 1 {
		_ = pilotTraceClose(a, "conn-A")
		t.Fatalf("SHM reader barrier SELECT v=%d err=%v", oldValue, err)
	}
	// This synchronous acknowledgement is the READER_READY barrier: B is not
	// opened until A's read snapshot and SHM lock callbacks have completed.
	b, err := pilotTraceOpen(ctx, uri, "conn-B")
	if err != nil {
		_ = pilotTraceClose(a, "conn-A")
		t.Fatalf("SHM writer open: %v", err)
	}
	if err = pilotTraceExec(ctx, b, "conn-B", "BEGIN IMMEDIATE; UPDATE t SET v=2; COMMIT"); err != nil {
		_ = pilotTraceClose(b, "conn-B")
		_ = pilotTraceClose(a, "conn-A")
		t.Fatalf("SHM writer transaction: %v", err)
	}
	if err = pilotTraceClose(b, "conn-B"); err != nil {
		_ = pilotTraceClose(a, "conn-A")
		t.Fatalf("SHM writer close: %v", err)
	}
	if err = pilotTraceExec(ctx, a, "conn-A", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(a, "conn-A")
		t.Fatalf("SHM reader rollback: %v", err)
	}
	if err = pilotTraceClose(a, "conn-A"); err != nil {
		t.Fatalf("SHM reader close: %v", err)
	}
	events := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, events, runID, scenarioID, sourceID)
	for _, callback := range []string{"xShmMap", "xShmLock", "xShmBarrier", "xShmUnmap"} {
		if len(eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == callback })) == 0 {
			t.Fatalf("docs/56 SHM oracle callback %s missing from exercised WAL scenario", callback)
		}
	}
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", Oracle: map[string]any{
		"reader_ready_after_value": oldValue, "writer_commit": "ok", "callbacks_observed": []string{"xShmMap", "xShmLock", "xShmBarrier", "xShmUnmap"},
		"scope": "same-process host unix VFS callback candidate; no cross-process or private-VFS claim",
	}, Events: events}, pilotTraceDump(), true
}

func pilotTraceSHMFinalUnmapReopen(t *testing.T, root, runID, sourceID, artifactDir string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "SHM-FINAL-UNMAP-REOPEN")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "shm-reopen-setup")
	if err != nil {
		t.Fatalf("SHM reopen setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "shm-reopen-setup", "PRAGMA journal_mode=WAL; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(7)"); err != nil {
		_ = pilotTraceClose(setup, "shm-reopen-setup")
		t.Fatalf("SHM reopen setup SQL: %v", err)
	}
	if err = pilotTraceClose(setup, "shm-reopen-setup"); err != nil {
		t.Fatalf("SHM reopen setup close: %v", err)
	}
	const scenarioID = "SHM-FINAL-UNMAP-REOPEN"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("create final-unmap control socket: %v", err)
	}
	parentFD, childFD := pair[0], pair[1]
	tracePath := filepath.Join(artifactDir, "shm-final-unmap-child.raw.jsonl")
	childFile := os.NewFile(uintptr(childFD), "pilot-final-unmap-control")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPilotVFSChildSHMFinalUnmapReopen$")
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.Env = pilotTraceFinalSHMChildEnvironment(runID, sourceID, uri, tracePath)
	var childStdout, childStderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childStdout, &childStderr
	if err := cmd.Start(); err != nil {
		_ = childFile.Close()
		_ = unix.Close(parentFD)
		t.Fatalf("start final-unmap child: %v", err)
	}
	childControlClosed := childFile.Close() == nil
	childFinished, childKilled, childTimedOut := false, false, false
	childTerminal := "NOT_OBSERVED"
	var parentControlClosed bool
	var waitErr error
	waitDone := make(chan error, 1)
	waitStarted := false
	waitChild := func(timeout time.Duration) bool {
		if !waitStarted {
			waitStarted = true
			go func() { waitDone <- cmd.Wait() }()
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case waitErr = <-waitDone:
			childFinished = true
			return true
		case <-timer.C:
			childTimedOut = true
			return false
		}
	}
	stopChild := func() bool {
		if childFinished {
			return true
		}
		if cmd.Process.Kill() == nil {
			childKilled = true
		}
		return waitChild(5 * time.Second)
	}
	defer func() {
		if unix.Close(parentFD) == nil {
			parentControlClosed = true
		}
		if !childFinished {
			_ = stopChild()
		}
	}()
	collectNotRun := func(reason string) (pilotTraceScenario, string, bool) {
		if !stopChild() {
			t.Fatalf("%s; final-unmap child could not be reaped", reason)
		}
		if pilotTraceMalformedControl(reason) {
			t.Fatalf("malformed final-unmap child control protocol; refusing NOT RUN: %s", reason)
		}
		if pilotTraceChildPublicationFailed(&childStdout, &childStderr) {
			t.Fatalf("final-unmap child trace publication failed; refusing NOT RUN: %s", childStdout.String()+childStderr.String())
		}
		childBytes, _ := pilotTraceReadArtifact(tracePath)
		parentRaw := pilotTraceDump()
		parentEvents := pilotTraceSnapshot(t)
		for i := range parentEvents {
			parentEvents[i].Process = "parent"
		}
		childEvents, _ := pilotTraceDecodeEvents(childBytes)
		for i := range childEvents {
			childEvents[i].Process = "child"
		}
		events := append(parentEvents, childEvents...)
		child := pilotTraceChildRecord(artifactDir, scenarioID, "child-A", "go test binary -test.run=^TestPilotVFSChildSHMFinalUnmapReopen$", cmd, &childStdout, &childStderr, len(childBytes) > 0, childTimedOut, childKilled, childFinished, childTerminal, "")
		child.ChildControlFDClosed = childControlClosed && childFinished
		child.ParentControlFDClosed = &parentControlClosed
		return pilotTraceScenario{ID: scenarioID, Status: "NOT RUN", Reason: reason, Events: events, ChildExecutions: []pilotTraceChildExecution{child}}, parentRaw + string(childBytes), true
	}
	seq, message, err := pilotTraceRecvControl(parentFD, 5000)
	if err == nil && (seq != 1 || message != 5) {
		return collectNotRun(pilotTraceUnexpectedControl(seq, message, 1, 5, 0).Error())
	}
	if err != nil {
		return collectNotRun(fmt.Sprintf("FINAL_CLOSE_READY barrier missing (seq=%d message=%d err=%v)", seq, message, err))
	}
	childTerminal = "FINAL_CLOSE_READY"
	childBefore, err := pilotTraceReadArtifact(tracePath)
	if err != nil {
		return collectNotRun(fmt.Sprintf("final-unmap pre-close child trace unavailable: %v", err))
	}
	preEvents, err := pilotTraceDecodeEvents(childBefore)
	if err != nil || firstTraceEvent(preEvents, func(e pilotTraceEvent) bool { return e.Callback == "xShmMap" && e.Actor == "participant-A" }) == nil {
		return collectNotRun("FINAL_CLOSE_READY arrived without a durable participant A xShmMap trace")
	}
	if err := pilotTraceSendControl(parentFD, 2, 6); err != nil {
		return collectNotRun(fmt.Sprintf("send final CLOSE command: %v", err))
	}
	seq, message, err = pilotTraceRecvControl(parentFD, 5000)
	if err == nil && (seq != 3 || message != 7) {
		return collectNotRun(pilotTraceUnexpectedControl(seq, message, 3, 7, 0).Error())
	}
	if err != nil {
		return collectNotRun(fmt.Sprintf("UNMAP_DONE barrier missing (seq=%d message=%d err=%v)", seq, message, err))
	}
	childTerminal = "UNMAP_DONE"
	if !waitChild(5 * time.Second) {
		return collectNotRun("final-unmap child did not exit after UNMAP_DONE")
	}
	if waitErr != nil {
		t.Fatalf("final-unmap child failed: %v", waitErr)
	}
	childBytes, err := pilotTraceReadArtifact(tracePath)
	if err != nil {
		t.Fatalf("read final-unmap child trace: %v", err)
	}
	childEvents, err := pilotTraceDecodeEvents(childBytes)
	if err != nil {
		t.Fatalf("decode final-unmap child trace: %v", err)
	}
	assertTraceCorrelation(t, childEvents, runID, scenarioID, sourceID)
	mapA := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool { return e.Callback == "xShmMap" && e.Actor == "participant-A" })
	unmap := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool { return e.Callback == "xShmUnmap" && e.Actor == "participant-A" })
	if mapA == nil || unmap == nil {
		return collectNotRun(fmt.Sprintf("child final mapping lifetime was not observed (map=%t unmap=%t)", mapA != nil, unmap != nil))
	}
	if mapA.Seq >= unmap.Seq || pilotTraceHandleID(*unmap) != pilotTraceHandleID(*mapA) {
		t.Fatalf("participant A SHM map/unmap identity or order mismatch: map=%+v unmap=%+v", mapA, unmap)
	}
	b, err := pilotTraceOpen(ctx, uri, "participant-B")
	if err != nil {
		t.Fatalf("SHM participant B reopen: %v", err)
	}
	reopenedValue, err := pilotTraceQueryInt(ctx, b, "participant-B", "SELECT v FROM t")
	if err != nil || reopenedValue != 7 {
		_ = pilotTraceClose(b, "participant-B")
		t.Fatalf("SHM participant B read value=%d want=7 err=%v", reopenedValue, err)
	}
	if err = pilotTraceClose(b, "participant-B"); err != nil {
		t.Fatalf("SHM participant B close: %v", err)
	}
	parentEvents := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, parentEvents, runID, scenarioID, sourceID)
	for i := range parentEvents {
		parentEvents[i].Process = "parent"
	}
	for i := range childEvents {
		childEvents[i].Process = "child"
	}
	events := append(childEvents, parentEvents...)
	var reopenMap *pilotTraceEvent
	for i := range parentEvents {
		if parentEvents[i].Callback == "xShmMap" && parentEvents[i].Actor == "participant-B" {
			reopenMap = &parentEvents[i]
			break
		}
	}
	if reopenMap == nil {
		return collectNotRun("participant B did not emit xShmMap after child A final unmap")
	}
	child := pilotTraceChildRecord(artifactDir, scenarioID, "child-A", "go test binary -test.run=^TestPilotVFSChildSHMFinalUnmapReopen$", cmd, &childStdout, &childStderr, true, childTimedOut, childKilled, childFinished, "UNMAP_DONE", "")
	child.ChildControlFDClosed = childControlClosed
	child.ParentControlFDClosed = &parentControlClosed
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", ChildExecutions: []pilotTraceChildExecution{child}, Oracle: map[string]any{
		"participant_a_value": 7, "participant_b_value": reopenedValue,
		"control_messages":          []map[string]any{{"seq": 1, "participant": "child-A", "type": "FINAL_CLOSE_READY"}, {"seq": 2, "participant": "parent", "type": "CLOSE"}, {"seq": 3, "participant": "child-A", "type": "UNMAP_DONE"}},
		"barrier":                   "participant A final close and child reap completed before participant B open began",
		"observed_callback_order":   []string{"child participant-A xShmUnmap", "parent participant-B xShmMap"},
		"participant_a_file_handle": map[string]any{"process_role": "child", "file_handle_id": pilotTraceHandleID(*mapA)}, "participant_a_map_seq": mapA.Seq,
		"participant_a_unmap_seq": unmap.Seq, "participant_b_map_handle_id": pilotTraceHandleID(*reopenMap),
		"participant_b_file_handle": map[string]any{"process_role": "parent", "file_handle_id": pilotTraceHandleID(*reopenMap)}, "participant_b_map_seq": reopenMap.Seq,
		"scope": "two-process sequential host unix VFS characterization; no split-inode or private-VFS safety claim",
	}, Events: events}, string(childBytes) + pilotTraceDump(), true
}

func pilotTraceLockLocal(t *testing.T, root, runID, sourceID string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "LOCK-LOCAL-CONTEND")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "lock-setup")
	if err != nil {
		t.Fatalf("lock setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "lock-setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "lock-setup")
		t.Fatalf("lock setup SQL: %v", err)
	}
	if err = pilotTraceClose(setup, "lock-setup"); err != nil {
		t.Fatalf("lock setup close: %v", err)
	}
	const scenarioID = "LOCK-LOCAL-CONTEND"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	a, err := pilotTraceOpen(ctx, uri, "participant-A")
	if err != nil {
		t.Fatalf("lock participant A open: %v", err)
	}
	if err = pilotTraceExec(ctx, a, "participant-A", "BEGIN"); err != nil {
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant A begin: %v", err)
	}
	value, err := pilotTraceQueryInt(ctx, a, "participant-A", "SELECT v FROM t")
	if err != nil || value != 1 {
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant A shared snapshot value=%d err=%v", value, err)
	}
	b, err := pilotTraceOpen(ctx, uri, "participant-B")
	if err != nil {
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant B open: %v", err)
	}
	if err = pilotTraceExec(ctx, b, "participant-B", "BEGIN IMMEDIATE"); err != nil {
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant B did not acquire RESERVED: %v", err)
	}
	// B's successful BEGIN IMMEDIATE is the RESERVED_ACQUIRED barrier.
	writeErr := pilotTraceExec(ctx, a, "participant-A", "UPDATE t SET v=v+1")
	var sqliteErr sqlite3.Error
	if !errors.As(writeErr, &sqliteErr) || sqliteErr.Code != sqlite3.ErrBusy {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("participant A competing update error=%v; want SQLITE_BUSY", writeErr)
	}
	if err = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant B rollback: %v", err)
	}
	if err = pilotTraceClose(b, "participant-B"); err != nil {
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant B close: %v", err)
	}
	if err = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock participant A rollback: %v", err)
	}
	if err = pilotTraceClose(a, "participant-A"); err != nil {
		t.Fatalf("lock participant A close: %v", err)
	}
	events := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, events, runID, scenarioID, sourceID)
	bReserved := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-B" && e.Arg0 == 2 && e.RC == 0
	})
	aBusy := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-A" && e.Arg0 == 2 && e.RC == int(sqlite3.ErrBusy)
	})
	if bReserved == nil || aBusy == nil || bReserved.Seq >= aBusy.Seq {
		t.Fatalf("docs/56 local-lock observed callback order mismatch: B reserved=%+v A busy=%+v", bReserved, aBusy)
	}
	bUnlock := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xUnlock" && e.Actor == "participant-B" && e.Arg0 == 1 && e.RC == 0 && e.Seq > aBusy.Seq
	})
	if bUnlock == nil {
		t.Fatalf("docs/56 local-lock observed no B unlock-to-SHARED after A busy: B reserved=%+v A busy=%+v", bReserved, aBusy)
	}
	callbacks := []string{"xLock", "xUnlock"}
	xCheckObserved := len(eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == "xCheckReservedLock" })) > 0
	if xCheckObserved {
		callbacks = append(callbacks, "xCheckReservedLock")
	}
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", Oracle: map[string]any{
		"participant_a_read_value": value, "participant_b_begin_immediate": "SQLITE_OK",
		"participant_a_update": "SQLITE_BUSY", "callbacks_observed": callbacks,
		"xCheckReservedLock_observed": xCheckObserved,
		"b_reserved_lock_seq":         bReserved.Seq, "a_busy_lock_seq": aBusy.Seq, "b_unlock_seq": bUnlock.Seq,
		"scope": "same-process host unix VFS callback candidate; passive syscall ranges, cross-process contention, and private-VFS policy are not tested",
	}, Events: events}, pilotTraceDump(), true
}

func pilotTraceLockDowngrade(t *testing.T, root, runID, sourceID string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "LOCK-DOWNGRADE")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "lock-downgrade-setup")
	if err != nil {
		t.Fatalf("lock downgrade setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "lock-downgrade-setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "lock-downgrade-setup")
		t.Fatalf("lock downgrade setup SQL: %v", err)
	}
	if err = pilotTraceClose(setup, "lock-downgrade-setup"); err != nil {
		t.Fatalf("lock downgrade setup close: %v", err)
	}
	const scenarioID = "LOCK-DOWNGRADE"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	conn, err := pilotTraceOpen(ctx, uri, "participant-A")
	if err != nil {
		t.Fatalf("lock downgrade participant open: %v", err)
	}
	if err = pilotTraceExec(ctx, conn, "participant-A", "BEGIN IMMEDIATE; UPDATE t SET v=v+1; COMMIT"); err != nil {
		_ = pilotTraceClose(conn, "participant-A")
		t.Fatalf("lock downgrade transaction: %v", err)
	}
	value, err := pilotTraceQueryInt(ctx, conn, "participant-A", "SELECT v FROM t")
	if err != nil || value != 2 {
		_ = pilotTraceClose(conn, "participant-A")
		t.Fatalf("lock downgrade committed value=%d err=%v", value, err)
	}
	if err = pilotTraceClose(conn, "participant-A"); err != nil {
		t.Fatalf("lock downgrade close: %v", err)
	}
	events := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, events, runID, scenarioID, sourceID)
	reserved := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-A" && e.Arg0 == 2 && e.RC == 0
	})
	exclusive := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-A" && e.Arg0 == 4 && e.RC == 0
	})
	shared := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xUnlock" && e.Actor == "participant-A" && e.Arg0 == 1 && e.RC == 0
	})
	if reserved == nil || exclusive == nil || shared == nil || !(reserved.Seq < exclusive.Seq && exclusive.Seq < shared.Seq) {
		t.Fatalf("docs/56 lock-downgrade order missing: RESERVED=%+v EXCLUSIVE=%+v downgrade=%+v", reserved, exclusive, shared)
	}
	none := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xUnlock" && e.Actor == "participant-A" && e.Arg0 == 0 && e.RC == 0 && e.Seq > shared.Seq
	})
	if none == nil {
		t.Fatalf("docs/56 lock-downgrade oracle missing final NONE unlock after SHARED: %+v", events)
	}
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", Oracle: map[string]any{
		"transaction_rc": "SQLITE_OK", "committed_value": value,
		"lock_order":   []string{"RESERVED", "EXCLUSIVE", "SHARED", "NONE"},
		"reserved_seq": reserved.Seq, "exclusive_seq": exclusive.Seq, "shared_unlock_seq": shared.Seq, "none_unlock_seq": none.Seq,
		"scope": "same-process host unix VFS callback candidate; passive syscall lock ranges and private-VFS policy are not tested",
	}, Events: events}, pilotTraceDump(), true
}

func pilotTraceLockPromotionBusy(t *testing.T, root, runID, sourceID string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "LOCK-PROMOTION-BUSY")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "lock-promotion-setup")
	if err != nil {
		t.Fatalf("lock promotion setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "lock-promotion-setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v INTEGER); INSERT INTO t VALUES(1)"); err != nil {
		_ = pilotTraceClose(setup, "lock-promotion-setup")
		t.Fatalf("lock promotion setup SQL: %v", err)
	}
	if err = pilotTraceClose(setup, "lock-promotion-setup"); err != nil {
		t.Fatalf("lock promotion setup close: %v", err)
	}
	const scenarioID = "LOCK-PROMOTION-BUSY"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	a, err := pilotTraceOpen(ctx, uri, "participant-A")
	if err != nil {
		t.Fatalf("lock promotion participant A open: %v", err)
	}
	if err = pilotTraceExec(ctx, a, "participant-A", "BEGIN"); err != nil {
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant A begin: %v", err)
	}
	valueA, err := pilotTraceQueryInt(ctx, a, "participant-A", "SELECT v FROM t")
	if err != nil || valueA != 1 {
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant A snapshot=%d err=%v", valueA, err)
	}
	b, err := pilotTraceOpen(ctx, uri, "participant-B")
	if err != nil {
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant B open: %v", err)
	}
	if err = pilotTraceExec(ctx, b, "participant-B", "BEGIN"); err != nil {
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant B begin: %v", err)
	}
	valueB, err := pilotTraceQueryInt(ctx, b, "participant-B", "SELECT v FROM t")
	if err != nil || valueB != valueA {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant B snapshot=%d want=%d err=%v", valueB, valueA, err)
	}
	// B's successful SELECT is SHARED_HELD; the parent now permits A's update.
	if err = pilotTraceExec(ctx, a, "participant-A", "UPDATE t SET v=v+1"); err != nil {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant A UPDATE did not acquire RESERVED: %v", err)
	}
	// A's successful UPDATE is RESERVED_ACQUIRED; the parent now permits B's write.
	writeErr := pilotTraceExec(ctx, b, "participant-B", "UPDATE t SET v=v+1")
	var sqliteErr sqlite3.Error
	if !errors.As(writeErr, &sqliteErr) || sqliteErr.Code != sqlite3.ErrBusy {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("participant B competing promotion error=%v; want SQLITE_BUSY", writeErr)
	}
	valueBAfterBusy, readAfterBusyErr := pilotTraceQueryInt(ctx, b, "participant-B", "SELECT v FROM t")
	if readAfterBusyErr != nil || valueBAfterBusy != valueB {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("B read snapshot after failed promotion=%d want=%d err=%v", valueBAfterBusy, valueB, readAfterBusyErr)
	}
	eventsBeforeRollback := pilotTraceSnapshot(t)
	aReservedBeforeB := firstTraceEvent(eventsBeforeRollback, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-A" && e.Arg0 == 2 && e.RC == 0
	})
	bShared := firstTraceEvent(eventsBeforeRollback, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-B" && e.Arg0 == 1 && e.RC == 0
	})
	bBusyBeforeRollback := firstTraceEvent(eventsBeforeRollback, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-B" && e.Arg0 == 2 && e.RC == int(sqlite3.ErrBusy)
	})
	if bShared == nil || aReservedBeforeB == nil || bBusyBeforeRollback == nil || !(bShared.Seq < aReservedBeforeB.Seq && aReservedBeforeB.Seq < bBusyBeforeRollback.Seq) {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("docs/56 promotion order missing B SHARED < A RESERVED < B BUSY: Bshared=%+v Areserved=%+v Bbusy=%+v", bShared, aReservedBeforeB, bBusyBeforeRollback)
	}
	if unlocks := eventSubset(eventsBeforeRollback, func(e pilotTraceEvent) bool {
		return e.Callback == "xUnlock" && e.Actor == "participant-B" && e.Seq > bBusyBeforeRollback.Seq
	}); len(unlocks) != 0 {
		_ = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK")
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("B unlocked before explicit rollback after failed promotion: %+v", unlocks)
	}
	if err = pilotTraceExec(ctx, b, "participant-B", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(b, "participant-B")
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant B rollback: %v", err)
	}
	if err = pilotTraceClose(b, "participant-B"); err != nil {
		_ = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK")
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant B close: %v", err)
	}
	if err = pilotTraceExec(ctx, a, "participant-A", "ROLLBACK"); err != nil {
		_ = pilotTraceClose(a, "participant-A")
		t.Fatalf("lock promotion participant A rollback: %v", err)
	}
	if err = pilotTraceClose(a, "participant-A"); err != nil {
		t.Fatalf("lock promotion participant A close: %v", err)
	}
	events := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, events, runID, scenarioID, sourceID)
	aReserved := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-A" && e.Arg0 == 2 && e.RC == 0
	})
	bBusy := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xLock" && e.Actor == "participant-B" && e.Arg0 == 2 && e.RC == int(sqlite3.ErrBusy)
	})
	if aReserved == nil || bBusy == nil || aReserved.Seq >= bBusy.Seq || aReserved.Seq != aReservedBeforeB.Seq || bBusy.Seq != bBusyBeforeRollback.Seq {
		t.Fatalf("docs/56 promotion oracle expected A RESERVED before B BUSY promotion: A=%+v B=%+v", aReserved, bBusy)
	}
	xCheckObserved := len(eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == "xCheckReservedLock" })) > 0
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", Oracle: map[string]any{
		"participant_a_snapshot": valueA, "participant_b_snapshot": valueB, "participant_b_after_busy": valueBAfterBusy,
		"participant_a_update": "SQLITE_OK", "participant_b_update": "SQLITE_BUSY",
		"b_shared_xlock_seq": bShared.Seq, "a_reserved_xlock_seq": aReserved.Seq, "b_busy_xlock_seq": bBusy.Seq,
		"xCheckReservedLock_observed": xCheckObserved,
		"scope":                       "same-process host unix VFS callback candidate; passive syscall lock ranges, cross-process promotion and private-VFS policy are not tested",
	}, Events: events}, pilotTraceDump(), true
}

func pilotTracePagerEmpty(t *testing.T, root, runID, sourceID string) (pilotTraceScenario, string, bool) {
	t.Helper()
	path, uri := pilotTraceCreateDBFile(t, root, "PAGER-EMPTY-OBSOLETE-RESERVED")
	journal, err := os.OpenFile(path+"-journal", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("create zero-byte obsolete journal: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	const scenarioID = "PAGER-EMPTY-OBSOLETE-RESERVED"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	conn, err := pilotTraceOpen(context.Background(), uri, "pager-empty")
	if err != nil {
		t.Fatalf("open zero-length database: %v", err)
	}
	version, err := pilotTraceQueryInt(context.Background(), conn, "pager-empty", "PRAGMA schema_version")
	if err != nil {
		_ = pilotTraceClose(conn, "pager-empty")
		t.Fatalf("read empty database schema_version: %v", err)
	}
	if err := pilotTraceClose(conn, "pager-empty"); err != nil {
		t.Fatalf("close zero-length database: %v", err)
	}
	events := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, events, runID, scenarioID, sourceID)
	deleted := len(eventSubset(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xDelete" && strings.Contains(e.Name, "operations.sqlite-journal")
	})) > 0
	openedJournal := len(eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "MAIN_JOURNAL" })) > 0
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", Oracle: map[string]any{
		"sqlite_rc": "SQLITE_OK", "schema_version": version, "obsolete_journal_xDelete": deleted,
		"main_journal_xOpen": openedJournal, "scope": "observed host unix callbacks; no deletion behavior inferred when absent",
	}, Events: events}, pilotTraceDump(), true
}

func pilotTracePagerCommit(t *testing.T, root, runID, sourceID string) (pilotTraceScenario, string, bool) {
	t.Helper()
	_, uri := pilotTraceCreateDBFile(t, root, "PAGER-COMMIT-EXCLUSIVE")
	ctx := context.Background()
	setup, err := pilotTraceOpen(ctx, uri, "pager-setup")
	if err != nil {
		t.Fatalf("pager setup open: %v", err)
	}
	if err = pilotTraceExec(ctx, setup, "pager-setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v TEXT); INSERT INTO t VALUES('old')"); err != nil {
		_ = pilotTraceClose(setup, "pager-setup")
		t.Fatalf("pager setup SQL: %v", err)
	}
	if err = pilotTraceClose(setup, "pager-setup"); err != nil {
		t.Fatalf("pager setup close: %v", err)
	}
	const scenarioID = "PAGER-COMMIT-EXCLUSIVE"
	pilotTraceReset()
	pilotTraceSetContext(runID, scenarioID, scenarioID, sourceID)
	conn, err := pilotTraceOpen(ctx, uri, "pager-commit")
	if err != nil {
		t.Fatalf("pager commit open: %v", err)
	}
	if err = pilotTraceExec(ctx, conn, "pager-commit", "PRAGMA synchronous=FULL; BEGIN; UPDATE t SET v='new'; COMMIT"); err != nil {
		_ = pilotTraceClose(conn, "pager-commit")
		t.Fatalf("pager synchronous commit: %v", err)
	}
	value, err := pilotTraceQueryText(ctx, conn, "pager-commit", "SELECT v FROM t")
	if err != nil || value != "new" {
		_ = pilotTraceClose(conn, "pager-commit")
		t.Fatalf("pager committed value=%q err=%v", value, err)
	}
	if err = pilotTraceClose(conn, "pager-commit"); err != nil {
		t.Fatalf("pager commit close: %v", err)
	}
	events := pilotTraceSnapshot(t)
	assertTraceCorrelation(t, events, runID, scenarioID, sourceID)
	journalOpen := firstTraceEvent(events, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "MAIN_JOURNAL" })
	journalClose := firstTraceEvent(events, func(e pilotTraceEvent) bool { return e.Callback == "xClose" && e.Role == "MAIN_JOURNAL" })
	journalDelete := firstTraceEvent(events, func(e pilotTraceEvent) bool {
		return e.Callback == "xDelete" && strings.Contains(e.Name, "operations.sqlite-journal")
	})
	if journalOpen == nil || journalClose == nil || journalDelete == nil || !(journalOpen.Seq < journalClose.Seq && journalClose.Seq < journalDelete.Seq) {
		t.Fatalf("docs/56 pager commit oracle requires journal xOpen < xClose < xDelete; open=%+v close=%+v delete=%+v", journalOpen, journalClose, journalDelete)
	}
	return pilotTraceScenario{ID: scenarioID, Status: "CANDIDATE", Oracle: map[string]any{
		"transaction_rc": "SQLITE_OK", "final_value": value, "main_journal_xOpen": len(eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "MAIN_JOURNAL" })) > 0,
		"journal_callback_order": []string{"xOpen", "xClose", "xDelete"},
		"journal_xDelete": len(eventSubset(events, func(e pilotTraceEvent) bool {
			return e.Callback == "xDelete" && strings.Contains(e.Name, "operations.sqlite-journal")
		})) > 0,
		"scope": "observed host unix callbacks with synchronous=FULL; no crash durability claim",
	}, Events: events}, pilotTraceDump(), true
}

func assertTraceCorrelation(t *testing.T, events []pilotTraceEvent, runID, scenarioID, sourceID string) {
	t.Helper()
	if len(events) == 0 {
		t.Fatalf("scenario %s produced no trace events", scenarioID)
	}
	for _, event := range events {
		if event.RunID != runID || event.ScenarioID != scenarioID || event.CaseID != scenarioID || event.SourceID != sourceID {
			t.Fatalf("trace correlation mismatch for %s: %+v", scenarioID, event)
		}
	}
}

type urlForPilotDB struct{ Path string }

func (u *urlForPilotDB) dsn() string { return "file://" + u.Path + "?mode=rw&vfs=" + pilotTraceVFSName }

func pilotTraceOpen(ctx context.Context, uri, actor string) (*sqlite3.SQLiteConn, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pilotTraceSetActor(actor)
	conn, err := (&sqlite3.SQLiteDriver{}).Open(uri)
	if err != nil {
		return nil, err
	}
	if err := conn.(driver.Pinger).Ping(ctx); err != nil {
		_ = pilotTraceClose(conn.(*sqlite3.SQLiteConn), actor)
		return nil, err
	}
	return conn.(*sqlite3.SQLiteConn), nil
}

func pilotTraceQueryOne(ctx context.Context, conn *sqlite3.SQLiteConn, actor, query string) error {
	value, err := pilotTraceQueryInt(ctx, conn, actor, query)
	if err != nil {
		return err
	}
	if value != 1 {
		return fmt.Errorf("SELECT 1 returned %d", value)
	}
	return nil
}

func pilotTraceQueryInt(ctx context.Context, conn *sqlite3.SQLiteConn, actor, query string) (int64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pilotTraceSetActor(actor)
	rows, err := conn.QueryContext(ctx, query, nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	values := make([]driver.Value, len(rows.Columns()))
	if err := rows.Next(values); err != nil {
		return 0, err
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("query returned %d columns, want 1", len(values))
	}
	value, ok := values[0].(int64)
	if !ok {
		return 0, fmt.Errorf("query returned %#v, want integer", values[0])
	}
	return value, nil
}

func pilotTraceQueryText(ctx context.Context, conn *sqlite3.SQLiteConn, actor, query string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pilotTraceSetActor(actor)
	rows, err := conn.QueryContext(ctx, query, nil)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	values := make([]driver.Value, len(rows.Columns()))
	if err := rows.Next(values); err != nil {
		return "", err
	}
	if len(values) != 1 {
		return "", fmt.Errorf("query returned %d columns, want 1", len(values))
	}
	value, ok := values[0].(string)
	if !ok {
		return "", fmt.Errorf("query returned %#v, want text", values[0])
	}
	return value, nil
}

func pilotTraceExec(ctx context.Context, conn *sqlite3.SQLiteConn, actor, query string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pilotTraceSetActor(actor)
	_, err := conn.ExecContext(ctx, query, nil)
	return err
}

func pilotTraceClose(conn *sqlite3.SQLiteConn, actor string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pilotTraceSetActor(actor)
	return conn.Close()
}

func pilotTraceSnapshot(t *testing.T) []pilotTraceEvent {
	t.Helper()
	if pilotTraceOverflowed() {
		t.Fatal("trace buffer overflowed; candidate status invalidated")
	}
	var events []pilotTraceEvent
	scanner := bufio.NewScanner(strings.NewReader(pilotTraceDump()))
	for scanner.Scan() {
		var event pilotTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode tracer event %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func eventSubset(events []pilotTraceEvent, keep func(pilotTraceEvent) bool) []pilotTraceEvent {
	var out []pilotTraceEvent
	for _, event := range events {
		if keep(event) {
			out = append(out, event)
		}
	}
	return out
}

func firstTraceEvent(events []pilotTraceEvent, match func(pilotTraceEvent) bool) *pilotTraceEvent {
	for i := range events {
		if match(events[i]) {
			return &events[i]
		}
	}
	return nil
}

func pilotTraceHarnessHashes() (map[string]string, string, error) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, "", fmt.Errorf("runtime.Caller did not find tracer test source")
	}
	dir := filepath.Dir(sourceFile)
	paths := []string{"pilot_vfs_trace_linux.c", "pilot_vfs_trace_linux.go", "pilot_vfs_trace_candidate_test.go", "pilot_vfs_missing_scenarios_test.go", "pilot_golden_manifest_test.go", "testdata/pilot_vfs_eexist_interposer.c"}
	hashes := make(map[string]string, len(paths))
	combined := sha256.New()
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(data)
		hashes[rel] = hex.EncodeToString(sum[:])
		_, _ = combined.Write([]byte(rel))
		_, _ = combined.Write(sum[:])
	}
	return hashes, hex.EncodeToString(combined.Sum(nil)), nil
}

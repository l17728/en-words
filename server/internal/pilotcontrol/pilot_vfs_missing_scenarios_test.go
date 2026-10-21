//go:build cgo && pilot_e2e && linux

package pilotcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const pagerCrashScenarioID = "PAGER-HOT-ROLLBACK-EXCLUSIVE"
const eexistScenarioID = "VFSFLAGS-SIDECAR-EEXIST-REOPEN"

type pilotEEXISTFrame struct {
	Version        int    `json:"version"`
	ScenarioID     string `json:"scenario_id"`
	Seq            uint64 `json:"seq"`
	Type           string `json:"type"`
	TargetToken    string `json:"target_token"`
	Flags          int    `json:"flags"`
	Result         int    `json:"result"`
	Errno          int    `json:"errno"`
	OpenCalls      uint64 `json:"open_calls"`
	Open64Calls    uint64 `json:"open64_calls"`
	OpenatCalls    uint64 `json:"openat_calls"`
	Openat64Calls  uint64 `json:"openat64_calls"`
	ExactPathCalls uint64 `json:"exact_path_attempts"`
	Qualifying     bool   `json:"qualifying_exclusive_create"`
	SymbolsOK      bool   `json:"symbols_ok"`
}

func pilotWaitFD(fd int, events int16, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return context.DeadlineExceeded
		}
		ms := int(left / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		pollEvents := events
		if events&unix.POLLIN != 0 {
			pollEvents |= unix.POLLRDHUP
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: pollEvents}}
		n, err := unix.Poll(fds, ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return context.DeadlineExceeded
		}
		if fds[0].Revents&events != 0 {
			// POLLIN can indicate queued data, a zero-length record, or EOF on a
			// closed SOCK_SEQPACKET peer. Preserve frames; with an empty peek,
			// distinguish a live-peer empty record from EOF using RDHUP/HUP.
			if events&unix.POLLIN != 0 {
				var peek [1]byte
				n, _, _, _, peekErr := unix.Recvmsg(fd, peek[:], nil, unix.MSG_PEEK|unix.MSG_DONTWAIT)
				if peekErr == nil && n == 0 && fds[0].Revents&(unix.POLLRDHUP|unix.POLLHUP) != 0 {
					return io.EOF
				}
				if peekErr != nil && !errors.Is(peekErr, unix.EAGAIN) && !errors.Is(peekErr, unix.EWOULDBLOCK) {
					return peekErr
				}
			}
			return nil
		}
		if fds[0].Revents&(unix.POLLRDHUP|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
		return fmt.Errorf("control descriptor poll events %#x", fds[0].Revents)
	}
}

func pilotSeqpacketZeroReadError(n int) error {
	if n != 0 {
		return nil
	}
	return fmt.Errorf("zero-length SOCK_SEQPACKET control record")
}

func pilotEEXISTQualifies(flags int) bool {
	return flags&unix.O_CREAT != 0 && flags&unix.O_EXCL != 0 && (flags&unix.O_TMPFILE) != unix.O_TMPFILE
}

func pilotEEXISTDecodeFrame(data []byte, expectedToken string, previousSeq uint64) (pilotEEXISTFrame, error) {
	var frame pilotEEXISTFrame
	if len(data) == 0 || len(data) > 4096 {
		return frame, fmt.Errorf("EEXIST frame size outside 1..4096 bytes")
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return frame, fmt.Errorf("decode EEXIST frame: %w", err)
	}
	if frame.Version != 1 || frame.ScenarioID != eexistScenarioID || frame.Seq == 0 || frame.Type == "" {
		return frame, fmt.Errorf("EEXIST frame schema mismatch")
	}
	if expectedToken != "" && frame.TargetToken != expectedToken {
		return frame, fmt.Errorf("EEXIST target token mismatch")
	}
	if previousSeq != 0 && frame.Seq != previousSeq+1 {
		return frame, fmt.Errorf("EEXIST frame sequence is not contiguous")
	}
	return frame, nil
}

type pilotEEXISTResultAudit struct {
	authorized bool
	injected   bool
	retryOK    bool
}

func (a *pilotEEXISTResultAudit) observe(frame pilotEEXISTFrame) error {
	switch frame.Type {
	case "INJECTED_EEXIST_RESULT":
		if !a.authorized || a.injected || frame.Result != -1 || frame.Errno != int(unix.EEXIST) || !frame.Qualifying {
			return fmt.Errorf("duplicate, unauthorized, or invalid injected EEXIST result")
		}
		a.injected = true
	case "SYSCALL_RESULT":
		if a.injected && frame.Result >= 0 {
			a.retryOK = true
		}
	}
	return nil
}

func (a *pilotEEXISTResultAudit) complete() error {
	if !a.injected {
		return fmt.Errorf("injected EEXIST result missing")
	}
	if !a.retryOK {
		return fmt.Errorf("successful retry result missing after injected EEXIST")
	}
	return nil
}

func TestPilotEEXISTFrameValidation(t *testing.T) {
	valid := pilotEEXISTFrame{Version: 1, ScenarioID: eexistScenarioID, Seq: 4, Type: "EXCLUSIVE_CREATE_OBSERVED", TargetToken: "expected"}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pilotEEXISTDecodeFrame(data, "expected", 3); err != nil {
		t.Fatalf("valid contiguous EEXIST frame rejected: %v", err)
	}
	if _, err := pilotEEXISTDecodeFrame([]byte("not-json"), "expected", 3); err == nil {
		t.Fatal("malformed EEXIST frame accepted")
	}
	if _, err := pilotEEXISTDecodeFrame(data, "wrong-target", 3); err == nil {
		t.Fatal("wrong target token accepted")
	}
	if _, err := pilotEEXISTDecodeFrame(data, "expected", 2); err == nil {
		t.Fatal("non-contiguous sequence accepted")
	}
}

func TestPilotEEXISTResultAuditRejectsDuplicateAndMissingRetry(t *testing.T) {
	unauthorized := pilotEEXISTResultAudit{}
	if err := unauthorized.observe(pilotEEXISTFrame{Type: "INJECTED_EEXIST_RESULT", Result: -1, Errno: int(unix.EEXIST), Qualifying: true}); err == nil {
		t.Fatal("unauthorized EEXIST injection result accepted")
	}
	audit := pilotEEXISTResultAudit{authorized: true}
	injected := pilotEEXISTFrame{Type: "INJECTED_EEXIST_RESULT", Result: -1, Errno: int(unix.EEXIST), Qualifying: true}
	if err := audit.observe(injected); err != nil {
		t.Fatalf("first authorized injection rejected: %v", err)
	}
	if err := audit.observe(injected); err == nil {
		t.Fatal("duplicate injected EEXIST result accepted")
	}
	if err := audit.complete(); err == nil {
		t.Fatal("injection without a successful syscall retry was accepted")
	}
	if err := audit.observe(pilotEEXISTFrame{Type: "SYSCALL_RESULT", Result: 7}); err != nil {
		t.Fatalf("retry result rejected: %v", err)
	}
	if err := audit.complete(); err != nil {
		t.Fatalf("successful retry not accepted: %v", err)
	}
}

func TestPilotEEXISTControlReportsEOFAndDeadline(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])
	if err := unix.Shutdown(pair[1], unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	if err := pilotWaitFD(pair[0], unix.POLLIN, time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("EEXIST peer close error=%v, want EOF", err)
	}
	pair2, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair2[0])
	defer unix.Close(pair2[1])
	if err := pilotWaitFD(pair2[0], unix.POLLIN, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("EEXIST control timeout error=%v, want deadline exceeded", err)
	}
	pair3, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair3[0])
	defer unix.Close(pair3[1])
	if _, err := unix.Write(pair3[1], []byte("queued-frame")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Shutdown(pair3[1], unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	if err := pilotWaitFD(pair3[0], unix.POLLIN, time.Second); err != nil {
		t.Fatalf("queued frame followed by close reported as %v", err)
	}
	var queued [32]byte
	n, _, err := unix.Recvfrom(pair3[0], queued[:], 0)
	if err != nil || string(queued[:n]) != "queued-frame" {
		t.Fatalf("queued frame lost at peer close: n=%d data=%q err=%v", n, queued[:n], err)
	}
	if err := pilotWaitFD(pair3[0], unix.POLLIN, time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("peer close after queued frame error=%v, want EOF", err)
	}
	pair4, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair4[0])
	defer unix.Close(pair4[1])
	if err := unix.Sendmsg(pair4[1], nil, nil, nil, 0); err != nil {
		t.Fatalf("send zero-length SOCK_SEQPACKET record: %v", err)
	}
	if err := pilotWaitFD(pair4[0], unix.POLLIN, time.Second); err != nil {
		t.Fatalf("zero-length record readiness error=%v, want readable", err)
	}
	var empty [1]byte
	n, _, _, _, err = unix.Recvmsg(pair4[0], empty[:], nil, 0)
	if err != nil {
		t.Fatalf("receive zero-length record: %v", err)
	}
	if err := pilotSeqpacketZeroReadError(n); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("zero-length record classified as %v, want malformed record error", err)
	}
}

func pilotEEXISTCanonicalTarget(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("canonicalize target parent: %w", err)
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func TestPilotEEXISTExclusiveCreateQualification(t *testing.T) {
	cases := []struct {
		name  string
		flags int
		want  bool
	}{
		{"read-only", unix.O_RDONLY, false},
		{"create-only", unix.O_CREAT | unix.O_RDWR, false},
		{"exclusive-only", unix.O_EXCL | unix.O_RDWR, false},
		{"create-exclusive", unix.O_CREAT | unix.O_EXCL | unix.O_RDWR, true},
		{"tmpfile", unix.O_TMPFILE | unix.O_CREAT | unix.O_EXCL | unix.O_RDWR, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pilotEEXISTQualifies(tc.flags); got != tc.want {
				t.Fatalf("qualifies(%#x)=%t want %t", tc.flags, got, tc.want)
			}
		})
	}
}

func TestPilotEEXISTCanonicalTargetResolvesParentSymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err := pilotEEXISTCanonicalTarget(filepath.Join(alias, "db-wal"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realDir, "db-wal")
	if got != want {
		t.Fatalf("canonical target=%q want %q", got, want)
	}
}

func TestPilotVFSChildEEXIST(t *testing.T) {
	if os.Getenv("PILOT_VFS_EEXIST_CHILD") != "1" {
		return
	}
	tracePath := os.Getenv("PILOT_VFS_EEXIST_TRACE_PATH")
	if !filepath.IsAbs(tracePath) {
		t.Fatal("EEXIST trace path must be absolute")
	}
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatalf("register EEXIST child trace VFS: %v", err)
	}
	defer func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("unregister EEXIST trace VFS: %v", err)
		}
	}()
	runID := os.Getenv("PILOT_VFS_EEXIST_RUN_ID")
	sourceID := os.Getenv("PILOT_VFS_EEXIST_SOURCE_ID")
	uri := os.Getenv("PILOT_VFS_EEXIST_DB_URI")
	if runID == "" || sourceID != pinnedSQLiteSourceID || uri == "" {
		t.Fatal("EEXIST child environment is incomplete")
	}
	pilotTraceReset()
	pilotTraceSetContext(runID, eexistScenarioID, eexistScenarioID, sourceID)
	pilotTraceSetActor("eexist-child")
	conn, err := pilotTraceOpen(context.Background(), uri, "eexist-child")
	if err != nil {
		_ = pilotTraceWriteChildTrace(tracePath)
		t.Fatalf("open WAL candidate database: %v", err)
	}
	if err := pilotTraceExec(context.Background(), conn, "eexist-child", "PRAGMA journal_mode=WAL; BEGIN; INSERT INTO t(v) VALUES('retry'); COMMIT"); err != nil {
		_ = pilotTraceClose(conn, "eexist-child")
		_ = pilotTraceWriteChildTrace(tracePath)
		t.Fatalf("WAL transaction after EEXIST injection: %v", err)
	}
	if err := pilotTraceClose(conn, "eexist-child"); err != nil {
		t.Fatalf("close EEXIST child connection: %v", err)
	}
	if err := pilotTraceWriteChildTrace(tracePath); err != nil {
		t.Fatalf("persist EEXIST child callback trace: %v", err)
	}
}

func pilotTraceSidecarEEXIST(t *testing.T, root, runID, sourceID, artifactDir string) (pilotTraceScenario, string, bool) {
	t.Helper()
	const command = "go test binary -test.run=^TestPilotVFSChildEEXIST$"
	dbPath, uri := pilotTraceCreateDBFile(t, root, eexistScenarioID)
	setup, err := pilotTraceOpen(context.Background(), uri, "eexist-setup")
	if err != nil {
		t.Fatalf("EEXIST setup open: %v", err)
	}
	if err := pilotTraceExec(context.Background(), setup, "eexist-setup", "CREATE TABLE t(v TEXT)"); err != nil {
		_ = pilotTraceClose(setup, "eexist-setup")
		t.Fatalf("EEXIST setup schema: %v", err)
	}
	if err := pilotTraceClose(setup, "eexist-setup"); err != nil {
		t.Fatalf("EEXIST setup close: %v", err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve interposer source")
	}
	interposerSource := filepath.Join(filepath.Dir(sourceFile), "testdata", "pilot_vfs_eexist_interposer.c")
	interposerPath := filepath.Join(artifactDir, "libpilot-vfs-eexist.so")
	cc := strings.Fields(os.Getenv("CC"))
	if len(cc) == 0 {
		cc = []string{"cc"}
	}
	args := append(append([]string(nil), cc[1:]...), "-std=c11", "-O2", "-fPIC", "-shared", interposerSource, "-o", interposerPath, "-ldl", "-pthread")
	build := exec.Command(cc[0], args...)
	var buildOut bytes.Buffer
	build.Stdout, build.Stderr = &buildOut, &buildOut
	if err := build.Run(); err != nil {
		buildLog := pilotTraceArtifactFromBytes(t, artifactDir, "eexist-interposer-build.log", buildOut.Bytes())
		return pilotTraceScenario{ID: eexistScenarioID, Status: "NOT RUN", Reason: "test interposer unavailable before child start: " + pilotTraceSanitizeReason(err.Error()), EvidenceArtifacts: []pilotTraceChildArtifact{buildLog}}, "", true
	}
	soBytes, err := os.ReadFile(interposerPath)
	if err != nil || len(soBytes) == 0 {
		t.Fatalf("compiled EEXIST interposer missing: %v", err)
	}
	soHash := sha256.Sum256(soBytes)
	soArtifact := pilotTraceChildArtifact{File: filepath.Base(interposerPath), SHA256: hex.EncodeToString(soHash[:]), Bytes: len(soBytes)}
	targetPath, err := pilotEEXISTCanonicalTarget(dbPath + "-wal")
	if err != nil {
		t.Fatalf("canonicalize EEXIST target path: %v", err)
	}
	tokenHash := sha256.Sum256([]byte(targetPath))
	token := hex.EncodeToString(tokenHash[:])
	tracePath := filepath.Join(root, "eexist-child.raw.jsonl")
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("create EEXIST control socket: %v", err)
	}
	parentFD, childFD := pair[0], pair[1]
	childControl := os.NewFile(uintptr(childFD), "pilot-eexist-control")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPilotVFSChildEEXIST$")
	cmd.ExtraFiles = []*os.File{childControl}
	cmd.Env = pilotEEXISTChildEnvironment(runID, sourceID, uri, interposerPath, targetPath, token, tracePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		_ = childControl.Close()
		_ = unix.Close(parentFD)
		return pilotTraceScenario{ID: eexistScenarioID, Status: "NOT RUN", Reason: "EEXIST child could not start: " + pilotTraceSanitizeReason(err.Error()), EvidenceArtifacts: []pilotTraceChildArtifact{soArtifact}, ChildExecutions: []pilotTraceChildExecution{pilotTraceNotStartedChild(command, "eexist-child")}}, "", true
	}
	childFDClosed := childControl.Close() == nil
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	var waitErr error
	reaped := false
	killed := false
	timedOut := false
	parentFDClosed := false
	var exact, qualifyingSeen, injectionAuthorized bool
	audit := pilotEEXISTResultAudit{}
	var attempts []map[string]any
	var previousSeq uint64
	finalized := false
	defer func() {
		cleanupErrors := make([]string, 0, 3)
		if !reaped {
			if cmd.Process.Kill() == nil {
				killed = true
			}
			select {
			case waitErr = <-wait:
				reaped = true
			case <-time.After(5 * time.Second):
				timedOut = true
				cleanupErrors = append(cleanupErrors, "child reap deadline exceeded")
			}
		}
		if !parentFDClosed {
			if closeErr := unix.Close(parentFD); closeErr == nil {
				parentFDClosed = true
			} else {
				cleanupErrors = append(cleanupErrors, "close parent control fd: "+closeErr.Error())
			}
		}
		if !finalized || len(cleanupErrors) != 0 {
			persist := func(name string, data []byte) {
				if persistErr := pilotTraceWriteNewArtifact(filepath.Join(artifactDir, name), data); persistErr != nil {
					cleanupErrors = append(cleanupErrors, "persist "+name+": "+persistErr.Error())
				}
			}
			persist("eexist-child.stdout.diagnostic", stdout.Bytes())
			persist("eexist-child.stderr.diagnostic", stderr.Bytes())
			if b, readErr := os.ReadFile(tracePath); readErr == nil {
				persist("eexist-child.raw.diagnostic.jsonl", b)
			}
			diag, _ := json.MarshalIndent(map[string]any{"scenario_id": eexistScenarioID, "exact_path_seen": exact, "qualifying_create_seen": qualifyingSeen, "injection_authorized": injectionAuthorized, "injected_eexist_observed": audit.injected, "retry_success": audit.retryOK, "timed_out": timedOut, "killed": killed, "reaped": reaped, "child_wait_error": fmt.Sprint(waitErr), "parent_control_fd_closed": parentFDClosed, "attempts": attempts, "cleanup_errors": cleanupErrors}, "", "  ")
			persist("eexist-parent.diagnostic.json", append(diag, '\n'))
		}
		if len(cleanupErrors) != 0 {
			t.Errorf("EEXIST child cleanup failed: %v", cleanupErrors)
		}
	}()
	killFail := func(message string) {
		if !reaped {
			if cmd.Process.Kill() == nil {
				killed = true
			}
			select {
			case waitErr = <-wait:
				reaped = true
			case <-time.After(5 * time.Second):
				timedOut = true
			}
		}
		t.Fatalf("%s (child reaped=%t wait=%v killed=%t timed_out=%t)", message, reaped, waitErr, killed, timedOut)
	}
	receive := func(timeout time.Duration) (pilotEEXISTFrame, error) {
		var frame pilotEEXISTFrame
		if err := pilotWaitFD(parentFD, unix.POLLIN, timeout); err != nil {
			return frame, err
		}
		buf := make([]byte, 4097)
		n, _, flags, _, err := unix.Recvmsg(parentFD, buf, nil, 0)
		if err != nil {
			return frame, err
		}
		if emptyErr := pilotSeqpacketZeroReadError(n); emptyErr != nil {
			return frame, emptyErr
		}
		if flags&unix.MSG_TRUNC != 0 || n >= len(buf) {
			return frame, fmt.Errorf("oversized EEXIST frame")
		}
		return pilotEEXISTDecodeFrame(buf[:n], token, previousSeq)
	}
	sendAck := func(seq uint64, typ string) error {
		return pilotPagerSend(parentFD, struct {
			Version    int    `json:"version"`
			ScenarioID string `json:"scenario_id"`
			Seq        uint64 `json:"seq"`
			Type       string `json:"type"`
		}{1, eexistScenarioID, seq, typ})
	}
	ready, err := receive(10 * time.Second)
	if errors.Is(err, context.DeadlineExceeded) {
		timedOut = true
	}
	if err == nil && ready.Type == "UNAVAILABLE" && ready.Seq == 1 && ready.TargetToken == token && !ready.SymbolsOK {
		select {
		case waitErr = <-wait:
			reaped = true
		case <-time.After(5 * time.Second):
			killFail("unavailable EEXIST interposer child did not exit within cleanup bound")
		}
		parentFDClosed = unix.Close(parentFD) == nil
		traceData, readErr := os.ReadFile(tracePath)
		if readErr != nil {
			t.Fatalf("read unavailable-interposer child trace: %v", readErr)
		}
		var events []pilotTraceEvent
		if len(traceData) != 0 {
			events, err = pilotTraceDecodeEvents(traceData)
			if err != nil {
				t.Fatalf("decode unavailable-interposer child trace: %v", err)
			}
		}
		for i := range events {
			events[i].Process = "eexist-child"
		}
		child := pilotTraceChildRecord(artifactDir, eexistScenarioID, "eexist-child", command, cmd, &stdout, &stderr, len(traceData) > 0, false, false, reaped, "INTERPOSER_UNAVAILABLE_BEFORE_EXCLUSIVE_CREATE", fmt.Sprintf("interposer unavailable (errno=%d)", ready.Errno))
		child.ChildControlFDClosed = childFDClosed && reaped
		child.ParentControlFDClosed = &parentFDClosed
		child.ExecutionStatus = "NOT RUN"
		finalized = true
		return pilotTraceScenario{ID: eexistScenarioID, Status: "NOT RUN", Reason: fmt.Sprintf("interposer failed closed before any injection (errno=%d)", ready.Errno), Events: events, ChildExecutions: []pilotTraceChildExecution{child}, EvidenceArtifacts: []pilotTraceChildArtifact{soArtifact}}, string(traceData), true
	}
	if err != nil || ready.Type != "READY" || !ready.SymbolsOK || ready.TargetToken != token || ready.Seq != 1 {
		killFail(fmt.Sprintf("EEXIST interposer READY failed: frame=%+v error=%v", ready, err))
	}
	previousSeq = ready.Seq
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
loop:
	for {
		select {
		case waitErr = <-wait:
			reaped = true
		default:
		}
		frame, readErr := receive(250 * time.Millisecond)
		if readErr == nil {
			if frame.Seq != previousSeq+1 || frame.TargetToken != token || (!frame.SymbolsOK && frame.Type != "UNAVAILABLE") {
				killFail(fmt.Sprintf("EEXIST control sequence/token/symbol mismatch: %+v previous=%d", frame, previousSeq))
			}
			previousSeq = frame.Seq
			switch frame.Type {
			case "EXCLUSIVE_CREATE_OBSERVED":
				exact = true
				wantQualifying := pilotEEXISTQualifies(frame.Flags)
				if frame.Qualifying != wantQualifying {
					killFail(fmt.Sprintf("exact WAL path qualifying flag mismatch: %+v", frame))
				}
				ack := "CONTINUE"
				if frame.Qualifying && !injectionAuthorized {
					fd, openErr := unix.Open(targetPath, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC, 0600)
					if openErr != nil {
						killFail("could not establish EEXIST collision: " + openErr.Error())
					}
					if closeErr := unix.Close(fd); closeErr != nil {
						killFail("close collision WAL seed: " + closeErr.Error())
					}
					ack, injectionAuthorized, qualifyingSeen = "INJECT_EEXIST", true, true
					audit.authorized = true
				}
				if frame.Qualifying {
					qualifyingSeen = true
				}
				if err := sendAck(frame.Seq, ack); err != nil {
					killFail("acknowledge exact WAL path attempt: " + err.Error())
				}
				attempts = append(attempts, map[string]any{"seq": frame.Seq, "flags": frame.Flags, "ack": ack, "exact_path_attempts": frame.ExactPathCalls, "wrapper_counts": map[string]uint64{"open": frame.OpenCalls, "open64": frame.Open64Calls, "openat": frame.OpenatCalls, "openat64": frame.Openat64Calls}})
			case "UNAVAILABLE":
				killFail(fmt.Sprintf("EEXIST interposer unavailable: errno=%d symbols_ok=%t", frame.Errno, frame.SymbolsOK))
			case "INJECTED_EEXIST_RESULT":
				attempts = append(attempts, map[string]any{"seq": frame.Seq, "type": frame.Type, "flags": frame.Flags, "result": frame.Result, "errno": frame.Errno})
				if err := audit.observe(frame); err != nil {
					killFail(err.Error())
				}
			case "SYSCALL_RESULT":
				attempts = append(attempts, map[string]any{"seq": frame.Seq, "flags": frame.Flags, "result": frame.Result, "errno": frame.Errno})
				if err := audit.observe(frame); err != nil {
					killFail(err.Error())
				}
			default:
				killFail("unexpected EEXIST control frame: " + frame.Type)
			}
		} else if errors.Is(readErr, io.EOF) {
			if reaped {
				break loop
			}
		} else if !errors.Is(readErr, context.DeadlineExceeded) {
			killFail("read EEXIST control frame: " + readErr.Error())
		}
		select {
		case <-deadline.C:
			timedOut = true
			killFail("EEXIST child exceeded protocol deadline")
		default:
		}
		if reaped && readErr == nil {
			continue
		}
		if reaped && errors.Is(readErr, context.DeadlineExceeded) {
			continue
		}
	}
	parentFDClosed = unix.Close(parentFD) == nil
	if !reaped {
		killFail("EEXIST child was not reaped")
	}
	if waitErr != nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("EEXIST child failed: wait=%v exit=%v stderr=%s", waitErr, cmd.ProcessState, stderr.String())
	}
	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read EEXIST callback trace: %v", err)
	}
	events, err := pilotTraceDecodeEvents(traceData)
	if err != nil {
		t.Fatalf("decode EEXIST callback trace: %v", err)
	}
	for i := range events {
		events[i].Process = "eexist-child"
	}
	walOpens := eventSubset(events, func(e pilotTraceEvent) bool { return e.Callback == "xOpen" && e.Role == "WAL" && e.RC == 0 })
	if !exact || !qualifyingSeen {
		terminal, reason := "CHILD_EXIT_NO_EXACT_PATH_ATTEMPT", "child completed without an exact temporary WAL path attempt"
		if exact {
			terminal, reason = "CHILD_EXIT_NO_QUALIFYING_EXCLUSIVE_CREATE", "exact WAL path was observed, but no O_CREAT|O_EXCL attempt occurred"
		}
		child := pilotTraceChildRecord(artifactDir, eexistScenarioID, "eexist-child", command, cmd, &stdout, &stderr, len(traceData) > 0, timedOut, killed, reaped, terminal, "")
		child.ChildControlFDClosed = childFDClosed && reaped
		child.ParentControlFDClosed = &parentFDClosed
		child.ExecutionStatus = "NOT RUN"
		finalized = true
		return pilotTraceScenario{ID: eexistScenarioID, Status: "NOT RUN", Reason: reason, Events: events, ChildExecutions: []pilotTraceChildExecution{child}, EvidenceArtifacts: []pilotTraceChildArtifact{soArtifact}}, string(traceData), true
	}
	if err := audit.complete(); err != nil || !injectionAuthorized || len(walOpens) == 0 {
		t.Fatalf("EEXIST retry incomplete: authorized=%t injected=%t retry_success=%t wal_open=%d attempts=%v audit_error=%v", injectionAuthorized, audit.injected, audit.retryOK, len(walOpens), attempts, err)
	}
	child := pilotTraceChildRecord(artifactDir, eexistScenarioID, "eexist-child", command, cmd, &stdout, &stderr, len(traceData) > 0, timedOut, killed, reaped, "EEXIST_INJECTED_AND_WAL_OPEN_RETRIED", "")
	child.ChildControlFDClosed = childFDClosed && reaped
	child.ParentControlFDClosed = &parentFDClosed
	child.ExecutionStatus = "CANDIDATE"
	finalized = true
	return pilotTraceScenario{ID: eexistScenarioID, Status: "CANDIDATE", Events: events, ChildExecutions: []pilotTraceChildExecution{child}, EvidenceArtifacts: []pilotTraceChildArtifact{soArtifact}, OracleDetails: map[string]any{"target_token": token, "exclusive_create_attempts": attempts, "injected_eexist_count": 1, "retry_success": audit.retryOK, "wal_open_count": len(walOpens), "interposer_sha256": soArtifact.SHA256, "scope": "test-only syscall interposition and host unix VFS behavior; not private VFS or syscall policy acceptance"}}, string(traceData), true
}

func pilotEEXISTChildEnvironment(runID, sourceID, uri, preload, target, token, tracePath string) []string {
	filtered := make([]string, 0, len(os.Environ())+10)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PILOT_VFS_EEXIST_") && !strings.HasPrefix(entry, "LD_PRELOAD=") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, "LD_PRELOAD="+preload, "PILOT_VFS_EEXIST_CHILD=1", "PILOT_VFS_EEXIST_CONTROL_FD=3", "PILOT_VFS_EEXIST_TARGET_PATH="+target, "PILOT_VFS_EEXIST_TARGET_TOKEN="+token, "PILOT_VFS_EEXIST_SCENARIO_ID="+eexistScenarioID, "PILOT_VFS_EEXIST_TRACE_PATH="+tracePath, "PILOT_VFS_EEXIST_RUN_ID="+runID, "PILOT_VFS_EEXIST_SOURCE_ID="+sourceID, "PILOT_VFS_EEXIST_DB_URI="+uri)
}

type pilotPagerControlFrame struct {
	Version          int    `json:"version"`
	ScenarioID       string `json:"scenario_id"`
	Seq              uint64 `json:"seq"`
	Type             string `json:"type"`
	HandleToken      int    `json:"handle_token"`
	Offset           int64  `json:"offset"`
	Size             int    `json:"size"`
	DelegateRC       int    `json:"delegate_rc"`
	CallbackReturned bool   `json:"callback_returned"`
}

func pilotPagerSend(fd int, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return fmt.Errorf("pager barrier frame too large: %d", len(data))
	}
	if err := pilotWaitFD(fd, unix.POLLOUT, 10*time.Second); err != nil {
		return fmt.Errorf("pager control send readiness: %w", err)
	}
	n, err := unix.SendmsgN(fd, data, nil, nil, unix.MSG_NOSIGNAL)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("pager barrier frame sent %d bytes, want %d", n, len(data))
	}
	return nil
}

func pilotPagerReceive(fd int, timeout time.Duration) (pilotPagerControlFrame, error) {
	var frame pilotPagerControlFrame
	if err := pilotWaitFD(fd, unix.POLLIN, timeout); err != nil {
		return frame, err
	}
	buf := make([]byte, 4097)
	n, _, flags, _, err := unix.Recvmsg(fd, buf, nil, 0)
	if err != nil {
		return frame, err
	}
	if emptyErr := pilotSeqpacketZeroReadError(n); emptyErr != nil {
		return frame, emptyErr
	}
	if flags&unix.MSG_TRUNC != 0 || n == len(buf) {
		return frame, fmt.Errorf("invalid or oversized pager barrier frame")
	}
	if err := json.Unmarshal(buf[:n], &frame); err != nil {
		return frame, fmt.Errorf("decode pager barrier frame: %w", err)
	}
	if frame.Version != 1 || frame.ScenarioID != pagerCrashScenarioID || frame.Seq == 0 || frame.Type == "" {
		return frame, fmt.Errorf("pager barrier frame identity/schema invalid")
	}
	return frame, nil
}

func pilotPagerAck(fd int, seq uint64, typ string) error {
	return pilotPagerSend(fd, struct {
		Version    int    `json:"version"`
		ScenarioID string `json:"scenario_id"`
		Seq        uint64 `json:"seq"`
		Type       string `json:"type"`
	}{Version: 1, ScenarioID: pagerCrashScenarioID, Seq: seq, Type: typ})
}

func TestPilotVFSChildPagerCrash(t *testing.T) {
	if os.Getenv("PILOT_VFS_PAGER_CRASH_CHILD") != "1" {
		return
	}
	control := os.NewFile(3, "pilot-pager-crash-control")
	trace := os.NewFile(4, "pilot-pager-crash-trace")
	if control == nil || trace == nil {
		t.Fatal("pager crash child missing inherited control/trace descriptors")
	}
	defer control.Close()
	defer trace.Close()
	if err := pilotTraceRegister("unix"); err != nil {
		t.Fatalf("register child trace VFS: %v", err)
	}
	defer func() {
		if err := pilotTraceUnregister(); err != nil {
			t.Errorf("unregister child trace VFS: %v", err)
		}
	}()
	runID := os.Getenv("PILOT_VFS_PAGER_RUN_ID")
	sourceID := os.Getenv("PILOT_VFS_PAGER_SOURCE_ID")
	uri := os.Getenv("PILOT_VFS_PAGER_DB_URI")
	if runID == "" || sourceID != pinnedSQLiteSourceID || uri == "" {
		t.Fatal("pager crash child environment is incomplete")
	}
	pilotTraceReset()
	pilotTraceSetContext(runID, pagerCrashScenarioID, pagerCrashScenarioID, sourceID)
	pilotTraceSetActor("crash-child")
	pilotTraceSetPagerBarrier(3, 4, pagerCrashScenarioID)
	conn, err := pilotTraceOpen(context.Background(), uri, "crash-child")
	if err != nil {
		t.Fatalf("pager crash child open: %v", err)
	}
	if err := pilotTraceExec(context.Background(), conn, "crash-child", "PRAGMA synchronous=FULL; BEGIN; UPDATE t SET v='new'; COMMIT"); err != nil {
		_ = pilotTraceClose(conn, "crash-child")
		t.Fatalf("pager crash child transaction: %v", err)
	}
	if err := pilotTraceClose(conn, "crash-child"); err != nil {
		t.Fatalf("pager crash child close: %v", err)
	}
	if err := pilotTraceFlushDurable(); err != nil {
		t.Fatalf("persist pager child trace: %v", err)
	}
}

func pilotTracePagerHotRollback(t *testing.T, root, runID, sourceID, artifactDir string) (pilotTraceScenario, string, bool) {
	t.Helper()
	const command = "go test binary -test.run=^TestPilotVFSChildPagerCrash$"
	dbPath, uri := pilotTraceCreateDBFile(t, root, pagerCrashScenarioID)
	setup, err := pilotTraceOpen(context.Background(), uri, "pager-setup")
	if err != nil {
		t.Fatalf("hot rollback setup open: %v", err)
	}
	if err := pilotTraceExec(context.Background(), setup, "pager-setup", "PRAGMA journal_mode=DELETE; CREATE TABLE t(v TEXT); INSERT INTO t VALUES('old')"); err != nil {
		_ = pilotTraceClose(setup, "pager-setup")
		t.Fatalf("hot rollback setup SQL: %v", err)
	}
	oldValue, err := pilotTraceQueryText(context.Background(), setup, "pager-setup", "SELECT v FROM t")
	if err != nil || oldValue != "old" {
		_ = pilotTraceClose(setup, "pager-setup")
		t.Fatalf("hot rollback setup value=%q err=%v", oldValue, err)
	}
	if err := pilotTraceClose(setup, "pager-setup"); err != nil {
		t.Fatalf("hot rollback setup close: %v", err)
	}
	tracePath := filepath.Join(root, "pager-hot-rollback-child.raw.jsonl")
	traceFile, err := os.OpenFile(tracePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("create pager child trace: %v", err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = traceFile.Close()
		t.Fatalf("create pager barrier socket: %v", err)
	}
	parentFD, childFD := pair[0], pair[1]
	childControl := os.NewFile(uintptr(childFD), "pilot-pager-child-control")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPilotVFSChildPagerCrash$")
	cmd.ExtraFiles = []*os.File{childControl, traceFile}
	cmd.Env = pilotPagerCrashChildEnvironment(runID, sourceID, uri)
	var childStdout, childStderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &childStdout, &childStderr
	if err := cmd.Start(); err != nil {
		_ = childControl.Close()
		_ = traceFile.Close()
		_ = unix.Close(parentFD)
		_ = unix.Close(childFD)
		return pilotTraceScenario{ID: pagerCrashScenarioID, Status: "NOT RUN", Reason: "pager crash child process could not start: " + pilotTraceSanitizeReason(err.Error()), ChildExecutions: []pilotTraceChildExecution{pilotTraceNotStartedChild(command, "crash-child")}}, "", true
	}
	childControlClosed := childControl.Close() == nil
	childTraceParentClosed := traceFile.Close() == nil
	if !childControlClosed || !childTraceParentClosed {
		t.Fatalf("close parent copies of child control/trace descriptors: control=%t trace=%t", childControlClosed, childTraceParentClosed)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	childFinished, childKilled, childTimedOut := false, false, false
	var waitErr error
	waitChild := func(timeout time.Duration) bool {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case waitErr = <-waitDone:
			childFinished = true
			return true
		case <-timer.C:
			return false
		}
	}
	stopChild := func(kill bool) bool {
		if childFinished {
			return true
		}
		if kill {
			if cmd.Process.Kill() == nil {
				childKilled = true
			}
		}
		if waitChild(5 * time.Second) {
			return true
		}
		childTimedOut = true
		return false
	}
	parentFDClosed := false
	finalized := false
	defer func() {
		cleanupErrors := make([]string, 0, 3)
		if !childFinished {
			if !stopChild(true) {
				cleanupErrors = append(cleanupErrors, "child reap deadline exceeded")
			}
		}
		if !parentFDClosed {
			if err := unix.Close(parentFD); err == nil {
				parentFDClosed = true
			} else {
				cleanupErrors = append(cleanupErrors, "close parent control fd: "+err.Error())
			}
		}
		if !childTraceParentClosed {
			if err := traceFile.Close(); err == nil {
				childTraceParentClosed = true
			} else {
				cleanupErrors = append(cleanupErrors, "close parent trace fd: "+err.Error())
			}
		}
		if !finalized || len(cleanupErrors) != 0 {
			persist := func(name string, data []byte) {
				if err := pilotTraceWriteNewArtifact(filepath.Join(artifactDir, name), data); err != nil {
					cleanupErrors = append(cleanupErrors, "persist "+name+": "+err.Error())
				}
			}
			persist("pager-hot-rollback-child.stdout.diagnostic", childStdout.Bytes())
			persist("pager-hot-rollback-child.stderr.diagnostic", childStderr.Bytes())
			if raw, err := os.ReadFile(tracePath); err == nil {
				persist("pager-hot-rollback-child.raw.diagnostic.jsonl", raw)
			} else {
				cleanupErrors = append(cleanupErrors, "read child trace: "+err.Error())
			}
			diag, _ := json.MarshalIndent(map[string]any{"scenario_id": pagerCrashScenarioID, "child_finished": childFinished, "child_timed_out": childTimedOut, "child_killed": childKilled, "child_reaped": childFinished, "child_wait_error": fmt.Sprint(waitErr), "child_control_fd_closed": childControlClosed && childFinished, "parent_control_fd_closed": parentFDClosed, "trace_fd_closed": childTraceParentClosed, "cleanup_errors": cleanupErrors}, "", "  ")
			persist("pager-hot-rollback-parent.diagnostic.json", append(diag, '\n'))
		}
		if len(cleanupErrors) != 0 {
			t.Errorf("pager crash child cleanup failed: %v", cleanupErrors)
		}
	}()
	closeParent := func() {
		if !parentFDClosed {
			parentFDClosed = unix.Close(parentFD) == nil
		}
	}
	readChildTrace := func() ([]byte, []pilotTraceEvent, error) {
		if !childTraceParentClosed {
			childTraceParentClosed = traceFile.Close() == nil
		}
		data, readErr := os.ReadFile(tracePath)
		if readErr != nil {
			return nil, nil, readErr
		}
		events, decodeErr := pilotTraceDecodeEvents(data)
		return data, events, decodeErr
	}
	notRun := func(reason string, terminal string) (pilotTraceScenario, string, bool) {
		if !childFinished {
			if !stopChild(false) {
				t.Fatalf("%s; pager child did not exit cleanly", reason)
			}
		}
		closeParent()
		data, events, traceErr := readChildTrace()
		if traceErr != nil {
			t.Fatalf("read partial pager child trace: %v", traceErr)
		}
		if !childTraceParentClosed || !parentFDClosed || !childControlClosed {
			t.Fatalf("pager NOT RUN cleanup incomplete: trace_fd_closed=%t parent_control_fd_closed=%t child_control_fd_closed=%t", childTraceParentClosed, parentFDClosed, childControlClosed)
		}
		for i := range events {
			events[i].Process = "crash-child"
		}
		child := pilotTraceChildRecord(artifactDir, pagerCrashScenarioID, "crash-child", command, cmd, &childStdout, &childStderr, len(data) > 0, false, false, true, terminal, "")
		child.ChildControlFDClosed = childControlClosed && childFinished
		child.ParentControlFDClosed = &parentFDClosed
		child.ExecutionStatus = "NOT RUN"
		finalized = true
		return pilotTraceScenario{ID: pagerCrashScenarioID, Status: "NOT RUN", Reason: reason, Events: events, ChildExecutions: []pilotTraceChildExecution{child}}, string(data), true
	}
	first, err := pilotPagerReceive(parentFD, 10*time.Second)
	if errors.Is(err, io.EOF) {
		if !waitChild(5*time.Second) || waitErr != nil {
			t.Fatalf("pager child exited before first barrier with error=%v", waitErr)
		}
		return notRun("child completed without a successful journal xSync barrier", "CHILD_EXIT_NO_JOURNAL_SYNC")
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			childTimedOut = true
		}
		if !stopChild(true) {
			t.Fatalf("pager child failed first barrier and could not be reaped: %v", err)
		}
		t.Fatalf("pager journal-sync barrier protocol failed: %v", err)
	}
	if first.Seq != 1 || first.Type != "JOURNAL_SYNC_OK" || first.DelegateRC != 0 || first.CallbackReturned || first.HandleToken <= 0 {
		_ = stopChild(true)
		t.Fatalf("invalid journal sync barrier: %+v", first)
	}
	if err := pilotPagerAck(parentFD, 1, "CONTINUE"); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			childTimedOut = true
		}
		_ = stopChild(true)
		t.Fatalf("acknowledge journal sync barrier: %v", err)
	}
	second, err := pilotPagerReceive(parentFD, 10*time.Second)
	if errors.Is(err, io.EOF) {
		if !waitChild(5*time.Second) || waitErr != nil {
			t.Fatalf("pager child exited between barriers with error=%v", waitErr)
		}
		return notRun("child completed without a qualifying main-page xWrite", "CHILD_EXIT_NO_MAIN_WRITE")
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			childTimedOut = true
		}
		_ = stopChild(true)
		t.Fatalf("pager main-write barrier protocol failed: %v", err)
	}
	if second.Seq != 2 || second.Type != "MAIN_WRITE_DELEGATED_OK" || second.DelegateRC != 0 || second.CallbackReturned || second.HandleToken <= 0 || second.Size <= 0 || second.Offset < 0 {
		_ = stopChild(true)
		t.Fatalf("invalid main write barrier: %+v", second)
	}
	childBytes, childEvents, err := readChildTrace()
	if err != nil || len(childBytes) == 0 {
		_ = stopChild(true)
		t.Fatalf("durable child trace missing before main-write acknowledgement: %v", err)
	}
	journalSync := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool { return e.Callback == "xSync" && e.Role == "MAIN_JOURNAL" && e.RC == 0 })
	mainWrite := firstTraceEvent(childEvents, func(e pilotTraceEvent) bool {
		return e.Callback == "xWrite" && e.Role == "MAIN_DB" && pilotTraceHandleID(e) == second.HandleToken && e.RC == 0
	})
	if journalSync == nil || mainWrite == nil || journalSync.Seq >= mainWrite.Seq || mainWrite.Arg0 != second.Size || int64(mainWrite.Arg1) != (second.Offset&0xffffffff) || int64(mainWrite.Arg2) != (second.Offset>>32) {
		_ = stopChild(true)
		t.Fatalf("durable barrier trace did not match ordered callback result: journal=%+v write=%+v frame=%+v", journalSync, mainWrite, second)
	}
	journalBytes, err := os.ReadFile(dbPath + "-journal")
	if err != nil || len(journalBytes) == 0 {
		_ = stopChild(true)
		t.Fatalf("rollback journal absent/empty after main write barrier: %v", err)
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil || len(dbBytes) == 0 {
		_ = stopChild(true)
		t.Fatalf("database snapshot unavailable at crash barrier: %v", err)
	}
	dbArtifact := pilotTraceArtifactFromBytes(t, artifactDir, "pager-hot-rollback-crash-db.bin", dbBytes)
	journalArtifact := pilotTraceArtifactFromBytes(t, artifactDir, "pager-hot-rollback-crash-journal.bin", journalBytes)
	if err := pilotPagerAck(parentFD, 2, "KILL_ACK"); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			childTimedOut = true
		}
		_ = stopChild(true)
		t.Fatalf("acknowledge crash barrier before SIGKILL: %v", err)
	}
	if cmd.Process.Kill() == nil {
		childKilled = true
	}
	if !waitChild(5 * time.Second) {
		t.Fatalf("pager crash child was not reaped after SIGKILL")
	}
	if !childKilled || waitErr == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != -1 {
		t.Fatalf("pager crash child did not terminate by SIGKILL: killed=%t wait=%v state=%v", childKilled, waitErr, cmd.ProcessState)
	}
	closeParent()
	childBytes, childEvents, err = readChildTrace()
	if err != nil {
		t.Fatalf("read final durable child trace: %v", err)
	}
	if !childTraceParentClosed || !parentFDClosed || !childControlClosed {
		t.Fatalf("pager candidate cleanup incomplete: trace_fd_closed=%t parent_control_fd_closed=%t child_control_fd_closed=%t", childTraceParentClosed, parentFDClosed, childControlClosed)
	}
	for i := range childEvents {
		childEvents[i].Process = "crash-child"
	}
	child := pilotTraceChildRecord(artifactDir, pagerCrashScenarioID, "crash-child", command, cmd, &childStdout, &childStderr, len(childBytes) > 0, childTimedOut, childKilled, childFinished, "SIGKILL_AFTER_MAIN_WRITE_DELEGATED_OK", "")
	child.ChildControlFDClosed = childControlClosed && childFinished
	child.ParentControlFDClosed = &parentFDClosed
	child.ExecutionStatus = "CANDIDATE"

	pilotTraceReset()
	pilotTraceSetContext(runID, pagerCrashScenarioID, pagerCrashScenarioID, sourceID)
	recovery, err := pilotTraceOpen(context.Background(), uri, "recovery-parent")
	if err != nil {
		t.Fatalf("reopen after hot rollback SIGKILL: %v", err)
	}
	recoveredValue, err := pilotTraceQueryText(context.Background(), recovery, "recovery-parent", "SELECT v FROM t")
	if err != nil {
		_ = pilotTraceClose(recovery, "recovery-parent")
		t.Fatalf("read recovered value after crash: %v", err)
	}
	if err := pilotTraceClose(recovery, "recovery-parent"); err != nil {
		t.Fatalf("close recovered database: %v", err)
	}
	if recoveredValue != oldValue {
		t.Fatalf("hot rollback recovery value=%q, want old committed value %q", recoveredValue, oldValue)
	}
	parentEvents := pilotTraceSnapshot(t)
	for i := range parentEvents {
		parentEvents[i].Process = "recovery-parent"
	}
	events := append(childEvents, parentEvents...)
	finalized = true
	return pilotTraceScenario{ID: pagerCrashScenarioID, Status: "CANDIDATE", Events: events, ChildExecutions: []pilotTraceChildExecution{child}, EvidenceArtifacts: []pilotTraceChildArtifact{dbArtifact, journalArtifact}, DurableTraceSeq: mainWrite.Seq, OracleDetails: map[string]any{
		"old_committed_value": oldValue, "recovered_value": recoveredValue,
		"journal_present_before_kill": true,
		"journal_sync_callback_seq":   journalSync.Seq, "main_write_callback_seq": mainWrite.Seq,
		"main_write_handle_id": second.HandleToken, "main_write_offset": second.Offset, "main_write_size": second.Size,
		"main_write_delegate_rc": second.DelegateRC, "main_write_callback_returned": second.CallbackReturned,
		"crash_db_snapshot": dbArtifact, "crash_journal_snapshot": journalArtifact,
		"durable_trace_stage": "fdatasync completed before MAIN_WRITE_DELEGATED_OK was sent",
		"scope":               "host unix VFS callback observation and rollback recovery only; not private VFS acceptance",
	}}, pilotTraceDump() + string(childBytes), true
}

func pilotPagerCrashChildEnvironment(runID, sourceID, uri string) []string {
	filtered := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PILOT_VFS_PAGER_") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered,
		"PILOT_VFS_PAGER_CRASH_CHILD=1",
		"PILOT_VFS_PAGER_RUN_ID="+runID,
		"PILOT_VFS_PAGER_SOURCE_ID="+sourceID,
		"PILOT_VFS_PAGER_DB_URI="+uri,
	)
}

func pilotTraceArtifactFromBytes(t *testing.T, artifactDir, name string, data []byte) pilotTraceChildArtifact {
	t.Helper()
	if err := pilotTraceWriteNewArtifact(filepath.Join(artifactDir, name), data); err != nil {
		t.Fatalf("persist immutable evidence %s: %v", name, err)
	}
	sum := sha256.Sum256(data)
	return pilotTraceChildArtifact{File: name, SHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}
}

func TestPilotPagerControlRejectsMalformedFrames(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])
	if _, err := unix.Write(pair[0], []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	if _, err := pilotPagerReceive(pair[1], time.Second); err == nil {
		t.Fatal("malformed pager barrier frame was accepted")
	}
}

func TestPilotPagerControlReportsDeadline(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer unix.Close(pair[1])
	if _, err := pilotPagerReceive(pair[0], time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pager control timeout error=%v, want deadline exceeded", err)
	}
}

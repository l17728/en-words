//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxOpenErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want error
	}{
		{name: "missing", in: unix.ENOENT, want: errVerifiedMemberInvalid},
		{name: "wrong_type", in: unix.ENOTDIR, want: errVerifiedPolicyMismatch},
		{name: "symlink", in: unix.ELOOP, want: errVerifiedPathInvalid},
		{name: "cross_mount", in: unix.EXDEV, want: errVerifiedPathInvalid},
		{name: "access_denied", in: unix.EACCES, want: errVerifiedIOFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := linuxOpenError(test.in); !errors.Is(got, test.want) {
				t.Fatalf("linuxOpenError(%v) = %v, want errors.Is(_, %v)", test.in, got, test.want)
			}
		})
	}
}

func TestVerifiedFilesystemErrorClassesRemainSpecificAndWrapped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "anchor", err: errVerifiedAnchorInvalid, want: "verified_anchor_invalid"},
		{name: "path", err: errVerifiedPathInvalid, want: "verified_path_invalid"},
		{name: "member", err: errVerifiedMemberInvalid, want: "verified_member_invalid"},
		{name: "policy", err: errVerifiedPolicyMismatch, want: "verified_policy_mismatch"},
		{name: "identity", err: errVerifiedIdentityChanged, want: "verified_identity_changed"},
		{name: "timeout", err: errVerifiedTimeout, want: "verified_timeout"},
		{name: "close", err: errVerifiedCloseUnknown, want: "verified_close_unknown"},
		{name: "configuration", err: errVerifiedConfigurationMissing, want: "verified_configuration_missing"},
		{name: "io fallback", err: errors.New("unclassified syscall error"), want: "verified_io_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifiedErrorClass(tc.err); got != tc.want {
				t.Fatalf("verifiedErrorClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}

	if classifyOpenError(nil) != nil {
		t.Fatal("nil open error was changed")
	}
	for _, original := range []error{errVerifiedPathInvalid, errVerifiedMemberInvalid, errVerifiedPolicyMismatch,
		errVerifiedIdentityChanged, errVerifiedIOFailed, errVerifiedTimeout, errVerifiedCloseUnknown} {
		if got := classifyOpenError(original); got != original {
			t.Fatalf("known error %v was wrapped or replaced: %v", original, got)
		}
	}
	unknown := errors.New("unexpected low-level error")
	classified := classifyOpenError(unknown)
	if !errors.Is(classified, errVerifiedIOFailed) || !errors.Is(classified, unknown) {
		t.Fatalf("unknown error lost category or cause: %v", classified)
	}
}

func newVerifiedTestAnchor(t *testing.T, path string) *AnchorHandle {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open test anchor: %v", err)
	}
	id, err := linuxVerifiedNative.inspect(uintptr(fd), verifiedDirectory)
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("inspect test anchor: %v", err)
	}
	if err := linuxVerifiedNative.checkFilesystem(uintptr(fd)); err != nil {
		_ = unix.Close(fd)
		t.Fatalf("BLOCKED: test anchor filesystem policy: %v", err)
	}
	anchor := &AnchorHandle{handle: uintptr(fd), identity: id, ops: linuxVerifiedNative}
	t.Cleanup(func() {
		anchor.mu.Lock()
		defer anchor.mu.Unlock()
		if !anchor.closed {
			if err := unix.Close(int(anchor.handle)); err != nil {
				t.Errorf("close borrowed test anchor: %v", err)
			}
			anchor.closed = true
		}
	})
	return anchor
}

func newVerifiedTestTree(t *testing.T) (string, string) {
	t.Helper()
	base := os.Getenv("PILOT_FS_TEST_ROOT")
	if base == "" {
		t.Fatal("BLOCKED: PILOT_FS_TEST_ROOT must point to a verified ext4/XFS directory")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatalf("prepare test base: %v", err)
	}
	tree, err := os.MkdirTemp(base, "pilot-root-")
	if err != nil {
		t.Fatalf("create test tree: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tree) })
	if err := os.Chmod(tree, 0700); err != nil {
		t.Fatalf("chmod anchor: %v", err)
	}
	epoch := "0123456789abcdef0123456789abcdef"
	runRoot := filepath.Join(tree, epoch)
	if err := os.Mkdir(runRoot, 0700); err != nil {
		t.Fatalf("create run root: %v", err)
	}
	return tree, runRoot
}

func assertVerifiedRootOpenError(t *testing.T, anchorPath, runRoot string, want error) {
	t.Helper()
	anchor := newVerifiedTestAnchor(t, anchorPath)
	epoch, err := ParseRunEpoch(filepath.Base(runRoot))
	if err != nil {
		t.Fatal(err)
	}
	if root, err := OpenVerifiedRoot(anchor, epoch); root != nil || !errors.Is(err, want) {
		t.Fatalf("OpenVerifiedRoot = (%v, %v), want errors.Is(_, %v)", root, err, want)
	}
}

func TestVerifiedRoot(t *testing.T) {
	t.Run("ROOT-01", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, err := ParseRunEpoch(filepath.Base(runRoot))
		if err != nil {
			t.Fatal(err)
		}
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		entries, err := root.ReadNames()
		if err != nil || len(entries) != 0 {
			t.Fatalf("ReadNames = %v, %v; want empty complete inventory", entries, err)
		}
		if err := root.VerifyIdentity(); err != nil {
			t.Fatalf("verify root: %v", err)
		}
		for _, closeErr := range []error{root.Close(), root.Close()} {
			if closeErr != nil {
				t.Fatalf("idempotent root close: %v", closeErr)
			}
		}
		if err := unix.Fstat(int(anchor.handle), &unix.Stat_t{}); err != nil {
			t.Fatalf("root close must not close borrowed anchor: %v", err)
		}
	})

	t.Run("ROOT-08/structured_log_fields", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		var output bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
		t.Cleanup(func() { slog.SetDefault(previous) })
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		if _, err := root.ReadNames(); err != nil {
			t.Fatalf("read names: %v", err)
		}
		if _, err := OpenVerifiedRoot(nil, epoch); !errors.Is(err, errVerifiedAnchorInvalid) {
			t.Fatalf("open with nil anchor = %v, want invalid anchor", err)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close root: %v", err)
		}
		logged := output.String()
		lines := strings.Split(strings.TrimSpace(logged), "\n")
		if len(lines) < 3 {
			t.Fatalf("got %d structured log lines, want open, enumerate, and failure: %s", len(lines), logged)
		}
		for _, line := range lines {
			for _, required := range []string{"trace_id=", "module=pilot.fs", "event=", "object_kind=", "result=", "duration_ms="} {
				if !strings.Contains(line, required) {
					t.Errorf("structured log line missing %q: %s", required, line)
				}
			}
			if !strings.Contains(line, "object_kind=directory") && !strings.Contains(line, "object_kind=root") {
				t.Errorf("structured log line has unknown object kind: %s", line)
			}
			if strings.Contains(line, "duration_ms=-") {
				t.Errorf("structured log line has a negative duration: %s", line)
			}
			if strings.Contains(line, "result=error") && !strings.Contains(line, "error_class=verified_anchor_invalid") {
				t.Errorf("failure event missing expected error class: %s", line)
			}
		}
		if !strings.Contains(logged, "event=root_open") || !strings.Contains(logged, "event=enumerate") || !strings.Contains(logged, "result=error") {
			t.Fatalf("structured logs missing expected operation outcomes: %s", logged)
		}
		if strings.Contains(logged, anchorPath) || strings.Contains(logged, runRoot) || strings.Contains(logged, "0123456789abcdef") {
			t.Fatalf("structured logs exposed filesystem path or run epoch: %s", logged)
		}
	})

	t.Run("ROOT-11/posix_readonly_fd", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "catalog.log"), nil, 0600); err != nil {
			t.Fatalf("create catalog member: %v", err)
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Errorf("close verified root: %v", err)
			}
		})
		member, err := root.OpenMember(CatalogLog)
		if err != nil {
			t.Fatalf("open catalog member: %v", err)
		}
		t.Cleanup(func() {
			if err := member.Close(); err != nil {
				t.Errorf("close catalog member: %v", err)
			}
		})
		flags, err := unix.FcntlInt(uintptr(member.child.handle), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
			t.Fatalf("member access flags = %#x, %v; want read-only", flags, err)
		}
	})

	t.Run("ROOT-02/extra_entry_open_root", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "unexpected"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		if root, err := OpenVerifiedRoot(anchor, epoch); root != nil || !errors.Is(err, errVerifiedPolicyMismatch) {
			t.Fatalf("OpenVerifiedRoot = (%v, %v), want policy mismatch", root, err)
		}
	})

	t.Run("ROOT-02/extra_entry_open_directory", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open initial valid root: %v", err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Errorf("close root after test: %v", err)
			}
		})
		if err := os.WriteFile(filepath.Join(runRoot, "unexpected"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if directory, err := root.OpenDirectory(CasesDir); directory != nil || !errors.Is(err, errVerifiedPolicyMismatch) {
			t.Fatalf("OpenDirectory = (%v, %v), want policy mismatch", directory, err)
		}
	})

	t.Run("ROOT-02/extra_entry_open_member", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open initial valid root: %v", err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Errorf("close root after test: %v", err)
			}
		})
		if err := os.WriteFile(filepath.Join(runRoot, "unexpected"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if member, err := root.OpenMember(CatalogLog); member != nil || !errors.Is(err, errVerifiedPolicyMismatch) {
			t.Fatalf("OpenMember = (%v, %v), want policy mismatch", member, err)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close poisoned root: %v", err)
		}
	})

	t.Run("ROOT-02/case_variant", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "Catalog.log"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		assertVerifiedRootOpenError(t, anchorPath, runRoot, errVerifiedPolicyMismatch)
	})

	t.Run("ROOT-02/symlink", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		outside := filepath.Join(anchorPath, "outside.log")
		if err := os.WriteFile(outside, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(runRoot, "catalog.log")); err != nil {
			t.Fatalf("create symlink fixture: %v", err)
		}
		assertVerifiedRootOpenError(t, anchorPath, runRoot, errVerifiedPathInvalid)
	})

	t.Run("ROOT-03/hardlink", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		original := filepath.Join(runRoot, "catalog.log")
		if err := os.WriteFile(original, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(original, filepath.Join(anchorPath, "second-link")); err != nil {
			t.Fatalf("create hardlink fixture: %v", err)
		}
		assertVerifiedRootOpenError(t, anchorPath, runRoot, errVerifiedPolicyMismatch)
	})

	t.Run("ROOT-03/wrong_type", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.Mkdir(filepath.Join(runRoot, "catalog.log"), 0700); err != nil {
			t.Fatal(err)
		}
		assertVerifiedRootOpenError(t, anchorPath, runRoot, errVerifiedPolicyMismatch)
	})

	t.Run("ROOT-03/wrong_mode", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		path := filepath.Join(runRoot, "catalog.log")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
		assertVerifiedRootOpenError(t, anchorPath, runRoot, errVerifiedPolicyMismatch)
	})

	t.Run("ROOT-13/stable_nameset", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		var err error
		for _, name := range []string{"catalog.bootstrap.lock", "catalog.log", "case-locks", "cases", "logs"} {
			path := filepath.Join(runRoot, name)
			if name == "case-locks" || name == "cases" || name == "logs" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.WriteFile(path, nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		counting := &enumerateCountOps{verifiedNative: linuxVerifiedNative}
		anchor.ops = counting
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Errorf("close verified root: %v", err)
			}
		})
		counting.mu.Lock()
		counting.target = root.handle
		counting.mu.Unlock()
		got, err := root.ReadNames()
		want := []RootEntry{RootCatalogBootstrapLock, RootCatalogLog, RootCaseLocksDir, RootCasesDir, RootLogsDir}
		if err != nil || !equalRootEntries(got, want) {
			t.Fatalf("ReadNames = %v, %v; want %v", got, err, want)
		}
		counting.mu.Lock()
		enumerations := counting.count
		counting.mu.Unlock()
		if enumerations != 2 {
			t.Fatalf("ReadNames root enumerations = %d, want exactly 2", enumerations)
		}
		for _, name := range []DirectoryName{CaseLocksDir, CasesDir, LogsDir} {
			directory, err := root.OpenDirectory(name)
			if err != nil {
				t.Fatalf("open allowlisted directory %d: %v", name, err)
			}
			if err := directory.VerifyIdentity(); err != nil {
				t.Fatalf("verify directory %d: %v", name, err)
			}
			if err := directory.Close(); err != nil {
				t.Fatalf("close directory %d: %v", name, err)
			}
		}
		for _, name := range []MemberName{CatalogBootstrapLock, CatalogLog} {
			member, err := root.OpenMember(name)
			if err != nil {
				t.Fatalf("open allowlisted member %d: %v", name, err)
			}
			if err := member.VerifyIdentity(); err != nil {
				t.Fatalf("verify member %d: %v", name, err)
			}
			if err := member.Close(); err != nil {
				t.Fatalf("close member %d: %v", name, err)
			}
		}
	})

	t.Run("ROOT-14/nil_anchor", func(t *testing.T) {
		if _, err := OpenVerifiedRoot(nil, RunEpoch("0123456789abcdef0123456789abcdef")); !errors.Is(err, errVerifiedAnchorInvalid) {
			t.Fatalf("nil anchor error = %v", err)
		}
	})

	t.Run("ROOT-14/bad_epoch", func(t *testing.T) {
		if _, err := ParseRunEpoch("../escape"); !errors.Is(err, errVerifiedPathInvalid) {
			t.Fatalf("bad epoch error = %v", err)
		}
	})

	t.Run("ROOT-14/zero_value_anchor", func(t *testing.T) {
		if _, err := OpenVerifiedRoot(&AnchorHandle{}, RunEpoch("0123456789abcdef0123456789abcdef")); !errors.Is(err, errVerifiedAnchorInvalid) {
			t.Fatalf("zero anchor error = %v", err)
		}
	})

	t.Run("ROOT-14/closed_anchor", func(t *testing.T) {
		closedAnchor := &AnchorHandle{closed: true}
		if _, err := OpenVerifiedRoot(closedAnchor, RunEpoch("0123456789abcdef0123456789abcdef")); !errors.Is(err, errVerifiedAnchorInvalid) {
			t.Fatalf("closed anchor error = %v", err)
		}
	})

	t.Run("ROOT-14/nil_root_open_directory", func(t *testing.T) {
		var nilRoot *VerifiedRoot
		if directory, err := nilRoot.OpenDirectory(CasesDir); directory != nil || !errors.Is(err, errVerifiedPathInvalid) {
			t.Fatalf("nil root OpenDirectory = (%v, %v)", directory, err)
		}
	})

	t.Run("ROOT-14/nil_root_open_member", func(t *testing.T) {
		var nilRoot *VerifiedRoot
		if member, err := nilRoot.OpenMember(CatalogLog); member != nil || !errors.Is(err, errVerifiedPathInvalid) {
			t.Fatalf("nil root OpenMember = (%v, %v)", member, err)
		}
	})

	t.Run("ROOT-14/no_content_read_method", func(t *testing.T) {
		fileType := reflect.TypeOf((*VerifiedFile)(nil))
		for index := 0; index < fileType.NumMethod(); index++ {
			name := fileType.Method(index).Name
			if name == "Read" || name == "ReadAt" || name == "Write" || name == "WriteAt" {
				t.Fatalf("VerifiedFile exposes content method %q", name)
			}
		}
	})

	t.Run("ROOT-14/unknown_enum", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		if directory, err := root.OpenDirectory(DirectoryName(255)); directory != nil || !errors.Is(err, errVerifiedMemberInvalid) {
			t.Fatalf("OpenDirectory(unknown) = (%v, %v)", directory, err)
		}
		if member, err := root.OpenMember(MemberName(255)); member != nil || !errors.Is(err, errVerifiedMemberInvalid) {
			t.Fatalf("OpenMember(unknown) = (%v, %v)", member, err)
		}
		if entries, err := root.ReadNames(); err != nil || len(entries) != 0 {
			t.Fatalf("ReadNames after invalid enum = (%v, %v), want usable empty root", entries, err)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close root after invalid enum: %v", err)
		}
	})

	t.Run("ROOT-14/missing_member", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		if member, err := root.OpenMember(CatalogBootstrapLock); member != nil || !errors.Is(err, errVerifiedMemberInvalid) {
			t.Fatalf("OpenMember(missing) = (%v, %v)", member, err)
		}
		if entries, err := root.ReadNames(); err != nil || len(entries) != 0 {
			t.Fatalf("ReadNames after missing member = (%v, %v), want usable empty root", entries, err)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close root after invalid inputs: %v", err)
		}
	})

	t.Run("ROOT-14/closed_member", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "catalog.log"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Errorf("close root after test: %v", err)
			}
		})
		member, err := root.OpenMember(CatalogLog)
		if err != nil {
			t.Fatalf("open member: %v", err)
		}
		if err := member.Close(); err != nil {
			t.Fatalf("close member: %v", err)
		}
		if err := member.VerifyIdentity(); !errors.Is(err, errVerifiedPathInvalid) {
			t.Fatalf("VerifyIdentity after member close = %v, want invalid handle", err)
		}
		if err := member.Close(); err != nil {
			t.Fatalf("repeated member close: %v", err)
		}
		if _, err := root.ReadNames(); err != nil {
			t.Fatalf("root unusable after member close: %v", err)
		}
	})

	t.Run("ROOT-07/close_unknown", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "catalog.log"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		faults := &closeFaultOps{verifiedNative: linuxVerifiedOps{}}
		faults.verifiedNative = linuxVerifiedOps{closeFile: faults.closeFile}
		anchor.ops = faults
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		member, err := root.OpenMember(CatalogLog)
		if err != nil {
			t.Fatalf("open member: %v", err)
		}
		faults.mu.Lock()
		faults.failClose = true
		faults.mu.Unlock()
		if err := member.Close(); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("member close = %v, want close unknown", err)
		}
		faults.mu.Lock()
		callsAfterFailure := faults.closeCalls
		faults.mu.Unlock()
		if err := root.Close(); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("root close = %v, want cached close unknown", err)
		}
		faults.mu.Lock()
		defer faults.mu.Unlock()
		if faults.closeCalls != callsAfterFailure {
			t.Fatalf("OS close retried after unknown result: before=%d after=%d", callsAfterFailure, faults.closeCalls)
		}
		// The injected failure leaves these real OS handles open; close them
		// directly after asserting the public API never retries an unknown close.
		_ = unix.Close(int(member.child.handle))
		_ = unix.Close(int(root.handle))
	})

	t.Run("ROOT-07/binding_close_unknown_stops_root_close", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		faults := &closeFaultOps{verifiedNative: linuxVerifiedOps{}}
		faults.verifiedNative = linuxVerifiedOps{closeFile: faults.closeFile}
		anchor.ops = faults
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		verifiedBootstrapOrphans.Lock()
		orphanStart := len(verifiedBootstrapOrphans.handles)
		verifiedBootstrapOrphans.Unlock()
		faults.mu.Lock()
		faults.failClose = true
		faults.mu.Unlock()
		if _, err := root.ReadNames(); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("ReadNames = %v, want close unknown", err)
		}
		faults.mu.Lock()
		callsAfterFailure := faults.closeCalls
		faults.mu.Unlock()
		if err := root.Close(); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("root close = %v, want cached close unknown", err)
		}
		faults.mu.Lock()
		if faults.closeCalls != callsAfterFailure {
			t.Fatalf("OS close retried after binding close unknown: before=%d after=%d", callsAfterFailure, faults.closeCalls)
		}
		faults.mu.Unlock()
		verifiedBootstrapOrphans.Lock()
		newOrphans := append([]uintptr(nil), verifiedBootstrapOrphans.handles[orphanStart:]...)
		verifiedBootstrapOrphans.handles = verifiedBootstrapOrphans.handles[:orphanStart]
		verifiedBootstrapOrphans.Unlock()
		for _, handle := range newOrphans {
			_ = unix.Close(int(handle))
		}
		_ = unix.Close(int(root.handle))
	})

	t.Run("ROOT-07/close_drain_observes_unknown_close", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "catalog.log"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		faults := &closeFaultOps{verifiedNative: linuxVerifiedOps{}}
		faults.verifiedNative = linuxVerifiedOps{closeFile: faults.closeFile}
		anchor.ops = faults
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		faults.mu.Lock()
		baselineCloseCalls := faults.closeCalls
		faults.mu.Unlock()
		verifiedBootstrapOrphans.Lock()
		orphanStart := len(verifiedBootstrapOrphans.handles)
		verifiedBootstrapOrphans.Unlock()
		t.Cleanup(func() {
			verifiedBootstrapOrphans.Lock()
			orphans := append([]uintptr(nil), verifiedBootstrapOrphans.handles[orphanStart:]...)
			verifiedBootstrapOrphans.handles = verifiedBootstrapOrphans.handles[:orphanStart]
			verifiedBootstrapOrphans.Unlock()
			for _, handle := range orphans {
				_ = unix.Close(int(handle))
			}
			_ = unix.Close(int(root.handle))
		})
		faults.mu.Lock()
		faults.failClose = true
		faults.closeEntered = make(chan struct{})
		faults.closeRelease = make(chan struct{})
		entered, release := faults.closeEntered, faults.closeRelease
		faults.mu.Unlock()
		defer releaseBarrier(release)
		readResult := make(chan error, 1)
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			_, readErr := root.ReadNames()
			readResult <- readErr
		}()
		t.Cleanup(func() { waitDone(t, readDone) })
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for injected binding close")
		}
		closeResult := make(chan error, 1)
		closeDone := make(chan struct{})
		go func() { defer close(closeDone); closeResult <- root.Close() }()
		t.Cleanup(func() { waitDone(t, closeDone) })
		deadline := time.Now().Add(2 * time.Second)
		for {
			root.mu.Lock()
			closing := root.closing
			root.mu.Unlock()
			if closing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("root Close did not enter drain")
			}
			runtime.Gosched()
		}
		close(release)
		if err := receiveWithin(t, readResult); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("ReadNames = %v, want close unknown", err)
		}
		if err := receiveWithin(t, closeResult); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("concurrent root Close = %v, want close unknown", err)
		}
		faults.mu.Lock()
		calls := faults.closeCalls
		faults.mu.Unlock()
		if calls != baselineCloseCalls+1 {
			t.Fatalf("close count after in-flight close unknown = %d, want exactly one attempted close after baseline %d", calls, baselineCloseCalls)
		}
		_ = root.Close()
		faults.mu.Lock()
		if faults.closeCalls != calls {
			t.Fatalf("OS close occurred after drain observed unknown: before=%d after=%d", calls, faults.closeCalls)
		}
		faults.mu.Unlock()
	})

	t.Run("ROOT-07/child_close_drain_observes_unknown_close", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		if err := os.WriteFile(filepath.Join(runRoot, "catalog.log"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		anchor := newVerifiedTestAnchor(t, anchorPath)
		faults := &closeFaultOps{verifiedNative: linuxVerifiedOps{}}
		faults.verifiedNative = linuxVerifiedOps{closeFile: faults.closeFile}
		anchor.ops = faults
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		member, err := root.OpenMember(CatalogLog)
		if err != nil {
			t.Fatalf("open member: %v", err)
		}
		faults.mu.Lock()
		baselineCloseCalls := faults.closeCalls
		faults.mu.Unlock()
		verifiedBootstrapOrphans.Lock()
		orphanStart := len(verifiedBootstrapOrphans.handles)
		verifiedBootstrapOrphans.Unlock()
		t.Cleanup(func() {
			verifiedBootstrapOrphans.Lock()
			orphans := append([]uintptr(nil), verifiedBootstrapOrphans.handles[orphanStart:]...)
			verifiedBootstrapOrphans.handles = verifiedBootstrapOrphans.handles[:orphanStart]
			verifiedBootstrapOrphans.Unlock()
			for _, handle := range orphans {
				_ = unix.Close(int(handle))
			}
			_ = unix.Close(int(member.child.handle))
			_ = unix.Close(int(root.handle))
		})
		faults.mu.Lock()
		faults.failClose = true
		faults.closeEntered = make(chan struct{})
		faults.closeRelease = make(chan struct{})
		entered, release := faults.closeEntered, faults.closeRelease
		faults.mu.Unlock()
		defer releaseBarrier(release)
		readResult := make(chan error, 1)
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			_, readErr := root.ReadNames()
			readResult <- readErr
		}()
		t.Cleanup(func() { waitDone(t, readDone) })
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for injected binding close")
		}
		closeResult := make(chan error, 1)
		closeDone := make(chan struct{})
		go func() { defer close(closeDone); closeResult <- member.Close() }()
		t.Cleanup(func() { waitDone(t, closeDone) })
		deadline := time.Now().Add(2 * time.Second)
		for {
			root.mu.Lock()
			closing := root.closeInFlight
			root.mu.Unlock()
			if closing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("member Close did not enter drain")
			}
			runtime.Gosched()
		}
		close(release)
		if err := receiveWithin(t, readResult); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("ReadNames = %v, want close unknown", err)
		}
		if err := receiveWithin(t, closeResult); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("concurrent member Close = %v, want close unknown", err)
		}
		faults.mu.Lock()
		calls := faults.closeCalls
		faults.mu.Unlock()
		if calls != baselineCloseCalls+1 {
			t.Fatalf("close count after in-flight close unknown = %d, want exactly one attempted close after baseline %d", calls, baselineCloseCalls)
		}
		if err := root.Close(); !errors.Is(err, errVerifiedCloseUnknown) {
			t.Fatalf("root Close = %v, want cached close unknown", err)
		}
		faults.mu.Lock()
		if faults.closeCalls != calls {
			t.Fatalf("OS close occurred after child drain observed unknown: before=%d after=%d", calls, faults.closeCalls)
		}
		faults.mu.Unlock()
	})

	t.Run("ROOT-15/queued_operation_stops_after_poison", func(t *testing.T) {
		anchorPath, runRoot := newVerifiedTestTree(t)
		anchor := newVerifiedTestAnchor(t, anchorPath)
		poison := &enumeratePoisonOps{verifiedNative: anchor.ops}
		anchor.ops = poison
		epoch, _ := ParseRunEpoch(filepath.Base(runRoot))
		root, err := OpenVerifiedRoot(anchor, epoch)
		if err != nil {
			t.Fatalf("open verified root: %v", err)
		}
		poison.mu.Lock()
		baselineEnumerations := poison.enumerations
		poison.mu.Unlock()
		poison.mu.Lock()
		poison.blockNext = true
		poison.entered = make(chan struct{})
		poison.release = make(chan struct{})
		entered, release := poison.entered, poison.release
		poison.mu.Unlock()
		defer releaseBarrier(release)
		first := make(chan error, 1)
		second := make(chan error, 1)
		firstDone := make(chan struct{})
		go func() { defer close(firstDone); _, err := root.ReadNames(); first <- err }()
		t.Cleanup(func() { waitDone(t, firstDone) })
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for inventory barrier")
		}
		secondDone := make(chan struct{})
		go func() { defer close(secondDone); _, err := root.OpenMember(CatalogLog); second <- err }()
		t.Cleanup(func() { waitDone(t, secondDone) })
		deadline := time.Now().Add(2 * time.Second)
		for {
			root.mu.Lock()
			inFlight := root.inFlight
			root.mu.Unlock()
			if inFlight == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("queued operation did not enter before releasing inventory barrier")
			}
			runtime.Gosched()
		}
		close(release)
		if err := receiveWithin(t, first); !errors.Is(err, errVerifiedPolicyMismatch) {
			t.Fatalf("first operation = %v, want policy mismatch", err)
		}
		if err := receiveWithin(t, second); !errors.Is(err, errVerifiedPolicyMismatch) {
			t.Fatalf("queued operation = %v, want existing poison", err)
		}
		poison.mu.Lock()
		calls := poison.enumerations
		poison.mu.Unlock()
		if calls != baselineEnumerations+1 {
			t.Fatalf("queued operation touched native enumeration after poison: baseline=%d calls=%d", baselineEnumerations, calls)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close poisoned root: %v", err)
		}
	})
}

func receiveWithin(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for goroutine result")
		return nil
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Errorf("goroutine did not stop after releasing test barrier")
	}
}

func releaseBarrier(release chan struct{}) {
	select {
	case <-release:
	default:
		close(release)
	}
}

type closeFaultOps struct {
	verifiedNative
	mu           sync.Mutex
	failClose    bool
	closeCalls   int
	closeEntered chan struct{}
	closeRelease chan struct{}
}

type enumerateCountOps struct {
	verifiedNative
	mu     sync.Mutex
	target uintptr
	count  int
}

func (f *enumerateCountOps) enumerate(handle uintptr) ([]string, error) {
	f.mu.Lock()
	if handle == f.target && f.target != 0 {
		f.count++
	}
	f.mu.Unlock()
	return f.verifiedNative.enumerate(handle)
}

func (f *closeFaultOps) close(handle uintptr) error {
	f.mu.Lock()
	f.closeCalls++
	fail := f.failClose
	entered, release := f.closeEntered, f.closeRelease
	f.mu.Unlock()
	if fail {
		if entered != nil {
			select {
			case <-entered:
			default:
				close(entered)
			}
		}
		if release != nil {
			<-release
		}
		return errVerifiedCloseUnknown
	}
	return unix.Close(int(handle))
}

type enumeratePoisonOps struct {
	verifiedNative
	mu           sync.Mutex
	blockNext    bool
	entered      chan struct{}
	release      chan struct{}
	enumerations int
}

func (f *enumeratePoisonOps) enumerate(handle uintptr) ([]string, error) {
	f.mu.Lock()
	f.enumerations++
	block := f.blockNext
	if block {
		f.blockNext = false
	}
	entered, release := f.entered, f.release
	f.mu.Unlock()
	if block {
		close(entered)
		<-release
		return []string{"unexpected"}, nil
	}
	return f.verifiedNative.enumerate(handle)
}

func (f *closeFaultOps) closeFile(fd int) error {
	return f.close(uintptr(fd))
}

func equalRootEntries(a, b []RootEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

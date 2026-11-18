//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// This integration case executes real ext4/openat2/statx/xattr/fsync/mkdirat
// operations as an unprivileged owner. It deliberately uses the same worker
// UID/GID as the test process, so it does not claim service-identity or
// privileged fchown coverage.
func TestCreateRunRootLinuxOnOwnedExt4Anchor(t *testing.T) {
	base := os.Getenv("PILOT_FS_TEST_ROOT")
	if base == "" {
		t.Fatal("BLOCKED: PILOT_FS_TEST_ROOT must point to a dedicated ext4 test directory")
	}
	baseInfo, err := os.Stat(base)
	if err != nil || !baseInfo.IsDir() {
		t.Fatalf("BLOCKED: PILOT_FS_TEST_ROOT must pre-exist as a directory: info=%v err=%v", baseInfo, err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("resolve test root: %v", err)
	}
	requireMountType(t, base, "ext4")
	work, err := os.MkdirTemp(base, "runroot-create-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	parentPath := filepath.Join(work, "parent")
	anchorPath := filepath.Join(parentPath, "anchor")
	if err := os.Mkdir(parentPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(anchorPath, 0710); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(anchorPath, 0710); err != nil {
		t.Fatal(err)
	}
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(parentFD) })
	anchorFD, err := unix.Open(anchorPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(anchorFD) })
	identity, anchorStat, err := inspectRunRootLinuxFD(anchorFD)
	if err != nil {
		t.Fatalf("inspect real anchor: %v", err)
	}
	requireMountType(t, anchorPath, "ext4")
	var fs unix.Statfs_t
	if err := unix.Fstatfs(anchorFD, &fs); err != nil {
		t.Fatal(err)
	}
	if uint64(fs.Type) != runRootExt4Magic {
		t.Fatalf("ext4 mount reported unexpected statfs magic: %x", uint64(fs.Type))
	}
	workerUID, workerGID := uint32(unix.Getuid()), uint32(unix.Getgid())
	if workerUID == 0 || workerGID == 0 || anchorStat.Uid != workerUID || anchorStat.Gid != workerGID {
		t.Fatalf("test must run as non-root owner of anchor: uid/gid=%d/%d anchor=%d/%d", workerUID, workerGID, anchorStat.Uid, anchorStat.Gid)
	}
	policy := runRootAnchorPolicy{Device: identity[0], Inode: identity[1], Mount: identity[2], UID: workerUID,
		GID: workerGID, ParentFD: parentFD, Name: filepath.Base(anchorPath)}
	if _, err := validateRunRootAnchorLinux(anchorFD, policy); err != nil {
		t.Fatalf("preflight real ext4 anchor: %v", err)
	}
	epoch := RunEpoch("0123456789abcdef0123456789abcdef")
	if got := createRunRootLinux(anchorFD, epoch, policy, 0, workerGID); got.Outcome != runRootCreateIndeterminate {
		t.Fatalf("zero worker UID outcome = %+v, want fail-closed indeterminate", got)
	}
	created := createRunRootLinux(anchorFD, epoch, policy, workerUID, workerGID)
	if created.Outcome != runRootCreateCompleted || created.Err != nil || created.RootFD < 0 {
		t.Fatalf("real ext4 create outcome = %+v", created)
	}
	t.Cleanup(func() { _ = unix.Close(created.RootFD) })
	rootID, rootStat, err := inspectRunRootLinuxFD(created.RootFD)
	if err != nil || rootID != created.RootIdentity || rootStat.Uid != workerUID || rootStat.Gid != workerGID || rootStat.Mode&07777 != 0700 {
		t.Fatalf("created root identity/stat mismatch: id=%v expected=%v stat=%+v err=%v", rootID, created.RootIdentity, rootStat, err)
	}
	collision := createRunRootLinux(anchorFD, epoch, policy, workerUID, workerGID)
	if collision.Outcome != runRootCreateCollision || collision.Err != errVerifiedPolicyMismatch || collision.RootFD != 0 {
		t.Fatalf("existing run-root collision = %+v", collision)
	}
}

func requireMountType(t *testing.T, path, want string) {
	t.Helper()
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatalf("read mountinfo: %v", err)
	}
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	cleanPath := filepath.Clean(path)
	bestLength, bestType := -1, ""
	for _, line := range strings.Split(string(mountInfo), "\n") {
		beforeAfter := strings.SplitN(line, " - ", 2)
		if len(beforeAfter) != 2 {
			continue
		}
		fields := strings.Fields(beforeAfter[0])
		fsFields := strings.Fields(beforeAfter[1])
		if len(fields) < 5 || len(fsFields) < 1 {
			continue
		}
		mountPoint := filepath.Clean(decode.Replace(fields[4]))
		within := cleanPath == mountPoint || strings.HasPrefix(cleanPath, strings.TrimSuffix(mountPoint, string(filepath.Separator))+string(filepath.Separator))
		if within && len(mountPoint) > bestLength {
			bestLength, bestType = len(mountPoint), fsFields[0]
		}
	}
	if bestType != want {
		t.Fatalf("BLOCKED: path %q resolves to mount type %q, want exact %q mount", path, bestType, want)
	}
}

func TestClassifyRunRootMkdirError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"permission", unix.EACCES, errVerifiedIOFailed},
		{"read-only", unix.EROFS, errVerifiedIOFailed},
		{"disk-full", unix.ENOSPC, errVerifiedIOFailed},
		{"quota", unix.EDQUOT, errVerifiedIOFailed},
		{"missing-parent", unix.ENOENT, errVerifiedPathInvalid},
		{"not-directory", unix.ENOTDIR, errVerifiedPathInvalid},
		{"symlink", unix.ELOOP, errVerifiedPathInvalid},
		{"foreign-mount", unix.EXDEV, errVerifiedPathInvalid},
		{"invalid", unix.EINVAL, errVerifiedPolicyMismatch},
		{"collision-special-cased", unix.EEXIST, errRunRootCreateIndeterminate},
		{"io-uncertain", unix.EIO, errRunRootCreateIndeterminate},
		{"interrupted", unix.EINTR, errRunRootCreateIndeterminate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRunRootMkdirError(tc.err); got != tc.want {
				t.Fatalf("classifyRunRootMkdirError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type fakeRunRootFinalizeOps struct {
	failAt   string
	calls    []string
	closeErr error
}

func (ops *fakeRunRootFinalizeOps) step(name string) error {
	ops.calls = append(ops.calls, name)
	if ops.failAt == name {
		return unix.EIO
	}
	return nil
}
func (ops *fakeRunRootFinalizeOps) fchmod(int, uint32) error   { return ops.step("fchmod") }
func (ops *fakeRunRootFinalizeOps) fchown(int, int, int) error { return ops.step("fchown") }
func (ops *fakeRunRootFinalizeOps) inspect(fd int) ([3]uint64, unix.Stat_t, error) {
	name := "inspect-root"
	identity := [3]uint64{4, 8, 6}
	stat := unix.Stat_t{Uid: 22, Gid: 33, Mode: unix.S_IFDIR | 0700}
	if fd == 1 {
		name = "inspect-anchor"
		identity = [3]uint64{4, 5, 6}
	}
	if err := ops.step(name); err != nil {
		return [3]uint64{}, unix.Stat_t{}, err
	}
	return identity, stat, nil
}
func (ops *fakeRunRootFinalizeOps) verifyNoACL(int) error { return ops.step("acl") }
func (ops *fakeRunRootFinalizeOps) fsync(fd int) error {
	if fd == 1 {
		return ops.step("fsync-anchor")
	}
	return ops.step("fsync-root")
}
func (ops *fakeRunRootFinalizeOps) verifyBinding(runRootAnchorPolicy, [3]uint64) error {
	return ops.step("binding")
}
func (ops *fakeRunRootFinalizeOps) close(int) error {
	if err := ops.step("close"); err != nil {
		return err
	}
	return ops.closeErr
}
func (ops *fakeRunRootFinalizeOps) retain(int) { ops.calls = append(ops.calls, "retain") }

func TestFinalizeRunRootLinuxPostMkdirFailuresAreIndeterminateAndNeverCleanedUp(t *testing.T) {
	stages := []string{"fchmod", "fchown", "inspect-root", "acl", "fsync-root", "fsync-anchor", "inspect-anchor", "binding"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			ops := &fakeRunRootFinalizeOps{failAt: stage}
			identity, err := finalizeRunRootLinux(1, 2, runRootAnchorPolicy{}, [3]uint64{4, 5, 6}, 22, 33, ops)
			if err != errRunRootCreateIndeterminate || identity != [3]uint64{} {
				t.Fatalf("identity/error = %v/%v, want empty/indeterminate", identity, err)
			}
			if len(ops.calls) == 0 || ops.calls[len(ops.calls)-1] != "close" {
				t.Fatalf("failure did not close the pinned root FD: %v", ops.calls)
			}
			for _, call := range ops.calls {
				if call == "unlink" || call == "mkdirat" {
					t.Fatalf("post-create path attempted cleanup or retry: %v", ops.calls)
				}
			}
		})
	}
}

func TestFinalizeRunRootLinuxCloseFailureRetainsHandleAndRequiresRestart(t *testing.T) {
	ops := &fakeRunRootFinalizeOps{failAt: "fchmod", closeErr: unix.EIO}
	_, err := finalizeRunRootLinux(1, 2, runRootAnchorPolicy{}, [3]uint64{4, 5, 6}, 22, 33, ops)
	if err != errRunRootRestartRequired {
		t.Fatalf("finalize error = %v, want restart-required", err)
	}
	if got := ops.calls[len(ops.calls)-1]; got != "retain" {
		t.Fatalf("last operation = %q, want retained unknown handle; calls=%v", got, ops.calls)
	}
}

func TestFinalizeRunRootLinuxSuccessKeepsPinnedFDForActiveRun(t *testing.T) {
	ops := &fakeRunRootFinalizeOps{}
	identity, err := finalizeRunRootLinux(1, 2, runRootAnchorPolicy{}, [3]uint64{4, 5, 6}, 22, 33, ops)
	if err != nil || identity != [3]uint64{4, 8, 6} {
		t.Fatalf("finalize result = %v/%v, want verified identity/success", identity, err)
	}
	for _, call := range ops.calls {
		if call == "close" || call == "retain" {
			t.Fatalf("successful finalize closed active root FD: %v", ops.calls)
		}
	}
}

func TestRunRootTrustedComponentAndEpochValidation(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "nul\x00byte"} {
		if validTrustedRunRootComponent(name) {
			t.Errorf("unsafe component %q accepted", name)
		}
	}
	for _, name := range []string{"pilot-runs", "anchor.v1", "root_1"} {
		if !validTrustedRunRootComponent(name) {
			t.Errorf("trusted component %q rejected", name)
		}
	}
	if _, err := ParseRunEpoch("ABCDEF0123456789abcdef0123456789"); err == nil {
		t.Fatal("non-canonical run epoch accepted")
	}
	if _, err := ParseRunEpoch("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("canonical run epoch rejected: %v", err)
	}
}

type fakeRunRootMkdirOps struct {
	bindingErrors []error
	callError     error
	absentError   error
	calls         []string
}

func (ops *fakeRunRootMkdirOps) verifyAnchorBinding([3]uint64) error {
	ops.calls = append(ops.calls, "binding")
	index := 0
	for _, call := range ops.calls {
		if call == "binding" {
			index++
		}
	}
	if index <= len(ops.bindingErrors) {
		return ops.bindingErrors[index-1]
	}
	return nil
}

func (ops *fakeRunRootMkdirOps) mkdirat(string) error {
	ops.calls = append(ops.calls, "mkdirat")
	return ops.callError
}

func (ops *fakeRunRootMkdirOps) verifyAbsent(string) error {
	ops.calls = append(ops.calls, "absence")
	return ops.absentError
}

func TestDispatchRunRootMkdirOutcomes(t *testing.T) {
	identity := [3]uint64{1, 2, 3}
	cases := []struct {
		name          string
		bindingErrors []error
		callError     error
		absentError   error
		wantOutcome   runRootCreateOutcome
		wantErr       error
		wantCalls     []string
	}{
		{"success", nil, nil, nil, runRootCreateCompleted, nil, []string{"binding", "mkdirat"}},
		{"collision-never-queries-target", nil, unix.EEXIST, nil, runRootCreateCollision, errVerifiedPolicyMismatch, []string{"binding", "mkdirat", "binding"}},
		{"deterministic-errno-with-absence", nil, unix.EACCES, nil, runRootCreateNoEffect, errVerifiedIOFailed, []string{"binding", "mkdirat", "binding", "absence"}},
		{"deterministic-errno-with-unknown-absence", nil, unix.EACCES, errVerifiedIOFailed, runRootCreateIndeterminate, errRunRootCreateIndeterminate, []string{"binding", "mkdirat", "binding", "absence"}},
		{"unknown-errno-skips-absence-query", nil, unix.EIO, nil, runRootCreateIndeterminate, errRunRootCreateIndeterminate, []string{"binding", "mkdirat", "binding"}},
		{"binding-drift-before-create", []error{errVerifiedIdentityChanged}, nil, nil, runRootCreateIndeterminate, errRunRootCreateIndeterminate, []string{"binding"}},
		{"binding-drift-after-error", []error{nil, errVerifiedIdentityChanged}, unix.EACCES, nil, runRootCreateIndeterminate, errRunRootCreateIndeterminate, []string{"binding", "mkdirat", "binding"}},
		{"close-unknown-requires-restart", []error{errVerifiedCloseUnknown}, nil, nil, runRootCreateIndeterminate, errRunRootRestartRequired, []string{"binding"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeRunRootMkdirOps{bindingErrors: tc.bindingErrors, callError: tc.callError, absentError: tc.absentError}
			got := dispatchRunRootMkdir(ops, "0123456789abcdef0123456789abcdef", identity)
			if got.Outcome != tc.wantOutcome || got.Err != tc.wantErr {
				t.Fatalf("dispatch outcome=(%v,%v), want (%v,%v)", got.Outcome, got.Err, tc.wantOutcome, tc.wantErr)
			}
			if len(ops.calls) != len(tc.wantCalls) {
				t.Fatalf("calls=%v, want %v", ops.calls, tc.wantCalls)
			}
			for i := range tc.wantCalls {
				if ops.calls[i] != tc.wantCalls[i] {
					t.Fatalf("calls=%v, want %v", ops.calls, tc.wantCalls)
				}
			}
		})
	}
}

func TestRunRootPostCreateFailureRequiresRestartOnUnknownClose(t *testing.T) {
	cases := []struct {
		name     string
		cause    error
		closeErr error
		want     error
	}{
		{"ordinary-post-create-failure", errVerifiedIOFailed, nil, errRunRootCreateIndeterminate},
		{"anchor-close-unknown", errVerifiedCloseUnknown, nil, errRunRootRestartRequired},
		{"root-close-error", errVerifiedIOFailed, unix.EIO, errRunRootRestartRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runRootPostCreateFailureCode(tc.cause, tc.closeErr); got != tc.want {
				t.Fatalf("post-create failure code = %v, want %v", got, tc.want)
			}
		})
	}
}

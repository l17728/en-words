//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	runRootExt4Magic = 0xEF53
	runRootXFSMagic  = 0x58465342
)

type runRootAnchorPolicy struct {
	Device   uint64
	Inode    uint64
	Mount    uint64
	UID      uint32
	GID      uint32
	ParentFD int
	Name     string
}

type runRootLinuxResult struct {
	RootFD       int
	RootIdentity [3]uint64
	Outcome      runRootCreateOutcome
	Err          error
}

type runRootCreateOutcome uint8

const (
	runRootCreateCompleted runRootCreateOutcome = iota + 1
	runRootCreateCollision
	runRootCreateNoEffect
	runRootCreateIndeterminate
)

// createRunRootLinux is the filesystem-effect layer only. Its caller must
// already hold the durable executing reservation and logger ACK required by
// docs/45 before entering this function. This function never retries or cleans
// up a directory after mkdirat succeeds.
func createRunRootLinux(anchorFD int, epoch RunEpoch, expected runRootAnchorPolicy, workerUID, workerGID uint32) runRootLinuxResult {
	if anchorFD < 0 || expected.ParentFD < 0 || !validTrustedRunRootComponent(expected.Name) || !runEpochPattern.MatchString(string(epoch)) {
		return runRootLinuxResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}
	if workerUID == 0 || workerGID == 0 {
		return runRootLinuxResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}
	anchorIdentity, err := validateRunRootAnchorLinux(anchorFD, expected)
	if err != nil {
		// The caller is already beyond the executing CAS here. A failed
		// revalidation cannot be converted into a pre-dispatch no-effect proof.
		if errors.Is(err, errVerifiedCloseUnknown) {
			return runRootLinuxResult{Outcome: runRootCreateIndeterminate, Err: errRunRootRestartRequired}
		}
		return runRootLinuxResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}

	mkdirResult := dispatchRunRootMkdir(runRootNativeMkdirOps{anchorFD: anchorFD, policy: expected}, string(epoch), anchorIdentity)
	if mkdirResult.Outcome != runRootCreateCompleted {
		return runRootLinuxResult{Outcome: mkdirResult.Outcome, Err: mkdirResult.Err}
	}

	rootFD, openErr := openRunRootLinux(anchorFD, string(epoch))
	if openErr != nil {
		return runRootLinuxResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}
	rootIdentity, finalizeErr := finalizeRunRootLinux(anchorFD, rootFD, expected, anchorIdentity, workerUID, workerGID, runRootNativeFinalizeOps{})
	if finalizeErr != nil {
		return runRootLinuxResult{Outcome: runRootCreateIndeterminate, Err: finalizeErr}
	}
	return runRootLinuxResult{RootFD: rootFD, RootIdentity: rootIdentity, Outcome: runRootCreateCompleted}
}

// runRootFinalizeOps keeps every post-mkdir operation injectable. Once mkdirat
// succeeds, every error is indeterminate; this interface deliberately has no
// unlink/retry method so tests can lock that fail-closed rule.
type runRootFinalizeOps interface {
	fchmod(int, uint32) error
	fchown(int, int, int) error
	inspect(int) ([3]uint64, unix.Stat_t, error)
	verifyNoACL(int) error
	fsync(int) error
	verifyBinding(runRootAnchorPolicy, [3]uint64) error
	close(int) error
	retain(int)
}

type runRootNativeFinalizeOps struct{}

func (runRootNativeFinalizeOps) fchmod(fd int, mode uint32) error { return unix.Fchmod(fd, mode) }
func (runRootNativeFinalizeOps) fchown(fd, uid, gid int) error    { return unix.Fchown(fd, uid, gid) }
func (runRootNativeFinalizeOps) inspect(fd int) ([3]uint64, unix.Stat_t, error) {
	return inspectRunRootLinuxFD(fd)
}
func (runRootNativeFinalizeOps) verifyNoACL(fd int) error { return verifyRunRootLinuxNoACL(fd) }
func (runRootNativeFinalizeOps) fsync(fd int) error       { return unix.Fsync(fd) }
func (runRootNativeFinalizeOps) verifyBinding(policy runRootAnchorPolicy, identity [3]uint64) error {
	return verifyRunRootLinuxAnchorBinding(policy, identity)
}
func (runRootNativeFinalizeOps) close(fd int) error { return unix.Close(fd) }
func (runRootNativeFinalizeOps) retain(fd int)      { retainVerifiedOrphan(uintptr(fd)) }

func finalizeRunRootLinux(anchorFD, rootFD int, policy runRootAnchorPolicy, anchorIdentity [3]uint64, workerUID, workerGID uint32, ops runRootFinalizeOps) ([3]uint64, error) {
	if ops == nil {
		// mkdirat already succeeded. Even an invalid internal adapter must not
		// abandon the open handle without attempting the conservative close.
		ops = runRootNativeFinalizeOps{}
	}
	fail := func(cause error) ([3]uint64, error) {
		closeErr := ops.close(rootFD)
		if closeErr != nil {
			ops.retain(rootFD)
		}
		return [3]uint64{}, runRootPostCreateFailureCode(cause, closeErr)
	}
	if err := ops.fchmod(rootFD, 0700); err != nil {
		return fail(errVerifiedIOFailed)
	}
	if err := ops.fchown(rootFD, int(workerUID), int(workerGID)); err != nil {
		return fail(errVerifiedIOFailed)
	}
	rootIdentity, rootStat, err := ops.inspect(rootFD)
	if err != nil {
		return fail(err)
	}
	if rootIdentity[0] != anchorIdentity[0] || rootIdentity[2] != anchorIdentity[2] ||
		rootStat.Uid != workerUID || rootStat.Gid != workerGID || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Mode&07777 != 0700 {
		return fail(errVerifiedPolicyMismatch)
	}
	if err = ops.verifyNoACL(rootFD); err != nil {
		return fail(err)
	}
	if err = ops.fsync(rootFD); err != nil {
		return fail(errVerifiedIOFailed)
	}
	if err = ops.fsync(anchorFD); err != nil {
		return fail(errVerifiedIOFailed)
	}
	currentAnchor, _, err := ops.inspect(anchorFD)
	if err != nil || currentAnchor != anchorIdentity {
		return fail(errVerifiedIdentityChanged)
	}
	if err = ops.verifyBinding(policy, anchorIdentity); err != nil {
		return fail(err)
	}
	return rootIdentity, nil
}

// validateRunRootAnchorLinux is the non-mutating preflight that must complete
// before the durable prepared reservation. createRunRootLinux repeats it
// after the executing CAS to fence identity or policy drift in the gap.
func validateRunRootAnchorLinux(anchorFD int, expected runRootAnchorPolicy) ([3]uint64, error) {
	if anchorFD < 0 || expected.ParentFD < 0 || !validTrustedRunRootComponent(expected.Name) {
		return [3]uint64{}, errVerifiedPathInvalid
	}
	identity, st, err := inspectRunRootLinuxFD(anchorFD)
	if err != nil {
		return [3]uint64{}, err
	}
	if identity != [3]uint64{expected.Device, expected.Inode, expected.Mount} {
		return [3]uint64{}, errVerifiedPathInvalid
	}
	if err = verifyRunRootLinuxAnchorBinding(expected, identity); err != nil {
		return [3]uint64{}, err
	}
	if st.Uid != expected.UID || st.Gid != expected.GID || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0710 {
		return [3]uint64{}, errVerifiedPolicyMismatch
	}
	if err = verifyRunRootLinuxFilesystem(anchorFD); err != nil {
		return [3]uint64{}, err
	}
	if err = verifyRunRootLinuxNoACL(anchorFD); err != nil {
		return [3]uint64{}, err
	}
	return identity, nil
}

var errRunRootCreateIndeterminate = errors.New("ipc_result_indeterminate")
var errRunRootRestartRequired = errors.New("restart_required")

func runRootPostCreateFailureCode(cause, closeErr error) error {
	if closeErr != nil || errors.Is(cause, errVerifiedCloseUnknown) {
		return errRunRootRestartRequired
	}
	return errRunRootCreateIndeterminate
}

type runRootMkdirOps interface {
	verifyAnchorBinding([3]uint64) error
	mkdirat(string) error
	verifyAbsent(string) error
}

type runRootMkdirResult struct {
	Outcome runRootCreateOutcome
	Err     error
}

type runRootNativeMkdirOps struct {
	anchorFD int
	policy   runRootAnchorPolicy
}

func (ops runRootNativeMkdirOps) verifyAnchorBinding(identity [3]uint64) error {
	return verifyRunRootLinuxAnchorBinding(ops.policy, identity)
}

func (ops runRootNativeMkdirOps) mkdirat(name string) error {
	return unix.Mkdirat(ops.anchorFD, name, 0700)
}

func (ops runRootNativeMkdirOps) verifyAbsent(name string) error {
	return verifyRunRootLinuxAbsent(ops.anchorFD, name)
}

// dispatchRunRootMkdir binds both the before-call and post-error checks to the
// pinned anchor. EEXIST deliberately skips all target queries.
func dispatchRunRootMkdir(ops runRootMkdirOps, epoch string, anchorIdentity [3]uint64) runRootMkdirResult {
	if ops == nil {
		return runRootMkdirResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}
	if err := ops.verifyAnchorBinding(anchorIdentity); err != nil {
		if errors.Is(err, errVerifiedCloseUnknown) {
			return runRootMkdirResult{Outcome: runRootCreateIndeterminate, Err: errRunRootRestartRequired}
		}
		return runRootMkdirResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}
	callErr := ops.mkdirat(epoch)
	if callErr == nil {
		return runRootMkdirResult{Outcome: runRootCreateCompleted}
	}
	if err := ops.verifyAnchorBinding(anchorIdentity); err != nil {
		if errors.Is(err, errVerifiedCloseUnknown) {
			return runRootMkdirResult{Outcome: runRootCreateIndeterminate, Err: errRunRootRestartRequired}
		}
		return runRootMkdirResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
	}
	if errors.Is(callErr, unix.EEXIST) {
		return runRootMkdirResult{Outcome: runRootCreateCollision, Err: errVerifiedPolicyMismatch}
	}
	mapped := classifyRunRootMkdirError(callErr)
	if mapped == errVerifiedIOFailed || mapped == errVerifiedPathInvalid || mapped == errVerifiedPolicyMismatch {
		if ops.verifyAbsent(epoch) == nil {
			return runRootMkdirResult{Outcome: runRootCreateNoEffect, Err: mapped}
		}
	}
	return runRootMkdirResult{Outcome: runRootCreateIndeterminate, Err: errRunRootCreateIndeterminate}
}

func inspectRunRootLinuxFD(fd int) ([3]uint64, unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return [3]uint64{}, unix.Stat_t{}, errVerifiedIOFailed
	}
	var sx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, int(unix.STATX_BASIC_STATS|unix.STATX_MNT_ID), &sx); err != nil || sx.Mask&unix.STATX_MNT_ID == 0 {
		return [3]uint64{}, unix.Stat_t{}, errVerifiedIOFailed
	}
	return [3]uint64{uint64(st.Dev), st.Ino, sx.Mnt_id}, st, nil
}

func verifyRunRootLinuxFilesystem(fd int) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return errVerifiedIOFailed
	}
	if uint64(fs.Type) != runRootExt4Magic && uint64(fs.Type) != runRootXFSMagic {
		return errVerifiedPolicyMismatch
	}
	return nil
}

func verifyRunRootLinuxNoACL(fd int) error {
	for _, attr := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Fgetxattr(fd, attr, nil)
		if err == nil {
			return errVerifiedPolicyMismatch
		}
		if !errors.Is(err, syscall.ENODATA) {
			return errVerifiedIOFailed
		}
	}
	return nil
}

func openRunRootLinux(anchorFD int, epoch string) (int, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	how := &unix.OpenHow{
		Flags:   uint64(flags),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	return unix.Openat2(anchorFD, epoch, how)
}

func verifyRunRootLinuxAnchorBinding(policy runRootAnchorPolicy, expected [3]uint64) error {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	how := &unix.OpenHow{
		Flags:   uint64(flags),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(policy.ParentFD, policy.Name, how)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
			return errVerifiedIdentityChanged
		}
		return errVerifiedIOFailed
	}
	actual, _, inspectErr := inspectRunRootLinuxFD(fd)
	closeErr := unix.Close(fd)
	if closeErr != nil {
		retainVerifiedOrphan(uintptr(fd))
		return errVerifiedCloseUnknown
	}
	if inspectErr != nil {
		return inspectErr
	}
	if actual != expected {
		return errVerifiedIdentityChanged
	}
	return nil
}

func validTrustedRunRootComponent(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, b := range []byte(name) {
		if b == '/' || b == '\\' || b == 0 {
			return false
		}
	}
	return true
}

func verifyRunRootLinuxAbsent(anchorFD int, epoch string) error {
	var st unix.Stat_t
	err := unix.Fstatat(anchorFD, epoch, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errVerifiedIOFailed
	}
	return errVerifiedIdentityChanged
}

func classifyRunRootMkdirError(err error) error {
	switch {
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM), errors.Is(err, unix.EROFS),
		errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT), errors.Is(err, unix.EMLINK), errors.Is(err, unix.ENOMEM):
		return errVerifiedIOFailed
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENAMETOOLONG):
		return errVerifiedPathInvalid
	case errors.Is(err, unix.EINVAL):
		return errVerifiedPolicyMismatch
	default:
		return errRunRootCreateIndeterminate
	}
}

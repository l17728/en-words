//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxVerifiedOps struct {
	closeFile func(int) error
}

var linuxVerifiedNative verifiedNative = linuxVerifiedOps{}

func (ops linuxVerifiedOps) open(parent uintptr, name string, kind verifiedKind) (uintptr, error) {
	if name == "" || strings.ContainsAny(name, "/\\\x00:") || name == "." || name == ".." {
		return 0, errVerifiedPathInvalid
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if kind == verifiedDirectory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := openat2Linux(int(parent), name, flags)
	if err != nil && (errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.E2BIG)) {
		fd, err = unix.Openat(int(parent), name, flags, 0)
	}
	if err != nil {
		return 0, linuxOpenError(err)
	}
	childIdentity, childErr := (linuxVerifiedOps{}).inspect(uintptr(fd), kind)
	parentIdentity, parentErr := (linuxVerifiedOps{}).inspect(parent, verifiedDirectory)
	if childErr != nil || parentErr != nil {
		if closeErr := ops.close(uintptr(fd)); closeErr != nil {
			retainVerifiedOrphan(uintptr(fd))
			return 0, errVerifiedCloseUnknown
		}
		if childErr != nil {
			return 0, childErr
		}
		return 0, parentErr
	}
	if childIdentity.volume != parentIdentity.volume || childIdentity.mount != parentIdentity.mount {
		if closeErr := ops.close(uintptr(fd)); closeErr != nil {
			retainVerifiedOrphan(uintptr(fd))
			return 0, errVerifiedCloseUnknown
		}
		return 0, errVerifiedPathInvalid
	}
	return uintptr(fd), nil
}

func openat2Linux(parent int, name string, flags int) (int, error) {
	how := &unix.OpenHow{
		Flags:   uint64(flags),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	return unix.Openat2(parent, name, how)
}

func linuxOpenError(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return errVerifiedMemberInvalid
	case errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.EISDIR):
		return errVerifiedPolicyMismatch
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		return errVerifiedPathInvalid
	default:
		return errVerifiedIOFailed
	}
}

func (linuxVerifiedOps) inspect(handle uintptr, kind verifiedKind) (verifiedIdentity, error) {
	fd := int(handle)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return verifiedIdentity{}, errVerifiedIOFailed
	}
	actualKind := verifiedRegularFile
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		actualKind = verifiedDirectory
	}
	if actualKind != kind || (st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Mode&unix.S_IFMT != unix.S_IFREG) {
		return verifiedIdentity{}, errVerifiedPolicyMismatch
	}
	if st.Uid != uint32(unix.Geteuid()) || st.Gid != uint32(unix.Getegid()) {
		return verifiedIdentity{}, errVerifiedPolicyMismatch
	}
	mode := uint32(0700)
	if kind == verifiedRegularFile {
		mode = 0600
		if st.Nlink != 1 {
			return verifiedIdentity{}, errVerifiedPolicyMismatch
		}
	}
	if st.Mode&07777 != mode {
		return verifiedIdentity{}, errVerifiedPolicyMismatch
	}
	for _, attr := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Fgetxattr(fd, attr, nil)
		if err == nil {
			return verifiedIdentity{}, errVerifiedPolicyMismatch
		}
		if !errors.Is(err, unix.ENODATA) {
			return verifiedIdentity{}, errVerifiedIOFailed
		}
	}
	var sx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, int(unix.STATX_BASIC_STATS|unix.STATX_MNT_ID), &sx); err != nil {
		return verifiedIdentity{}, errVerifiedIOFailed
	}
	if sx.Mask&unix.STATX_MNT_ID == 0 {
		return verifiedIdentity{}, errVerifiedIOFailed
	}
	return verifiedIdentity{volume: uint64(st.Dev), object: st.Ino, mount: sx.Mnt_id, kind: kind}, nil
}

func (ops linuxVerifiedOps) enumerate(handle uintptr) ([]string, error) {
	fd := int(handle)
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		return nil, errVerifiedIOFailed
	}
	var names []string
	buf := make([]byte, 16*1024)
	for {
		n, err := unix.ReadDirent(fd, buf)
		if err != nil {
			return nil, errVerifiedIOFailed
		}
		if n == 0 {
			break
		}
		_, _, parsed := unix.ParseDirent(buf[:n], -1, nil)
		for _, name := range parsed {
			if name != "." && name != ".." {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

func (ops linuxVerifiedOps) verifyBinding(parent uintptr, name string, expected verifiedIdentity, kind verifiedKind) error {
	h, err := ops.open(parent, name, kind)
	if err != nil {
		if errors.Is(err, errVerifiedMemberInvalid) || errors.Is(err, errVerifiedPathInvalid) {
			return errVerifiedIdentityChanged
		}
		return err
	}
	actual, inspectErr := ops.inspect(h, kind)
	closeErr := ops.close(h)
	if closeErr != nil {
		retainVerifiedOrphan(h)
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

func (linuxVerifiedOps) checkFilesystem(handle uintptr) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(handle), &fs); err != nil {
		return errVerifiedIOFailed
	}
	switch uint64(fs.Type) {
	case 0xEF53, 0x58465342: // ext4, XFS
		return nil
	default:
		return errVerifiedPolicyMismatch
	}
}

func (ops linuxVerifiedOps) close(handle uintptr) error {
	var err error
	if ops.closeFile != nil {
		err = ops.closeFile(int(handle))
	} else {
		err = unix.Close(int(handle))
	}
	if err != nil {
		return errVerifiedCloseUnknown
	}
	return nil
}

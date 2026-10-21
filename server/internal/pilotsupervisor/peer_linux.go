//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type linuxPeerOps struct {
	getPeerCred func(int) (*unix.Ucred, error)
	pollFD      func(int) (bool, error)
}

var realLinuxPeerOps = linuxPeerOps{
	getPeerCred: func(fd int) (*unix.Ucred, error) { return unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED) },
	pollFD: func(fd int) (bool, error) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
		_, err := unix.Poll(fds, 0)
		if err != nil {
			return false, err
		}
		return fds[0].Revents != 0, nil
	},
}

type linuxPeerParent struct {
	mu       sync.Mutex
	pid      int
	uid      uint32
	fd       *os.File
	ops      linuxPeerOps
	closed   bool
	closeErr error
}

func newLinuxPeerParent(fd uintptr, pid int, uid uint32, ops *linuxPeerOps) (*linuxPeerParent, error) {
	if fd == ^uintptr(0) || pid <= 0 {
		return nil, errPeerRejected
	}
	file := os.NewFile(fd, "gateway-pidfd")
	if file == nil {
		return nil, errPeerRejected
	}
	selected := realLinuxPeerOps
	if ops != nil {
		selected = *ops
	}
	p := &linuxPeerParent{pid: pid, uid: uid, fd: file, ops: selected}
	if err := p.checkAlive(); err != nil {
		_ = file.Close()
		return nil, errPeerParentGone
	}
	return p, nil
}

func openLinuxPeerParent(pid int, uid uint32) (*linuxPeerParent, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, errPeerParentGone
	}
	return newLinuxPeerParent(uintptr(fd), pid, uid, nil)
}

func (p *linuxPeerParent) checkAlive() error {
	if p == nil {
		return errPeerParentGone
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.fd == nil || p.fd.Fd() == ^uintptr(0) {
		return errPeerParentGone
	}
	dead, err := p.ops.pollFD(int(p.fd.Fd()))
	if err != nil || dead {
		return errPeerParentGone
	}
	return nil
}

func (p *linuxPeerParent) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	if p.fd != nil {
		p.closeErr = p.fd.Close()
	}
	return p.closeErr
}

var linuxEndpointPattern = regexp.MustCompile(`^worker-[0-9a-f]{32}-[0-9a-f]{32}\.sock$`)

func linuxEndpoint(controlDir, generation, nonce string) (string, error) {
	if !workerGenerationPattern.MatchString(generation) || !noncePattern.MatchString(nonce) {
		return "", errPeerBootstrapInvalid
	}
	base := "worker-" + generation + "-" + nonce + ".sock"
	if len(base) >= 104 {
		return "", errPeerBootstrapInvalid
	}
	endpoint := filepath.Join(controlDir, base)
	if len(endpoint) >= 108 {
		return "", errPeerBootstrapInvalid
	}
	return endpoint, nil
}

type linuxUnixListener struct {
	listener *net.UnixListener
	path     string
	device   uint64
	inode    uint64
	uid      uint32
}

func listenLinuxPeer(controlDir, endpoint string, parent *linuxPeerParent) (*linuxUnixListener, error) {
	if parent == nil || filepath.Dir(endpoint) != filepath.Clean(controlDir) || !linuxEndpointPattern.MatchString(filepath.Base(endpoint)) {
		return nil, errPeerListenerFailed
	}
	dirInfo, err := os.Lstat(controlDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || dirInfo.Mode().Perm() != 0700 {
		return nil, errPeerListenerFailed
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid != parent.uid {
		return nil, errPeerListenerFailed
	}
	if _, err := os.Lstat(endpoint); err == nil || !os.IsNotExist(err) {
		return nil, errPeerListenerFailed
	}
	addr := &net.UnixAddr{Name: endpoint, Net: "unix"}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, errPeerListenerFailed
	}
	l.SetUnlinkOnClose(false)
	if err := os.Chmod(endpoint, 0600); err != nil {
		_ = l.Close()
		return nil, errPeerListenerFailed
	}
	info, err := os.Lstat(endpoint)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		_ = l.Close()
		return nil, errPeerListenerFailed
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != parent.uid {
		_ = l.Close()
		return nil, errPeerListenerFailed
	}
	return &linuxUnixListener{listener: l, path: endpoint, device: uint64(st.Dev), inode: st.Ino, uid: st.Uid}, nil
}

func (l *linuxUnixListener) verifyIdentity() error {
	if l == nil || l.listener == nil {
		return errPeerListenerFailed
	}
	info, err := os.Lstat(l.path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return errPeerListenerFailed
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != l.device || st.Ino != l.inode || st.Uid != l.uid {
		return errPeerListenerFailed
	}
	return nil
}

func unlinkReapedLinuxSocket(controlDir string, expected *linuxUnixListener, oldWorkerReaped bool) error {
	if !oldWorkerReaped || expected == nil || filepath.Dir(expected.path) != filepath.Clean(controlDir) || !linuxEndpointPattern.MatchString(filepath.Base(expected.path)) {
		return errPeerListenerFailed
	}
	dirFD, err := unix.Open(controlDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errPeerListenerFailed
	}
	defer unix.Close(dirFD)
	var dir unix.Stat_t
	if err := unix.Fstat(dirFD, &dir); err != nil || dir.Uid != expected.uid || dir.Mode&unix.S_IFMT != unix.S_IFDIR || dir.Mode&0777 != 0700 {
		return errPeerListenerFailed
	}
	var current unix.Stat_t
	if err := unix.Fstatat(dirFD, filepath.Base(expected.path), &current, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		current.Mode&unix.S_IFMT != unix.S_IFSOCK || current.Mode&0777 != 0600 || current.Uid != expected.uid ||
		uint64(current.Dev) != expected.device || current.Ino != expected.inode {
		return errPeerListenerFailed
	}
	if err := unix.Unlinkat(dirFD, filepath.Base(expected.path), 0); err != nil {
		return errPeerListenerFailed
	}
	return nil
}

func authenticateLinuxPeer(conn *net.UnixConn, l *linuxUnixListener, parent *linuxPeerParent, ops *linuxPeerOps) (authenticatedIPCPeer, error) {
	if conn == nil || l == nil || parent == nil {
		return authenticatedIPCPeer{}, errPeerRejected
	}
	if err := l.verifyIdentity(); err != nil {
		return authenticatedIPCPeer{}, errPeerListenerFailed
	}
	if parent.checkAlive() != nil {
		return authenticatedIPCPeer{}, errPeerParentGone
	}
	selected := realLinuxPeerOps
	if ops != nil {
		selected = *ops
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return authenticatedIPCPeer{}, errPeerRejected
	}
	var cred *unix.Ucred
	var queryErr error
	if err := raw.Control(func(fd uintptr) { cred, queryErr = selected.getPeerCred(int(fd)) }); err != nil || queryErr != nil || cred == nil {
		return authenticatedIPCPeer{}, errPeerRejected
	}
	if int(cred.Pid) != parent.pid || cred.Uid != parent.uid {
		return authenticatedIPCPeer{}, errPeerRejected
	}
	if err := parent.checkAlive(); err != nil {
		return authenticatedIPCPeer{}, errPeerParentGone
	}
	return newAuthenticatedIPCPeer(conn, parent, uint32(cred.Pid), "uid:"+strconv.FormatUint(uint64(cred.Uid), 10))
}

// validateLinuxWorkerAncestry is called once after bootstrap and before the
// listener starts. The actual socket peer is independently checked per accept.
func validateLinuxWorkerAncestry(parent *linuxPeerParent) error {
	if parent == nil || os.Getppid() != parent.pid || parent.checkAlive() != nil {
		return errPeerRejected
	}
	return nil
}

func readLinuxBootstrapFD(fd int, timeout time.Duration) (peerBootstrap, error) {
	if fd < 0 || timeout <= 0 {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	deadline := time.Now().Add(timeout)
	readExact := func(dst []byte) (bool, error) {
		for len(dst) > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false, errPeerTimeout
			}
			ms := int((remaining + time.Millisecond - 1) / time.Millisecond)
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
			_, err := unix.Poll(fds, ms)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return false, errPeerBootstrapInvalid
			}
			if fds[0].Revents == 0 {
				return false, errPeerTimeout
			}
			n, readErr := unix.Read(fd, dst)
			if readErr == unix.EAGAIN || readErr == unix.EINTR {
				continue
			}
			if readErr != nil {
				return false, errPeerBootstrapInvalid
			}
			if n == 0 {
				return false, nil
			}
			dst = dst[n:]
		}
		return true, nil
	}
	var prefix [4]byte
	complete, err := readExact(prefix[:])
	if err == errPeerTimeout {
		return peerBootstrap{}, err
	}
	if err != nil || !complete {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	n := int(prefix[0])<<24 | int(prefix[1])<<16 | int(prefix[2])<<8 | int(prefix[3])
	if n <= 0 || n > peerBootstrapMaxBody {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	body := make([]byte, n)
	complete, err = readExact(body)
	if err == errPeerTimeout {
		return peerBootstrap{}, err
	}
	if err != nil || !complete {
		return peerBootstrap{}, errPeerBootstrapInvalid
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return peerBootstrap{}, errPeerTimeout
		}
		ms := int((remaining + time.Millisecond - 1) / time.Millisecond)
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
		_, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		if fds[0].Revents == 0 {
			return peerBootstrap{}, errPeerTimeout
		}
		var extra [1]byte
		got, readErr := unix.Read(fd, extra[:])
		if readErr == unix.EAGAIN || readErr == unix.EINTR {
			continue
		}
		if readErr != nil {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		if got != 0 {
			return peerBootstrap{}, errPeerBootstrapInvalid
		}
		break
	}
	return decodePeerBootstrapBody(body)
}

func acceptLinuxPeer(ctx context.Context, l *linuxUnixListener, parent *linuxPeerParent, ops *linuxPeerOps) (authenticatedIPCPeer, error) {
	if l == nil || l.listener == nil {
		return authenticatedIPCPeer{}, errPeerListenerFailed
	}
	for {
		if err := ctx.Err(); err != nil {
			return authenticatedIPCPeer{}, errPeerCancelled
		}
		if err := parent.checkAlive(); err != nil {
			return authenticatedIPCPeer{}, errPeerParentGone
		}
		if err := l.listener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return authenticatedIPCPeer{}, errPeerListenerFailed
		}
		conn, err := l.listener.AcceptUnix()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return authenticatedIPCPeer{}, errPeerCancelled
			}
			return authenticatedIPCPeer{}, errPeerListenerFailed
		}
		peer, authErr := authenticateLinuxPeer(conn, l, parent, ops)
		if authErr != nil {
			_ = conn.Close()
			return authenticatedIPCPeer{}, authErr
		}
		return peer, nil
	}
}

func watchLinuxParent(ctx context.Context, parent *linuxPeerParent, closeWorkerResources func() error) <-chan error {
	done := make(chan error, 1)
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				done <- errPeerCancelled
				return
			case <-t.C:
				if err := parent.checkAlive(); err != nil {
					if closeErr := closeWorkerResources(); closeErr != nil {
						done <- peerFailure{code: errPeerListenerFailed, stage: "parent-exit-cleanup", cause: closeErr}
						return
					}
					done <- errPeerParentGone
					return
				}
			}
		}
	}()
	return done
}

func readLinuxPeerFrame(conn *net.UnixConn, timeout time.Duration) ([]byte, error) {
	if conn == nil {
		return nil, errPeerRejected
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})
	frame := make([]byte, 0, peerBootstrapMaxFrame)
	tmp := make([]byte, peerBootstrapMaxFrame)
	// Linux allows up to SCM_MAX_FD (253) descriptors per rights message;
	// leave room for the full control record so every installed FD is visible
	// to the cleanup loop, even when a peer deliberately sends many handles.
	oob := make([]byte, 4096)
	need := 4
	for len(frame) < need {
		n, oobn, flags, _, err := conn.ReadMsgUnix(tmp, oob)
		if err != nil {
			return nil, errPeerRejected
		}
		if oobn != 0 {
			messages, parseErr := unix.ParseSocketControlMessage(oob[:oobn])
			if parseErr == nil {
				for i := range messages {
					fds, rightsErr := unix.ParseUnixRights(&messages[i])
					if rightsErr == nil {
						for _, receivedFD := range fds {
							_ = unix.Close(receivedFD)
						}
					}
				}
			}
			// A truncated SCM_RIGHTS tail is closed by the kernel when it
			// cannot fit in the receiver's control buffer. All complete rights
			// records above are explicitly closed before rejecting the frame.
			return nil, errPeerRejected
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n == 0 || len(frame)+n > peerBootstrapMaxFrame {
			return nil, errPeerRejected
		}
		frame = append(frame, tmp[:n]...)
		if len(frame) >= 4 && need == 4 {
			bodyLen := int(frame[0])<<24 | int(frame[1])<<16 | int(frame[2])<<8 | int(frame[3])
			if bodyLen <= 0 || bodyLen > peerBootstrapMaxBody {
				return nil, errPeerRejected
			}
			need = 4 + bodyLen
		}
	}
	if len(frame) != need {
		return nil, errPeerRejected
	}
	return append([]byte(nil), frame[4:]...), nil
}

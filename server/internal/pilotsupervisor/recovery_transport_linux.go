//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

var errRecoveryTransportRejected = errors.New("pilot recovery transport rejected")
var errRecoveryAckDeliveryUncertain = errors.New("pilot recovery ACK delivery uncertain; reap peer before retry")

// recoveryBootstrapTransport owns the inherited one-shot SOCK_SEQPACKET FD.
// expected must come from the launcher's protected configuration, not a frame.
type recoveryBootstrapTransport struct {
	file            *os.File
	expected        unix.Ucred
	closeReceivedFD func(int) error
	used            atomic.Bool
}

func newRecoveryBootstrapTransport(file *os.File, expected unix.Ucred) (*recoveryBootstrapTransport, error) {
	return newRecoveryBootstrapTransportForParent(file, expected, os.Getppid())
}

func newRecoveryBootstrapTransportForParent(file *os.File, expected unix.Ucred, parentPID int) (*recoveryBootstrapTransport, error) {
	if file == nil {
		return nil, errRecoveryTransportRejected
	}
	closeOnError := func() (*recoveryBootstrapTransport, error) {
		_ = file.Close()
		return nil, errRecoveryTransportRejected
	}
	if expected.Pid <= 0 || parentPID <= 0 || expected.Pid != int32(parentPID) {
		return closeOnError()
	}
	fd := int(file.Fd())
	if fd < 0 {
		return closeOnError()
	}
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return closeOnError()
	}
	typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_SEQPACKET {
		return closeOnError()
	}
	peer, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || !sameUcred(peer, &expected) {
		return closeOnError()
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return closeOnError()
	}
	return &recoveryBootstrapTransport{file: file, expected: expected, closeReceivedFD: unix.Close}, nil
}

func sameUcred(left, right *unix.Ucred) bool {
	return left != nil && right != nil && left.Pid == right.Pid && left.Uid == right.Uid && left.Gid == right.Gid
}

func validRecoveryCredentialSet(credentials []*unix.Ucred, expected *unix.Ucred) bool {
	return len(credentials) == 1 && sameUcred(credentials[0], expected)
}

func (t *recoveryBootstrapTransport) close() error {
	if t == nil || t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

func (t *recoveryBootstrapTransport) receiveRequest() (recoveryBootstrapRequest, error) {
	return t.receiveRequestContext(context.Background())
}

func (t *recoveryBootstrapTransport) receiveRequestContext(ctx context.Context) (recoveryBootstrapRequest, error) {
	return t.receiveRequestContextWithControlSize(ctx, 4096)
}

func (t *recoveryBootstrapTransport) receiveRequestContextWithControlSize(ctx context.Context, controlSize int) (recoveryBootstrapRequest, error) {
	if t == nil || t.file == nil {
		return recoveryBootstrapRequest{}, errRecoveryTransportRejected
	}
	if controlSize <= 0 {
		return recoveryBootstrapRequest{}, errRecoveryTransportRejected
	}
	packet := make([]byte, recoveryBootstrapMaxPacket)
	control := make([]byte, controlSize)
	if err := waitRecoveryFD(ctx, int(t.file.Fd()), unix.POLLIN); err != nil {
		return recoveryBootstrapRequest{}, err
	}
	n, controlN, flags, _, err := unix.Recvmsg(int(t.file.Fd()), packet, control, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return recoveryBootstrapRequest{}, err
	}
	receivedFDs, credentials, controlErr := inspectRecoveryControl(control[:controlN])
	for _, fd := range receivedFDs {
		_ = t.closeReceivedFD(fd)
	}
	if controlErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || len(receivedFDs) != 0 ||
		!validRecoveryCredentialSet(credentials, &t.expected) || n == 0 {
		return recoveryBootstrapRequest{}, errRecoveryTransportRejected
	}
	return decodeRecoveryBootstrap(packet[:n])
}

func inspectRecoveryControl(control []byte) ([]int, []*unix.Ucred, error) {
	var receivedFDs []int
	var credentials []*unix.Ucred
	invalid := false
	remaining := control
	for len(remaining) > 0 {
		if len(remaining) < unix.CmsgLen(0) {
			for _, b := range remaining {
				if b != 0 {
					return receivedFDs, credentials, errRecoveryTransportRejected
				}
			}
			break // zero alignment padding after the final control message
		}
		header, data, tail, err := unix.ParseOneSocketControlMessage(remaining)
		if err != nil {
			return receivedFDs, credentials, errRecoveryTransportRejected
		}
		message := unix.SocketControlMessage{Header: header, Data: data}
		switch {
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS:
			fds, parseErr := unix.ParseUnixRights(&message)
			if parseErr != nil {
				invalid = true
			} else {
				receivedFDs = append(receivedFDs, fds...)
				invalid = true
			}
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_CREDENTIALS:
			credential, parseErr := unix.ParseUnixCredentials(&message)
			if parseErr != nil {
				invalid = true
			} else {
				credentials = append(credentials, credential)
			}
		default:
			invalid = true
		}
		if len(tail) == len(remaining) {
			return receivedFDs, credentials, errRecoveryTransportRejected
		}
		remaining = tail
	}
	if invalid {
		return receivedFDs, credentials, errRecoveryTransportRejected
	}
	return receivedFDs, credentials, nil
}

func (t *recoveryBootstrapTransport) sendAck(ack recoveryBootstrapAck) error {
	return t.sendAckContext(context.Background(), ack)
}

func (t *recoveryBootstrapTransport) sendAckContext(ctx context.Context, ack recoveryBootstrapAck) error {
	if t == nil || t.file == nil {
		return errRecoveryTransportRejected
	}
	packet, err := encodeRecoveryBootstrapAck(ack)
	if err != nil {
		return errRecoveryAckDeliveryUncertain
	}
	fd := int(t.file.Fd())
	if err := waitRecoveryFD(ctx, fd, unix.POLLOUT); err != nil {
		return errRecoveryAckDeliveryUncertain
	}
	n, err := unix.SendmsgN(fd, packet, nil, nil, unix.MSG_DONTWAIT)
	if err != nil || n != len(packet) {
		return errRecoveryAckDeliveryUncertain
	}
	return nil
}

// serveRecoveryBootstrap accepts exactly one authenticated recovery request.
// Malformed, unauthenticated, or descriptor-bearing packets get no ACK.
func (t *recoveryBootstrapTransport) serveRecoveryBootstrap(ctx context.Context, ledger recoveryBootstrapLedger) error {
	if t == nil || t.file == nil || !t.used.CompareAndSwap(false, true) {
		return errRecoveryTransportRejected
	}
	defer t.close()
	if ctx == nil {
		return errRecoveryTransportRejected
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := t.receiveRequestContext(ctx)
	if err != nil {
		return err
	}
	var pending [1]unix.PollFd
	pending[0] = unix.PollFd{Fd: int32(t.file.Fd()), Events: unix.POLLIN}
	if n, err := unix.Poll(pending[:], 0); err != nil || (n > 0 && pending[0].Revents&unix.POLLIN != 0) {
		return errRecoveryTransportRejected
	}
	return t.sendAckContext(ctx, handleRecoveryBootstrap(ctx, request, ledger))
}

func waitRecoveryFD(ctx context.Context, fd int, events int16) error {
	deadline := time.Now().Add(2 * time.Second)
	if ctx == nil {
		return errRecoveryTransportRejected
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errRecoveryTransportRejected
		}
		interval := 50 * time.Millisecond
		if remaining < interval {
			interval = remaining
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: events}}
		_, err := unix.Poll(poll, int(interval/time.Millisecond))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if poll[0].Revents&(events|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			if poll[0].Revents&unix.POLLNVAL != 0 {
				return errRecoveryTransportRejected
			}
			return nil
		}
	}
}

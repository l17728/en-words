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

var (
	errBootstrapTransportRejected = errors.New("pilot bootstrap transport rejected")
	errBootstrapHandoffRejected   = errors.New("pilot bootstrap rejected")
	errBootstrapPIDFDRejected     = errors.New("pilot bootstrap pidfd rejected")
	errBootstrapAckUncertain      = errors.New("pilot bootstrap ACK delivery uncertain; reap peer before retry")
)

// bootstrapHandoffLedger durably consumes a control generation, nonce, run
// epoch, and launcher-attested identity as one transaction before ACKing.
type bootstrapHandoffLedger interface {
	ConsumeBootstrap(context.Context, bootstrapHandoffRequest) (bool, error)
}

// bootstrapHandoffTransport owns the inherited one-shot control socket. Its
// expected credentials come from protected launcher configuration.
type bootstrapHandoffTransport struct {
	file            *os.File
	expected        unix.Ucred
	closeReceivedFD func(int) error
	used            atomic.Bool
}

func newBootstrapHandoffTransport(file *os.File, expected unix.Ucred) (*bootstrapHandoffTransport, error) {
	return newBootstrapHandoffTransportForParent(file, expected, os.Getppid())
}

func newBootstrapHandoffTransportForParent(file *os.File, expected unix.Ucred, parentPID int) (*bootstrapHandoffTransport, error) {
	if file == nil {
		return nil, errBootstrapTransportRejected
	}
	closeOnError := func() (*bootstrapHandoffTransport, error) {
		if err := file.Close(); err != nil {
			return nil, errBootstrapAckUncertain
		}
		return nil, errBootstrapTransportRejected
	}
	if expected.Pid <= 0 || parentPID <= 0 || expected.Pid != int32(parentPID) {
		return closeOnError()
	}
	fd := int(file.Fd())
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
	return &bootstrapHandoffTransport{file: file, expected: expected, closeReceivedFD: unix.Close}, nil
}

func (t *bootstrapHandoffTransport) close() error {
	if t == nil || t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

func (t *bootstrapHandoffTransport) receiveRequest(ctx context.Context) (bootstrapHandoffRequest, *os.File, error) {
	if t == nil || t.file == nil || ctx == nil {
		return bootstrapHandoffRequest{}, nil, errBootstrapTransportRejected
	}
	if err := waitRecoveryFD(ctx, int(t.file.Fd()), unix.POLLIN); err != nil {
		return bootstrapHandoffRequest{}, nil, err
	}
	packet := make([]byte, bootstrapHandoffMaxPacket)
	control := make([]byte, 4096)
	n, controlN, flags, _, err := unix.Recvmsg(int(t.file.Fd()), packet, control, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return bootstrapHandoffRequest{}, nil, errBootstrapTransportRejected
	}
	fds, credentials, controlErr := inspectBootstrapControl(control[:controlN], true)
	closeFDs := func() error {
		var closeErr error
		for _, fd := range fds {
			if err := t.closeReceivedFD(fd); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		fds = nil
		return closeErr
	}
	if controlErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 ||
		len(fds) != 1 || !validRecoveryCredentialSet(credentials, &t.expected) || n == 0 {
		if closeFDs() != nil {
			return bootstrapHandoffRequest{}, nil, errBootstrapAckUncertain
		}
		return bootstrapHandoffRequest{}, nil, errBootstrapTransportRejected
	}
	request, decodeErr := decodeBootstrapHandoff(packet[:n])
	if decodeErr != nil {
		if closeFDs() != nil {
			return bootstrapHandoffRequest{}, nil, errBootstrapAckUncertain
		}
		return bootstrapHandoffRequest{}, nil, errBootstrapTransportRejected
	}
	var pending [1]unix.PollFd
	pending[0] = unix.PollFd{Fd: int32(t.file.Fd()), Events: unix.POLLIN}
	if count, pollErr := unix.Poll(pending[:], 0); pollErr != nil || (count > 0 && pending[0].Revents&unix.POLLIN != 0) {
		if closeFDs() != nil {
			return bootstrapHandoffRequest{}, nil, errBootstrapAckUncertain
		}
		return bootstrapHandoffRequest{}, nil, errBootstrapTransportRejected
	}
	fdFlags, flagErr := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0)
	if flagErr != nil || fdFlags&unix.FD_CLOEXEC == 0 || !bootstrapPIDFDAlive(fds[0]) {
		if closeFDs() != nil {
			return bootstrapHandoffRequest{}, nil, errBootstrapAckUncertain
		}
		return request, nil, errBootstrapPIDFDRejected
	}
	pidfd := os.NewFile(uintptr(fds[0]), "gateway-pidfd")
	if pidfd == nil {
		if closeFDs() != nil {
			return bootstrapHandoffRequest{}, nil, errBootstrapAckUncertain
		}
		return bootstrapHandoffRequest{}, nil, errBootstrapTransportRejected
	}
	fds = nil // ownership transferred to pidfd
	return request, pidfd, nil
}

func inspectBootstrapControl(control []byte, requireOneFD bool) ([]int, []*unix.Ucred, error) {
	var fds []int
	var credentials []*unix.Ucred
	invalid := false
	remaining := control
	for len(remaining) > 0 {
		if len(remaining) < unix.CmsgLen(0) {
			for _, b := range remaining {
				if b != 0 {
					return fds, credentials, errBootstrapTransportRejected
				}
			}
			break
		}
		header, data, tail, err := unix.ParseOneSocketControlMessage(remaining)
		if err != nil {
			return fds, credentials, errBootstrapTransportRejected
		}
		message := unix.SocketControlMessage{Header: header, Data: data}
		switch {
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS:
			rights, parseErr := unix.ParseUnixRights(&message)
			if parseErr != nil {
				invalid = true
			} else {
				fds = append(fds, rights...)
				if len(rights) != 1 {
					invalid = true
				}
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
			return fds, credentials, errBootstrapTransportRejected
		}
		remaining = tail
	}
	if invalid || len(credentials) != 1 || (requireOneFD && len(fds) != 1) || (!requireOneFD && len(fds) != 0) {
		return fds, credentials, errBootstrapTransportRejected
	}
	return fds, credentials, nil
}

func bootstrapPIDFDAlive(fd int) bool {
	// A readiness poll alone accepts many ordinary descriptors (for example an
	// empty pipe). pidfd_send_signal with signal 0 proves the descriptor type
	// without sending a signal; EPERM is also proof of a live pidfd when the
	// Reaper intentionally lacks permission to signal the Gateway.
	if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil && err != unix.EPERM {
		return false
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
	count, err := unix.Poll(poll, 0)
	return err == nil && count == 0 && poll[0].Revents == 0
}

func (t *bootstrapHandoffTransport) sendAck(ctx context.Context, ack bootstrapHandoffAck) error {
	if t == nil || t.file == nil || ctx == nil {
		return errBootstrapAckUncertain
	}
	packet, err := encodeBootstrapHandoffAck(ack)
	if err != nil {
		return errBootstrapAckUncertain
	}
	fd := int(t.file.Fd())
	if err = waitRecoveryFD(ctx, fd, unix.POLLOUT); err != nil {
		return errBootstrapAckUncertain
	}
	n, err := unix.SendmsgN(fd, packet, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil || n != len(packet) {
		return errBootstrapAckUncertain
	}
	return nil
}

func (t *bootstrapHandoffTransport) serve(ctx context.Context, ledger bootstrapHandoffLedger) (*os.File, error) {
	if t == nil || t.file == nil || ledger == nil || ctx == nil || !t.used.CompareAndSwap(false, true) {
		return nil, errBootstrapTransportRejected
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, pidfd, err := t.receiveRequest(ctx)
	if errors.Is(err, errBootstrapPIDFDRejected) {
		ack := bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK", Generation: request.Generation,
			LaunchNonce: request.LaunchNonce, Result: "rejected"}
		if ackErr := t.sendAck(ctx, ack); ackErr != nil {
			return nil, t.closeOnFailure(ackErr)
		}
		return nil, t.closeOnFailure(errBootstrapHandoffRejected)
	}
	if err != nil {
		return nil, t.closeOnFailure(err)
	}
	ack := bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK", Generation: request.Generation,
		LaunchNonce: request.LaunchNonce, Result: "rejected"}
	if !bootstrapPIDFDAlive(int(pidfd.Fd())) {
		if err = t.sendAck(ctx, ack); err != nil {
			return nil, t.closeOnFailure(t.closePIDFDOnFailure(pidfd, err))
		}
		return nil, t.closeOnFailure(t.closePIDFDOnFailure(pidfd, errBootstrapHandoffRejected))
	}
	consumed, ledgerErr := ledger.ConsumeBootstrap(ctx, request)
	if ledgerErr != nil {
		ack.Result = "restart_required"
	} else if consumed && bootstrapPIDFDAlive(int(pidfd.Fd())) {
		ack.Result = "ok"
	} else if consumed {
		ack.Result = "restart_required"
	}
	if err = t.sendAck(ctx, ack); err != nil {
		return nil, t.closeOnFailure(t.closePIDFDOnFailure(pidfd, err))
	}
	if ack.Result != "ok" {
		failure := errBootstrapHandoffRejected
		if ack.Result == "restart_required" {
			failure = errBootstrapAckUncertain
		}
		return nil, t.closeOnFailure(t.closePIDFDOnFailure(pidfd, failure))
	}
	// The authenticated control socket remains open for REGISTER, UNREGISTER,
	// and CONTAINMENT_EMPTY. The caller takes ownership of the pinned pidfd.
	return pidfd, nil
}

func (t *bootstrapHandoffTransport) closePIDFDOnFailure(pidfd *os.File, cause error) error {
	if pidfd == nil {
		return cause
	}
	if err := pidfd.Close(); err != nil {
		return errBootstrapAckUncertain
	}
	return cause
}

func (t *bootstrapHandoffTransport) closeOnFailure(cause error) error {
	if err := t.close(); err != nil {
		return errBootstrapAckUncertain
	}
	return cause
}

// sendBootstrapHandoff transfers exactly one already-pinned Gateway pidfd and
// one canonical BOOTSTRAP packet. Callers must not retry after an error.
func sendBootstrapHandoff(ctx context.Context, channel *os.File, request bootstrapHandoffRequest, gatewayPIDFD int, expectedSocketPeer, expectedAckPeer unix.Ucred) error {
	if ctx == nil || channel == nil || channel.Fd() == ^uintptr(0) || gatewayPIDFD < 0 || !bootstrapPIDFDAlive(gatewayPIDFD) {
		return errBootstrapTransportRejected
	}
	if err := validateBootstrapControlPeer(int(channel.Fd()), expectedSocketPeer); err != nil {
		return errBootstrapTransportRejected
	}
	packet, err := encodeBootstrapHandoff(request)
	if err != nil {
		return err
	}
	fd := int(channel.Fd())
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = waitRecoveryFD(ctx, fd, unix.POLLOUT); err != nil {
		_ = channel.Close()
		return errBootstrapAckUncertain
	}
	oob := unix.UnixRights(gatewayPIDFD)
	n, err := unix.SendmsgN(fd, packet, oob, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil || n != len(packet) {
		_ = channel.Close()
		return errBootstrapAckUncertain
	}
	ack, err := receiveBootstrapHandoffAck(ctx, channel, request, expectedAckPeer)
	if err != nil {
		_ = channel.Close()
		return errBootstrapAckUncertain
	}
	if ack.Result == "ok" {
		return nil
	}
	if ack.Result == "rejected" {
		if err := channel.Close(); err != nil {
			return errBootstrapAckUncertain
		}
		return errBootstrapHandoffRejected
	}
	_ = channel.Close()
	return errBootstrapAckUncertain
}

func validateBootstrapControlPeer(fd int, expected unix.Ucred) error {
	if expected.Pid <= 0 {
		return errBootstrapTransportRejected
	}
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return errBootstrapTransportRejected
	}
	typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_SEQPACKET {
		return errBootstrapTransportRejected
	}
	peer, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || !sameUcred(peer, &expected) {
		return errBootstrapTransportRejected
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return errBootstrapTransportRejected
	}
	return nil
}

func receiveBootstrapHandoffAck(ctx context.Context, channel *os.File, request bootstrapHandoffRequest, expected unix.Ucred) (bootstrapHandoffAck, error) {
	if ctx == nil || channel == nil || channel.Fd() == ^uintptr(0) {
		return bootstrapHandoffAck{}, errBootstrapAckUncertain
	}
	fd := int(channel.Fd())
	if err := waitRecoveryFD(ctx, fd, unix.POLLIN); err != nil {
		return bootstrapHandoffAck{}, errBootstrapAckUncertain
	}
	var packet [bootstrapHandoffMaxPacket]byte
	control := make([]byte, 4096)
	n, controlN, flags, _, err := unix.Recvmsg(fd, packet[:], control, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	fds, credentials, parseErr := inspectBootstrapControl(control[:controlN], false)
	var closeErr error
	for _, receivedFD := range fds {
		if err := unix.Close(receivedFD); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if err != nil || parseErr != nil || closeErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n == 0 ||
		!validRecoveryCredentialSet(credentials, &expected) {
		return bootstrapHandoffAck{}, errBootstrapAckUncertain
	}
	ack, err := decodeBootstrapHandoffAck(packet[:n])
	if err != nil || !bootstrapHandoffAckMatches(request, ack) {
		return bootstrapHandoffAck{}, errBootstrapAckUncertain
	}
	extra, peerEOF, probeErr := bootstrapControlHasPendingRecord(fd)
	if probeErr != nil || extra || (peerEOF && ack.Result == "ok") {
		return ack, errBootstrapAckUncertain
	}
	return ack, nil
}

// bootstrapControlHasPendingRecord separates a queued zero-length SOCK_SEQPACKET
// record from EOF. SO_PASSCRED makes even an empty peer record carry ancillary
// credentials; any duplicated SCM_RIGHTS received while peeking is closed.
func bootstrapControlHasPendingRecord(fd int) (extra, peerEOF bool, err error) {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP}}
	count, err := unix.Poll(poll, 0)
	if err != nil {
		return false, false, err
	}
	if count == 0 || poll[0].Revents == 0 {
		return false, false, nil
	}
	if poll[0].Revents & ^(int16(unix.POLLIN|unix.POLLHUP)) != 0 {
		return false, false, errBootstrapAckUncertain
	}
	if poll[0].Revents&unix.POLLIN == 0 {
		return false, poll[0].Revents&unix.POLLHUP != 0, nil
	}
	var packet [bootstrapHandoffMaxPacket]byte
	control := make([]byte, 4096)
	n, controlN, flags, _, peekErr := unix.Recvmsg(fd, packet[:], control, unix.MSG_DONTWAIT|unix.MSG_PEEK|unix.MSG_CMSG_CLOEXEC)
	var fds []int
	var parseErr error
	if controlN > 0 {
		fds, _, parseErr = inspectBootstrapControl(control[:controlN], false)
	}
	var closeErr error
	for _, receivedFD := range fds {
		if closeErr == nil {
			closeErr = unix.Close(receivedFD)
		} else {
			_ = unix.Close(receivedFD)
		}
	}
	if peekErr != nil && peekErr != unix.EAGAIN && peekErr != unix.EWOULDBLOCK {
		return false, false, peekErr
	}
	if parseErr != nil || closeErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return false, false, errBootstrapAckUncertain
	}
	if peekErr == unix.EAGAIN || peekErr == unix.EWOULDBLOCK {
		return false, false, nil
	}
	if n > 0 || controlN > 0 {
		return true, false, nil
	}
	if poll[0].Revents&unix.POLLHUP != 0 {
		return false, true, nil
	}
	// A zero-byte, ancillary-free record is not a valid ACK-channel state.
	return true, false, nil
}

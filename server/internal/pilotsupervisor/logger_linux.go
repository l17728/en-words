//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const hostLoggerACKTimeout = 2 * time.Second

var errHostLoggerUnavailable = errors.New("verified_io_failed")

// hostLoggerClient owns one already-connected AF_UNIX SOCK_SEQPACKET FD passed
// by the trusted launcher. It is single-flight and becomes unusable after any
// ambiguous or malformed exchange; it never reconnects or retries internally.
type hostLoggerClient struct {
	mu         sync.Mutex
	file       *os.File
	fd         int
	oobSize    int
	poisoned   bool
	rateTokens float64
	rateLast   time.Time
	contextID  string
	generation uint64
}

// hostLoggerPeerIdentity must come from the trusted launcher configuration,
// never from the worker's IPC request. SO_PEERCRED then binds the connected FD
// to that configured host logger process identity.
type hostLoggerPeerIdentity struct {
	PID            int32
	UID            uint32
	GID            uint32
	EventProcessID string
}

// hostLoggerServiceIdentity comes from protected, read-only host service
// configuration. Logger PID is deliberately not trusted as an identity input;
// the dedicated UID/GID exclusivity invariant is checked during host preflight.
type hostLoggerServiceIdentity struct {
	UID uint32
	GID uint32
}

func newHostLoggerClient(file *os.File, expected hostLoggerPeerIdentity) (*hostLoggerClient, error) {
	if file == nil || file.Fd() == ^uintptr(0) {
		return nil, errHostLoggerUnavailable
	}
	if expected.PID <= 0 {
		return nil, errHostLoggerUnavailable
	}
	fd := int(file.Fd())
	typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || typ != unix.SOCK_SEQPACKET {
		return nil, errHostLoggerUnavailable
	}
	peer, err := unix.Getpeername(fd)
	if err != nil {
		return nil, errHostLoggerUnavailable
	}
	if _, ok := peer.(*unix.SockaddrUnix); !ok {
		return nil, errHostLoggerUnavailable
	}
	credentials, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || credentials == nil || credentials.Pid != expected.PID ||
		credentials.Uid != expected.UID || credentials.Gid != expected.GID {
		return nil, errHostLoggerUnavailable
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		return nil, errHostLoggerUnavailable
	}
	return &hostLoggerClient{file: file, fd: fd, oobSize: 4096}, nil
}

func connectHostLoggerClient(ctx context.Context, endpoint string, expected hostLoggerServiceIdentity, contextID string, generation uint64) (*hostLoggerClient, error) {
	if ctx == nil || endpoint == "" || !strings.HasPrefix(endpoint, "\x00") ||
		!hostLogHex32.MatchString(contextID) || generation == 0 {
		return nil, errHostLoggerUnavailable
	}
	overallDeadline := time.Now().Add(6 * time.Second)
	var lastErr error
	for attempt := 0; attempt < 3 && time.Now().Before(overallDeadline); attempt++ {
		attemptDeadline := time.Now().Add(hostLoggerACKTimeout)
		if attemptDeadline.After(overallDeadline) {
			attemptDeadline = overallDeadline
		}
		client, err := connectHostLoggerClientOnce(ctx, endpoint, expected, contextID, generation, attemptDeadline)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, errPeerCancelled
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errPeerTimeout
}

func connectHostLoggerClientOnce(ctx context.Context, endpoint string, expected hostLoggerServiceIdentity, contextID string, generation uint64, deadline time.Time) (*hostLoggerClient, error) {
	return connectHostLoggerClientOnceWithWait(ctx, endpoint, expected, contextID, generation, deadline, loggerWait)
}

func connectHostLoggerClientOnceWithWait(ctx context.Context, endpoint string, expected hostLoggerServiceIdentity, contextID string, generation uint64, deadline time.Time, wait func(context.Context, int, int16, time.Time) error) (*hostLoggerClient, error) {
	if wait == nil {
		return nil, errHostLoggerUnavailable
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, errHostLoggerUnavailable
	}
	if err = unix.Connect(fd, &unix.SockaddrUnix{Name: endpoint}); err != nil && err != unix.EINPROGRESS && err != unix.EAGAIN {
		_ = unix.Close(fd)
		return nil, errHostLoggerUnavailable
	}
	if err = wait(ctx, fd, unix.POLLOUT, deadline); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	soerr, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil || soerr != 0 {
		_ = unix.Close(fd)
		return nil, errHostLoggerUnavailable
	}
	file := os.NewFile(uintptr(fd), "host-logger-channel")
	client, err := newHostLoggerClientWithReady(ctx, file, expected, contextID, generation, deadline)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return client, nil
}

func newHostLoggerClientWithReady(ctx context.Context, file *os.File, expected hostLoggerServiceIdentity, contextID string, generation uint64, deadline time.Time) (*hostLoggerClient, error) {
	if file == nil || file.Fd() == ^uintptr(0) || !hostLogHex32.MatchString(contextID) || generation == 0 {
		return nil, errHostLoggerUnavailable
	}
	fd := int(file.Fd())
	if !hostLoggerSeqpacket(fd) {
		return nil, errHostLoggerUnavailable
	}
	peer, err := unix.Getpeername(fd)
	if err != nil {
		return nil, errHostLoggerUnavailable
	}
	if _, ok := peer.(*unix.SockaddrUnix); !ok {
		return nil, errHostLoggerUnavailable
	}
	credentials, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || credentials == nil || credentials.Uid != expected.UID || credentials.Gid != expected.GID {
		return nil, errHostLoggerUnavailable
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, errHostLoggerUnavailable
	}
	client := &hostLoggerClient{file: file, fd: fd, oobSize: 4096, contextID: contextID, generation: generation}
	frame, err := client.receivePacket(ctx, deadline)
	if err != nil || validateHostLoggerChannelReady(frame, contextID, generation) != nil {
		client.poisoned = true
		return nil, errHostLoggerUnavailable
	}
	return client, nil
}

func (c *hostLoggerClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return nil
	}
	file := c.file
	c.file = nil
	c.fd = -1
	c.poisoned = true
	return file.Close()
}

func (c *hostLoggerClient) SendCritical(ctx context.Context, event hostReaperEvent) error {
	if c == nil || ctx == nil {
		return errHostLoggerUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.file == nil || c.fd < 0 {
		return errHostLoggerUnavailable
	}
	if c.generation != 0 && event.ProcessID != hostLoggerEventProcessID(c.contextID, c.generation) {
		return errHostLoggerProtocol
	}
	frame, expected, err := encodeHostLoggerEnvelope(event)
	if err != nil {
		return errHostLoggerUnavailable
	}
	deadline := time.Now().Add(hostLoggerACKTimeout)
	if err = c.waitRunRate(ctx, deadline); err != nil {
		c.poisoned = true
		return err
	}
	if err = c.sendPacket(ctx, frame, deadline); err != nil {
		c.poisoned = true
		return err
	}
	ack, err := c.receivePacket(ctx, deadline)
	if err != nil {
		c.poisoned = true
		if errors.Is(err, errPeerTimeout) || errors.Is(err, errPeerCancelled) {
			return err
		}
		return errHostLoggerUnavailable
	}
	if validateHostLoggerACK(ack, expected) != nil {
		c.poisoned = true
		return errHostLoggerUnavailable
	}
	return nil
}

func (c *hostLoggerClient) waitRunRate(ctx context.Context, deadline time.Time) error {
	const ratePerSecond = 100.0
	const burst = 200.0
	now := time.Now()
	if c.rateLast.IsZero() {
		c.rateTokens = burst
		c.rateLast = now
	}
	for {
		now = time.Now()
		c.rateTokens += now.Sub(c.rateLast).Seconds() * ratePerSecond
		if c.rateTokens > burst {
			c.rateTokens = burst
		}
		c.rateLast = now
		if c.rateTokens >= 1 {
			c.rateTokens--
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errPeerTimeout
		}
		wait := time.Duration((1 - c.rateTokens) / ratePerSecond * float64(time.Second))
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return errPeerCancelled
			}
			return errPeerTimeout
		case <-timer.C:
		}
	}
}

func (c *hostLoggerClient) sendPacket(ctx context.Context, frame []byte, deadline time.Time) error {
	for {
		if err := loggerWait(ctx, c.fd, unix.POLLOUT, deadline); err != nil {
			return err
		}
		n, err := unix.SendmsgN(c.fd, frame, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil || n != len(frame) {
			return errHostLoggerUnavailable
		}
		return nil
	}
}

func (c *hostLoggerClient) receivePacket(ctx context.Context, deadline time.Time) ([]byte, error) {
	for {
		if err := loggerWait(ctx, c.fd, unix.POLLIN, deadline); err != nil {
			return nil, err
		}
		buf := make([]byte, hostLoggerMaxRecord+1)
		oobSize := c.oobSize
		if oobSize <= 0 {
			oobSize = 4096
		}
		oob := make([]byte, oobSize)
		n, oobn, flags, _, err := unix.Recvmsg(c.fd, buf, oob, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if oobn > 0 || flags&unix.MSG_CTRUNC != 0 {
			closeReceivedLoggerRights(oob[:oobn])
			return nil, errHostLoggerUnavailable
		}
		if err != nil || n == 0 || n > hostLoggerMaxRecord || flags&unix.MSG_TRUNC != 0 {
			return nil, errHostLoggerUnavailable
		}
		return append([]byte(nil), buf[:n]...), nil
	}
}

func loggerWait(ctx context.Context, fd int, events int16, deadline time.Time) error {
	return loggerWaitWithPoll(ctx, fd, events, deadline, unix.Poll)
}

func loggerWaitWithPoll(ctx context.Context, fd int, events int16, deadline time.Time,
	pollFn func([]unix.PollFd, int) (int, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return errPeerCancelled
			}
			return errPeerTimeout
		}
		ms := 10
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return errPeerTimeout
			}
			ms = int((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		if ms > 10 {
			ms = 10
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: events | unix.POLLHUP | unix.POLLERR}}
		_, err := pollFn(poll, ms)
		if err == unix.EINTR {
			continue
		}
		if poll[0].Revents&unix.POLLHUP != 0 && events&unix.POLLIN != 0 {
			// On SOCK_SEQPACKET HUP is only a drain signal; recvmsg must consume
			// queued records and the final EOF before the channel is retired.
			return nil
		}
		if err != nil || poll[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return errHostLoggerUnavailable
		}
		if poll[0].Revents&events != 0 {
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return errPeerTimeout
			}
			return nil
		}
	}
}

func closeReceivedLoggerRights(oob []byte) {
	if len(oob) == 0 {
		return
	}
	messages, _ := unix.ParseSocketControlMessage(oob)
	for i := range messages {
		if messages[i].Header.Level != unix.SOL_SOCKET || messages[i].Header.Type != unix.SCM_RIGHTS {
			continue
		}
		fds, parseErr := unix.ParseUnixRights(&messages[i])
		if parseErr == nil {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
		}
	}
}

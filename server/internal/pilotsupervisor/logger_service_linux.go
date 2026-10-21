//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	hostLoggerControlLimit             = 1024
	hostLoggerRegisterFDs              = 1
	hostLoggerAcceptPoll               = 50 * time.Millisecond
	hostLoggerRegistrationHistoryLimit = 65536
)

var (
	errHostLoggerEOF         = errors.New("host logger peer eof")
	errHostLoggerHistoryFull = errors.New("host logger registration history full")
)

type hostLoggerRegisterRequest struct {
	Version              int    `json:"version"`
	Type                 string `json:"type"`
	ContextID            string `json:"context_id"`
	RunEpoch             string `json:"run_epoch"`
	ControlGeneration    uint64 `json:"control_generation"`
	GatewayGeneration    uint64 `json:"gateway_generation"`
	LaunchNonce          string `json:"launch_nonce"`
	ReaperPID            int32  `json:"reaper_pid"`
	ReaperUID            uint32 `json:"reaper_uid"`
	ReaperGID            uint32 `json:"reaper_gid"`
	ReaperStartTimeTicks uint64 `json:"reaper_start_time_ticks"`
	ReaperPIDNSDevice    uint64 `json:"reaper_pidns_device"`
	ReaperPIDNSInode     uint64 `json:"reaper_pidns_inode"`
	ReaperCgroupMountID  uint64 `json:"reaper_cgroup_mount_id"`
	ReaperCgroupDevice   uint64 `json:"reaper_cgroup_device"`
	ReaperCgroupInode    uint64 `json:"reaper_cgroup_inode"`
	EventProcessID       string `json:"event_process_id"`
}

type hostLoggerUnregisterRequest struct {
	Version           int    `json:"version"`
	Type              string `json:"type"`
	ContextID         string `json:"context_id"`
	ControlGeneration uint64 `json:"control_generation"`
	LaunchNonce       string `json:"launch_nonce"`
}

type hostLoggerLifecycleACK struct {
	Version           int    `json:"version"`
	Type              string `json:"type"`
	ContextID         string `json:"context_id"`
	ControlGeneration uint64 `json:"control_generation"`
	LaunchNonce       string `json:"launch_nonce"`
	Result            string `json:"result"`
}

type hostLoggerServiceContext struct {
	request         hostLoggerRegisterRequest
	peer            hostLoggerPeerIdentity
	pidfd           int
	connActive      bool
	handlerActive   bool
	eof             bool
	everPacket      bool
	failed          bool
	traceID         string
	connectAttempts int
	connectWindow   time.Time
}

type hostLoggerGenerationKey struct {
	contextID  string
	generation uint64
}

type hostLoggerServiceConfig struct {
	LoggerUID    uint32
	LoggerGID    uint32
	EndpointName string
	LogDevice    uint64
	LogMountID   uint64
}

// hostLoggerService owns the listener and registered Reaper pidfds. The
// launcher is authenticated on the inherited control socket; registrations
// are the sole source of accepted data-peer identity.
type hostLoggerService struct {
	mu                  sync.Mutex
	store               *hostLoggerStore
	listener            *os.File
	listenerFD          int
	contexts            map[string]*hostLoggerServiceContext
	byGeneration        map[hostLoggerGenerationKey]struct{}
	byEventProcess      map[string]string
	registrationCount   int
	launcher            unix.Ucred
	logger              *slog.Logger
	closed              bool
	activeConnCount     int
	handlers            sync.WaitGroup
	stateChanged        chan struct{}
	beforeReadiness     func()
	beforeWatcherRetire func()
}

func newHostLoggerService(store *hostLoggerStore, listener *os.File, launcher unix.Ucred, config hostLoggerServiceConfig) (*hostLoggerService, error) {
	if store == nil || listener == nil || listener.Fd() == ^uintptr(0) || launcher.Pid <= 0 || config.LoggerUID == 0 || config.LoggerGID == 0 ||
		config.EndpointName == "" || !strings.HasPrefix(config.EndpointName, "\x00") || config.LogMountID == 0 {
		return nil, errHostLoggerStore
	}
	if uint32(unix.Geteuid()) != config.LoggerUID || uint32(unix.Getegid()) != config.LoggerGID {
		return nil, errHostLoggerStore
	}
	fd := int(listener.Fd())
	domain, domainErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	typ, typeErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	accepting, acceptErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	name, nameErr := unix.Getsockname(fd)
	abstract, isUnix := name.(*unix.SockaddrUnix)
	canonicalName := ""
	if isUnix {
		canonicalName = abstract.Name
		if strings.HasPrefix(canonicalName, "@") {
			canonicalName = "\x00" + canonicalName[1:]
		}
	}
	if domainErr != nil || typeErr != nil || acceptErr != nil || nameErr != nil || domain != unix.AF_UNIX ||
		typ != unix.SOCK_SEQPACKET || accepting != 1 || !isUnix || abstract.Name == "" ||
		canonicalName != config.EndpointName || !(strings.HasPrefix(abstract.Name, "\x00") || strings.HasPrefix(abstract.Name, "@")) {
		return nil, errHostLoggerStore
	}
	var st unix.Stat_t
	var sx unix.Statx_t
	if unix.Fstat(store.fd, &st) != nil || uint64(st.Dev) != config.LogDevice ||
		unix.Statx(store.fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW,
			int(unix.STATX_BASIC_STATS|unix.STATX_MNT_ID), &sx) != nil || sx.Mask&unix.STATX_MNT_ID == 0 || sx.Mnt_id != config.LogMountID {
		return nil, errHostLoggerStore
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, errHostLoggerStore
	}
	return &hostLoggerService{store: store, listener: listener, listenerFD: fd, logger: slog.Default(),
		contexts: make(map[string]*hostLoggerServiceContext), byGeneration: make(map[hostLoggerGenerationKey]struct{}),
		byEventProcess: make(map[string]string), launcher: launcher}, nil
}

// run serves one authenticated launcher control channel and accepts only
// already-registered Reapers. The caller owns the control *os.File.
func (s *hostLoggerService) run(ctx context.Context, control *os.File) error {
	if s == nil || ctx == nil || control == nil || control.Fd() == ^uintptr(0) {
		return errHostLoggerStore
	}
	controlFD := int(control.Fd())
	launcher, peerErr := unix.GetsockoptUcred(controlFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
	domain, domainErr := unix.GetsockoptInt(controlFD, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if domainErr != nil || domain != unix.AF_UNIX || !hostLoggerSeqpacket(controlFD) || peerErr != nil || !sameUcred(launcher, &s.launcher) ||
		unix.SetsockoptInt(controlFD, unix.SOL_SOCKET, unix.SO_PASSCRED, 1) != nil {
		return errHostLoggerStore
	}
	if err := hostLoggerSendFrame(ctx, controlFD, []byte(`{"version":1,"type":"LOGGER_READY","result":"ok"}`), time.Now().Add(hostLoggerACKTimeout)); err != nil {
		return errHostLoggerRestartRequired
	}
	s.logLifecycle("ready", "ok", time.Now())
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	acceptDone := make(chan struct{})
	go func() { defer close(acceptDone); s.acceptLoop(serveCtx) }()
	defer func() {
		cancel()
		_ = s.listener.Close()
		<-acceptDone
		s.handlers.Wait()
		s.closeOwned()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if err := s.handleControl(ctx, controlFD); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (s *hostLoggerService) handleControl(ctx context.Context, fd int) error {
	if err := loggerWait(ctx, fd, unix.POLLIN, time.Now().Add(50*time.Millisecond)); err != nil {
		if errors.Is(err, errPeerTimeout) {
			return nil
		}
		return err
	}
	packet := make([]byte, hostLoggerControlLimit+1)
	oob := make([]byte, 4096)
	n, oobn, flags, _, err := unix.Recvmsg(fd, packet, oob, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
		return nil
	}
	fds, credentials, ctlErr := inspectLoggerControl(oob[:oobn])
	defer closeFDs(fds)
	if err != nil || n == 0 || n > hostLoggerControlLimit || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return errHostLoggerRestartRequired
	}
	if ctlErr != nil || len(credentials) != 1 || !sameUcred(credentials[0], &s.launcher) {
		return errHostLoggerRestartRequired
	}
	var head struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(packet[:n], &head) != nil || head.Version != 1 {
		return errHostLoggerRestartRequired
	}
	switch head.Type {
	case "LOGGER_REGISTER":
		var req hostLoggerRegisterRequest
		if len(fds) != hostLoggerRegisterFDs || decodeLoggerControl(packet[:n], &req) != nil {
			return errHostLoggerRestartRequired
		}
		if !validHostLoggerRegister(req) {
			return s.sendLifecycleACK(ctx, fd, req.ContextID, req.ControlGeneration, req.LaunchNonce, "rejected", "LOGGER_REGISTER_ACK")
		}
		if !hostLoggerPIDFDAlive(fds[0]) {
			return s.sendLifecycleACK(ctx, fd, req.ContextID, req.ControlGeneration, req.LaunchNonce, "rejected", "LOGGER_REGISTER_ACK")
		}
		if err := s.register(req, fds[0]); err != nil {
			if errors.Is(err, errHostLoggerHistoryFull) {
				return s.sendLifecycleACK(ctx, fd, req.ContextID, req.ControlGeneration, req.LaunchNonce, "restart_required", "LOGGER_REGISTER_ACK")
			}
			return s.sendLifecycleACK(ctx, fd, req.ContextID, req.ControlGeneration, req.LaunchNonce, "rejected", "LOGGER_REGISTER_ACK")
		}
		// register duplicates the pidfd into context ownership. The received
		// SCM_RIGHTS descriptor remains owned by this request and closeFDs will
		// close it on return.
		if err := s.sendLifecycleACK(ctx, fd, req.ContextID, req.ControlGeneration, req.LaunchNonce, "ok", "LOGGER_REGISTER_ACK"); err != nil {
			return errHostLoggerRestartRequired
		}
		s.logContextLifecycle(req.ContextID, "register", "ok", time.Now())
		return nil
	case "LOGGER_UNREGISTER":
		var req hostLoggerUnregisterRequest
		if len(fds) != 0 || decodeLoggerControl(packet[:n], &req) != nil {
			return errHostLoggerRestartRequired
		}
		result := "rejected"
		if req.Version == 1 && req.Type == "LOGGER_UNREGISTER" && hostLogHex32.MatchString(req.ContextID) &&
			req.ControlGeneration > 0 && hostLogHex32.MatchString(req.LaunchNonce) && s.unregister(req) == nil {
			result = "ok"
		}
		if err := s.sendLifecycleACK(ctx, fd, req.ContextID, req.ControlGeneration, req.LaunchNonce, result, "LOGGER_UNREGISTER_ACK"); err != nil {
			return errHostLoggerRestartRequired
		}
		s.logLifecycle("unregister", result, time.Now())
		return nil
	default:
		return errHostLoggerRestartRequired
	}
}

func (s *hostLoggerService) register(req hostLoggerRegisterRequest, pidfd int) error {
	peer := hostLoggerPeerIdentity{PID: req.ReaperPID, UID: req.ReaperUID, GID: req.ReaperGID, EventProcessID: req.EventProcessID}
	traceID := newLoggerTraceID()
	if traceID == "" {
		return errHostLoggerStore
	}
	dup, err := unix.FcntlInt(uintptr(pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return errHostLoggerStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	generationKey := hostLoggerGenerationKey{contextID: req.ContextID, generation: req.ControlGeneration}
	_, generationExists := s.byGeneration[generationKey]
	if s.registrationCount >= hostLoggerRegistrationHistoryLimit {
		_ = unix.Close(dup)
		return errHostLoggerHistoryFull
	}
	if s.closed || len(s.contexts) >= hostLoggerMaxRunBuckets || s.contexts[req.ContextID] != nil ||
		generationExists || s.byEventProcess[req.EventProcessID] != "" {
		_ = unix.Close(dup)
		return errHostLoggerStore
	}
	for _, active := range s.contexts {
		if active.peer.PID == peer.PID || (active.request.ContextID == req.ContextID && active.request.ControlGeneration == req.ControlGeneration) ||
			(active.request.ContextID == req.ContextID && active.request.LaunchNonce == req.LaunchNonce) {
			_ = unix.Close(dup)
			return errHostLoggerStore
		}
	}
	if err := s.store.RegisterRunContext(req.ContextID, peer); err != nil {
		_ = unix.Close(dup)
		return err
	}
	s.contexts[req.ContextID] = &hostLoggerServiceContext{request: req, peer: peer, pidfd: dup, traceID: traceID}
	s.byGeneration[generationKey] = struct{}{}
	s.byEventProcess[req.EventProcessID] = req.ContextID
	s.registrationCount++
	return nil
}

func (s *hostLoggerService) unregister(req hostLoggerUnregisterRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.contexts[req.ContextID]
	if c == nil || c.request.ControlGeneration != req.ControlGeneration || c.request.LaunchNonce != req.LaunchNonce ||
		c.connActive || c.handlerActive || !c.eof || c.failed {
		return errHostLoggerStore
	}
	if err := s.store.ReleaseRunContext(req.ContextID, c.peer); err != nil {
		return err
	}
	delete(s.contexts, req.ContextID)
	_ = unix.Close(c.pidfd)
	return nil
}

func (s *hostLoggerService) acceptLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		s.reapUnconnectedDeadContexts()
		poll := []unix.PollFd{{Fd: int32(s.listenerFD), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, int(hostLoggerAcceptPoll/time.Millisecond))
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil || poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		fd, _, err := unix.Accept4(s.listenerFD, unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil {
			return
		}
		c := s.claimAccepted(fd)
		if c == nil {
			_ = unix.Close(fd)
			continue
		}
		s.handlers.Add(1)
		go func() { defer s.handlers.Done(); s.serveConnection(ctx, fd, c) }()
	}
}

// A Reaper may exit after registration but before opening the data channel.
// In that state no connection handler exists to observe pidfd readiness, so
// the accept loop records the terminal EOF-equivalent state for UNREGISTER.
func (s *hostLoggerService) reapUnconnectedDeadContexts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.contexts {
		if !c.connActive && !c.eof && !c.failed && !hostLoggerPIDFDAlive(c.pidfd) {
			c.eof = true
			s.signalStateChanged()
		}
	}
}

func (s *hostLoggerService) claimAccepted(fd int) *hostLoggerServiceContext {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.contexts {
		if c.connActive || c.failed || c.everPacket || !hostLoggerPIDFDAlive(c.pidfd) || cred.Pid != c.peer.PID || cred.Uid != c.peer.UID || cred.Gid != c.peer.GID {
			continue
		}
		now := time.Now()
		if c.connectWindow.IsZero() {
			c.connectWindow = now
		}
		if c.connectAttempts >= 3 || now.Sub(c.connectWindow) > 6*time.Second {
			c.failed = true
			return nil
		}
		if unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1) != nil {
			return nil
		}
		c.connectAttempts++
		c.connActive = true
		c.handlerActive = true
		c.eof = false
		return c
	}
	return nil
}

func (s *hostLoggerService) serveConnection(parent context.Context, fd int, c *hostLoggerServiceContext) {
	ctx, cancel := context.WithCancel(parent)
	var watcherDone chan struct{}
	fdOpen := true
	released := false
	defer func() {
		// Retire and join the pidfd watcher before publishing handler completion.
		// UNREGISTER may close c.pidfd only after this point.
		if s.beforeWatcherRetire != nil {
			s.beforeWatcherRetire()
		}
		cancel()
		if watcherDone != nil {
			<-watcherDone
		}
		if fdOpen {
			_ = unix.Close(fd)
		}
		s.mu.Lock()
		if !released {
			c.connActive = false
			s.activeConnCount--
		}
		c.handlerActive = false
		s.mu.Unlock()
		s.signalStateChanged()
	}()
	s.mu.Lock()
	c.handlerActive = true
	s.activeConnCount++
	s.mu.Unlock()
	if !hostLoggerPIDFDAlive(c.pidfd) {
		drained := drainDeadHostLoggerPeer(fd)
		_ = unix.Close(fd)
		fdOpen = false
		s.mu.Lock()
		c.eof, c.connActive, c.failed = drained, false, !drained
		s.activeConnCount--
		s.mu.Unlock()
		s.signalStateChanged()
		released = true
		return
	}
	ready, err := encodeHostLoggerChannelReady(c.request.ContextID, c.request.ControlGeneration, "ok")
	if err != nil {
		return
	}
	if s.beforeReadiness != nil {
		s.beforeReadiness()
	}
	// Encoding is intentionally done before this final pidfd check so the
	// readiness send is immediately preceded by a live-child check.
	if !hostLoggerPIDFDAlive(c.pidfd) {
		drained := drainDeadHostLoggerPeer(fd)
		_ = unix.Close(fd)
		fdOpen = false
		s.mu.Lock()
		c.eof, c.connActive, c.failed = drained, false, !drained
		s.activeConnCount--
		s.mu.Unlock()
		s.signalStateChanged()
		released = true
		return
	}
	if hostLoggerSendFrame(ctx, fd, ready, time.Now().Add(hostLoggerACKTimeout)) != nil {
		// No event was admitted or sent, so this channel can be retried after
		// the handler retires and the Reaper observes the closed socket.
		return
	}
	s.logContextLifecycle(c.request.ContextID, "channel_ready", "ok", time.Now())
	watcherDone = make(chan struct{})
	go func() {
		defer close(watcherDone)
		watchHostLoggerPIDFD(ctx, cancel, c.pidfd)
	}()
	for {
		err = s.store.serveOnePacketGuarded(ctx, fd, c.request.ContextID, func() bool {
			return hostLoggerPIDFDAlive(c.pidfd)
		})
		if errors.Is(err, errHostLoggerEOF) {
			_ = unix.Close(fd)
			fdOpen = false
			s.mu.Lock()
			c.eof, c.connActive = true, false
			s.activeConnCount--
			s.mu.Unlock()
			s.signalStateChanged()
			released = true
			s.logContextLifecycle(c.request.ContextID, "channel_eof", "ok", time.Now())
			return
		}
		if errors.Is(err, errPeerCancelled) && !hostLoggerPIDFDAlive(c.pidfd) && drainDeadHostLoggerPeer(fd) {
			_ = unix.Close(fd)
			fdOpen = false
			s.mu.Lock()
			c.eof, c.connActive = true, false
			s.activeConnCount--
			s.mu.Unlock()
			s.signalStateChanged()
			released = true
			return
		}
		if err != nil {
			s.mu.Lock()
			c.failed = true
			s.mu.Unlock()
			s.logContextLifecycle(c.request.ContextID, "channel_retired", "restart_required", time.Now())
			return
		}
		s.mu.Lock()
		c.everPacket = true
		s.mu.Unlock()
	}
}

func (s *hostLoggerService) signalStateChanged() {
	if s == nil || s.stateChanged == nil {
		return
	}
	select {
	case s.stateChanged <- struct{}{}:
	default:
	}
}

// drainDeadHostLoggerPeer discards kernel-queued packets after the registered
// pidfd becomes readable. Dead-peer events are never admitted or ACKed, but the
// channel is drained to recvmsg EOF before the context can be unregistered.
func drainDeadHostLoggerPeer(fd int) bool {
	deadline := time.Now().Add(hostLoggerACKTimeout)
	for time.Now().Before(deadline) {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ms := int(time.Until(deadline) / time.Millisecond)
		if ms > 20 {
			ms = 20
		}
		_, err := unix.Poll(poll, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil || poll[0].Revents&unix.POLLNVAL != 0 {
			return false
		}
		if poll[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
			continue
		}
		data, control := make([]byte, hostLoggerMaxRecord+1), make([]byte, 4096)
		n, controlN, _, _, recvErr := unix.Recvmsg(fd, data, control, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
		fds, _, parseErr := inspectLoggerControl(control[:controlN])
		closeFDs(fds)
		if parseErr != nil || recvErr != nil && recvErr != unix.EINTR && recvErr != unix.EAGAIN && recvErr != unix.EWOULDBLOCK {
			return false
		}
		if recvErr == unix.EINTR || recvErr == unix.EAGAIN || recvErr == unix.EWOULDBLOCK {
			continue
		}
		if n == 0 && controlN == 0 {
			return true
		}
	}
	return false
}

func watchHostLoggerPIDFD(ctx context.Context, cancel context.CancelFunc, pidfd int) {
	for ctx.Err() == nil {
		poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, 20)
		if err == nil && poll[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			cancel()
			return
		}
		if err != nil && err != unix.EINTR {
			cancel()
			return
		}
	}
}

func (s *hostLoggerService) sendLifecycleACK(ctx context.Context, fd int, contextID string, generation uint64, nonce, result, kind string) error {
	if result != "ok" && result != "rejected" && result != "restart_required" {
		return errHostLoggerProtocol
	}
	if err := rejectQueuedLoggerControl(fd); err != nil {
		return err
	}
	frame, err := json.Marshal(hostLoggerLifecycleACK{Version: 1, Type: kind, ContextID: contextID,
		ControlGeneration: generation, LaunchNonce: nonce, Result: result})
	if err != nil || len(frame) > hostLoggerControlLimit {
		return errHostLoggerProtocol
	}
	return hostLoggerSendFrame(ctx, fd, frame, time.Now().Add(hostLoggerACKTimeout))
}

// rejectQueuedLoggerControl enforces stop-and-wait for packets already queued
// before an ACK. It is a defensive point-in-time check; the trusted launcher
// remains responsible for never sending a request before receiving its ACK.
func rejectQueuedLoggerControl(fd int) error {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(poll, 0)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return errHostLoggerRestartRequired
		}
		if n > 0 && poll[0].Revents != 0 {
			return errHostLoggerRestartRequired
		}
		return nil
	}
}

func (s *hostLoggerService) closeOwned() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	contexts := make([]*hostLoggerServiceContext, 0, len(s.contexts))
	for _, c := range s.contexts {
		contexts = append(contexts, c)
	}
	s.contexts = make(map[string]*hostLoggerServiceContext)
	s.mu.Unlock()
	for _, c := range contexts {
		_ = unix.Close(c.pidfd)
	}
	s.logLifecycle("shutdown", "ok", time.Now())
}

func (s *hostLoggerService) logContextLifecycle(contextID, stage, result string, started time.Time) {
	traceID := ""
	s.mu.Lock()
	if c := s.contexts[contextID]; c != nil {
		traceID = c.traceID
	}
	s.mu.Unlock()
	if traceID == "" {
		traceID = newLoggerTraceID()
	}
	s.writeLifecycleLog(traceID, stage, result, started)
}

func (s *hostLoggerService) logLifecycle(stage, result string, started time.Time) {
	s.writeLifecycleLog(newLoggerTraceID(), stage, result, started)
}

func (s *hostLoggerService) writeLifecycleLog(traceID, stage, result string, started time.Time) {
	if s == nil || s.logger == nil || traceID == "" {
		return
	}
	s.mu.Lock()
	contexts, handlers := len(s.contexts), s.activeConnCount
	s.mu.Unlock()
	durationMS := time.Since(started).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	s.logger.Info("pilot_host_logger_lifecycle", "trace_id", traceID, "module", "pilot_host_logger",
		"event", stage, "result_class", result, "duration_ms", durationMS,
		"context_count", contexts, "active_handler_count", handlers)
}

func newLoggerTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func validHostLoggerRegister(r hostLoggerRegisterRequest) bool {
	return r.Version == 1 && r.Type == "LOGGER_REGISTER" && hostLogHex32.MatchString(r.ContextID) &&
		r.RunEpoch == r.ContextID && r.ControlGeneration > 0 && r.GatewayGeneration > 0 &&
		hostLogHex32.MatchString(r.LaunchNonce) && r.ReaperPID > 0 && r.ReaperStartTimeTicks > 0 &&
		r.ReaperPIDNSInode > 0 && r.ReaperCgroupMountID > 0 && r.ReaperCgroupInode > 0 &&
		hostLogHex32.MatchString(r.EventProcessID) && r.EventProcessID == hostLoggerEventProcessID(r.RunEpoch, r.ControlGeneration)
}

func decodeLoggerControl(frame []byte, dst any) error {
	if len(frame) == 0 || len(frame) > hostLoggerControlLimit || decodeCanonicalJSON(frame, dst) != nil {
		return errHostLoggerProtocol
	}
	return nil
}

func inspectLoggerControl(control []byte) ([]int, []*unix.Ucred, error) {
	var fds []int
	var credentials []*unix.Ucred
	invalid := false
	remaining := control
	for len(remaining) > 0 {
		if len(remaining) < unix.CmsgLen(0) {
			for _, b := range remaining {
				if b != 0 {
					return fds, credentials, errHostLoggerProtocol
				}
			}
			break
		}
		header, data, tail, err := unix.ParseOneSocketControlMessage(remaining)
		if err != nil {
			return fds, credentials, errHostLoggerProtocol
		}
		message := unix.SocketControlMessage{Header: header, Data: data}
		switch {
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS:
			got, parseErr := unix.ParseUnixRights(&message)
			if parseErr != nil {
				invalid = true
			} else {
				fds = append(fds, got...)
			}
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_CREDENTIALS:
			got, parseErr := unix.ParseUnixCredentials(&message)
			if parseErr != nil {
				invalid = true
			} else {
				credentials = append(credentials, got)
			}
		default:
			invalid = true
		}
		if len(tail) >= len(remaining) {
			return fds, credentials, errHostLoggerProtocol
		}
		remaining = tail
	}
	if invalid {
		return fds, credentials, errHostLoggerProtocol
	}
	return fds, credentials, nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
}

func hostLoggerSeqpacket(fd int) bool {
	typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	return err == nil && typ == unix.SOCK_SEQPACKET
}

func hostLoggerPIDFDAlive(fd int) bool {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := unix.Poll(poll, 0)
	if err != nil || poll[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
		return false
	}
	err = unix.PidfdSendSignal(fd, 0, nil, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

func hostLoggerSendFrame(ctx context.Context, fd int, frame []byte, deadline time.Time) error {
	for {
		if err := loggerWait(ctx, fd, unix.POLLOUT, deadline); err != nil {
			return err
		}
		n, err := unix.SendmsgN(fd, frame, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil || n != len(frame) {
			return errHostLoggerStore
		}
		return nil
	}
}

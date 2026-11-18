//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestHostLoggerServiceRegistrationChannelAndUnregister(t *testing.T) {
	logFile, store := newTestHostLoggerStore(t)
	var fakeNowMu sync.Mutex
	fakeNow := time.Now()
	store.hooks.now = func() time.Time {
		fakeNowMu.Lock()
		defer fakeNowMu.Unlock()
		return fakeNow
	}
	idleReceive := make(chan struct{}, 4)
	baseWait := store.hooks.wait
	store.hooks.wait = func(ctx context.Context, fd int, events int16, deadline time.Time) error {
		if events == unix.POLLIN && deadline.IsZero() {
			select {
			case idleReceive <- struct{}{}:
			default:
			}
		}
		return baseWait(ctx, fd, events, deadline)
	}
	listenerFD, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	address := "\x00en-words-test-logger-" + strings.TrimPrefix(t.Name(), "Test")
	if err := unix.Bind(listenerFD, &unix.SockaddrUnix{Name: address}); err != nil {
		_ = unix.Close(listenerFD)
		t.Fatal(err)
	}
	if err := unix.Listen(listenerFD, 16); err != nil {
		_ = unix.Close(listenerFD)
		t.Fatal(err)
	}
	listener := os.NewFile(uintptr(listenerFD), "logger-test-listener")
	t.Cleanup(func() { _ = listener.Close() })
	controlFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	controlServer := os.NewFile(uintptr(controlFDs[0]), "logger-test-control-server")
	controlLauncher := os.NewFile(uintptr(controlFDs[1]), "logger-test-control-launcher")
	t.Cleanup(func() { _ = controlServer.Close(); _ = controlLauncher.Close() })
	if err := unix.SetsockoptInt(int(controlLauncher.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	launcherCred := unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(unix.Getuid()), Gid: uint32(unix.Getgid())}
	var logStat unix.Stat_t
	var logStatx unix.Statx_t
	if err := unix.Fstat(int(logFile.Fd()), &logStat); err != nil {
		t.Fatal(err)
	}
	if err := unix.Statx(int(logFile.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW,
		int(unix.STATX_BASIC_STATS|unix.STATX_MNT_ID), &logStatx); err != nil {
		t.Fatal(err)
	}
	service, err := newHostLoggerService(store, listener, launcherCred, hostLoggerServiceConfig{
		LoggerUID: uint32(unix.Geteuid()), LoggerGID: uint32(unix.Getegid()), EndpointName: address,
		LogDevice: uint64(logStat.Dev), LogMountID: logStatx.Mnt_id,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.stateChanged = make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- service.run(ctx, controlServer) }()
	if err := loggerWait(ctx, int(controlLauncher.Fd()), unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	readyFrame, readyCreds := receiveTestLoggerFrame(t, int(controlLauncher.Fd()), true)
	if string(readyFrame) != `{"version":1,"type":"LOGGER_READY","result":"ok"}` || len(readyCreds) != 1 || !sameUcred(readyCreds[0], &launcherCred) {
		t.Fatalf("bad authenticated LOGGER_READY: frame=%s creds=%v", readyFrame, readyCreds)
	}

	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	epoch := strings.Repeat("0", 32)
	req := hostLoggerRegisterRequest{Version: 1, Type: "LOGGER_REGISTER", ContextID: epoch, RunEpoch: epoch,
		ControlGeneration: 1, GatewayGeneration: 1, LaunchNonce: strings.Repeat("a", 32),
		ReaperPID: int32(os.Getpid()), ReaperUID: uint32(unix.Getuid()), ReaperGID: uint32(unix.Getgid()),
		ReaperStartTimeTicks: 1, ReaperPIDNSInode: 1, ReaperCgroupMountID: 1, ReaperCgroupInode: 1,
		EventProcessID: hostLoggerEventProcessID(epoch, 1)}
	registerFrame, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.SendmsgN(int(controlLauncher.Fd()), registerFrame, unix.UnixRights(pidfd), nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, int(controlLauncher.Fd()), unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	registerAck, _ := receiveTestLoggerFrame(t, int(controlLauncher.Fd()), true)
	var lifecycleACK hostLoggerLifecycleACK
	if decodeLoggerControl(registerAck, &lifecycleACK) != nil || lifecycleACK.Type != "LOGGER_REGISTER_ACK" || lifecycleACK.Result != "ok" {
		t.Fatalf("REGISTER failed: %s", registerAck)
	}

	dataFD := -1
	t.Cleanup(func() {
		if dataFD >= 0 {
			_ = unix.Close(dataFD)
		}
	})
	dataFD, err = unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Connect(dataFD, &unix.SockaddrUnix{Name: address}); err != nil && err != unix.EINPROGRESS {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, dataFD, unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	channelReady, _ := receiveTestLoggerFrame(t, dataFD, false)
	if validateHostLoggerChannelReady(channelReady, epoch, 1) != nil {
		t.Fatalf("data channel was not ready: %s", channelReady)
	}
	if err := unix.Shutdown(dataFD, unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(dataFD); err != nil {
		t.Fatal(err)
	}
	dataFD = -1
	waitForLoggerCondition(t, service.stateChanged, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		c := service.contexts[epoch]
		return c != nil && c.eof && !c.connActive && !c.handlerActive
	})
	for len(idleReceive) > 0 {
		<-idleReceive
	}
	dataFD, err = unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Connect(dataFD, &unix.SockaddrUnix{Name: address}); err != nil && err != unix.EINPROGRESS {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, dataFD, unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	channelReady, _ = receiveTestLoggerFrame(t, dataFD, false)
	if validateHostLoggerChannelReady(channelReady, epoch, 1) != nil {
		t.Fatalf("reconnected data channel was not ready: %s", channelReady)
	}
	// Cross an idle interval beyond the ACK deadline using the injected clock;
	// the read barrier proves the handler is already waiting for its next packet.
	<-idleReceive
	fakeNowMu.Lock()
	fakeNow = fakeNow.Add(hostLoggerACKTimeout + time.Second)
	fakeNowMu.Unlock()
	event := testHostReaperEvent()
	event.ProcessID = req.EventProcessID
	dataFrame, expectedACK, err := encodeHostLoggerEnvelope(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.SendmsgN(dataFD, dataFrame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, dataFD, unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	dataACK, _ := receiveTestLoggerFrame(t, dataFD, false)
	if validateHostLoggerACK(dataACK, expectedACK) != nil {
		t.Fatalf("data ACK was not correlated: %s", dataACK)
	}
	<-idleReceive
	fakeNowMu.Lock()
	fakeNow = fakeNow.Add(hostLoggerACKTimeout + time.Second)
	fakeNowMu.Unlock()
	event.OperationID = strings.Repeat("4", 32)
	secondFrame, secondExpectedACK, err := encodeHostLoggerEnvelope(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.SendmsgN(dataFD, secondFrame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, dataFD, unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	secondACK, _ := receiveTestLoggerFrame(t, dataFD, false)
	if validateHostLoggerACK(secondACK, secondExpectedACK) != nil {
		t.Fatalf("second idle-separated data ACK was not correlated: %s", secondACK)
	}
	if err := unix.Shutdown(dataFD, unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	waitForLoggerCondition(t, service.stateChanged, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		c := service.contexts[epoch]
		return c != nil && c.eof && !c.connActive && !c.handlerActive
	})
	unregister, _ := json.Marshal(hostLoggerUnregisterRequest{Version: 1, Type: "LOGGER_UNREGISTER", ContextID: epoch,
		ControlGeneration: 1, LaunchNonce: req.LaunchNonce})
	if _, err := unix.SendmsgN(int(controlLauncher.Fd()), unregister, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, int(controlLauncher.Fd()), unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	unregisterAck, _ := receiveTestLoggerFrame(t, int(controlLauncher.Fd()), true)
	if decodeLoggerControl(unregisterAck, &lifecycleACK) != nil || lifecycleACK.Type != "LOGGER_UNREGISTER_ACK" || lifecycleACK.Result != "ok" {
		t.Fatalf("UNREGISTER failed: %s", unregisterAck)
	}
	// A successful UNREGISTER releases the live context but retains the
	// process-lifetime generation/event tombstones against stale REGISTER replay.
	if _, err := unix.SendmsgN(int(controlLauncher.Fd()), registerFrame, unix.UnixRights(pidfd), nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := loggerWait(ctx, int(controlLauncher.Fd()), unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	replayAck, _ := receiveTestLoggerFrame(t, int(controlLauncher.Fd()), true)
	if decodeLoggerControl(replayAck, &lifecycleACK) != nil || lifecycleACK.Type != "LOGGER_REGISTER_ACK" || lifecycleACK.Result != "rejected" {
		t.Fatalf("stale REGISTER replay was not rejected: %s", replayAck)
	}
	service.mu.Lock()
	_, stillRegistered := service.contexts[epoch]
	_, generationRetained := service.byGeneration[hostLoggerGenerationKey{contextID: epoch, generation: req.ControlGeneration}]
	eventProcessRetained := service.byEventProcess[req.EventProcessID] == epoch
	service.mu.Unlock()
	if stillRegistered || !generationRetained || !eventProcessRetained {
		t.Fatalf("UNREGISTER replay tombstone state invalid: active=%v generation=%v event=%v", stillRegistered, generationRetained, eventProcessRetained)
	}
	cancel()
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatalf("service shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service failed to stop after context cancellation")
	}
}

func TestHostLoggerServiceRejectsRegistrationHistoryOverflow(t *testing.T) {
	_, store := newTestHostLoggerStore(t)
	baselineStoreContexts := len(store.contexts) // the fixture retains its default run bucket
	service := &hostLoggerService{store: store, contexts: make(map[string]*hostLoggerServiceContext),
		byGeneration: make(map[hostLoggerGenerationKey]struct{}), byEventProcess: make(map[string]string)}
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	for i := 1; i <= hostLoggerRegistrationHistoryLimit; i++ {
		contextID := fmt.Sprintf("%032x", i)
		request := hostLoggerRegisterRequest{ContextID: contextID, RunEpoch: contextID, ControlGeneration: 1,
			LaunchNonce: fmt.Sprintf("%032x", i+hostLoggerRegistrationHistoryLimit), EventProcessID: hostLoggerEventProcessID(contextID, 1),
			ReaperPID: int32(os.Getpid()), ReaperUID: uint32(unix.Getuid()), ReaperGID: uint32(unix.Getgid())}
		if err := service.register(request, pidfd); err != nil {
			t.Fatalf("register history item %d/%d: %v", i, hostLoggerRegistrationHistoryLimit, err)
		}
		service.contexts[contextID].eof = true
		if err := service.unregister(hostLoggerUnregisterRequest{ContextID: contextID,
			ControlGeneration: 1, LaunchNonce: request.LaunchNonce}); err != nil {
			t.Fatalf("unregister history item %d/%d: %v", i, hostLoggerRegistrationHistoryLimit, err)
		}
	}
	if service.registrationCount != hostLoggerRegistrationHistoryLimit || len(service.byGeneration) != hostLoggerRegistrationHistoryLimit ||
		len(service.byEventProcess) != hostLoggerRegistrationHistoryLimit || len(service.contexts) != 0 || len(store.contexts) != baselineStoreContexts {
		t.Fatalf("accepted history was not retained after unregister: count=%d generations=%d events=%d contexts=%d store=%d",
			service.registrationCount, len(service.byGeneration), len(service.byEventProcess), len(service.contexts), len(store.contexts))
	}

	controlFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(controlFDs[0])
	defer unix.Close(controlFDs[1])
	if err := unix.SetsockoptInt(controlFDs[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetsockoptInt(controlFDs[1], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	service.launcher = unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(unix.Getuid()), Gid: uint32(unix.Getgid())}
	contextID := strings.Repeat("7", 32)
	request := hostLoggerRegisterRequest{Version: 1, Type: "LOGGER_REGISTER", ContextID: contextID, RunEpoch: contextID,
		ControlGeneration: 3, GatewayGeneration: 1, LaunchNonce: strings.Repeat("c", 32),
		ReaperPID: int32(os.Getpid()), ReaperUID: uint32(unix.Getuid()), ReaperGID: uint32(unix.Getgid()),
		ReaperStartTimeTicks: 1, ReaperPIDNSInode: 1, ReaperCgroupMountID: 1, ReaperCgroupInode: 1,
		EventProcessID: hostLoggerEventProcessID(contextID, 3)}
	frame, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.SendmsgN(controlFDs[1], frame, unix.UnixRights(pidfd), nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fdCountBefore, err := loggerTestOpenFDCount(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleControl(ctx, controlFDs[0]); err != nil {
		t.Fatalf("capacity response: %v", err)
	}
	fdCountAfter, err := loggerTestOpenFDCount(t)
	if err != nil || fdCountAfter != fdCountBefore {
		t.Fatalf("capacity rejection leaked received pidfd: before=%d after=%d err=%v", fdCountBefore, fdCountAfter, err)
	}
	if err := loggerWait(ctx, controlFDs[1], unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ack, _ := receiveTestLoggerFrame(t, controlFDs[1], true)
	var lifecycleACK hostLoggerLifecycleACK
	if decodeLoggerControl(ack, &lifecycleACK) != nil || lifecycleACK.Result != "restart_required" || lifecycleACK.Type != "LOGGER_REGISTER_ACK" {
		t.Fatalf("history capacity ACK = %s, want restart_required", ack)
	}
	if service.registrationCount != hostLoggerRegistrationHistoryLimit || len(service.contexts) != 0 || len(store.contexts) != baselineStoreContexts ||
		len(service.byGeneration) != hostLoggerRegistrationHistoryLimit || len(service.byEventProcess) != hostLoggerRegistrationHistoryLimit {
		t.Fatalf("history overflow mutated state: count=%d contexts=%d store=%d generations=%d events=%d", service.registrationCount,
			len(service.contexts), len(store.contexts), len(service.byGeneration), len(service.byEventProcess))
	}
}

func TestHostLoggerServiceRejectsQueuedControlBeforeACK(t *testing.T) {
	for _, operation := range []string{"register", "unregister"} {
		t.Run(operation, func(t *testing.T) {
			_, store := newTestHostLoggerStore(t)
			baselineStoreContexts := len(store.contexts)
			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fds[0])
			defer unix.Close(fds[1])
			if err := unix.SetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
				t.Fatal(err)
			}
			launcher := unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(unix.Getuid()), Gid: uint32(unix.Getgid())}
			service := &hostLoggerService{store: store, launcher: launcher, contexts: make(map[string]*hostLoggerServiceContext),
				byGeneration: make(map[hostLoggerGenerationKey]struct{}), byEventProcess: make(map[string]string)}
			contextID := strings.Repeat("8", 32)
			pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(pidfd)
			request := hostLoggerRegisterRequest{Version: 1, Type: "LOGGER_REGISTER", ContextID: contextID, RunEpoch: contextID,
				ControlGeneration: 4, GatewayGeneration: 1, LaunchNonce: strings.Repeat("d", 32),
				ReaperPID: int32(os.Getpid()), ReaperUID: launcher.Uid, ReaperGID: launcher.Gid,
				ReaperStartTimeTicks: 1, ReaperPIDNSInode: 1, ReaperCgroupMountID: 1, ReaperCgroupInode: 1,
				EventProcessID: hostLoggerEventProcessID(contextID, 4)}
			var firstFrame []byte
			if operation == "register" {
				firstFrame, err = json.Marshal(request)
			} else {
				if err := service.register(request, pidfd); err != nil {
					t.Fatal(err)
				}
				service.contexts[contextID].eof = true
				firstFrame, err = json.Marshal(hostLoggerUnregisterRequest{Version: 1, Type: "LOGGER_UNREGISTER", ContextID: contextID,
					ControlGeneration: request.ControlGeneration, LaunchNonce: request.LaunchNonce})
			}
			if err != nil {
				t.Fatal(err)
			}
			if operation == "register" {
				if _, err := unix.SendmsgN(fds[1], firstFrame, unix.UnixRights(pidfd), nil, unix.MSG_NOSIGNAL); err != nil {
					t.Fatal(err)
				}
			} else if _, err := unix.SendmsgN(fds[1], firstFrame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
				t.Fatal(err)
			}
			secondFrame := []byte(`{"version":1,"type":"LOGGER_UNREGISTER","context_id":"99999999999999999999999999999999","control_generation":9,"launch_nonce":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`)
			if _, err := unix.SendmsgN(fds[1], secondFrame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			fdCountBefore, err := loggerTestOpenFDCount(t)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.handleControl(ctx, fds[0]); !errors.Is(err, errHostLoggerRestartRequired) {
				t.Fatalf("pipelined request result = %v, want restart-required channel close", err)
			}
			fdCountAfter, err := loggerTestOpenFDCount(t)
			wantFDCount := fdCountBefore
			if operation == "register" {
				wantFDCount++ // the duplicated pidfd is held by the committed context
			} else {
				wantFDCount-- // UNREGISTER closed the context-owned pidfd
			}
			if err != nil || fdCountAfter != wantFDCount {
				t.Fatalf("unexpected FD ownership after pipelined %s: before=%d after=%d want=%d err=%v", operation, fdCountBefore, fdCountAfter, wantFDCount, err)
			}
			if operation == "register" {
				if service.contexts[contextID] == nil {
					t.Fatal("first registration should be committed before ACK uncertainty")
				}
			} else if service.contexts[contextID] != nil || len(store.contexts) != baselineStoreContexts {
				t.Fatal("first unregister should be committed before ACK uncertainty")
			}
			buf := make([]byte, hostLoggerControlLimit+1)
			n, _, _, _, recvErr := unix.Recvmsg(fds[1], buf, nil, unix.MSG_DONTWAIT)
			if recvErr != unix.EAGAIN && recvErr != unix.EWOULDBLOCK {
				t.Fatalf("unexpected ACK after queued control frame: n=%d err=%v", n, recvErr)
			}
			service.closeOwned()
			fdCountAfterClose, err := loggerTestOpenFDCount(t)
			wantAfterClose := fdCountBefore
			if operation == "unregister" {
				wantAfterClose--
			}
			if err != nil || fdCountAfterClose != wantAfterClose {
				t.Fatalf("service cleanup leaked descriptor after pipelined %s: before=%d after=%d want=%d err=%v", operation, fdCountBefore, fdCountAfterClose, wantAfterClose, err)
			}
		})
	}
}

func TestHostLoggerServiceUnregisterWaitsForHandlerAndPidfdWatcher(t *testing.T) {
	_, store := newTestHostLoggerStore(t)
	child := exec.Command("sh", "-c", "exec sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childExited := false
	defer func() {
		if !childExited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	pidfd, err := unix.PidfdOpen(child.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	contextID := strings.Repeat("9", 32)
	request := hostLoggerRegisterRequest{ContextID: contextID, RunEpoch: contextID, ControlGeneration: 12,
		LaunchNonce: strings.Repeat("f", 32), EventProcessID: hostLoggerEventProcessID(contextID, 12),
		// The socketpair below is owned by this process, so bind peer credentials
		// to this PID while retaining the child's pidfd as the liveness watcher.
		ReaperPID: int32(os.Getpid()), ReaperUID: uint32(unix.Getuid()), ReaperGID: uint32(unix.Getgid())}
	service := &hostLoggerService{store: store, contexts: make(map[string]*hostLoggerServiceContext),
		byGeneration: make(map[hostLoggerGenerationKey]struct{}), byEventProcess: make(map[string]string),
		stateChanged: make(chan struct{}, 4)}
	if err := service.register(request, pidfd); err != nil {
		t.Fatal(err)
	}
	c := service.contexts[contextID]
	c.connActive = true
	watcherRetireEntered, releaseWatcher := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseWatcher) })
	service.beforeWatcherRetire = func() { close(watcherRetireEntered); <-releaseWatcher }
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	if err := unix.SetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); service.serveConnection(context.Background(), fds[0], c) }()
	if err := loggerWait(context.Background(), fds[1], unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ready, _ := receiveTestLoggerFrame(t, fds[1], false)
	if validateHostLoggerChannelReady(ready, contextID, 12) != nil {
		t.Fatalf("missing channel readiness: %s", ready)
	}
	if err := unix.Shutdown(fds[1], unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watcherRetireEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not reach the watcher-retirement barrier after EOF")
	}
	if c.connActive || !c.eof || !c.handlerActive {
		t.Fatalf("unexpected state at teardown barrier: conn=%v eof=%v handler=%v", c.connActive, c.eof, c.handlerActive)
	}
	unregister := hostLoggerUnregisterRequest{ContextID: contextID, ControlGeneration: 12, LaunchNonce: request.LaunchNonce}
	if err := service.unregister(unregister); err == nil {
		t.Fatal("UNREGISTER succeeded before handler/watcher teardown completed")
	}
	if _, err := unix.FcntlInt(uintptr(c.pidfd), unix.F_GETFD, 0); err != nil {
		t.Fatalf("UNREGISTER closed pidfd while watcher was still live: %v", err)
	}
	releaseOnce.Do(func() { close(releaseWatcher) })
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not finish after watcher cancellation")
	}
	if c.handlerActive {
		t.Fatal("handler-active flag remained set after complete teardown")
	}
	if err := service.unregister(unregister); err != nil {
		t.Fatalf("UNREGISTER after handler/watcher join: %v", err)
	}
	if len(service.contexts) != 0 || len(store.contexts) != 1 {
		t.Fatal("final unregister did not release context state")
	}
}

func loggerTestOpenFDCount(t *testing.T) (int, error) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func TestHostLoggerServiceReapsDeadUnconnectedContext(t *testing.T) {
	_, store := newTestHostLoggerStore(t)
	child := exec.Command("sh", "-c", "exec sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childExited := false
	defer func() {
		if !childExited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	pidfd, err := unix.PidfdOpen(child.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	contextID := strings.Repeat("0", 32)
	request := hostLoggerRegisterRequest{ContextID: contextID, RunEpoch: contextID, ControlGeneration: 7,
		LaunchNonce: strings.Repeat("a", 32), EventProcessID: hostLoggerEventProcessID(contextID, 7),
		ReaperPID: int32(child.Process.Pid), ReaperUID: uint32(unix.Getuid()), ReaperGID: uint32(unix.Getgid())}
	service := &hostLoggerService{store: store, contexts: make(map[string]*hostLoggerServiceContext),
		byGeneration: make(map[hostLoggerGenerationKey]struct{}), byEventProcess: make(map[string]string),
		stateChanged: make(chan struct{}, 1)}
	if err := service.register(request, pidfd); err != nil {
		t.Fatalf("register real child pidfd: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := child.Wait()
	childExited = true
	if waitErr == nil {
		t.Fatal("expected child process to exit from SIGKILL")
	}
	service.reapUnconnectedDeadContexts()
	if !service.contexts[contextID].eof {
		t.Fatal("dead unconnected context was not moved to its unregisterable terminal state")
	}
	unregister := hostLoggerUnregisterRequest{ContextID: contextID, ControlGeneration: 7, LaunchNonce: request.LaunchNonce}
	if err := service.unregister(unregister); err != nil {
		t.Fatalf("unregister dead child context: %v", err)
	}
	if len(service.contexts) != 0 || len(store.contexts) != 1 {
		t.Fatal("unregister did not release service and store context state")
	}
}

// This barrier test isolates the final pidfd check immediately before READY;
// full SO_PEERCRED-to-pidfd binding remains part of privileged Linux E2E.
func TestHostLoggerServiceSuppressesReadinessWhenReaperDiesAtBarrier(t *testing.T) {
	_, store := newTestHostLoggerStore(t)
	child := exec.Command("sh", "-c", "exec sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childExited := false
	defer func() {
		if !childExited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	pidfd, err := unix.PidfdOpen(child.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	contextID := strings.Repeat("5", 32)
	request := hostLoggerRegisterRequest{ContextID: contextID, RunEpoch: contextID, ControlGeneration: 9,
		LaunchNonce: strings.Repeat("b", 32), EventProcessID: hostLoggerEventProcessID(contextID, 9),
		ReaperPID: int32(child.Process.Pid), ReaperUID: uint32(unix.Getuid()), ReaperGID: uint32(unix.Getgid())}
	readinessEntered, releaseReadiness := make(chan struct{}), make(chan struct{})
	var releaseReadinessOnce sync.Once
	defer releaseReadinessOnce.Do(func() { close(releaseReadiness) })
	service := &hostLoggerService{store: store, contexts: make(map[string]*hostLoggerServiceContext),
		byGeneration: make(map[hostLoggerGenerationKey]struct{}), byEventProcess: make(map[string]string),
		beforeReadiness: func() { close(readinessEntered); <-releaseReadiness }}
	if err := service.register(request, pidfd); err != nil {
		t.Fatal(err)
	}
	c := service.contexts[contextID]
	c.connActive = true
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	observer, err := unix.FcntlInt(uintptr(fds[0]), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(observer)
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); service.serveConnection(context.Background(), fds[0], c) }()
	<-readinessEntered
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := child.Wait()
	childExited = true
	if waitErr == nil {
		t.Fatal("expected child process to exit from SIGKILL")
	}
	_ = unix.Close(fds[1])
	releaseReadinessOnce.Do(func() { close(releaseReadiness) })
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("readiness handler did not retire after the child died")
	}
	if !c.eof || c.failed || c.connActive {
		t.Fatalf("dead child readiness state invalid: eof=%v failed=%v active=%v", c.eof, c.failed, c.connActive)
	}
	if err := loggerWait(context.Background(), observer, unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("expected peer closure without readiness: %v", err)
	}
	buf := make([]byte, hostLoggerControlLimit+1)
	n, _, _, _, recvErr := unix.Recvmsg(observer, buf, nil, unix.MSG_DONTWAIT)
	if recvErr != nil || n != 0 {
		t.Fatalf("dead Reaper received a readiness frame: n=%d err=%v frame=%s", n, recvErr, buf[:max(0, n)])
	}
	if err := service.unregister(hostLoggerUnregisterRequest{ContextID: contextID,
		ControlGeneration: 9, LaunchNonce: request.LaunchNonce}); err != nil {
		t.Fatalf("unregister after dead-child EOF: %v", err)
	}
}

// This is an isolated PIDFD cancellation/drain boundary test: the packet peer
// is the test process while a separate child supplies the controllable pidfd.
// It proves the post-receive liveness guard and no-ACK drain path, not the
// launcher-attested child-to-peer binding, which needs privileged Linux E2E.
func TestHostLoggerServiceDropsQueuedPacketAfterPidfdDeath(t *testing.T) {
	logFile, store := newTestHostLoggerStore(t)
	initialLog, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sh", "-c", "exec sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childExited := false
	defer func() {
		if !childExited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	pidfd, err := unix.PidfdOpen(child.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	contextID := strings.Repeat("6", 32)
	peer := hostLoggerPeerIdentity{PID: int32(os.Getpid()), UID: uint32(unix.Getuid()), GID: uint32(unix.Getgid()),
		EventProcessID: hostLoggerEventProcessID(contextID, 11)}
	if err := store.RegisterRunContext(contextID, peer); err != nil {
		t.Fatal(err)
	}
	waitEntered, releaseWait := make(chan struct{}), make(chan struct{})
	var waitOnce sync.Once
	var releaseWaitOnce sync.Once
	defer releaseWaitOnce.Do(func() { close(releaseWait) })
	baseWait := store.hooks.wait
	store.hooks.wait = func(ctx context.Context, fd int, events int16, deadline time.Time) error {
		if events == unix.POLLIN && deadline.IsZero() {
			waitOnce.Do(func() { close(waitEntered); <-releaseWait })
			return nil
		}
		return baseWait(ctx, fd, events, deadline)
	}
	request := hostLoggerRegisterRequest{ContextID: contextID, ControlGeneration: 11}
	c := &hostLoggerServiceContext{request: request, peer: peer, pidfd: pidfd, connActive: true}
	service := &hostLoggerService{store: store, contexts: map[string]*hostLoggerServiceContext{contextID: c},
		stateChanged: make(chan struct{}, 1)}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); service.serveConnection(context.Background(), fds[0], c) }()
	if err := loggerWait(context.Background(), fds[1], unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ready, _ := receiveTestLoggerFrame(t, fds[1], false)
	if validateHostLoggerChannelReady(ready, contextID, 11) != nil {
		t.Fatalf("missing channel ready: %s", ready)
	}
	<-waitEntered
	event := testHostReaperEvent()
	event.ProcessID = peer.EventProcessID
	frame, _, err := encodeHostLoggerEnvelope(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.SendmsgN(fds[1], frame, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := unix.Shutdown(fds[1], unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := child.Wait()
	childExited = true
	if waitErr == nil {
		t.Fatal("expected child process to exit from SIGKILL")
	}
	releaseWaitOnce.Do(func() { close(releaseWait) })
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler failed to drain the dead peer")
	}
	if !c.eof || c.failed || c.connActive {
		t.Fatalf("dead peer terminal state invalid: eof=%v failed=%v active=%v", c.eof, c.failed, c.connActive)
	}
	if err := loggerWait(context.Background(), fds[1], unix.POLLIN, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("expected EOF without ACK: %v", err)
	}
	buf := make([]byte, hostLoggerControlLimit+1)
	n, _, _, _, recvErr := unix.Recvmsg(fds[1], buf, nil, unix.MSG_DONTWAIT)
	if recvErr != nil || n != 0 {
		t.Fatalf("dead peer received an ACK: n=%d err=%v", n, recvErr)
	}
	afterLog, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(afterLog) != string(initialLog) {
		t.Fatal("queued dead-peer packet was admitted to the durable log")
	}
	if err := store.ReleaseRunContext(contextID, peer); err != nil {
		t.Fatalf("release dead peer store context: %v", err)
	}
}

func receiveTestLoggerFrame(t *testing.T, fd int, expectCredentials bool) ([]byte, []*unix.Ucred) {
	t.Helper()
	buf, oob := make([]byte, hostLoggerControlLimit+1), make([]byte, 4096)
	n, oobn, flags, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	if err != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n == 0 {
		t.Fatalf("receive logger frame: n=%d flags=%x err=%v", n, flags, err)
	}
	fds, credentials, parseErr := inspectLoggerControl(oob[:oobn])
	closeFDs(fds)
	if parseErr != nil || len(fds) != 0 || expectCredentials && len(credentials) != 1 || !expectCredentials && len(credentials) != 0 {
		t.Fatalf("unexpected ancillary data: fds=%v credentials=%v err=%v", fds, credentials, parseErr)
	}
	return append([]byte(nil), buf[:n]...), credentials
}

func waitForLoggerCondition(t *testing.T, changed <-chan struct{}, predicate func() bool) {
	t.Helper()
	for {
		if predicate() {
			return
		}
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("logger lifecycle condition did not become true")
		}
	}
}

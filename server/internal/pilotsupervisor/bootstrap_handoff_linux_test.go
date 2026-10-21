//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fakeBootstrapHandoffLedger struct {
	consumed bool
	err      error
	calls    int
	request  bootstrapHandoffRequest
}

func (l *fakeBootstrapHandoffLedger) ConsumeBootstrap(_ context.Context, request bootstrapHandoffRequest) (bool, error) {
	l.calls++
	l.request = request
	return l.consumed, l.err
}

func sendBootstrapPacket(t *testing.T, fd int, request bootstrapHandoffRequest, pidfd int) {
	t.Helper()
	packet, err := encodeBootstrapHandoff(request)
	if err != nil {
		t.Fatal(err)
	}
	oob := unix.UnixRights(pidfd)
	n, err := unix.SendmsgN(fd, packet, oob, nil, 0)
	if err != nil || n != len(packet) {
		t.Fatalf("send BOOTSTRAP = %d, %v", n, err)
	}
}

func newTestBootstrapHandoff(t *testing.T) ([2]int, *bootstrapHandoffTransport) {
	t.Helper()
	fds := recoverySocketPair(t)
	expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	controlFile := recoverySocketFile(fds[0], "bootstrap-control")
	transport, err := newBootstrapHandoffTransportForParent(controlFile, expected, unix.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	fds[0] = -1 // transport owns this endpoint
	t.Cleanup(func() { _ = transport.close() })
	return fds, transport
}

func TestBootstrapHandoffTransportAuthenticatesTransfersAndConsumesOnce(t *testing.T) {
	fds, transport := newTestBootstrapHandoff(t)
	gatewayPIDFD, err := unix.PidfdOpen(unix.Getpid(), 0)
	if err != nil {
		t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
	}
	defer unix.Close(gatewayPIDFD)
	request := validBootstrapHandoffRequest()
	sendBootstrapPacket(t, fds[1], request, gatewayPIDFD)
	ledger := &fakeBootstrapHandoffLedger{consumed: true}
	pidfd, err := transport.serve(context.Background(), ledger)
	if err != nil {
		t.Fatal(err)
	}
	defer pidfd.Close()
	if ledger.calls != 1 || ledger.request != request {
		t.Fatalf("ledger calls/request = %d/%#v", ledger.calls, ledger.request)
	}
	fdFlags, err := unix.FcntlInt(pidfd.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("received pidfd flags = %#x, %v", fdFlags, err)
	}
	var ackPacket [bootstrapHandoffMaxPacket]byte
	n, _, _, _, err := unix.Recvmsg(fds[1], ackPacket[:], nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := decodeBootstrapHandoffAck(ackPacket[:n])
	if err != nil || !bootstrapHandoffAckMatches(request, ack) || ack.Result != "ok" {
		t.Fatalf("ACK = %#v, %v", ack, err)
	}
	if _, err := transport.serve(context.Background(), ledger); !errors.Is(err, errBootstrapTransportRejected) {
		t.Fatalf("second serve = %v", err)
	}
}

func TestBootstrapHandoffPidfdHelper(t *testing.T) {
	if os.Getenv("PILOT_BOOTSTRAP_PIDFD_HELPER") == "1" {
		os.Exit(0)
	}
}

func TestBootstrapHandoffAckChildHelper(t *testing.T) {
	if os.Getenv("PILOT_BOOTSTRAP_ACK_CHILD") != "1" {
		return
	}
	fd := 3 // os/exec maps ExtraFiles[0] to descriptor 3.
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	var packet [bootstrapHandoffMaxPacket]byte
	control := make([]byte, 4096)
	n, controlN, _, _, err := unix.Recvmsg(fd, packet[:], control, 0)
	if err != nil || n == 0 {
		t.Fatalf("child receive BOOTSTRAP = %d, %v", n, err)
	}
	receivedFDs, _, err := inspectBootstrapControl(control[:controlN], true)
	if err != nil || len(receivedFDs) != 1 {
		t.Fatalf("child BOOTSTRAP ancillary data = %v, %v", receivedFDs, err)
	}
	for _, receivedFD := range receivedFDs {
		if err := unix.Close(receivedFD); err != nil {
			t.Fatal(err)
		}
	}
	request, err := decodeBootstrapHandoff(packet[:n])
	if err != nil {
		t.Fatal(err)
	}
	ack, err := encodeBootstrapHandoffAck(bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK",
		Generation: request.Generation, LaunchNonce: request.LaunchNonce, Result: bootstrapAckChildResult()})
	if err != nil {
		t.Fatal(err)
	}
	if sent, err := unix.SendmsgN(fd, ack, nil, nil, unix.MSG_NOSIGNAL); err != nil || sent != len(ack) {
		t.Fatalf("child send ACK = %d, %v", sent, err)
	}
	if os.Getenv("PILOT_BOOTSTRAP_ACK_CHILD_EMPTY_RECORD") == "1" {
		if sent, err := unix.SendmsgN(fd, []byte{}, nil, nil, unix.MSG_NOSIGNAL); err != nil || sent != 0 {
			t.Fatalf("child send empty record = %d, %v", sent, err)
		}
	}
	if os.Getenv("PILOT_BOOTSTRAP_ACK_CHILD_EXTRA_ACK") == "1" {
		if sent, err := unix.SendmsgN(fd, ack, nil, nil, unix.MSG_NOSIGNAL); err != nil || sent != len(ack) {
			t.Fatalf("child send extra ACK = %d, %v", sent, err)
		}
	}
	if os.Getenv("PILOT_BOOTSTRAP_ACK_CHILD_HOLD_OPEN") == "1" {
		var release [1]byte
		if n, err := unix.Read(4, release[:]); err != nil || n != 1 {
			t.Fatalf("child release read = %d, %v", n, err)
		}
		_ = unix.Close(4)
	}
	os.Exit(0)
}

func bootstrapAckChildResult() string {
	if result := os.Getenv("PILOT_BOOTSTRAP_ACK_CHILD_RESULT"); result != "" {
		return result
	}
	return "ok"
}

func waitBootstrapAckChild(command *exec.Cmd, forceKill bool) error {
	if command == nil || command.Process == nil {
		return errors.New("ACK child was not started")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if forceKill {
		_ = command.Process.Kill()
	}
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("ACK child timed out and was killed: %w", err)
		}
		return errors.New("ACK child timed out and was killed")
	case <-time.After(2 * time.Second):
		return errors.New("ACK child did not exit after kill")
	}
}

func TestBootstrapHandoffSenderSeparatesSocketpairAndACKCredentials(t *testing.T) {
	fds := recoverySocketPair(t)
	childEndpoint := recoverySocketFile(fds[0], "bootstrap-ack-child")
	defer childEndpoint.Close()
	fds[0] = -1
	releasePipe := []int{0, 0}
	if err := unix.Pipe(releasePipe); err != nil {
		t.Fatal(err)
	}
	releaseReader := recoverySocketFile(releasePipe[0], "bootstrap-ack-child-release")
	defer releaseReader.Close()
	releaseWriter := recoverySocketFile(releasePipe[1], "bootstrap-ack-parent-release")
	var childWaited bool
	var child *exec.Cmd
	t.Cleanup(func() {
		_ = releaseWriter.Close()
		if child != nil && !childWaited && child.Process != nil {
			_ = waitBootstrapAckChild(child, true)
		}
	})
	child = exec.Command(os.Args[0], "-test.run=^TestBootstrapHandoffAckChildHelper$")
	child.Env = append(os.Environ(), "PILOT_BOOTSTRAP_ACK_CHILD=1", "PILOT_BOOTSTRAP_ACK_CHILD_HOLD_OPEN=1")
	child.ExtraFiles = []*os.File{childEndpoint, releaseReader}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childEndpoint.Close()
	_ = releaseReader.Close()
	sender := recoverySocketFile(fds[1], "bootstrap-ack-sender")
	fds[1] = -1
	defer sender.Close()
	pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
	if err != nil {
		t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
	}
	defer unix.Close(pidfd)
	socketPeer := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	ackPeer := unix.Ucred{Pid: int32(child.Process.Pid), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	if err := sendBootstrapHandoff(context.Background(), sender, validBootstrapHandoffRequest(), pidfd, socketPeer, ackPeer); err != nil {
		t.Fatalf("sender rejected child ACK credentials: %v", err)
	}
	if n, err := releaseWriter.Write([]byte{1}); err != nil || n != 1 {
		t.Fatalf("release ACK child = %d, %v", n, err)
	}
	_ = releaseWriter.Close()
	childWaited = true
	if err := waitBootstrapAckChild(child, false); err != nil {
		t.Fatalf("ACK child exit = %v", err)
	}
}

func TestBootstrapHandoffSenderClassifiesACKEOFAndExtraEmptyRecord(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     string
		emptyExtra bool
		extraACK   bool
		wantErr    error
	}{
		{name: "ok-ack-followed-by-eof-is-uncertain", result: "ok", wantErr: errBootstrapAckUncertain},
		{name: "rejected-ack-followed-by-eof-remains-rejected", result: "rejected"},
		{name: "rejected-ack-then-empty-record-is-uncertain", result: "rejected", emptyExtra: true, wantErr: errBootstrapAckUncertain},
		{name: "rejected-ack-then-extra-ack-is-uncertain", result: "rejected", extraACK: true, wantErr: errBootstrapAckUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fds := recoverySocketPair(t)
			childEndpoint := recoverySocketFile(fds[0], "bootstrap-ack-matrix-child")
			defer childEndpoint.Close()
			fds[0] = -1
			command := exec.Command(os.Args[0], "-test.run=^TestBootstrapHandoffAckChildHelper$")
			command.Env = append(os.Environ(), "PILOT_BOOTSTRAP_ACK_CHILD=1", "PILOT_BOOTSTRAP_ACK_CHILD_RESULT="+tc.result)
			if tc.emptyExtra {
				command.Env = append(command.Env, "PILOT_BOOTSTRAP_ACK_CHILD_EMPTY_RECORD=1")
			}
			if tc.extraACK {
				command.Env = append(command.Env, "PILOT_BOOTSTRAP_ACK_CHILD_EXTRA_ACK=1")
			}
			command.ExtraFiles = []*os.File{childEndpoint}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			_ = childEndpoint.Close()
			sender := recoverySocketFile(fds[1], "bootstrap-ack-matrix-sender")
			fds[1] = -1
			defer sender.Close()
			waited := false
			t.Cleanup(func() {
				if !waited && command.Process != nil {
					_ = waitBootstrapAckChild(command, true)
				}
			})
			pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
			if err != nil {
				t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
			}
			defer unix.Close(pidfd)
			socketPeer := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
			ackPeer := unix.Ucred{Pid: int32(command.Process.Pid), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
			if err := validateBootstrapControlPeer(int(sender.Fd()), socketPeer); err != nil {
				t.Fatal(err)
			}
			request := validBootstrapHandoffRequest()
			packet, err := encodeBootstrapHandoff(request)
			if err != nil {
				t.Fatal(err)
			}
			if sent, err := unix.SendmsgN(int(sender.Fd()), packet, unix.UnixRights(pidfd), nil, unix.MSG_NOSIGNAL); err != nil || sent != len(packet) {
				t.Fatalf("send BOOTSTRAP = %d, %v", sent, err)
			}
			waited = true
			if err := waitBootstrapAckChild(command, false); err != nil {
				t.Fatalf("ACK child exit = %v", err)
			}
			ack, ackErr := receiveBootstrapHandoffAck(context.Background(), sender, request, ackPeer)
			if !errors.Is(ackErr, tc.wantErr) {
				t.Fatalf("ACK %#v receive error = %v, want %v", ack, ackErr, tc.wantErr)
			}
			if ack.Result != tc.result {
				t.Fatalf("ACK result = %q, want %q", ack.Result, tc.result)
			}
		})
	}
}

func TestBootstrapHandoffDeadPIDFDSendsCorrelatedRejectedACKWithoutLedgerConsume(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestBootstrapHandoffPidfdHelper$")
	command.Env = append(os.Environ(), "PILOT_BOOTSTRAP_PIDFD_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pidfd, err := unix.PidfdOpen(command.Process.Pid, 0)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
	}
	if err := command.Wait(); err != nil {
		_ = unix.Close(pidfd)
		t.Fatalf("helper process exit = %v", err)
	}
	defer unix.Close(pidfd)

	fds, transport := newTestBootstrapHandoff(t)
	request := validBootstrapHandoffRequest()
	sendBootstrapPacket(t, fds[1], request, pidfd)
	ledger := &fakeBootstrapHandoffLedger{consumed: true}
	if _, err := transport.serve(context.Background(), ledger); !errors.Is(err, errBootstrapHandoffRejected) {
		t.Fatalf("serve with dead pidfd = %v, want rejection", err)
	}
	if ledger.calls != 0 {
		t.Fatalf("dead pidfd reached ledger %d times", ledger.calls)
	}
	var ackPacket [bootstrapHandoffMaxPacket]byte
	n, _, _, _, err := unix.Recvmsg(fds[1], ackPacket[:], nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := decodeBootstrapHandoffAck(ackPacket[:n])
	if err != nil || !bootstrapHandoffAckMatches(request, ack) || ack.Result != "rejected" {
		t.Fatalf("dead-pidfd ACK = %#v, %v", ack, err)
	}
}

func TestBootstrapHandoffRejectsAndClosesWrongDescriptorMultiplicity(t *testing.T) {
	tests := []struct {
		name string
		fds  int
	}{
		{name: "no-rights", fds: 0},
		{name: "two-rights", fds: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fds, transport := newTestBootstrapHandoff(t)
			pipe := []int{0, 0}
			if err := unix.Pipe(pipe); err != nil {
				t.Fatal(err)
			}
			defer unix.Close(pipe[0])
			defer unix.Close(pipe[1])
			packet, err := encodeBootstrapHandoff(validBootstrapHandoffRequest())
			if err != nil {
				t.Fatal(err)
			}
			var rights []byte
			if tc.fds == 2 {
				rights = unix.UnixRights(pipe[0], pipe[1])
			}
			before := openProcFDs(t)
			if n, err := unix.SendmsgN(fds[1], packet, rights, nil, 0); err != nil || n != len(packet) {
				t.Fatalf("send malformed rights = %d, %v", n, err)
			}
			if _, _, err := transport.receiveRequest(context.Background()); !errors.Is(err, errBootstrapTransportRejected) {
				t.Fatalf("receive error = %v", err)
			}
			if after := openProcFDs(t); !equalIntSets(before, after) {
				t.Fatalf("received descriptors leaked: before=%v after=%v", before, after)
			}
		})
	}
}

func TestBootstrapHandoffConsumesGenerationBeforeSuccessfulAckAndFencesUncertainOutcome(t *testing.T) {
	t.Run("ledger-reject", func(t *testing.T) {
		fds, transport := newTestBootstrapHandoff(t)
		pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
		if err != nil {
			t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
		}
		defer unix.Close(pidfd)
		request := validBootstrapHandoffRequest()
		sendBootstrapPacket(t, fds[1], request, pidfd)
		ledger := &fakeBootstrapHandoffLedger{consumed: false}
		if _, err := transport.serve(context.Background(), ledger); !errors.Is(err, errBootstrapHandoffRejected) {
			t.Fatalf("serve = %v", err)
		}
		if ledger.calls != 1 {
			t.Fatalf("ledger calls = %d", ledger.calls)
		}
		var packet [bootstrapHandoffMaxPacket]byte
		n, _, _, _, err := unix.Recvmsg(fds[1], packet[:], nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := decodeBootstrapHandoffAck(packet[:n])
		if err != nil || ack.Result != "rejected" {
			t.Fatalf("rejection ACK = %#v, %v", ack, err)
		}
	})

	t.Run("uncertain-ledger", func(t *testing.T) {
		fds, transport := newTestBootstrapHandoff(t)
		pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
		if err != nil {
			t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
		}
		defer unix.Close(pidfd)
		request := validBootstrapHandoffRequest()
		sendBootstrapPacket(t, fds[1], request, pidfd)
		ledger := &fakeBootstrapHandoffLedger{err: errors.New("durability uncertain")}
		if _, err := transport.serve(context.Background(), ledger); !errors.Is(err, errBootstrapAckUncertain) {
			t.Fatalf("serve = %v, want restart-required uncertain outcome", err)
		}
		var packet [bootstrapHandoffMaxPacket]byte
		n, _, _, _, err := unix.Recvmsg(fds[1], packet[:], nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := decodeBootstrapHandoffAck(packet[:n])
		if err != nil || ack.Result != "restart_required" {
			t.Fatalf("uncertain ACK = %#v, %v", ack, err)
		}
	})
}

func TestBootstrapHandoffRejectsQueuedPacketsBeforeLedgerDispatch(t *testing.T) {
	fds, transport := newTestBootstrapHandoff(t)
	pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
	if err != nil {
		t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
	}
	defer unix.Close(pidfd)
	request := validBootstrapHandoffRequest()
	sendBootstrapPacket(t, fds[1], request, pidfd)
	sendBootstrapPacket(t, fds[1], request, pidfd)
	ledger := &fakeBootstrapHandoffLedger{consumed: true}
	if _, err := transport.serve(context.Background(), ledger); !errors.Is(err, errBootstrapTransportRejected) {
		t.Fatalf("serve = %v, want queued-packet rejection", err)
	}
	if ledger.calls != 0 {
		t.Fatalf("queued packet reached ledger %d times", ledger.calls)
	}
}

func TestInspectBootstrapControlRejectsUnknownAndDuplicateControlMessages(t *testing.T) {
	credential := &unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	valid := unix.UnixCredentials(credential)
	if _, got, err := inspectBootstrapControl(valid, false); err != nil || !validRecoveryCredentialSet(got, credential) {
		t.Fatalf("credential parse = %v, %v", got, err)
	}
	duplicate := append(append([]byte(nil), valid...), valid...)
	if _, _, err := inspectBootstrapControl(duplicate, false); !errors.Is(err, errBootstrapTransportRejected) {
		t.Fatalf("duplicate credentials accepted: %v", err)
	}
	unknown := unix.UnixRights(0)
	if _, _, err := inspectBootstrapControl(unknown, false); !errors.Is(err, errBootstrapTransportRejected) {
		t.Fatalf("rights-only control accepted: %v", err)
	}
	withFD := append(append([]byte(nil), valid...), unknown...)
	if fds, creds, err := inspectBootstrapControl(withFD, true); err != nil || len(fds) != 1 || !validRecoveryCredentialSet(creds, credential) {
		t.Fatalf("credential+single-rights parse = fds:%v creds:%v err:%v", fds, creds, err)
	}
}

func TestBootstrapHandoffSenderWaitsForCorrelatedACK(t *testing.T) {
	fds, transport := newTestBootstrapHandoff(t)
	request := validBootstrapHandoffRequest()
	pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
	if err != nil {
		t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
	}
	defer unix.Close(pidfd)
	peer := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	sender := os.NewFile(uintptr(fds[1]), "bootstrap-sender")
	fds[1] = -1
	defer sender.Close()
	sendDone := make(chan error, 1)
	go func() { sendDone <- sendBootstrapHandoff(context.Background(), sender, request, pidfd, peer, peer) }()
	acceptedPIDFD, err := transport.serve(context.Background(), &fakeBootstrapHandoffLedger{consumed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer acceptedPIDFD.Close()
	if err := <-sendDone; err != nil {
		t.Fatalf("sender did not accept correlated ACK: %v", err)
	}
	if _, err := transport.file.Stat(); err != nil {
		t.Fatalf("control channel closed after successful BOOTSTRAP; continuation requires it: %v", err)
	}
}

func TestBootstrapHandoffSenderRejectsNonPIDFDBeforeTransfer(t *testing.T) {
	fds, transport := newTestBootstrapHandoff(t)
	pipe := []int{0, 0}
	if err := unix.Pipe(pipe); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[0])
	defer unix.Close(pipe[1])
	sender := os.NewFile(uintptr(fds[1]), "bootstrap-sender")
	fds[1] = -1
	defer sender.Close()
	peer := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	if err := sendBootstrapHandoff(context.Background(), sender, validBootstrapHandoffRequest(), pipe[0], peer, peer); !errors.Is(err, errBootstrapTransportRejected) {
		t.Fatalf("non-pidfd sender error = %v, want rejection before transfer", err)
	}
	var packet [bootstrapHandoffMaxPacket]byte
	if _, _, _, _, err := unix.Recvmsg(int(transport.file.Fd()), packet[:], nil, unix.MSG_DONTWAIT); err == nil {
		t.Fatal("sender transferred a non-pidfd descriptor")
	}
}

func TestBootstrapHandoffSenderRejectsLedgerAndClosesControlAfterRejectedACK(t *testing.T) {
	fds, transport := newTestBootstrapHandoff(t)
	pidfd, err := unix.PidfdOpen(unix.Getpid(), 0)
	if err != nil {
		t.Fatalf("BLOCKED: required Linux pidfd_open unavailable: %v", err)
	}
	defer unix.Close(pidfd)
	peer := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	sender := os.NewFile(uintptr(fds[1]), "bootstrap-sender")
	fds[1] = -1
	defer sender.Close()
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- sendBootstrapHandoff(context.Background(), sender, validBootstrapHandoffRequest(), pidfd, peer, peer)
	}()
	_, serveErr := transport.serve(context.Background(), &fakeBootstrapHandoffLedger{consumed: false})
	if !errors.Is(serveErr, errBootstrapHandoffRejected) {
		t.Fatalf("server error = %v, want explicit rejection", serveErr)
	}
	if err := <-sendDone; !errors.Is(err, errBootstrapHandoffRejected) {
		t.Fatalf("sender error = %v, want explicit rejection", err)
	}
	if _, err := sender.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("sender control channel remains open after rejected ACK: %v", err)
	}
}

func TestReceiveBootstrapAckRejectsMismatchedGenerationAndAncillaryFD(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withFD    bool
		mutateAck func(*bootstrapHandoffAck)
	}{
		{name: "generation-mismatch", mutateAck: func(a *bootstrapHandoffAck) { a.Generation++ }},
		{name: "nonce-mismatch", mutateAck: func(a *bootstrapHandoffAck) { a.LaunchNonce = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }},
		{name: "rights-forbidden", withFD: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fds := recoverySocketPair(t)
			expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
			channel := os.NewFile(uintptr(fds[0]), "bootstrap-ack-channel")
			fds[0] = -1
			defer channel.Close()
			if err := unix.SetsockoptInt(int(channel.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
				t.Fatal(err)
			}
			request := validBootstrapHandoffRequest()
			ack := bootstrapHandoffAck{Version: 1, Type: "BOOTSTRAP_ACK", Generation: request.Generation,
				LaunchNonce: request.LaunchNonce, Result: "ok"}
			if tc.mutateAck != nil {
				tc.mutateAck(&ack)
			}
			packet, err := encodeBootstrapHandoffAck(ack)
			if err != nil {
				t.Fatal(err)
			}
			var rights []byte
			pipe := []int{-1, -1}
			if tc.withFD {
				pipe = []int{0, 0}
				if err := unix.Pipe(pipe); err != nil {
					t.Fatal(err)
				}
				defer unix.Close(pipe[0])
				defer unix.Close(pipe[1])
				rights = unix.UnixRights(pipe[0])
			}
			if n, err := unix.SendmsgN(fds[1], packet, rights, nil, 0); err != nil || n != len(packet) {
				t.Fatalf("send ACK fixture = %d, %v", n, err)
			}
			before := openProcFDs(t)
			ack, err = receiveBootstrapHandoffAck(context.Background(), channel, request, expected)
			if !errors.Is(err, errBootstrapAckUncertain) {
				t.Fatalf("invalid ACK result = %#v, %v", ack, err)
			}
			if tc.withFD {
				after := openProcFDs(t)
				if !equalIntSets(before, after) {
					t.Fatalf("forbidden ACK descriptor leaked: before=%v after=%v", before, after)
				}
			}
		})
	}
}

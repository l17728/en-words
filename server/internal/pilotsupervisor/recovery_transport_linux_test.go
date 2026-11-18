//go:build pilot_e2e && linux

package pilotsupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"golang.org/x/sys/unix"
)

func recoverySocketPair(t *testing.T) [2]int {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	})
	return fds
}

func recoverySocketFile(fd int, name string) *os.File {
	return os.NewFile(uintptr(fd), name)
}

func TestRecoveryBootstrapTransportAuthenticatesAndServesOnePacket(t *testing.T) {
	fds := recoverySocketPair(t)
	expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	controlFile := recoverySocketFile(fds[0], "recovery-control")
	transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, int(expected.Pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.close() })
	fds[0] = -1 // transport now owns the descriptor

	request := recoveryBootstrapRequest{
		Version: 1, Type: "RECOVERY_BOOTSTRAP", Generation: 2,
		LaunchNonce:      "0123456789abcdef0123456789abcdef",
		TargetGeneration: 1, TargetLaunchNonce: "abcdef0123456789abcdef0123456789",
		TargetRunEpoch: "fedcba9876543210fedcba9876543210",
	}
	packet, err := encodeRecoveryBootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := unix.SendmsgN(fds[1], packet, nil, nil, 0); err != nil || n != len(packet) {
		t.Fatalf("send request = %d, %v", n, err)
	}
	ledger := &fakeRecoveryBootstrapLedger{snapshot: recoveryTargetSnapshot{Identity: recoveryTargetIdentity{
		Generation: 1, LaunchNonce: request.TargetLaunchNonce, RunEpoch: request.TargetRunEpoch,
	}, Found: true, Terminal: true}}
	ackPacket := make([]byte, recoveryBootstrapMaxPacket)
	serveDone := make(chan error, 1)
	go func() { serveDone <- transport.serveRecoveryBootstrap(context.Background(), ledger) }()
	n, _, _, _, err := unix.Recvmsg(fds[1], ackPacket, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	ack, err := decodeRecoveryBootstrapAck(ackPacket[:n])
	if err != nil || !recoveryBootstrapAckMatches(request, ack) || ack.Result != "already_terminal" {
		t.Fatalf("ACK = %#v, %v", ack, err)
	}
	if ledger.loadCalls != 1 || ledger.finalizeCalls != 0 {
		t.Fatalf("ledger calls load=%d finalize=%d", ledger.loadCalls, ledger.finalizeCalls)
	}
	if _, err := transport.receiveRequest(); !errors.Is(err, errRecoveryTransportRejected) {
		t.Fatalf("second request call error = %v", err)
	}
}

func TestRecoveryBootstrapTransportRejectsAndClosesSCMRights(t *testing.T) {
	fds := recoverySocketPair(t)
	expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	controlFile := recoverySocketFile(fds[0], "recovery-control")
	transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, int(expected.Pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.close() })
	fds[0] = -1

	pipe := []int{0, 0}
	if err := unix.Pipe(pipe); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[0])
	defer unix.Close(pipe[1])
	packet, err := encodeRecoveryBootstrap(recoveryBootstrapRequest{
		Version: 1, Type: "RECOVERY_BOOTSTRAP", Generation: 2,
		LaunchNonce: "0123456789abcdef0123456789abcdef", TargetGeneration: 1,
		TargetLaunchNonce: "abcdef0123456789abcdef0123456789",
		TargetRunEpoch:    "fedcba9876543210fedcba9876543210",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := openProcFDs(t)
	before = removeFD(before, int(controlFile.Fd()))
	if n, err := unix.SendmsgN(fds[1], packet, unix.UnixRights(pipe[0]), nil, 0); err != nil || n != len(packet) {
		t.Fatalf("send descriptor-bearing request = %d, %v", n, err)
	}
	if err := transport.serveRecoveryBootstrap(context.Background(), &fakeRecoveryBootstrapLedger{}); !errors.Is(err, errRecoveryTransportRejected) {
		t.Fatalf("serve error = %v, want transport rejection", err)
	}
	if _, err := controlFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("transport file not closed after rejection: %v", err)
	}

	if after := openProcFDs(t); !equalIntSets(before, after) {
		t.Fatalf("descriptor set changed after rejected SCM_RIGHTS: before=%v after=%v", before, after)
	}
}

func TestRecoveryBootstrapAncillaryParserRejectsBadAndDuplicateCredentials(t *testing.T) {
	credential := &unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	one := unix.UnixCredentials(credential)
	if _, creds, err := inspectRecoveryControl(one); err != nil || !validRecoveryCredentialSet(creds, credential) {
		t.Fatalf("single credentials = %v, %v", creds, err)
	}
	duplicate := append(append([]byte{}, one...), one...)
	if _, creds, err := inspectRecoveryControl(duplicate); err != nil || validRecoveryCredentialSet(creds, credential) {
		t.Fatalf("duplicate credentials = %v, %v; transport must reject count != 1", creds, err)
	}
	for _, mismatch := range []*unix.Ucred{
		{Pid: credential.Pid + 1, Uid: credential.Uid, Gid: credential.Gid},
		{Pid: credential.Pid, Uid: credential.Uid + 1, Gid: credential.Gid},
		{Pid: credential.Pid, Uid: credential.Uid, Gid: credential.Gid + 1},
	} {
		if validRecoveryCredentialSet([]*unix.Ucred{mismatch}, credential) {
			t.Fatalf("credential mismatch accepted: %+v", mismatch)
		}
	}
	if _, _, err := inspectRecoveryControl([]byte{0x01}); !errors.Is(err, errRecoveryTransportRejected) {
		t.Fatalf("malformed ancillary error = %v", err)
	}
}

func openProcFDs(t *testing.T) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	result := make([]int, 0, len(entries))
	for _, entry := range entries {
		var fd int
		if _, err := fmt.Sscanf(entry.Name(), "%d", &fd); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Readlink("/proc/self/fd/" + entry.Name()); err == nil {
			result = append(result, fd)
		}
	}
	sort.Ints(result)
	return result
}

func removeFD(fds []int, target int) []int {
	result := make([]int, 0, len(fds))
	for _, fd := range fds {
		if fd != target {
			result = append(result, fd)
		}
	}
	return result
}

func equalIntSets(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestRecoveryBootstrapTransportRejectsWrongPeerAndSocketType(t *testing.T) {
	t.Run("wrong-peer-pid", func(t *testing.T) {
		fds := recoverySocketPair(t)
		file := recoverySocketFile(fds[0], "recovery-control")
		fds[0] = -1
		defer file.Close()
		_, err := newRecoveryBootstrapTransportForParent(file, unix.Ucred{Pid: int32(unix.Getpid() + 1), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}, unix.Getpid()+1)
		if !errors.Is(err, errRecoveryTransportRejected) {
			t.Fatalf("constructor error = %v", err)
		}
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("constructor failure did not close owned file: %v", err)
		}
	})
	t.Run("parent-pid-mismatch", func(t *testing.T) {
		fds := recoverySocketPair(t)
		file := recoverySocketFile(fds[0], "recovery-control")
		fds[0] = -1
		expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
		_, err := newRecoveryBootstrapTransportForParent(file, expected, unix.Getpid()+1)
		if !errors.Is(err, errRecoveryTransportRejected) {
			t.Fatalf("constructor error = %v", err)
		}
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("constructor failure did not close owned file: %v", err)
		}
	})
	t.Run("stream-socket", func(t *testing.T) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		file := recoverySocketFile(fds[0], "wrong-type")
		fds[0] = -1
		defer file.Close()
		_, err = newRecoveryBootstrapTransportForParent(file, unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}, unix.Getpid())
		_ = unix.Close(fds[1])
		if !errors.Is(err, errRecoveryTransportRejected) {
			t.Fatalf("constructor error = %v", err)
		}
	})
}

func TestRecoveryBootstrapTransportRejectsTruncatedPacketsAndAncillary(t *testing.T) {
	t.Run("oversized-payload", func(t *testing.T) {
		fds := recoverySocketPair(t)
		expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
		controlFile := recoverySocketFile(fds[0], "recovery-control")
		transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, unix.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		fds[0] = -1
		if _, err := unix.SendmsgN(fds[1], make([]byte, recoveryBootstrapMaxPacket+1), nil, nil, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := transport.receiveRequest(); !errors.Is(err, errRecoveryTransportRejected) {
			t.Fatalf("oversized receive error = %v", err)
		}
		_ = transport.close()
	})
	t.Run("ancillary-truncation-closes-visible-rights", func(t *testing.T) {
		fds := recoverySocketPair(t)
		expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
		controlFile := recoverySocketFile(fds[0], "recovery-control")
		transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, unix.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		fds[0] = -1
		pipe := []int{0, 0}
		if err := unix.Pipe(pipe); err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pipe[0])
		defer unix.Close(pipe[1])
		packet, err := encodeRecoveryBootstrap(validRecoveryRequest(2))
		if err != nil {
			t.Fatal(err)
		}
		before := removeFD(openProcFDs(t), int(controlFile.Fd()))
		closedRights := 0
		cloexecRights := 0
		transport.closeReceivedFD = func(fd int) error {
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err == nil {
				closedRights++
				if flags&unix.FD_CLOEXEC != 0 {
					cloexecRights++
				}
			}
			return unix.Close(fd)
		}
		rights := unix.UnixRights(pipe[0], pipe[0], pipe[0])
		if n, err := unix.SendmsgN(fds[1], packet, rights, nil, 0); err != nil || n != len(packet) {
			t.Fatalf("send rights request = %d, %v", n, err)
		}
		controlSize := unix.CmsgSpace(unix.SizeofUcred) + unix.CmsgSpace(2*4)
		if _, err := transport.receiveRequestContextWithControlSize(context.Background(), controlSize); !errors.Is(err, errRecoveryTransportRejected) {
			t.Fatalf("truncated ancillary receive error = %v", err)
		}
		if closedRights == 0 || cloexecRights != closedRights {
			t.Fatalf("visible truncated rights closed=%d cloexec=%d; need visible CLOEXEC FDs to be closed", closedRights, cloexecRights)
		}
		_ = transport.close()
		if after := openProcFDs(t); !equalIntSets(before, after) {
			t.Fatalf("FD set changed after ancillary truncation: before=%v after=%v", before, after)
		}
	})
	t.Run("canceled-before-request-closes-channel", func(t *testing.T) {
		fds := recoverySocketPair(t)
		expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
		controlFile := recoverySocketFile(fds[0], "recovery-control")
		transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, unix.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		fds[0] = -1
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := transport.serveRecoveryBootstrap(ctx, &fakeRecoveryBootstrapLedger{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("serve error = %v", err)
		}
		if _, err := controlFile.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("transport file remained open: %v", err)
		}
	})
}

func TestRecoveryBootstrapTransportClosesOnMalformedAndUncertainAck(t *testing.T) {
	t.Run("malformed-request", func(t *testing.T) {
		fds := recoverySocketPair(t)
		expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
		controlFile := recoverySocketFile(fds[0], "recovery-control")
		transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, unix.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		fds[0] = -1
		if _, err := unix.Write(fds[1], []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if err := transport.serveRecoveryBootstrap(context.Background(), &fakeRecoveryBootstrapLedger{}); !errors.Is(err, errRecoveryBootstrapInvalid) {
			t.Fatalf("serve error = %v", err)
		}
		if _, err := controlFile.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("transport file remained open: %v", err)
		}
		var ack [recoveryBootstrapMaxPacket]byte
		n, _, _, _, err := unix.Recvmsg(fds[1], ack[:], nil, unix.MSG_DONTWAIT)
		if err == nil && n != 0 {
			t.Fatalf("malformed request received an ACK: %s", ack[:n])
		}
	})
	t.Run("ack-delivery-uncertain", func(t *testing.T) {
		fds := recoverySocketPair(t)
		expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
		controlFile := recoverySocketFile(fds[0], "recovery-control")
		transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, unix.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		fds[0] = -1
		request, err := encodeRecoveryBootstrap(validRecoveryRequest(2))
		if err != nil {
			t.Fatal(err)
		}
		if n, err := unix.SendmsgN(fds[1], request, nil, nil, 0); err != nil || n != len(request) {
			t.Fatalf("send request = %d, %v", n, err)
		}
		// Keep the peer endpoint alive so the authenticated request is consumed,
		// but make ACK delivery impossible after the durable ledger transition.
		if err := unix.Shutdown(fds[1], unix.SHUT_RD); err != nil {
			t.Fatal(err)
		}
		ledger := &fakeRecoveryBootstrapLedger{snapshot: readyRecoverySnapshot(), finalizeResult: true}
		if err := transport.serveRecoveryBootstrap(context.Background(), ledger); !errors.Is(err, errRecoveryAckDeliveryUncertain) {
			t.Fatalf("serve error = %v, want uncertain ACK", err)
		}
		if ledger.finalizeCalls != 1 {
			t.Fatalf("finalize calls = %d, want 1 before uncertain ACK", ledger.finalizeCalls)
		}
		if !reflect.DeepEqual(ledger.events, []string{"consume", "load", "finalize"}) {
			t.Fatalf("ledger event order before uncertain ACK = %v", ledger.events)
		}
		if _, err := controlFile.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("transport file remained open: %v", err)
		}
	})
}

func TestRecoveryBootstrapTransportRejectsQueuedSecondPacket(t *testing.T) {
	fds := recoverySocketPair(t)
	expected := unix.Ucred{Pid: int32(unix.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	controlFile := recoverySocketFile(fds[0], "recovery-control")
	transport, err := newRecoveryBootstrapTransportForParent(controlFile, expected, unix.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	fds[0] = -1
	packet, err := encodeRecoveryBootstrap(validRecoveryRequest(2))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i != 2; i++ {
		if n, err := unix.SendmsgN(fds[1], packet, nil, nil, 0); err != nil || n != len(packet) {
			t.Fatalf("send packet %d = %d, %v", i, n, err)
		}
	}
	ledger := &fakeRecoveryBootstrapLedger{snapshot: readyRecoverySnapshot(), finalizeResult: true}
	if err := transport.serveRecoveryBootstrap(context.Background(), ledger); !errors.Is(err, errRecoveryTransportRejected) {
		t.Fatalf("serve error = %v, want queued-packet rejection", err)
	}
	if ledger.loadCalls != 0 || ledger.finalizeCalls != 0 {
		t.Fatalf("queued packet reached ledger: load=%d finalize=%d", ledger.loadCalls, ledger.finalizeCalls)
	}
	if _, err := controlFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("transport file remained open: %v", err)
	}
}

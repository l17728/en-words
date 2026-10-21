//go:build pilot_e2e

package pilotsupervisor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testCreateRequest = "create-request-0001"
const testPrincipal = "principal-student-a"
const testCaseSelector = "abcdef0123456789abcdef0123456789"
const testCreateDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testWaitTimeout = 2 * time.Second

func awaitCreateBounded(lease *CreateLease) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), testWaitTimeout)
	defer cancel()
	return lease.Await(ctx)
}

func waitGroupBounded(t *testing.T, wg *sync.WaitGroup, description string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestCreateRegistryCoalescesConcurrentExactRetries(t *testing.T) {
	var registry CreateRegistry
	owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	const retries = 32
	leases := make([]*CreateLease, retries)
	for i := range leases {
		leases[i], err = registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		if leases[i].owner {
			t.Fatal("retry became a second dispatcher")
		}
	}

	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, lease := range leases {
		wg.Add(1)
		go func(lease *CreateLease) {
			defer wg.Done()
			got, err := awaitCreateBounded(lease)
			if err == nil && got == testCaseSelector {
				successes.Add(1)
			}
		}(lease)
	}
	if _, err := owner.Dispatch(func() (string, error) { return testCaseSelector, nil }); err != nil {
		t.Fatal(err)
	}
	waitGroupBounded(t, &wg, "retry waiters")
	if got := successes.Load(); got != retries {
		t.Fatalf("exact retries returned cached selector %d/%d times", got, retries)
	}
	if _, err := owner.Dispatch(func() (string, error) { return testCaseSelector, nil }); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("second dispatch error = %v, want invalid grant", err)
	}
}

func TestCreateRegistryConcurrentFirstUseHasOneOwner(t *testing.T) {
	var registry CreateRegistry
	const calls = 32
	start := make(chan struct{})
	leases := make(chan *CreateLease, calls)
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
			if err != nil {
				errs <- err
				return
			}
			leases <- lease
		}()
	}
	close(start)
	waitGroupBounded(t, &wg, "concurrent first-use calls")
	close(leases)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	all := make([]*CreateLease, 0, calls)
	owners := 0
	for lease := range leases {
		all = append(all, lease)
		if lease.owner {
			owners++
		}
	}
	if owners != 1 || len(all) != calls {
		t.Fatalf("concurrent first use created %d leases with %d owners, want %d leases and one owner", len(all), owners, calls)
	}
	for _, lease := range all {
		if lease.owner {
			if _, err := lease.Dispatch(func() (string, error) { return testCaseSelector, nil }); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	for _, lease := range all {
		if got, err := awaitCreateBounded(lease); err != nil || got != testCaseSelector {
			t.Fatalf("concurrent first-use result = (%q, %v), want selector", got, err)
		}
	}
}

func TestCreateRegistryRejectsCrossPrincipalOrPayloadConflict(t *testing.T) {
	var registry CreateRegistry
	owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{
		{testPrincipal, "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", testCreateRequest},
		{"principal-student-b", testCreateDigest, testCreateRequest},
	} {
		_, err := registry.BeginCreate(testEpoch, args[2], args[0], args[1])
		if !errors.Is(err, errCreateConflict) {
			t.Fatalf("conflicting retry error = %v, want conflict", err)
		}
	}
	if _, err := owner.Dispatch(func() (string, error) { return "", errors.New("uncertain") }); err == nil {
		t.Fatal("dispatch error was not preserved")
	}
	if _, err := awaitCreateBounded(owner); !errors.Is(err, errCreateUnknown) {
		t.Fatalf("uncertain owner result = %v, want unknown", err)
	}
	duplicate, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awaitCreateBounded(duplicate); !errors.Is(err, errCreateUnknown) {
		t.Fatalf("uncertain retry result = %v, want unknown", err)
	}
}

func TestCreateRegistryCanceledWaitDoesNotReleaseDispatchLease(t *testing.T) {
	var registry CreateRegistry
	owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waiter.Await(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	if _, err := owner.Dispatch(func() (string, error) { return testCaseSelector, nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := awaitCreateBounded(waiter); err != nil || got != testCaseSelector {
		t.Fatalf("retry after canceled wait = (%q, %v), want cached selector", got, err)
	}
}

func TestCreateRegistryDispatchErrorAndPanicAreTerminal(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		var registry CreateRegistry
		owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		backendErr := errors.New("sensitive backend detail")
		if _, err := owner.Dispatch(func() (string, error) { return "", backendErr }); !errors.Is(err, errCreateUnknown) {
			t.Fatalf("dispatch error = %v, want generic uncertain result", err)
		} else if err.Error() == backendErr.Error() {
			t.Fatal("raw backend error escaped the dispatch boundary")
		}
		retry, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := awaitCreateBounded(retry); !errors.Is(err, errCreateUnknown) {
			t.Fatalf("retry after dispatch error = %v, want terminal unknown", err)
		}
	})

	t.Run("panic", func(t *testing.T) {
		var registry CreateRegistry
		owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if recovered := recover(); recovered != "backend panic" {
					t.Fatalf("panic = %v, want backend panic", recovered)
				}
			}()
			_, _ = owner.Dispatch(func() (string, error) { panic("backend panic") })
		}()
		retry, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := awaitCreateBounded(retry); !errors.Is(err, errCreateUnknown) {
			t.Fatalf("retry after dispatch panic = %v, want terminal unknown", err)
		}
	})

	t.Run("invalid selector", func(t *testing.T) {
		var registry CreateRegistry
		owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Dispatch(func() (string, error) { return "../untrusted", nil }); !errors.Is(err, errGrantInvalid) {
			t.Fatalf("invalid selector dispatch error = %v, want invalid grant", err)
		}
		retry, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := awaitCreateBounded(retry); !errors.Is(err, errCreateUnknown) {
			t.Fatalf("retry after invalid selector = %v, want terminal unknown", err)
		}
	})
}

func TestCreateRegistryDispatchClaimIsSharedByCopiedLease(t *testing.T) {
	var registry CreateRegistry
	owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	copiedOwner := *owner
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var calls atomic.Int32
	firstDone := make(chan error, 1)
	go func() {
		_, err := owner.Dispatch(func() (string, error) {
			calls.Add(1)
			close(started)
			<-release
			return testCaseSelector, nil
		})
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first dispatcher did not enter create callback")
	}
	if _, err := copiedOwner.Dispatch(func() (string, error) {
		calls.Add(1)
		return testCaseSelector, nil
	}); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("copied lease dispatch error = %v, want already-claimed rejection", err)
	}
	close(release)
	released = true
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("create callback invoked %d times, want exactly once", got)
	}
	retry, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := awaitCreateBounded(retry); err != nil || got != testCaseSelector {
		t.Fatalf("cached dispatched result = (%q, %v), want selector", got, err)
	}
}

func TestCreateRegistryGoexitDoesNotLeaveRetriesBlocked(t *testing.T) {
	var registry CreateRegistry
	owner, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := registry.BeginCreate(testEpoch, testCreateRequest, testPrincipal, testCreateDigest)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = owner.Dispatch(func() (string, error) {
			runtime.Goexit()
			return "", nil
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Goexit dispatcher did not terminate")
	}
	if _, err := awaitCreateBounded(waiter); !errors.Is(err, errCreateUnknown) {
		t.Fatalf("retry after Goexit = %v, want terminal unknown", err)
	}
}

func TestCreateRegistryValidatesIdentityAndBoundsRunEntries(t *testing.T) {
	var registry CreateRegistry
	if _, err := registry.BeginCreate("../invalid", testCreateRequest, testPrincipal, testCreateDigest); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("invalid epoch error = %v", err)
	}
	for i := 0; i < maxCreateRequests; i++ {
		requestID := fmt.Sprintf("request-%04d-0000", i)
		if _, err := registry.BeginCreate(testEpoch, requestID, testPrincipal, testCreateDigest); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}
	otherEpoch := "fedcba9876543210fedcba9876543210"
	if _, err := registry.BeginCreate(otherEpoch, "another-epoch-request", testPrincipal, testCreateDigest); !errors.Is(err, errGrantInvalid) {
		t.Fatalf("second epoch error = %v, want same-run rejection", err)
	}
	if _, err := registry.BeginCreate(testEpoch, "one-more-request", testPrincipal, testCreateDigest); !errors.Is(err, errCreateRegistryFull) {
		t.Fatalf("full registry error = %v, want bounded failure", err)
	}
}

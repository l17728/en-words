//go:build pilot_e2e

package pilotsupervisor

import (
	"context"
	"errors"
	"sync"
)

var (
	errCreateConflict     = errors.New("pilot create request conflict")
	errCreateUnknown      = errors.New("pilot create outcome unknown")
	errCreateRegistryFull = errors.New("pilot create registry full")
)

const maxCreateRequests = 4096

type createKey struct {
	epoch     string
	requestID string
}

type createEntry struct {
	principal string
	digest    string
	done      chan struct{}
	selector  string
	state     createState
}

type createState uint8

const (
	createPending createState = iota
	createDispatching
	createComplete
	createUncertain
)

// CreateRegistry coalesces retries for one live Gateway run. It is deliberately
// process-local: a Gateway restart invalidates the run and its worker instead
// of attempting to reconstruct authority from volatile mappings.
type CreateRegistry struct {
	mu       sync.Mutex
	runEpoch string
	entries  map[createKey]*createEntry
}

// CreateLease is held by the sole dispatcher for a request. Until it resolves,
// exact retries wait on the same entry and conflicting retries fail closed.
type CreateLease struct {
	registry *CreateRegistry
	entry    *createEntry
	owner    bool
}

// BeginCreate returns the unique dispatch lease for the first request. Exact
// retries receive a non-owner lease that can only await/cache the first result.
func (r *CreateRegistry) BeginCreate(epoch, requestID, principal, digest string) (*CreateLease, error) {
	if r == nil || !epochPattern.MatchString(epoch) || !identifier.MatchString(requestID) ||
		!identifier.MatchString(principal) || !digestPattern.MatchString(digest) {
		return nil, errGrantInvalid
	}
	key := createKey{epoch: epoch, requestID: requestID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runEpoch == "" {
		r.runEpoch = epoch
	} else if r.runEpoch != epoch {
		return nil, errGrantInvalid
	}
	if r.entries == nil {
		r.entries = make(map[createKey]*createEntry)
	}
	if existing := r.entries[key]; existing != nil {
		if existing.principal != principal || existing.digest != digest {
			return nil, errCreateConflict
		}
		return &CreateLease{registry: r, entry: existing}, nil
	}
	if len(r.entries) >= maxCreateRequests {
		return nil, errCreateRegistryFull
	}
	entry := &createEntry{principal: principal, digest: digest, done: make(chan struct{}), state: createPending}
	r.entries[key] = entry
	return &CreateLease{registry: r, entry: entry, owner: true}, nil
}

// Await returns the cached selector after the owner commits it. An uncertain
// result is terminal for this run and must never trigger a second dispatch.
func (l *CreateLease) Await(ctx context.Context) (string, error) {
	if l == nil || l.registry == nil || l.entry == nil || ctx == nil {
		return "", errGrantInvalid
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-l.entry.done:
	}
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	switch l.entry.state {
	case createComplete:
		return l.entry.selector, nil
	case createUncertain:
		return "", errCreateUnknown
	default:
		return "", errGrantInvalid
	}
}

// Dispatch runs the sole create operation and closes the lease on every exit.
// Any error or panic is conservatively uncertain because a side effect may
// already be durable; the registry must never guess and issue it again.
func (l *CreateLease) Dispatch(create func() (string, error)) (selector string, err error) {
	if l == nil || !l.owner || l.registry == nil || l.entry == nil || create == nil {
		return "", errGrantInvalid
	}
	l.registry.mu.Lock()
	if l.entry.state != createPending {
		l.registry.mu.Unlock()
		return "", errGrantInvalid
	}
	l.entry.state = createDispatching
	l.registry.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			l.finishDispatchUncertain()
			panic(recovered)
		}
		// Also handles runtime.Goexit and every ordinary early return. The
		// transition is a no-op after successful completion.
		l.finishDispatchUncertain()
	}()
	selector, err = create()
	if err != nil {
		// The callback may carry paths, IDs, or other diagnostics. Keep this
		// boundary's outward error stable and non-sensitive.
		return "", errCreateUnknown
	}
	if !caseIDPattern.MatchString(selector) {
		return "", errGrantInvalid
	}
	if err = l.finishDispatchComplete(selector); err != nil {
		return "", err
	}
	return selector, nil
}

func (l *CreateLease) finishDispatchComplete(selector string) error {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	if l.entry.state != createDispatching {
		return errGrantInvalid
	}
	l.entry.selector = selector
	l.entry.state = createComplete
	close(l.entry.done)
	return nil
}

func (l *CreateLease) finishDispatchUncertain() {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	if l.entry.state == createDispatching {
		l.entry.state = createUncertain
		close(l.entry.done)
	}
}

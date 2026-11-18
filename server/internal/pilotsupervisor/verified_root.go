//go:build pilot_e2e

package pilotsupervisor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"
)

var (
	errVerifiedAnchorInvalid        = errors.New("verified_anchor_invalid")
	errVerifiedPathInvalid          = errors.New("verified_path_invalid")
	errVerifiedMemberInvalid        = errors.New("verified_member_invalid")
	errVerifiedPolicyMismatch       = errors.New("verified_policy_mismatch")
	errVerifiedIdentityChanged      = errors.New("verified_identity_changed")
	errVerifiedIOFailed             = errors.New("verified_io_failed")
	errVerifiedTimeout              = errors.New("verified_timeout")
	errVerifiedCloseUnknown         = errors.New("verified_close_unknown")
	errVerifiedConfigurationMissing = errors.New("verified_configuration_missing")
)

// RunEpoch is an opaque, canonical run identifier. Construct it with ParseRunEpoch.
type RunEpoch string

var runEpochPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ParseRunEpoch(value string) (RunEpoch, error) {
	if !runEpochPattern.MatchString(value) {
		return "", errVerifiedPathInvalid
	}
	return RunEpoch(value), nil
}

type DirectoryName uint8

const (
	CaseLocksDir DirectoryName = iota + 1
	CasesDir
	LogsDir
)

type MemberName uint8

const (
	CatalogBootstrapLock MemberName = iota + 1
	CatalogLog
)

type RootEntry uint8

const (
	RootCatalogBootstrapLock RootEntry = iota + 1
	RootCatalogLog
	RootCaseLocksDir
	RootCasesDir
	RootLogsDir
)

type verifiedKind uint8

const (
	verifiedDirectory verifiedKind = iota + 1
	verifiedRegularFile
)

type verifiedIdentity struct {
	volume     uint64
	object     uint64
	objectHigh uint64
	mount      uint64
	kind       verifiedKind
}

type verifiedNative interface {
	open(parent uintptr, name string, kind verifiedKind) (uintptr, error)
	inspect(handle uintptr, kind verifiedKind) (verifiedIdentity, error)
	enumerate(handle uintptr) ([]string, error)
	verifyBinding(parent uintptr, name string, expected verifiedIdentity, kind verifiedKind) error
	checkFilesystem(handle uintptr) error
	close(handle uintptr) error
}

type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// AnchorHandle is borrowed by OpenVerifiedRoot and must remain open until the
// returned root is closed. Its fields are intentionally private.
type AnchorHandle struct {
	noCopy
	mu       sync.Mutex
	handle   uintptr
	identity verifiedIdentity
	ops      verifiedNative
	closed   bool
}

type verifiedChild struct {
	noCopy
	mu       sync.Mutex
	handle   uintptr
	parent   uintptr
	name     string
	kind     verifiedKind
	identity verifiedIdentity
	ops      verifiedNative
	root     *VerifiedRoot
	closed   bool
}

type VerifiedDirectory struct{ child *verifiedChild }
type VerifiedFile struct{ child *verifiedChild }

type VerifiedRoot struct {
	noCopy
	mu             sync.Mutex
	inventoryMu    sync.Mutex
	cond           *sync.Cond
	handle         uintptr
	parent         *AnchorHandle
	name           string
	traceID        string
	identity       verifiedIdentity
	ops            verifiedNative
	children       []*verifiedChild
	unknownHandles []uintptr
	inFlight       int
	gated          bool
	closing        bool
	closed         bool
	closeInFlight  bool
	closeUnknown   error
	poison         error
}

var verifiedBootstrapOrphans struct {
	sync.Mutex
	handles []uintptr
}

func retainVerifiedOrphan(handle uintptr) {
	if handle == 0 {
		return
	}
	verifiedBootstrapOrphans.Lock()
	verifiedBootstrapOrphans.handles = append(verifiedBootstrapOrphans.handles, handle)
	verifiedBootstrapOrphans.Unlock()
}

func OpenVerifiedRoot(anchor *AnchorHandle, epoch RunEpoch) (*VerifiedRoot, error) {
	started := time.Now()
	traceID, traceErr := newVerifiedTraceID()
	if traceErr != nil {
		emitVerifiedEvent("", "root_open", "directory", "error", "verified_io_failed", started)
		return nil, errVerifiedIOFailed
	}
	if anchor == nil || !runEpochPattern.MatchString(string(epoch)) {
		if anchor == nil {
			emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_anchor_invalid", started)
			return nil, errVerifiedAnchorInvalid
		}
		emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_path_invalid", started)
		return nil, errVerifiedPathInvalid
	}
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.closed || anchor.handle == 0 || anchor.ops == nil {
		emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_anchor_invalid", started)
		return nil, errVerifiedAnchorInvalid
	}
	actual, err := anchor.ops.inspect(anchor.handle, verifiedDirectory)
	if err != nil {
		emitVerifiedEvent(traceID, "root_open", "directory", "error", verifiedErrorClass(err), started)
		return nil, err
	}
	if actual != anchor.identity {
		emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_anchor_invalid", started)
		return nil, errVerifiedAnchorInvalid
	}
	h, err := anchor.ops.open(anchor.handle, string(epoch), verifiedDirectory)
	if err != nil {
		classified := classifyOpenError(err)
		emitVerifiedEvent(traceID, "root_open", "directory", "error", verifiedErrorClass(classified), started)
		return nil, classified
	}
	id, err := anchor.ops.inspect(h, verifiedDirectory)
	if err == nil {
		err = anchor.ops.verifyBinding(anchor.handle, string(epoch), id, verifiedDirectory)
	}
	if err == nil {
		err = anchor.ops.checkFilesystem(h)
	}
	if err != nil {
		if errors.Is(err, errVerifiedCloseUnknown) {
			retainVerifiedOrphan(h)
			emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_close_unknown", started)
			return nil, errVerifiedCloseUnknown
		}
		if closeErr := anchor.ops.close(h); closeErr != nil {
			verifiedBootstrapOrphans.Lock()
			verifiedBootstrapOrphans.handles = append(verifiedBootstrapOrphans.handles, h)
			verifiedBootstrapOrphans.Unlock()
			emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_close_unknown", started)
			return nil, errVerifiedCloseUnknown
		}
		emitVerifiedEvent(traceID, "root_open", "directory", "error", verifiedErrorClass(err), started)
		return nil, err
	}
	r := &VerifiedRoot{handle: h, parent: anchor, name: string(epoch), traceID: traceID, identity: id, ops: anchor.ops}
	r.cond = sync.NewCond(&r.mu)
	if _, err = r.validateRootInventory(); err != nil {
		r.mu.Lock()
		unknown := append([]uintptr(nil), r.unknownHandles...)
		closeUnknown := r.closeUnknown != nil
		r.mu.Unlock()
		if closeUnknown {
			// An uncertain child close terminates the rollback sequence. Keep all
			// still-owned kernel handles reachable until the worker exits.
			verifiedBootstrapOrphans.Lock()
			verifiedBootstrapOrphans.handles = append(verifiedBootstrapOrphans.handles, unknown...)
			verifiedBootstrapOrphans.handles = append(verifiedBootstrapOrphans.handles, h)
			verifiedBootstrapOrphans.Unlock()
			emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_close_unknown", started)
			return nil, errVerifiedCloseUnknown
		}
		if closeErr := anchor.ops.close(h); closeErr != nil {
			verifiedBootstrapOrphans.Lock()
			verifiedBootstrapOrphans.handles = append(verifiedBootstrapOrphans.handles, h)
			verifiedBootstrapOrphans.Unlock()
			emitVerifiedEvent(traceID, "root_open", "directory", "error", "verified_close_unknown", started)
			return nil, errVerifiedCloseUnknown
		}
		emitVerifiedEvent(traceID, "root_open", "directory", "error", verifiedErrorClass(err), started)
		return nil, err
	}
	emitVerifiedEvent(traceID, "root_open", "directory", "ok", "", started)
	return r, nil
}

func newVerifiedTraceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func emitVerifiedEvent(traceID, event, objectKind, result, errorClass string, started time.Time) {
	if errorClass == "" {
		slog.Info("pilot filesystem operation", "trace_id", traceID, "module", "pilot.fs", "event", event, "object_kind", objectKind, "result", result, "duration_ms", time.Since(started).Milliseconds())
		return
	}
	slog.Info("pilot filesystem operation", "trace_id", traceID, "module", "pilot.fs", "event", event, "object_kind", objectKind, "result", result, "duration_ms", time.Since(started).Milliseconds(), "error_class", errorClass)
}

func verifiedErrorClass(err error) string {
	switch {
	case errors.Is(err, errVerifiedAnchorInvalid):
		return "verified_anchor_invalid"
	case errors.Is(err, errVerifiedPathInvalid):
		return "verified_path_invalid"
	case errors.Is(err, errVerifiedMemberInvalid):
		return "verified_member_invalid"
	case errors.Is(err, errVerifiedPolicyMismatch):
		return "verified_policy_mismatch"
	case errors.Is(err, errVerifiedIdentityChanged):
		return "verified_identity_changed"
	case errors.Is(err, errVerifiedTimeout):
		return "verified_timeout"
	case errors.Is(err, errVerifiedCloseUnknown):
		return "verified_close_unknown"
	case errors.Is(err, errVerifiedConfigurationMissing):
		return "verified_configuration_missing"
	default:
		return "verified_io_failed"
	}
}

func classifyOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errVerifiedPathInvalid) || errors.Is(err, errVerifiedMemberInvalid) || errors.Is(err, errVerifiedPolicyMismatch) || errors.Is(err, errVerifiedIdentityChanged) || errors.Is(err, errVerifiedIOFailed) || errors.Is(err, errVerifiedTimeout) || errors.Is(err, errVerifiedCloseUnknown) {
		return err
	}
	return fmt.Errorf("%w: %w", errVerifiedIOFailed, err)
}

func (r *VerifiedRoot) begin() error {
	if r == nil {
		return errVerifiedPathInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.closing || r.gated || r.poison != nil {
		if r.poison != nil {
			return r.poison
		}
		return errVerifiedPathInvalid
	}
	r.inFlight++
	return nil
}

func (r *VerifiedRoot) end() {
	r.mu.Lock()
	r.inFlight--
	if r.inFlight == 0 {
		r.cond.Broadcast()
	}
	r.mu.Unlock()
}

func (r *VerifiedRoot) poisonWith(err error) error {
	if err == nil {
		return nil
	}
	r.mu.Lock()
	if r.poison == nil {
		r.poison = err
	}
	if errors.Is(err, errVerifiedCloseUnknown) {
		r.closeUnknown = errVerifiedCloseUnknown
	}
	poison := r.poison
	r.mu.Unlock()
	return poison
}

// ensureUsable is called after acquiring inventoryMu so an operation that was
// queued before another operation poisoned the root cannot touch the OS.
func (r *VerifiedRoot) ensureUsable() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poison != nil {
		return r.poison
	}
	if r.closed || r.closing || r.gated {
		return errVerifiedPathInvalid
	}
	return nil
}

func (r *VerifiedRoot) retainUnknown(handles []uintptr) {
	r.mu.Lock()
	r.unknownHandles = append(r.unknownHandles, handles...)
	r.closeUnknown = errVerifiedCloseUnknown
	r.poison = errVerifiedCloseUnknown
	r.mu.Unlock()
}

// waitForInflightLocked is called with r.mu held. The deadline timer wakes a
// Cond waiter even when no operation completes before the bounded drain.
func (r *VerifiedRoot) waitForInflightLocked(deadline time.Time) bool {
	if r.inFlight == 0 {
		return true
	}
	timer := time.AfterFunc(time.Until(deadline), func() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	})
	defer timer.Stop()
	for r.inFlight > 0 && time.Now().Before(deadline) {
		r.cond.Wait()
	}
	return r.inFlight == 0
}

func (r *VerifiedRoot) VerifyIdentity() error {
	started := time.Now()
	if err := r.begin(); err != nil {
		if r != nil {
			emitVerifiedEvent(r.traceID, "identity", "directory", "error", verifiedErrorClass(err), started)
		}
		return err
	}
	defer r.end()
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	if err := r.ensureUsable(); err != nil {
		emitVerifiedEvent(r.traceID, "identity", "directory", "error", verifiedErrorClass(err), started)
		return err
	}
	err := r.verifyRootIdentity()
	result, class := "ok", ""
	if err != nil {
		result, class = "error", verifiedErrorClass(err)
	}
	emitVerifiedEvent(r.traceID, "identity", "directory", result, class, started)
	return err
}

func (r *VerifiedRoot) verifyRootIdentity() error {
	actual, err := r.ops.inspect(r.handle, verifiedDirectory)
	if err == nil && actual != r.identity {
		err = errVerifiedIdentityChanged
	}
	if err == nil {
		err = r.ops.verifyBinding(r.parent.handle, r.name, r.identity, verifiedDirectory)
	}
	if err != nil {
		return r.poisonWith(err)
	}
	return nil
}

func (r *VerifiedRoot) OpenDirectory(name DirectoryName) (*VerifiedDirectory, error) {
	if r == nil {
		return nil, errVerifiedPathInvalid
	}
	entry, ok := directoryRootEntry(name)
	if !ok {
		emitVerifiedEvent(r.traceID, "open_member", "directory", "error", "verified_member_invalid", time.Now())
		return nil, errVerifiedMemberInvalid
	}
	child, err := r.openEntry(rootEntryName(entry), verifiedDirectory)
	if err != nil {
		return nil, err
	}
	return &VerifiedDirectory{child: child}, nil
}

func (r *VerifiedRoot) OpenMember(name MemberName) (*VerifiedFile, error) {
	if r == nil {
		return nil, errVerifiedPathInvalid
	}
	entry, ok := memberRootEntry(name)
	if !ok {
		emitVerifiedEvent(r.traceID, "open_member", "file", "error", "verified_member_invalid", time.Now())
		return nil, errVerifiedMemberInvalid
	}
	child, err := r.openEntry(rootEntryName(entry), verifiedRegularFile)
	if err != nil {
		return nil, err
	}
	return &VerifiedFile{child: child}, nil
}

func (r *VerifiedRoot) openEntry(name string, kind verifiedKind) (*verifiedChild, error) {
	started := time.Now()
	objectKind := "file"
	if kind == verifiedDirectory {
		objectKind = "directory"
	}
	finish := func(err error) {
		result, class := "ok", ""
		if err != nil {
			result, class = "error", verifiedErrorClass(err)
		}
		emitVerifiedEvent(r.traceID, "open_member", objectKind, result, class, started)
	}
	if err := r.begin(); err != nil {
		finish(err)
		return nil, err
	}
	defer r.end()
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	if err := r.ensureUsable(); err != nil {
		finish(err)
		return nil, err
	}
	entries, err := r.validateRootInventoryLocked()
	if err != nil {
		err = r.poisonWith(err)
		finish(err)
		return nil, err
	}
	entry := rootEntryFromName(name)
	found := false
	for _, actual := range entries {
		if actual == entry {
			found = true
			break
		}
	}
	if !found {
		finish(errVerifiedMemberInvalid)
		return nil, errVerifiedMemberInvalid
	}
	if err := r.ensureUsable(); err != nil {
		finish(err)
		return nil, err
	}
	h, err := r.ops.open(r.handle, name, kind)
	if err != nil {
		err = classifyOpenError(err)
		if errors.Is(err, errVerifiedMemberInvalid) {
			err = r.poisonWith(errVerifiedIdentityChanged)
		} else {
			err = r.poisonWith(err)
		}
		finish(err)
		return nil, err
	}
	id, err := r.ops.inspect(h, kind)
	if err == nil {
		err = r.ops.verifyBinding(r.handle, name, id, kind)
	}
	if err != nil {
		if errors.Is(err, errVerifiedCloseUnknown) {
			r.retainUnknown([]uintptr{h})
			finish(errVerifiedCloseUnknown)
			return nil, errVerifiedCloseUnknown
		}
		if closeErr := r.ops.close(h); closeErr != nil {
			r.retainUnknown([]uintptr{h})
			finish(errVerifiedCloseUnknown)
			return nil, errVerifiedCloseUnknown
		}
		err = r.poisonWith(err)
		finish(err)
		return nil, err
	}
	c := &verifiedChild{handle: h, parent: r.handle, name: name, kind: kind, identity: id, ops: r.ops, root: r}
	r.mu.Lock()
	if r.poison != nil || r.closed || r.closing || r.gated {
		err = r.poison
		if err == nil {
			err = errVerifiedPathInvalid
		}
		r.mu.Unlock()
		if closeErr := r.ops.close(h); closeErr != nil {
			r.retainUnknown([]uintptr{h})
			finish(errVerifiedCloseUnknown)
			return nil, errVerifiedCloseUnknown
		}
		finish(err)
		return nil, err
	}
	r.children = append(r.children, c)
	r.mu.Unlock()
	finish(nil)
	return c, nil
}

func (r *VerifiedRoot) ReadNames() ([]RootEntry, error) {
	started := time.Now()
	if err := r.begin(); err != nil {
		if r != nil {
			emitVerifiedEvent(r.traceID, "enumerate", "directory", "error", verifiedErrorClass(err), started)
		}
		return nil, err
	}
	defer r.end()
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	if err := r.ensureUsable(); err != nil {
		emitVerifiedEvent(r.traceID, "enumerate", "directory", "error", verifiedErrorClass(err), started)
		return nil, err
	}
	entries, err := r.validateRootInventoryLocked()
	if err != nil {
		err = r.poisonWith(err)
		emitVerifiedEvent(r.traceID, "enumerate", "directory", "error", verifiedErrorClass(err), started)
		return nil, err
	}
	emitVerifiedEvent(r.traceID, "enumerate", "directory", "ok", "", started)
	return entries, nil
}

// validateRootInventory is non-recursive and must run only while an operation
// owns an in-flight reference or before the root is published.
func (r *VerifiedRoot) validateRootInventory() ([]RootEntry, error) {
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	return r.validateRootInventoryLocked()
}

func (r *VerifiedRoot) validateRootInventoryLocked() ([]RootEntry, error) {
	if err := r.ensureUsable(); err != nil {
		return nil, err
	}
	if err := r.verifyRootIdentity(); err != nil {
		return nil, err
	}
	first, err := r.ops.enumerate(r.handle)
	if err != nil {
		return nil, errVerifiedIOFailed
	}
	allowed := make(map[string]verifiedKind, 5)
	for _, entry := range allRootEntries() {
		kind := verifiedRegularFile
		if entry == RootCaseLocksDir || entry == RootCasesDir || entry == RootLogsDir {
			kind = verifiedDirectory
		}
		allowed[rootEntryName(entry)] = kind
	}
	seen := make(map[string]struct{}, len(first))
	for _, name := range first {
		if _, ok := allowed[name]; !ok {
			return nil, errVerifiedPolicyMismatch
		}
		if _, ok := seen[name]; ok {
			return nil, errVerifiedPolicyMismatch
		}
		seen[name] = struct{}{}
	}
	ids := make(map[verifiedIdentity]string, len(first))
	opened := make([]uintptr, 0, len(first))
	closeOpened := func() error {
		for i := len(opened) - 1; i >= 0; i-- {
			if closeErr := r.ops.close(opened[i]); closeErr != nil {
				r.retainUnknown(opened[:i+1])
				return errVerifiedCloseUnknown
			}
		}
		return nil
	}
	sort.Strings(first)
	for _, name := range first {
		kind := allowed[name]
		h, openErr := r.ops.open(r.handle, name, kind)
		if openErr != nil {
			if closeErr := closeOpened(); closeErr != nil {
				return nil, closeErr
			}
			return nil, classifyOpenError(openErr)
		}
		opened = append(opened, h)
		id, inspectErr := r.ops.inspect(h, kind)
		if inspectErr == nil {
			inspectErr = r.ops.verifyBinding(r.handle, name, id, kind)
		}
		if inspectErr != nil {
			if errors.Is(inspectErr, errVerifiedCloseUnknown) {
				r.retainUnknown(opened)
				return nil, errVerifiedCloseUnknown
			}
			if closeErr := closeOpened(); closeErr != nil {
				return nil, closeErr
			}
			return nil, inspectErr
		}
		if prior, duplicate := ids[id]; duplicate && prior != name {
			if closeErr := closeOpened(); closeErr != nil {
				return nil, closeErr
			}
			return nil, errVerifiedIdentityChanged
		}
		ids[id] = name
		if closeErr := r.ops.close(h); closeErr != nil {
			r.retainUnknown([]uintptr{h})
			return nil, errVerifiedCloseUnknown
		}
		opened = opened[:len(opened)-1]
	}
	second, err := r.ops.enumerate(r.handle)
	if err != nil {
		return nil, errVerifiedIOFailed
	}
	sort.Strings(second)
	if !equalStrings(first, second) {
		return nil, errVerifiedIdentityChanged
	}
	if err = r.verifyRootIdentity(); err != nil {
		return nil, err
	}
	if err = r.ensureUsable(); err != nil {
		return nil, err
	}
	result := make([]RootEntry, 0, len(first))
	for _, name := range first {
		result = append(result, rootEntryFromName(name))
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (c *verifiedChild) VerifyIdentity() error {
	if c == nil || c.root == nil {
		return errVerifiedPathInvalid
	}
	started := time.Now()
	objectKind := "file"
	if c.kind == verifiedDirectory {
		objectKind = "directory"
	}
	if err := c.root.begin(); err != nil {
		emitVerifiedEvent(c.root.traceID, "identity", objectKind, "error", verifiedErrorClass(err), started)
		return err
	}
	defer c.root.end()
	c.root.inventoryMu.Lock()
	defer c.root.inventoryMu.Unlock()
	if err := c.root.ensureUsable(); err != nil {
		emitVerifiedEvent(c.root.traceID, "identity", objectKind, "error", verifiedErrorClass(err), started)
		return err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		emitVerifiedEvent(c.root.traceID, "identity", objectKind, "error", "verified_path_invalid", started)
		return errVerifiedPathInvalid
	}
	actual, err := c.ops.inspect(c.handle, c.kind)
	if err == nil && actual != c.identity {
		err = errVerifiedIdentityChanged
	}
	if err == nil {
		err = c.ops.verifyBinding(c.parent, c.name, c.identity, c.kind)
	}
	if err != nil {
		err = c.root.poisonWith(err)
		emitVerifiedEvent(c.root.traceID, "identity", objectKind, "error", verifiedErrorClass(err), started)
		return err
	}
	emitVerifiedEvent(c.root.traceID, "identity", objectKind, "ok", "", started)
	return nil
}

func (d *VerifiedDirectory) VerifyIdentity() error {
	if d == nil {
		return errVerifiedPathInvalid
	}
	return d.child.VerifyIdentity()
}

func (f *VerifiedFile) VerifyIdentity() error {
	if f == nil {
		return errVerifiedPathInvalid
	}
	return f.child.VerifyIdentity()
}

func (d *VerifiedDirectory) Close() error {
	if d == nil {
		return nil
	}
	return d.child.Close()
}

func (f *VerifiedFile) Close() error {
	if f == nil {
		return nil
	}
	return f.child.Close()
}

func (c *verifiedChild) Close() error {
	if c == nil || c.root == nil {
		return nil
	}
	return c.root.closeChild(c)
}

func (r *VerifiedRoot) closeChild(c *verifiedChild) error {
	started := time.Now()
	r.mu.Lock()
	for r.closeInFlight {
		r.cond.Wait()
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "member", "ok", "", started)
		return nil
	}
	if r.closeUnknown != nil {
		err := r.closeUnknown
		r.mu.Unlock()
		return err
	}
	if r.closing {
		r.mu.Unlock()
		return errVerifiedTimeout
	}
	r.gated = true
	r.closeInFlight = true
	deadline := time.Now().Add(500 * time.Millisecond)
	if !r.waitForInflightLocked(deadline) {
		r.poison = errVerifiedTimeout
		r.closeInFlight = false
		r.cond.Broadcast()
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "member", "error", "verified_timeout", started)
		return errVerifiedTimeout
	}
	if r.closeUnknown != nil {
		err := r.closeUnknown
		r.closeInFlight = false
		r.cond.Broadcast()
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "member", "error", "verified_close_unknown", started)
		return err
	}
	r.mu.Unlock()
	if err := r.ops.close(c.handle); err != nil {
		r.mu.Lock()
		r.closeUnknown = errVerifiedCloseUnknown
		r.poison = errVerifiedCloseUnknown
		r.closeInFlight = false
		r.cond.Broadcast()
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "member", "error", "verified_close_unknown", started)
		return errVerifiedCloseUnknown
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	r.mu.Lock()
	r.gated = false
	r.closeInFlight = false
	r.cond.Broadcast()
	r.mu.Unlock()
	emitVerifiedEvent(r.traceID, "close", "member", "ok", "", started)
	return nil
}

func (r *VerifiedRoot) Close() error {
	if r == nil {
		return nil
	}
	started := time.Now()
	r.mu.Lock()
	for r.closeInFlight {
		r.cond.Wait()
	}
	if r.closed {
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "root", "ok", "", started)
		return nil
	}
	if r.closeUnknown != nil {
		err := r.closeUnknown
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "root", "error", "verified_close_unknown", started)
		return err
	}
	r.closing = true
	r.gated = true
	r.closeInFlight = true
	deadline := time.Now().Add(500 * time.Millisecond)
	if !r.waitForInflightLocked(deadline) {
		r.poison = errVerifiedTimeout
		r.closeInFlight = false
		r.cond.Broadcast()
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "root", "error", "verified_timeout", started)
		return errVerifiedTimeout
	}
	if r.closeUnknown != nil {
		err := r.closeUnknown
		r.closeInFlight = false
		r.cond.Broadcast()
		r.mu.Unlock()
		emitVerifiedEvent(r.traceID, "close", "root", "error", "verified_close_unknown", started)
		return err
	}
	children := append([]*verifiedChild(nil), r.children...)
	r.mu.Unlock()
	for i := len(children) - 1; i >= 0; i-- {
		c := children[i]
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			continue
		}
		if err := r.ops.close(c.handle); err != nil {
			return r.failCloseUnknown(started)
		}
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}
	if err := r.ops.close(r.handle); err != nil {
		return r.failCloseUnknown(started)
	}
	r.mu.Lock()
	r.closed = true
	r.closing = false
	r.closeInFlight = false
	r.cond.Broadcast()
	r.mu.Unlock()
	emitVerifiedEvent(r.traceID, "close", "root", "ok", "", started)
	return nil
}

func (r *VerifiedRoot) failCloseUnknown(started time.Time) error {
	r.mu.Lock()
	r.closeUnknown = errVerifiedCloseUnknown
	r.poison = errVerifiedCloseUnknown
	r.closing = false
	r.closeInFlight = false
	r.cond.Broadcast()
	r.mu.Unlock()
	emitVerifiedEvent(r.traceID, "close", "root", "error", "verified_close_unknown", started)
	return errVerifiedCloseUnknown
}

func allRootEntries() []RootEntry {
	return []RootEntry{RootCatalogBootstrapLock, RootCatalogLog, RootCaseLocksDir, RootCasesDir, RootLogsDir}
}

func rootEntryName(entry RootEntry) string {
	switch entry {
	case RootCatalogBootstrapLock:
		return "catalog.bootstrap.lock"
	case RootCatalogLog:
		return "catalog.log"
	case RootCaseLocksDir:
		return "case-locks"
	case RootCasesDir:
		return "cases"
	case RootLogsDir:
		return "logs"
	default:
		return ""
	}
}

func rootEntryFromName(name string) RootEntry {
	for _, entry := range allRootEntries() {
		if rootEntryName(entry) == name {
			return entry
		}
	}
	return 0
}

func directoryRootEntry(name DirectoryName) (RootEntry, bool) {
	switch name {
	case CaseLocksDir:
		return RootCaseLocksDir, true
	case CasesDir:
		return RootCasesDir, true
	case LogsDir:
		return RootLogsDir, true
	default:
		return 0, false
	}
}

func memberRootEntry(name MemberName) (RootEntry, bool) {
	switch name {
	case CatalogBootstrapLock:
		return RootCatalogBootstrapLock, true
	case CatalogLog:
		return RootCatalogLog, true
	default:
		return 0, false
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

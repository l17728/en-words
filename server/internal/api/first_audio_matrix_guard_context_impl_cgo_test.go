//go:build cgo

package api

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

var (
	errFirstAudioGuardContextInvalid    = errors.New("invalid first-audio guard context")
	errFirstAudioGuardContextStale      = errors.New("stale first-audio guard context version")
	errFirstAudioGuardContextTransition = errors.New("invalid first-audio guard context transition")
)

type firstAudioGuardContextPhase string

const (
	firstAudioGuardDriverProbe  firstAudioGuardContextPhase = "DRIVER_PROBE"
	firstAudioGuardStartupProbe firstAudioGuardContextPhase = "STARTUP_PROBE"
	firstAudioGuardPreflight    firstAudioGuardContextPhase = "PREFLIGHT"
	firstAudioGuardJourney      firstAudioGuardContextPhase = "JOURNEY"
	firstAudioGuardResetApply   firstAudioGuardContextPhase = "RESET_APPLY"
	firstAudioGuardTerminal     firstAudioGuardContextPhase = "TERMINAL"
)

type firstAudioGuardContextSpec struct {
	runID                 string
	currentLessonID       int64
	priorLessonID         int64
	nextLessonID          int64
	generation            uint64
	phase                 firstAudioGuardContextPhase
	version               uint64
	oldRuntimeKey         string
	newRuntimeKey         string
	connectionIDs         []string
	probeContracts        []firstAudioGuardProbeContract
	rowContracts          []firstAudioGuardRowContract
	requirePhysicalProbes bool
}

type firstAudioGuardProbeContract struct {
	connectionID string
	probeID      string
	tokenDigest  [sha256.Size]byte
	runID        string
	lessonID     int64
	generation   uint64
	version      uint64
}

type firstAudioGuardRowContract struct {
	table     string
	operation string
	identity  string
	oldRow    []byte
	newRow    []byte
	traceID   string
}

type firstAudioGuardContext struct {
	runID                 string
	currentLessonID       int64
	priorLessonID         int64
	nextLessonID          int64
	generation            uint64
	phase                 firstAudioGuardContextPhase
	version               uint64
	oldRuntimeKey         string
	newRuntimeKey         string
	connectionIDs         []string
	probes                map[string]firstAudioGuardProbeContract
	rows                  map[string]firstAudioGuardRowContract
	requirePhysicalProbes bool
}

func newFirstAudioGuardContext(spec firstAudioGuardContextSpec) (*firstAudioGuardContext, error) {
	if !firstAudioRunIDPattern.MatchString(spec.runID) || (spec.version == 0 && spec.phase != firstAudioGuardDriverProbe) || spec.currentLessonID <= 0 || spec.generation == 0 {
		return nil, errFirstAudioGuardContextInvalid
	}
	if spec.requirePhysicalProbes && spec.phase != firstAudioGuardPreflight {
		return nil, errFirstAudioGuardContextInvalid
	}

	ctx := &firstAudioGuardContext{
		runID: spec.runID, currentLessonID: spec.currentLessonID, priorLessonID: spec.priorLessonID,
		nextLessonID: spec.nextLessonID, generation: spec.generation, phase: spec.phase,
		version: spec.version, oldRuntimeKey: spec.oldRuntimeKey, newRuntimeKey: spec.newRuntimeKey,
		connectionIDs:         append([]string(nil), spec.connectionIDs...),
		probes:                make(map[string]firstAudioGuardProbeContract, len(spec.probeContracts)),
		rows:                  make(map[string]firstAudioGuardRowContract, len(spec.rowContracts)),
		requirePhysicalProbes: spec.requirePhysicalProbes,
	}
	probeIDs := make(map[string]struct{}, len(spec.probeContracts))
	for _, probe := range spec.probeContracts {
		if probe.connectionID == "" || !firstAudioIDPattern.MatchString(probe.connectionID) {
			return nil, errFirstAudioGuardContextInvalid
		}
		if _, exists := ctx.probes[probe.connectionID]; exists {
			return nil, fmt.Errorf("%w: duplicate probe connection", errFirstAudioGuardContextInvalid)
		}
		if probe.probeID == "" || !firstAudioIDPattern.MatchString(probe.probeID) || probe.tokenDigest == ([sha256.Size]byte{}) {
			return nil, errFirstAudioGuardContextInvalid
		}
		if _, exists := probeIDs[probe.probeID]; exists {
			return nil, fmt.Errorf("%w: duplicate probe ID", errFirstAudioGuardContextInvalid)
		}
		probeIDs[probe.probeID] = struct{}{}
		ctx.probes[probe.connectionID] = probe
	}
	for _, row := range spec.rowContracts {
		if row.table == "" || row.operation == "" || row.identity == "" {
			return nil, errFirstAudioGuardContextInvalid
		}
		key := firstAudioGuardRowKey(row.table, row.operation, row.identity)
		if _, exists := ctx.rows[key]; exists {
			return nil, fmt.Errorf("%w: duplicate row contract", errFirstAudioGuardContextInvalid)
		}
		row.oldRow = append([]byte(nil), row.oldRow...)
		row.newRow = append([]byte(nil), row.newRow...)
		ctx.rows[key] = row
	}
	if err := ctx.validateShape(); err != nil {
		return nil, err
	}
	return ctx, nil
}

func firstAudioGuardRowKey(table, operation, identity string) string {
	return table + "\x00" + operation + "\x00" + identity
}

func (c *firstAudioGuardContext) validateShape() error {
	noConnections := len(c.connectionIDs) == 0 && len(c.probes) == 0
	noRows := len(c.rows) == 0
	idsClear := c.priorLessonID == 0 && c.nextLessonID == 0
	keysClear := c.oldRuntimeKey == "" && c.newRuntimeKey == ""

	switch c.phase {
	case firstAudioGuardDriverProbe:
		if c.version != 0 || c.generation != 1 || !idsClear || !keysClear || !noConnections || !noRows {
			return errFirstAudioGuardContextInvalid
		}
	case firstAudioGuardStartupProbe:
		if c.generation != 1 || !idsClear || !keysClear || !noConnections || !noRows {
			return errFirstAudioGuardContextInvalid
		}
	case firstAudioGuardPreflight:
		if !idsClear || !keysClear || len(c.connectionIDs) == 0 || len(c.probes) != len(c.connectionIDs) || len(c.rows) != len(c.connectionIDs) {
			return errFirstAudioGuardContextInvalid
		}
		registered := make(map[string]struct{}, len(c.connectionIDs))
		for _, connectionID := range c.connectionIDs {
			if !firstAudioIDPattern.MatchString(connectionID) {
				return errFirstAudioGuardContextInvalid
			}
			if _, duplicate := registered[connectionID]; duplicate {
				return fmt.Errorf("%w: duplicate hooked connection", errFirstAudioGuardContextInvalid)
			}
			registered[connectionID] = struct{}{}
			probe, ok := c.probes[connectionID]
			if !ok || probe.runID != c.runID || probe.lessonID != c.currentLessonID || probe.generation != c.generation || probe.version != c.version {
				return fmt.Errorf("%w: preflight probe binding mismatch", errFirstAudioGuardContextInvalid)
			}
			row, ok := c.rows[firstAudioGuardRowKey("lessons", "UPDATE", fmt.Sprintf("lesson:%d/conn:%s", c.currentLessonID, connectionID))]
			if !ok || len(row.oldRow) == 0 || !bytes.Equal(row.oldRow, row.newRow) || row.traceID != "" {
				return fmt.Errorf("%w: invalid preflight lesson probe row", errFirstAudioGuardContextInvalid)
			}
		}
	case firstAudioGuardJourney:
		if !idsClear || !noConnections || c.oldRuntimeKey != firstAudioSnapshotKey(c.currentLessonID) || c.newRuntimeKey != firstAudioSnapshotKey(c.currentLessonID) {
			return errFirstAudioGuardContextInvalid
		}
		runtimeCount := 0
		for _, row := range c.rows {
			switch {
			case row.table == "runtime_records" && row.operation == "UPSERT" && row.identity == "snapshot:"+strconv.FormatInt(c.currentLessonID, 10):
				runtimeCount++
				if len(row.newRow) == 0 || row.traceID != "" {
					return errFirstAudioGuardContextInvalid
				}
			case row.table == "audit_logs" && row.operation == "INSERT" && strings.HasPrefix(row.identity, "audit:"):
				auditID, err := strconv.ParseInt(strings.TrimPrefix(row.identity, "audit:"), 10, 64)
				if err != nil || auditID <= 0 || row.identity != "audit:"+strconv.FormatInt(auditID, 10) || len(row.oldRow) != 0 || len(row.newRow) == 0 || row.traceID != "lesson-"+strconv.FormatInt(c.currentLessonID, 10) {
					return errFirstAudioGuardContextInvalid
				}
			default:
				return fmt.Errorf("%w: unknown journey row contract", errFirstAudioGuardContextInvalid)
			}
		}
		if runtimeCount != 1 {
			return errFirstAudioGuardContextInvalid
		}
	case firstAudioGuardResetApply:
		if c.priorLessonID != c.currentLessonID || c.nextLessonID <= 0 || c.nextLessonID == c.currentLessonID || !noConnections || c.oldRuntimeKey != firstAudioSnapshotKey(c.currentLessonID) || c.newRuntimeKey != firstAudioSnapshotKey(c.nextLessonID) || len(c.rows) != 2 {
			return errFirstAudioGuardContextInvalid
		}
		expected := []struct {
			table, operation, identity string
			oldRequired, newRequired   bool
		}{
			{"lessons", "UPDATE", fmt.Sprintf("lesson:%d-to-%d", c.currentLessonID, c.nextLessonID), true, true},
			{"runtime_records", "DELETE", "snapshot:" + strconv.FormatInt(c.currentLessonID, 10), true, false},
		}
		for _, want := range expected {
			row, ok := c.rows[firstAudioGuardRowKey(want.table, want.operation, want.identity)]
			if !ok || (len(row.oldRow) > 0) != want.oldRequired || (len(row.newRow) > 0) != want.newRequired || row.traceID != "" {
				return errFirstAudioGuardContextInvalid
			}
		}
	case firstAudioGuardTerminal:
		if !idsClear || !keysClear || !noConnections || !noRows {
			return errFirstAudioGuardContextInvalid
		}
	default:
		return errFirstAudioGuardContextInvalid
	}
	return nil
}

func firstAudioSnapshotKey(lessonID int64) string {
	return "snapshot/" + strconv.FormatInt(lessonID, 10)
}

func (c *firstAudioGuardContext) clone() (*firstAudioGuardContext, error) {
	if c == nil {
		return nil, errFirstAudioGuardContextInvalid
	}
	spec := firstAudioGuardContextSpec{
		runID: c.runID, currentLessonID: c.currentLessonID, priorLessonID: c.priorLessonID,
		nextLessonID: c.nextLessonID, generation: c.generation, phase: c.phase, version: c.version,
		oldRuntimeKey: c.oldRuntimeKey, newRuntimeKey: c.newRuntimeKey,
		connectionIDs:         append([]string(nil), c.connectionIDs...),
		requirePhysicalProbes: c.requirePhysicalProbes,
	}
	for _, probe := range c.probes {
		spec.probeContracts = append(spec.probeContracts, probe)
	}
	for _, row := range c.rows {
		spec.rowContracts = append(spec.rowContracts, row)
	}
	return newFirstAudioGuardContext(spec)
}

func (c *firstAudioGuardContext) Version() uint64 { return c.version }

func (c *firstAudioGuardContext) Phase() firstAudioGuardContextPhase { return c.phase }

func (c *firstAudioGuardContext) RowContracts() []firstAudioGuardRowContract {
	if c == nil {
		return nil
	}
	out := make([]firstAudioGuardRowContract, 0, len(c.rows))
	for _, row := range c.rows {
		row.oldRow = append([]byte(nil), row.oldRow...)
		row.newRow = append([]byte(nil), row.newRow...)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		return firstAudioGuardRowKey(out[i].table, out[i].operation, out[i].identity) < firstAudioGuardRowKey(out[j].table, out[j].operation, out[j].identity)
	})
	return out
}

func (c *firstAudioGuardContext) ProbeCount() int {
	if c == nil {
		return 0
	}
	return len(c.probes)
}

type firstAudioGuardContextPublisher struct {
	current           atomic.Pointer[firstAudioGuardContext]
	callbackTerminal  atomic.Bool
	beforePublishCAS  func()
	beforeTerminalCAS func()
}

const firstAudioGuardContextCASLimit = 128

func (p *firstAudioGuardContextPublisher) Publish(candidate *firstAudioGuardContext) error {
	return p.publish(candidate, false)
}

func (p *firstAudioGuardContextPublisher) publishDriverStartup(candidate *firstAudioGuardContext, receipt *firstAudioDriverProbeReceipt) error {
	if p == nil || candidate == nil {
		return errFirstAudioGuardContextInvalid
	}
	active := p.Load()
	if active == nil || active.phase != firstAudioGuardDriverProbe || candidate.phase != firstAudioGuardStartupProbe || receipt == nil || !receipt.matchesAndConsume(active, candidate) {
		return errFirstAudioGuardContextTransition
	}
	err := receipt.registry.WithLiveRegistration(receipt.connectionID, receipt.rawConnection, func() error {
		if !receipt.consumed.CompareAndSwap(false, true) {
			return errFirstAudioGuardContextTransition
		}
		return p.publish(candidate, true)
	})
	if errors.Is(err, errFirstAudioSQLiteRegistrationNotLive) {
		return errFirstAudioGuardContextTransition
	}
	return err
}

func (p *firstAudioGuardContextPublisher) publish(candidate *firstAudioGuardContext, driverStartupReceipt bool) error {
	if p == nil || candidate == nil {
		return errFirstAudioGuardContextInvalid
	}
	detached, err := candidate.clone()
	if err != nil {
		return err
	}
	for attempt := 0; attempt < firstAudioGuardContextCASLimit; attempt++ {
		if p.callbackTerminal.Load() {
			return errFirstAudioGuardContextTransition
		}
		active := p.current.Load()
		if active != nil && detached.version <= active.version {
			return errFirstAudioGuardContextStale
		}
		if !firstAudioGuardContextTransitionAllowed(active, detached) {
			return errFirstAudioGuardContextTransition
		}
		if active != nil && active.phase == firstAudioGuardDriverProbe && detached.phase == firstAudioGuardStartupProbe && !driverStartupReceipt {
			return errFirstAudioGuardContextTransition
		}
		if p.beforePublishCAS != nil {
			p.beforePublishCAS()
		}
		if p.current.CompareAndSwap(active, detached) {
			if p.callbackTerminal.Load() {
				_ = p.terminalizeFromCallback()
				return errFirstAudioGuardContextTransition
			}
			return nil
		}
		runtime.Gosched()
	}
	return fmt.Errorf("%w: publisher CAS contention", errFirstAudioGuardContextStale)
}

// terminalizeFromCallback arbitrates SQLite callback rejection against phase
// publication using the same atomic context pointer, without lifecycle locks.
func (p *firstAudioGuardContextPublisher) terminalizeFromCallback() error {
	if p == nil {
		return errFirstAudioGuardContextInvalid
	}
	p.callbackTerminal.Store(true)
	for attempt := 0; attempt < firstAudioGuardContextCASLimit; attempt++ {
		active := p.current.Load()
		if active == nil || active.phase == firstAudioGuardTerminal {
			return nil
		}
		terminal, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
			runID: active.runID, currentLessonID: active.currentLessonID,
			generation: active.generation, phase: firstAudioGuardTerminal, version: active.version + 1,
		})
		if err != nil {
			return err
		}
		if p.beforeTerminalCAS != nil {
			p.beforeTerminalCAS()
		}
		if p.current.CompareAndSwap(active, terminal) {
			return nil
		}
		runtime.Gosched()
	}
	return fmt.Errorf("%w: callback terminal CAS contention", errFirstAudioGuardContextStale)
}

func firstAudioGuardContextTransitionAllowed(active, candidate *firstAudioGuardContext) bool {
	if candidate == nil {
		return false
	}
	if active == nil {
		return candidate.phase == firstAudioGuardDriverProbe && candidate.version == 0
	}
	if active.phase == firstAudioGuardTerminal || active.runID != candidate.runID || candidate.version <= active.version {
		return false
	}
	if candidate.phase == firstAudioGuardTerminal {
		return active.currentLessonID == candidate.currentLessonID && active.generation == candidate.generation
	}
	switch active.phase {
	case firstAudioGuardDriverProbe:
		return candidate.phase == firstAudioGuardStartupProbe && active.currentLessonID == candidate.currentLessonID && active.generation == candidate.generation
	case firstAudioGuardStartupProbe:
		return candidate.phase == firstAudioGuardPreflight && active.currentLessonID == candidate.currentLessonID && active.generation == candidate.generation
	case firstAudioGuardPreflight:
		return candidate.phase == firstAudioGuardJourney && active.currentLessonID == candidate.currentLessonID && active.generation == candidate.generation
	case firstAudioGuardJourney:
		sameIdentity := active.currentLessonID == candidate.currentLessonID && active.generation == candidate.generation
		return candidate.phase == firstAudioGuardJourney && sameIdentity || candidate.phase == firstAudioGuardResetApply && sameIdentity && candidate.priorLessonID == active.currentLessonID
	case firstAudioGuardResetApply:
		if candidate.phase != firstAudioGuardJourney || candidate.currentLessonID != active.nextLessonID || candidate.generation != active.generation+1 {
			return false
		}
		initialSnapshot, ok := candidate.rows[firstAudioGuardRowKey(
			"runtime_records", "UPSERT", "snapshot:"+strconv.FormatInt(candidate.currentLessonID, 10),
		)]
		return ok && len(initialSnapshot.oldRow) == 0 && len(initialSnapshot.newRow) > 0 && initialSnapshot.traceID == ""
	default:
		return false
	}
}

func (p *firstAudioGuardContextPublisher) Load() *firstAudioGuardContext {
	if p == nil {
		return nil
	}
	return p.current.Load()
}

//go:build cgo

package api

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFirstAudioGuardContextValidPhaseShapes(t *testing.T) {
	cases := []struct {
		name string
		spec firstAudioGuardContextSpec
	}{
		{"startup", firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1}},
		{"preflight", firstAudioValidPreflightSpec(2)},
		{"journey", firstAudioValidJourneySpec(2, 101, 1, 1)},
		{"reset", firstAudioValidResetSpec(2)},
		{"terminal", firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardTerminal, version: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := newFirstAudioGuardContext(tc.spec)
			if err != nil {
				t.Fatalf("valid context rejected: %v", err)
			}
			if ctx.Phase() != tc.spec.phase || ctx.Version() != tc.spec.version {
				t.Fatalf("phase/version = %s/%d, want %s/%d", ctx.Phase(), ctx.Version(), tc.spec.phase, tc.spec.version)
			}
		})
	}
}

func TestFirstAudioGuardContextRejectsMalformedShapes(t *testing.T) {
	validStartup := firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1}
	validPreflight := firstAudioValidPreflightSpec(2)
	validJourney := firstAudioValidJourneySpec(2, 101, 1, 1)
	validReset := firstAudioValidResetSpec(2)
	validTerminal := firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardTerminal, version: 3}

	cases := []struct {
		name string
		spec firstAudioGuardContextSpec
	}{
		{"empty run id", func() firstAudioGuardContextSpec { s := validStartup; s.runID = ""; return s }()},
		{"version zero", func() firstAudioGuardContextSpec { s := validStartup; s.version = 0; return s }()},
		{"startup generation", func() firstAudioGuardContextSpec { s := validStartup; s.generation = 2; return s }()},
		{"startup extra id", func() firstAudioGuardContextSpec { s := validStartup; s.priorLessonID = 99; return s }()},
		{"startup extra row", func() firstAudioGuardContextSpec {
			s := validStartup
			s.rowContracts = []firstAudioGuardRowContract{{table: "users", operation: "INSERT", identity: "x"}}
			return s
		}()},
		{"preflight missing connection", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.connectionIDs = s.connectionIDs[:1]
			return s
		}()},
		{"preflight duplicate connection", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.connectionIDs = append(s.connectionIDs, s.connectionIDs[0])
			return s
		}()},
		{"preflight duplicate probe connection", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.probeContracts = append(s.probeContracts, s.probeContracts[0])
			return s
		}()},
		{"preflight duplicate probe ID", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.probeContracts[1].probeID = s.probeContracts[0].probeID
			return s
		}()},
		{"preflight digest missing", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.probeContracts[0].tokenDigest = [sha256.Size]byte{}
			return s
		}()},
		{"preflight token wrong version", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.probeContracts[0].version++
			return s
		}()},
		{"preflight row differs", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.rowContracts[0].newRow = []byte("different")
			return s
		}()},
		{"preflight extra contract", func() firstAudioGuardContextSpec {
			s := validPreflight
			s.rowContracts = append(s.rowContracts, firstAudioGuardRowContract{table: "audit_logs", operation: "INSERT", identity: "audit:1", newRow: []byte("x")})
			return s
		}()},
		{"journey missing snapshot", func() firstAudioGuardContextSpec {
			s := validJourney
			s.rowContracts = s.rowContracts[1:]
			return s
		}()},
		{"journey wrong snapshot key", func() firstAudioGuardContextSpec {
			s := validJourney
			s.oldRuntimeKey = "snapshot/102"
			return s
		}()},
		{"journey wrong audit trace", func() firstAudioGuardContextSpec {
			s := validJourney
			s.rowContracts[1].traceID = "lesson-102"
			return s
		}()},
		{"journey unknown table", func() firstAudioGuardContextSpec {
			s := validJourney
			s.rowContracts = append(s.rowContracts, firstAudioGuardRowContract{table: "users", operation: "UPDATE", identity: "user:1", oldRow: []byte("a"), newRow: []byte("b")})
			return s
		}()},
		{"reset same id", func() firstAudioGuardContextSpec {
			s := validReset
			s.nextLessonID = s.currentLessonID
			return s
		}()},
		{"reset wrong prior", func() firstAudioGuardContextSpec {
			s := validReset
			s.priorLessonID--
			return s
		}()},
		{"reset insert has old row", func() firstAudioGuardContextSpec {
			s := validReset
			s.rowContracts[2].oldRow = []byte("old")
			return s
		}()},
		{"reset extra row", func() firstAudioGuardContextSpec {
			s := validReset
			s.rowContracts = append(s.rowContracts, firstAudioGuardRowContract{table: "audit_logs", operation: "INSERT", identity: "audit:1", newRow: []byte("x")})
			return s
		}()},
		{"terminal extra key", func() firstAudioGuardContextSpec {
			s := validTerminal
			s.newRuntimeKey = "snapshot/101"
			return s
		}()},
		{"unknown phase", func() firstAudioGuardContextSpec {
			s := validStartup
			s.phase = "UNKNOWN"
			return s
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newFirstAudioGuardContext(tc.spec); !errors.Is(err, errFirstAudioGuardContextInvalid) {
				t.Fatalf("newFirstAudioGuardContext() error = %v, want invalid context", err)
			}
		})
	}
}

func TestFirstAudioGuardContextRejectsDuplicateKeys(t *testing.T) {
	spec := firstAudioValidJourneySpec(2, 101, 1, 1)
	spec.rowContracts = append(spec.rowContracts, spec.rowContracts[0])
	if _, err := newFirstAudioGuardContext(spec); !errors.Is(err, errFirstAudioGuardContextInvalid) {
		t.Fatalf("duplicate contract error = %v, want invalid context", err)
	}
}

func TestFirstAudioGuardContextDeepCopiesInputsAndAccessors(t *testing.T) {
	spec := firstAudioValidJourneySpec(2, 101, 1, 1)
	ctx, err := newFirstAudioGuardContext(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.rowContracts[0].newRow[0] = 'X'
	spec.rowContracts[1].newRow[0] = 'Y'
	rows := ctx.RowContracts()
	snapshotRow, auditRow := firstAudioContextRowsByTable(rows)
	if !bytes.Equal(snapshotRow.newRow, []byte("snapshot-1")) || !bytes.Equal(auditRow.newRow, []byte("audit-1")) {
		t.Fatalf("published context changed after caller mutation: %#v", rows)
	}
	snapshotRow.newRow[0] = 'Z'
	storedSnapshot, _ := firstAudioContextRowsByTable(ctx.RowContracts())
	if got := storedSnapshot.newRow; !bytes.Equal(got, []byte("snapshot-1")) {
		t.Fatalf("accessor mutation changed stored row: %q", got)
	}

	preflight := firstAudioValidPreflightSpec(2)
	preflightCtx, err := newFirstAudioGuardContext(preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.probeContracts[0].tokenDigest[0] = 'X'
	if preflightCtx.probes["conn-a"].tokenDigest[0] == 'X' {
		t.Fatalf("published token digest changed after caller mutation: %x", preflightCtx.probes["conn-a"].tokenDigest[0])
	}
	clone, err := preflightCtx.clone()
	if err != nil {
		t.Fatal(err)
	}
	probeCopy := clone.probes["conn-a"]
	probeCopy.tokenDigest[0] = 'Y'
	if got := preflightCtx.probes["conn-a"].tokenDigest[0]; got == 'Y' {
		t.Fatalf("cloned context mutation changed stored token digest: %x", got)
	}
}

func TestFirstAudioGuardContextPublisherMonotonicAndNilSafe(t *testing.T) {
	var publisher firstAudioGuardContextPublisher
	if err := publisher.Publish(nil); !errors.Is(err, errFirstAudioGuardContextInvalid) {
		t.Fatalf("nil publish error = %v, want invalid context", err)
	}
	if publisher.Load() != nil {
		t.Fatal("nil publish changed empty active pointer")
	}
	first := firstAudioPublishJourney(t, &publisher, 3)
	active := publisher.Load()
	if active == nil || active.Version() != 3 {
		t.Fatalf("active context = %#v, want version 3", active)
	}
	if err := publisher.Publish(nil); !errors.Is(err, errFirstAudioGuardContextInvalid) || publisher.Load() != active {
		t.Fatalf("nil publish changed active context or returned wrong error: err=%v", err)
	}
	first.rows[firstAudioGuardRowKey("runtime_records", "UPSERT", "snapshot:101")].newRow[0] = 'X'
	activeSnapshot, _ := firstAudioContextRowsByTable(active.RowContracts())
	if got := activeSnapshot.newRow; !bytes.Equal(got, []byte("snapshot-3")) {
		t.Fatalf("active context changed after candidate mutation: %q", got)
	}
	if err := publisher.Publish(first); !errors.Is(err, errFirstAudioGuardContextStale) {
		t.Fatalf("equal-version publish error = %v, want stale", err)
	}
	if publisher.Load() != active {
		t.Fatal("nil or stale publish replaced active pointer")
	}
	stale, _ := newFirstAudioGuardContext(firstAudioValidJourneySpec(2, 101, 1, 2))
	if err := publisher.Publish(stale); !errors.Is(err, errFirstAudioGuardContextStale) {
		t.Fatalf("older-version publish error = %v, want stale", err)
	}
	if publisher.Load() != active {
		t.Fatal("stale publish replaced active pointer")
	}
}

func TestFirstAudioGuardContextPublisherConcurrentVersionsDoNotRegress(t *testing.T) {
	var publisher firstAudioGuardContextPublisher
	firstAudioPublishJourney(t, &publisher, 3)
	candidates := make([]*firstAudioGuardContext, 2)
	for i, version := range []uint64{4, 5} {
		candidate, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(version, 101, 1, int(version)))
		if err != nil {
			t.Fatal(err)
		}
		candidates[i] = candidate
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- publisher.Publish(candidate)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded := false
	for err := range errs {
		if err == nil {
			succeeded = true
			continue
		}
		if !errors.Is(err, errFirstAudioGuardContextStale) {
			t.Fatalf("concurrent publication returned unexpected error: %v", err)
		}
	}
	if !succeeded {
		t.Fatal("all concurrent context publications failed")
	}
	if got := publisher.Load().Version(); got != 5 {
		t.Fatalf("concurrent publication regressed or dropped newest version: got %d want 5", got)
	}
}

func TestFirstAudioGuardContextPublisherReadersSeeWholeJourneySnapshots(t *testing.T) {
	var publisher firstAudioGuardContextPublisher
	firstAudioPublishJourney(t, &publisher, 3)

	done := make(chan struct{})
	var failures atomic.Int64
	var readers sync.WaitGroup
	ready := make(chan struct{}, 8)
	start := make(chan struct{})
	type observation struct {
		readerID int
		version  uint64
	}
	observed := make(chan observation, 16384)
	const readerCount = 8
	initialRelease := make(chan struct{})
	for readerID := range readerCount {
		readerID := readerID
		readers.Add(1)
		go func() {
			defer readers.Done()
			ready <- struct{}{}
			<-start
			initial := publisher.Load()
			if initial == nil || initial.Version() != 3 {
				failures.Add(1)
				observed <- observation{readerID: readerID}
			} else {
				observed <- observation{readerID: readerID, version: initial.Version()}
			}
			<-initialRelease
			for {
				ctx := publisher.Load()
				if ctx == nil {
					failures.Add(1)
					select {
					case observed <- observation{readerID: readerID}:
					case <-done:
						return
					}
				} else {
					version := ctx.Version()
					rows := ctx.RowContracts()
					if version == 0 || len(rows) != 2 {
						failures.Add(1)
					} else {
						wantSnapshot := []byte(fmt.Sprintf("snapshot-%d", version))
						wantAudit := []byte(fmt.Sprintf("audit-%d", version))
						snapshotRow, auditRow := firstAudioContextRowsByTable(rows)
						if !bytes.Equal(snapshotRow.newRow, wantSnapshot) || auditRow.identity != fmt.Sprintf("audit:%d", version) || !bytes.Equal(auditRow.newRow, wantAudit) {
							failures.Add(1)
						}
					}
					select {
					case observed <- observation{readerID: readerID, version: version}:
					case <-done:
						return
					}
				}
				select {
				case <-done:
					return
				default:
				}
			}
		}()
	}
	for range readerCount {
		<-ready
	}
	close(start)
	seenInitial := make(map[int]struct{}, readerCount)
	for len(seenInitial) < readerCount {
		observation := <-observed
		if observation.version != 3 {
			t.Fatalf("reader %d initial version = %d, want 3", observation.readerID, observation.version)
		}
		seenInitial[observation.readerID] = struct{}{}
	}
	close(initialRelease)
	for version := uint64(4); version <= 252; version++ {
		candidate, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(version, 101, 1, int(version)))
		if err != nil {
			t.Fatal(err)
		}
		if err := publisher.Publish(candidate); err != nil {
			t.Fatalf("publish version %d: %v", version, err)
		}
		for {
			if got := <-observed; got.version == version {
				break
			}
		}
	}
	close(done)
	readers.Wait()
	if got := failures.Load(); got != 0 {
		t.Fatalf("readers observed %d partial or inconsistent snapshots", got)
	}
}

func firstAudioPublishJourney(t *testing.T, publisher *firstAudioGuardContextPublisher, version uint64) *firstAudioGuardContext {
	t.Helper()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := newFirstAudioGuardContext(firstAudioValidPreflightSpec(2))
	if err != nil {
		t.Fatal(err)
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(version, 101, 1, int(version)))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*firstAudioGuardContext{startup, preflight, journey} {
		if err := publisher.Publish(candidate); err != nil {
			t.Fatalf("publish phase %s version %d: %v", candidate.Phase(), candidate.Version(), err)
		}
	}
	return journey
}

func firstAudioValidPreflightSpec(version uint64) firstAudioGuardContextSpec {
	connections := []string{"conn-a", "conn-b"}
	probes := make([]firstAudioGuardProbeContract, 0, len(connections))
	rows := make([]firstAudioGuardRowContract, 0, len(connections))
	for _, connectionID := range connections {
		token := bytes.Repeat([]byte{0xA5}, 32)
		digest := sha256.Sum256(token)
		for i := range token {
			token[i] = 0
		}
		probes = append(probes, firstAudioGuardProbeContract{connectionID: connectionID, probeID: "probe-" + connectionID, tokenDigest: digest, runID: "run-0001", lessonID: 101, generation: 1, version: version})
		rows = append(rows, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: "lesson:101/conn:" + connectionID, oldRow: []byte("lesson-row"), newRow: []byte("lesson-row")})
	}
	return firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardPreflight, version: version, connectionIDs: connections, probeContracts: probes, rowContracts: rows}
}

func firstAudioValidJourneySpec(version uint64, lessonID int64, generation uint64, record int) firstAudioGuardContextSpec {
	return firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: lessonID, generation: generation,
		phase: firstAudioGuardJourney, version: version,
		oldRuntimeKey: firstAudioSnapshotKey(lessonID), newRuntimeKey: firstAudioSnapshotKey(lessonID),
		rowContracts: []firstAudioGuardRowContract{
			{table: "runtime_records", operation: "UPSERT", identity: "snapshot:" + strconv.FormatInt(lessonID, 10), oldRow: []byte("old-snapshot"), newRow: []byte(fmt.Sprintf("snapshot-%d", record))},
			{table: "audit_logs", operation: "INSERT", identity: fmt.Sprintf("audit:%d", record), newRow: []byte(fmt.Sprintf("audit-%d", record)), traceID: "lesson-" + strconv.FormatInt(lessonID, 10)},
		},
	}
}

func firstAudioValidResetSpec(version uint64) firstAudioGuardContextSpec {
	return firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, priorLessonID: 101, nextLessonID: 202,
		generation: 1, phase: firstAudioGuardResetApply, version: version,
		oldRuntimeKey: firstAudioSnapshotKey(101), newRuntimeKey: firstAudioSnapshotKey(202),
		rowContracts: []firstAudioGuardRowContract{
			{table: "lessons", operation: "UPDATE", identity: "lesson:101-to-202", oldRow: []byte("old-lesson"), newRow: []byte("new-lesson")},
			{table: "runtime_records", operation: "DELETE", identity: "snapshot:101", oldRow: []byte("old-snapshot")},
			{table: "runtime_records", operation: "INSERT", identity: "snapshot:202", newRow: []byte("new-snapshot")},
		},
	}
}

func firstAudioContextRowsByTable(rows []firstAudioGuardRowContract) (snapshot, audit firstAudioGuardRowContract) {
	for _, row := range rows {
		switch row.table {
		case "runtime_records":
			snapshot = row
		case "audit_logs":
			audit = row
		}
	}
	return snapshot, audit
}

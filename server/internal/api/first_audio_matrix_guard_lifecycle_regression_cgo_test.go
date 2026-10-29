//go:build cgo

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

func TestFirstAudioPreflightPrepareStrictOwnerPublishesExactUnarmedReceipts(t *testing.T) {
	controller := newFirstAudioGuardControllerWithPolicy(true, 3)
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_owner", currentLessonID: 701, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal("create startup context:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish startup:", err)
	}
	firstAudioTestMarkStartupVerified(t, transition)
	prepare, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
	if err != nil {
		t.Fatal("create prepare context:", err)
	}
	var concurrentRawErr error
	controller.publisher.beforePublishCAS = func() {
		done := make(chan error, 1)
		go func() { done <- controller.publisher.publish(prepare, false) }()
		concurrentRawErr = <-done
	}
	if err := transition.Publish(context.Background(), prepare, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("publish prepare:", err)
	}
	controller.publisher.beforePublishCAS = nil
	if !errors.Is(concurrentRawErr, errFirstAudioGuardContextTransition) {
		t.Fatalf("concurrent raw publish during validated transition error=%v, want rejection", concurrentRawErr)
	}
	if !controller.IsActivePhase(firstAudioGuardPreflightPrepare) {
		t.Fatal("PREPARE was not published")
	}

	bindings := make([]firstAudioGuardProbeBinding, 0, 3)
	contracts := make([]firstAudioGuardProbeContract, 0, 3)
	rows := make([]firstAudioGuardRowContract, 0, 3)
	receipts := make([]*firstAudioGuardProbeReceipt, 0, 3)
	expectedConnections := make(map[string]*sqlite3.SQLiteConn, 3)
	ids := make([]string, 0, 3)
	tokens := make([][]byte, 0, 3)
	for range 3 {
		raw := firstAudioTestOpenSQLiteConnection(t)
		registration, registerErr := controller.connections.Register(raw)
		if registerErr != nil {
			t.Fatal("register physical connection:", registerErr)
		}
		expectedConnections[registration.ID] = raw
		id := registration.ID
		t.Cleanup(func() { controller.connections.Retire(raw) })
		binding := firstAudioGuardProbeBinding{runID: startup.runID, lessonID: startup.currentLessonID, generation: 1, version: 3, connection: id, probeID: "probe-" + id}
		token, challenge, issueErr := newFirstAudioPhysicalProbeChallenge(binding)
		if issueErr != nil {
			t.Fatal("issue physical challenge:", issueErr)
		}
		tokens = append(tokens, token)
		bindings = append(bindings, binding)
		contracts = append(contracts, challenge.Contract())
		rows = append(rows, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, id), oldRow: []byte("same"), newRow: []byte("same")})
		receipts = append(receipts, challenge.receipt)
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
	})
	candidate, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflight, version: 3, connectionIDs: ids, probeContracts: contracts, rowContracts: rows, requirePhysicalProbes: true})
	if err != nil {
		t.Fatal("create final preflight context:", err)
	}
	if err := transition.Publish(context.Background(), candidate, firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, expectedConnections: expectedConnections}); err != nil {
		t.Fatal("publish final PREFLIGHT:", err)
	}
	if !controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("final PREFLIGHT phase was not published")
	}
	for _, receipt := range receipts {
		if !receipt.armed.Load() || firstAudioGuardProbeReceiptState(receipt.state.Load()) != firstAudioGuardProbeIssued || receipt.active.Load() != 0 {
			t.Fatal("receipt was not armed after publication in ISSUED state")
		}
	}
	if controller.admissionOpen {
		t.Fatal("protocol admission opened before JOURNEY")
	}
}

func TestFirstAudioPreflightPrepareStrictOwnerRejectsLegacyDirectTransition(t *testing.T) {
	controller := newFirstAudioGuardControllerWithPolicy(true, 3)
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_bypass", currentLessonID: 702, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal("create startup context:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish startup:", err)
	}
	bindings := make([]firstAudioGuardProbeBinding, 0, 3)
	contracts := make([]firstAudioGuardProbeContract, 0, 3)
	rows := make([]firstAudioGuardRowContract, 0, 3)
	receipts := make([]*firstAudioGuardProbeReceipt, 0, 3)
	ids := make([]string, 0, 3)
	tokens := make([][]byte, 0, 3)
	for range 3 {
		raw := firstAudioTestOpenSQLiteConnection(t)
		registration, registerErr := controller.connections.Register(raw)
		if registerErr != nil {
			t.Fatal("register physical connection:", registerErr)
		}
		t.Cleanup(func() { controller.connections.Retire(raw) })
		binding := firstAudioGuardProbeBinding{runID: startup.runID, lessonID: startup.currentLessonID, generation: 1, version: 2, connection: registration.ID, probeID: "probe-" + registration.ID}
		token, challenge, issueErr := newFirstAudioPhysicalProbeChallenge(binding)
		if issueErr != nil {
			t.Fatal("issue physical challenge:", issueErr)
		}
		tokens = append(tokens, token)
		bindings = append(bindings, binding)
		contracts = append(contracts, challenge.Contract())
		rows = append(rows, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, registration.ID), oldRow: []byte("same"), newRow: []byte("same")})
		receipts = append(receipts, challenge.receipt)
		ids = append(ids, registration.ID)
	}
	t.Cleanup(func() {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
	})
	direct, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflight, version: 2, connectionIDs: ids, probeContracts: contracts, rowContracts: rows, requirePhysicalProbes: true})
	if err != nil {
		t.Fatal("create direct candidate:", err)
	}
	if err := transition.Publish(context.Background(), direct, firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts}); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("strict owner direct transition error=%v", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("strict owner bypass did not terminalize before PREFLIGHT")
	}
	for _, receipt := range receipts {
		if receipt.armed.Load() {
			t.Fatal("bypass armed a receipt")
		}
	}
}

func TestFirstAudioPreflightPrepareStrictPublisherRejectsTransitionWithoutLease(t *testing.T) {
	controller := newFirstAudioGuardControllerWithPolicy(true, 3)
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_no_lease", currentLessonID: 703, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal("create startup context:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish startup:", err)
	}
	prepare, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
	if err != nil {
		t.Fatal("create prepare context:", err)
	}
	if err := controller.publisher.Publish(prepare); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("publisher without transition lease error=%v, want transition rejection", err)
	}
	controller.mu.Lock()
	guardedErr := controller.publisher.publishGuardedTransition(&firstAudioValidatedTransition{
		owner: controller, lease: &firstAudioGuardTransitionLease{owner: controller}, candidate: prepare,
	})
	controller.mu.Unlock()
	if !errors.Is(guardedErr, errFirstAudioGuardContextTransition) {
		t.Fatalf("forged guarded transition error=%v, want transition rejection", guardedErr)
	}
	controller.mu.Lock()
	activeLeaseErr := controller.publisher.publishGuardedTransition(&firstAudioValidatedTransition{
		owner: controller, lease: transition, candidate: prepare, capability: controller.publisher.transitionCapability,
	})
	controller.mu.Unlock()
	if !errors.Is(activeLeaseErr, errFirstAudioGuardContextTransition) {
		t.Fatalf("active lease without startup verification error=%v, want rejection", activeLeaseErr)
	}
	controller.mu.Lock()
	rawErr := controller.publisher.publish(prepare, false)
	controller.mu.Unlock()
	if !errors.Is(rawErr, errFirstAudioGuardContextTransition) {
		t.Fatalf("raw strict publisher error=%v, want transition rejection", rawErr)
	}
	if !controller.IsActivePhase(firstAudioGuardStartupProbe) {
		t.Fatal("direct publisher call changed active phase")
	}
}

func TestFirstAudioPreflightPrepareHelperRejectsActiveLeaseWithoutPhysicalReceipts(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_helper_receipts", 7034)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_helper_receipts", 7034, 3)
	controller.mu.Lock()
	err := controller.publisher.publishGuardedTransition(&firstAudioValidatedTransition{
		owner: controller, lease: transition, candidate: candidate, capability: controller.publisher.transitionCapability,
		evidence: firstAudioGuardTransitionEvidence{expectedConnections: evidence.expectedConnections},
	})
	controller.mu.Unlock()
	if !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("active lease without receipts error=%v, want rejection", err)
	}
	if !controller.IsActivePhase(firstAudioGuardPreflightPrepare) || controller.IsTerminal() {
		t.Fatal("missing receipts changed active phase or terminalized outside lease path")
	}
	for _, receipt := range evidence.probeReceipts {
		if receipt.armed.Load() {
			t.Fatal("helper armed a receipt without validating the complete evidence set")
		}
	}
}

func TestFirstAudioPreflightPrepareRejectsBackwardPhaseAndMutatedVersion(t *testing.T) {
	t.Run("backward_phase", func(t *testing.T) {
		controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_backward", 7031)
		startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_backward", currentLessonID: 7031, generation: 1, phase: firstAudioGuardStartupProbe, version: 3})
		if err != nil {
			t.Fatal("create backward startup context:", err)
		}
		if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextTransition) {
			t.Fatalf("backward transition error=%v, want transition rejection", err)
		}
		if !controller.IsActivePhase(firstAudioGuardPreflightPrepare) || controller.IsTerminal() {
			t.Fatal("invalid backward transition changed active PREPARE state")
		}
	})
	t.Run("wrong_generation", func(t *testing.T) {
		controller := newFirstAudioGuardControllerWithPolicy(true, 3)
		startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_wrong_gen", currentLessonID: 7032, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
		if err != nil {
			t.Fatal("create startup context:", err)
		}
		transition, err := firstAudioTestPublishInitialStartup(controller, startup)
		if err != nil {
			t.Fatal("publish startup:", err)
		}
		firstAudioTestMarkStartupVerified(t, transition)
		candidate, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
		if err != nil {
			t.Fatal("create valid candidate:", err)
		}
		candidate.generation = 2
		if err := transition.Publish(context.Background(), candidate, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextTransition) && !errors.Is(err, errFirstAudioGuardContextInvalid) {
			t.Fatalf("wrong-generation controller publish error=%v", err)
		}
		if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflightPrepare) {
			t.Fatal("wrong generation was published or owner remained live")
		}
	})
	t.Run("wrong_version", func(t *testing.T) {
		controller := newFirstAudioGuardControllerWithPolicy(true, 3)
		startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_wrong_ver", currentLessonID: 7033, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
		if err != nil {
			t.Fatal("create startup context:", err)
		}
		transition, err := firstAudioTestPublishInitialStartup(controller, startup)
		if err != nil {
			t.Fatal("publish startup:", err)
		}
		firstAudioTestMarkStartupVerified(t, transition)
		candidate, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
		if err != nil {
			t.Fatal("create valid candidate:", err)
		}
		candidate.version = 3
		if err := transition.Publish(context.Background(), candidate, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextTransition) && !errors.Is(err, errFirstAudioGuardContextInvalid) {
			t.Fatalf("wrong-version controller publish error=%v", err)
		}
		if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflightPrepare) {
			t.Fatal("wrong version was published or owner remained live")
		}
	})
}

func TestFirstAudioPreflightPrepareRejectsMissingStartupVerificationReceipt(t *testing.T) {
	controller := newFirstAudioGuardControllerWithPolicy(true, 3)
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_forged_startup", currentLessonID: 7035, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal("create startup context:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish startup:", err)
	}
	prepare, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
	if err != nil {
		t.Fatal("create PREPARE context:", err)
	}
	if err := transition.Publish(context.Background(), prepare, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("publish without controller verification receipt=%v, want rejection", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardStartupProbe) || controller.IsActivePhase(firstAudioGuardPreflightPrepare) {
		t.Fatal("missing startup verification receipt did not fail closed")
	}
}

func TestFirstAudioPreflightPrepareLegacyControllerRetainsDirectTransition(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_legacy", currentLessonID: 704, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal("create startup context:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish startup:", err)
	}
	binding := firstAudioGuardProbeBinding{runID: startup.runID, lessonID: startup.currentLessonID, generation: 1, version: 2, connection: "conn-legacy", probeID: "probe-legacy"}
	token, challenge, err := newFirstAudioGuardProbeChallenge(binding)
	if err != nil {
		t.Fatal("create legacy probe:", err)
	}
	defer zeroFirstAudioSecret(token)
	candidate, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: startup.runID, currentLessonID: startup.currentLessonID, generation: 1,
		phase: firstAudioGuardPreflight, version: 2, connectionIDs: []string{binding.connection},
		probeContracts: []firstAudioGuardProbeContract{challenge.Contract()},
		rowContracts:   []firstAudioGuardRowContract{{table: "lessons", operation: "UPDATE", identity: "lesson:704/conn:conn-legacy", oldRow: []byte("same"), newRow: []byte("same")}},
	})
	if err != nil {
		t.Fatal("create legacy preflight context:", err)
	}
	if err := transition.Publish(context.Background(), candidate, firstAudioGuardTransitionEvidence{probeBindings: []firstAudioGuardProbeBinding{binding}, probeReceipts: []*firstAudioGuardProbeReceipt{challenge.receipt}}); err != nil {
		t.Fatal("legacy direct preflight transition rejected:", err)
	}
	if !controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("legacy direct transition did not publish PREFLIGHT")
	}
}

func TestFirstAudioPreflightPrepareAuthorizerDeniesSQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		udf  bool
	}{
		{name: "read", sql: "SELECT 1"},
		{name: "write", sql: "INSERT INTO sqlite_master(type,name,tbl_name,rootpage,sql) VALUES('table','denied_prepare','denied_prepare',0,'CREATE TABLE denied_prepare(x INTEGER)')"},
		{name: "transaction", sql: "BEGIN"},
		{name: "udf", sql: "SELECT first_audio_probe(X'00')", udf: true},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runID := fmt.Sprintf("preflight_prepare_sql_%d", index)
			controller, _ := firstAudioTestStrictPreflightPrepare(t, runID, int64(710+index))
			raw := firstAudioTestOpenSQLiteConnection(t)
			if _, err := controller.connections.Register(raw); err != nil {
				t.Fatal("register raw connection:", err)
			}
			var udfCalls atomic.Uint64
			if testCase.udf {
				if err := raw.RegisterFunc("first_audio_probe", func([]byte) bool { udfCalls.Add(1); return true }, true); err != nil {
					t.Fatal("register test UDF:", err)
				}
			}
			authorizer := newFirstAudioRealSQLAuthorizer(controller, &firstAudioRealSQLProbeSessionRef{}, firstAudioDatabaseSnapshot{})
			if err := authorizer.install(raw); err != nil {
				t.Fatal("install authorizer:", err)
			}
			queryer, ok := any(raw).(driver.ExecerContext)
			if !ok {
				t.Fatal("SQLite connection lacks ExecerContext")
			}
			if _, err := queryer.ExecContext(context.Background(), testCase.sql, nil); err == nil {
				t.Fatalf("PREPARE allowed %s", testCase.name)
			}
			if !controller.IsTerminal() || authorizer.denials.Load() != 1 {
				t.Fatalf("PREPARE %s denial did not terminalize exactly once", testCase.name)
			}
			if udfCalls.Load() != 0 {
				t.Fatalf("PREPARE executed denied UDF %d times", udfCalls.Load())
			}
		})
	}
}

func firstAudioTestStrictPreflightPrepare(t *testing.T, runID string, lessonID int64) (*firstAudioGuardController, *firstAudioGuardTransitionLease) {
	t.Helper()
	controller := newFirstAudioGuardControllerWithPolicy(true, 3)
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: runID, currentLessonID: lessonID, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal("create startup context:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish startup:", err)
	}
	firstAudioTestMarkStartupVerified(t, transition)
	prepare, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: runID, currentLessonID: lessonID, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
	if err != nil {
		t.Fatal("create prepare context:", err)
	}
	if err := transition.Publish(context.Background(), prepare, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("publish prepare:", err)
	}
	return controller, transition
}

func firstAudioTestMarkStartupVerified(t *testing.T, transition *firstAudioGuardTransitionLease) {
	t.Helper()
	controller := transition.owner
	hash := strings.Repeat("a", 64)
	controller.mu.Lock()
	defer controller.mu.Unlock()
	active := controller.publisher.Load()
	if active == nil || active.phase != firstAudioGuardStartupProbe || controller.activeTransition != transition {
		t.Fatal("test startup context is not active")
	}
	controller.startupBaselineHash = hash
	controller.startupVerificationReceipt = &firstAudioStartupVerificationReceipt{
		runID: active.runID, lessonID: active.currentLessonID, generation: active.generation, version: active.version,
		baselineHash: hash, verifiedHash: hash,
	}
}

func firstAudioTestPhysicalPreflightCandidate(t *testing.T, controller *firstAudioGuardController, runID string, lessonID int64, count int) (*firstAudioGuardContext, firstAudioGuardTransitionEvidence, [][]byte) {
	t.Helper()
	bindings := make([]firstAudioGuardProbeBinding, 0, count)
	contracts := make([]firstAudioGuardProbeContract, 0, count)
	rows := make([]firstAudioGuardRowContract, 0, count)
	receipts := make([]*firstAudioGuardProbeReceipt, 0, count)
	ids := make([]string, 0, count)
	tokens := make([][]byte, 0, count)
	expected := make(map[string]*sqlite3.SQLiteConn, count)
	for range count {
		raw := firstAudioTestOpenSQLiteConnection(t)
		registration, err := controller.connections.Register(raw)
		if err != nil {
			t.Fatal("register physical connection:", err)
		}
		t.Cleanup(func() { controller.connections.Retire(raw) })
		binding := firstAudioGuardProbeBinding{runID: runID, lessonID: lessonID, generation: 1, version: 3, connection: registration.ID, probeID: "probe-" + registration.ID}
		token, challenge, err := newFirstAudioPhysicalProbeChallenge(binding)
		if err != nil {
			t.Fatal("issue physical challenge:", err)
		}
		bindings = append(bindings, binding)
		contracts = append(contracts, challenge.Contract())
		rows = append(rows, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", lessonID, registration.ID), oldRow: []byte("same"), newRow: []byte("same")})
		receipts = append(receipts, challenge.receipt)
		ids = append(ids, registration.ID)
		tokens = append(tokens, token)
		expected[registration.ID] = raw
	}
	t.Cleanup(func() {
		for _, token := range tokens {
			zeroFirstAudioSecret(token)
		}
	})
	candidate, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: runID, currentLessonID: lessonID, generation: 1, phase: firstAudioGuardPreflight, version: 3, connectionIDs: ids, probeContracts: contracts, rowContracts: rows, requirePhysicalProbes: true})
	if err != nil {
		t.Fatal("create final context:", err)
	}
	return candidate, firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, expectedConnections: expected}, tokens
}

func TestFirstAudioPreflightPrepareRejectsFewerThanRequiredConnections(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_short", 705)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_short", 705, 2)
	if err := transition.Publish(context.Background(), candidate, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) && !errors.Is(err, errFirstAudioSQLiteRegistrationNotLive) {
		t.Fatalf("two-connection PREFLIGHT error=%v", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("short connection set did not terminalize before PREFLIGHT")
	}
}

func TestFirstAudioPreflightPrepareRegistryLockCoversPublishAndReceiptArm(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_lock_arm", 7051)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_lock_arm", 7051, 3)
	var retireID string
	var retireRaw *sqlite3.SQLiteConn
	for id, raw := range evidence.expectedConnections {
		retireID, retireRaw = id, raw
		break
	}
	if retireRaw == nil {
		t.Fatal("no pinned connection in evidence")
	}
	started := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	controller.beforeReceiptArm = func() {
		once.Do(func() {
			go func() {
				close(started)
				controller.connections.Retire(retireRaw)
				close(done)
			}()
			<-started
			select {
			case <-done:
				t.Error("physical retirement crossed the registry lock before all receipts were armed")
			default:
			}
		})
	}
	if err := transition.Publish(context.Background(), candidate, evidence); err != nil {
		t.Fatal("publish PREFLIGHT and arm receipts:", err)
	}
	controller.beforeReceiptArm = nil
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("physical retirement did not resume after registry lock release")
	}
	if !controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("PREFLIGHT context was not published")
	}
	if _, live := controller.connections.Lookup(retireRaw); live {
		t.Fatalf("connection %s remained registered after retirement", retireID)
	}
	for _, receipt := range evidence.probeReceipts {
		if !receipt.armed.Load() {
			t.Fatal("not all receipts were armed before registry lock release")
		}
	}
}

func TestFirstAudioPreflightPrepareRejectsExtraConnections(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_extra", 708)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_extra", 708, 4)
	if err := transition.Publish(context.Background(), candidate, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("four-connection PREFLIGHT error=%v", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("extra connection set did not terminalize before PREFLIGHT")
	}
}

func TestFirstAudioPreflightPrepareRejectsReplacedRawIdentity(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_replaced", 706)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_replaced", 706, 3)
	replacement := firstAudioTestOpenSQLiteConnection(t)
	for id := range evidence.expectedConnections {
		evidence.expectedConnections[id] = replacement
		break
	}
	if err := transition.Publish(context.Background(), candidate, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) && !errors.Is(err, errFirstAudioSQLiteRegistrationNotLive) {
		t.Fatalf("replaced raw identity error=%v", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("replaced raw identity did not terminalize before PREFLIGHT")
	}
	for _, receipt := range evidence.probeReceipts {
		if receipt.armed.Load() {
			t.Fatal("replaced identity armed a receipt")
		}
	}
}

func TestFirstAudioPreflightPrepareRejectsIncompleteReceiptSet(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_receipt", 707)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_receipt", 707, 3)
	evidence.probeReceipts = evidence.probeReceipts[:2]
	if err := transition.Publish(context.Background(), candidate, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("incomplete receipt set error=%v", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("incomplete receipt set did not terminalize before PREFLIGHT")
	}
}

func TestFirstAudioPreflightPrepareRejectsForeignReceipt(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_foreign", 709)
	candidate, evidence, _ := firstAudioTestPhysicalPreflightCandidate(t, controller, "preflight_prepare_foreign", 709, 3)
	foreignBinding := firstAudioGuardProbeBinding{runID: "preflight_prepare_other", lessonID: 999, generation: 1, version: 3, connection: "conn-foreign", probeID: "probe-foreign"}
	foreignToken, foreignChallenge, err := newFirstAudioPhysicalProbeChallenge(foreignBinding)
	if err != nil {
		t.Fatal("create foreign challenge:", err)
	}
	defer zeroFirstAudioSecret(foreignToken)
	evidence.probeReceipts[0] = foreignChallenge.receipt
	if err := transition.Publish(context.Background(), candidate, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("foreign receipt error=%v", err)
	}
	if !controller.IsTerminal() || controller.IsActivePhase(firstAudioGuardPreflight) {
		t.Fatal("foreign receipt did not terminalize before PREFLIGHT")
	}
	for _, receipt := range evidence.probeReceipts {
		if receipt.armed.Load() {
			t.Fatal("foreign receipt set armed a receipt")
		}
	}
}

func TestFirstAudioPreflightPrepareContextRejectsWrongPhaseVersionAndGeneration(t *testing.T) {
	cases := []firstAudioGuardContextSpec{
		{runID: "preflight_prepare_ctx", currentLessonID: 710, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 3},
		{runID: "preflight_prepare_ctx", currentLessonID: 710, generation: 2, phase: firstAudioGuardPreflightPrepare, version: 2},
		{runID: "preflight_prepare_ctx", currentLessonID: 710, generation: 1, phase: firstAudioGuardPreflight, version: 2},
	}
	for index, spec := range cases {
		if _, err := newFirstAudioGuardContext(spec); err == nil {
			t.Fatalf("invalid PREPARE context %d was accepted", index)
		}
	}
}

func TestFirstAudioPreflightPrepareRegistrationProvenance(t *testing.T) {
	cases := []struct {
		phase   firstAudioGuardContextPhase
		version uint64
		id      string
		next    uint64
		want    bool
	}{
		{firstAudioGuardDriverProbe, 0, "conn-1", 1, true},
		{firstAudioGuardPreflightPrepare, 2, "conn-2", 2, true},
		{firstAudioGuardPreflightPrepare, 2, "conn-3", 3, true},
		{firstAudioGuardStartupProbe, 1, "conn-2", 2, false},
		{firstAudioGuardPreflightPrepare, 1, "conn-2", 2, false},
		{firstAudioGuardPreflightPrepare, 2, "conn-1", 1, false},
		{firstAudioGuardPreflightPrepare, 2, "conn-4", 4, false},
		{firstAudioGuardPreflightPrepare, 2, "unexpected", 2, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_v%d_%s_next%d", tc.phase, tc.version, tc.id, tc.next), func(t *testing.T) {
			if got := firstAudioPrepareRegistrationAllowed(tc.phase, tc.version, tc.id, tc.next); got != tc.want {
				t.Fatalf("registration allowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFirstAudioPreflightPrepareRejectsRepeatAndJourneySkip(t *testing.T) {
	controller, transition := firstAudioTestStrictPreflightPrepare(t, "preflight_prepare_skip", 711)
	if _, err := controller.Acquire(); err == nil {
		t.Fatal("PREPARE exposed a user lease")
	}
	repeated, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "preflight_prepare_skip", currentLessonID: 711, generation: 1, phase: firstAudioGuardPreflightPrepare, version: 2})
	if err != nil {
		t.Fatal("create repeated context:", err)
	}
	if err := transition.Publish(context.Background(), repeated, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextStale) && !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("repeated PREPARE publish error=%v", err)
	}
	skipped, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 711, 1, 3))
	if err != nil {
		t.Fatal("create skip candidate:", err)
	}
	if err := transition.Publish(context.Background(), skipped, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("PREPARE->JOURNEY skip error=%v", err)
	}
	if !controller.IsActivePhase(firstAudioGuardPreflightPrepare) || controller.IsTerminal() {
		t.Fatal("rejected skip changed the active PREPARE phase")
	}
}

func TestFirstAudioReadbackRejectsLogicalEvidenceForTransactionBoundReceipt(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "readback_epoch", lessonID: 1, generation: 1, version: 1, connection: "conn-1", probeID: "probe-1"}
	token, challenge, err := newFirstAudioGuardProbeChallenge(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if !challenge.receipt.arm() {
		t.Fatal("could not arm readback fixture")
	}
	if err := challenge.BindTransactionEpoch(42); err != nil {
		t.Fatal("bind transaction epoch:", err)
	}
	if err := challenge.Consume(token, binding); err != nil {
		t.Fatal(err)
	}
	if err := challenge.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	executor := newFirstAudioGuardProbeExecutor(challenge)
	if err := executor.VerifyRollback(challenge.receipt, binding, nil); err != nil {
		t.Fatal(err)
	}
	if err := firstAudioConfirmLogicalReadbackForTest(challenge, true); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("logical readback evidence completed a SQL-bound receipt: %v", err)
	}
	if challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatal("SQL-bound receipt became complete without physical connection readback evidence")
	}
}

func TestFirstAudioReadbackRejectsLogicalEvidenceForPhysicalReceiptBeforeBegin(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "physical_readback", lessonID: 1, generation: 1, version: 1, connection: "conn-1", probeID: "probe-1"}
	token, challenge, err := newFirstAudioPhysicalProbeChallenge(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if !challenge.receipt.arm() {
		t.Fatal("could not arm physical readback fixture")
	}
	if challenge.receipt.txEpoch.Load() != 0 {
		t.Fatal("physical readback fixture unexpectedly has a transaction epoch")
	}
	challenge.receipt.state.Store(uint32(firstAudioGuardProbeRollbackVerified))
	if err := firstAudioConfirmLogicalReadbackForTest(challenge, true); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("physical receipt without epoch accepted logical readback: %v", err)
	}
	if challenge.receipt.completeFor(binding, challenge.digest) {
		t.Fatal("physical receipt completed without BeginTx-bound readback")
	}
}

func TestFirstAudioPreflightAdmissionRequiresExplicitReceiptMode(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "receipt_mode", lessonID: 1, generation: 1, version: 1, connection: "conn-1", probeID: "probe-1"}
	for _, test := range []struct {
		name            string
		physicalReceipt bool
		physicalContext bool
	}{
		{name: "logical receipt cannot enter physical preflight", physicalReceipt: false, physicalContext: true},
		{name: "physical receipt cannot enter logical preflight", physicalReceipt: true, physicalContext: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var challenge *firstAudioGuardProbeChallenge
			var token []byte
			var err error
			if test.physicalReceipt {
				token, challenge, err = newFirstAudioPhysicalProbeChallenge(binding)
			} else {
				token, challenge, err = newFirstAudioGuardProbeChallenge(binding)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer zeroFirstAudioSecret(token)
			candidate, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
				runID: binding.runID, currentLessonID: binding.lessonID, generation: binding.generation,
				phase: firstAudioGuardPreflight, version: binding.version,
				connectionIDs: []string{binding.connection}, probeContracts: []firstAudioGuardProbeContract{challenge.Contract()},
				rowContracts:          []firstAudioGuardRowContract{{table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", binding.lessonID, binding.connection), oldRow: []byte("typed"), newRow: []byte("typed")}},
				requirePhysicalProbes: test.physicalContext,
			})
			if err != nil {
				t.Fatal(err)
			}
			evidence := firstAudioGuardTransitionEvidence{probeBindings: []firstAudioGuardProbeBinding{binding}, probeReceipts: []*firstAudioGuardProbeReceipt{challenge.receipt}}
			if _, ok := evidence.preflightReceiptsFor(candidate); ok {
				t.Fatal("preflight admitted receipt whose mode disagrees with context")
			}
		})
	}
}

func TestFirstAudioGuardDriverProbeAdvancesOnlyToNewerStartupContext(t *testing.T) {
	driverSpec := firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	}
	driver, err := newFirstAudioGuardContext(driverSpec)
	if err != nil {
		t.Fatal("valid driver-probe context rejected:", err)
	}
	if !firstAudioGuardContextTransitionAllowed(nil, driver) {
		t.Fatal("driver probe is not accepted as the initial context")
	}

	startupSpec := driverSpec
	startupSpec.phase = firstAudioGuardStartupProbe
	startupSpec.version = 1
	startup, err := newFirstAudioGuardContext(startupSpec)
	if err != nil {
		t.Fatal("valid next startup context rejected:", err)
	}
	if !firstAudioGuardContextTransitionAllowed(driver, startup) {
		t.Fatal("DRIVER_PROBE -> newer STARTUP_PROBE transition rejected")
	}

	skipped, err := newFirstAudioGuardContext(firstAudioValidPreflightSpec(2))
	if err != nil {
		t.Fatal("valid-shaped preflight context rejected during construction:", err)
	}
	if firstAudioGuardContextTransitionAllowed(driver, skipped) {
		t.Fatal("DRIVER_PROBE skipped STARTUP_PROBE and entered PREFLIGHT")
	}

	wrongIdentitySpec := startupSpec
	wrongIdentitySpec.runID = "run-0002"
	wrongIdentity, err := newFirstAudioGuardContext(wrongIdentitySpec)
	if err != nil {
		t.Fatal("valid-shaped different-run context rejected during construction:", err)
	}
	if firstAudioGuardContextTransitionAllowed(driver, wrongIdentity) {
		t.Fatal("startup transition changed the run identity")
	}

	controller := newFirstAudioGuardController()
	if err := controller.PublishInitial(startup); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("direct initial STARTUP_PROBE publish = %v, want mandatory DRIVER_PROBE rejection", err)
	}
	if err := controller.PublishInitial(driver); err != nil {
		t.Fatal("controller rejected initial driver phase:", err)
	}
	versionedDriverSpec := driverSpec
	versionedDriverSpec.version = 1
	if _, err := newFirstAudioGuardContext(versionedDriverSpec); !errors.Is(err, errFirstAudioGuardContextInvalid) {
		t.Fatalf("nonzero driver-probe bootstrap version error = %v, want invalid context", err)
	}
	if err := controller.publisher.publishDriverStartup(startup, nil); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("startup publication without connection evidence = %v, want evidence rejection", err)
	}
	rawConn := firstAudioTestOpenSQLiteConnection(t)
	if _, err := controller.connections.Register(rawConn); err != nil {
		t.Fatal("register exact driver-probe connection:", err)
	}
	t.Cleanup(func() { controller.connections.Retire(rawConn) })
	if err := controller.ObserveDriverProbe(rawConn, sqlite3.SQLITE_SELECT, "", "", "", ""); err != nil {
		t.Fatal("observe exact dialector SELECT tuple:", err)
	}
	if err := controller.ObserveDriverProbe(rawConn, sqlite3.SQLITE_FUNCTION, "", "sqlite_version", "", ""); err != nil {
		t.Fatal("observe exact sqlite_version tuple:", err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal("could not acquire startup phase transition:", err)
	}
	if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("controller rejected monotonic driver-to-startup publication:", err)
	}
	if active := controller.publisher.Load(); active == nil || active.phase != firstAudioGuardStartupProbe || active.version != 1 {
		t.Fatalf("active context = %#v, want STARTUP_PROBE version 1", active)
	}
	if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextStale) {
		t.Fatalf("repeated startup publication = %v, want stale-context rejection", err)
	}
}

func TestFirstAudioGuardSQLiteCallbackTerminalLatchDoesNotAcquireLifecycleLock(t *testing.T) {
	driver, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil {
		t.Fatal("driver-probe context construction failed:", err)
	}
	controller := newFirstAudioGuardController()
	if err := controller.PublishInitial(driver); err != nil {
		t.Fatal("publish initial context:", err)
	}

	controller.mu.Lock()
	locked := true
	defer func() {
		if locked {
			controller.mu.Unlock()
		}
	}()
	callbackDone := make(chan struct{})
	go func() {
		_ = controller.TerminalizeFromSQLiteCallback()
		close(callbackDone)
	}()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("SQLite callback terminal latch blocked on the lifecycle mutex")
	}
	if controller.IsActivePhase(firstAudioGuardDriverProbe) {
		t.Fatal("terminal callback latch left the phase authorizer active")
	}
	controller.mu.Unlock()
	locked = false

	if !controller.IsTerminal() {
		t.Fatal("lock-free terminal latch was not visible to the lifecycle controller")
	}
	if _, err := controller.CloseAndDrain(context.Background()); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("controller admitted a transition after callback terminalization: %v", err)
	}
}

func TestFirstAudioGuardCallbackTerminalCASWinsAgainstPausedPhasePublish(t *testing.T) {
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		t.Fatal("startup context construction failed:", err)
	}
	preflight, err := newFirstAudioGuardContext(firstAudioValidPreflightSpec(2))
	if err != nil {
		t.Fatal("preflight context construction failed:", err)
	}
	controller := newFirstAudioGuardController()
	controller.publisher.current.Store(startup)
	enteredCAS := make(chan struct{})
	releaseCAS := make(chan struct{})
	var pauseOnce sync.Once
	controller.publisher.beforePublishCAS = func() {
		pauseOnce.Do(func() {
			close(enteredCAS)
			<-releaseCAS
		})
	}
	publishResult := make(chan error, 1)
	go func() { publishResult <- controller.publisher.Publish(preflight) }()
	select {
	case <-enteredCAS:
	case <-time.After(time.Second):
		close(releaseCAS)
		t.Fatal("phase publisher did not reach its compare-and-swap barrier")
	}
	controller.TerminalizeFromSQLiteCallback()
	close(releaseCAS)
	select {
	case err := <-publishResult:
		if err == nil {
			t.Fatal("phase publication succeeded after callback terminal CAS")
		}
	case <-time.After(time.Second):
		t.Fatal("phase publisher did not finish after releasing the CAS barrier")
	}
	active := controller.publisher.Load()
	if active == nil || active.phase != firstAudioGuardTerminal || active.version != 2 {
		t.Fatalf("callback refusal lost atomic phase arbitration: active=%#v", active)
	}
}

func TestFirstAudioGuardCallbackTerminalCASFailureStillBlocksPublisher(t *testing.T) {
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		t.Fatal("startup context construction failed:", err)
	}
	preflight, err := newFirstAudioGuardContext(firstAudioValidPreflightSpec(2))
	if err != nil {
		t.Fatal("preflight context construction failed:", err)
	}
	controller := newFirstAudioGuardController()
	controller.publisher.current.Store(startup)
	forcedConflicts := 0
	controller.publisher.beforeTerminalCAS = func() {
		active := controller.publisher.Load()
		clone, cloneErr := active.clone()
		if cloneErr != nil {
			t.Errorf("clone active context during forced CAS contention: %v", cloneErr)
			return
		}
		controller.publisher.current.Store(clone)
		forcedConflicts++
	}
	if err := controller.TerminalizeFromSQLiteCallback(); !errors.Is(err, errFirstAudioGuardContextStale) {
		t.Fatalf("terminal CAS exhaustion = %v, want contention error", err)
	}
	if forcedConflicts != firstAudioGuardContextCASLimit {
		t.Fatalf("forced terminal CAS conflicts=%d, want %d", forcedConflicts, firstAudioGuardContextCASLimit)
	}
	controller.publisher.beforeTerminalCAS = nil
	if err := controller.publisher.Publish(preflight); err == nil {
		t.Fatal("publisher accepted a later phase after terminal CAS exhausted")
	}
	if !controller.IsTerminal() {
		t.Fatal("terminal CAS failure did not leave a sticky terminal gate")
	}
	active := controller.publisher.Load()
	if active == nil || active.phase != firstAudioGuardStartupProbe {
		t.Fatalf("phase changed despite terminal CAS failure: %#v", active)
	}
}

func TestFirstAudioGuardCallbackTerminalVersionOverflowStaysFailClosed(t *testing.T) {
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardStartupProbe, version: ^uint64(0),
	})
	if err != nil {
		t.Fatal("maximum-version startup context construction failed:", err)
	}
	controller := newFirstAudioGuardController()
	controller.publisher.current.Store(startup)
	if err := controller.TerminalizeFromSQLiteCallback(); err == nil {
		t.Fatal("terminal context wrapped the maximum phase version")
	}
	preflight, err := newFirstAudioGuardContext(firstAudioValidPreflightSpec(^uint64(0)))
	if err != nil {
		t.Fatal("maximum-version preflight context construction failed:", err)
	}
	if err := controller.publisher.Publish(preflight); err == nil {
		t.Fatal("publisher accepted a phase after terminal version overflow")
	}
	if !controller.IsTerminal() {
		t.Fatal("version overflow did not leave a sticky terminal gate")
	}
	if active := controller.publisher.Load(); active == nil || active.phase != firstAudioGuardStartupProbe {
		t.Fatalf("version overflow changed active phase: %#v", active)
	}
}

func TestFirstAudioGuardDriverProbeRejectsCallbackFromDifferentPhysicalConnection(t *testing.T) {
	driver, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardDriverProbe, version: 0})
	if err != nil {
		t.Fatal(err)
	}
	controller := newFirstAudioGuardController()
	if err := controller.PublishInitial(driver); err != nil {
		t.Fatal(err)
	}
	connectionA := firstAudioTestOpenSQLiteConnection(t)
	connectionB := firstAudioTestOpenSQLiteConnection(t)
	if _, err := controller.connections.Register(connectionA); err != nil {
		t.Fatal("register connection A:", err)
	}
	t.Cleanup(func() { controller.connections.Retire(connectionA) })
	if _, err := controller.connections.Register(connectionB); err != nil {
		t.Fatal("register connection B:", err)
	}
	t.Cleanup(func() { controller.connections.Retire(connectionB) })
	if err := controller.ObserveDriverProbe(connectionA, sqlite3.SQLITE_SELECT, "", "", "", ""); err != nil {
		t.Fatal("first connection SELECT callback rejected:", err)
	}
	if err := controller.ObserveDriverProbe(connectionB, sqlite3.SQLITE_FUNCTION, "", "sqlite_version", "", ""); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("second physical connection callback = %v, want connection identity rejection", err)
	}
	if err := controller.PublishInitial(driver); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("terminalized driver context republished = %v, want transition rejection", err)
	}
	if !controller.terminal {
		t.Fatal("mismatched physical connection evidence left the controller nonterminal")
	}
}

func TestFirstAudioGuardDriverReceiptCannotPublishAfterPhysicalRetirement(t *testing.T) {
	driver, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardDriverProbe, version: 0})
	if err != nil {
		t.Fatal(err)
	}
	controller := newFirstAudioGuardController()
	if err := controller.PublishInitial(driver); err != nil {
		t.Fatal(err)
	}
	raw := firstAudioTestOpenSQLiteConnection(t)
	if _, err := controller.connections.Register(raw); err != nil {
		t.Fatal("register physical connection:", err)
	}
	if err := controller.ObserveDriverProbe(raw, sqlite3.SQLITE_SELECT, "", "", "", ""); err != nil {
		t.Fatal("observe driver SELECT:", err)
	}
	if err := controller.ObserveDriverProbe(raw, sqlite3.SQLITE_FUNCTION, "", "sqlite_version", "", ""); err != nil {
		t.Fatal("observe sqlite_version function:", err)
	}
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	controller.connections.Retire(raw)
	if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("startup publication after physical retirement = %v, want rejection", err)
	}
	if active := controller.publisher.Load(); active == nil || active.phase != firstAudioGuardDriverProbe || !controller.terminal {
		t.Fatalf("retired connection phase outcome active=%#v terminal=%v, want terminal DRIVER_PROBE", active, controller.terminal)
	}
}

func TestFirstAudioGuardProbeChallengeReceiptIsBoundSingleUseAndOrdered(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"}
	seed := bytes.Repeat([]byte{0x5A}, 32)
	token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if len(token) != 32 || challenge.Contract().tokenDigest != sha256.Sum256(token) {
		t.Fatal("challenge digest or token length is invalid")
	}
	if !challenge.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := challenge.Consume(token, binding); err != nil {
		t.Fatal("valid challenge rejected:", err)
	}
	if err := challenge.Consume(token, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("duplicate token use error = %v, want rejected", err)
	}
	if challenge.receipt.completeFor(binding, challenge.Contract().tokenDigest) {
		t.Fatal("reused token incorrectly completed a probe receipt")
	}

	secret, success, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(secret)
	if !success.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := success.Consume(secret, binding); err != nil {
		t.Fatal(err)
	}
	if err := success.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	executor := newFirstAudioGuardProbeExecutor(success)
	if err := executor.VerifyRollback(success.receipt, binding, nil); err != nil {
		t.Fatal("verified rollback rejected:", err)
	}
	if success.receipt.completeFor(binding, success.Contract().tokenDigest) {
		t.Fatal("rollback alone completed a receipt before snapshot readback")
	}
	if err := firstAudioConfirmLogicalReadbackForTest(success, true); err != nil {
		t.Fatal("snapshot readback confirmation rejected:", err)
	}
	if !success.receipt.completeFor(binding, success.Contract().tokenDigest) {
		t.Fatal("probe receipt did not reach readback-verified state")
	}
	if err := success.Consume(secret, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("post-success token replay error = %v, want rejected", err)
	}
	if success.receipt.completeFor(binding, success.Contract().tokenDigest) {
		t.Fatal("post-success denial left probe eligible for preflight")
	}
	failedReadbackToken, failedReadback, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal("create failed-readback fixture:", err)
	}
	defer zeroFirstAudioSecret(failedReadbackToken)
	if !failedReadback.receipt.arm() {
		t.Fatal("could not arm failed-readback fixture")
	}
	if err := failedReadback.Consume(failedReadbackToken, binding); err != nil {
		t.Fatal(err)
	}
	if err := failedReadback.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	failedReadbackExecutor := newFirstAudioGuardProbeExecutor(failedReadback)
	if err := failedReadbackExecutor.VerifyRollback(failedReadback.receipt, binding, nil); err != nil {
		t.Fatal(err)
	}
	if err := firstAudioConfirmLogicalReadbackForTest(failedReadback, false); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("unequal snapshot readback was accepted: %v", err)
	}
	if failedReadback.receipt.completeFor(binding, failedReadback.Contract().tokenDigest) {
		t.Fatal("failed snapshot readback left receipt eligible for PREFLIGHT")
	}
	productionToken, productionChallenge, err := newFirstAudioGuardProbeChallenge(binding)
	if err != nil {
		t.Fatal("crypto/rand challenge construction failed:", err)
	}
	defer zeroFirstAudioSecret(productionToken)
	if len(productionToken) != 32 || productionChallenge.Contract().tokenDigest != sha256.Sum256(productionToken) {
		t.Fatal("crypto/rand challenge has invalid length or digest")
	}
}

func TestFirstAudioGuardProbeReplayDenialBlocksJourneyDespiteCallerEvidence(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	activePreflight := controller.publisher.Load()
	firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
	challenge := &firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}
	if err := challenge.Consume(bytes.Repeat([]byte{0x7F}, 32), bindings[0]); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("replayed challenge = %v, want rejected", err)
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true, unexpectedDenials: 0}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("journey after probe replay = %v, want denial", err)
	}
	if controller.publisher.Load() != activePreflight {
		t.Fatal("replayed challenge advanced the active context")
	}
}

func TestFirstAudioGuardProbeActivityEndRejectsUnderflow(t *testing.T) {
	receipt := &firstAudioGuardProbeReceipt{activityDone: make(chan struct{}, 1)}
	receipt.endProbeActivity()
	if receipt.active.Load()&firstAudioGuardProbeActiveMask != 0 {
		t.Fatal("activity underflow changed the active count")
	}
	if receipt.denials.Load() != 1 {
		t.Fatalf("underflow denial count = %d, want 1", receipt.denials.Load())
	}
}

func TestFirstAudioGuardProbeChallengeRejectsBindingEntropyAndReceiptFailures(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"}
	seed := bytes.Repeat([]byte{0x11}, 32)
	if _, _, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed[:4]), binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("short entropy error = %v, want rejected", err)
	}
	if _, _, err := newFirstAudioGuardProbeChallengeFrom(nil, binding); !errors.Is(err, errFirstAudioGuardContextInvalid) {
		t.Fatalf("nil entropy source error = %v, want invalid binding", err)
	}

	token, wrongBindingChallenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if !wrongBindingChallenge.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	wrongConnection := binding
	wrongConnection.connection = "conn-b"
	if err := wrongBindingChallenge.Consume(token, wrongConnection); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("wrong connection error = %v, want rejected", err)
	}
	if state := firstAudioGuardProbeReceiptState(wrongBindingChallenge.receipt.state.Load()); state != firstAudioGuardProbeFailed {
		t.Fatalf("wrong binding receipt state = %d, want failed", state)
	}

	badToken := bytes.Repeat([]byte{0x22}, 32)
	unusedValidToken, wrongTokenChallenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(unusedValidToken)
	if !wrongTokenChallenge.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := wrongTokenChallenge.Consume(badToken, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("wrong token error = %v, want rejected", err)
	}
	zeroFirstAudioSecret(badToken)

	outOfOrderToken, outOfOrder, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(outOfOrderToken)
	if !outOfOrder.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := outOfOrder.ObserveRowGuard(binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("out-of-order row guard error = %v, want rejected", err)
	}

	rollbackToken, rollbackFailure, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !rollbackFailure.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := rollbackFailure.Consume(rollbackToken, binding); err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(rollbackToken)
	if err := rollbackFailure.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	wrongReceipt := &firstAudioGuardProbeReceipt{binding: binding}
	if err := newFirstAudioGuardProbeExecutor(rollbackFailure).VerifyRollback(wrongReceipt, binding, nil); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("foreign receipt error = %v, want rejected", err)
	}
	if rollbackFailure.receipt.completeFor(binding, rollbackFailure.Contract().tokenDigest) {
		t.Fatal("foreign receipt verified rollback")
	}
	rollbackToken, failedRollback, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !failedRollback.receipt.arm() {
		t.Fatal("could not arm unit-test challenge")
	}
	if err := failedRollback.Consume(rollbackToken, binding); err != nil {
		t.Fatal(err)
	}
	zeroFirstAudioSecret(rollbackToken)
	if err := failedRollback.ObserveRowGuard(binding); err != nil {
		t.Fatal(err)
	}
	rollbackErr := errors.New("injected rollback failure")
	if err := newFirstAudioGuardProbeExecutor(failedRollback).VerifyRollback(failedRollback.receipt, binding, rollbackErr); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("failed rollback error = %v, want rejected", err)
	}
	if failedRollback.receipt.completeFor(binding, failedRollback.Contract().tokenDigest) || firstAudioGuardProbeReceiptState(failedRollback.receipt.state.Load()) != firstAudioGuardProbeFailed {
		t.Fatal("failed rollback completed its probe receipt")
	}
}

func TestFirstAudioGuardControllerGatesStartupAndVerifiedPreflight(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal("acquire after verified preflight:", err)
	}
	if lease.Context().Phase() != firstAudioGuardJourney || lease.Context().Version() != 3 {
		t.Fatalf("lease context = %s/%d, want JOURNEY/3", lease.Context().Phase(), lease.Context().Version())
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); !errors.Is(err, errFirstAudioGuardLeaseReleased) {
		t.Fatalf("double release = %v, want rejected", err)
	}
}

func TestFirstAudioGuardControllerPublishesPreflightBeforeProbes(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal(err)
	}
	bindings := []firstAudioGuardProbeBinding{
		{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"},
		{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-b", probeID: "probe-b"},
	}
	spec := firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardPreflight, version: 2}
	tokens := make([][]byte, 0, len(bindings))
	receipts := make([]*firstAudioGuardProbeReceipt, 0, len(bindings))
	challenges := make([]*firstAudioGuardProbeChallenge, 0, len(bindings))
	for i, binding := range bindings {
		seed := bytes.Repeat([]byte{byte(0x30 + i)}, 32)
		token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
		zeroFirstAudioSecret(seed)
		if err != nil {
			t.Fatal(err)
		}
		if firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeIssued {
			t.Fatal("new probe was advanced before PREFLIGHT publication")
		}
		tokens = append(tokens, token)
		receipts = append(receipts, challenge.receipt)
		challenges = append(challenges, challenge)
		spec.connectionIDs = append(spec.connectionIDs, binding.connection)
		spec.probeContracts = append(spec.probeContracts, challenge.Contract())
		spec.rowContracts = append(spec.rowContracts, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: "lesson:101/conn:" + binding.connection, oldRow: []byte("lesson-row"), newRow: []byte("lesson-row")})
	}
	preflight, err := newFirstAudioGuardContext(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	activePreflight := controller.publisher.Load()
	if activePreflight == nil || activePreflight.Phase() != firstAudioGuardPreflight {
		t.Fatal("PREFLIGHT context was not published before probe execution")
	}
	for i, challenge := range challenges {
		if err := challenge.Consume(tokens[i], bindings[i]); err != nil {
			t.Fatal("consume probe token:", err)
		}
		zeroFirstAudioSecret(tokens[i])
		if err := challenge.ObserveRowGuard(bindings[i]); err != nil {
			t.Fatal("observe row guard:", err)
		}
		if err := newFirstAudioGuardProbeExecutor(challenge).VerifyRollback(challenge.receipt, bindings[i], nil); err != nil {
			t.Fatal("verify probe rollback:", err)
		}
		if err := firstAudioConfirmLogicalReadbackForTest(challenge, true); err != nil {
			t.Fatal("confirm preflight snapshot readback:", err)
		}
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
	if err := transition.Publish(context.Background(), journey, evidence); err != nil {
		t.Fatal(err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		zeroFirstAudioSecret(token)
	}
}

func TestFirstAudioGuardChallengeCannotRunBeforePreflightArmsIt(t *testing.T) {
	binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: 2, connection: "conn-a", probeID: "probe-a"}
	seed := bytes.Repeat([]byte{0x3C}, 32)
	token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if err := challenge.Consume(token, binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("pre-PREFLIGHT consume = %v, want rejected", err)
	}
	if challenge.receipt.denials.Load() != 1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeFailed {
		t.Fatal("pre-PREFLIGHT probe attempt did not fail closed")
	}
}

func TestFirstAudioGuardControllerCloseDrainAndReopenWithNewContext(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	oldLease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	oldContext := oldLease.Context()
	drainResult := make(chan *firstAudioGuardTransitionLease, 1)
	drainError := make(chan error, 1)
	go func() {
		transition, err := controller.CloseAndDrain(context.Background())
		drainResult <- transition
		drainError <- err
	}()
	waitFirstAudioAdmissionClosed(t, controller)
	if err := oldLease.Release(); err != nil {
		t.Fatal(err)
	}
	transition := <-drainResult
	if err := <-drainError; err != nil {
		t.Fatal(err)
	}
	candidate, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(4, 101, 1, 4))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{snapshotUnchanged: true, registryReconciled: true, writeCommitted: true, readbackVerified: true}
	if err := transition.Publish(context.Background(), candidate, evidence); err != nil {
		t.Fatal("publish next complete context:", err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	newLease, err := controller.Acquire()
	if err != nil {
		t.Fatal("admission did not reopen:", err)
	}
	if newLease.Context() == oldContext || newLease.Context().Version() != 4 {
		t.Fatal("new lease did not bind the newly published pointer/version")
	}
	if err := newLease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerDrainTimeoutIsOutOfBandTerminal(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	before := controller.publisher.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controller.CloseAndDrain(ctx); !errors.Is(err, errFirstAudioGuardDrainTimeout) {
		t.Fatalf("drain error = %v, want timeout", err)
	}
	if controller.publisher.Load() != before {
		t.Fatal("drain timeout replaced a context still held by an active lease")
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("admission after timeout = %v, want closed", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerLeaseAcquisitionIsBoundToGateVersion(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	old, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := old.Context().Version()
	drained := make(chan *firstAudioGuardTransitionLease, 1)
	drainErr := make(chan error, 1)
	go func() {
		transition, err := controller.CloseAndDrain(context.Background())
		drained <- transition
		drainErr <- err
	}()
	waitFirstAudioAdmissionClosed(t, controller)
	if err := old.Release(); err != nil {
		t.Fatal(err)
	}
	transition := <-drained
	if err := <-drainErr; err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("acquire while closed = %v", err)
	}
	candidate, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(oldVersion+1, 101, 1, int(oldVersion+1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), candidate, firstAudioGuardTransitionEvidence{snapshotUnchanged: true, registryReconciled: true, writeCommitted: true, readbackVerified: true}); err != nil {
		t.Fatal(err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	newLease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if newLease.version == oldVersion || newLease.Context() != controller.publisher.Load() {
		t.Fatal("acquire did not bind the active context under the gate")
	}
	if err := newLease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerRejectsIncompletePreflightReceiptSet(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	for _, secret := range tokens {
		zeroFirstAudioSecret(secret)
	}
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts[:1], snapshotUnchanged: true, registryReconciled: true}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("incomplete receipt set publish error = %v, want transition denial", err)
	}
	if active := controller.publisher.Load(); active == nil || active.phase != firstAudioGuardPreflight {
		t.Fatal("incomplete preflight evidence changed the active context")
	}
	if err := transition.OpenJourney(); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("open after rejected journey = %v, want transition denial", err)
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("admission after incomplete probe set = %v, want closed", err)
	}
	if err := transition.MarkTerminal(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardPreflightReceiptMustMatchActiveTokenDigest(t *testing.T) {
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal(err)
	}
	activePreflight := controller.publisher.Load()
	firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
	// Keep the same identity binding but create a completed receipt for another token.
	seed := bytes.Repeat([]byte{0xE7}, 32)
	token, foreign, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(seed), bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	defer zeroFirstAudioSecret(token)
	if !foreign.receipt.arm() {
		t.Fatal("could not arm foreign unit-test challenge")
	}
	if err := foreign.Consume(token, bindings[0]); err != nil {
		t.Fatal(err)
	}
	if err := foreign.ObserveRowGuard(bindings[0]); err != nil {
		t.Fatal(err)
	}
	if err := newFirstAudioGuardProbeExecutor(foreign).VerifyRollback(foreign.receipt, bindings[0], nil); err != nil {
		t.Fatal(err)
	}
	if foreign.receipt.completeFor(bindings[0], preflight.probes[bindings[0].connection].tokenDigest) {
		t.Fatal("same-identity foreign digest satisfied the active preflight contract")
	}
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: []*firstAudioGuardProbeReceipt{foreign.receipt, receipts[1]}, snapshotUnchanged: true, registryReconciled: true}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("foreign token receipt publish error = %v, want transition denial", err)
	}
	if controller.publisher.Load() != activePreflight {
		t.Fatal("foreign token receipt replaced active preflight context")
	}
}

func TestFirstAudioGuardConcurrentCloseAndDrainHasSingleOwner(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan *firstAudioGuardTransitionLease, 1)
	firstErr := make(chan error, 1)
	go func() { tr, e := controller.CloseAndDrain(context.Background()); firstResult <- tr; firstErr <- e }()
	waitFirstAudioAdmissionClosed(t, controller)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if tr, err := controller.CloseAndDrain(ctx); !errors.Is(err, errFirstAudioGuardAdmissionClosed) || tr != nil {
		t.Fatalf("second drain = (%v, %v), want immediate single-owner rejection", tr, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if tr := <-firstResult; tr == nil {
		t.Fatal("first drain did not receive transition ownership")
	}
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardPreflightPublishWaitsForProbeActivityAndTimesOutTerminal(t *testing.T) {
	newPreflightController := func() (*firstAudioGuardController, *firstAudioGuardTransitionLease, *firstAudioGuardContext, []firstAudioGuardProbeBinding, []*firstAudioGuardProbeReceipt) {
		t.Helper()
		c := newFirstAudioGuardController()
		startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
		if err != nil {
			t.Fatal(err)
		}
		tr, err := firstAudioTestPublishInitialStartup(c, startup)
		if err != nil {
			t.Fatal(err)
		}
		pre, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
		if err := tr.Publish(context.Background(), pre, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
			t.Fatal(err)
		}
		firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
		return c, tr, c.publisher.Load(), bindings, receipts
	}
	t.Run("waits for admitted probe callback", func(t *testing.T) {
		c, tr, pre, bindings, receipts := newPreflightController()
		if !receipts[0].beginProbeActivity() {
			t.Fatal("failed to start probe activity")
		}
		journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
		done := make(chan error, 1)
		go func() { done <- tr.Publish(context.Background(), journey, evidence) }()
		deadline := time.Now().Add(time.Second)
		for !receipts[0].probeAdmissionClosed() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !receipts[0].probeAdmissionClosed() {
			t.Fatal("probe admission did not close")
		}
		if receipts[0].probeGateDrained() {
			t.Fatal("probe drain completed while callback remained active")
		}
		late := &firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}
		denials := receipts[0].denials.Load()
		if err := late.Consume([]byte("late callback"), bindings[0]); !errors.Is(err, errFirstAudioGuardProbeRejected) {
			t.Fatalf("post-close callback = %v, want rejected", err)
		}
		if receipts[0].denials.Load() != denials || firstAudioGuardProbeReceiptState(receipts[0].state.Load()) != firstAudioGuardProbeReadbackVerified {
			t.Fatal("post-close callback mutated the completed receipt")
		}
		if c.publisher.Load() != pre {
			t.Fatal("context pointer changed before probe callback drained")
		}
		receipts[0].endProbeActivity()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := tr.OpenJourney(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("in-flight replay invalidates proof before publication", func(t *testing.T) {
		c, tr, pre, bindings, receipts := newPreflightController()
		if !receipts[0].beginProbeActivity() {
			t.Fatal("failed to admit in-flight probe callback")
		}
		challenge := &firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}
		journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
		done := make(chan error, 1)
		go func() { done <- tr.Publish(context.Background(), journey, evidence) }()
		deadline := time.Now().Add(time.Second)
		for !receipts[0].probeAdmissionClosed() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !receipts[0].probeAdmissionClosed() {
			t.Fatal("probe admission did not close")
		}
		if err := challenge.consumeAdmitted([]byte("late replay from admitted callback")); !errors.Is(err, errFirstAudioGuardProbeRejected) {
			t.Fatalf("in-flight replay = %v, want rejection", err)
		}
		receipts[0].endProbeActivity()
		if err := <-done; !errors.Is(err, errFirstAudioGuardContextTransition) {
			t.Fatalf("journey publish after in-flight replay = %v, want transition denial", err)
		}
		if c.publisher.Load() != pre {
			t.Fatal("in-flight replay changed context pointer")
		}
		c.mu.Lock()
		terminal := c.terminal
		c.mu.Unlock()
		if !terminal {
			t.Fatal("in-flight replay did not terminalize controller")
		}
	})
	t.Run("timeout fails closed without pointer swap", func(t *testing.T) {
		c, tr, pre, bindings, receipts := newPreflightController()
		if !receipts[0].beginProbeActivity() {
			t.Fatal("failed to start probe activity")
		}
		defer receipts[0].endProbeActivity()
		journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
		if err != nil {
			t.Fatal(err)
		}
		evidence := firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := tr.Publish(ctx, journey, evidence); !errors.Is(err, errFirstAudioGuardDrainTimeout) {
			t.Fatalf("publish error = %v, want drain timeout", err)
		}
		if c.publisher.Load() != pre {
			t.Fatal("timeout replaced active context")
		}
		if _, err := c.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
			t.Fatalf("admission after timeout = %v, want closed", err)
		}
		c.mu.Lock()
		terminal := c.terminal
		c.mu.Unlock()
		if !terminal {
			t.Fatal("probe drain timeout did not mark out-of-band terminal")
		}
		for i, receipt := range receipts {
			if !receipt.probeAdmissionClosed() {
				t.Fatalf("receipt %d remained open after drain timeout", i)
			}
		}
		if (&firstAudioGuardProbeChallenge{binding: bindings[0], digest: receipts[0].digest, receipt: receipts[0]}).Consume([]byte("late"), bindings[0]) == nil {
			t.Fatal("callback accepted after probe admission closed")
		}
	})
}

func TestFirstAudioGuardLateProbeIsRejectedAfterJourney(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	controller.mu.Lock()
	var challenge *firstAudioGuardProbeChallenge
	for _, receipt := range controller.probeReceipts {
		challenge = &firstAudioGuardProbeChallenge{binding: receipt.binding, digest: receipt.digest, receipt: receipt}
		break
	}
	controller.mu.Unlock()
	if challenge == nil {
		t.Fatal("controller lost registered probe receipt")
	}
	denials := challenge.receipt.denials.Load()
	if err := challenge.Consume(bytes.Repeat([]byte{0x55}, 32), challenge.binding); !errors.Is(err, errFirstAudioGuardProbeRejected) {
		t.Fatalf("late probe consume error = %v, want rejected", err)
	}
	if challenge.receipt.denials.Load() != denials {
		t.Fatal("closed-gate late probe changed the completed receipt denial count")
	}
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatalf("ordinary admission changed after expected late-probe rejection: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardControllerCloseAcquireRaceDrainsAllAdmittedLeases(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	const callers = 24
	start := make(chan struct{})
	results := make(chan error, callers)
	var callersWG sync.WaitGroup
	for range callers {
		callersWG.Add(1)
		go func() {
			defer callersWG.Done()
			<-start
			lease, err := controller.Acquire()
			if err != nil {
				if !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
					results <- fmt.Errorf("unexpected acquire error: %w", err)
					return
				}
				results <- nil
				return
			}
			results <- lease.Release()
		}()
	}
	close(start)
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	callersWG.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if firstAudioActiveLeaseCountForTest(controller) != 0 {
		t.Fatal("transition acquired while application leases remained")
	}
	if err := transition.MarkTerminal(context.Background()); err != nil {
		t.Fatal("terminalize drained controller:", err)
	}
	if active := controller.publisher.Load(); active == nil || active.Phase() != firstAudioGuardTerminal {
		t.Fatal("drained terminal transition did not publish terminal context")
	}
	if _, err := controller.Acquire(); !errors.Is(err, errFirstAudioGuardAdmissionClosed) {
		t.Fatalf("admission reopened after terminal transition: %v", err)
	}
}

func TestFirstAudioGuardControllerResetRequiresVerifiedNextGeneration(t *testing.T) {
	controller := firstAudioTestOpenJourney(t)
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reset, err := newFirstAudioGuardContext(firstAudioValidResetSpec(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), reset, firstAudioGuardTransitionEvidence{}); err != nil {
		t.Fatal("publish reset context:", err)
	}
	next, err := newFirstAudioGuardContext(firstAudioValidFreshJourneySpec(5, 202, 2, 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := transition.Publish(context.Background(), next, firstAudioGuardTransitionEvidence{snapshotUnchanged: true, registryReconciled: true, resetCommitted: true, readbackVerified: true}); err != nil {
		t.Fatal("publish verified next-generation context:", err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	lease, err := controller.Acquire()
	if err != nil {
		t.Fatal("admission did not reopen for next generation:", err)
	}
	if lease.Context().currentLessonID != 202 || lease.Context().generation != 2 || lease.Context().version != 5 {
		t.Fatalf("next generation context = lesson %d generation %d version %d", lease.Context().currentLessonID, lease.Context().generation, lease.Context().version)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFirstAudioGuardPublisherRejectsIllegalTransitionsAndTerminalBackEdges(t *testing.T) {
	var publisher firstAudioGuardContextPublisher
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(1, 101, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(journey); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("initial JOURNEY publish = %v, want transition denial", err)
	}
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(startup); !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("initial STARTUP_PROBE publish = %v, want mandatory driver receipt", err)
	}
	firstAudioPublishJourney(t, &publisher, 3)
	terminal, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardTerminal, version: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(terminal); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(startup); !errors.Is(err, errFirstAudioGuardContextStale) && !errors.Is(err, errFirstAudioGuardContextTransition) {
		t.Fatalf("terminal back-edge = %v, want rejected", err)
	}
}

func firstAudioTestOpenJourney(t *testing.T) *firstAudioGuardController {
	t.Helper()
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardStartupProbe, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal(err)
	}
	preflight, tokens, bindings, receipts := firstAudioTestPreflight(t, 2)
	if err := transition.Publish(context.Background(), preflight, firstAudioTestPreflightEvidence(bindings, receipts)); err != nil {
		t.Fatal("publish preflight context:", err)
	}
	firstAudioTestCompleteProbes(t, tokens, bindings, receipts)
	journey, err := newFirstAudioGuardContext(firstAudioValidJourneySpec(3, 101, 1, 3))
	if err != nil {
		t.Fatal(err)
	}
	evidence := firstAudioGuardTransitionEvidence{
		probeBindings: bindings, probeReceipts: receipts, snapshotUnchanged: true, registryReconciled: true,
	}
	if err := transition.Publish(context.Background(), journey, evidence); err != nil {
		t.Fatal("publish verified journey context:", err)
	}
	if err := transition.OpenJourney(); err != nil {
		t.Fatal(err)
	}
	return controller
}

func firstAudioTestPreflightEvidence(bindings []firstAudioGuardProbeBinding, receipts []*firstAudioGuardProbeReceipt) firstAudioGuardTransitionEvidence {
	return firstAudioGuardTransitionEvidence{probeBindings: bindings, probeReceipts: receipts}
}

func firstAudioTestCompleteProbes(t *testing.T, tokens [][]byte, bindings []firstAudioGuardProbeBinding, receipts []*firstAudioGuardProbeReceipt) {
	t.Helper()
	if len(tokens) != len(bindings) || len(receipts) != len(bindings) {
		t.Fatal("probe fixture dimensions do not match")
	}
	for i, receipt := range receipts {
		challenge := &firstAudioGuardProbeChallenge{binding: bindings[i], digest: receipt.digest, receipt: receipt}
		if err := challenge.Consume(tokens[i], bindings[i]); err != nil {
			t.Fatal("consume preflight probe:", err)
		}
		zeroFirstAudioSecret(tokens[i])
		if err := challenge.ObserveRowGuard(bindings[i]); err != nil {
			t.Fatal("observe preflight row guard:", err)
		}
		if err := newFirstAudioGuardProbeExecutor(challenge).VerifyRollback(receipt, bindings[i], nil); err != nil {
			t.Fatal("verify preflight rollback:", err)
		}
		if err := firstAudioConfirmLogicalReadbackForTest(challenge, true); err != nil {
			t.Fatal("confirm preflight snapshot readback:", err)
		}
	}
}

func firstAudioTestPreflight(t *testing.T, version uint64) (*firstAudioGuardContext, [][]byte, []firstAudioGuardProbeBinding, []*firstAudioGuardProbeReceipt) {
	t.Helper()
	connections := []string{"conn-a", "conn-b"}
	spec := firstAudioGuardContextSpec{runID: "run-0001", currentLessonID: 101, generation: 1, phase: firstAudioGuardPreflight, version: version}
	var tokens [][]byte
	var bindings []firstAudioGuardProbeBinding
	var receipts []*firstAudioGuardProbeReceipt
	for i, connection := range connections {
		binding := firstAudioGuardProbeBinding{runID: "run-0001", lessonID: 101, generation: 1, version: version, connection: connection, probeID: fmt.Sprintf("probe-%d", i)}
		tokenSeed := bytes.Repeat([]byte{byte(0xA0 + i)}, 32)
		token, challenge, err := newFirstAudioGuardProbeChallengeFrom(bytes.NewReader(tokenSeed), binding)
		if err != nil {
			t.Fatal(err)
		}
		zeroFirstAudioSecret(tokenSeed)
		spec.probeContracts = append(spec.probeContracts, challenge.Contract())
		spec.connectionIDs = append(spec.connectionIDs, connection)
		spec.rowContracts = append(spec.rowContracts, firstAudioGuardRowContract{table: "lessons", operation: "UPDATE", identity: "lesson:101/conn:" + connection, oldRow: []byte("lesson-row"), newRow: []byte("lesson-row")})
		tokens = append(tokens, token)
		bindings = append(bindings, binding)
		receipts = append(receipts, challenge.receipt)
	}
	ctx, err := newFirstAudioGuardContext(spec)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, tokens, bindings, receipts
}

func waitFirstAudioAdmissionClosed(t *testing.T, controller *firstAudioGuardController) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		controller.mu.Lock()
		closed := !controller.admissionOpen
		controller.mu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("admission did not close")
		}
		time.Sleep(time.Millisecond)
	}
}

func firstAudioActiveLeaseCountForTest(controller *firstAudioGuardController) int {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.activeLeasesLocked()
}

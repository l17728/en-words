//go:build pilot_e2e

package pilotsupervisor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/obs"
	"github.com/fxamacker/cbor/v2"
	_ "modernc.org/sqlite"
)

func testBootstrapHandoffRequest(generation uint64) bootstrapHandoffRequest {
	return bootstrapHandoffRequest{
		Version: 1, Type: "BOOTSTRAP", Generation: generation,
		LaunchNonce: "0123456789abcdef0123456789abcdef", RunEpoch: "abcdef0123456789abcdef0123456789",
		PID: 1234, UID: 1001, StartTimeTicks: 987654, PIDNSDevice: 11, PIDNSInode: 12,
		CgroupMountID: 13, CgroupDevice: 14, CgroupInode: 15,
	}
}

type runRootSQLiteCodeError struct{ code int }

func (e runRootSQLiteCodeError) Error() string { return "driver error" }
func (e runRootSQLiteCodeError) Code() int     { return e.code }

func TestRunRootLedgerSQLiteBusyClassificationUsesPrimaryCode(t *testing.T) {
	for _, code := range []int{5, 261, 6, 262} {
		if !runRootLedgerSQLiteBusyOrLocked(runRootSQLiteCodeError{code: code}) {
			t.Fatalf("SQLite BUSY/LOCKED code %d not classified as retryable", code)
		}
	}
	for _, code := range []int{0, -5, 19, 275, 787} {
		if runRootLedgerSQLiteBusyOrLocked(runRootSQLiteCodeError{code: code}) {
			t.Fatalf("non-BUSY/LOCKED code %d classified as retryable", code)
		}
	}
	if runRootLedgerSQLiteBusyOrLocked(errors.New("database is locked")) {
		t.Fatal("text-only lookalike error classified as retryable")
	}
}

func TestRunRootLedgerScalarDecoderAndStateResponseBoundaries(t *testing.T) {
	if decodeRunRootUint64([]byte{1}) != 0 || decodeRunRootUint64(encodeRunRootUint64(^uint64(0))) != ^uint64(0) {
		t.Fatal("uint64 decoder did not enforce fixed-width unsigned encoding")
	}
	for _, tc := range []struct {
		state    LedgerState
		response string
		want     bool
	}{
		{LedgerPrepared, "", true},
		{LedgerExecuting, "", true},
		{LedgerIndeterminate, "restart_required", true},
		{LedgerIndeterminate, "ok", false},
		{LedgerCompleted, "ok", false},
	} {
		if got := validPersistedRunRootStateResponse(string(tc.state), tc.response); got != tc.want {
			t.Errorf("validPersistedRunRootStateResponse(%q, %q) = %v, want %v", tc.state, tc.response, got, tc.want)
		}
	}
}

func TestRunRootLedgerRestoresFiveSecondWriterBusyTimeout(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "writer-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	var timeout int
	if err = ledger.conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("writer busy timeout = %d, want 5000, err=%v", timeout, err)
	}
}

func TestRunRootLedgerConsumeBootstrapPersistsImmutableIdentityAndRejectsReplay(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	request := testBootstrapHandoffRequest(1)
	if consumed, err := ledger.ConsumeBootstrap(ctx, request); err != nil || !consumed {
		t.Fatalf("first consume = %v, %v", consumed, err)
	}
	var generation, startTicks []byte
	var nonce, epoch string
	var pid, uid int64
	if err := ledger.conn.QueryRowContext(ctx, `SELECT generation,launch_nonce,run_epoch,pid,uid,start_time_ticks FROM pilot_bootstrap_handoffs`).
		Scan(&generation, &nonce, &epoch, &pid, &uid, &startTicks); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(generation) != "0000000000000001" || hex.EncodeToString(startTicks) != "00000000000f1206" ||
		nonce != request.LaunchNonce || epoch != request.RunEpoch || pid != int64(request.PID) || uid != int64(request.UID) {
		t.Fatalf("persisted handoff tuple mismatch: gen=%x start=%x pid=%d uid=%d", generation, startTicks, pid, uid)
	}
	if consumed, err := ledger.ConsumeBootstrap(ctx, request); err != nil || consumed {
		t.Fatalf("same generation replay = %v, %v", consumed, err)
	}
	newer := testBootstrapHandoffRequest(2)
	newer.LaunchNonce = "1123456789abcdef0123456789abcdef"
	newer.RunEpoch = "bb23456789abcdef0123456789abcdef"
	if consumed, err := ledger.ConsumeBootstrap(ctx, newer); err != nil || !consumed {
		t.Fatalf("increasing generation = %v, %v", consumed, err)
	}
	stale := testBootstrapHandoffRequest(1)
	stale.LaunchNonce = "2123456789abcdef0123456789abcdef"
	stale.RunEpoch = "cb23456789abcdef0123456789abcdef"
	if consumed, err := ledger.ConsumeBootstrap(ctx, stale); err != nil || consumed {
		t.Fatalf("stale generation accepted = %v, %v", consumed, err)
	}
	duplicateNonce := testBootstrapHandoffRequest(3)
	duplicateNonce.LaunchNonce = request.LaunchNonce
	duplicateNonce.RunEpoch = "db23456789abcdef0123456789abcdef"
	if consumed, err := ledger.ConsumeBootstrap(ctx, duplicateNonce); err != nil || consumed {
		t.Fatalf("duplicate nonce accepted = %v, %v", consumed, err)
	}
	duplicateEpoch := testBootstrapHandoffRequest(3)
	duplicateEpoch.LaunchNonce = "3123456789abcdef0123456789abcdef"
	duplicateEpoch.RunEpoch = request.RunEpoch
	if consumed, err := ledger.ConsumeBootstrap(ctx, duplicateEpoch); err != nil || consumed {
		t.Fatalf("duplicate run epoch accepted = %v, %v", consumed, err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE pilot_bootstrap_handoffs SET uid=1002 WHERE generation=?`, generation); err == nil {
		t.Fatal("immutable handoff row was updated")
	}
	if _, err := ledger.conn.ExecContext(ctx, `DELETE FROM pilot_bootstrap_handoffs WHERE generation=?`, generation); err == nil {
		t.Fatal("immutable handoff row was deleted")
	}
}

func TestRunRootLedgerConsumeBootstrapRejectsNilAndInvalidRequests(t *testing.T) {
	ctx := context.Background()
	valid := testBootstrapHandoffRequest(1)
	var nilLedger *RunRootLedger
	if consumed, err := nilLedger.ConsumeBootstrap(ctx, valid); consumed || !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil ledger result = %v, %v", consumed, err)
	}
	ledger := newTestRunRootLedger(t)
	if consumed, err := ledger.ConsumeBootstrap(nil, valid); consumed || !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil context result = %v, %v", consumed, err)
	}
	invalid := valid
	invalid.Generation = 0
	if consumed, err := ledger.ConsumeBootstrap(ctx, invalid); consumed || !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("invalid request result = %v, %v", consumed, err)
	}
	var count int
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM pilot_bootstrap_handoffs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid request wrote rows: count=%d err=%v", count, err)
	}
}

func TestRunRootLedgerConsumeBootstrapFencesAfterHighWaterReadFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "highwater-read-failure.db")
	ownerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerDB.Close()
	ledger, err := NewRunRootLedger(ctx, ownerDB)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	attackerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer attackerDB.Close()
	if _, err = attackerDB.ExecContext(ctx, `DROP TABLE pilot_bootstrap_handoffs`); err != nil {
		t.Fatal(err)
	}
	if consumed, err := ledger.ConsumeBootstrap(ctx, testBootstrapHandoffRequest(1)); consumed || !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("high-water read failure result = %v, %v", consumed, err)
	}
	if !ledger.fenced || ledger.conn != nil {
		t.Fatal("high-water read failure did not fence and discard the pinned connection")
	}
}

func TestRunRootLedgerConsumeBootstrapUsesUnsignedUint64Ordering(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	for i, generation := range []uint64{1<<63 - 1, 1 << 63, ^uint64(0)} {
		request := testBootstrapHandoffRequest(generation)
		request.LaunchNonce = fmt.Sprintf("%032x", i+1)
		request.RunEpoch = fmt.Sprintf("%032x", i+11)
		if consumed, err := ledger.ConsumeBootstrap(ctx, request); err != nil || !consumed {
			t.Fatalf("accept generation %d = %v, %v", generation, consumed, err)
		}
	}
	if consumed, err := ledger.ConsumeBootstrap(ctx, testBootstrapHandoffRequest(^uint64(0))); err != nil || consumed {
		t.Fatalf("MaxUint64 replay = %v, %v", consumed, err)
	}
	var highWater []byte
	if err := ledger.conn.QueryRowContext(ctx, `SELECT generation FROM pilot_bootstrap_handoffs ORDER BY generation DESC LIMIT 1`).Scan(&highWater); err != nil {
		t.Fatal(err)
	}
	if got := decodeRunRootUint64(highWater); got != ^uint64(0) {
		t.Fatalf("unsigned high-water = %d, want MaxUint64", got)
	}
}

func TestRunRootLedgerBootstrapSchemaRejectsMalformedWireValues(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	good := testBootstrapHandoffRequest(1)
	generation := encodeRunRootUint64(1)
	insert := `INSERT INTO pilot_bootstrap_handoffs(generation,launch_nonce,run_epoch,pid,uid,start_time_ticks,pidns_device,pidns_inode,cgroup_mount_id,cgroup_device,cgroup_inode) VALUES(?,?,?,?,?,?,?,?,?,?,?)`
	validArgs := func(nonce, epoch any) []any {
		return []any{generation, nonce, epoch, int64(good.PID), int64(good.UID), encodeRunRootUint64(good.StartTimeTicks),
			encodeRunRootUint64(good.PIDNSDevice), encodeRunRootUint64(good.PIDNSInode), encodeRunRootUint64(good.CgroupMountID),
			encodeRunRootUint64(good.CgroupDevice), encodeRunRootUint64(good.CgroupInode)}
	}
	invalid := []struct {
		name         string
		nonce, epoch any
	}{
		{name: "null nonce", nonce: nil, epoch: good.RunEpoch},
		{name: "blob nonce", nonce: []byte(good.LaunchNonce), epoch: good.RunEpoch},
		{name: "uppercase nonce", nonce: "ABCDEF0123456789ABCDEF0123456789", epoch: good.RunEpoch},
		{name: "wrong length nonce", nonce: "0123456789abcdef", epoch: good.RunEpoch},
		{name: "invalid character nonce", nonce: "0123456789abcdef0123456789abcdef!", epoch: good.RunEpoch},
		{name: "embedded nul suffix nonce", nonce: good.LaunchNonce + "\x00suffix", epoch: good.RunEpoch},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ledger.conn.ExecContext(ctx, insert, validArgs(tc.nonce, tc.epoch)...); err == nil {
				t.Fatal("malformed nonce accepted by SQLite schema")
			}
		})
	}
}

func TestRunRootLedgerConsumeBootstrapPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reopen.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	request := testBootstrapHandoffRequest(7)
	if consumed, err := ledger.ConsumeBootstrap(ctx, request); err != nil || !consumed {
		t.Fatalf("consume = %v, %v", consumed, err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err = NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if consumed, err := ledger.ConsumeBootstrap(ctx, request); err != nil || consumed {
		t.Fatalf("reopened same-generation replay = %v, %v", consumed, err)
	}
}

func TestRunRootLedgerConsumeBootstrapFencesOnPersistenceError(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	if _, err := ledger.conn.ExecContext(ctx, `CREATE TRIGGER fail_bootstrap_insert BEFORE INSERT ON pilot_bootstrap_handoffs BEGIN SELECT RAISE(ABORT,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if consumed, err := ledger.ConsumeBootstrap(ctx, testBootstrapHandoffRequest(1)); err == nil || consumed {
		t.Fatalf("injected ledger failure = %v, %v", consumed, err)
	}
	if ledger.conn != nil || !ledger.fenced {
		t.Fatal("ledger instance was not fenced after an uncertain write failure")
	}
	if consumed, err := ledger.ConsumeBootstrap(ctx, testBootstrapHandoffRequest(2)); err == nil || consumed {
		t.Fatalf("fenced ledger accepted follow-up = %v, %v", consumed, err)
	}
}

func TestRunRootLedgerBootstrapLogIsCorrelatedAndRedacted(t *testing.T) {
	ctx := context.Background()
	var output bytes.Buffer
	logger := obs.NewWithSink(&output)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := NewRunRootLedgerWithLogger(ctx, db, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	request := testBootstrapHandoffRequest(41)
	if consumed, err := ledger.ConsumeBootstrap(ctx, request); err != nil || !consumed {
		t.Fatalf("consume = %v, %v", consumed, err)
	}
	line := output.String()
	for _, required := range []string{`"trace_id"`, `"module":"pilot_runroot_ledger"`, `"event":"bootstrap.consume"`,
		`"stage":"bootstrap.consume"`, `"elapsed_ms"`, `"generation":41`, `"result":"accepted"`, `"error_class":""`} {
		if !strings.Contains(line, required) {
			t.Fatalf("log missing %s: %s", required, line)
		}
	}
	for _, private := range []string{request.LaunchNonce, request.RunEpoch, `"pid":1234`, `"uid":1001`, `"start_time_ticks"`, `"cgroup_inode"`} {
		if strings.Contains(line, private) {
			t.Fatalf("log exposed private value %q: %s", private, line)
		}
	}
}

func TestRunRootLedgerBootstrapCommitFailureOutcomesAreAtomicAndFenced(t *testing.T) {
	for _, tc := range []struct {
		name      string
		commitRow bool
		wantRows  int
	}{
		{name: "failure before commit", wantRows: 0},
		{name: "commit succeeded but driver returned error", commitRow: true, wantRows: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "commit-fault.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := NewRunRootLedger(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			ledger.commitBootstrap = func(ctx context.Context, conn *sql.Conn) error {
				if tc.commitRow {
					if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
						return err
					}
				}
				return errors.New("injected commit outcome")
			}
			request := testBootstrapHandoffRequest(1)
			if consumed, err := ledger.ConsumeBootstrap(ctx, request); err == nil || consumed {
				t.Fatalf("injected commit fault = %v, %v", consumed, err)
			}
			if ledger.conn != nil || !ledger.fenced {
				t.Fatal("ledger was not fenced after commit uncertainty")
			}
			_ = db.Close()
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			reopened, err := NewRunRootLedger(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var count int
			if err = reopened.conn.QueryRowContext(ctx, `SELECT count(*) FROM pilot_bootstrap_handoffs`).Scan(&count); err != nil || count != tc.wantRows {
				t.Fatalf("durable rows = %d, want %d, err=%v", count, tc.wantRows, err)
			}
			if tc.commitRow {
				if consumed, err := reopened.ConsumeBootstrap(ctx, request); err != nil || consumed {
					t.Fatalf("committed-but-unacknowledged replay = %v, %v", consumed, err)
				}
			}
		})
	}
}

func TestRunRootLedgerBootstrapHighWaterIsSharedAcrossPools(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared-highwater.db")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	first, err := NewRunRootLedger(ctx, db1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	second, err := NewRunRootLedger(ctx, db2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	newer := testBootstrapHandoffRequest(2)
	newer.LaunchNonce = "1123456789abcdef0123456789abcdef"
	newer.RunEpoch = "bb23456789abcdef0123456789abcdef"
	if _, err = second.conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = second.conn.ExecContext(ctx, `INSERT INTO pilot_bootstrap_handoffs(generation,launch_nonce,run_epoch,pid,uid,start_time_ticks,pidns_device,pidns_inode,cgroup_mount_id,cgroup_device,cgroup_inode) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		encodeRunRootUint64(newer.Generation), newer.LaunchNonce, newer.RunEpoch, int64(newer.PID), int64(newer.UID),
		encodeRunRootUint64(newer.StartTimeTicks), encodeRunRootUint64(newer.PIDNSDevice), encodeRunRootUint64(newer.PIDNSInode),
		encodeRunRootUint64(newer.CgroupMountID), encodeRunRootUint64(newer.CgroupDevice), encodeRunRootUint64(newer.CgroupInode)); err != nil {
		t.Fatal(err)
	}
	stale := testBootstrapHandoffRequest(1)
	stale.LaunchNonce = "2123456789abcdef0123456789abcdef"
	stale.RunEpoch = "cb23456789abcdef0123456789abcdef"
	type consumeResult struct {
		consumed bool
		err      error
	}
	staleResult := make(chan consumeResult, 1)
	staleStarted := make(chan struct{})
	go func() {
		close(staleStarted)
		consumed, err := first.ConsumeBootstrap(ctx, stale)
		staleResult <- consumeResult{consumed: consumed, err: err}
	}()
	<-staleStarted
	time.Sleep(75 * time.Millisecond)
	if _, err = second.conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	var result consumeResult
	select {
	case result = <-staleResult:
	case <-time.After(2 * time.Second):
		t.Fatal("stale-generation consumer did not finish after the newer writer committed")
	}
	if result.err != nil || result.consumed {
		t.Fatalf("stale generation from independent pool = %v, %v", result.consumed, result.err)
	}
}

func TestRunRootLedgerBootstrapCancellationWhileWaitingForWriterFencesWithoutInsert(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "writer-cancel.db")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	owner, err := NewRunRootLedger(ctx, db1)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	contender, err := NewRunRootLedger(ctx, db2)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	if _, err = owner.conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = owner.conn.ExecContext(context.Background(), `ROLLBACK`) }()
	cancelCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	request := testBootstrapHandoffRequest(1)
	if consumed, err := contender.ConsumeBootstrap(cancelCtx, request); err == nil || consumed {
		t.Fatalf("writer-wait cancellation = %v, %v", consumed, err)
	}
	if !contender.fenced || contender.conn != nil {
		t.Fatal("canceled writer contender was not fenced")
	}
	var count int
	if err = owner.conn.QueryRowContext(ctx, `SELECT count(*) FROM pilot_bootstrap_handoffs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled contender inserted row: count=%d err=%v", count, err)
	}
}

func TestRunRootLedgerBootstrapBusyTimeoutIsInfrastructureFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "writer-busy.db")
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	owner, err := NewRunRootLedger(ctx, db1)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	contender, err := NewRunRootLedger(ctx, db2)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	if _, err = owner.conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = owner.conn.ExecContext(context.Background(), `ROLLBACK`) }()
	started := time.Now()
	if consumed, err := contender.ConsumeBootstrap(ctx, testBootstrapHandoffRequest(1)); err == nil || consumed {
		t.Fatalf("busy writer result = %v, %v", consumed, err)
	}
	if elapsed := time.Since(started); elapsed < 4*time.Second {
		t.Fatalf("writer busy timeout returned too early: %s", elapsed)
	}
	if !contender.fenced || contender.conn != nil {
		t.Fatal("busy timeout did not fence the contender")
	}
	var count int
	if err = owner.conn.QueryRowContext(ctx, `SELECT count(*) FROM pilot_bootstrap_handoffs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("busy contender inserted row: count=%d err=%v", count, err)
	}
}

func TestRunRootLedgerConcurrentFirstOpenCreatesOneCompleteSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-init.db")
	dbs := make([]*sql.DB, 2)
	for i := range dbs {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		dbs[i] = db
		defer db.Close()
	}
	start := make(chan struct{})
	ledgers := make([]*RunRootLedger, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range dbs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ledgers[i], errs[i] = NewRunRootLedger(ctx, dbs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent opener %d failed: %v", i, err)
		}
		defer ledgers[i].Close()
	}
	for _, ledger := range ledgers {
		if !validRunRootObjectInventory(ctx, ledger.conn) || validateRunRootLedgerSchema(ctx, ledger.conn) != nil {
			t.Fatal("concurrent initialization exposed incomplete schema")
		}
	}
}

func TestRunRootLedgerWALTransitionHonorsContextCancellation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal-cancel.db")
	holderDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holderDB.Close()
	if _, err = holderDB.ExecContext(ctx, `CREATE TABLE lock_probe(value INTEGER); INSERT INTO lock_probe VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	reader, err := holderDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var value int
	if err = reader.QueryRowContext(ctx, `SELECT value FROM lock_probe`).Scan(&value); err != nil {
		t.Fatal(err)
	}

	contenderDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer contenderDB.Close()
	openCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		ledger, openErr := NewRunRootLedger(openCtx, contenderDB)
		if ledger != nil {
			_ = ledger.Close()
		}
		result <- openErr
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case openErr := <-result:
		if openErr == nil {
			t.Fatal("WAL transition unexpectedly succeeded while the reader lock was held")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WAL transition did not honor context cancellation")
	}
}

func TestRunRootLedgerWALTransitionReturnsPreCanceledContext(t *testing.T) {
	ledger := newTestRunRootLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := enableRunRootLedgerWAL(ctx, ledger.conn); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled WAL transition error = %v", err)
	}
}

func TestRunRootLedgerRejectsV1SchemaWithoutMutation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE run_root_ledger_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),schema_version INTEGER NOT NULL);
INSERT INTO run_root_ledger_meta(singleton,schema_version) VALUES(1,1);
CREATE TABLE legacy_marker(value TEXT); INSERT INTO legacy_marker VALUES('keep');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("v1 schema accepted: %v", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='pilot_bootstrap_handoffs'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("v1 schema was mutated: table count=%d err=%v", count, err)
	}
	var version int
	if err = db.QueryRowContext(ctx, `SELECT schema_version FROM run_root_ledger_meta WHERE singleton=1`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("v1 meta changed: version=%d err=%v", version, err)
	}
	var userVersion int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil || userVersion != 0 {
		t.Fatalf("v1 user_version changed: version=%d err=%v", userVersion, err)
	}
	_ = db.Close()
}

func TestRunRootLedgerStartupRejectsTamperedBootstrapStorageType(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tampered-row.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	request := testBootstrapHandoffRequest(1)
	_, err = ledger.conn.ExecContext(ctx, `INSERT INTO pilot_bootstrap_handoffs(generation,launch_nonce,run_epoch,pid,uid,start_time_ticks,pidns_device,pidns_inode,cgroup_mount_id,cgroup_device,cgroup_inode) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"0000000000000001", request.LaunchNonce, request.RunEpoch, int64(request.PID), int64(request.UID),
		encodeRunRootUint64(request.StartTimeTicks), encodeRunRootUint64(request.PIDNSDevice), encodeRunRootUint64(request.PIDNSInode),
		encodeRunRootUint64(request.CgroupMountID), encodeRunRootUint64(request.CgroupDevice), encodeRunRootUint64(request.CgroupInode))
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("startup accepted text-typed generation: %v", err)
	}
}

func newTestRunRootLedger(t *testing.T) *RunRootLedger {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ledger, err := NewRunRootLedger(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

func testRunRootLedgerRequest() RunRootLedgerRequest {
	return RunRootLedgerRequest{
		Epoch: RunEpoch("0123456789abcdef0123456789abcdef"), RequestID: "req-01", PrincipalRef: "student-1",
		SessionKeyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", GatewayGeneration: 1,
		ProcessIdentity: [6]uint64{1, 2, 3, 4, 5, 6}, SchemaVersion: 1, PayloadDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func createdRunRootEvidence(t *testing.T, req RunRootLedgerRequest) RunRootLedgerEvidence {
	t.Helper()
	kind := "run-root-created-v1"
	identity := []any{uint64(10), uint64(11), uint64(12), uint64(10), uint64(13), uint64(12)}
	process := []any{uint64(1), uint64(2), uint64(3), uint64(4), uint64(5), uint64(6)}
	payload := []any{string(req.Epoch), req.RequestID, req.GatewayGeneration, process, uint64(22), uint64(33), int64(0700), true, true,
		uint64(44), uint64(55), int64(0710), true, true, true, true}
	evidence, err := NewRunRootLedgerEvidence(kind, identity, payload)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestRunRootLedgerPrepareClaimAndCompletedReceiptAreDurableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	prepared, created, err := ledger.Prepare(ctx, req)
	if err != nil || !created || prepared.State != LedgerPrepared || !prepared.LeaseHeld {
		t.Fatalf("prepare = %+v, created=%v, err=%v", prepared, created, err)
	}
	retry, created, err := ledger.Prepare(ctx, req)
	if err != nil || created || retry.State != LedgerPrepared || !retry.LeaseHeld {
		t.Fatalf("idempotent prepare = %+v, created=%v, err=%v", retry, created, err)
	}
	if _, _, err = ledger.Prepare(ctx, func() RunRootLedgerRequest { r := req; r.RequestID = "other"; return r }()); !errors.Is(err, errRunRootLedgerConflict) {
		t.Fatalf("second request reused permanent epoch: %v", err)
	}
	owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
	if err != nil || owner != 1 {
		t.Fatalf("claim = %d, %v", owner, err)
	}
	if _, err = ledger.Claim(ctx, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("second owner claim was not rejected: %v", err)
	}
	wrongBinding := req
	wrongBinding.GatewayGeneration++
	if err = ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalCreated, "ok", createdRunRootEvidence(t, wrongBinding)); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("receipt from a different gateway generation accepted: %v", err)
	}
	if err = ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalCreated, "ok", createdRunRootEvidence(t, req)); err != nil {
		t.Fatalf("finish created: %v", err)
	}
	completedRetry, created, err := ledger.Prepare(ctx, req)
	if err != nil || created || completedRetry.State != LedgerCompleted || len(completedRetry.EvidenceReceipt) == 0 {
		t.Fatalf("completed idempotent prepare = %+v, created=%v, err=%v", completedRetry, created, err)
	}
	result, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || result.State != LedgerCompleted || result.AllocationState != LedgerCompleted ||
		result.RunTerminalState != "active" || !result.LeaseHeld || result.OwnerGeneration != 1 || len(result.EvidenceReceipt) == 0 {
		t.Fatalf("completed lookup = %+v, err=%v", result, err)
	}
	receiptA, err := newRunRootOperationReceipt(req, owner, "completed", createdRunRootEvidence(t, req))
	if err != nil {
		t.Fatal(err)
	}
	receiptB, err := newRunRootOperationReceipt(req, owner, "completed", createdRunRootEvidence(t, req))
	if err != nil || receiptA.ID() != receiptB.ID() || string(receiptA.Bytes()) != string(receiptB.Bytes()) {
		t.Fatalf("operation receipt is not deterministic: %v", err)
	}
	var envelope []any
	if err = cbor.Unmarshal(result.EvidenceReceipt, &envelope); err != nil || len(envelope) != 2 {
		t.Fatalf("invalid persisted OperationEvidenceReceipt envelope: %v", err)
	}
	fields, ok := envelope[0].([]any)
	if !ok || len(fields) != 10 || fields[2] != "run_root_create" || fields[8] != "completed" || fields[9] != uint64(1) || envelope[1] != receiptA.ID() {
		t.Fatalf("persisted receipt fields are incomplete or mismatched: %#v", envelope)
	}
	receiptFields, err := encodeRunRootCBOR(fields)
	if err != nil || runRootDigestHex(hashRunRootDomain("en-words-operation-evidence-v1\x00", receiptFields)) != receiptA.ID() {
		t.Fatalf("receipt digest does not bind its complete field array: %v", err)
	}
	if err = ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalCreated, "ok", createdRunRootEvidence(t, req)); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("terminal overwrite was not rejected: %v", err)
	}
}

func TestRunRootLedgerMutatorsRejectMissingContextIdentityAndClosedStore(t *testing.T) {
	ctx := context.Background()
	req := testRunRootLedgerRequest()
	var nilLedger *RunRootLedger
	if _, _, err := nilLedger.Prepare(ctx, req); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-ledger Prepare = %v", err)
	}
	ledger := newTestRunRootLedger(t)
	if _, _, err := ledger.Prepare(nil, req); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-context Prepare = %v", err)
	}
	invalid := req
	invalid.Epoch = RunEpoch("../invalid")
	if _, _, err := ledger.Prepare(ctx, invalid); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("invalid-request Prepare = %v", err)
	}
	if _, err := nilLedger.Claim(ctx, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-ledger Claim = %v", err)
	}
	if _, err := ledger.Claim(nil, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-context Claim = %v", err)
	}
	if _, err := ledger.Claim(ctx, RunEpoch("bad"), req.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("invalid-epoch Claim = %v", err)
	}
	if err := nilLedger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-ledger RecoverAfterRestart = %v", err)
	}
	if err := ledger.RecoverAfterRestart(nil); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-context RecoverAfterRestart = %v", err)
	}
	if err := nilLedger.Finish(ctx, req.Epoch, req.RequestID, 1, RunRootTerminalCreated, "ok", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-ledger Finish = %v", err)
	}
	if err := ledger.Finish(nil, req.Epoch, req.RequestID, 1, RunRootTerminalCreated, "ok", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("nil-context Finish = %v", err)
	}
	if err := ledger.Finish(ctx, req.Epoch, req.RequestID, 0, RunRootTerminalCreated, "ok", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("zero-owner Finish = %v", err)
	}
	if err := ledger.Finish(ctx, req.Epoch, req.RequestID, 1, RunRootTerminalCreated, "unknown", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("invalid-response Finish = %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Prepare(ctx, req); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("closed-store Prepare = %v", err)
	}
	if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("closed-store Claim = %v", err)
	}
	if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("closed-store RecoverAfterRestart = %v", err)
	}
	if err := ledger.Finish(ctx, req.Epoch, req.RequestID, 1, RunRootTerminalCreated, "ok", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("closed-store Finish = %v", err)
	}
}

func TestRunRootLedgerCollisionAndAmbiguousOutcomesKeepLeaseFenced(t *testing.T) {
	for _, outcome := range []RunRootTerminal{RunRootTerminalCollision, RunRootTerminalIndeterminate} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			evidence := RunRootLedgerEvidence{}
			if outcome == RunRootTerminalCollision {
				process := []any{uint64(1), uint64(2), uint64(3), uint64(4), uint64(5), uint64(6)}
				evidence, err = NewRunRootLedgerEvidence("run-root-create-collision-v1", []any{uint64(10), uint64(11), uint64(12)},
					[]any{string(req.Epoch), req.RequestID, req.GatewayGeneration, process, "mkdirat", "EEXIST", false})
				if err != nil {
					t.Fatal(err)
				}
			}
			response := "ipc_result_indeterminate"
			if outcome == RunRootTerminalCollision {
				response = "verified_policy_mismatch"
			}
			if err = ledger.Finish(ctx, req.Epoch, req.RequestID, owner, outcome, response, evidence); err != nil {
				t.Fatal(err)
			}
			retry, created, retryErr := ledger.Prepare(ctx, req)
			wantReceipt := outcome != RunRootTerminalIndeterminate
			if retryErr != nil || created || retry.LeaseHeld != true || (len(retry.EvidenceReceipt) > 0) != wantReceipt {
				t.Fatalf("terminal retry failed to return fenced durable result: %+v created=%v err=%v", retry, created, retryErr)
			}
			result, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
			wantOperation := LedgerIndeterminate
			if outcome == RunRootTerminalCollision {
				wantOperation = LedgerAbandoned
			}
			if err != nil || result.State != wantOperation || result.AllocationState != LedgerIndeterminate ||
				result.RunTerminalState != "indeterminate" || !result.LeaseHeld {
				t.Fatalf("fenced lookup = %+v, err=%v", result, err)
			}
		})
	}
}

func TestRunRootLedgerPrepareRejectsChangedImmutableAllocationBindings(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	base := testRunRootLedgerRequest()
	if _, created, err := ledger.Prepare(ctx, base); err != nil || !created {
		t.Fatalf("initial prepare = created %v, err %v", created, err)
	}
	mutations := []struct {
		name   string
		mutate func(*RunRootLedgerRequest)
	}{
		{name: "principal", mutate: func(r *RunRootLedgerRequest) { r.PrincipalRef = "different-principal" }},
		{name: "session hash", mutate: func(r *RunRootLedgerRequest) {
			r.SessionKeyHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		}},
		{name: "gateway generation", mutate: func(r *RunRootLedgerRequest) { r.GatewayGeneration++ }},
		{name: "process identity", mutate: func(r *RunRootLedgerRequest) { r.ProcessIdentity[5]++ }},
		{name: "payload digest", mutate: func(r *RunRootLedgerRequest) {
			r.PayloadDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.mutate(&changed)
			if _, created, err := ledger.Prepare(ctx, changed); created || !errors.Is(err, errRunRootLedgerConflict) {
				t.Fatalf("changed immutable binding accepted: created=%v err=%v", created, err)
			}
			var allocations, operations int
			if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&allocations); err != nil {
				t.Fatal(err)
			}
			if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_root_operations`).Scan(&operations); err != nil {
				t.Fatal(err)
			}
			if allocations != 1 || operations != 1 {
				t.Fatalf("conflicting retry mutated durable rows: allocations=%d operations=%d", allocations, operations)
			}
			processBytes, err := json.Marshal(base.ProcessIdentity)
			if err != nil {
				t.Fatal(err)
			}
			var allocationPrincipal, allocationSession, allocationDigest string
			var allocationGeneration uint64
			var allocationIdentity []byte
			var allocationSchema uint32
			if err := ledger.conn.QueryRowContext(ctx, `SELECT principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest FROM run_epoch_allocations WHERE run_epoch=?`, base.Epoch).
				Scan(&allocationPrincipal, &allocationSession, &allocationGeneration, &allocationIdentity, &allocationSchema, &allocationDigest); err != nil {
				t.Fatal(err)
			}
			if allocationPrincipal != base.PrincipalRef || allocationSession != base.SessionKeyHash || allocationGeneration != base.GatewayGeneration ||
				!bytes.Equal(allocationIdentity, processBytes) || allocationSchema != base.SchemaVersion || allocationDigest != base.PayloadDigest {
				t.Fatalf("conflicting retry changed allocation immutable bindings: principal=%q session=%q generation=%d identity=%x schema=%d digest=%q",
					allocationPrincipal, allocationSession, allocationGeneration, allocationIdentity, allocationSchema, allocationDigest)
			}
			var operationPrincipal, operationSession, operationDigest string
			var operationGeneration uint64
			var operationIdentity []byte
			var operationSchema uint32
			if err := ledger.conn.QueryRowContext(ctx, `SELECT principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest FROM run_root_operations WHERE run_epoch=? AND request_id=?`, base.Epoch, base.RequestID).
				Scan(&operationPrincipal, &operationSession, &operationGeneration, &operationIdentity, &operationSchema, &operationDigest); err != nil {
				t.Fatal(err)
			}
			if operationPrincipal != base.PrincipalRef || operationSession != base.SessionKeyHash || operationGeneration != base.GatewayGeneration ||
				!bytes.Equal(operationIdentity, processBytes) || operationSchema != base.SchemaVersion || operationDigest != base.PayloadDigest {
				t.Fatalf("conflicting retry changed operation immutable bindings: principal=%q session=%q generation=%d identity=%x schema=%d digest=%q",
					operationPrincipal, operationSession, operationGeneration, operationIdentity, operationSchema, operationDigest)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ledger.Prepare(canceled, testRunRootLedgerRequest()); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("canceled transaction context = %v", err)
	}
}

func TestRunRootLedgerRecoveryIsIdempotentBeforeAndAfterClaim(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecoverAfterRestart(ctx); err != nil {
		t.Fatalf("prepared recovery: %v", err)
	}
	if err := ledger.RecoverAfterRestart(ctx); err != nil {
		t.Fatalf("idempotent prepared recovery: %v", err)
	}
	prepared, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || prepared.State != LedgerPrepared || !prepared.LeaseHeld {
		t.Fatalf("prepared recovery changed state: %+v err=%v", prepared, err)
	}
	owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
	if err != nil || owner != 1 {
		t.Fatalf("claim after safe prepared recovery: owner=%d err=%v", owner, err)
	}
	if err := ledger.RecoverAfterRestart(ctx); err != nil {
		t.Fatalf("executing recovery: %v", err)
	}
	if err := ledger.RecoverAfterRestart(ctx); err != nil {
		t.Fatalf("idempotent indeterminate recovery: %v", err)
	}
	recovered, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || recovered.State != LedgerIndeterminate || recovered.AllocationState != LedgerIndeterminate || !recovered.LeaseHeld {
		t.Fatalf("executing recovery result = %+v err=%v", recovered, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := ledger.RecoverAfterRestart(canceled); !errors.Is(err, errRunRootLedgerCorrupt) && !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("canceled recovery context returned unexpected error: %v", err)
	}
}

func TestRunRootLedgerClaimRejectsMissingRowAndAllocationDrift(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	request := testRunRootLedgerRequest()
	if _, err := ledger.Claim(ctx, request.Epoch, request.RequestID); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("claim of missing operation = %v", err)
	}
	if _, _, err := ledger.Prepare(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state='completed',run_terminal_state='active' WHERE run_epoch=?`, request.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Claim(ctx, request.Epoch, request.RequestID); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("claim whose paired allocation left prepared state = %v", err)
	}
	var opState string
	if err := ledger.conn.QueryRowContext(ctx, `SELECT operation_state FROM run_root_operations WHERE run_epoch=?`, request.Epoch).Scan(&opState); err != nil || opState != string(LedgerPrepared) {
		t.Fatalf("failed CAS did not roll back operation state: %q, %v", opState, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ledger.Claim(canceled, request.Epoch, request.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("canceled claim context = %v", err)
	}
}

func TestRunRootLedgerFinishRejectsInvalidOwnerOutcomeAndCanceledCommit(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	request := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, request); err != nil {
		t.Fatal(err)
	}
	owner, err := ledger.Claim(ctx, request.Epoch, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	evidence := createdRunRootEvidence(t, request)
	if err := ledger.Finish(ctx, request.Epoch, request.RequestID, owner+1, RunRootTerminalCreated, "ok", evidence); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("wrong owner Finish = %v", err)
	}
	if err := ledger.Finish(ctx, request.Epoch, request.RequestID, owner, RunRootTerminal("unknown"), "ok", evidence); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("unknown outcome Finish = %v", err)
	}
	if err := ledger.Finish(ctx, request.Epoch, request.RequestID, owner, RunRootTerminalIndeterminate, "restart_required", evidence); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("indeterminate outcome accepted unrelated evidence = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := ledger.Finish(canceled, request.Epoch, request.RequestID, owner, RunRootTerminalIndeterminate, "restart_required", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("canceled Finish transaction = %v", err)
	}
	state, err := ledger.Lookup(ctx, request.Epoch, request.RequestID)
	if err != nil || state.State != LedgerExecuting || !state.LeaseHeld {
		t.Fatalf("canceled Finish mutated durable operation: %+v err=%v", state, err)
	}
	if err := ledger.Finish(ctx, request.Epoch, request.RequestID, owner, RunRootTerminalCreated, "ok", evidence); err != nil {
		t.Fatalf("valid Finish after rejected requests = %v", err)
	}
}

func TestRunRootLedgerPrepareRejectsOperationDriftAndCorruptPreparedResponse(t *testing.T) {
	for _, tc := range []struct {
		name, update string
		want         error
	}{
		{"operation binding drift", `UPDATE run_root_operations SET principal_ref='other-student'`, errRunRootLedgerConflict},
		{"prepared response corruption", `UPDATE run_root_operations SET response_code='unexpected'`, errRunRootLedgerCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.conn.ExecContext(ctx, tc.update+` WHERE run_epoch=?`, req.Epoch); err != nil {
				t.Fatal(err)
			}
			if _, created, err := ledger.Prepare(ctx, req); created || !errors.Is(err, tc.want) {
				t.Fatalf("Prepare after persisted operation corruption = created %v, err %v; want %v", created, err, tc.want)
			}
		})
	}
}

func TestRunRootLedgerPrepareRollsBackWhenOperationInsertIsRejected(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, err := ledger.conn.ExecContext(ctx, `CREATE TRIGGER reject_run_root_operation BEFORE INSERT ON run_root_operations BEGIN SELECT RAISE(ABORT,'injected test fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, created, err := ledger.Prepare(ctx, req); created || !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("Prepare with rejected operation insert = created %v, err %v", created, err)
	}
	var allocations, operations int
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&allocations); err != nil {
		t.Fatal(err)
	}
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_root_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if allocations != 0 || operations != 0 {
		t.Fatalf("failed Prepare did not roll back allocation: allocations=%d operations=%d", allocations, operations)
	}
}

func TestRunRootLedgerPrepareEnforcesPerPrincipalCapacity(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	_, err := ledger.conn.ExecContext(ctx, `WITH RECURSIVE seq(n) AS (
  SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 100000
)
INSERT INTO run_epoch_allocations(run_epoch,principal_ref,session_key_hash,gateway_generation,process_identity,schema_version,payload_digest,allocation_state,run_terminal_state,run_lease_held)
SELECT printf('%032x',n),'student-1',printf('%064x',n),1,x'010203040506',1,printf('%064x',n),'prepared','none',1 FROM seq`)
	if err != nil {
		t.Fatalf("construct exact per-principal capacity fixture: %v", err)
	}
	req := testRunRootLedgerRequest()
	if _, created, err := ledger.Prepare(ctx, req); created || !errors.Is(err, errRunRootLedgerFull) {
		t.Fatalf("Prepare beyond per-principal limit = created %v, err %v", created, err)
	}
	var allocations, operations int
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&allocations); err != nil {
		t.Fatal(err)
	}
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_root_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if allocations != 100000 || operations != 0 {
		t.Fatalf("capacity rejection mutated permanent rows: allocations=%d operations=%d", allocations, operations)
	}
}

func TestRunRootLedgerRecoverRejectsInconsistentAndOrphanRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(context.Context, *testing.T, *RunRootLedger, RunRootLedgerRequest)
	}{
		{
			name: "allocation and operation state mismatch",
			corrupt: func(ctx context.Context, t *testing.T, ledger *RunRootLedger, req RunRootLedgerRequest) {
				if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state='completed',run_terminal_state='active' WHERE run_epoch=?`, req.Epoch); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan operation",
			corrupt: func(ctx context.Context, t *testing.T, ledger *RunRootLedger, req RunRootLedgerRequest) {
				if _, err := ledger.conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := ledger.conn.ExecContext(ctx, `DELETE FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			tc.corrupt(ctx, t, ledger, req)
			if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerCorrupt) {
				t.Fatalf("recovery of inconsistent durable rows = %v", err)
			}
		})
	}
}

func TestRunRootLedgerRecoverFailurePathsDoNotPartiallyFence(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
	}{
		{"allocation fence update rejected", `CREATE TRIGGER reject_recovery_allocation BEFORE UPDATE ON run_epoch_allocations BEGIN SELECT RAISE(ABORT,'injected test fault'); END`},
		{"operation fence update rejected", `CREATE TRIGGER reject_recovery_operation BEFORE UPDATE ON run_root_operations BEGIN SELECT RAISE(ABORT,'injected test fault'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.conn.ExecContext(ctx, tc.trigger); err != nil {
				t.Fatal(err)
			}
			if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerInvalid) {
				t.Fatalf("recovery with rejected fence update = %v", err)
			}
			var operation, allocation string
			if err := ledger.conn.QueryRowContext(ctx, `SELECT operation_state FROM run_root_operations WHERE run_epoch=?`, req.Epoch).Scan(&operation); err != nil {
				t.Fatal(err)
			}
			if err := ledger.conn.QueryRowContext(ctx, `SELECT allocation_state FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch).Scan(&allocation); err != nil {
				t.Fatal(err)
			}
			if operation != string(LedgerExecuting) || allocation != string(LedgerExecuting) {
				t.Fatalf("failed recovery partially fenced state: operation=%q allocation=%q", operation, allocation)
			}
		})
	}
}

func TestRunRootLedgerRecoverRejectsCorruptOwnerGenerationType(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "negative-owner-generation.db")
	dbBeforeRestart, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbBeforeRestart.Close() })
	dbBeforeRestart.SetMaxOpenConns(1)
	ledger, err := NewRunRootLedger(ctx, dbBeforeRestart)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_root_operations SET owner_generation=-1 WHERE run_epoch=?`, req.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dbBeforeRestart.Close(); err != nil {
		t.Fatal(err)
	}
	dbAfterRestart, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbAfterRestart.Close() })
	dbAfterRestart.SetMaxOpenConns(1)
	recoveredLedger, err := NewRunRootLedger(ctx, dbAfterRestart)
	if err != nil {
		t.Fatalf("reopening schema-valid corrupted ledger: %v", err)
	}
	t.Cleanup(func() { _ = recoveredLedger.Close() })
	operationsBefore := snapshotRunRootLedgerRows(t, recoveredLedger.conn, `SELECT * FROM run_root_operations WHERE run_epoch=?`, req.Epoch)
	allocationsBefore := snapshotRunRootLedgerRows(t, recoveredLedger.conn, `SELECT * FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch)
	if err := recoveredLedger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("recovery accepted a negative owner generation after reopen: %v", err)
	}
	var owner int64
	var operationState, allocationState string
	var leaseHeld int
	var operations, allocations int
	if err := recoveredLedger.conn.QueryRowContext(ctx, `SELECT owner_generation,operation_state FROM run_root_operations WHERE run_epoch=?`, req.Epoch).Scan(&owner, &operationState); err != nil {
		t.Fatal(err)
	}
	if err := recoveredLedger.conn.QueryRowContext(ctx, `SELECT allocation_state,run_lease_held FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch).Scan(&allocationState, &leaseHeld); err != nil {
		t.Fatal(err)
	}
	if err := recoveredLedger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_root_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := recoveredLedger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&allocations); err != nil {
		t.Fatal(err)
	}
	if owner != -1 || operationState != string(LedgerExecuting) || allocationState != string(LedgerExecuting) || leaseHeld != 1 || operations != 1 || allocations != 1 {
		t.Fatalf("failed recovery mutated corrupt durable rows: owner=%d operation=%q allocation=%q lease=%d ops=%d allocations=%d", owner, operationState, allocationState, leaseHeld, operations, allocations)
	}
	if operationsAfter := snapshotRunRootLedgerRows(t, recoveredLedger.conn, `SELECT * FROM run_root_operations WHERE run_epoch=?`, req.Epoch); !reflect.DeepEqual(operationsAfter, operationsBefore) {
		t.Fatalf("failed recovery changed operation row bytes: before=%#v after=%#v", operationsBefore, operationsAfter)
	}
	if allocationsAfter := snapshotRunRootLedgerRows(t, recoveredLedger.conn, `SELECT * FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch); !reflect.DeepEqual(allocationsAfter, allocationsBefore) {
		t.Fatalf("failed recovery changed allocation row bytes: before=%#v after=%#v", allocationsBefore, allocationsAfter)
	}
}

func snapshotRunRootLedgerRows(t *testing.T, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) [][]any {
	t.Helper()
	rows, err := queryer.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRunRootLedgerRecoverRejectsCorruptCompletedGenerationEncodingAfterRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "corrupt-generation.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Finish(ctx, req.Epoch, req.RequestID, 1, RunRootTerminalCreated, "ok", createdRunRootEvidence(t, req)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_epoch_allocations SET gateway_generation='bad' WHERE run_epoch=?`, req.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_root_operations SET gateway_generation='bad' WHERE run_epoch=?`, req.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dbAfterRestart, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbAfterRestart.Close() })
	dbAfterRestart.SetMaxOpenConns(1)
	recoveredLedger, err := NewRunRootLedger(ctx, dbAfterRestart)
	if err != nil {
		t.Fatalf("reopen schema-valid ledger with corrupt values: %v", err)
	}
	t.Cleanup(func() { _ = recoveredLedger.Close() })
	if err := recoveredLedger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerInvalid) && !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("recovery accepted nonnumeric generation encoding: %v", err)
	}
}

func TestRunRootLedgerMutatorsFailClosedWhenPinnedSQLConnDies(t *testing.T) {
	for _, method := range []string{"Prepare", "Claim", "RecoverAfterRestart", "Finish"} {
		t.Run(method, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if err := ledger.conn.Close(); err != nil {
				t.Fatal(err)
			}
			switch method {
			case "Prepare":
				if _, created, err := ledger.Prepare(ctx, req); created || !errors.Is(err, errRunRootLedgerInvalid) {
					t.Fatalf("Prepare with closed pinned connection = created %v, err %v", created, err)
				}
			case "Claim":
				if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
					t.Fatalf("Claim with closed pinned connection = %v", err)
				}
			case "RecoverAfterRestart":
				if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerInvalid) {
					t.Fatalf("RecoverAfterRestart with closed pinned connection = %v", err)
				}
			case "Finish":
				if err := ledger.Finish(ctx, req.Epoch, req.RequestID, 1, RunRootTerminalIndeterminate, "restart_required", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerInvalid) {
					t.Fatalf("Finish with closed pinned connection = %v", err)
				}
			}
		})
	}
}

func TestRunRootLedgerClaimSQLFailurePathsAreAtomic(t *testing.T) {
	for _, tc := range []struct {
		name string
		ddl  string
	}{
		{"operation update rejected", `CREATE TRIGGER reject_operation_claim BEFORE UPDATE ON run_root_operations BEGIN SELECT RAISE(ABORT,'blocked'); END`},
		{"allocation update rejected", `CREATE TRIGGER reject_allocation_claim BEFORE UPDATE ON run_epoch_allocations BEGIN SELECT RAISE(ABORT,'blocked'); END`},
		{"owner row disappears", `CREATE TRIGGER remove_claimed_operation AFTER UPDATE ON run_root_operations BEGIN DELETE FROM run_root_operations WHERE run_epoch=NEW.run_epoch; END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.conn.ExecContext(ctx, tc.ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerInvalid) {
				t.Fatalf("Claim with SQL fault = %v", err)
			}
			var state string
			err := ledger.conn.QueryRowContext(ctx, `SELECT operation_state FROM run_root_operations WHERE run_epoch=?`, req.Epoch).Scan(&state)
			if err != nil || state != string(LedgerPrepared) {
				t.Fatalf("failed Claim changed operation state: %q err=%v", state, err)
			}
		})
	}
}

func TestRunRootLedgerFinishRejectsCorruptPersistedIdentityAndAllocationCAS(t *testing.T) {
	for _, mode := range []string{"invalid identity encoding", "allocation CAS mismatch"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid identity encoding" {
				if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_root_operations SET process_identity=? WHERE run_epoch=?`, []byte("not-json"), req.Epoch); err != nil {
					t.Fatal(err)
				}
				if err := ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalIndeterminate, "restart_required", RunRootLedgerEvidence{}); !errors.Is(err, errRunRootLedgerCorrupt) {
					t.Fatalf("Finish with malformed persisted identity = %v", err)
				}
				return
			}
			if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state='prepared',run_terminal_state='none' WHERE run_epoch=?`, req.Epoch); err != nil {
				t.Fatal(err)
			}
			if err := ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalCreated, "ok", createdRunRootEvidence(t, req)); !errors.Is(err, errRunRootLedgerCAS) {
				t.Fatalf("Finish with allocation CAS mismatch = %v", err)
			}
			var state string
			if err := ledger.conn.QueryRowContext(ctx, `SELECT operation_state FROM run_root_operations WHERE run_epoch=?`, req.Epoch).Scan(&state); err != nil || state != string(LedgerExecuting) {
				t.Fatalf("failed Finish was not rolled back: state=%q err=%v", state, err)
			}
		})
	}
}

func TestRunRootLedgerFinishRejectsMalformedEvidenceAndSQLWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
		corrupt       bool
		tamperItem    bool
	}{
		{name: "malformed source payload", corrupt: true},
		{name: "tampered prevalidated evidence item", tamperItem: true},
		{name: "operation receipt update rejected", trigger: `CREATE TRIGGER reject_finish_operation BEFORE UPDATE ON run_root_operations BEGIN SELECT RAISE(ABORT,'injected test fault'); END`},
		{name: "allocation terminal update rejected", trigger: `CREATE TRIGGER reject_finish_allocation BEFORE UPDATE ON run_epoch_allocations BEGIN SELECT RAISE(ABORT,'injected test fault'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ledger := newTestRunRootLedger(t)
			req := testRunRootLedgerRequest()
			if _, _, err := ledger.Prepare(ctx, req); err != nil {
				t.Fatal(err)
			}
			owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			evidence := createdRunRootEvidence(t, req)
			if tc.corrupt {
				evidence.payload = []any{"malformed"}
			} else if tc.tamperItem {
				evidence.item = runRootEvidenceItem{}
			} else if _, err := ledger.conn.ExecContext(ctx, tc.trigger); err != nil {
				t.Fatal(err)
			}
			if err := ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalCreated, "ok", evidence); !errors.Is(err, errRunRootLedgerInvalid) {
				t.Fatalf("Finish fault (%s) = %v", tc.name, err)
			}
			state, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
			if err != nil || state.State != LedgerExecuting || state.AllocationState != LedgerExecuting {
				t.Fatalf("failed Finish mutated durable state: %+v err=%v", state, err)
			}
		})
	}
}

func TestRunRootLedgerNoEffectReceiptDoesNotReleaseLeaseWithoutRunTerminalProof(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	parent := []any{uint64(10), uint64(11), uint64(12)}
	epochDigest := runRootBasenameDigest(string(req.Epoch))
	identity := []any{uint64(10), uint64(11), uint64(12), []any{"absent", parent, epochDigest, "ENOENT"}}
	process := []any{uint64(1), uint64(2), uint64(3), uint64(4), uint64(5), uint64(6)}
	payload := []any{string(req.Epoch), req.RequestID, req.GatewayGeneration, process, "mkdirat", "EACCES", true}
	evidence, err := NewRunRootLedgerEvidence("run-root-create-effect-absent-v1", identity, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalNoEffect, "verified_io_failed", evidence); err != nil {
		t.Fatal(err)
	}
	result, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || result.State != LedgerAbandoned || result.AllocationState != LedgerIndeterminate || result.RunTerminalState != "indeterminate" || !result.LeaseHeld {
		t.Fatalf("no-effect lookup released or unfenced lease: %+v, err=%v", result, err)
	}
}

func TestRunRootLedgerRejectsInvalidRequestBeforeMutation(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	bad := testRunRootLedgerRequest()
	bad.Epoch = RunEpoch("../unsafe")
	if _, _, err := ledger.Prepare(ctx, bad); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("invalid epoch error = %v", err)
	}
	var count int
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid request mutated ledger: count=%d err=%v", count, err)
	}
}

func TestRunRootLedgerRestartFencesExecutingAndLeavesPreparedResumable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pilot-control.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	req := testRunRootLedgerRequest()
	if _, _, err = ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
	if err != nil || owner != 1 {
		t.Fatalf("claim = %d, %v", owner, err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err = NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if err = ledger.RecoverAfterRestart(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || result.State != LedgerIndeterminate || result.AllocationState != LedgerIndeterminate || !result.LeaseHeld {
		t.Fatalf("recovered executing record = %+v, err=%v", result, err)
	}
	if _, err = ledger.Claim(ctx, req.Epoch, req.RequestID); !errors.Is(err, errRunRootLedgerCAS) {
		t.Fatalf("restart recovery allowed redispatch: %v", err)
	}
	if _, _, err = ledger.Prepare(ctx, req); err != nil {
		t.Fatalf("same-key retry should return durable fenced state, got %v", err)
	}
}

func TestRunRootLedgerAllowsOnlyOneHeldLeasePerPrincipalSession(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	first := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Epoch = RunEpoch("abcdef0123456789abcdef0123456789")
	second.RequestID = "req-02"
	if _, _, err := ledger.Prepare(ctx, second); !errors.Is(err, errRunRootLedgerConflict) {
		t.Fatalf("second held lease for same principal/session accepted: %v", err)
	}
	count := 0
	if err := ledger.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conflicting epoch mutated permanent allocations: count=%d err=%v", count, err)
	}
}

func TestRunRootLedgerConcurrentEpochsCannotBothAcquireSessionLease(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "shared-control.db")
	db1, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	ledger1, err := NewRunRootLedger(ctx, db1)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger1.Close()
	db2, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	ledger2, err := NewRunRootLedger(ctx, db2)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger2.Close()
	first, second := testRunRootLedgerRequest(), testRunRootLedgerRequest()
	second.Epoch, second.RequestID = RunEpoch("abcdef0123456789abcdef0123456789"), "req-02"
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, candidate := range []struct {
		ledger *RunRootLedger
		req    RunRootLedgerRequest
	}{{ledger1, first}, {ledger2, second}} {
		go func(ledger *RunRootLedger, req RunRootLedgerRequest) {
			<-start
			_, _, err := ledger.Prepare(ctx, req)
			results <- err
		}(candidate.ledger, candidate.req)
	}
	close(start)
	firstErr, secondErr := <-results, <-results
	created := 0
	for _, result := range []error{firstErr, secondErr} {
		if result == nil {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("successful concurrent allocations=%d, errors=(%v,%v)", created, firstErr, secondErr)
	}
	var count int
	if err = ledger1.conn.QueryRowContext(ctx, `SELECT count(*) FROM run_epoch_allocations WHERE run_lease_held=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("held leases=%d err=%v", count, err)
	}
}

func TestRunRootLedgerRestartRefusesCrossTableStateMismatch(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Claim(ctx, req.Epoch, req.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_epoch_allocations SET allocation_state='prepared' WHERE run_epoch=?`, req.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("recovery accepted inconsistent ledger: %v", err)
	}
	result, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || result.State != LedgerExecuting || result.AllocationState != LedgerPrepared || !result.LeaseHeld {
		t.Fatalf("corrupt ledger was mutated during failed recovery: %+v err=%v", result, err)
	}
}

func TestRunRootLedgerRestartRejectsImmutableBindingDrift(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_root_operations SET payload_digest=? WHERE run_epoch=?`, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", req.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("binding drift accepted: %v", err)
	}
	var state string
	if err := ledger.conn.QueryRowContext(ctx, `SELECT allocation_state FROM run_epoch_allocations WHERE run_epoch=?`, req.Epoch).Scan(&state); err != nil || state != string(LedgerPrepared) {
		t.Fatalf("failed recovery mutated allocation: state=%s err=%v", state, err)
	}
}

func TestRunRootLedgerRestartRejectsPreparedResponseCode(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.conn.ExecContext(ctx, `UPDATE run_root_operations SET response_code='ipc_result_indeterminate' WHERE run_epoch=?`, req.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("prepared response code accepted: %v", err)
	}
	record, err := ledger.Lookup(ctx, req.Epoch, req.RequestID)
	if err != nil || record.State != LedgerPrepared || record.AllocationState != LedgerPrepared || record.CachedResponse != "ipc_result_indeterminate" {
		t.Fatalf("failed recovery mutated invalid row: %+v err=%v", record, err)
	}
}

func TestRunRootLedgerRefusesIncompatibleSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE run_root_ledger_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), schema_version INTEGER NOT NULL); INSERT INTO run_root_ledger_meta VALUES(1,99)`); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("incompatible schema accepted: %v", err)
	}
	_ = db.Close()
}

func TestRunRootLedgerRefusesUnversionedLegacyTables(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE run_epoch_allocations(run_epoch TEXT PRIMARY KEY, principal_ref TEXT, session_key_hash TEXT, run_lease_held INTEGER); CREATE TABLE run_root_operations(run_epoch TEXT, request_id TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("legacy schema accepted: %v", err)
	}
	_ = db.Close()
}

func TestRunRootLedgerRefusesMalformedSameNameLeaseIndex(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bad-index.db"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DROP INDEX run_root_one_held_lease_per_session; CREATE UNIQUE INDEX run_root_one_held_lease_per_session ON run_epoch_allocations(principal_ref) WHERE run_lease_held=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("malformed same-name index accepted: %v", err)
	}
	_ = db.Close()
}

func TestRunRootLedgerRefusesLeaseIndexWithExtraPredicate(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bad-predicate.db"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DROP INDEX run_root_one_held_lease_per_session; CREATE UNIQUE INDEX run_root_one_held_lease_per_session ON run_epoch_allocations(principal_ref,session_key_hash) WHERE run_lease_held=1 AND 0=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("index with extra predicate accepted: %v", err)
	}
	_ = db.Close()
}

func TestRunRootLedgerRefusesAlteredTableCheckLiteral(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bad-check.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `PRAGMA writable_schema=ON; UPDATE sqlite_master SET sql=replace(sql,'prepared','preparedifnotexists') WHERE type='table' AND name='run_epoch_allocations'; PRAGMA writable_schema=OFF`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("altered table check accepted: %v", err)
	}
}

func TestRunRootLedgerRefusesCaseAlteredCheckLiteral(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "case-check.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewRunRootLedger(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `PRAGMA writable_schema=ON; UPDATE sqlite_master SET sql=replace(sql,'run_root_create','RUN_ROOT_CREATE') WHERE type='table' AND name='run_root_operations'; PRAGMA writable_schema=OFF`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = NewRunRootLedger(ctx, db); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("case-altered CHECK literal accepted: %v", err)
	}
}

func TestRunRootLedgerRejectsCorruptReceiptOnRetryAndRecovery(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	req := testRunRootLedgerRequest()
	if _, _, err := ledger.Prepare(ctx, req); err != nil {
		t.Fatal(err)
	}
	owner, err := ledger.Claim(ctx, req.Epoch, req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Finish(ctx, req.Epoch, req.RequestID, owner, RunRootTerminalCreated, "ok", createdRunRootEvidence(t, req)); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.conn.ExecContext(ctx, `UPDATE run_root_operations SET evidence_receipt=? WHERE run_epoch=?`, []byte{0xa0}, req.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ledger.Prepare(ctx, req); !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("corrupt cached receipt was returned: %v", err)
	}
	if err = ledger.RecoverAfterRestart(ctx); !errors.Is(err, errRunRootLedgerCorrupt) {
		t.Fatalf("recovery accepted corrupt receipt: %v", err)
	}
}

func TestRunRootLedgerPersistedReceiptValidationRejectsTampering(t *testing.T) {
	req := testRunRootLedgerRequest()
	evidence := createdRunRootEvidence(t, req)
	receipt, err := newRunRootOperationReceipt(req, 1, "completed", evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = validatePersistedRunRootOperationReceipt(receipt.Bytes(), req, 1, "completed", "ok"); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]any)
	}{
		{"operation id", func(f []any) { f[0] = []byte("wrong-operation-id") }},
		{"principal", func(f []any) { f[1] = "other-principal" }},
		{"operation kind", func(f []any) { f[2] = "other-operation" }},
		{"schema version", func(f []any) { f[3] = uint64(2) }},
		{"payload digest", func(f []any) { f[4] = strings.Repeat("c", 64) }},
		{"epoch", func(f []any) { f[5] = strings.Repeat("1", 32) }},
		{"owner generation", func(f []any) { f[6] = uint64(2) }},
		{"evidence kind", func(f []any) { f[7].([]any)[0].([]any)[0] = "unknown-evidence" }},
		{"evidence digest format", func(f []any) { f[7].([]any)[0].([]any)[1] = "BAD" }},
		{"outcome", func(f []any) { f[8] = "abandoned" }},
		{"receipt version", func(f []any) { f[9] = uint64(2) }},
		{"empty evidence list", func(f []any) { f[7] = []any{} }},
		{"wrong evidence item shape", func(f []any) { f[7] = []any{[]any{"run-root-created-v1"}} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var envelope []any
			if e := cbor.Unmarshal(receipt.Bytes(), &envelope); e != nil || len(envelope) != 2 {
				t.Fatalf("decode fixture receipt: %v", e)
			}
			fields, ok := envelope[0].([]any)
			if !ok {
				t.Fatal("fixture fields are not an array")
			}
			fields = append([]any(nil), fields...)
			// Clone the nested evidence vectors because several mutations target them.
			if items, ok := fields[7].([]any); ok && len(items) > 0 {
				copiedItems := append([]any(nil), items...)
				if item, ok := copiedItems[0].([]any); ok {
					copiedItems[0] = append([]any(nil), item...)
				}
				fields[7] = copiedItems
			}
			tc.mutate(fields)
			encodedFields, e := encodeRunRootCBOR(fields)
			if e != nil {
				t.Fatal(e)
			}
			id := runRootDigestHex(hashRunRootDomain("en-words-operation-evidence-v1\x00", encodedFields))
			encoded, e := encodeRunRootCBOR([]any{fields, id})
			if e != nil {
				t.Fatal(e)
			}
			if e = validatePersistedRunRootOperationReceipt(encoded, req, 1, "completed", "ok"); !errors.Is(e, errRunRootLedgerCorrupt) {
				t.Fatalf("tampered receipt accepted: %v", e)
			}
		})
	}

	for name, malformed := range map[string][]byte{
		"empty": nil, "oversize": make([]byte, maxRunRootLedgerReceiptBytes+1), "invalid CBOR": {0xff},
		"wrong envelope length": mustCBORForTest(t, []any{[]any{}}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePersistedRunRootOperationReceipt(malformed, req, 1, "completed", "ok"); !errors.Is(err, errRunRootLedgerCorrupt) {
				t.Fatalf("malformed receipt accepted: %v", err)
			}
		})
	}
}

func TestRunRootLedgerReceiptHelperBoundariesAndAbandonedResponses(t *testing.T) {
	for _, tc := range []struct {
		value    any
		expected uint64
		want     bool
	}{
		{uint64(9), 9, true}, {uint32(9), 9, true}, {int64(9), 9, true}, {int(9), 9, true},
		{int64(-1), 9, false}, {"9", 9, false}, {uint64(8), 9, false},
	} {
		if got := cborUintEquals(tc.value, tc.expected); got != tc.want {
			t.Errorf("cborUintEquals(%T(%v), %d) = %v, want %v", tc.value, tc.value, tc.expected, got, tc.want)
		}
	}
	if !validPersistedRunRootAbandonedResponse("verified_policy_mismatch", "run-root-create-collision-v1") ||
		validPersistedRunRootAbandonedResponse("verified_io_failed", "run-root-create-collision-v1") ||
		!validPersistedRunRootAbandonedResponse("verified_path_invalid", "run-root-create-effect-absent-v1") ||
		validPersistedRunRootAbandonedResponse("ok", "unknown") {
		t.Fatal("abandoned receipt response policy mismatch")
	}

	req := testRunRootLedgerRequest()
	process := []any{uint64(1), uint64(2), uint64(3), uint64(4), uint64(5), uint64(6)}
	evidence, err := NewRunRootLedgerEvidence("run-root-create-collision-v1", []any{uint64(10), uint64(11), uint64(12)},
		[]any{string(req.Epoch), req.RequestID, req.GatewayGeneration, process, "mkdirat", "EEXIST", false})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newRunRootOperationReceipt(req, 1, "abandoned", evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = validatePersistedRunRootOperationReceipt(receipt.Bytes(), req, 1, "abandoned", "verified_policy_mismatch"); err != nil {
		t.Fatalf("valid abandoned collision receipt rejected: %v", err)
	}
}

func TestRunRootLedgerOperationReceiptConstructorRejectsInvalidInputs(t *testing.T) {
	req := testRunRootLedgerRequest()
	evidence := createdRunRootEvidence(t, req)
	if _, err := NewRunRootLedgerEvidence("unsupported-evidence", []any{}, []any{}); !errors.Is(err, errRunRootEvidenceInvalid) {
		t.Fatalf("unsupported evidence kind accepted: %v", err)
	}
	tests := []struct {
		name     string
		request  RunRootLedgerRequest
		owner    uint64
		outcome  string
		evidence RunRootLedgerEvidence
	}{
		{"zero owner", req, 0, "completed", evidence},
		{"invalid evidence", req, 1, "completed", RunRootLedgerEvidence{}},
		{"request binding drift", func() RunRootLedgerRequest {
			altered := req
			altered.Epoch = RunEpoch(strings.Repeat("1", 32))
			return altered
		}(), 1, "completed", evidence},
		{"invalid outcome", req, 1, "unknown", evidence},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newRunRootOperationReceipt(tc.request, tc.owner, tc.outcome, tc.evidence); !errors.Is(err, errRunRootEvidenceInvalid) {
				t.Fatalf("invalid receipt input accepted: %v", err)
			}
		})
	}
}

func TestRunRootLedgerConstructorRejectsInvalidDependencies(t *testing.T) {
	ctx := context.Background()
	logger := obs.New(io.Discard)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "constructor-errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for name, openErr := range map[string]error{
		"nil context":  func() error { _, e := NewRunRootLedgerWithLogger(nil, db, logger); return e }(),
		"nil database": func() error { _, e := NewRunRootLedgerWithLogger(ctx, nil, logger); return e }(),
		"nil logger":   func() error { _, e := NewRunRootLedgerWithLogger(ctx, db, nil); return e }(),
	} {
		if !errors.Is(openErr, errRunRootLedgerInvalid) {
			t.Errorf("%s error = %v", name, openErr)
		}
	}
	closedDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = closedDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunRootLedgerWithLogger(ctx, closedDB, logger); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("closed database accepted: %v", err)
	}
	memoryDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer memoryDB.Close()
	if _, err = NewRunRootLedgerWithLogger(ctx, memoryDB, logger); !errors.Is(err, errRunRootLedgerInvalid) {
		t.Fatalf("non-WAL in-memory database accepted: %v", err)
	}
}

func TestRunRootLedgerIndexValidatorRejectsMetadataDrift(t *testing.T) {
	ctx := context.Background()
	ledger := newTestRunRootLedger(t)
	assertInvalid := func(name string) {
		t.Helper()
		if validRunRootIndex(ctx, ledger.conn, "run_root_operations", "run_root_one_operation_per_epoch", []string{"run_epoch"}, false, "") {
			t.Fatalf("index metadata drift accepted: %s", name)
		}
	}
	if !validRunRootIndex(ctx, ledger.conn, "run_root_operations", "run_root_one_operation_per_epoch", []string{"run_epoch"}, false, "") {
		t.Fatal("canonical operation index rejected")
	}
	if _, err := ledger.conn.ExecContext(ctx, `DROP INDEX run_root_one_operation_per_epoch`); err != nil {
		t.Fatal(err)
	}
	assertInvalid("missing")
	for _, tc := range []struct {
		name string
		ddl  string
	}{
		{"non-unique", `CREATE INDEX run_root_one_operation_per_epoch ON run_root_operations(run_epoch)`},
		{"wrong column count", `CREATE UNIQUE INDEX run_root_one_operation_per_epoch ON run_root_operations(request_id,run_epoch)`},
		{"wrong column", `CREATE UNIQUE INDEX run_root_one_operation_per_epoch ON run_root_operations(request_id)`},
		{"unexpected partial", `CREATE UNIQUE INDEX run_root_one_operation_per_epoch ON run_root_operations(run_epoch) WHERE request_id<>''`},
		{"altered definition", `CREATE UNIQUE INDEX run_root_one_operation_per_epoch ON run_root_operations(run_epoch DESC)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ledger.conn.ExecContext(ctx, `DROP INDEX IF EXISTS run_root_one_operation_per_epoch`); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.conn.ExecContext(ctx, tc.ddl); err != nil {
				t.Fatal(err)
			}
			assertInvalid(tc.name)
		})
	}
}

func mustCBORForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := encodeRunRootCBOR(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

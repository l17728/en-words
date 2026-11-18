//go:build cgo && pilot_e2e

package pilotsupervisor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/obs"
	"github.com/mattn/go-sqlite3"
)

func init() {
	sql.Register("pilot_busy_test", &sqlite3.SQLiteDriver{})
}

func TestRunRootLedgerBootstrapHighWaterIsSharedAcrossPools(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared-highwater.db")
	db1, err := sql.Open("pilot_busy_test", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	first, err := NewRunRootLedger(ctx, db1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	db2, err := sql.Open("pilot_busy_test", path)
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
	staleDone := make(chan struct{})
	busyEntered := make(chan int, 1)
	releaseBusy := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBusy) }) }
	if err := first.conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterBusyHandler(func(retry int) bool {
			select {
			case busyEntered <- retry:
			default:
			}
			<-releaseBusy
			return true
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		release()
		select {
		case <-staleDone:
		case <-time.After(3 * time.Second):
			t.Error("stale consumer did not join before closing its SQL connections")
		}
	}()
	go func() {
		defer close(staleDone)
		consumed, err := first.ConsumeBootstrap(ctx, stale)
		staleResult <- consumeResult{consumed: consumed, err: err}
	}()
	select {
	case retry := <-busyEntered:
		if retry != 0 {
			t.Errorf("first SQLITE_BUSY retry count=%d, want 0", retry)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SQLite busy handler was not reached before releasing the newer writer")
	}
	if _, err = second.conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	release()
	var result consumeResult
	select {
	case result = <-staleResult:
	case <-time.After(2 * time.Second):
		t.Fatal("stale-generation consumer did not finish after the newer writer committed")
	}
	if result.err != nil || result.consumed {
		t.Fatalf("stale generation from independent pool = %v, %v", result.consumed, result.err)
	}
	rows, err := first.conn.QueryContext(ctx, `SELECT generation,launch_nonce,run_epoch FROM pilot_bootstrap_handoffs ORDER BY generation`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotGeneration []byte
	var gotNonce, gotEpoch string
	if !rows.Next() {
		t.Fatalf("expected durable generation %d; row error=%v", newer.Generation, rows.Err())
	}
	if err := rows.Scan(&gotGeneration, &gotNonce, &gotEpoch); err != nil {
		t.Fatal(err)
	}
	if generation := decodeRunRootUint64(gotGeneration); generation != newer.Generation || gotNonce != newer.LaunchNonce || gotEpoch != newer.RunEpoch {
		t.Fatalf("durable high-water row=(%d,%q,%q), want newer generation only", generation, gotNonce, gotEpoch)
	}
	if rows.Next() {
		t.Fatal("stale consumer inserted an extra durable ledger row")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("test_id=runroot.stale_highwater busy_retry=0 gate_reached=true action_released=true outcome=stale_rejected durable_rows=1 durable_generation=%d", newer.Generation)
}

func TestRunRootLedgerWALTransitionHonorsContextCancellation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal-cancel.db")
	holderDB, err := sql.Open("pilot_busy_test", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holderDB.Close()
	initialMode := setAndReadJournalMode(t, ctx, holderDB, `PRAGMA journal_mode=DELETE`)
	if !strings.EqualFold(initialMode, "delete") {
		t.Fatalf("initial journal mode=%q, want delete", initialMode)
	}
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
	var holderMode string
	if err = reader.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&holderMode); err != nil || !strings.EqualFold(holderMode, "delete") {
		t.Fatalf("holder mode during pinned read transaction=%q err=%v, want delete", holderMode, err)
	}

	contenderDB, err := sql.Open("pilot_busy_test", path)
	if err != nil {
		t.Fatal(err)
	}
	defer contenderDB.Close()
	contender, err := contenderDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	contenderMode := readJournalMode(t, ctx, contender)
	if !strings.EqualFold(contenderMode, "delete") {
		t.Fatalf("contender mode before WAL transition=%q, want delete", contenderMode)
	}
	openCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	done := make(chan struct{})
	busyEntered := make(chan int, 1)
	releaseBusy := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBusy) }) }
	retryWaitEntered := make(chan struct{})
	releaseRetryWait := make(chan struct{})
	var retryWaitReleaseOnce sync.Once
	releaseWait := func() { retryWaitReleaseOnce.Do(func() { close(releaseRetryWait) }) }
	fakeTimer := &neverFiringRunRootWALRetryTimer{ready: make(chan time.Time)}
	var retryWaitObserverCount atomic.Int32
	var busyCallbackCount atomic.Int32
	if err := contender.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterBusyHandler(func(retry int) bool {
			busyCallbackCount.Add(1)
			select {
			case busyEntered <- retry:
			default:
			}
			<-releaseBusy
			return false
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		release()
		releaseWait()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("WAL transition did not join before closing its SQL connections")
		}
	}()
	go func() {
		defer close(done)
		openErr := enableRunRootLedgerWALWithWaitHooks(openCtx, contender,
			func(time.Duration) runRootLedgerWALRetryTimer { return fakeTimer },
			func() {
				retryWaitObserverCount.Add(1)
				close(retryWaitEntered)
				<-releaseRetryWait
			},
		)
		result <- openErr
	}()
	select {
	case retry := <-busyEntered:
		if retry != 0 {
			t.Errorf("first SQLITE_BUSY retry count=%d, want 0", retry)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SQLite busy handler was not reached during WAL transition")
	}
	release()
	select {
	case <-retryWaitEntered:
		// The observer is reachable only after SQLite returned BUSY/LOCKED and
		// the retry timer was created. The fake timer never fires, so the
		// cancellation select has exactly one ready case after release.
	case <-time.After(3 * time.Second):
		select {
		case openErr := <-result:
			t.Fatalf("WAL transition returned before retry barrier: err=%v busy_callbacks=%d", openErr, busyCallbackCount.Load())
		default:
			t.Fatal("WAL retry did not reach the named pre-wait barrier")
		}
	}
	cancel()
	releaseWait()
	select {
	case openErr := <-result:
		if !errors.Is(openErr, context.Canceled) {
			t.Fatalf("WAL transition error=%v, want context.Canceled", openErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WAL transition did not honor context cancellation")
	}
	if count := busyCallbackCount.Load(); count != 1 {
		t.Fatalf("busy callback count=%d, want exactly one reached callback", count)
	}
	if count := fakeTimer.stopCount.Load(); count != 1 {
		t.Fatalf("retry timer Stop count=%d, want one cancellation cleanup", count)
	}
	if count := retryWaitObserverCount.Load(); count != 1 {
		t.Fatalf("retry wait observer count=%d, want exactly one reached retry", count)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatalf("release holder read transaction: %v", err)
	}
	finalMode := readJournalMode(t, context.Background(), contender)
	if !strings.EqualFold(finalMode, "delete") {
		t.Fatalf("journal mode after canceled WAL transition=%q, want delete", finalMode)
	}
	var schemaCount int
	if err := holderDB.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type IN ('table','trigger','view') AND name IN ('run_epoch_allocations','run_root_operations','run_root_ledger_meta','pilot_bootstrap_handoffs','pilot_bootstrap_handoffs_no_update','pilot_bootstrap_handoffs_no_delete')`).Scan(&schemaCount); err != nil || schemaCount != 0 {
		t.Fatalf("RunRoot ledger schema objects after canceled WAL transition=%d err=%v, want 0", schemaCount, err)
	}
	t.Logf("test_id=runroot.wal_cancel initial_mode=%s holder_mode=%s contender_mode=%s busy_callback_count=%d retry_wait_observer_count=%d fake_timer_stop_count=%d context_case_selected=true gate_reached=true gate_released=true outcome=context_canceled final_mode=%s ledger_schema_objects=%d", initialMode, holderMode, contenderMode, busyCallbackCount.Load(), retryWaitObserverCount.Load(), fakeTimer.stopCount.Load(), finalMode, schemaCount)
}

func TestRunRootLedgerSQLiteBusyClassificationUsesNativeDriverError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "busy-value", err: sqlite3.Error{Code: sqlite3.ErrBusy}, want: true},
		{name: "locked-value", err: sqlite3.Error{Code: sqlite3.ErrLocked}, want: true},
		{name: "busy-pointer", err: &sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusy.Extend(1)}, want: true},
		{name: "locked-pointer", err: &sqlite3.Error{Code: sqlite3.ErrLocked, ExtendedCode: sqlite3.ErrLocked.Extend(1)}, want: true},
		{name: "wrapped-busy", err: fmt.Errorf("pragma journal mode: %w", sqlite3.Error{Code: sqlite3.ErrBusy}), want: true},
		{name: "constraint", err: sqlite3.Error{Code: sqlite3.ErrConstraint}, want: false},
	}
	for _, tc := range cases {
		if got := runRootLedgerSQLiteBusyOrLocked(tc.err); got != tc.want {
			t.Errorf("%s classified retryable=%v, want %v (error=%v)", tc.name, got, tc.want, tc.err)
		}
	}
	if runRootLedgerSQLiteBusyOrLocked(errors.New("database is locked")) {
		t.Fatal("text-only SQLite lookalike classified as retryable")
	}
}

func TestRunRootLedgerDeadlineTakesPrecedenceOverLateSQLiteBusy(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("pilot_busy_test", filepath.Join(t.TempDir(), "wal-deadline-precedence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if mode := setAndReadJournalMode(t, ctx, db, `PRAGMA journal_mode=DELETE`); !strings.EqualFold(mode, "delete") {
		t.Fatalf("initial journal mode=%q, want delete", mode)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	busy := sqlite3.Error{Code: sqlite3.ErrBusy}
	if !runRootLedgerSQLiteBusyOrLocked(busy) {
		t.Fatal("typed SQLite BUSY fixture was not classified as retryable")
	}
	var queryCalls, retryTimerCalls atomic.Int32
	var postDeadline time.Time
	result := enableRunRootLedgerWALWithHooks(ctx, conn, runRootLedgerWALHooks{
		queryWAL: func(queryCtx context.Context) (string, error) {
			queryCalls.Add(1)
			deadline, ok := queryCtx.Deadline()
			if !ok {
				t.Fatal("WAL attempt did not have a bounded deadline")
			}
			// The SQL seam returns a native-driver BUSY error just after the
			// effective deadline, before the context timer callback is allowed
			// to fire. This deterministically exercises the precedence branch.
			postDeadline = deadline.Add(time.Nanosecond)
			return "", busy
		},
		now: func() time.Time { return postDeadline },
		newTimer: func(time.Duration) runRootLedgerWALRetryTimer {
			retryTimerCalls.Add(1)
			return &neverFiringRunRootWALRetryTimer{ready: make(chan time.Time)}
		},
	})
	if !errors.Is(result, context.DeadlineExceeded) {
		t.Fatalf("WAL deadline result=%v, want context.DeadlineExceeded", result)
	}
	if got := queryCalls.Load(); got != 1 {
		t.Fatalf("WAL query attempts=%d, want exactly one (BUSY must not be retried after deadline)", got)
	}
	if got := retryTimerCalls.Load(); got != 0 {
		t.Fatalf("retry timers created=%d, want none after deadline", got)
	}
	if mode := readJournalMode(t, ctx, conn); !strings.EqualFold(mode, "delete") {
		t.Fatalf("journal mode after late BUSY=%q, want delete", mode)
	}
	var schemaCount int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type IN ('table','trigger','view') AND name IN ('run_epoch_allocations','run_root_operations','run_root_ledger_meta','pilot_bootstrap_handoffs','pilot_bootstrap_handoffs_no_update','pilot_bootstrap_handoffs_no_delete')`).Scan(&schemaCount); err != nil || schemaCount != 0 {
		t.Fatalf("RunRoot ledger schema objects after late BUSY=%d err=%v, want 0", schemaCount, err)
	}
	t.Logf("test_id=runroot.wal_late_busy deadline_checked=true typed_busy_classified=true query_attempts=%d retry_timers=%d outcome=deadline_exceeded final_mode=%s ledger_schema_objects=%d", queryCalls.Load(), retryTimerCalls.Load(), "delete", schemaCount)
}

func TestRunRootLedgerCancellationTakesPrecedenceOverLateSQLiteBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	busy := sqlite3.Error{Code: sqlite3.ErrBusy}
	var queryCalls, retryTimerCalls atomic.Int32
	result := enableRunRootLedgerWALWithHooks(ctx, nil, runRootLedgerWALHooks{
		queryWAL: func(context.Context) (string, error) {
			queryCalls.Add(1)
			cancel()
			return "", busy
		},
		newTimer: func(time.Duration) runRootLedgerWALRetryTimer {
			retryTimerCalls.Add(1)
			return &neverFiringRunRootWALRetryTimer{ready: make(chan time.Time)}
		},
	})
	if !errors.Is(result, context.Canceled) {
		t.Fatalf("WAL cancellation result=%v, want context.Canceled", result)
	}
	if got := queryCalls.Load(); got != 1 {
		t.Fatalf("WAL query attempts=%d, want exactly one", got)
	}
	if got := retryTimerCalls.Load(); got != 0 {
		t.Fatalf("retry timers created=%d, want none after cancellation", got)
	}
	t.Logf("test_id=runroot.wal_late_busy_cancel typed_busy_classified=%v query_attempts=%d retry_timers=%d outcome=context_canceled", runRootLedgerSQLiteBusyOrLocked(busy), queryCalls.Load(), retryTimerCalls.Load())
}

type neverFiringRunRootWALRetryTimer struct {
	ready     chan time.Time
	stopCount atomic.Int32
}

func (timer *neverFiringRunRootWALRetryTimer) C() <-chan time.Time { return timer.ready }

func (timer *neverFiringRunRootWALRetryTimer) Stop() bool {
	timer.stopCount.Add(1)
	return true
}

type trackedSQLiteConnector struct {
	dsn               string
	closed            chan struct{}
	walAttempted      *atomic.Bool
	walResult         *atomic.Value
	walBusy           *atomic.Bool
	contenderMode     *atomic.Value
	busyEntered       chan struct{}
	busyCallbacks     *atomic.Int32
	busyReturnedFalse *atomic.Bool
}

func (c *trackedSQLiteConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := (&sqlite3.SQLiteDriver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	conn := raw.(*sqlite3.SQLiteConn)
	mode, modeErr := readSQLiteDriverJournalMode(conn)
	if modeErr != nil || !strings.EqualFold(mode, "delete") {
		_ = conn.Close()
		if modeErr != nil {
			return nil, fmt.Errorf("contender journal-mode preflight: %w", modeErr)
		}
		return nil, fmt.Errorf("contender journal-mode preflight=%q, want delete", mode)
	}
	if c.contenderMode != nil {
		c.contenderMode.Store(mode)
	}
	return &trackedSQLiteConn{SQLiteConn: conn, closed: c.closed, walAttempted: c.walAttempted,
		walResult: c.walResult, walBusy: c.walBusy, busyEntered: c.busyEntered,
		busyCallbacks: c.busyCallbacks, busyReturnedFalse: c.busyReturnedFalse}, nil
}
func (c *trackedSQLiteConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }

type trackedSQLiteConn struct {
	*sqlite3.SQLiteConn
	closed            chan struct{}
	once              sync.Once
	walAttempted      *atomic.Bool
	walResult         *atomic.Value
	walBusy           *atomic.Bool
	busyEntered       chan struct{}
	busyCallbacks     *atomic.Int32
	busyReturnedFalse *atomic.Bool
}

func (c *trackedSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.walAttempted != nil && strings.EqualFold(strings.TrimSpace(query), "PRAGMA journal_mode=WAL") {
		c.walAttempted.Store(true)
		if c.busyEntered != nil {
			if err := c.SQLiteConn.RegisterBusyHandler(func(int) bool {
				if c.busyCallbacks != nil {
					c.busyCallbacks.Add(1)
				}
				select {
				case c.busyEntered <- struct{}{}:
				default:
				}
				if c.busyReturnedFalse != nil {
					c.busyReturnedFalse.Store(true)
				}
				return false
			}); err != nil {
				return nil, err
			}
		}
	}
	rows, err := c.SQLiteConn.QueryContext(ctx, query, args)
	if c.walAttempted != nil && strings.EqualFold(strings.TrimSpace(query), "PRAGMA journal_mode=WAL") && c.walResult != nil {
		if err != nil {
			c.walResult.Store("query-error:" + err.Error())
		} else {
			c.walResult.Store("rows-open")
		}
	}
	if err != nil && c.walAttempted != nil && strings.EqualFold(strings.TrimSpace(query), "PRAGMA journal_mode=WAL") && c.walBusy != nil {
		if runRootLedgerSQLiteBusyOrLocked(err) {
			c.walBusy.Store(true)
		}
	}
	if err != nil || c.walAttempted == nil || !strings.EqualFold(strings.TrimSpace(query), "PRAGMA journal_mode=WAL") {
		return rows, err
	}
	first := make([]driver.Value, len(rows.Columns()))
	firstErr := rows.Next(first)
	if firstErr != nil {
		if firstErr != io.EOF && c.walBusy != nil {
			var sqliteErr sqlite3.Error
			busy := errors.As(firstErr, &sqliteErr) && (int(sqliteErr.Code)&0xff == 5 || int(sqliteErr.Code)&0xff == 6)
			if busy || runRootLedgerSQLiteBusyOrLocked(firstErr) {
				c.walBusy.Store(true)
			}
		}
		if c.walResult != nil {
			c.walResult.Store("first-error:" + firstErr.Error())
		}
	}
	if firstErr == nil && len(first) > 0 && c.walResult != nil {
		c.walResult.Store(fmt.Sprint(first[0]))
	}
	return &trackedSQLiteRows{Rows: rows, walResult: c.walResult, walBusy: c.walBusy, first: first, firstErr: firstErr, firstPending: true}, nil
}

type trackedSQLiteRows struct {
	driver.Rows
	walResult    *atomic.Value
	walBusy      *atomic.Bool
	first        []driver.Value
	firstErr     error
	firstPending bool
}

func (r *trackedSQLiteRows) Next(dest []driver.Value) error {
	if r.firstPending {
		r.firstPending = false
		if r.firstErr == nil {
			copy(dest, r.first)
		}
		return r.firstErr
	}
	err := r.Rows.Next(dest)
	if err != nil && r.walBusy != nil {
		r.walBusy.Store(runRootLedgerSQLiteBusyOrLocked(err))
	}
	if err == nil && len(dest) > 0 && r.walResult != nil {
		switch mode := dest[0].(type) {
		case string:
			r.walResult.Store(mode)
		case []byte:
			r.walResult.Store(string(mode))
		default:
			r.walResult.Store(fmt.Sprint(mode))
		}
	}
	return err
}

func (c *trackedSQLiteConn) Close() error {
	err := c.SQLiteConn.Close()
	if err == nil {
		c.once.Do(func() { close(c.closed) })
	}
	return err
}

type journalModeQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readJournalMode(t *testing.T, ctx context.Context, query journalModeQuery) string {
	t.Helper()
	var mode string
	if err := query.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read SQLite journal mode: %v", err)
	}
	return mode
}

func setAndReadJournalMode(t *testing.T, ctx context.Context, query journalModeQuery, statement string) string {
	t.Helper()
	var mode string
	if err := query.QueryRowContext(ctx, statement).Scan(&mode); err != nil {
		t.Fatalf("set SQLite journal mode with %q: %v", statement, err)
	}
	return mode
}

func readSQLiteDriverJournalMode(conn *sqlite3.SQLiteConn) (string, error) {
	rows, err := conn.Query(`PRAGMA journal_mode`, nil)
	if err != nil {
		return "", err
	}
	values := make([]driver.Value, len(rows.Columns()))
	readErr := rows.Next(values)
	closeErr := rows.Close()
	if readErr != nil {
		if closeErr != nil {
			return "", errors.Join(readErr, closeErr)
		}
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(values) == 0 {
		return "", errors.New("journal mode query returned no columns")
	}
	switch mode := values[0].(type) {
	case string:
		return mode, nil
	case []byte:
		return string(mode), nil
	default:
		return "", fmt.Errorf("journal mode query returned %T", values[0])
	}
}

func TestRunRootLedgerConstructorDiscardsConnectionAfterWALInitFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "constructor-wal-failure.db")
	holderDB, err := sql.Open("pilot_busy_test", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holderDB.Close()
	initialMode := setAndReadJournalMode(t, ctx, holderDB, `PRAGMA journal_mode=DELETE`)
	if !strings.EqualFold(initialMode, "delete") {
		t.Fatalf("initial journal mode=%q, want delete", initialMode)
	}
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
	var holderMode string
	if err = reader.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&holderMode); err != nil || !strings.EqualFold(holderMode, "delete") {
		t.Fatalf("holder mode during pinned read transaction=%q err=%v, want delete", holderMode, err)
	}
	closed := make(chan struct{})
	walAttempted := &atomic.Bool{}
	walResult := &atomic.Value{}
	walBusy := &atomic.Bool{}
	contenderMode := &atomic.Value{}
	busyEntered := make(chan struct{}, 1)
	busyCallbacks := &atomic.Int32{}
	busyReturnedFalse := &atomic.Bool{}
	db := sql.OpenDB(&trackedSQLiteConnector{dsn: path, closed: closed, walAttempted: walAttempted, walResult: walResult,
		walBusy: walBusy, contenderMode: contenderMode, busyEntered: busyEntered,
		busyCallbacks: busyCallbacks, busyReturnedFalse: busyReturnedFalse})
	defer db.Close()
	ledger, initErr := NewRunRootLedgerWithLogger(ctx, db, obs.New(io.Discard))
	if initErr == nil || ledger != nil {
		t.Fatalf("ledger initialization under reader lock returned ledger=%v err=%v", ledger, initErr)
	}
	if !walAttempted.Load() {
		t.Fatal("constructor failed before attempting the WAL transition; disposal case did not reach its target stage")
	}
	if !walBusy.Load() {
		t.Fatalf("WAL transition did not fail with SQLITE_BUSY/LOCKED; step result=%v", walResult.Load())
	}
	select {
	case <-busyEntered:
	default:
		t.Fatal("constructor WAL busy callback was not observed")
	}
	if callbacks := busyCallbacks.Load(); callbacks == 0 || !busyReturnedFalse.Load() {
		t.Fatalf("constructor busy callback count=%d returned_false=%v; want positive count and false", callbacks, busyReturnedFalse.Load())
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("constructor did not discard its pinned connection after WAL initialization failure")
	}
	if err := reader.Rollback(); err != nil {
		t.Fatalf("release holder read transaction: %v", err)
	}
	finalMode := readJournalMode(t, ctx, holderDB)
	if !strings.EqualFold(finalMode, "delete") {
		t.Fatalf("journal mode after constructor failure=%q, want delete", finalMode)
	}
	var schemaCount int
	if err := holderDB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type IN ('table','trigger','view') AND name IN ('run_epoch_allocations','run_root_operations','run_root_ledger_meta','pilot_bootstrap_handoffs','pilot_bootstrap_handoffs_no_update','pilot_bootstrap_handoffs_no_delete')`).Scan(&schemaCount); err != nil || schemaCount != 0 {
		t.Fatalf("RunRoot ledger schema objects after constructor failure=%d err=%v, want 0", schemaCount, err)
	}
	actualContenderMode, _ := contenderMode.Load().(string)
	actualWALResult, _ := walResult.Load().(string)
	t.Logf("test_id=runroot.constructor_discard initial_mode=%s holder_mode=%s contender_mode=%s busy_callback_count=%d busy_callback_reached=true busy_callback_returned_false=%v wal_result=%q wal_busy=%v outcome=wal_lock_rejected connection_discarded=true final_mode=%s ledger_schema_objects=%d", initialMode, holderMode, actualContenderMode, busyCallbacks.Load(), busyReturnedFalse.Load(), actualWALResult, walBusy.Load(), finalMode, schemaCount)
}

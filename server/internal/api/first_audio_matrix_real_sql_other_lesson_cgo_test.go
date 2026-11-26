//go:build cgo

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const firstAudioOtherLessonChildMarker = "ENWORDS_FIRST_AUDIO_OTHER_LESSON_CHILD"
const firstAudioZeroRowChildMarker = "ENWORDS_FIRST_AUDIO_ZERO_ROW_CHILD"
const firstAudioCorruptProjectionChildMarker = "ENWORDS_FIRST_AUDIO_CORRUPT_PROJECTION_CHILD"

func TestFirstAudioMatrixRealSQLRejectsOtherLessonUpdate(t *testing.T) {
	if os.Getenv(firstAudioOtherLessonChildMarker) == "1" {
		testFirstAudioOtherLessonUpdateChild(t, false, false)
		return
	}

	childEnv := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, firstAudioOtherLessonChildMarker+"=") {
			childEnv = append(childEnv, entry)
		}
	}
	childEnv = append(childEnv, firstAudioOtherLessonChildMarker+"=1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFirstAudioMatrixRealSQLRejectsOtherLessonUpdate$", "-test.v")
	command.Env = childEnv
	output := &firstAudioCappedChildOutput{limit: 256 << 10}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatalf("isolated other-lesson child was not reaped: err=%v output=%s", err, output.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("isolated other-lesson child exceeded 30-second deadline and was terminated/reaped: %v; output=%s", ctx.Err(), output.String())
	}
	if err != nil {
		t.Fatalf("isolated other-lesson child failed: %v; output=%s", err, output.String())
	}
	t.Log(output.String())
}

func TestFirstAudioMatrixRealSQLRejectsZeroRowUpdate(t *testing.T) {
	if os.Getenv(firstAudioZeroRowChildMarker) == "1" {
		testFirstAudioOtherLessonUpdateChild(t, true, false)
		return
	}
	childEnv := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, firstAudioZeroRowChildMarker+"=") {
			childEnv = append(childEnv, entry)
		}
	}
	childEnv = append(childEnv, firstAudioZeroRowChildMarker+"=1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFirstAudioMatrixRealSQLRejectsZeroRowUpdate$", "-test.v")
	command.Env = childEnv
	output := &firstAudioCappedChildOutput{limit: 256 << 10}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatalf("isolated zero-row child was not reaped: err=%v output=%s", err, output.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("isolated zero-row child exceeded 30-second deadline and was terminated/reaped: %v; output=%s", ctx.Err(), output.String())
	}
	if err != nil {
		t.Fatalf("isolated zero-row child failed: %v; output=%s", err, output.String())
	}
	t.Log(output.String())
}

func TestFirstAudioMatrixRealSQLRejectsCorruptedExpectedProjection(t *testing.T) {
	if os.Getenv(firstAudioCorruptProjectionChildMarker) == "1" {
		testFirstAudioOtherLessonUpdateChild(t, false, true)
		return
	}
	childEnv := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, firstAudioCorruptProjectionChildMarker+"=") {
			childEnv = append(childEnv, entry)
		}
	}
	childEnv = append(childEnv, firstAudioCorruptProjectionChildMarker+"=1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFirstAudioMatrixRealSQLRejectsCorruptedExpectedProjection$", "-test.v")
	command.Env = childEnv
	output := &firstAudioCappedChildOutput{limit: 256 << 10}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatalf("isolated corrupted-projection child was not reaped: err=%v output=%s", err, output.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("isolated corrupted-projection child exceeded 30-second deadline and was terminated/reaped: %v; output=%s", ctx.Err(), output.String())
	}
	if err != nil {
		t.Fatalf("isolated corrupted-projection child failed: %v; output=%s", err, output.String())
	}
	t.Log(output.String())
}

type firstAudioOtherLessonSnapshot struct {
	schemaSHA string
	objects   []firstAudioSQLiteObject
	rows      [][]firstAudioSnapshotCell
}

func captureFirstAudioOtherLessonSnapshot(path string) (firstAudioOtherLessonSnapshot, error) {
	db, err := gorm.Open(gormsqlite.Open(path), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return firstAudioOtherLessonSnapshot{}, err
	}
	pool, err := db.DB()
	if err != nil {
		return firstAudioOtherLessonSnapshot{}, err
	}
	defer pool.Close()
	manifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return firstAudioOtherLessonSnapshot{}, fmt.Errorf("read synthetic schema manifest: %w", err)
	}
	conn, err := pool.Conn(context.Background())
	if err != nil {
		return firstAudioOtherLessonSnapshot{}, err
	}
	defer conn.Close()
	columns := make([]string, len(firstAudioLessonProbeColumns))
	for i, column := range firstAudioLessonProbeColumns {
		columns[i] = quoteFirstAudioIdentifier(column)
	}
	rows, err := conn.QueryContext(context.Background(), "SELECT "+strings.Join(columns, ",")+" FROM main.lessons ORDER BY id")
	if err != nil {
		return firstAudioOtherLessonSnapshot{}, err
	}
	defer rows.Close()
	snapshot := firstAudioOtherLessonSnapshot{schemaSHA: manifest.SHA256, objects: append([]firstAudioSQLiteObject(nil), manifest.Objects...), rows: make([][]firstAudioSnapshotCell, 0, 2)}
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return firstAudioOtherLessonSnapshot{}, err
		}
		cells := make([]firstAudioSnapshotCell, len(values))
		for i, value := range values {
			cell, err := normalizeFirstAudioSnapshotCell(value)
			if err != nil {
				return firstAudioOtherLessonSnapshot{}, fmt.Errorf("normalize lessons.%s: %w", columns[i], err)
			}
			cells[i] = cell
		}
		snapshot.rows = append(snapshot.rows, cells)
	}
	if err := rows.Err(); err != nil {
		return firstAudioOtherLessonSnapshot{}, err
	}
	if len(snapshot.rows) != 2 || len(snapshot.rows[0]) != len(columns) || len(snapshot.rows[1]) != len(columns) {
		return firstAudioOtherLessonSnapshot{}, fmt.Errorf("synthetic snapshot rows=%d columns=%d, want two complete rows with %d columns", len(snapshot.rows), len(columns), len(columns))
	}
	return snapshot, nil
}

func testFirstAudioOtherLessonUpdateChild(t *testing.T, zeroRow, corruptProjection bool) {
	t.Helper()
	const (
		lessonA int64 = 701
		lessonB int64 = 702
	)
	path := filepath.Join(t.TempDir(), "other-lesson.db")
	setup, err := gorm.Open(gormsqlite.Open(path), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("open isolated synthetic setup database:", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		t.Fatal("get isolated setup pool:", err)
	}
	columns := make([]string, len(firstAudioLessonProbeColumns))
	for i, column := range firstAudioLessonProbeColumns {
		columns[i] = quoteFirstAudioIdentifier(column)
	}
	if err := setup.Exec("CREATE TABLE lessons (" + strings.Join(columns, ",") + ")").Error; err != nil {
		_ = setupPool.Close()
		t.Fatal("create synthetic dynamic-typing lessons table:", err)
	}
	insertSQL := "INSERT INTO lessons (" + strings.Join(columns, ",") + ") VALUES (" + strings.TrimRight(strings.Repeat("?,", len(columns)), ",") + ")"
	for _, values := range [][]any{firstAudioOtherLessonValues(lessonA, "lesson-a"), firstAudioOtherLessonValues(lessonB, "lesson-b")} {
		if err := setup.Exec(insertSQL, values...).Error; err != nil {
			_ = setupPool.Close()
			t.Fatal("seed target and decoy lesson rows before trigger installation:", err)
		}
	}
	if err := installFirstAudioLessonProbeTriggers(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install and validate approved trigger manifest:", err)
	}
	manifest, _, err := firstAudioReadSchemaManifest(setup)
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("read installed trigger manifest:", err)
	}
	triggerNames := make([]string, 0, 3)
	for _, object := range manifest.Objects {
		if object.Type == "trigger" {
			triggerNames = append(triggerNames, object.Name)
		}
	}
	wantTriggers := []string{"fa_lessons_guard_delete", "fa_lessons_guard_insert", "fa_lessons_guard_update"}
	if !reflect.DeepEqual(triggerNames, wantTriggers) {
		_ = setupPool.Close()
		t.Fatalf("synthetic fixture trigger inventory=%v, want exactly %v", triggerNames, wantTriggers)
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("close setup handles before candidate pool:", err)
	}
	baseline, err := captureFirstAudioOtherLessonSnapshot(path)
	if err != nil {
		t.Fatal("capture pre-probe synthetic schema and both-row snapshot:", err)
	}

	runID := "other_lesson_20261029"
	probeID := "probe_other_lesson"
	if zeroRow {
		runID, probeID = "zero_row_20261029", "probe_zero_row"
	} else if corruptProjection {
		runID, probeID = "corrupt_projection_20261029", "probe_corrupt_projection"
	}
	controller := newFirstAudioGuardController()
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: runID, currentLessonID: lessonA, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		t.Fatal("construct synthetic startup phase:", err)
	}
	transition, err := firstAudioTestPublishInitialStartup(controller, startup)
	if err != nil {
		t.Fatal("publish synthetic startup lifecycle:", err)
	}
	sessionRef := &firstAudioRealSQLProbeSessionRef{}
	var token []byte
	defer func() { zeroFirstAudioSecret(token) }()
	var challenge *firstAudioGuardProbeChallenge
	var authorizer *firstAudioRealSQLAuthorizer
	var trackedConnection *firstAudioTrackedSQLiteConn
	var registration firstAudioSQLiteConnectionRegistration
	var tokenCalls, rowGuardCalls, successfulRowObservations atomic.Uint64
	var callbackErr atomic.Value
	var callbackArgs atomic.Pointer[firstAudioCapturedTypedCallbackArgs]
	var authorizerEventsMu sync.Mutex
	var authorizerEvents []firstAudioRealSQLAuthorizerEvent
	driverName := fmt.Sprintf("first_audio_other_lesson_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(driverName, func(raw *sqlite3.SQLiteConn) error {
		var err error
		registration, err = controller.connections.Register(raw)
		if err != nil {
			return err
		}
		binding := firstAudioGuardProbeBinding{runID: runID, lessonID: lessonA, generation: 1, version: 2, connection: registration.ID, probeID: probeID}
		token, challenge, err = newFirstAudioPhysicalProbeChallenge(binding)
		if err != nil {
			return err
		}
		preflightSpec := firstAudioValidPreflightSpec(2)
		preflightSpec.runID = binding.runID
		preflightSpec.currentLessonID = binding.lessonID
		preflightSpec.connectionIDs = []string{binding.connection}
		preflightSpec.probeContracts = []firstAudioGuardProbeContract{challenge.Contract()}
		preflightSpec.rowContracts = []firstAudioGuardRowContract{{
			table: "lessons", operation: "UPDATE", identity: fmt.Sprintf("lesson:%d/conn:%s", lessonA, binding.connection),
			oldRow: []byte("bound-lesson-a-noop-projection"), newRow: []byte("bound-lesson-a-noop-projection"),
		}}
		preflightSpec.requirePhysicalProbes = true
		preflight, err := newFirstAudioGuardContext(preflightSpec)
		if err != nil {
			return err
		}
		if err := transition.Publish(context.Background(), preflight, firstAudioGuardTransitionEvidence{
			probeBindings: []firstAudioGuardProbeBinding{binding}, probeReceipts: []*firstAudioGuardProbeReceipt{challenge.receipt},
		}); err != nil {
			return fmt.Errorf("publish one-connection physical PREFLIGHT: %w", err)
		}
		if err := raw.RegisterFunc("first_audio_probe", func(probeToken []byte) bool {
			tokenCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.challenge != challenge || session.binding.connection != registration.ID || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() {
				callbackErr.Store("token_session_or_epoch_mismatch")
				return false
			}
			if err := challenge.ConsumeInTransaction(probeToken, session.binding, session.txEpoch.Load()); err != nil {
				callbackErr.Store("token_rejected")
				return false
			}
			return true
		}, true); err != nil {
			return err
		}
		if err := raw.RegisterFunc("first_audio_row_guard", func(args ...any) bool {
			rowGuardCalls.Add(1)
			session := sessionRef.session.Load()
			if session == nil || session.challenge != challenge || session.binding.lessonID != lessonA || session.binding.connection != registration.ID || session.tracked == nil || !session.tracked.txActive.Load() || session.txEpoch.Load() == 0 || session.txEpoch.Load() != session.tracked.txEpoch.Load() || len(args) != 72 {
				callbackErr.Store("row_guard_session_or_argument_mismatch")
				return false
			}
			callbackArgs.Store(&firstAudioCapturedTypedCallbackArgs{values: cloneFirstAudioSQLiteCallbackArgs(args)})
			actualOld := normalizeFirstAudioTypedSQLitePairs(args[:36])
			expected := session.expectedProjection.Load()
			if expected == nil {
				callbackErr.Store("expected_target_projection_missing")
				return false
			}
			if !reflect.DeepEqual(expected.values, actualOld) {
				if corruptProjection {
					callbackErr.Store("corrupt_expected_old_projection_mismatch")
				} else {
					callbackErr.Store("other_lesson_old_projection_mismatch")
				}
				return false
			}
			actualNew := normalizeFirstAudioTypedSQLitePairs(args[36:])
			if !reflect.DeepEqual(actualOld, actualNew) {
				callbackErr.Store("old_new_projection_mismatch")
				return false
			}
			if err := challenge.ObserveRowGuardInTransaction(session.binding, session.txEpoch.Load()); err != nil {
				callbackErr.Store("row_observation_rejected")
				return false
			}
			successfulRowObservations.Add(1)
			return true
		}, true); err != nil {
			return err
		}
		authorizer = newFirstAudioRealSQLAuthorizer(controller, sessionRef, firstAudioDatabaseSnapshot{})
		authorizer.onAuthorize = func(event firstAudioRealSQLAuthorizerEvent) {
			authorizerEventsMu.Lock()
			authorizerEvents = append(authorizerEvents, event)
			authorizerEventsMu.Unlock()
		}
		return authorizer.install(raw)
	}, controller.connections.Retire); err != nil {
		t.Fatal("register tracked other-lesson SQLite driver:", err)
	}
	pool, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal("open candidate SQLite pool:", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	pinned, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin candidate SQLite connection:", err)
	}
	t.Cleanup(func() { _ = pinned.Close() })
	if err := pinned.Raw(func(driverConn any) error {
		var ok bool
		trackedConnection, ok = driverConn.(*firstAudioTrackedSQLiteConn)
		if !ok || trackedConnection == nil || trackedConnection.raw == nil {
			return fmt.Errorf("unexpected tracked connection %T", driverConn)
		}
		if trackedConnection.raw != authorizer.raw || registration.ID != authorizer.registryID {
			return fmt.Errorf("candidate authorizer binding does not match actual raw/registry identity")
		}
		return nil
	}); err != nil {
		t.Fatal("resolve tracked candidate connection:", err)
	}
	authorizer.tracked, authorizer.pinned = trackedConnection, pinned
	session := &firstAudioRealSQLProbeSession{challenge: challenge, binding: challenge.binding, tracked: trackedConnection}
	sessionRef.session.Store(session)
	authorizer.probeChallenge.Store(challenge)
	registration, live := controller.connections.Lookup(trackedConnection.raw)
	if !live || !controller.connections.IsLive(challenge.binding.connection, trackedConnection.raw) || registration.ID != challenge.binding.connection {
		t.Fatal("candidate physical connection does not match the challenge registry identity")
	}
	t.Cleanup(func() {
		if !controller.connections.IsLive(challenge.binding.connection, trackedConnection.raw) {
			return
		}
		if cleanupErr := authorizer.terminalizeAndDiscardUncertainConnection(); !errors.Is(cleanupErr, errFirstAudioGuardProbeRejected) {
			t.Errorf("terminalize/discard candidate after early failure: %v", cleanupErr)
		}
	})

	var tx *sql.Tx
	if err := authorizer.withPermit(firstAudioSQLPermitProbeSQL, false, func() error {
		var beginErr error
		tx, beginErr = pinned.BeginTx(context.Background(), nil)
		return beginErr
	}); err != nil {
		t.Fatal("begin candidate PREFLIGHT transaction:", err)
	}
	authorizer.activeTx.Store(tx)
	epoch := trackedConnection.txEpoch.Load()
	if !trackedConnection.txActive.Load() || epoch == 0 {
		t.Fatal("candidate transaction did not begin at a tracked nonzero epoch")
	}
	authorizer.txEpoch.Store(epoch)
	session.txEpoch.Store(epoch)
	if err := challenge.BindTransactionEpoch(epoch); err != nil {
		t.Fatal("bind physical challenge to candidate transaction:", err)
	}
	authorizer.cleanupPreflight.Store(true)
	authorizer.cleanupAvailable.Store(true)
	if token == nil || len(token) != 32 {
		t.Fatal("physical challenge did not create a 32-byte token")
	}
	var tokenAccepted bool
	if err := authorizer.withPermit(firstAudioSQLPermitTokenConsumption, true, func() error {
		return tx.QueryRowContext(context.Background(), "SELECT first_audio_probe(?)", token).Scan(&tokenAccepted)
	}); err != nil || !tokenAccepted {
		t.Fatalf("lesson A token consumption accepted=%t err=%v callback=%v", tokenAccepted, err, callbackErr.Load())
	}
	zeroFirstAudioSecret(token)
	if tokenCalls.Load() != 1 || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeTokenConsumed {
		t.Fatalf("token callback count=%d receipt state=%d, want exactly one consumed token", tokenCalls.Load(), challenge.receipt.state.Load())
	}
	projection, err := authorizer.readTargetLessonProjection(tx, lessonA)
	if err != nil || len(projection) != 36 {
		t.Fatalf("lesson A typed projection values=%d err=%v", len(projection), err)
	}
	session.expectedProjection.Store(&firstAudioExpectedTypedProjection{values: normalizeFirstAudioTypedSQLitePairs(projection)})
	if corruptProjection {
		expected := session.expectedProjection.Load()
		if expected == nil {
			t.Fatal("expected lesson A id projection is nil")
		}
		if len(expected.values) != 36 {
			t.Fatalf("expected lesson A typed projection length=%d, want 36", len(expected.values))
		}
		if expected.values[0] != "integer" {
			t.Fatalf("expected lesson A id storage tag type=%T, want integer", expected.values[0])
		}
		originalID, ok := expected.values[1].(int64)
		if !ok || originalID != lessonA {
			t.Fatalf("expected lesson A id Go type=%T matches_seeded_id=%t", expected.values[1], ok && originalID == lessonA)
		}
		corrupted := append([]any(nil), expected.values...)
		corrupted[1] = lessonA + 1000
		for i := range expected.values {
			if i != 1 && !reflect.DeepEqual(expected.values[i], corrupted[i]) {
				t.Fatalf("projection corruption changed unrelated typed value index=%d", i)
			}
		}
		session.expectedProjection.Store(&firstAudioExpectedTypedProjection{values: corrupted})
	}
	denialsBefore := authorizer.denials.Load()
	authorizerEventsMu.Lock()
	eventStart := len(authorizerEvents)
	authorizerEventsMu.Unlock()
	updateTarget := lessonB
	if zeroRow {
		updateTarget = lessonB + 1000
	} else if corruptProjection {
		updateTarget = lessonA
	}
	sqliteCode := 0
	sqliteExtendedCode := 0
	var updateRows int64 = -1
	queryErr := authorizer.withPermit(firstAudioSQLPermitProbeSQL, true, func() error {
		result, err := tx.ExecContext(context.Background(), "UPDATE main.lessons SET id=id WHERE id=?", updateTarget)
		if err != nil {
			return err
		}
		updateRows, err = result.RowsAffected()
		return err
	})
	if zeroRow {
		if queryErr != nil || updateRows != 0 {
			t.Fatalf("nonexistent-row no-op UPDATE affected=%d err=%v, want 0 rows and no SQL error", updateRows, queryErr)
		}
		if callbackErr.Load() != nil || rowGuardCalls.Load() != 0 || successfulRowObservations.Load() != 0 {
			t.Fatalf("zero-row UPDATE unexpectedly invoked row guard: callback=%v calls=%d observations=%d", callbackErr.Load(), rowGuardCalls.Load(), successfulRowObservations.Load())
		}
	} else if corruptProjection {
		var sqliteErr sqlite3.Error
		if queryErr == nil || !errors.As(queryErr, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint || sqliteErr.ExtendedCode != sqlite3.ErrConstraintTrigger || !strings.Contains(queryErr.Error(), "first_audio_guard") {
			t.Fatalf("lesson A corrupted-projection UPDATE error=%T %v sqlite_code=%d sqlite_extended=%d want trigger constraint abort", queryErr, queryErr, sqliteErr.Code, sqliteErr.ExtendedCode)
		}
		sqliteCode = int(sqliteErr.Code)
		sqliteExtendedCode = int(sqliteErr.ExtendedCode)
		if callbackErr.Load() != "corrupt_expected_old_projection_mismatch" || rowGuardCalls.Load() != 1 || successfulRowObservations.Load() != 0 {
			t.Fatalf("corrupted expected projection result=%v callbacks=%d successful_observations=%d", callbackErr.Load(), rowGuardCalls.Load(), successfulRowObservations.Load())
		}
		args := callbackArgs.Load()
		if args == nil || len(args.values) != 72 || args.values[0] != "integer" {
			argCount, idTagType := 0, "<nil>"
			if args != nil {
				argCount = len(args.values)
				if len(args.values) != 0 {
					idTagType = fmt.Sprintf("%T", args.values[0])
				}
			}
			t.Fatalf("corrupted projection callback arg_count=%d id_tag_go_type=%s, want 72 arguments and string tag", argCount, idTagType)
		}
		oldID, ok := args.values[1].(int64)
		if !ok || oldID != lessonA {
			t.Fatalf("actual trigger OLD id Go type=%T matches_seeded_id=%t", args.values[1], ok && oldID == lessonA)
		}
	} else {
		var sqliteErr sqlite3.Error
		if queryErr == nil || !errors.As(queryErr, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint || !strings.Contains(queryErr.Error(), "first_audio_guard") {
			t.Fatalf("lesson B no-op UPDATE affected=%d error=%T %v sqlite_code=%d want trigger constraint abort containing first_audio_guard", updateRows, queryErr, queryErr, sqliteErr.Code)
		}
		sqliteCode = int(sqliteErr.Code)
		sqliteExtendedCode = int(sqliteErr.ExtendedCode)
		if callbackErr.Load() != "other_lesson_old_projection_mismatch" || rowGuardCalls.Load() != 1 || successfulRowObservations.Load() != 0 {
			t.Fatalf("wrong-row callback outcome=%v calls=%d successful_observations=%d", callbackErr.Load(), rowGuardCalls.Load(), successfulRowObservations.Load())
		}
		args := callbackArgs.Load()
		if args == nil || len(args.values) != 72 {
			t.Fatalf("captured row-guard args=%v, want exactly 72", args)
		}
		oldID, ok := args.values[1].(int64)
		if !ok || oldID != lessonB {
			t.Fatalf("row-guard OLD id=%T(%v), want decoy lesson id %d", args.values[1], args.values[1], lessonB)
		}
	}
	if authorizer.denials.Load() != denialsBefore || !trackedConnection.txActive.Load() || trackedConnection.raw.AutoCommit() {
		t.Fatalf("unexpected authorizer denial or transaction state: denials=%d before=%d txActive=%t autocommit=%t", authorizer.denials.Load(), denialsBefore, trackedConnection.txActive.Load(), trackedConnection.raw.AutoCommit())
	}
	authorizerEventsMu.Lock()
	var allowedTriggerCalls int
	var triggerEvent firstAudioRealSQLAuthorizerEvent
	for _, event := range authorizerEvents {
		if event.phase == firstAudioGuardPreflight && event.permit == firstAudioSQLPermitProbeSQL && event.action == sqlite3.SQLITE_FUNCTION && event.databaseName == "" && event.triggerName == "fa_lessons_guard_update" && event.arg1 == "" && event.arg2 == "first_audio_row_guard" {
			allowedTriggerCalls++
			triggerEvent = event
		}
	}
	authorizerEventsMu.Unlock()
	if allowedTriggerCalls != 1 || !triggerEvent.allowed {
		t.Fatalf("observed approved update-trigger function tuple count=%d event=%#v want one admitted tuple", allowedTriggerCalls, triggerEvent)
	}
	if zeroRow || corruptProjection {
		authorizerEventsMu.Lock()
		statementEvents := append([]firstAudioRealSQLAuthorizerEvent(nil), authorizerEvents[eventStart:]...)
		authorizerEventsMu.Unlock()
		tupleSummary := assertFirstAudioZeroRowAuthorizerMultiset(t, statementEvents)
		targetClass := "lesson_a"
		if zeroRow {
			targetClass = "nonexistent"
		}
		t.Logf("trace_id=%s module=first_audio_preflight event=update_authorizer_multiset_verified payload={statement_id:%q,target_class:%q,tuple_count:%d,denial_delta:0,tuple_multiset:%s}", runID, "UPDATE main.lessons SET id=id WHERE id=?", targetClass, len(statementEvents), strings.Join(tupleSummary, ";"))
	}
	if firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeTokenConsumed || challenge.receipt.completeFor(challenge.binding, challenge.digest) {
		t.Fatalf("failed update negative receipt state=%d complete=%t, want token-consumed and incomplete", challenge.receipt.state.Load(), challenge.receipt.completeFor(challenge.binding, challenge.digest))
	}
	if err := authorizer.rollbackProbeTransaction(); err != nil {
		t.Fatalf("rollback rejected failed update negative: %v", err)
	}
	if trackedConnection.txActive.Load() || !trackedConnection.raw.AutoCommit() || firstAudioGuardProbeReceiptState(challenge.receipt.state.Load()) != firstAudioGuardProbeFailed || challenge.receipt.completeFor(challenge.binding, challenge.digest) {
		t.Fatalf("rollback/invalidation state txActive=%t autocommit=%t receipt=%d complete=%t", trackedConnection.txActive.Load(), trackedConnection.raw.AutoCommit(), challenge.receipt.state.Load(), challenge.receipt.completeFor(challenge.binding, challenge.digest))
	}
	if challenge.receipt.denials.Load() == 0 {
		t.Fatal("failed probe rollback did not record denial evidence before JOURNEY publication")
	}
	if err := pinned.Close(); err != nil {
		t.Fatal("release rolled-back candidate pin:", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal("close candidate pool before observer readback:", err)
	}
	postRollback, err := captureFirstAudioOtherLessonSnapshot(path)
	if err != nil {
		t.Fatal("capture post-rollback synthetic schema and both-row snapshot:", err)
	}
	if !reflect.DeepEqual(baseline, postRollback) {
		t.Fatalf("post-rollback synthetic snapshot changed: baseline_sha=%s after_sha=%s", baseline.schemaSHA, postRollback.schemaSHA)
	}
	journeySpec := firstAudioValidJourneySpec(4, lessonA, 1, 1)
	journeySpec.runID = runID
	journey, err := newFirstAudioGuardContext(journeySpec)
	if err != nil {
		t.Fatal("bind JOURNEY candidate to current run:", err)
	}
	evidence := firstAudioGuardTransitionEvidence{
		probeBindings: []firstAudioGuardProbeBinding{challenge.binding}, probeReceipts: []*firstAudioGuardProbeReceipt{challenge.receipt},
		snapshotUnchanged: true, registryReconciled: true,
	}
	if err := transition.Publish(context.Background(), journey, evidence); !errors.Is(err, errFirstAudioGuardContextTransition) || !controller.IsTerminal() {
		t.Fatalf("JOURNEY transition after failed other-row receipt=%v terminal=%t, want rejected and terminal", err, controller.IsTerminal())
	}
	eventName := "other_lesson_update_rejected"
	if zeroRow {
		eventName = "zero_row_update_rejected"
	} else if corruptProjection {
		eventName = "corrupted_expected_projection_rejected"
	}
	affectedRows := "unavailable_after_trigger_error"
	if queryErr == nil {
		affectedRows = fmt.Sprintf("%d", updateRows)
	}
	corruptedField, corruptedStorageType := "", ""
	if corruptProjection {
		corruptedField, corruptedStorageType = "id", "integer"
	}
	t.Logf("trace_id=%s module=first_audio_preflight event=%s payload={statement_id:%q,trigger_phase:%q,trigger_permit:%d,trigger_action:%d,trigger_db:%q,trigger_source:%q,trigger_arg1:%q,trigger_arg2:%q,trigger_allowed:%t,affected_rows:%s,sqlite_code:%d,sqlite_extended_code:%d,corrupted_field:%q,corrupted_storage_type:%q,token_accepted:%t,token_calls:%d,row_guard_calls:%d,successful_observations:%d,mismatch:%v,receipt_state:%d,receipt_denials:%d,rollback_confirmed:true,autocommit:true,snapshot_unchanged:true,journey_rejected:true}", runID, eventName, "UPDATE main.lessons SET id=id WHERE id=?", triggerEvent.phase, triggerEvent.permit, triggerEvent.action, triggerEvent.databaseName, triggerEvent.triggerName, triggerEvent.arg1, triggerEvent.arg2, triggerEvent.allowed, affectedRows, sqliteCode, sqliteExtendedCode, corruptedField, corruptedStorageType, tokenAccepted, tokenCalls.Load(), rowGuardCalls.Load(), successfulRowObservations.Load(), callbackErr.Load(), challenge.receipt.state.Load(), challenge.receipt.denials.Load())
}

func assertFirstAudioZeroRowAuthorizerMultiset(t *testing.T, events []firstAudioRealSQLAuthorizerEvent) []string {
	t.Helper()
	type eventKey = firstAudioRealSQLAuthorizerEvent
	want := make(map[eventKey]int, 6+len(firstAudioLessonProbeColumns))
	add := func(action int, databaseName, triggerName, arg1, arg2 string, count int) {
		want[eventKey{phase: firstAudioGuardPreflight, permit: firstAudioSQLPermitProbeSQL, action: action, databaseName: databaseName, triggerName: triggerName, arg1: arg1, arg2: arg2, allowed: true}] = count
	}
	add(sqlite3.SQLITE_READ, "main", "", "lessons", "id", 2)
	add(sqlite3.SQLITE_UPDATE, "main", "", "lessons", "id", 1)
	add(sqlite3.SQLITE_SELECT, "", "fa_lessons_guard_update", "", "", 1)
	add(sqlite3.SQLITE_FUNCTION, "", "fa_lessons_guard_update", "", "first_audio_row_guard", 1)
	add(sqlite3.SQLITE_FUNCTION, "", "fa_lessons_guard_update", "", "typeof", 72)
	for _, column := range firstAudioLessonProbeColumns {
		add(sqlite3.SQLITE_READ, "main", "fa_lessons_guard_update", "lessons", column, 8)
	}
	if len(events) != 221 {
		t.Fatalf("zero-row statement authorizer event count=%d want=221", len(events))
	}
	got := make(map[eventKey]int, len(want))
	for _, event := range events {
		if !event.allowed {
			t.Fatalf("zero-row statement authorizer denied tuple: %#v", event)
		}
		got[event]++
	}
	if len(got) != len(want) {
		t.Fatalf("zero-row statement authorizer unique tuple count=%d want=%d; got=%#v", len(got), len(want), got)
	}
	for tuple, wantCount := range want {
		if gotCount := got[tuple]; gotCount != wantCount {
			t.Fatalf("zero-row statement authorizer tuple multiplicity=%d want=%d tuple=%#v", gotCount, wantCount, tuple)
		}
	}
	summary := make([]string, 0, len(got))
	for tuple, count := range got {
		summary = append(summary, fmt.Sprintf("%s|%d|%d|db=%q|source=%q|arg1=%q|arg2=%q|allowed=%t|count=%d", tuple.phase, tuple.permit, tuple.action, tuple.databaseName, tuple.triggerName, tuple.arg1, tuple.arg2, tuple.allowed, count))
	}
	sort.Strings(summary)
	return summary
}

func firstAudioOtherLessonValues(id int64, label string) []any {
	index := int64(0)
	if label == "lesson-b" {
		index = 1
	}
	return []any{
		id, int64(31) + index, int64(41) + index, int64(1) + index, map[bool]string{true: "queued", false: "started"}[index == 1],
		nil, float64(1700000000.25) + float64(index), nil, []byte(fmt.Sprintf("[%d,%d]", index+1, index+2)), "TEACH",
		index, index + 1, int64(1), int64(0), []byte("{}"), []byte(fmt.Sprintf("[%d]", index+1)), label, float64(1700000100.5) + float64(index),
	}
}

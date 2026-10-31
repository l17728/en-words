//go:build cgo

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/obs"
	"enwords/internal/scheduler"
	"enwords/internal/store"

	"github.com/gorilla/websocket"
	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type firstAudioRunPhase uint32

const (
	firstAudioRunDriver firstAudioRunPhase = iota + 1
	firstAudioRunStartup
	firstAudioRunPreflight
	firstAudioRunJourney
	firstAudioRunReset
	firstAudioRunShutdown
)

type firstAudioGuardContext struct {
	phase          firstAudioRunPhase
	lessonID       int64
	studentID      int64
	bookID         int64
	wordCount      int
	wordIDs        []int64
	nextLesson     int64
	probeToken     string
	resetSnapshot  string
	preflightProbe bool
}

type firstAudioMatrixPolicy struct {
	state             atomic.Pointer[firstAudioGuardContext]
	hooks             atomic.Uint64
	denials           atomic.Uint64
	expectedDenials   atomic.Uint64
	unexpectedDenials atomic.Uint64
	guardInvocations  atomic.Uint64
	probeCalls        atomic.Uint64
	probeUsed         [3]atomic.Bool
	rowProbeUsed      [3]atomic.Bool
	lastDenial        atomic.Value
	reads             map[string]map[string]struct{}
}

func newFirstAudioMatrixPolicy(probe string) *firstAudioMatrixPolicy {
	p := &firstAudioMatrixPolicy{reads: firstAudioStartupReadManifest()}
	p.publish(firstAudioGuardContext{phase: firstAudioRunDriver, probeToken: probe})
	return p
}

func (p *firstAudioMatrixPolicy) publish(next firstAudioGuardContext) {
	copy := next
	copy.wordIDs = append([]int64(nil), next.wordIDs...)
	p.state.Store(&copy)
}

func (p *firstAudioMatrixPolicy) install(conn *sqlite3.SQLiteConn) error {
	connectionID := p.hooks.Add(1)
	if err := conn.RegisterFunc("first_audio_row_guard", func(values ...any) bool {
		return p.rowGuardForConnection(connectionID, values...)
	}, true); err != nil {
		return err
	}
	if err := conn.RegisterFunc("first_audio_probe", func(token string) bool {
		state := p.state.Load()
		if state == nil || state.phase != firstAudioRunPreflight || token != state.probeToken || connectionID == 0 || connectionID > uint64(len(p.probeUsed)) || !p.probeUsed[connectionID-1].CompareAndSwap(false, true) {
			p.guardInvocations.Add(1)
			if state != nil && state.phase == firstAudioRunPreflight {
				p.expectedDenials.Add(1)
			} else {
				p.unexpectedDenials.Add(1)
			}
			return false
		}
		p.probeCalls.Add(1)
		return true
	}, false); err != nil {
		return err
	}
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, database, trigger string) int {
		if p.authorize(action, arg1, arg2, database, trigger) {
			return sqlite3.SQLITE_OK
		}
		p.denials.Add(1)
		p.lastDenial.Store(fmt.Sprintf("action=%d arg1=%q arg2=%q database=%q trigger=%q phase=%d", action, arg1, arg2, database, trigger, func() firstAudioRunPhase {
			if state := p.state.Load(); state != nil {
				return state.phase
			}
			return 0
		}()))
		state := p.state.Load()
		if state != nil && state.phase == firstAudioRunPreflight {
			p.expectedDenials.Add(1)
		} else {
			p.unexpectedDenials.Add(1)
		}
		return sqlite3.SQLITE_DENY
	})
	return nil
}

var firstAudioGuardTriggerNames = map[string]struct{}{
	"fa_users_no_insert": {}, "fa_users_no_update": {}, "fa_users_no_delete": {},
	"fa_config_globals_no_insert": {}, "fa_config_globals_no_update": {}, "fa_config_globals_no_delete": {},
	"fa_runtime_guard_insert": {}, "fa_runtime_guard_update": {}, "fa_runtime_guard_delete": {},
	"fa_lessons_guard_update": {}, "fa_audit_guard_insert": {}, "fa_audit_no_replace": {},
}

func (p *firstAudioMatrixPolicy) authorize(action int, arg1, arg2, database, trigger string) bool {
	state := p.state.Load()
	if state == nil {
		return false
	}
	phase := state.phase
	switch action {
	case sqlite3.SQLITE_SELECT:
		if trigger == "" {
			return true
		}
		_, ok := firstAudioGuardTriggerNames[trigger]
		return ok
	case sqlite3.SQLITE_READ:
		if trigger == "fa_audit_no_replace" {
			return (database == "main" || database == "") && arg1 == "audit_logs" && (arg2 == "id" || arg2 == "")
		}
		_, ok := p.reads[arg1][arg2]
		return ok && (database == "main" || (database == "" && arg2 == ""))
	case sqlite3.SQLITE_TRANSACTION:
		return trigger == "" && (arg1 == "BEGIN" || arg1 == "COMMIT" || arg1 == "ROLLBACK")
	case sqlite3.SQLITE_FUNCTION:
		fn := strings.ToLower(arg2)
		if fn == "sqlite_version" {
			return phase == firstAudioRunDriver && trigger == ""
		}
		if fn == "first_audio_probe" {
			return phase == firstAudioRunPreflight && trigger == ""
		}
		if fn == "coalesce" && trigger == "fa_lessons_guard_update" && (phase == firstAudioRunPreflight || phase == firstAudioRunReset) {
			return true
		}
		if fn == "count" || fn == "coalesce" || fn == "max" {
			return phase >= firstAudioRunStartup && phase <= firstAudioRunReset && arg1 == "" && trigger == ""
		}
		if fn == "first_audio_row_guard" {
			_, ok := firstAudioGuardTriggerNames[trigger]
			return ok
		}
		if fn == "raise" {
			_, ok := firstAudioGuardTriggerNames[trigger]
			return ok
		}
	case sqlite3.SQLITE_UPDATE:
		if database != "main" {
			return false
		}
		if phase == firstAudioRunStartup && trigger == "" && arg1 == "users" && arg2 == "id" {
			return true // api.NewApp's exact zero-row reservation; row trigger rejects effects.
		}
		if phase >= firstAudioRunStartup && phase <= firstAudioRunReset && trigger == "" && arg1 == "config_globals" && arg2 == "key" {
			return true // WithAtomicWriteObserved's zero-row reservation.
		}
		if phase >= firstAudioRunStartup && phase <= firstAudioRunReset && trigger == "rev_config_globals_UPDATE" && arg1 == "config_revisions" {
			return true
		}
		if (phase == firstAudioRunJourney || phase == firstAudioRunReset) && trigger == "" && arg1 == "runtime_records" && arg2 == "value" {
			return true // immutable row guard restricts the key at execution time.
		}
		if phase == firstAudioRunPreflight && trigger == "" && arg1 == "lessons" && arg2 == "phase" {
			return true // the fixed row trigger requires an exact no-op update.
		}
		return phase == firstAudioRunReset && trigger == "" && arg1 == "lessons" && arg2 == "id"
	case sqlite3.SQLITE_INSERT:
		if database != "main" {
			return false
		}
		if phase >= firstAudioRunStartup && phase <= firstAudioRunReset && trigger == "rev_config_globals_UPDATE" && arg1 == "config_revisions" {
			return true
		}
		if phase == firstAudioRunReset && trigger == "" && arg1 == "runtime_records" {
			return true
		}
		if phase == firstAudioRunJourney && trigger == "" && arg1 == "runtime_records" {
			return true // row guard restricts upserts to a valid snapshot for this lesson.
		}
		if phase == firstAudioRunJourney && trigger == "" && arg1 == "audit_logs" {
			return true // the trigger validates the complete bounded teacher-tool audit row.
		}
		return false
	case sqlite3.SQLITE_DELETE:
		return database == "main" && phase == firstAudioRunReset && trigger == "" && arg1 == "runtime_records"
	}
	return false
}

func (p *firstAudioMatrixPolicy) rowGuard(values ...any) bool {
	return p.rowGuardForConnection(0, values...)
}

func (p *firstAudioMatrixPolicy) rowGuardForConnection(connectionID uint64, values ...any) bool {
	p.guardInvocations.Add(1)
	if len(values) < 4 || len(values) > 10 {
		p.unexpectedDenials.Add(1)
		return false
	}
	op, key := firstAudioSQLiteText(values[0]), firstAudioSQLiteText(values[1])
	oldID, newID := firstAudioSQLiteText(values[2]), firstAudioSQLiteText(values[3])
	payload := ""
	if len(values) == 5 {
		payload = firstAudioSQLiteText(values[4])
	}
	oldPhase, newPhase := "", ""
	if len(values) == 6 {
		oldPhase, newPhase = firstAudioSQLiteText(values[4]), firstAudioSQLiteText(values[5])
	}
	state := p.state.Load()
	if state == nil {
		p.unexpectedDenials.Add(1)
		return false
	}
	if state.phase == firstAudioRunPreflight {
		if !state.preflightProbe {
			p.unexpectedDenials.Add(1)
			return false
		}
		if connectionID == 0 || connectionID > uint64(len(p.rowProbeUsed)) || !p.rowProbeUsed[connectionID-1].CompareAndSwap(false, true) {
			p.expectedDenials.Add(1)
			return false
		}
	}
	allowed := false
	switch state.phase {
	case firstAudioRunJourney:
		allowed = (op == "audit_insert" && firstAudioMatrixAuditAllowed(values, state.lessonID)) ||
			((op == "runtime_insert" || op == "runtime_update") && key == fmt.Sprintf("snapshot/%d", state.lessonID) && firstAudioMatrixSnapshotAllowed(payload, *state))
	case firstAudioRunReset:
		allowed = op == "runtime_delete" && key == fmt.Sprintf("snapshot/%d", state.lessonID) ||
			(op == "runtime_insert" && key == fmt.Sprintf("snapshot/%d", state.nextLesson) && payload == state.resetSnapshot && state.resetSnapshot != "") ||
			(op == "lesson_update" && oldID == fmt.Sprint(state.lessonID) && newID == fmt.Sprint(state.nextLesson) && state.nextLesson > state.lessonID && oldPhase == newPhase)
	case firstAudioRunPreflight:
		allowed = state.preflightProbe && op == "lesson_update" && oldID == fmt.Sprint(state.lessonID) && newID == fmt.Sprint(state.lessonID) && oldPhase == newPhase
	}
	if !allowed {
		p.unexpectedDenials.Add(1)
	}
	return allowed
}

func firstAudioMatrixSnapshotAllowed(payload string, state firstAudioGuardContext) bool {
	if len(payload) == 0 || len(payload) > 1<<20 || state.lessonID <= 0 || state.studentID <= 0 || state.bookID <= 0 || state.wordCount <= 0 || len(state.wordIDs) != state.wordCount {
		return false
	}
	wordIDs := make(map[int64]struct{}, len(state.wordIDs))
	for _, id := range state.wordIDs {
		if id <= 0 {
			return false
		}
		if _, duplicate := wordIDs[id]; duplicate {
			return false
		}
		wordIDs[id] = struct{}{}
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var snapshot classroom.State
	if decoder.Decode(&snapshot) != nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || snapshot.LessonID != state.lessonID || snapshot.StudentID != state.studentID || snapshot.BookID != state.bookID || snapshot.Config == nil {
		return false
	}
	switch snapshot.Phase {
	case classroom.PhaseShowWord, classroom.PhaseTTSSeg, classroom.PhaseRepeating, classroom.PhaseWaitAnswer, classroom.PhaseJudging, classroom.PhaseReteachShow, classroom.PhaseLessonEnd:
	default:
		return false
	}
	if snapshot.WordIdx < 0 || snapshot.WordIdx > state.wordCount || snapshot.QuizIdx < 0 || snapshot.QuizIdx > len(snapshot.QuizQueue) || snapshot.ReviewIdx < 0 || snapshot.ReviewIdx > len(snapshot.ReviewList) {
		return false
	}
	for _, item := range snapshot.QuizQueue {
		if _, ok := wordIDs[item.WordID]; !ok {
			return false
		}
	}
	for _, wordID := range snapshot.ReviewList {
		if _, ok := wordIDs[wordID]; !ok {
			return false
		}
	}
	needsQuiz := snapshot.Phase == classroom.PhaseWaitAnswer || snapshot.Phase == classroom.PhaseJudging || snapshot.Phase == classroom.PhaseReteachShow || (snapshot.Phase == classroom.PhaseRepeating && snapshot.InReteach)
	if needsQuiz && (snapshot.QuizIdx >= len(snapshot.QuizQueue)) {
		return false
	}
	if snapshot.InReview && snapshot.Phase == classroom.PhaseTTSSeg {
		if snapshot.ReviewIdx >= len(snapshot.ReviewList) {
			return false
		}
	} else if !snapshot.InReteach && (snapshot.Phase == classroom.PhaseShowWord || snapshot.Phase == classroom.PhaseTTSSeg || snapshot.Phase == classroom.PhaseRepeating) && snapshot.WordIdx >= state.wordCount {
		return false
	}
	return true
}

func firstAudioMatrixAuditAllowed(values []any, lessonID int64) bool {
	if len(values) != 10 || firstAudioSQLiteText(values[1]) != fmt.Sprintf("lesson-%d", lessonID) ||
		firstAudioSQLiteText(values[2]) != "teacher_agent" || firstAudioSQLiteText(values[3]) != "teacher_agent" ||
		firstAudioSQLiteText(values[5]) != "low" {
		return false
	}
	switch firstAudioSQLiteText(values[4]) {
	case "classroom.say", "classroom.show_word", "classroom.play_tts", "classroom.start_repeat", "classroom.start_quiz", "classroom.judge_answer", "classroom.start_review", "classroom.show_feedback_ui", "classroom.get_state":
	default:
		return false
	}
	if ok := firstAudioSQLiteText(values[6]); ok != "0" && ok != "1" {
		return false
	}
	result := firstAudioSQLiteText(values[7])
	if result == "" || len(result) > 64 {
		return false
	}
	for _, char := range result {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_') {
			return false
		}
	}
	args := firstAudioSQLiteText(values[8])
	const prefix = `{"bytes":`
	if !strings.HasPrefix(args, prefix) || !strings.HasSuffix(args, "}") {
		return false
	}
	argBytes, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(args, prefix), "}"))
	if err != nil || argBytes < 2 || argBytes > 512 {
		return false
	}
	duration, err := strconv.ParseInt(firstAudioSQLiteText(values[9]), 10, 64)
	return err == nil && duration >= 0
}

func firstAudioSQLiteText(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case []byte:
		return string(item)
	case int64:
		return fmt.Sprint(item)
	default:
		return ""
	}
}

var firstAudioBackendSequence atomic.Uint64

// Updated only after reviewing the schema-object diff in the isolated backend
// SDD. This pins the complete ordered sqlite_master inventory, including SQL.
const firstAudioExpectedSchemaDigest = "75f798665a3d99e1e8e896425aefba3e8c3f0eb50b0de2348d1877072bbaa804"

type firstAudioGuardedBackend struct {
	mu                 sync.Mutex
	runID              string
	dbPath             string
	capability         []byte
	probeToken         string
	policy             *firstAudioMatrixPolicy
	db                 *gorm.DB
	pool               *sql.DB
	app                *App
	listener           net.Listener
	server             *http.Server
	admission          *firstAudioHTTPAdmission
	identity           firstAudioFixtureIdentity
	password           string
	bound              firstAudioBound
	closed             bool
	closeErr           error
	serveErr           chan error
	runtimeCancel      context.CancelFunc
	runtimeDone        chan struct{}
	runtimeStopTimeout time.Duration
}

func newFirstAudioGuardedBackend() (firstAudioBackend, error) {
	capability, err := firstAudioCapabilityFromEnvironment()
	if err != nil {
		return nil, err
	}
	return &firstAudioGuardedBackend{
		runID: os.Getenv(firstAudioHarnessRunEnv), dbPath: os.Getenv(firstAudioHarnessDBEnv),
		capability: capability, serveErr: make(chan error, 1),
	}, nil
}

func (b *firstAudioGuardedBackend) Bind(ctx context.Context, fixture string) (firstAudioBound, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.db != nil || fixture != firstAudioFixture {
		return firstAudioBound{}, errors.New("backend bind state or fixture invalid")
	}
	if err := ctx.Err(); err != nil {
		return firstAudioBound{}, err
	}
	setup, err := store.Open(b.dbPath)
	if err != nil {
		return firstAudioBound{}, fmt.Errorf("setup database open failed: %w", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		return firstAudioBound{}, err
	}
	identity, password, err := seedFirstAudioSetupFixture(setup, b.capability, b.runID, time.Now().UTC())
	if err == nil {
		err = setup.Create(&store.ConfigGlobal{Key: "wordsPerLesson", Value: `60`}).Error
	}
	if err == nil {
		err = installFirstAudioMatrixGuards(setup)
	}
	if err != nil {
		_ = setupPool.Close()
		return firstAudioBound{}, fmt.Errorf("fixture setup failed: %w", err)
	}
	baseline, err := captureFirstAudioSnapshot(context.Background(), setupPool)
	if err != nil {
		_ = setupPool.Close()
		return firstAudioBound{}, fmt.Errorf("fixture baseline failed: %w", err)
	}
	if digest := firstAudioSchemaDigest(baseline.objects); digest != firstAudioExpectedSchemaDigest {
		_ = setupPool.Close()
		return firstAudioBound{}, fmt.Errorf("fixture schema is not the reviewed matrix schema: got digest %s", digest)
	}
	if err := setupPool.Close(); err != nil {
		return firstAudioBound{}, fmt.Errorf("setup pool close failed: %w", err)
	}
	if err := setupPool.PingContext(context.Background()); err == nil {
		return firstAudioBound{}, errors.New("closed setup pool remained usable")
	}

	b.probeToken = firstAudioProbeToken(b.capability, b.runID)
	b.policy = newFirstAudioMatrixPolicy(b.probeToken)
	driverName := fmt.Sprintf("first_audio_guarded_%d", firstAudioBackendSequence.Add(1))
	if err := registerFirstAudioProbeDriver(driverName, b.policy.install); err != nil {
		return firstAudioBound{}, err
	}
	db, err := gorm.Open(gormsqlite.Dialector{DriverName: driverName, DSN: b.dbPath}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return firstAudioBound{}, fmt.Errorf("guarded app pool open failed: %w", err)
	}
	pool, err := db.DB()
	if err != nil {
		return firstAudioBound{}, err
	}
	pool.SetMaxOpenConns(3)
	pool.SetMaxIdleConns(3)
	b.db, b.pool = db, pool
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunDriver, lessonID: identity.LessonID, studentID: identity.StudentID, bookID: identity.BookID, wordCount: len(identity.WordIDs), wordIDs: identity.WordIDs, probeToken: b.probeToken})
	if err := pool.PingContext(ctx); err != nil {
		return firstAudioBound{}, fmt.Errorf("driver probe failed: %w", err)
	}
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunStartup, lessonID: identity.LessonID, studentID: identity.StudentID, bookID: identity.BookID, wordCount: len(identity.WordIDs), wordIDs: identity.WordIDs, probeToken: b.probeToken})
	b.identity, b.password = identity, password
	b.app = NewApp(scheduler.RealClock{}, db)
	if b.app == nil || b.app.StartupError != nil {
		return firstAudioBound{}, errors.New("api.App startup failed under guarded pool")
	}
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunPreflight, lessonID: identity.LessonID, studentID: identity.StudentID, bookID: identity.BookID, wordCount: len(identity.WordIDs), wordIDs: identity.WordIDs, probeToken: b.probeToken})
	if err := b.preflightConnections(ctx); err != nil {
		return firstAudioBound{}, err
	}
	if b.policy.unexpectedDenials.Load() != 0 {
		return firstAudioBound{}, errors.New("unexpected denial during guarded startup")
	}
	afterStartup, err := captureFirstAudioSnapshot(ctx, pool)
	if err != nil || !equalFirstAudioSnapshots(baseline, afterStartup) {
		return firstAudioBound{}, errors.New("guarded app startup changed the fixture snapshot")
	}
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunJourney, lessonID: identity.LessonID, studentID: identity.StudentID, bookID: identity.BookID, wordCount: len(identity.WordIDs), wordIDs: identity.WordIDs, probeToken: b.probeToken})
	b.admission = newFirstAudioHTTPAdmission()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return firstAudioBound{}, err
	}
	if host, _, err := net.SplitHostPort(listener.Addr().String()); err != nil || host != "127.0.0.1" {
		_ = listener.Close()
		return firstAudioBound{}, errors.New("matrix listener did not bind IPv4 loopback")
	}
	b.listener = listener
	b.server = &http.Server{Handler: b.admission.wrap(b.app.Router()), ReadHeaderTimeout: 5 * time.Second}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	b.runtimeCancel = runtimeCancel
	b.runtimeDone = make(chan struct{})
	b.runtimeStopTimeout = 30 * time.Second
	go func() {
		defer close(b.runtimeDone)
		b.app.Rooms.RunTimersContext(runtimeCtx)
	}()
	go func() {
		err := b.server.Serve(listener)
		if !errors.Is(err, http.ErrServerClosed) {
			b.serveErr <- err
		}
	}()
	b.bound = firstAudioBound{
		Origin: "http://" + listener.Addr().String(), SourceBuildIdentity: firstAudioSourceIdentity(b.dbPath),
		StudentID: identity.StudentID, BookID: identity.BookID, WordIDs: append([]int64(nil), identity.WordIDs...),
		LessonID: identity.LessonID, Generation: 1,
	}
	if !validFirstAudioBound(b.bound) || b.bound.SourceBuildIdentity == "" {
		return firstAudioBound{}, errors.New("initial guarded binding is invalid")
	}
	return b.bound, nil
}

func firstAudioProbeToken(capability []byte, runID string) string {
	digest := sha256.Sum256(append(append([]byte(nil), capability...), []byte("\x00first_audio_preflight\x00"+runID)...))
	return hex.EncodeToString(digest[:])
}

func firstAudioSourceIdentity(dbPath string) string {
	path := filepath.Join(filepath.Dir(dbPath), "source-manifest.json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (b *firstAudioGuardedBackend) preflightConnections(ctx context.Context) error {
	connections := make([]*sql.Conn, 0, 3)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range 3 {
		conn, err := b.pool.Conn(ctx)
		if err != nil {
			return fmt.Errorf("pin guarded connection: %w", err)
		}
		connections = append(connections, conn)
	}
	if got := b.policy.hooks.Load(); got != 3 {
		return fmt.Errorf("guarded connection hook count %d, want 3", got)
	}
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunPreflight, lessonID: b.identity.LessonID, studentID: b.identity.StudentID, bookID: b.identity.BookID, wordCount: len(b.identity.WordIDs), wordIDs: b.identity.WordIDs, probeToken: b.probeToken, preflightProbe: true})
	for index, conn := range connections {
		var allowed bool
		if err := conn.QueryRowContext(ctx, "SELECT first_audio_probe(?)", b.probeToken).Scan(&allowed); err != nil || !allowed {
			return fmt.Errorf("physical connection %d lacks the preflight guard: %w", index, err)
		}
		if err := conn.QueryRowContext(ctx, "SELECT first_audio_probe(?)", b.probeToken).Scan(&allowed); err != nil || allowed {
			return fmt.Errorf("physical connection %d accepted a replayed probe token", index)
		}
		guardCalls := b.policy.guardInvocations.Load()
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return fmt.Errorf("physical connection %d preflight transaction: %w", index, err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE lessons SET phase=phase WHERE id=?", b.identity.LessonID); err != nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			return fmt.Errorf("physical connection %d exact row-guard probe: %w", index, err)
		}
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			return fmt.Errorf("physical connection %d probe rollback: %w", index, err)
		}
		if guarded := b.policy.guardInvocations.Load(); guarded <= guardCalls {
			return fmt.Errorf("physical connection %d did not invoke the row guard: before=%d after=%d", index, guardCalls, guarded)
		}
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return fmt.Errorf("physical connection %d repeated probe transaction: %w", index, err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE lessons SET phase=phase WHERE id=?", b.identity.LessonID); err == nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			return fmt.Errorf("physical connection %d accepted a replayed row-guard challenge", index)
		}
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			return fmt.Errorf("physical connection %d replay rollback: %w", index, err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE lessons SET id=id WHERE id=?", b.identity.LessonID); err == nil {
			return fmt.Errorf("physical connection %d accepted a forbidden lesson update", index)
		}
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			return fmt.Errorf("release preflight connection: %w", err)
		}
	}
	connections = nil
	if got := b.policy.probeCalls.Load(); got != 3 {
		return fmt.Errorf("preflight UDF calls %d, want one on every connection", got)
	}
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunPreflight, lessonID: b.identity.LessonID, studentID: b.identity.StudentID, bookID: b.identity.BookID, wordCount: len(b.identity.WordIDs), wordIDs: b.identity.WordIDs, probeToken: b.probeToken})
	return nil
}

func installFirstAudioMatrixGuards(db *gorm.DB) error {
	statements := []string{
		`CREATE TRIGGER fa_users_no_insert BEFORE INSERT ON users BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_users_no_update BEFORE UPDATE ON users BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_users_no_delete BEFORE DELETE ON users BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_config_globals_no_insert BEFORE INSERT ON config_globals BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_config_globals_no_update BEFORE UPDATE ON config_globals BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_config_globals_no_delete BEFORE DELETE ON config_globals BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_runtime_guard_insert BEFORE INSERT ON runtime_records BEGIN SELECT CASE WHEN first_audio_row_guard('runtime_insert',NEW.key,'','',NEW.value)=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_runtime_guard_update BEFORE UPDATE ON runtime_records BEGIN SELECT CASE WHEN first_audio_row_guard('runtime_update',NEW.key,'','',NEW.value)=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_runtime_guard_delete BEFORE DELETE ON runtime_records BEGIN SELECT CASE WHEN first_audio_row_guard('runtime_delete',OLD.key,'','')=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_lessons_guard_update BEFORE UPDATE ON lessons BEGIN SELECT CASE WHEN first_audio_row_guard('lesson_update','',CAST(OLD.id AS TEXT),CAST(NEW.id AS TEXT),COALESCE(OLD.phase,''),COALESCE(NEW.phase,''))=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_audit_no_replace BEFORE INSERT ON audit_logs WHEN EXISTS(SELECT 1 FROM audit_logs WHERE id=NEW.id) BEGIN SELECT RAISE(ABORT,'matrix_audit_collision'); END`,
		`CREATE TRIGGER fa_audit_guard_insert BEFORE INSERT ON audit_logs BEGIN SELECT CASE WHEN first_audio_row_guard('audit_insert',NEW.trace_id,NEW.actor_id,NEW.actor_role,NEW.tool,NEW.risk,NEW.ok,NEW.result,NEW.args,NEW.duration_ms)=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_audit_guard_update BEFORE UPDATE ON audit_logs BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
		`CREATE TRIGGER fa_audit_guard_delete BEFORE DELETE ON audit_logs BEGIN SELECT CASE WHEN first_audio_row_guard()=0 THEN RAISE(ABORT,'matrix_guard') END; END`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

type firstAudioHTTPAdmission struct {
	mu       sync.Mutex
	open     bool
	inFlight int
	changed  chan struct{}
}

func newFirstAudioHTTPAdmission() *firstAudioHTTPAdmission {
	return &firstAudioHTTPAdmission{open: true, changed: make(chan struct{})}
}

func (g *firstAudioHTTPAdmission) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if !g.open {
			g.mu.Unlock()
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		g.inFlight++
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			g.inFlight--
			close(g.changed)
			g.changed = make(chan struct{})
			g.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (g *firstAudioHTTPAdmission) stopAndDrain(ctx context.Context) error {
	g.stop()
	return g.drain(ctx)
}

func (g *firstAudioHTTPAdmission) stop() {
	g.mu.Lock()
	g.open = false
	g.mu.Unlock()
}

func (g *firstAudioHTTPAdmission) drain(ctx context.Context) error {
	g.mu.Lock()
	for g.inFlight != 0 {
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		g.mu.Lock()
	}
	g.mu.Unlock()
	return nil
}

func (g *firstAudioHTTPAdmission) resume() {
	g.mu.Lock()
	g.open = true
	g.mu.Unlock()
}

func (b *firstAudioGuardedBackend) Reset(ctx context.Context, lessonID int64) (firstAudioBound, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.db == nil || lessonID != b.bound.LessonID || b.bound.Generation != 1 {
		return firstAudioBound{}, errors.New("reset state or lesson identity invalid")
	}
	priorRoom, _ := b.app.Rooms.Room(lessonID)
	b.admission.stop()
	drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.app.Rooms.Drop(lessonID); err != nil && !errors.Is(err, errGenerationRetired) {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	if err := b.admission.drain(drainCtx); err != nil {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	if priorRoom == nil {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	priorRoom.mu.Lock()
	priorState := priorRoom.machine.Snapshot()
	words := make([]classroom.Word, 0, len(b.identity.WordIDs))
	for _, wordID := range b.identity.WordIDs {
		word, ok := priorRoom.words[wordID]
		if !ok {
			priorRoom.mu.Unlock()
			return firstAudioBound{}, errFirstAudioOutcomeUncertain
		}
		words = append(words, word)
	}
	priorRoom.mu.Unlock()
	if priorState.Config == nil {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	before, err := captureFirstAudioSnapshot(drainCtx, b.pool)
	if err != nil {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	nextLesson := lessonID + 1
	nextMachine := classroom.NewMachine(nextLesson, b.identity.BookID, b.identity.StudentID, words, *priorState.Config, b.app.Clock)
	if err := nextMachine.Start(); err != nil {
		return firstAudioBound{}, err
	}
	nextState, err := json.Marshal(nextMachine.Snapshot())
	if err != nil {
		return firstAudioBound{}, err
	}
	nextSnapshot := string(nextState)
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunReset, lessonID: lessonID, studentID: b.identity.StudentID, bookID: b.identity.BookID, wordCount: len(b.identity.WordIDs), wordIDs: b.identity.WordIDs, nextLesson: nextLesson, probeToken: b.probeToken, resetSnapshot: nextSnapshot})
	txErr := b.db.WithContext(drainCtx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&store.Lesson{}).Where("id = ?", lessonID).UpdateColumn("id", nextLesson)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("reset lesson row count mismatch")
		}
		result = tx.Where("key = ?", fmt.Sprintf("snapshot/%d", lessonID)).Delete(&store.RuntimeRecord{})
		if result.Error != nil {
			return result.Error
		}
		return tx.Create(&store.RuntimeRecord{Key: fmt.Sprintf("snapshot/%d", nextLesson), Value: nextSnapshot}).Error
	})
	if txErr != nil {
		b.policy.publish(firstAudioGuardContext{phase: firstAudioRunJourney, lessonID: lessonID, studentID: b.identity.StudentID, bookID: b.identity.BookID, wordCount: len(b.identity.WordIDs), wordIDs: b.identity.WordIDs, probeToken: b.probeToken})
		if errors.Is(txErr, context.Canceled) || errors.Is(txErr, context.DeadlineExceeded) {
			return firstAudioBound{}, errFirstAudioOutcomeUncertain
		}
		return firstAudioBound{}, txErr
	}
	var row store.Lesson
	if err := b.db.WithContext(drainCtx).First(&row, nextLesson).Error; err != nil || row.StudentID != b.identity.StudentID || row.BookID != b.identity.BookID {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	var nextSnapshotRow store.RuntimeRecord
	if err := b.db.WithContext(drainCtx).Where("key = ?", fmt.Sprintf("snapshot/%d", nextLesson)).First(&nextSnapshotRow).Error; err != nil || nextSnapshotRow.Value != nextSnapshot {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	var restoredState classroom.State
	if err := json.Unmarshal([]byte(nextSnapshotRow.Value), &restoredState); err != nil || restoredState.LessonID != nextLesson || restoredState.StudentID != b.identity.StudentID || restoredState.BookID != b.identity.BookID {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	after, err := captureFirstAudioSnapshot(drainCtx, b.pool)
	if err != nil || !validFirstAudioResetSnapshot(before, after, lessonID, nextLesson) || b.policy.unexpectedDenials.Load() != 0 {
		return firstAudioBound{}, errFirstAudioOutcomeUncertain
	}
	b.app.mu.Lock()
	delete(b.app.lessonStates, lessonID)
	delete(b.app.reports, lessonID)
	b.app.mu.Unlock()
	b.identity.LessonID = nextLesson
	b.identity.WordIDs = append([]int64(nil), b.identity.WordIDs...)
	b.bound.LessonID = nextLesson
	b.bound.Generation++
	b.policy.publish(firstAudioGuardContext{phase: firstAudioRunJourney, lessonID: nextLesson, studentID: b.identity.StudentID, bookID: b.identity.BookID, wordCount: len(b.identity.WordIDs), wordIDs: b.identity.WordIDs, probeToken: b.probeToken})
	b.admission.resume()
	return b.bound, nil
}

func (b *firstAudioGuardedBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return b.closeErr
	}
	b.closed = true
	var errs []error
	if b.admission != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		b.admission.stop()
		if b.app != nil && b.bound.LessonID > 0 {
			if err := b.app.Rooms.Drop(b.bound.LessonID); err != nil && !errors.Is(err, errGenerationRetired) {
				cancel()
				b.closeErr = fmt.Errorf("retire active classroom before shutdown: %w", err)
				return b.closeErr
			}
		}
		if err := b.admission.drain(ctx); err != nil {
			cancel()
			b.closeErr = fmt.Errorf("drain HTTP handlers before shutdown: %w", err)
			return b.closeErr
		}
		cancel()
	}
	if b.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := b.server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
			_ = b.server.Close()
		}
		cancel()
	}
	if b.runtimeCancel != nil {
		b.runtimeCancel()
		stopTimeout := b.runtimeStopTimeout
		if stopTimeout <= 0 {
			stopTimeout = 30 * time.Second
		}
		timer := time.NewTimer(stopTimeout)
		defer timer.Stop()
		select {
		case <-b.runtimeDone:
		case <-timer.C:
			b.closeErr = errors.New("classroom timer worker did not stop")
			return b.closeErr
		}
	}
	if b.listener != nil {
		if err := b.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if b.pool != nil {
		if err := b.pool.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	b.closeErr = errors.Join(errs...)
	return b.closeErr
}

type firstAudioSnapshot struct {
	objects []string
	rows    map[string][]string
}

func captureFirstAudioSnapshot(ctx context.Context, pool *sql.DB) (firstAudioSnapshot, error) {
	rows, err := pool.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)
	if err != nil {
		return firstAudioSnapshot{}, err
	}
	var objects []string
	var tables []string
	for rows.Next() {
		var typ, name, table string
		var sqlText sql.NullString
		if err := rows.Scan(&typ, &name, &table, &sqlText); err != nil {
			_ = rows.Close()
			return firstAudioSnapshot{}, err
		}
		if typ == "table" {
			tables = append(tables, name)
		}
		objects = append(objects, typ+"\x00"+name+"\x00"+table+"\x00"+sqlText.String)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return firstAudioSnapshot{}, err
	}
	_ = rows.Close()
	snapshot := firstAudioSnapshot{objects: objects, rows: make(map[string][]string, len(tables))}
	for _, table := range tables {
		query := `SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
		data, err := pool.QueryContext(ctx, query)
		if err != nil {
			return firstAudioSnapshot{}, err
		}
		columns, err := data.Columns()
		if err != nil {
			_ = data.Close()
			return firstAudioSnapshot{}, err
		}
		var normalized []string
		for data.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := data.Scan(pointers...); err != nil {
				_ = data.Close()
				return firstAudioSnapshot{}, err
			}
			row := make(map[string]any, len(columns))
			for index, column := range columns {
				if blob, ok := values[index].([]byte); ok {
					row[column] = string(blob)
				} else {
					row[column] = values[index]
				}
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				_ = data.Close()
				return firstAudioSnapshot{}, err
			}
			normalized = append(normalized, string(encoded))
		}
		if err := data.Err(); err != nil {
			_ = data.Close()
			return firstAudioSnapshot{}, err
		}
		_ = data.Close()
		sort.Strings(normalized)
		snapshot.rows[table] = normalized
	}
	return snapshot, nil
}

func equalFirstAudioSnapshots(left, right firstAudioSnapshot) bool {
	return reflect.DeepEqual(left, right)
}

func firstAudioSchemaDigest(objects []string) string {
	hasher := sha256.New()
	for _, object := range objects {
		_, _ = hasher.Write([]byte(object))
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func validFirstAudioResetSnapshot(before, after firstAudioSnapshot, oldID, newID int64) bool {
	if len(before.objects) != len(after.objects) {
		return false
	}
	for index := range before.objects {
		if before.objects[index] != after.objects[index] {
			return false
		}
	}
	for table, beforeRows := range before.rows {
		afterRows := after.rows[table]
		switch table {
		case "lessons":
			if !equalLessonRowsExceptID(beforeRows, afterRows, oldID, newID) {
				return false
			}
		case "runtime_records":
			if !equalRuntimeRowsExceptSnapshot(beforeRows, afterRows, oldID) {
				return false
			}
		default:
			if !equalStringSlices(beforeRows, afterRows) {
				return false
			}
		}
	}
	return len(before.rows) == len(after.rows)
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalLessonRowsExceptID(before, after []string, oldID, newID int64) bool {
	normalize := func(rows []string, ids ...int64) ([]string, bool) {
		result := make([]string, 0, len(rows))
		allowedIDCount := 0
		for _, encoded := range rows {
			var row map[string]any
			if json.Unmarshal([]byte(encoded), &row) != nil {
				return nil, false
			}
			id := int64(0)
			switch value := row["id"].(type) {
			case float64:
				id = int64(value)
			case int64:
				id = value
			}
			found := false
			for _, allowed := range ids {
				if id == allowed {
					found = true
					allowedIDCount++
				}
			}
			if !found {
				result = append(result, encoded)
				continue
			}
			row["id"] = float64(0)
			canonical, _ := json.Marshal(row)
			result = append(result, string(canonical))
		}
		sort.Strings(result)
		return result, allowedIDCount == 1
	}
	left, okLeft := normalize(before, oldID)
	right, okRight := normalize(after, newID)
	return okLeft && okRight && equalStringSlices(left, right) && len(before) == len(after)
}

func equalRuntimeRowsExceptSnapshot(before, after []string, oldID int64) bool {
	filter := func(rows []string) ([]string, int, int, bool) {
		result := make([]string, 0, len(rows))
		oldCount := 0
		newCount := 0
		for _, encoded := range rows {
			var row map[string]any
			if json.Unmarshal([]byte(encoded), &row) != nil {
				return nil, 0, 0, false
			}
			key, _ := row["key"].(string)
			if key == fmt.Sprintf("snapshot/%d", oldID) {
				oldCount++
				continue
			}
			if key == fmt.Sprintf("snapshot/%d", oldID+1) {
				newCount++
				continue
			}
			result = append(result, encoded)
		}
		sort.Strings(result)
		return result, oldCount, newCount, oldCount <= 1 && newCount <= 1
	}
	left, oldBefore, newBefore, okLeft := filter(before)
	right, oldAfter, newAfter, okRight := filter(after)
	if !okLeft || !okRight || !equalStringSlices(left, right) {
		return false
	}
	return oldBefore <= 1 && oldAfter == 0 && newBefore == 0 && newAfter == 1
}

func (b *firstAudioGuardedBackend) waitForServerError() error {
	select {
	case err := <-b.serveErr:
		return err
	default:
		return nil
	}
}

func TestFirstAudioMatrixGuardedBackendRunsAppEntryAndSamePoolReset(t *testing.T) {
	artifactRoot := filepath.Join(t.TempDir(), ".test-artifacts")
	runID := "backend_20261031"
	runRoot := filepath.Join(artifactRoot, "first-audio-matrix-"+runID)
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := createFirstAudioPrivateTestDirectory(runRoot); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Mkdir(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "source-manifest.json"), []byte(`{"source":"test-fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	capability := firstAudioTestCapability()
	t.Setenv(firstAudioHarnessGateEnv, firstAudioHarnessGate)
	t.Setenv(firstAudioHarnessCapEnv, base64.RawURLEncoding.EncodeToString(capability))
	t.Setenv(firstAudioHarnessRunEnv, runID)
	t.Setenv(firstAudioHarnessDBEnv, filepath.Join(runRoot, "matrix.db"))

	backendValue, err := newFirstAudioGuardedBackend()
	if err != nil {
		t.Fatal(err)
	}
	backend := backendValue.(*firstAudioGuardedBackend)
	t.Cleanup(func() { _ = backend.Close() })
	var appLog bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	first, err := backend.Bind(ctx, firstAudioFixture)
	if err != nil {
		t.Fatalf("guarded app bind failed: %v", err)
	}
	backend.app.Log = obs.NewWithSink(&appLog)
	studentToken := firstAudioLoginToken(t, first.Origin, backend.identity.Username, backend.password)
	firstTicket := firstAudioEnterLesson(t, first.Origin, studentToken, first.LessonID)
	firstAudioJoinUntilFirstTTS(t, backend, &appLog, first.Origin, first.LessonID, firstTicket)

	second, err := backend.Reset(ctx, first.LessonID)
	if err != nil {
		t.Fatalf("same-pool reset failed: %v; guard_denials=%d last=%v", err, backend.policy.unexpectedDenials.Load(), backend.policy.denials.Load())
	}
	if second.LessonID == first.LessonID || second.Generation != first.Generation+1 || second.Origin != first.Origin {
		t.Fatalf("reset did not preserve the app/listener and advance lesson: first=%#v second=%#v", first, second)
	}
	studentToken = firstAudioLoginToken(t, second.Origin, backend.identity.Username, backend.password)
	secondTicket := firstAudioEnterLesson(t, second.Origin, studentToken, second.LessonID)
	firstAudioJoinUntilFirstTTS(t, backend, &appLog, second.Origin, second.LessonID, secondTicket)
	if err := backend.Close(); err != nil {
		t.Fatalf("guarded backend close failed: %v", err)
	}
	if err := backend.pool.PingContext(context.Background()); err == nil {
		t.Fatal("guarded application pool remained usable after close")
	}
}

func TestFirstAudioMatrixRowGuardPinsResetPayloadAndLessonPhase(t *testing.T) {
	policy := newFirstAudioMatrixPolicy("probe")
	policy.publish(firstAudioGuardContext{phase: firstAudioRunReset, lessonID: 40, nextLesson: 41, resetSnapshot: `{"LessonID":41}`})
	if !policy.rowGuard("lesson_update", "", "40", "41", "teaching", "teaching") {
		t.Fatal("exact lesson ID transition with unchanged phase was denied")
	}
	if policy.rowGuard("lesson_update", "", "40", "41", "teaching", "finished") {
		t.Fatal("lesson update that changes phase was allowed")
	}
	if !policy.rowGuard("runtime_insert", "snapshot/41", "", "", `{"LessonID":41}`) {
		t.Fatal("canonical next snapshot was denied")
	}
	if policy.rowGuard("runtime_insert", "snapshot/41", "", "", `{"LessonID":999}`) {
		t.Fatal("noncanonical snapshot payload was allowed")
	}
	policy.publish(firstAudioGuardContext{phase: firstAudioRunJourney, lessonID: 41})
	if policy.rowGuard("runtime_insert", "snapshot/41", "", "", `{"LessonID":41}`) {
		t.Fatal("first-audio journey unexpectedly allowed a runtime write")
	}
	audit := []any{"audit_insert", "lesson-41", "teacher_agent", "teacher_agent", "classroom.play_tts", "low", int64(1), "ok", `{"bytes":24}`, int64(3)}
	if !policy.rowGuard(audit...) {
		t.Fatal("bounded classroom tool audit was denied")
	}
	audit[2] = "admin"
	if policy.rowGuard(audit...) {
		t.Fatal("non-teacher audit identity was allowed")
	}
}

func TestFirstAudioMatrixSnapshotGuardRejectsUnrestorablePositions(t *testing.T) {
	guard := firstAudioGuardContext{lessonID: 41, studentID: 7, bookID: 2, wordCount: 2, wordIDs: []int64{11, 12}}
	valid := `{"LessonID":41,"StudentID":7,"BookID":2,"Phase":"tts_seg","WordIdx":0,"QuizIdx":0,"QuizQueue":[],"ReviewList":[],"Config":{}}`
	guard.phase = firstAudioRunJourney
	if !firstAudioMatrixSnapshotAllowed(valid, guard) {
		t.Fatal("valid first-audio classroom snapshot was rejected")
	}
	for name, payload := range map[string]string{
		"teaching index at end":     `{"LessonID":41,"StudentID":7,"BookID":2,"Phase":"tts_seg","WordIdx":2,"QuizIdx":0,"QuizQueue":[],"ReviewList":[],"Config":{}}`,
		"answer phase without item": `{"LessonID":41,"StudentID":7,"BookID":2,"Phase":"wait_answer","WordIdx":0,"QuizIdx":0,"QuizQueue":[],"ReviewList":[],"Config":{}}`,
		"foreign quiz word":         `{"LessonID":41,"StudentID":7,"BookID":2,"Phase":"wait_answer","WordIdx":0,"QuizIdx":0,"QuizQueue":[{"word_id":999,"kind":"final","round":1,"seq":1}],"ReviewList":[],"Config":{}}`,
		"foreign review word":       `{"LessonID":41,"StudentID":7,"BookID":2,"Phase":"tts_seg","WordIdx":0,"QuizIdx":0,"QuizQueue":[],"ReviewList":[999],"ReviewIdx":0,"InReview":true,"Config":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if firstAudioMatrixSnapshotAllowed(payload, guard) {
				t.Fatal("unrestorable snapshot was allowed")
			}
		})
	}
}

func TestFirstAudioMatrixShutdownDrainFailurePreservesPoolAndError(t *testing.T) {
	pool, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(); err != nil {
		t.Fatal(err)
	}
	backend := &firstAudioGuardedBackend{
		pool: pool, runtimeCancel: func() {}, runtimeDone: make(chan struct{}), runtimeStopTimeout: time.Millisecond,
	}
	firstErr := backend.Close()
	if firstErr == nil || !strings.Contains(firstErr.Error(), "classroom timer worker did not stop") {
		t.Fatalf("shutdown drain failure not reported: %v", firstErr)
	}
	if secondErr := backend.Close(); secondErr != firstErr {
		t.Fatalf("repeated Close did not return the original error: first=%v second=%v", firstErr, secondErr)
	}
	if err := pool.Ping(); err != nil {
		t.Fatalf("backend closed the pool while its timer worker remained active: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func firstAudioLoginToken(t *testing.T, origin, username, password string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(origin+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || response.StatusCode != http.StatusOK || envelope.Code != 0 || envelope.Data.Token == "" {
		t.Fatalf("synthetic login failed: status=%d code=%d err=%v", response.StatusCode, envelope.Code, err)
	}
	return envelope.Data.Token
}

func firstAudioEnterLesson(t *testing.T, origin, token string, lessonID int64) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, origin+"/api/v1/classroom/lessons/"+strconv.FormatInt(lessonID, 10)+"/enter", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Ticket string `json:"ticket"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || response.StatusCode != http.StatusOK || envelope.Code != 0 || envelope.Data.Ticket == "" {
		t.Fatalf("classroom entry failed for lesson %d: status=%d code=%d err=%v", lessonID, response.StatusCode, envelope.Code, err)
	}
	return envelope.Data.Ticket
}

func firstAudioJoinUntilFirstTTS(t *testing.T, backend *firstAudioGuardedBackend, appLog *bytes.Buffer, origin string, lessonID int64, ticket string) {
	t.Helper()
	wsOrigin := "ws" + strings.TrimPrefix(origin, "http")
	endpoint := fmt.Sprintf("%s/ws/classroom?lessonId=%d&ticket=%s", wsOrigin, lessonID, url.QueryEscape(ticket))
	connection, response, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		if response != nil {
			t.Fatalf("first-audio websocket admission failed: status=%d err=%v", response.StatusCode, err)
		}
		t.Fatalf("first-audio websocket admission failed: %v", err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var received []string
	for {
		var frame map[string]any
		if err := connection.ReadJSON(&frame); err != nil {
			lastDenial, _ := backend.policy.lastDenial.Load().(string)
			t.Fatalf("first-audio websocket closed before TTS: %v; frames=%v; audits=%v; guard_denials=%d expected=%d unexpected=%d last_denial=%q logs=%s", err, received, backend.app.ToolAudit.Snapshot(), backend.policy.denials.Load(), backend.policy.expectedDenials.Load(), backend.policy.unexpectedDenials.Load(), lastDenial, appLog.String())
		}
		received = append(received, fmt.Sprint(frame))
		action, _ := frame["action"].(map[string]any)
		if action["type"] == "play_tts" {
			return
		}
	}
}

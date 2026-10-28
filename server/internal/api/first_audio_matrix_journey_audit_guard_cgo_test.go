//go:build cgo

package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enwords/internal/store"
	"enwords/internal/tools"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	firstAudioAuditProbePolicy     = "AUDIT_GUARD_PROBE"
	firstAudioAuditBeginPermit     = "AUDIT_BEGIN"
	firstAudioAuditBootstrapPermit = "AUDIT_BOOTSTRAP"
	firstAudioAuditIDPermit        = "AUDIT_ID_DERIVE"
	firstAudioAuditInsertPermit    = "AUDIT_INSERT"
	firstAudioAuditReadPermit      = "AUDIT_READBACK"
	firstAudioAuditCommitPermit    = "AUDIT_COMMIT"
	firstAudioAuditCleanupPermit   = "AUDIT_CLEANUP"
)

var firstAudioJourneyAuditColumns = []string{
	"id", "ts", "actor_type", "actor_id", "actor_role", "tool", "risk", "ok", "result", "args", "duration_ms", "trace_id",
}

var firstAudioJourneyAuditToolNames = []string{"classroom.say", "classroom.show_word", "classroom.play_tts"}

var firstAudioAllowedJourneyAuditTools = map[string]struct{}{
	firstAudioJourneyAuditToolNames[0]: {}, firstAudioJourneyAuditToolNames[1]: {}, firstAudioJourneyAuditToolNames[2]: {},
}

type firstAudioJourneyAuthorizerTuple struct {
	Phase    string `json:"phase"`
	Source   string `json:"source"`
	Action   int    `json:"action"`
	Database string `json:"database"`
	Arg1     string `json:"arg1"`
	Arg2     string `json:"arg2"`
	Permit   string `json:"permit"`
}

type firstAudioJourneyAuthorizerManifest struct {
	FormatVersion int                                `json:"format_version"`
	Tuples        []firstAudioJourneyAuthorizerTuple `json:"tuples"`
}

type firstAudioAuditProbe struct {
	mu                    sync.Mutex
	connections           *firstAudioSQLiteConnectionRegistry
	registryID            string
	raw                   *sqlite3.SQLiteConn
	trackedConn           *firstAudioTrackedSQLiteConn
	retirementErr         error
	permit                atomic.Value // string
	leaseActive           atomic.Bool
	txEpoch               atomic.Uint64
	contextVersion        uint64
	captureOnly           bool
	manifest              map[firstAudioJourneyAuthorizerTuple]struct{}
	tuples                []firstAudioJourneyAuthorizerTuple
	denials               atomic.Uint64
	callbacks             atomic.Uint64
	terminal              atomic.Bool
	forceCommitFailure    atomic.Bool
	forceIDQueryFailure   atomic.Bool
	forceRollbackFailure  atomic.Bool
	forceReadbackMismatch atomic.Bool
	transactionActive     atomic.Bool
	expected              atomic.Pointer[firstAudioAuditExpectedRow]
	rowPermit             atomic.Bool
	permitMu              sync.Mutex
	usedPermits           map[uint64]map[string]bool
	databasePath          string
	predictedID           atomic.Int64
	closePool             func() error
}

type firstAudioAuditExpectedRow struct {
	values         []any
	contextVersion uint64
	txEpoch        uint64
	registryID     string
	raw            *sqlite3.SQLiteConn
}

func firstAudioJourneyAuditTriggerDDL() []string {
	newValues := make([]string, 0, len(firstAudioJourneyAuditColumns)*2)
	for _, column := range firstAudioJourneyAuditColumns {
		newValues = append(newValues,
			"typeof(NEW."+column+")",
			"CASE WHEN typeof(NEW."+column+")='text' THEN CAST(NEW."+column+" AS TEXT) ELSE NEW."+column+" END",
		)
	}
	return []string{
		`CREATE TRIGGER fa_audit_guard_insert AFTER INSERT ON audit_logs BEGIN SELECT CASE WHEN first_audio_row_guard(` + strings.Join(newValues, ",") + `)=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_audit_guard_update BEFORE UPDATE ON audit_logs BEGIN SELECT RAISE(ABORT,'first_audio_guard'); END`,
		`CREATE TRIGGER fa_audit_guard_delete BEFORE DELETE ON audit_logs BEGIN SELECT RAISE(ABORT,'first_audio_guard'); END`,
	}
}

func installFirstAudioJourneyAuditTriggers(db *gorm.DB) error {
	for _, statement := range firstAudioJourneyAuditTriggerDDL() {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateFirstAudioJourneyAuditTriggerDDL(db *gorm.DB) error {
	manifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return fmt.Errorf("read journey audit trigger schema: %w", err)
	}
	actual := make([]firstAudioSQLiteObject, 0, len(firstAudioJourneyAuditTriggerDDL()))
	for _, object := range manifest.Objects {
		if object.Type == "trigger" && strings.HasPrefix(object.Name, "fa_audit_guard_") {
			actual = append(actual, object)
		}
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	if len(actual) == 0 {
		return nil // The base application schema has no journey extension.
	}
	manifestPath := filepath.Join("testdata", "first_audio_matrix_journey_trigger_manifest.json")
	baselineJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		actualJSON, _ := json.MarshalIndent(actual, "", "  ")
		return fmt.Errorf("read journey audit trigger manifest %s: %w\nactual manifest follows:\n%s", manifestPath, err, actualJSON)
	}
	var expected firstAudioSchemaManifest
	if err := json.Unmarshal(baselineJSON, &expected); err != nil {
		return fmt.Errorf("decode journey audit trigger manifest: %w", err)
	}
	canonical, err := json.Marshal(actual)
	if err != nil {
		return fmt.Errorf("encode journey audit trigger manifest: %w", err)
	}
	if expected.FormatVersion != 1 || !reflect.DeepEqual(expected.Objects, actual) || !firstAudioManifestDigestMatches(expected.SHA256, canonical) {
		return fmt.Errorf("journey audit trigger manifest drift: got %d objects, expected %d", len(actual), len(expected.Objects))
	}
	want := make(map[string]string, len(firstAudioJourneyAuditTriggerDDL()))
	for _, statement := range firstAudioJourneyAuditTriggerDDL() {
		fields := strings.Fields(statement)
		want[fields[2]] = statement
	}
	for _, object := range actual {
		if want[object.Name] != object.SQL {
			return fmt.Errorf("journey audit trigger SQL drift for %s", object.Name)
		}
	}
	return nil
}

func requireFirstAudioJourneyAuditTriggerDDL(db *gorm.DB) error {
	manifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return err
	}
	count := 0
	for _, object := range manifest.Objects {
		if object.Type == "trigger" && strings.HasPrefix(object.Name, "fa_audit_guard_") {
			count++
		}
	}
	if count != len(firstAudioJourneyAuditTriggerDDL()) {
		return fmt.Errorf("journey audit trigger count=%d, want=%d", count, len(firstAudioJourneyAuditTriggerDDL()))
	}
	return validateFirstAudioJourneyAuditTriggerDDL(db)
}

func requireFirstAudioJourneyGuardSchemaManifest(db *gorm.DB) error {
	if err := firstAudioValidateSchemaManifest(db); err != nil {
		return err
	}
	manifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return err
	}
	lessonCount := 0
	for _, object := range manifest.Objects {
		if object.Type == "trigger" && strings.HasPrefix(object.Name, "fa_lessons_guard_") {
			lessonCount++
		}
	}
	if lessonCount != len(firstAudioLessonProbeTriggerDDL()) {
		return fmt.Errorf("journey lesson guard count=%d, want=%d", lessonCount, len(firstAudioLessonProbeTriggerDDL()))
	}
	if err := validateFirstAudioLessonProbeTriggerDDL(db); err != nil {
		return err
	}
	return requireFirstAudioJourneyAuditTriggerDDL(db)
}

func firstAudioManifestDigestMatches(want string, canonical []byte) bool {
	digest := sha256.Sum256(canonical)
	return want == hex.EncodeToString(digest[:])
}

func (p *firstAudioAuditProbe) install(conn *sqlite3.SQLiteConn) error {
	if p == nil || conn == nil || p.connections == nil {
		return errors.New("audit guard probe is incomplete")
	}
	registration, ok := p.connections.Lookup(conn)
	if !ok {
		return errors.New("audit guard connection is not registered")
	}
	p.registryID, p.raw = registration.ID, conn
	if err := conn.RegisterFunc("first_audio_row_guard", func(args ...any) bool {
		p.callbacks.Add(1)
		// Consume authority before inspecting the callback. A wrong first row
		// cannot be followed by a valid row using the same permit.
		if !p.leaseActive.Load() || !p.transactionActive.Load() || p.txEpoch.Load() == 0 || !p.rowPermit.CompareAndSwap(true, false) || !p.connections.IsLive(p.registryID, p.raw) {
			p.terminal.Store(true)
			return false
		}
		expected := p.expected.Load()
		if expected == nil || expected.contextVersion != p.contextVersion || expected.txEpoch != p.txEpoch.Load() || expected.registryID != p.registryID || expected.raw != p.raw || len(args) != 24 || !reflect.DeepEqual(expected.values, args) {
			p.denials.Add(1)
			p.terminal.Store(true)
			return false
		}
		return true
	}, true); err != nil {
		return err
	}
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
		permit, _ := p.permit.Load().(string)
		tuple := firstAudioJourneyAuthorizerTuple{Phase: firstAudioAuditProbePolicy, Source: triggerName, Action: action, Database: databaseName, Arg1: arg1, Arg2: arg2, Permit: permit}
		p.mu.Lock()
		p.tuples = append(p.tuples, tuple)
		p.mu.Unlock()
		if p.captureOnly {
			return sqlite3.SQLITE_OK
		}
		_, allowed := p.manifest[tuple]
		if p.forceRollbackFailure.Load() && permit == firstAudioAuditCleanupPermit && action == sqlite3.SQLITE_TRANSACTION && arg1 == "ROLLBACK" {
			p.denials.Add(1)
			p.terminal.Store(true)
			return sqlite3.SQLITE_DENY
		}
		validPhase := permit == firstAudioAuditBootstrapPermit && !p.transactionActive.Load() && p.txEpoch.Load() == 0 || permit == firstAudioAuditBeginPermit && !p.transactionActive.Load() || permit != firstAudioAuditBootstrapPermit && permit != firstAudioAuditBeginPermit && p.transactionActive.Load()
		if !allowed || !p.leaseActive.Load() || !validPhase || (p.terminal.Load() && permit != firstAudioAuditCleanupPermit) || !p.connections.IsLive(p.registryID, p.raw) {
			p.denials.Add(1)
			p.terminal.Store(true)
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	})
	return nil
}

func (p *firstAudioAuditProbe) withPermit(name string, operation func() error) error {
	if p == nil || name == "" || operation == nil || (p.terminal.Load() && name != firstAudioAuditCleanupPermit) || !p.leaseActive.Load() {
		return errors.New("audit probe permit rejected")
	}
	if name == firstAudioAuditBeginPermit && p.transactionActive.Load() || name != firstAudioAuditBeginPermit && name != firstAudioAuditBootstrapPermit && !p.transactionActive.Load() {
		return errors.New("audit probe permit is outside its transaction phase")
	}
	if !p.permit.CompareAndSwap("", name) {
		return errors.New("audit probe permit already active")
	}
	epoch := p.txEpoch.Load()
	if name == firstAudioAuditBeginPermit {
		epoch++
	} else if name == firstAudioAuditBootstrapPermit {
		epoch = 0
	}
	p.permitMu.Lock()
	if p.usedPermits == nil {
		p.usedPermits = make(map[uint64]map[string]bool)
	}
	used := p.usedPermits[epoch]
	if used == nil {
		used = make(map[string]bool)
		p.usedPermits[epoch] = used
	}
	if used[name] {
		p.permitMu.Unlock()
		p.permit.Store("")
		p.denials.Add(1)
		p.terminal.Store(true)
		return errors.New("audit probe permit already consumed")
	}
	used[name] = true
	p.permitMu.Unlock()
	err := operation()
	p.permit.Store("")
	if err != nil {
		return err
	}
	return nil
}

func firstAudioValidateExpectedAuditRow(row store.AuditLog) error {
	if _, ok := firstAudioAllowedJourneyAuditTools[row.Tool]; !ok {
		return fmt.Errorf("audit tool %q is outside the fixed journey allowlist", row.Tool)
	}
	if row.ID != 0 || row.TS.Before(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) || row.TS.After(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)) {
		return errors.New("audit expected row has invalid generated ID or timestamp bounds")
	}
	if row.ActorType != "" || row.ActorID != "teacher_agent" || row.ActorRole != "teacher_agent" || row.Risk != "low" || !row.OK || row.Result != "ok" || row.TraceID != "lesson-7001" {
		return errors.New("audit expected row violates the fixed actor, result, risk, or trace contract")
	}
	if row.DurationMs < 1 {
		return errors.New("audit expected row duration must be at least 1ms")
	}
	var bytes int
	if _, err := fmt.Sscanf(row.Args, `{"bytes":%d}`, &bytes); err != nil || row.Args != fmt.Sprintf(`{"bytes":%d}`, bytes) || bytes < 1 || bytes > 512 {
		return errors.New("audit expected row args are not canonical bytes metadata")
	}
	return nil
}

func (p *firstAudioAuditProbe) capturedTuples() []firstAudioJourneyAuthorizerTuple {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := make(map[firstAudioJourneyAuthorizerTuple]struct{}, len(p.tuples))
	for _, tuple := range p.tuples {
		seen[tuple] = struct{}{}
	}
	out := make([]firstAudioJourneyAuthorizerTuple, 0, len(seen))
	for tuple := range seen {
		out = append(out, tuple)
	}
	sort.Slice(out, func(i, j int) bool {
		left, _ := json.Marshal(out[i])
		right, _ := json.Marshal(out[j])
		return string(left) < string(right)
	})
	return out
}

func (p *firstAudioAuditProbe) retireConnection() {
	if p == nil {
		return
	}
	p.connections.Retire(p.raw)
	p.transactionActive.Store(false)
	if p.closePool != nil {
		closeErr := p.closePool()
		p.closePool = nil
		p.retirementErr = errors.Join(p.retirementErr, closeErr)
	}
	if p.trackedConn == nil {
		p.retirementErr = errors.Join(p.retirementErr, errors.New("tracked SQLite connection close was not observed"))
		return
	}
	if p.trackedConn.raw != p.raw {
		p.retirementErr = errors.Join(p.retirementErr, errors.New("tracked SQLite connection close observer references a different handle"))
		return
	}
	if !p.trackedConn.closeOK.Load() {
		p.retirementErr = errors.Join(p.retirementErr, fmt.Errorf("tracked SQLite physical connection close was not confirmed: %v", p.trackedConn.closeErr))
	}
}

func firstAudioLoadJourneyAuthorizerManifest() (map[firstAudioJourneyAuthorizerTuple]struct{}, error) {
	path := filepath.Join("testdata", "first_audio_matrix_journey_authorizer_manifest.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest firstAudioJourneyAuthorizerManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return nil, err
	}
	if manifest.FormatVersion != 1 || len(manifest.Tuples) == 0 {
		return nil, errors.New("journey authorizer tuple manifest is incomplete")
	}
	out := make(map[firstAudioJourneyAuthorizerTuple]struct{}, len(manifest.Tuples))
	for _, tuple := range manifest.Tuples {
		if tuple.Phase != firstAudioAuditProbePolicy || tuple.Permit == "" {
			return nil, errors.New("journey authorizer tuple manifest has invalid phase or permit")
		}
		switch tuple.Permit {
		case firstAudioAuditBootstrapPermit, firstAudioAuditBeginPermit, firstAudioAuditIDPermit, firstAudioAuditInsertPermit, firstAudioAuditReadPermit, firstAudioAuditCommitPermit, firstAudioAuditCleanupPermit:
		default:
			return nil, fmt.Errorf("journey authorizer tuple manifest has unknown permit %q", tuple.Permit)
		}
		if _, duplicate := out[tuple]; duplicate {
			return nil, errors.New("journey authorizer tuple manifest has duplicates")
		}
		out[tuple] = struct{}{}
	}
	return out, nil
}

func firstAudioFixtureAuditRow(tool string, ts time.Time) store.AuditLog {
	return store.AuditLog{
		TS: ts, ActorType: "", ActorID: "teacher_agent", ActorRole: "teacher_agent", Tool: tool,
		Risk: "low", OK: true, Result: "ok", Args: `{"bytes":12}`, DurationMs: 17, TraceID: "lesson-7001",
	}
}

func firstAudioFixtureAuditEntry(tool string, ts time.Time) tools.AuditEntry {
	return tools.AuditEntry{TS: ts, ActorID: "teacher_agent", ActorRole: tools.Role("teacher_agent"), Tool: tool, Risk: tools.Risk("low"), OK: true, Result: "ok", Args: `{"bytes":12}`, DurationMs: 17, TraceID: "lesson-7001"}
}

func assertFirstAudioAuditProbeRolledBack(t *testing.T, probe *firstAudioAuditProbe) {
	t.Helper()
	if probe.transactionActive.Load() || !probe.connections.IsLive(probe.registryID, probe.raw) || probe.retirementErr != nil || probe.closePool == nil {
		t.Fatalf("probe rollback outcome invalid: txActive=%t connectionLive=%t retirementErr=%v poolOpen=%t", probe.transactionActive.Load(), probe.connections.IsLive(probe.registryID, probe.raw), probe.retirementErr, probe.closePool != nil)
	}
}

func firstAudioAuditTypedProjection(row store.AuditLog, id int64) []any {
	values := make([]any, 0, 24)
	appendText := func(value string) { values = append(values, "text", value) }
	appendInteger := func(value int64) { values = append(values, "integer", value) }
	appendInteger(id)
	appendText(row.TS.Format(sqlite3.SQLiteTimestampFormats[0]))
	appendText(row.ActorType)
	appendText(row.ActorID)
	appendText(row.ActorRole)
	appendText(row.Tool)
	appendText(row.Risk)
	appendInteger(boolAsSQLiteInt(row.OK))
	appendText(row.Result)
	appendText(row.Args)
	appendInteger(row.DurationMs)
	appendText(row.TraceID)
	return values
}

func boolAsSQLiteInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func firstAudioJourneyAuditReadback(tx *gorm.DB, id int64) ([]any, error) {
	projection := make([]string, 0, len(firstAudioJourneyAuditColumns)*2)
	for _, column := range firstAudioJourneyAuditColumns {
		projection = append(projection, "typeof("+column+")", "CASE WHEN typeof("+column+")='text' THEN CAST("+column+" AS TEXT) ELSE "+column+" END")
	}
	rows, err := tx.Raw(`SELECT `+strings.Join(projection, ",")+` FROM audit_logs WHERE id=?`, id).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	actual := make([]any, len(projection))
	dest := make([]any, len(projection))
	for i := range actual {
		dest[i] = &actual[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	return actual, nil
}

func firstAudioDeriveNextAuditID(tx *gorm.DB) (int64, error) {
	var id int64
	err := tx.Raw(`SELECT max(COALESCE((SELECT seq FROM sqlite_sequence WHERE name='audit_logs'),0), COALESCE((SELECT MAX(id) FROM audit_logs),0))+1`).Row().Scan(&id)
	return id, err
}

func TestFirstAudioJourneyAuditGuardCapturesAuthorizerTuples(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	capture := os.Getenv("FIRST_AUDIO_CAPTURE_MANIFESTS") == "1"
	if err != nil && !capture {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, capture, manifest)
	defer closeDB()
	if err := firstAudioExecuteAuditProbe(t, probe, db, firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC)), nil); err != nil {
		t.Fatal("guarded audit probe failed:", err)
	}
	wrong := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	wrong.Args = `{"bytes":13}`
	if err := firstAudioExecuteAuditProbe(t, probe, db, firstAudioFixtureAuditRow("classroom.say", wrong.TS), &wrong); err == nil {
		t.Fatal("mismatched capture candidate was accepted")
	}
	assertFirstAudioAuditRowCount(t, probe, 1)
	encoded, err := json.MarshalIndent(firstAudioJourneyAuthorizerManifest{FormatVersion: 1, Tuples: probe.capturedTuples()}, "", "  ")
	if err != nil {
		t.Fatal("encode captured authorizer tuples:", err)
	}
	t.Logf("captured tuple manifest (copy only after review):\n%s", encoded)
	if !capture {
		var frozen firstAudioJourneyAuthorizerManifest
		baseline, readErr := os.ReadFile(firstAudioJourneyAuditManifestPath())
		if readErr != nil || json.Unmarshal(baseline, &frozen) != nil || !reflect.DeepEqual(frozen.Tuples, probe.capturedTuples()) {
			t.Fatalf("live authorizer callbacks differ from frozen manifest (read error=%v)", readErr)
		}
	}
	var ddlManifest []byte
	if capture {
		ddlManifest, err = firstAudioJourneyAuditDDLManifest(db)
		if err != nil {
			t.Fatal("capture exact audit trigger manifest:", err)
		}
		t.Logf("captured audit trigger manifest (copy only after review):\n%s", ddlManifest)
	} else {
		ddlManifest, err = os.ReadFile(filepath.Join("testdata", "first_audio_matrix_journey_trigger_manifest.json"))
		if err != nil {
			t.Fatal("read reviewed audit trigger manifest:", err)
		}
	}
	if os.Getenv("FIRST_AUDIO_CAPTURE_MANIFESTS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal("create test manifest directory:", err)
		}
		if err := os.WriteFile(firstAudioJourneyAuditManifestPath(), encoded, 0o644); err != nil {
			t.Fatal("write captured authorizer manifest:", err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "first_audio_matrix_journey_trigger_manifest.json"), ddlManifest, 0o644); err != nil {
			t.Fatal("write captured trigger manifest:", err)
		}
	}
}

func firstAudioJourneyAuditDDLManifest(db *gorm.DB) ([]byte, error) {
	manifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return nil, err
	}
	guard := firstAudioSchemaManifest{FormatVersion: 1}
	for _, object := range manifest.Objects {
		if object.Type == "trigger" && strings.HasPrefix(object.Name, "fa_audit_guard_") {
			guard.Objects = append(guard.Objects, object)
		}
	}
	sort.Slice(guard.Objects, func(i, j int) bool { return guard.Objects[i].Name < guard.Objects[j].Name })
	canonical, err := json.Marshal(guard.Objects)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	guard.SHA256 = hex.EncodeToString(digest[:])
	return json.MarshalIndent(guard, "", "  ")
}

func TestFirstAudioJourneyAuditGuardAcceptsExactGeneratedAuditRow(t *testing.T) {
	for _, tool := range firstAudioJourneyAuditToolNames {
		t.Run(tool, func(t *testing.T) {
			manifest, err := firstAudioLoadJourneyAuthorizerManifest()
			if err != nil {
				t.Fatal("load reviewed journey authorizer manifest:", err)
			}
			probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
			defer closeDB()
			row := firstAudioFixtureAuditRow(tool, time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
			if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err != nil {
				t.Fatal("exact audit row was rejected:", err)
			}
			if probe.callbacks.Load() != 1 || probe.rowPermit.Load() || probe.terminal.Load() || !probe.connections.IsLive(probe.registryID, probe.raw) {
				t.Fatalf("callback/live/terminal evidence mismatch: callbacks=%d terminal=%t", probe.callbacks.Load(), probe.terminal.Load())
			}
		})
	}
}

func TestFirstAudioJourneyAuditGuardUsesProductionAuditWriter(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	for _, toolName := range firstAudioJourneyAuditToolNames {
		t.Run(toolName, func(t *testing.T) {
			probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
			defer closeDB()
			row := firstAudioFixtureAuditRow(toolName, time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
			var writerCalls atomic.Int64
			entry := firstAudioFixtureAuditEntry(toolName, time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
			writer := func(tx *gorm.DB) error {
				writerCalls.Add(1)
				return persistToolAudits(tx, []tools.AuditEntry{entry}, false)
			}
			if err := firstAudioExecuteAuditProbeWithWriter(t, probe, db, row, nil, writer); err != nil {
				t.Fatal("production audit writer rejected exact row:", err)
			}
			if writerCalls.Load() != 1 || probe.callbacks.Load() != 1 {
				t.Fatalf("writer/callback count: writer=%d callback=%d", writerCalls.Load(), probe.callbacks.Load())
			}
			assertFirstAudioAuditRowCount(t, probe, 1)
		})
	}
}

func TestFirstAudioJourneyAuditGuardProductionWriterTerminalizesAfterUnlistedTool(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal(err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	expected := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	entry := firstAudioFixtureAuditEntry("classroom.answer", expected.TS)
	var calls atomic.Int64
	writer := func(tx *gorm.DB) error {
		calls.Add(1)
		return persistToolAudits(tx, []tools.AuditEntry{entry}, false)
	}
	if err := firstAudioExecuteAuditProbeWithWriter(t, probe, db, expected, nil, writer); err == nil {
		t.Fatal("unlisted production audit tool was accepted")
	}
	if calls.Load() != 2 || probe.callbacks.Load() != 1 || !probe.terminal.Load() || probe.denials.Load() < 2 {
		t.Fatalf("writer/callback/terminal/denial evidence: calls=%d callbacks=%d terminal=%t denials=%d", calls.Load(), probe.callbacks.Load(), probe.terminal.Load(), probe.denials.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
	assertFirstAudioAuditProbeRolledBack(t, probe)
}

func TestFirstAudioJourneyAuditGuardProductionWriterReadbackMismatchDeniesSecondWrite(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal(err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	probe.forceReadbackMismatch.Store(true)
	row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	var calls atomic.Int64
	entry := firstAudioFixtureAuditEntry("classroom.say", row.TS)
	writer := func(tx *gorm.DB) error {
		calls.Add(1)
		return persistToolAudits(tx, []tools.AuditEntry{entry}, false)
	}
	if err := firstAudioExecuteAuditProbeWithWriter(t, probe, db, row, nil, writer); err == nil {
		t.Fatal("injected readback mismatch was accepted")
	}
	if calls.Load() != 2 || probe.callbacks.Load() != 1 || !probe.terminal.Load() || probe.denials.Load() == 0 {
		t.Fatalf("writer/callback/terminal/denial evidence: calls=%d callbacks=%d terminal=%t denials=%d", calls.Load(), probe.callbacks.Load(), probe.terminal.Load(), probe.denials.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
	assertFirstAudioAuditProbeRolledBack(t, probe)
}

func TestFirstAudioJourneyAuditGuardRejectsUnlistedToolBeforeSQL(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	row := firstAudioFixtureAuditRow("classroom.answer", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err == nil {
		t.Fatal("unlisted audit tool was accepted")
	}
	if probe.callbacks.Load() != 0 || probe.txEpoch.Load() != 0 {
		t.Fatalf("unlisted tool reached SQL: callbacks=%d txEpoch=%d", probe.callbacks.Load(), probe.txEpoch.Load())
	}
}

func TestFirstAudioJourneyAuditGuardConsumesPermitOnFirstMismatch(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	good := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	wrong := good
	wrong.Tool = "classroom.play_tts"
	if err := firstAudioExecuteAuditProbe(t, probe, db, good, &wrong); err == nil {
		t.Fatal("wrong then correct row reused a consumed callback permit")
	}
	if probe.callbacks.Load() != 1 || !probe.terminal.Load() || probe.denials.Load() == 0 {
		t.Fatalf("mismatch/replay evidence incomplete: callbacks=%d denials=%d terminal=%t", probe.callbacks.Load(), probe.denials.Load(), probe.terminal.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
}

func TestFirstAudioJourneyAuditGuardRejectsEveryMutableAuditField(t *testing.T) {
	base := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	cases := []struct {
		name   string
		mutate func(*store.AuditLog)
	}{
		{"timestamp", func(row *store.AuditLog) { row.TS = row.TS.Add(time.Microsecond) }},
		{"actor_type", func(row *store.AuditLog) { row.ActorType = "agent" }},
		{"actor_id", func(row *store.AuditLog) { row.ActorID = "other" }},
		{"actor_role", func(row *store.AuditLog) { row.ActorRole = "student" }},
		{"tool", func(row *store.AuditLog) { row.Tool = "classroom.play_tts" }},
		{"risk", func(row *store.AuditLog) { row.Risk = "high" }},
		{"ok", func(row *store.AuditLog) { row.OK = false }},
		{"result", func(row *store.AuditLog) { row.Result = "denied" }},
		{"args", func(row *store.AuditLog) { row.Args = `{"bytes":13}` }},
		{"duration", func(row *store.AuditLog) { row.DurationMs++ }},
		{"trace_id", func(row *store.AuditLog) { row.TraceID = "lesson-elsewhere" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := firstAudioLoadJourneyAuthorizerManifest()
			if err != nil {
				t.Fatal("load reviewed journey authorizer manifest:", err)
			}
			probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
			defer closeDB()
			wrong := base
			tc.mutate(&wrong)
			if err := firstAudioExecuteAuditProbe(t, probe, db, base, &wrong); err == nil {
				t.Fatal("mutated row was accepted")
			}
			if probe.callbacks.Load() != 1 || probe.rowPermit.Load() || !probe.terminal.Load() || probe.denials.Load() == 0 {
				t.Fatalf("guard evidence incomplete: callbacks=%d rowPermit=%t terminal=%t denials=%d", probe.callbacks.Load(), probe.rowPermit.Load(), probe.terminal.Load(), probe.denials.Load())
			}
			assertFirstAudioAuditRowCount(t, probe, 0)
		})
	}
}

func TestFirstAudioJourneyAuditGuardRejectsInvalidExpectedContract(t *testing.T) {
	base := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	cases := []struct {
		name   string
		mutate func(*store.AuditLog)
	}{
		{"wrong_actor", func(row *store.AuditLog) { row.ActorID = "attacker" }},
		{"wrong_trace", func(row *store.AuditLog) { row.TraceID = "lesson-7002" }},
		{"wrong_args", func(row *store.AuditLog) { row.Args = `{"bytes":513}` }},
		{"zero_duration", func(row *store.AuditLog) { row.DurationMs = 0 }},
		{"future_timestamp", func(row *store.AuditLog) { row.TS = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := firstAudioLoadJourneyAuthorizerManifest()
			if err != nil {
				t.Fatal("load reviewed journey authorizer manifest:", err)
			}
			probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
			defer closeDB()
			invalid := base
			tc.mutate(&invalid)
			if err := firstAudioExecuteAuditProbe(t, probe, db, invalid, nil); err == nil {
				t.Fatal("invalid expected row contract was accepted")
			}
			if probe.txEpoch.Load() != 0 || probe.callbacks.Load() != 0 {
				t.Fatalf("invalid expectation reached transaction/callback: epoch=%d callbacks=%d", probe.txEpoch.Load(), probe.callbacks.Load())
			}
			assertFirstAudioAuditRowCount(t, probe, 0)
		})
	}
}

func TestFirstAudioJourneyAuditGuardAcceptsLocalTimestampAndLongToolDuration(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.FixedZone("UTC+8", 8*60*60)))
	row.DurationMs = 6001
	if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err != nil {
		t.Fatal("valid local timestamp or long duration was rejected:", err)
	}
	assertFirstAudioAuditRowCount(t, probe, 1)
}

func TestFirstAudioJourneyAuditGuardRejectsWrongSQLiteStorageClasses(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	for _, column := range []string{"ts", "args", "duration_ms"} {
		t.Run(column+"_blob", func(t *testing.T) {
			probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
			defer closeDB()
			expected := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
			if err := firstAudioExecuteBlobAuditProbe(t, probe, db, expected, column); err != nil {
				t.Fatal("wrong SQLite storage class was not rejected cleanly:", err)
			}
			assertFirstAudioAuditRowCount(t, probe, 0)
		})
	}
}

func firstAudioExecuteBlobAuditProbe(t *testing.T, probe *firstAudioAuditProbe, db *gorm.DB, expected store.AuditLog, mutatedColumn string) (retErr error) {
	t.Helper()
	if err := firstAudioValidateExpectedAuditRow(expected); err != nil {
		return err
	}
	probe.contextVersion++
	probe.leaseActive.Store(true)
	tx := new(gorm.DB)
	if err := probe.withPermit(firstAudioAuditBeginPermit, func() error { *tx = *db.Begin(); return tx.Error }); err != nil {
		probe.leaseActive.Store(false)
		return fmt.Errorf("begin storage-class probe transaction: %w", err)
	}
	if probe.txEpoch.Add(1) == 0 {
		probe.terminal.Store(true)
		probe.transactionActive.Store(true)
		if rollbackErr := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return tx.Rollback().Error }); rollbackErr != nil {
			probe.retireConnection()
			probe.leaseActive.Store(false)
			return errors.Join(errors.New("storage-class transaction epoch overflow"), rollbackErr)
		}
		probe.transactionActive.Store(false)
		probe.leaseActive.Store(false)
		return errors.New("storage-class transaction epoch overflow")
	}
	probe.transactionActive.Store(true)
	finished := false
	defer func() {
		if !finished && probe.transactionActive.Load() {
			probe.terminal.Store(true)
			if rollbackErr := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return tx.Rollback().Error }); rollbackErr != nil {
				probe.retireConnection()
				retErr = errors.Join(retErr, fmt.Errorf("storage-class probe rollback failed: %w", rollbackErr))
			} else {
				probe.transactionActive.Store(false)
			}
		}
		probe.leaseActive.Store(false)
	}()
	var id int64
	if err := probe.withPermit(firstAudioAuditIDPermit, func() error {
		return tx.Raw(`SELECT max(COALESCE((SELECT seq FROM sqlite_sequence WHERE name='audit_logs'),0), COALESCE((SELECT MAX(id) FROM audit_logs),0))+1`).Row().Scan(&id)
	}); err != nil {
		return fmt.Errorf("derive storage-class probe ID: %w", err)
	}
	values := []any{id, expected.TS.Format(sqlite3.SQLiteTimestampFormats[0]), expected.ActorType, expected.ActorID, expected.ActorRole, expected.Tool, expected.Risk, int64(1), expected.Result, expected.Args, expected.DurationMs, expected.TraceID}
	for index, column := range firstAudioJourneyAuditColumns {
		if column == mutatedColumn {
			values[index] = []byte(fmt.Sprint(values[index]))
		}
	}
	projection := strings.Join(firstAudioJourneyAuditColumns, ",")
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	probe.expected.Store(&firstAudioAuditExpectedRow{values: firstAudioAuditTypedProjection(expected, id), contextVersion: probe.contextVersion, txEpoch: probe.txEpoch.Load(), registryID: probe.registryID, raw: probe.raw})
	probe.rowPermit.Store(true)
	err := probe.withPermit(firstAudioAuditInsertPermit, func() error {
		return tx.Exec(`INSERT INTO audit_logs (`+projection+`) VALUES (`+placeholders+`)`, values...).Error
	})
	if err == nil || probe.callbacks.Load() != 1 || !probe.terminal.Load() || probe.rowPermit.Load() {
		return fmt.Errorf("storage-class violation evidence mismatch: err=%v callbacks=%d terminal=%t rowPermit=%t", err, probe.callbacks.Load(), probe.terminal.Load(), probe.rowPermit.Load())
	}
	rollbackErr := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return tx.Rollback().Error })
	finished = true
	if rollbackErr != nil {
		probe.retireConnection()
		return fmt.Errorf("rollback storage-class violation: %w", rollbackErr)
	}
	probe.transactionActive.Store(false)
	return nil
}

func TestFirstAudioJourneyAuditAppendOnlyTriggersRejectMutationsAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append-only.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal("open append-only fixture:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	seed := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	seed.ID = 41
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal("seed existing audit row:", err)
	}
	if err := installFirstAudioStartupGuards(db); err != nil {
		t.Fatal("install duplicate/replacement guard:", err)
	}
	if err := installFirstAudioJourneyAuditTriggers(db); err != nil {
		t.Fatal("install append-only triggers:", err)
	}
	var before []store.AuditLog
	if err := db.Order("id").Find(&before).Error; err != nil {
		t.Fatal("read initial audit snapshot:", err)
	}
	columns := `id, ts, actor_type, actor_id, actor_role, tool, risk, ok, result, args, duration_ms, trace_id`
	values := `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`
	args := []any{seed.ID, seed.TS, seed.ActorType, seed.ActorID, seed.ActorRole, seed.Tool, seed.Risk, seed.OK, seed.Result, seed.Args, seed.DurationMs, seed.TraceID}
	operations := []struct {
		name string
		run  func() error
	}{
		{"update", func() error {
			return db.Model(&store.AuditLog{}).Where("id = ?", seed.ID).Update("result", "tampered").Error
		}},
		{"delete", func() error { return db.Delete(&store.AuditLog{}, seed.ID).Error }},
		{"duplicate_id", func() error {
			return db.Exec("INSERT INTO audit_logs ("+columns+") VALUES ("+values+")", args...).Error
		}},
		{"replace", func() error {
			return db.Exec("INSERT OR REPLACE INTO audit_logs ("+columns+") VALUES ("+values+")", args...).Error
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err == nil {
				t.Fatal("prohibited audit mutation unexpectedly succeeded")
			}
			var after []store.AuditLog
			if err := db.Order("id").Find(&after).Error; err != nil {
				t.Fatal("read audit snapshot after rejected mutation:", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected operation changed audit rows: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestFirstAudioJourneyAuditGuardDeniesUpdateDeleteAtPrepareTime(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	for _, operation := range []string{"UPDATE audit_logs SET result='x'", "DELETE FROM audit_logs"} {
		t.Run(operation[:6], func(t *testing.T) {
			probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
			defer closeDB()
			if err := db.Exec(operation).Error; err == nil {
				t.Fatal("unguarded UPDATE/DELETE unexpectedly prepared")
			}
			if probe.denials.Load() == 0 || !probe.terminal.Load() {
				t.Fatalf("prepare-time denial evidence missing: denials=%d terminal=%t", probe.denials.Load(), probe.terminal.Load())
			}
		})
	}
}

func TestFirstAudioJourneyGuardSchemaManifestRequiresBothExtensions(t *testing.T) {
	for _, missing := range []string{"audit", "lesson"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "partial-journey.db"))
			if err != nil {
				t.Fatal("open partial schema fixture:", err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if err := installFirstAudioStartupGuards(db); err != nil {
				t.Fatal("install fixed startup guards:", err)
			}
			if missing != "lesson" {
				if err := installFirstAudioLessonProbeTriggers(db); err != nil {
					t.Fatal("install lesson extension:", err)
				}
			}
			if missing != "audit" {
				if err := installFirstAudioJourneyAuditTriggers(db); err != nil {
					t.Fatal("install audit extension:", err)
				}
			}
			if err := requireFirstAudioJourneyGuardSchemaManifest(db); err == nil {
				t.Fatalf("schema gate accepted missing %s trigger extension", missing)
			}
		})
	}
}

func TestFirstAudioJourneyAuditGuardDerivesIDsFromMaximumSequenceAndExistingID(t *testing.T) {
	cases := []struct {
		name string
		seq  int64
		want int64
	}{
		{"ordinary_increment", 41, 42},
		{"sequence_greater_than_max_id", 73, 74},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := firstAudioLoadJourneyAuthorizerManifest()
			if err != nil {
				t.Fatal("load reviewed journey authorizer manifest:", err)
			}
			seed := func(db *gorm.DB) error {
				row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC))
				row.ID = 41
				if err := db.Create(&row).Error; err != nil {
					return err
				}
				return db.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='audit_logs'`, tc.seq).Error
			}
			probe, db, closeDB := openFirstAudioAuditProbeWithSetup(t, false, manifest, seed)
			defer closeDB()
			row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
			if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err != nil {
				t.Fatal("valid audit insert failed:", err)
			}
			if got := probe.predictedID.Load(); got != tc.want {
				t.Fatalf("derived ID=%d, want max(sequence, existing ID)+1=%d", got, tc.want)
			}
			assertFirstAudioAuditRowCount(t, probe, 2)
		})
	}
}

func TestFirstAudioJourneyAuditGuardRejectsGeneratedIDMismatchAndRollsBack(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	seed := func(db *gorm.DB) error {
		row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC))
		row.ID = 41
		return db.Create(&row).Error
	}
	probe, db, closeDB := openFirstAudioAuditProbeWithSetup(t, false, manifest, seed)
	defer closeDB()
	good := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	wrongID := good
	wrongID.ID = 99
	if err := firstAudioExecuteAuditProbe(t, probe, db, good, &wrongID); err == nil {
		t.Fatal("generated ID mismatch was accepted")
	}
	if probe.predictedID.Load() != 42 || !probe.terminal.Load() || probe.callbacks.Load() != 1 {
		t.Fatalf("ID mismatch evidence incomplete: predicted=%d terminal=%t callbacks=%d", probe.predictedID.Load(), probe.terminal.Load(), probe.callbacks.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 1)
}

func TestFirstAudioJourneyAuditGuardCommitFailureRollsBackBeforePoolReuse(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	probe.forceCommitFailure.Store(true)
	row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err == nil {
		t.Fatal("injected COMMIT denial unexpectedly succeeded")
	}
	if probe.transactionActive.Load() && probe.connections.IsLive(probe.registryID, probe.raw) || !probe.terminal.Load() || probe.denials.Load() == 0 {
		t.Fatalf("commit failure was not closed or retired: txActive=%t connectionLive=%t terminal=%t denials=%d", probe.transactionActive.Load(), probe.connections.IsLive(probe.registryID, probe.raw), probe.terminal.Load(), probe.denials.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
	if err := probe.withPermit(firstAudioAuditInsertPermit, func() error { return errors.New("must not execute") }); err == nil {
		t.Fatal("terminal candidate allowed permit reuse after failed commit")
	}
}

func TestFirstAudioJourneyAuditGuardIDQueryFailureRollsBack(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	probe.forceIDQueryFailure.Store(true)
	row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err == nil {
		t.Fatal("injected ID query denial unexpectedly succeeded")
	}
	if probe.transactionActive.Load() && probe.connections.IsLive(probe.registryID, probe.raw) || !probe.terminal.Load() {
		t.Fatalf("ID query failure was not rolled back or retired: txActive=%t connectionLive=%t terminal=%t", probe.transactionActive.Load(), probe.connections.IsLive(probe.registryID, probe.raw), probe.terminal.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
}

func TestFirstAudioJourneyAuditGuardRollbackFailureRetiresConnection(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	probe.forceIDQueryFailure.Store(true)
	probe.forceRollbackFailure.Store(true)
	row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	err = firstAudioExecuteAuditProbe(t, probe, db, row, nil)
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("forced rollback failure not surfaced to probe: %v", err)
	}
	if !probe.terminal.Load() || probe.transactionActive.Load() || probe.connections.IsLive(probe.registryID, probe.raw) || probe.closePool != nil || probe.retirementErr != nil || probe.trackedConn == nil || !probe.trackedConn.closeOK.Load() || probe.trackedConn.raw != probe.raw {
		t.Fatalf("rollback failure did not confirm retirement of the same physical connection: terminal=%t txActive=%t live=%t poolOpen=%t retirementErr=%v tracked=%p closeOK=%t sameRaw=%t", probe.terminal.Load(), probe.transactionActive.Load(), probe.connections.IsLive(probe.registryID, probe.raw), probe.closePool != nil, probe.retirementErr, probe.trackedConn, probe.trackedConn != nil && probe.trackedConn.closeOK.Load(), probe.trackedConn != nil && probe.trackedConn.raw == probe.raw)
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
	if err := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return nil }); err == nil {
		t.Fatal("retired candidate allowed follow-up cleanup SQL")
	}
}

func TestFirstAudioJourneyAuditGuardEpochOverflowRollsBack(t *testing.T) {
	manifest, err := firstAudioLoadJourneyAuthorizerManifest()
	if err != nil {
		t.Fatal("load reviewed journey authorizer manifest:", err)
	}
	probe, db, closeDB := openFirstAudioAuditProbe(t, false, manifest)
	defer closeDB()
	probe.txEpoch.Store(^uint64(0))
	row := firstAudioFixtureAuditRow("classroom.say", time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC))
	if err := firstAudioExecuteAuditProbe(t, probe, db, row, nil); err == nil {
		t.Fatal("wrapped transaction epoch unexpectedly proceeded")
	}
	if probe.transactionActive.Load() || !probe.terminal.Load() {
		t.Fatalf("epoch overflow did not close transaction: active=%t terminal=%t", probe.transactionActive.Load(), probe.terminal.Load())
	}
	assertFirstAudioAuditRowCount(t, probe, 0)
}

func assertFirstAudioAuditRowCount(t *testing.T, probe *firstAudioAuditProbe, want int) {
	t.Helper()
	conn, err := sql.Open("sqlite3", probe.databasePath)
	if err != nil {
		t.Fatal("open independent audit row verifier:", err)
	}
	defer conn.Close()
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&count); err != nil {
		t.Fatal("read independent audit row count:", err)
	}
	if count != want {
		t.Fatalf("persisted audit row count=%d, want %d after transaction close", count, want)
	}
}

func openFirstAudioAuditProbe(t *testing.T, captureOnly bool, manifest map[firstAudioJourneyAuthorizerTuple]struct{}) (*firstAudioAuditProbe, *gorm.DB, func()) {
	return openFirstAudioAuditProbeWithSetup(t, captureOnly, manifest, nil)
}

func openFirstAudioAuditProbeWithSetup(t *testing.T, captureOnly bool, manifest map[firstAudioJourneyAuthorizerTuple]struct{}, seed func(*gorm.DB) error) (*firstAudioAuditProbe, *gorm.DB, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journey-audit.db")
	setup, err := store.Open(path)
	if err != nil {
		t.Fatal("open audit setup DB:", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		if err := seed(setup); err != nil {
			_ = setupPool.Close()
			t.Fatal("seed audit probe database:", err)
		}
	}
	if err := installFirstAudioStartupGuards(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install startup and collision guards:", err)
	}
	if err := installFirstAudioLessonProbeTriggers(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install reviewed lesson guard extension:", err)
	}
	if err := installFirstAudioJourneyAuditTriggers(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install journey audit guards:", err)
	}
	if !captureOnly {
		if err := requireFirstAudioJourneyGuardSchemaManifest(setup); err != nil {
			_ = setupPool.Close()
			t.Fatal("validate complete journey guard schema extensions:", err)
		}
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("close setup DB:", err)
	}

	probe := &firstAudioAuditProbe{connections: newFirstAudioSQLiteConnectionRegistry(), contextVersion: 12, captureOnly: captureOnly, manifest: manifest, databasePath: path}
	probe.permit.Store("")
	name := fmt.Sprintf("first_audio_journey_audit_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(conn *sqlite3.SQLiteConn) error {
		if _, err := probe.connections.Register(conn); err != nil {
			return err
		}
		return probe.install(conn)
	}, probe.connections.Retire, func(conn *firstAudioTrackedSQLiteConn) {
		if probe.trackedConn == nil {
			probe.trackedConn = conn
		}
	}); err != nil {
		t.Fatal("register journey audit SQLite driver:", err)
	}
	// GORM's SQLite dialector probes sqlite_version during initialization.
	// Pin that one reviewed bootstrap tuple to the tracked physical connection.
	probe.leaseActive.Store(true)
	var db *gorm.DB
	err = probe.withPermit(firstAudioAuditBootstrapPermit, func() error {
		var openErr error
		db, openErr = gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
		return openErr
	})
	probe.leaseActive.Store(false)
	if err != nil {
		if db != nil {
			if pool, poolErr := db.DB(); poolErr == nil {
				_ = pool.Close()
			}
		}
		t.Fatal("open journey audit probe DB:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(0)
	probe.closePool = pool.Close
	if err := pool.PingContext(context.Background()); err != nil {
		_ = pool.Close()
		t.Fatal("connect guarded probe DB:", err)
	}
	if !probe.connections.IsLive(probe.registryID, probe.raw) || probe.registryID == "" {
		_ = pool.Close()
		t.Fatal("probe physical connection was not registered")
	}
	return probe, db, func() {
		if probe.closePool != nil {
			_ = probe.closePool()
			probe.closePool = nil
		}
	}
}

func firstAudioExecuteAuditProbe(t *testing.T, probe *firstAudioAuditProbe, db *gorm.DB, expected store.AuditLog, firstActual *store.AuditLog) (retErr error) {
	return firstAudioExecuteAuditProbeWithWriter(t, probe, db, expected, firstActual, nil)
}

func firstAudioExecuteAuditProbeWithWriter(t *testing.T, probe *firstAudioAuditProbe, db *gorm.DB, expected store.AuditLog, firstActual *store.AuditLog, productionWriter func(*gorm.DB) error) (retErr error) {
	t.Helper()
	if err := firstAudioValidateExpectedAuditRow(expected); err != nil {
		return err
	}
	probe.contextVersion++
	probe.leaseActive.Store(true)
	tx := new(gorm.DB)
	if err := probe.withPermit(firstAudioAuditBeginPermit, func() error { *tx = *db.Begin(); return tx.Error }); err != nil {
		probe.leaseActive.Store(false)
		return fmt.Errorf("begin probe transaction: %w", err)
	}
	epoch := probe.txEpoch.Add(1)
	probe.transactionActive.Store(true)
	if epoch == 0 {
		probe.terminal.Store(true)
		rollbackErr := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return tx.Rollback().Error })
		if rollbackErr != nil {
			probe.retireConnection()
			rollbackErr = errors.Join(rollbackErr, probe.retirementErr)
		} else {
			probe.transactionActive.Store(false)
		}
		probe.leaseActive.Store(false)
		return errors.Join(errors.New("audit transaction epoch overflow"), rollbackErr)
	}
	finished := false
	defer func() {
		if !finished && tx != nil && probe.transactionActive.Load() {
			probe.terminal.Store(true)
			rollbackErr := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return tx.Rollback().Error })
			if rollbackErr != nil {
				probe.retireConnection()
				retErr = errors.Join(retErr, probe.retirementErr)
				retErr = errors.Join(retErr, fmt.Errorf("rollback failed; guarded connection retired: %w", rollbackErr))
			} else {
				probe.transactionActive.Store(false)
			}
		}
		probe.leaseActive.Store(false)
	}()
	var expectedID int64
	if err := probe.withPermit(firstAudioAuditIDPermit, func() error {
		if probe.forceIDQueryFailure.Load() {
			probe.terminal.Store(true)
		}
		return tx.Raw(`SELECT max(COALESCE((SELECT seq FROM sqlite_sequence WHERE name='audit_logs'),0), COALESCE((SELECT MAX(id) FROM audit_logs),0))+1`).Row().Scan(&expectedID)
	}); err != nil {
		return fmt.Errorf("derive expected ID: %w", err)
	}
	probe.predictedID.Store(expectedID)
	probe.expected.Store(&firstAudioAuditExpectedRow{values: firstAudioAuditTypedProjection(expected, expectedID), contextVersion: probe.contextVersion, txEpoch: probe.txEpoch.Load(), registryID: probe.registryID, raw: probe.raw})
	probe.rowPermit.Store(true)
	actual := expected
	if firstActual != nil {
		actual = *firstActual
	}
	write := func(row store.AuditLog) error {
		if productionWriter != nil {
			return productionWriter(tx)
		}
		return tx.Create(&row).Error
	}
	if err := probe.withPermit(firstAudioAuditInsertPermit, func() error { return write(actual) }); err != nil {
		if productionWriter != nil {
			if !probe.terminal.Load() {
				return fmt.Errorf("production writer rejection did not terminalize: %w", err)
			}
			if retryErr := write(actual); retryErr == nil {
				return errors.New("terminal probe unexpectedly accepted second production-writer invocation")
			}
			return fmt.Errorf("production writer insert rejected and terminal retry denied: %w", err)
		}
		if firstActual == nil {
			return fmt.Errorf("insert exact audit row: %w", err)
		}
		// The first DML is expected to fail at the trigger. Retry a correct row
		// deliberately; terminal state and consumed permit must deny it.
		if !probe.terminal.Load() {
			return fmt.Errorf("mismatched row did not terminalize: %w", err)
		}
		correct := expected
		if retryErr := probe.withPermit(firstAudioAuditInsertPermit, func() error { return tx.Create(&correct).Error }); retryErr == nil {
			return errors.New("second row unexpectedly reused a consumed row callback permit")
		}
		rollbackErr := probe.withPermit(firstAudioAuditCleanupPermit, func() error { return tx.Rollback().Error })
		finished = true
		if rollbackErr != nil {
			probe.retireConnection()
			return fmt.Errorf("rollback rejected candidate: %w", rollbackErr)
		}
		probe.transactionActive.Store(false)
		return fmt.Errorf("first mismatched row rejected as required: %w", err)
	}
	if productionWriter == nil && actual.ID != expectedID {
		return fmt.Errorf("GORM generated ID=%d, controller predicted %d", actual.ID, expectedID)
	}
	if probe.callbacks.Load() == 0 || probe.rowPermit.Load() {
		return errors.New("expected exactly one consumed row callback permit")
	}
	var readback []any
	if err := probe.withPermit(firstAudioAuditReadPermit, func() error {
		var readErr error
		readback, readErr = firstAudioJourneyAuditReadback(tx, expectedID)
		return readErr
	}); err != nil {
		return fmt.Errorf("read back inserted audit row: %w", err)
	}
	if probe.forceReadbackMismatch.Load() && len(readback) > 1 {
		readback[1] = "injected_readback_mismatch"
		probe.terminal.Store(true)
	}
	if !reflect.DeepEqual(readback, firstAudioAuditTypedProjection(actual, expectedID)) {
		if productionWriter != nil && probe.terminal.Load() {
			if retryErr := write(actual); retryErr == nil {
				return errors.New("terminal probe unexpectedly accepted second production-writer invocation after readback mismatch")
			}
		}
		return fmt.Errorf("same-transaction audit readback mismatch: got=%#v want=%#v", readback, firstAudioAuditTypedProjection(actual, expectedID))
	}
	if probe.callbacks.Load() != 1 || probe.rowPermit.Load() {
		return errors.New("audit callback count or consumed permit changed before commit")
	}
	if err := probe.withPermit(firstAudioAuditCommitPermit, func() error {
		if probe.forceCommitFailure.Load() {
			probe.terminal.Store(true)
		}
		return tx.Commit().Error
	}); err != nil {
		return fmt.Errorf("commit guarded audit row: %w", err)
	}
	probe.transactionActive.Store(false)
	finished = true
	return nil
}

func firstAudioJourneyAuditManifestPath() string {
	return filepath.Join("testdata", "first_audio_matrix_journey_authorizer_manifest.json")
}

//go:build cgo

package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var firstAudioLessonProbeColumns = []string{
	"id", "student_id", "book_id", "seq", "status", "open_at", "started_at", "finished_at",
	"batch_word_ids", "phase", "group_idx", "word_idx", "repeat_count", "tts_seg_idx",
	"quiz_state", "cumulative_ids", "result", "state_upd_at",
}

func firstAudioLessonProbeTriggerDDL() []string {
	oldValues := make([]string, 0, len(firstAudioLessonProbeColumns)*2)
	newValues := make([]string, 0, len(firstAudioLessonProbeColumns)*2)
	for _, column := range firstAudioLessonProbeColumns {
		oldValues = append(oldValues, "typeof(OLD."+column+")", "CASE WHEN typeof(OLD."+column+")='text' THEN CAST(OLD."+column+" AS TEXT) ELSE OLD."+column+" END")
		newValues = append(newValues, "typeof(NEW."+column+")", "CASE WHEN typeof(NEW."+column+")='text' THEN CAST(NEW."+column+" AS TEXT) ELSE NEW."+column+" END")
	}
	nullValues := make([]string, len(firstAudioLessonProbeColumns)*2)
	for i := range nullValues {
		nullValues[i] = "NULL"
	}
	insertArgs := strings.Join(append(append([]string(nil), nullValues...), newValues...), ",")
	updateArgs := strings.Join(append(append([]string(nil), oldValues...), newValues...), ",")
	deleteArgs := strings.Join(append(append([]string(nil), oldValues...), nullValues...), ",")
	return []string{
		`CREATE TRIGGER fa_lessons_guard_insert BEFORE INSERT ON lessons BEGIN SELECT CASE WHEN first_audio_row_guard(` + insertArgs + `)=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_lessons_guard_update BEFORE UPDATE ON lessons BEGIN SELECT CASE WHEN first_audio_row_guard(` + updateArgs + `)=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
		`CREATE TRIGGER fa_lessons_guard_delete BEFORE DELETE ON lessons BEGIN SELECT CASE WHEN first_audio_row_guard(` + deleteArgs + `)=0 THEN RAISE(ABORT,'first_audio_guard') END; END`,
	}
}

func installFirstAudioLessonProbeTriggers(db *gorm.DB) error {
	for _, statement := range firstAudioLessonProbeTriggerDDL() {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return validateFirstAudioLessonProbeTriggerDDL(db)
}

func validateFirstAudioLessonProbeTriggerDDL(db *gorm.DB) error {
	manifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return fmt.Errorf("read lesson trigger schema manifest: %w", err)
	}
	manifestPath := filepath.Join("testdata", "first_audio_matrix_lesson_trigger_manifest.json")
	baselineJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read checked-in lesson trigger manifest %s: %w", manifestPath, err)
	}
	var expected firstAudioSchemaManifest
	if err := json.Unmarshal(baselineJSON, &expected); err != nil {
		return fmt.Errorf("decode checked-in lesson trigger manifest: %w", err)
	}
	actual := make([]firstAudioSQLiteObject, 0, 3)
	for _, object := range manifest.Objects {
		if object.Type == "trigger" && strings.HasPrefix(object.Name, "fa_lessons_guard_") {
			actual = append(actual, object)
		}
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	canonical, err := json.Marshal(actual)
	if err != nil {
		return fmt.Errorf("encode actual lesson trigger manifest: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if expected.FormatVersion != 1 || expected.SHA256 != hex.EncodeToString(digest[:]) || !reflect.DeepEqual(expected.Objects, actual) {
		return fmt.Errorf("lesson trigger schema drift: expected sha256=%s (%d objects), actual sha256=%s (%d objects)", expected.SHA256, len(expected.Objects), hex.EncodeToString(digest[:]), len(actual))
	}
	want := make(map[string]string, 3)
	for _, statement := range firstAudioLessonProbeTriggerDDL() {
		fields := strings.Fields(statement)
		if len(fields) < 4 {
			return fmt.Errorf("invalid fixed lesson trigger statement")
		}
		want[fields[2]] = statement
	}
	got := make(map[string]string, 3)
	for _, object := range actual {
		got[object.Name] = object.SQL
	}
	if len(got) != len(want) {
		return fmt.Errorf("lesson trigger manifest count=%d, want=%d", len(got), len(want))
	}
	for name, sql := range want {
		if got[name] != sql {
			return fmt.Errorf("lesson trigger SQL drift for %s: got=%q want=%q", name, got[name], sql)
		}
	}
	return nil
}

func (c *firstAudioAuthorizerCapture) reset() {
	c.mu.Lock()
	c.tuples = nil
	c.mu.Unlock()
}

func firstAudioHasAuthorizerTuple(tuples []firstAudioCapturedAuthorizerTuple, action int, arg1, arg2, databaseName, triggerName string) bool {
	for _, tuple := range tuples {
		if tuple.action == action && tuple.arg1 == arg1 && tuple.arg2 == arg2 && tuple.databaseName == databaseName && tuple.triggerName == triggerName {
			return true
		}
	}
	return false
}

func TestFirstAudioMatrixRealSQLLessonProbeTriggerUsesTyped72ArgumentProjectionAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lesson-probe.db")
	name := fmt.Sprintf("first_audio_matrix_lesson_probe_%d", firstAudioProbeDriverSequence.Add(1))
	var callbackCount int
	var callbackArgs []any
	if err := registerFirstAudioProbeDriver(name, func(conn *sqlite3.SQLiteConn) error {
		return conn.RegisterFunc("first_audio_row_guard", func(args ...any) bool {
			callbackCount++
			callbackArgs = cloneFirstAudioSQLiteCallbackArgs(args)
			return len(args) == 72 && reflect.DeepEqual(args[:36], args[36:])
		}, true)
	}); err != nil {
		t.Fatal("register isolated lesson-probe SQLite driver:", err)
	}
	guarded, err := gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: path}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("open isolated lesson-probe SQLite database:", err)
	}
	pool, err := guarded.DB()
	if err != nil {
		t.Fatal("get isolated lesson-probe sql.DB:", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })

	columns := make([]string, len(firstAudioLessonProbeColumns))
	for i, column := range firstAudioLessonProbeColumns {
		columns[i] = quoteFirstAudioIdentifier(column)
	}
	if _, err := pool.Exec("CREATE TABLE lessons (" + strings.Join(columns, ",") + ")"); err != nil {
		t.Fatal("create synthetic dynamic-type lessons table:", err)
	}
	values := []any{int64(7), int64(11), int64(13), int64(1), "started", nil, nil, nil, []byte("[1,2]"), "TEACH", int64(0), int64(0), int64(1), int64(0), []byte("{}"), []byte("[]"), "result", float64(1700000000.25)}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	if _, err := pool.Exec("INSERT INTO lessons ("+strings.Join(columns, ",")+") VALUES ("+placeholders+")", values...); err != nil {
		t.Fatal("seed heterogeneous synthetic lesson row:", err)
	}
	if err := installFirstAudioLessonProbeTriggers(guarded); err != nil {
		t.Fatal("install and validate fixed lesson probe triggers:", err)
	}

	conn, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("pin synthetic lesson probe connection:", err)
	}
	defer conn.Close()
	before := readFirstAudioProbeRow(t, conn)
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("begin explicit lesson probe transaction:", err)
	}
	result, err := tx.ExecContext(context.Background(), "UPDATE main.lessons SET id=id WHERE id=?", int64(7))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal("execute fixed no-op lesson update:", err)
	}
	matched, err := result.RowsAffected()
	if err != nil || matched != 1 {
		_ = tx.Rollback()
		t.Fatalf("fixed lesson update matched=%d err=%v, want exactly one row", matched, err)
	}
	if callbackCount != 1 || len(callbackArgs) != 72 {
		_ = tx.Rollback()
		t.Fatalf("row-guard calls=%d args=%d, want one call and 72 args", callbackCount, len(callbackArgs))
	}
	assertFirstAudioProbeCallbackTypes(t, callbackArgs)
	if err := tx.Rollback(); err != nil {
		t.Fatal("explicit lesson probe rollback:", err)
	}
	after := readFirstAudioProbeRow(t, conn)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("explicit rollback changed synthetic lesson row: before=%#v after=%#v", before, after)
	}
	if callbackCount != 1 {
		t.Fatalf("row-guard callback count after readback=%d, want one", callbackCount)
	}
}

func TestFirstAudioMatrixDriverProbeReceiptComesFromSynchronousSQLiteDialectorQuery(t *testing.T) {
	controller := newFirstAudioGuardController()
	driverContext, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardDriverProbe, version: 0,
	})
	if err != nil {
		t.Fatal("construct initial DRIVER_PROBE context:", err)
	}
	if err := controller.PublishInitial(driverContext); err != nil {
		t.Fatal("publish initial DRIVER_PROBE context:", err)
	}
	name := fmt.Sprintf("first_audio_matrix_driver_receipt_%d", firstAudioProbeDriverSequence.Add(1))
	var callbackCount int
	if err := registerFirstAudioTrackedSQLiteDriver(name, func(conn *sqlite3.SQLiteConn) error {
		if _, err := controller.connections.Register(conn); err != nil {
			return err
		}
		conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, databaseName, triggerName string) int {
			callbackCount++
			if err := controller.ObserveDriverProbe(conn, action, arg1, arg2, databaseName, triggerName); err != nil {
				return sqlite3.SQLITE_DENY
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	}, controller.connections.Retire); err != nil {
		t.Fatal("register driver-probe callback:", err)
	}
	guarded, err := gorm.Open(gormsqlite.Dialector{DriverName: name, DSN: filepath.Join(t.TempDir(), "driver-probe.db")}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal("gorm SQLite dialector rejected its own sqlite_version query:", err)
	}
	pool, err := guarded.DB()
	if err != nil {
		t.Fatal("obtain dialector pool:", err)
	}
	if callbackCount != 2 {
		_ = pool.Close()
		t.Fatalf("dialector authorizer callbacks=%d, want exact SELECT and sqlite_version callbacks", callbackCount)
	}
	if controller.driverProbe == nil || !controller.connections.IsLive(controller.driverProbe.connectionID, controller.driverProbe.rawConnection) {
		_ = pool.Close()
		t.Fatal("dialector callback receipt is not tied to a live ConnectHook registry entry")
	}
	registeredID := controller.driverProbe.connectionID
	registeredConn := controller.driverProbe.rawConnection
	startup, err := newFirstAudioGuardContext(firstAudioGuardContextSpec{
		runID: "run-0001", currentLessonID: 101, generation: 1,
		phase: firstAudioGuardStartupProbe, version: 1,
	})
	if err != nil {
		_ = pool.Close()
		t.Fatal("construct STARTUP_PROBE context:", err)
	}
	transition, err := controller.CloseAndDrain(context.Background())
	if err != nil {
		_ = pool.Close()
		t.Fatal("close driver-probe admission:", err)
	}
	if err := transition.Publish(context.Background(), startup, firstAudioGuardTransitionEvidence{}); err != nil {
		_ = pool.Close()
		t.Fatal("publish startup after real dialector callbacks:", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal("close dialector test pool:", err)
	}
	if controller.connections.IsLive(registeredID, registeredConn) {
		t.Fatal("physical driver close did not retire the exact connection registration")
	}
}

func cloneFirstAudioSQLiteCallbackArgs(args []any) []any {
	cloned := make([]any, len(args))
	for i, value := range args {
		if blob, ok := value.([]byte); ok {
			cloned[i] = append([]byte(nil), blob...)
			continue
		}
		cloned[i] = value
	}
	return cloned
}

func assertFirstAudioProbeCallbackTypes(t *testing.T, args []any) {
	t.Helper()
	if len(args) != 72 {
		t.Fatalf("row-guard callback argument count=%d, want 72", len(args))
	}
	wantTypes := []string{"integer", "integer", "integer", "integer", "text", "null", "null", "null", "blob", "text", "integer", "integer", "integer", "integer", "blob", "blob", "text", "real"}
	for i, want := range wantTypes {
		oldType, oldOK := args[i*2].(string)
		newType, newOK := args[36+i*2].(string)
		if !oldOK || !newOK || oldType != want || newType != want {
			t.Errorf("lesson column %s type tags old=%#v new=%#v, want %q", firstAudioLessonProbeColumns[i], args[i*2], args[36+i*2], want)
			continue
		}
		if !reflect.DeepEqual(args[i*2+1], args[36+i*2+1]) {
			t.Errorf("lesson column %s old/new values differ: old=%#v new=%#v", firstAudioLessonProbeColumns[i], args[i*2+1], args[36+i*2+1])
		}
	}
}

func readFirstAudioProbeRow(t *testing.T, conn *sql.Conn) []any {
	t.Helper()
	columns := make([]string, len(firstAudioLessonProbeColumns))
	for i, column := range firstAudioLessonProbeColumns {
		columns[i] = quoteFirstAudioIdentifier(column)
	}
	rows, err := conn.QueryContext(context.Background(), "SELECT "+strings.Join(columns, ",")+" FROM main.lessons WHERE id=?", int64(7))
	if err != nil {
		t.Fatal("query synthetic lesson row:", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("synthetic lesson row missing: %v", rows.Err())
	}
	values := make([]any, len(columns))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		t.Fatal("scan synthetic lesson row:", err)
	}
	if rows.Next() {
		t.Fatal("synthetic lesson query returned more than one row")
	}
	if err := rows.Err(); err != nil {
		t.Fatal("iterate synthetic lesson row:", err)
	}
	return cloneFirstAudioSQLiteCallbackArgs(values)
}

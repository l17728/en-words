//go:build cgo

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/store"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// This recorder is capture-only: it runs against a disposable database and
// always allows SQLite callbacks. It must never be reused as an authorizer.
type firstAudioResetCapture struct {
	mu         sync.Mutex
	recording  bool
	statement  string
	statements []firstAudioResetCapturedStatement
}

type firstAudioResetCapturedStatement struct {
	Sequence  int                            `json:"sequence"`
	Operation string                         `json:"operation"`
	SQL       string                         `json:"sql_template"`
	Tuples    []firstAudioResetCapturedTuple `json:"tuples"`
}

type firstAudioResetCapturedTuple struct {
	Action   int    `json:"action"`
	Arg1     string `json:"arg1"`
	Arg2     string `json:"arg2"`
	Database string `json:"database"`
	Trigger  string `json:"trigger"`
}

func (c *firstAudioResetCapture) install(conn *sqlite3.SQLiteConn) error {
	if err := conn.RegisterFunc("first_audio_row_guard", func(args ...any) bool { return true }, true); err != nil {
		return err
	}
	conn.RegisterAuthorizerWithTriggerName(func(action int, arg1, arg2, database, trigger string) int {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.recording {
			return sqlite3.SQLITE_OK
		}
		if len(c.statements) == 0 || c.statement == "" {
			c.statements = append(c.statements, firstAudioResetCapturedStatement{Sequence: len(c.statements) + 1, Operation: "UNBOUND_CAPTURE_CALLBACK"})
		}
		last := &c.statements[len(c.statements)-1]
		last.Tuples = append(last.Tuples, firstAudioResetCapturedTuple{Action: action, Arg1: arg1, Arg2: arg2, Database: database, Trigger: trigger})
		return sqlite3.SQLITE_OK
	})
	return nil
}

func (c *firstAudioResetCapture) enable() { c.mu.Lock(); c.recording = true; c.mu.Unlock() }

func (c *firstAudioResetCapture) begin(operation, sqlTemplate string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statement = operation
	c.statements = append(c.statements, firstAudioResetCapturedStatement{
		Sequence: len(c.statements) + 1, Operation: operation, SQL: sqlTemplate,
	})
}

func (c *firstAudioResetCapture) end() { c.mu.Lock(); c.statement = ""; c.mu.Unlock() }

// TestFirstAudioResetAuthorizerCaptureOnly generates the reviewed tuple
// artifact from an isolated, synthetic fixture. Set FIRST_AUDIO_WRITE_RESET_MANIFEST=1
// to update the checked-in capture; default runs compare and fail on drift.
func TestFirstAudioResetAuthorizerCaptureOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "reset-capture.db")
	setup, err := store.Open(dbPath)
	if err != nil {
		t.Fatal("open isolated setup fixture:", err)
	}
	setupPool, err := setup.DB()
	if err != nil {
		t.Fatal(err)
	}
	capability := firstAudioTestCapability()
	identity, _, err := seedFirstAudioSetupFixture(setup, capability, "reset_capture_20261029", time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	zeroFirstAudioSecret(capability)
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("seed synthetic fixture:", err)
	}
	if err := setup.Create(&store.RuntimeRecord{Key: fmt.Sprintf("snapshot/%d", identity.LessonID), Value: `{"phase":"INTRO"}`}).Error; err != nil {
		_ = setupPool.Close()
		t.Fatal("seed old snapshot:", err)
	}
	if err := installFirstAudioStartupGuards(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install reviewed startup guards:", err)
	}
	if err := installFirstAudioLessonProbeTriggers(setup); err != nil {
		_ = setupPool.Close()
		t.Fatal("install reviewed lesson triggers:", err)
	}
	schemaManifest, _, err := firstAudioReadSchemaManifest(setup)
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("read fixture schema manifest:", err)
	}
	baseline, err := firstAudioCaptureDatabaseSnapshotWithKnownSchema(setup)
	if err != nil {
		_ = setupPool.Close()
		t.Fatal("capture pre-reset fixture snapshot:", err)
	}
	if err := setupPool.Close(); err != nil {
		t.Fatal("close setup database before capture:", err)
	}

	capture := &firstAudioResetCapture{}
	driverName := fmt.Sprintf("first_audio_reset_capture_%d", firstAudioProbeDriverSequence.Add(1))
	if err := registerFirstAudioProbeDriver(driverName, capture.install); err != nil {
		t.Fatal("register capture-only driver:", err)
	}
	db, err := gorm.Open(sqlite.Dialector{DriverName: driverName, DSN: dbPath}, &gorm.Config{DisableAutomaticPing: true, PrepareStmt: false})
	if err != nil {
		t.Fatal("open capture-only GORM pool:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal("pin capture connection:", err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error {
		if _, ok := raw.(*sqlite3.SQLiteConn); !ok {
			return fmt.Errorf("unexpected connection type %T", raw)
		}
		return nil
	}); err != nil {
		t.Fatal("verify pinned SQLite connection:", err)
	}
	// Exclude driver open/connection setup callbacks. The capture begins only
	// after the physical connection has been pinned and verified.
	capture.enable()

	oldID, newID := identity.LessonID, identity.LessonID+1
	columns := firstAudioLessonColumns(t)
	selectLesson := "SELECT " + strings.Join(columns, ",") + " FROM lessons WHERE id=?"
	updateLesson := "UPDATE lessons SET id=? WHERE id=?"
	deleteSnapshot := "DELETE FROM runtime_records WHERE key=?"
	capture.begin("01_begin_tx", "driver.ConnBeginTx")
	tx, err := conn.BeginTx(ctx, nil)
	capture.end()
	if err != nil {
		t.Fatal("capture BEGIN transaction callback:", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	capture.begin("02_select_old_lesson", selectLesson)
	rows, err := tx.QueryContext(ctx, selectLesson, oldID)
	capture.end()
	if err != nil {
		t.Fatal("capture old lesson SELECT:", err)
	}
	if !rows.Next() {
		_ = rows.Close()
		t.Fatal("old synthetic lesson missing")
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := rows.Scan(destinations...); err != nil {
		_ = rows.Close()
		t.Fatal("scan old lesson:", err)
	}
	if rows.Next() {
		_ = rows.Close()
		t.Fatal("old lesson ID is not unique")
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal("read old lesson rows:", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal("close old lesson rows:", err)
	}

	capture.begin("03_update_lesson_id", updateLesson)
	result, err := tx.ExecContext(ctx, updateLesson, newID, oldID)
	capture.end()
	if err != nil {
		t.Fatal("capture lesson ID update:", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("lesson update rows=%d err=%v", changed, err)
	}
	capture.begin("04_delete_old_snapshot", deleteSnapshot)
	result, err = tx.ExecContext(ctx, deleteSnapshot, fmt.Sprintf("snapshot/%d", oldID))
	capture.end()
	if err != nil {
		t.Fatal("capture old snapshot delete:", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("snapshot delete rows=%d err=%v", changed, err)
	}
	capture.begin("05_commit_tx", "driver.Tx.Commit")
	err = tx.Commit()
	capture.end()
	if err != nil {
		t.Fatal("capture COMMIT transaction callback:", err)
	}
	committed = true

	const schemaQuery = "SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name,tbl_name"
	capture.begin("06_schema_manifest_read", schemaQuery)
	schemaRows, err := conn.QueryContext(ctx, schemaQuery)
	if err == nil {
		count := 0
		for schemaRows.Next() {
			var objectType, name, tableName string
			var ddl sql.NullString
			if scanErr := schemaRows.Scan(&objectType, &name, &tableName, &ddl); scanErr != nil {
				err = scanErr
				break
			}
			count++
		}
		if err == nil {
			err = schemaRows.Err()
		}
		if closeErr := schemaRows.Close(); err == nil {
			err = closeErr
		}
		if err == nil && count == 0 {
			err = fmt.Errorf("empty schema manifest")
		}
	}
	capture.end()
	if err != nil {
		t.Fatal("capture schema manifest read:", err)
	}

	specs := firstAudioResetSnapshotSpecs(t)
	for _, spec := range specs {
		quotedCols := make([]string, len(spec.columns))
		for i, column := range spec.columns {
			quotedCols[i] = quoteFirstAudioIdentifier(column)
		}
		quotedOrder := make([]string, len(spec.orderBy))
		for i, column := range spec.orderBy {
			quotedOrder[i] = quoteFirstAudioIdentifier(column)
		}
		query := "SELECT " + strings.Join(quotedCols, ",") + " FROM " + quoteFirstAudioIdentifier(spec.name) + " ORDER BY " + strings.Join(quotedOrder, ",")
		capture.begin("07_snapshot_"+spec.name, query)
		rows, err := conn.QueryContext(ctx, query)
		capture.end()
		if err != nil {
			t.Fatalf("capture readback %s: %v", spec.name, err)
		}
		for rows.Next() {
			row := make([]any, len(spec.columns))
			dst := make([]any, len(row))
			for i := range row {
				dst[i] = &row[i]
			}
			if err := rows.Scan(dst...); err != nil {
				_ = rows.Close()
				t.Fatalf("scan readback %s: %v", spec.name, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("iterate readback %s: %v", spec.name, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close readback %s: %v", spec.name, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal("close pinned capture connection:", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal("close capture pool:", err)
	}
	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatal("reopen fixture for post-reset verification:", err)
	}
	verifyPool, err := verify.DB()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := firstAudioCaptureDatabaseSnapshotWithKnownSchema(verify)
	if closeErr := verifyPool.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal("capture post-reset fixture snapshot:", err)
	}
	expectedTables := cloneFirstAudioSnapshotTables(baseline.Tables)
	lessonFound, oldSnapshotFound, newSnapshotBefore := false, false, false
	for ti := range expectedTables {
		table := &expectedTables[ti]
		if table.Name == "lessons" {
			idColumn := -1
			for i, column := range table.Columns {
				if column == "id" {
					idColumn = i
					break
				}
			}
			if idColumn < 0 {
				t.Fatal("lesson snapshot is missing id column")
			}
			for ri := range table.Rows {
				if table.Rows[ri].Values[idColumn].Value == oldID {
					lessonFound = true
					table.Rows[ri].Values[idColumn].Value = newID
				}
			}
		} else if table.Name == "runtime_records" {
			keyColumn := -1
			for i, column := range table.Columns {
				if column == "key" {
					keyColumn = i
					break
				}
			}
			if keyColumn < 0 {
				t.Fatal("runtime snapshot is missing key column")
			}
			kept := table.Rows[:0]
			for _, row := range table.Rows {
				if row.Values[keyColumn].Value == fmt.Sprintf("snapshot/%d", oldID) {
					oldSnapshotFound = true
					continue
				}
				if row.Values[keyColumn].Value == fmt.Sprintf("snapshot/%d", newID) {
					newSnapshotBefore = true
				}
				kept = append(kept, row)
			}
			table.Rows = kept
		}
	}
	if !lessonFound || !oldSnapshotFound || newSnapshotBefore {
		t.Fatalf("pre-reset fixture does not meet reset preconditions: lesson=%v old_snapshot=%v new_snapshot=%v", lessonFound, oldSnapshotFound, newSnapshotBefore)
	}
	if !reflect.DeepEqual(actual.Tables, expectedTables) || actual.SchemaSHA256 != baseline.SchemaSHA256 {
		t.Fatal("post-reset snapshot differs from exact expected projection (lesson ID only; old snapshot deleted)")
	}

	for _, statement := range capture.statements {
		if statement.Operation == "UNBOUND_CAPTURE_CALLBACK" {
			t.Fatalf("authorizer callbacks escaped driver-operation boundary: %#v", statement)
		}
	}
	version, versionNumber, sourceID := sqlite3.Version()
	// go.mod pins v1.14.22 and replaces it with the repository's audited fork.
	const driverModuleVersion = "v1.14.22 (repository fork)"
	driverSourceSHA256, err := firstAudioGoSQLiteSourceDigest()
	if err != nil {
		t.Fatal("digest local SQLite driver source:", err)
	}
	triggerBytes, err := os.ReadFile(filepath.Join("testdata", "first_audio_matrix_lesson_trigger_manifest.json"))
	if err != nil {
		t.Fatal("read frozen lesson trigger manifest:", err)
	}
	var triggerManifest firstAudioSchemaManifest
	if err := json.Unmarshal(triggerBytes, &triggerManifest); err != nil || triggerManifest.FormatVersion != 1 || triggerManifest.SHA256 == "" {
		t.Fatalf("lesson trigger manifest metadata is invalid: version=%d err=%v", triggerManifest.FormatVersion, err)
	}
	// Windows checkouts may use CRLF while Linux checkouts use LF. Hash a
	// canonical representation so the reviewed trigger manifest is identical
	// across the platform matrix.
	triggerBytes = canonicalFirstAudioManifestBytes(triggerBytes)
	triggerDigest := sha256.Sum256(triggerBytes)
	artifact := struct {
		FormatVersion          int                                `json:"format_version"`
		Driver                 string                             `json:"driver"`
		DriverModuleVersion    string                             `json:"driver_module_version"`
		DriverSourceSHA256     string                             `json:"driver_source_sha256"`
		SQLite                 map[string]any                     `json:"sqlite"`
		Runtime                string                             `json:"go_runtime"`
		SchemaSHA256           string                             `json:"schema_sha256"`
		TriggerManifestVersion int                                `json:"trigger_manifest_version"`
		TriggerManifestSHA256  string                             `json:"trigger_manifest_sha256"`
		Statements             []firstAudioResetCapturedStatement `json:"statements"`
	}{1, "github.com/mattn/go-sqlite3", driverModuleVersion, driverSourceSHA256, map[string]any{"version": version, "version_number": versionNumber, "source_id": sourceID}, runtime.Version(), schemaManifest.SHA256, triggerManifest.FormatVersion, hex.EncodeToString(triggerDigest[:]), capture.statements}
	canonical, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal("marshal reset manifest:", err)
	}
	digest := sha256.Sum256(canonical)
	output, err := json.MarshalIndent(struct {
		Digest                 string                             `json:"sha256"`
		FormatVersion          int                                `json:"format_version"`
		Driver                 string                             `json:"driver"`
		DriverModuleVersion    string                             `json:"driver_module_version"`
		DriverSourceSHA256     string                             `json:"driver_source_sha256"`
		SQLite                 map[string]any                     `json:"sqlite"`
		Runtime                string                             `json:"go_runtime"`
		SchemaSHA256           string                             `json:"schema_sha256"`
		TriggerManifestVersion int                                `json:"trigger_manifest_version"`
		TriggerManifestSHA256  string                             `json:"trigger_manifest_sha256"`
		Statements             []firstAudioResetCapturedStatement `json:"statements"`
	}{hex.EncodeToString(digest[:]), artifact.FormatVersion, artifact.Driver, artifact.DriverModuleVersion, artifact.DriverSourceSHA256, artifact.SQLite, artifact.Runtime, artifact.SchemaSHA256, artifact.TriggerManifestVersion, artifact.TriggerManifestSHA256, artifact.Statements}, "", "  ")
	if err != nil {
		t.Fatal("format reset manifest:", err)
	}
	manifestPath := filepath.Join("testdata", "first_audio_reset_authorizer_manifest.json")
	if os.Getenv("FIRST_AUDIO_WRITE_RESET_MANIFEST") == "1" {
		if err := writeFirstAudioResetManifestAtomically(manifestPath, append(output, '\n'), "."); err != nil {
			t.Fatal("write capture-only manifest:", err)
		}
		t.Logf("trace_id=reset_capture_20261029 module=first_audio_reset_capture event=manifest_written statement_count=%d schema_sha256=%s digest=%s", len(artifact.Statements), artifact.SchemaSHA256, hex.EncodeToString(digest[:]))
		return
	}
	want, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal("read reviewed reset authorizer manifest:", err)
	}
	if !bytes.Equal(canonicalFirstAudioManifestBytes(want), canonicalFirstAudioManifestBytes(append(output, '\n'))) {
		t.Fatalf("capture-only reset authorizer manifest drift: %s", firstAudioResetManifestDifference(want, append(output, '\n')))
	}
}

func canonicalFirstAudioManifestBytes(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func TestFirstAudioTriggerManifestDigestIgnoresLineEndings(t *testing.T) {
	lf := []byte("{\n  \"format_version\": 1\n}\n")
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	if !bytes.Equal(canonicalFirstAudioManifestBytes(lf), canonicalFirstAudioManifestBytes(crlf)) {
		t.Fatal("trigger manifest canonical bytes differ between LF and CRLF")
	}
	lfDigest := sha256.Sum256(canonicalFirstAudioManifestBytes(lf))
	crlfDigest := sha256.Sum256(canonicalFirstAudioManifestBytes(crlf))
	if lfDigest != crlfDigest {
		t.Fatal("trigger manifest digest differs between LF and CRLF")
	}
}

func firstAudioResetManifestDifference(wantJSON, actualJSON []byte) string {
	type manifestView struct {
		SHA256              string                             `json:"sha256"`
		FormatVersion       int                                `json:"format_version"`
		Driver              string                             `json:"driver"`
		DriverModuleVersion string                             `json:"driver_module_version"`
		DriverSourceSHA256  string                             `json:"driver_source_sha256"`
		SQLite              map[string]any                     `json:"sqlite"`
		Runtime             string                             `json:"go_runtime"`
		SchemaSHA256        string                             `json:"schema_sha256"`
		TriggerVersion      int                                `json:"trigger_manifest_version"`
		TriggerSHA256       string                             `json:"trigger_manifest_sha256"`
		Statements          []firstAudioResetCapturedStatement `json:"statements"`
	}
	var want, actual manifestView
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		return "checked-in manifest is invalid JSON"
	}
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		return "captured manifest is invalid JSON"
	}
	differences := make([]string, 0)
	if want.SHA256 != actual.SHA256 {
		differences = append(differences, "sha256")
	}
	if want.FormatVersion != actual.FormatVersion {
		differences = append(differences, "format_version")
	}
	if want.Driver != actual.Driver {
		differences = append(differences, "driver")
	}
	if want.DriverModuleVersion != actual.DriverModuleVersion {
		differences = append(differences, "driver_module_version")
	}
	if want.DriverSourceSHA256 != actual.DriverSourceSHA256 {
		differences = append(differences, "driver_source_sha256")
	}
	if !reflect.DeepEqual(want.SQLite, actual.SQLite) {
		differences = append(differences, "sqlite_build")
	}
	if want.Runtime != actual.Runtime {
		differences = append(differences, "go_runtime")
	}
	if want.SchemaSHA256 != actual.SchemaSHA256 {
		differences = append(differences, "schema_sha256")
	}
	if want.TriggerVersion != actual.TriggerVersion {
		differences = append(differences, "trigger_manifest_version")
	}
	if want.TriggerSHA256 != actual.TriggerSHA256 {
		differences = append(differences, "trigger_manifest_sha256")
	}
	if len(want.Statements) != len(actual.Statements) {
		differences = append(differences, fmt.Sprintf("statement_count:%d!=%d", len(want.Statements), len(actual.Statements)))
	}
	for i := 0; i < len(want.Statements) && i < len(actual.Statements); i++ {
		left, right := want.Statements[i], actual.Statements[i]
		if left.Sequence != right.Sequence || left.Operation != right.Operation || left.SQL != right.SQL || !reflect.DeepEqual(left.Tuples, right.Tuples) {
			detail := fmt.Sprintf("statement[%d]", i+1)
			if left.Sequence != right.Sequence {
				detail += fmt.Sprintf(" sequence:%d!=%d", left.Sequence, right.Sequence)
			}
			if left.Operation != right.Operation {
				detail += fmt.Sprintf(" operation:%s!=%s", left.Operation, right.Operation)
			}
			if left.SQL != right.SQL {
				detail += fmt.Sprintf(" sql_template:%s!=%s", left.SQL, right.SQL)
			}
			if len(left.Tuples) != len(right.Tuples) {
				detail += fmt.Sprintf(" tuple_count:%d!=%d", len(left.Tuples), len(right.Tuples))
			}
			for ti := 0; ti < len(left.Tuples) && ti < len(right.Tuples); ti++ {
				if left.Tuples[ti] != right.Tuples[ti] {
					detail += fmt.Sprintf(" tuple[%d]:%#v!=%#v", ti, left.Tuples[ti], right.Tuples[ti])
					break
				}
			}
			differences = append(differences, detail)
			break
		}
	}
	if len(differences) == 0 {
		return "canonical JSON or digest differs"
	}
	return strings.Join(differences, "; ")
}

func writeFirstAudioResetManifestAtomically(path string, content []byte, trustedRoot string) (resultErr error) {
	// This writer is reachable only through the explicit local developer opt-in
	// below. It rejects static symlinks and atomically replaces the artifact,
	// but does not claim dirfd-level protection against a hostile process that
	// concurrently swaps the trusted repository checkout itself.
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return fmt.Errorf("refuse non-directory or symlink manifest parent")
	}
	if resolved, err := filepath.EvalSymlinks(parent); err != nil {
		return err
	} else if !sameResolvedFirstAudioResetManifestPath(resolved, parent, trustedRoot) {
		return fmt.Errorf("manifest parent resolves through a different path")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refuse non-regular manifest target")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".first-audio-reset-manifest-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) && resultErr == nil {
			resultErr = removeErr
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFirstAudioResetManifest(temporaryPath, path)
}

func sameResolvedFirstAudioResetManifestPath(resolved, input, trustedRoot string) bool {
	resolvedAbs, resolvedErr := filepath.Abs(resolved)
	inputAbs, inputErr := filepath.Abs(input)
	rootAbs, rootErr := filepath.Abs(trustedRoot)
	if resolvedErr != nil || inputErr != nil || rootErr != nil {
		return false
	}
	resolvedAbs = filepath.Clean(resolvedAbs)
	inputAbs = filepath.Clean(inputAbs)
	rootAbs = filepath.Clean(rootAbs)
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	relative, err := filepath.Rel(rootAbs, inputAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false
	}
	expectedResolved := filepath.Clean(filepath.Join(resolvedRoot, relative))
	if runtime.GOOS == "windows" {
		// EvalSymlinks may return extended paths and the runner's checkout root
		// may itself have an alias. Compare relative to the canonical trusted
		// root, then apply Windows' case-insensitive path semantics.
		resolvedAbs = normalizeWindowsResetManifestPath(resolvedAbs)
		expectedResolved = normalizeWindowsResetManifestPath(expectedResolved)
		return strings.EqualFold(resolvedAbs, expectedResolved)
	}
	return resolvedAbs == expectedResolved
}

func normalizeWindowsResetManifestPath(path string) string {
	const extendedUNC = `\\?\UNC\`
	const extendedPath = `\\?\`
	if strings.HasPrefix(strings.ToUpper(path), extendedUNC) {
		return `\\` + path[len(extendedUNC):]
	}
	if strings.HasPrefix(strings.ToUpper(path), extendedPath) {
		return path[len(extendedPath):]
	}
	return path
}

func TestFirstAudioResetManifestWriterRejectsSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	regularTarget := filepath.Join(root, "regular.json")
	if err := os.WriteFile(regularTarget, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFirstAudioResetManifestAtomically(regularTarget, []byte("after"), root); err != nil {
		t.Fatal("atomic regular-file replacement failed:", err)
	}
	regularContent, err := os.ReadFile(regularTarget)
	if err != nil || string(regularContent) != "after" {
		t.Fatalf("atomic replacement content=%q err=%v", regularContent, err)
	}
	external := filepath.Join(root, "external.json")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "target.json")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := writeFirstAudioResetManifestAtomically(link, []byte("replace"), root); err == nil {
		t.Fatal("writer accepted a symlink target")
	}
	content, err := os.ReadFile(external)
	if err != nil || string(content) != "keep" {
		t.Fatalf("symlink target changed: content=%q err=%v", content, err)
	}
	externalDir := filepath.Join(root, "external-dir")
	if err := os.Mkdir(externalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	externalFile := filepath.Join(externalDir, "manifest.json")
	if err := os.WriteFile(externalFile, []byte("parent-keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(root, "linked-parent")
	if err := os.Symlink(externalDir, parentLink); err != nil {
		t.Skipf("directory symlink creation unavailable: %v", err)
	}
	if err := writeFirstAudioResetManifestAtomically(filepath.Join(parentLink, "manifest.json"), []byte("parent-replace"), root); err == nil {
		t.Fatal("writer accepted a symlink parent")
	}
	content, err = os.ReadFile(externalFile)
	if err != nil || string(content) != "parent-keep" {
		t.Fatalf("symlink-parent target changed: content=%q err=%v", content, err)
	}
	ancestorLink := filepath.Join(root, "linked-ancestor")
	if err := os.Symlink(root, ancestorLink); err != nil {
		t.Skipf("ancestor symlink creation unavailable: %v", err)
	}
	if err := writeFirstAudioResetManifestAtomically(filepath.Join(ancestorLink, "external-dir", "manifest.json"), []byte("ancestor-replace"), root); err == nil {
		t.Fatal("writer accepted a parent reached through a symlink ancestor")
	}
	content, err = os.ReadFile(externalFile)
	if err != nil || string(content) != "parent-keep" {
		t.Fatalf("symlink-ancestor target changed: content=%q err=%v", content, err)
	}
}

func firstAudioGoSQLiteSourceDigest() (string, error) {
	root, err := filepath.Abs(filepath.Join("..", "..", "third_party", "go-sqlite3"))
	if err != nil {
		return "", err
	}
	files := make([]string, 0, 64)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("driver source contains symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("driver source contains non-regular file %q", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, rel := range files {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
		_, _ = fmt.Fprintf(hash, "%d:", len(rel))
		_, _ = hash.Write([]byte(rel))
		_, _ = fmt.Fprintf(hash, "%d:", len(content))
		_, _ = hash.Write(content)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func cloneFirstAudioSnapshotTables(source []firstAudioSnapshotTable) []firstAudioSnapshotTable {
	clone := make([]firstAudioSnapshotTable, len(source))
	for i, table := range source {
		clone[i] = firstAudioSnapshotTable{Name: table.Name, Columns: append([]string(nil), table.Columns...), Rows: make([]firstAudioSnapshotRow, len(table.Rows))}
		for j, row := range table.Rows {
			clone[i].Rows[j].Values = append([]firstAudioSnapshotCell(nil), row.Values...)
		}
	}
	return clone
}

func firstAudioLessonColumns(t *testing.T) []string {
	t.Helper()
	parsed, err := schema.Parse(&store.Lesson{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal("parse lesson model:", err)
	}
	columns := make([]string, 0, len(parsed.Fields))
	for _, field := range parsed.Fields {
		if field.DBName != "" {
			columns = append(columns, quoteFirstAudioIdentifier(field.DBName))
		}
	}
	return columns
}

func firstAudioResetSnapshotSpecs(t *testing.T) []firstAudioSnapshotSpec {
	t.Helper()
	cache := &sync.Map{}
	specs := make([]firstAudioSnapshotSpec, 0, 24)
	for _, model := range firstAudioStartupModels() {
		parsed, err := schema.Parse(model, cache, schema.NamingStrategy{})
		if err != nil {
			t.Fatal("parse snapshot model:", err)
		}
		spec := firstAudioSnapshotSpec{name: parsed.Table}
		for _, field := range parsed.Fields {
			if field.DBName != "" {
				spec.columns = append(spec.columns, field.DBName)
			}
		}
		for _, field := range parsed.PrimaryFields {
			if field.DBName != "" {
				spec.orderBy = append(spec.orderBy, field.DBName)
			}
		}
		if len(spec.orderBy) == 0 {
			spec.orderBy = append(spec.orderBy, spec.columns...)
		}
		specs = append(specs, spec)
	}
	specs = append(specs, firstAudioSnapshotSpec{name: "sqlite_sequence", columns: []string{"name", "seq"}, orderBy: []string{"name", "seq"}})
	sort.Slice(specs, func(i, j int) bool { return specs[i].name < specs[j].name })
	return specs
}

//go:build cgo

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"enwords/internal/store"
	"gorm.io/gorm"
)

type firstAudioSQLiteObject struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Table string `json:"table" gorm:"column:tbl_name"`
	SQL   string `json:"sql"`
}

type firstAudioSchemaManifest struct {
	FormatVersion int                      `json:"format_version"`
	SHA256        string                   `json:"sha256"`
	Objects       []firstAudioSQLiteObject `json:"objects"`
}

func firstAudioReadSchemaManifest(db *gorm.DB) (firstAudioSchemaManifest, []byte, error) {
	objects := make([]firstAudioSQLiteObject, 0)
	err := db.Raw(`SELECT type, name, tbl_name, COALESCE(sql, '') AS sql FROM sqlite_master ORDER BY type, name, tbl_name`).Scan(&objects).Error
	if err != nil {
		return firstAudioSchemaManifest{}, nil, err
	}
	canonical, err := json.Marshal(objects)
	if err != nil {
		return firstAudioSchemaManifest{}, nil, err
	}
	digest := sha256.Sum256(canonical)
	manifest := firstAudioSchemaManifest{FormatVersion: 1, SHA256: hex.EncodeToString(digest[:]), Objects: objects}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	return manifest, encoded, err
}

func firstAudioValidateSchemaManifest(db *gorm.DB) error {
	actual, actualJSON, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return fmt.Errorf("read SQLite schema inventory: %w", err)
	}
	manifestPath := filepath.Join("testdata", "first_audio_matrix_schema_manifest.json")
	baselineJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read checked-in schema inventory %s: %w\nactual manifest follows:\n%s", manifestPath, err, actualJSON)
	}
	var expected firstAudioSchemaManifest
	if err := json.Unmarshal(baselineJSON, &expected); err != nil {
		return fmt.Errorf("decode checked-in schema inventory: %w", err)
	}
	if expected.FormatVersion != 1 || expected.SHA256 == "" || len(expected.Objects) == 0 {
		return fmt.Errorf("checked-in schema inventory is incomplete\nactual manifest follows:\n%s", actualJSON)
	}
	if !reflect.DeepEqual(expected, actual) {
		expectedJSON, _ := json.MarshalIndent(expected, "", "  ")
		return fmt.Errorf("SQLite schema drift: expected sha256=%s (%d objects), actual sha256=%s (%d objects)\nexpected:\n%s\nactual:\n%s", expected.SHA256, len(expected.Objects), actual.SHA256, len(actual.Objects), expectedJSON, actualJSON)
	}
	return nil
}

func firstAudioReadPhysicalColumnManifest(db *gorm.DB) (map[string]map[string]struct{}, error) {
	schemaManifest, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return nil, fmt.Errorf("read schema for physical column manifest: %w", err)
	}
	objects := append([]firstAudioSQLiteObject(nil), schemaManifest.Objects...)
	// sqlite_master is a read-only SQLite catalog table and is not listed in
	// sqlite_master itself, so include its physical columns explicitly.
	objects = append(objects, firstAudioSQLiteObject{Type: "table", Name: "sqlite_master"})
	columnsByTable := make(map[string]map[string]struct{})
	for _, object := range objects {
		if object.Type != "table" {
			continue
		}
		if _, exists := columnsByTable[object.Name]; exists {
			return nil, fmt.Errorf("duplicate physical table in SQLite schema: %s", object.Name)
		}
		rows, err := db.Raw("PRAGMA table_xinfo(" + quoteFirstAudioIdentifier(object.Name) + ")").Rows()
		if err != nil {
			return nil, fmt.Errorf("read physical columns for %s: %w", object.Name, err)
		}
		columns := make(map[string]struct{})
		for rows.Next() {
			var cid, notNull, primaryKey, hidden int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan physical columns for %s: %w", object.Name, err)
			}
			if _, exists := columns[name]; exists {
				_ = rows.Close()
				return nil, fmt.Errorf("duplicate physical column %s.%s", object.Name, name)
			}
			columns[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate physical columns for %s: %w", object.Name, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close physical column rows for %s: %w", object.Name, err)
		}
		if len(columns) == 0 {
			return nil, fmt.Errorf("SQLite table has no physical columns: %s", object.Name)
		}
		columnsByTable[object.Name] = columns
	}
	return columnsByTable, nil
}

func firstAudioCheckReadManifestColumns(readManifest, physical map[string]map[string]struct{}) error {
	missingTables := make([]string, 0)
	for table := range physical {
		if _, ok := readManifest[table]; !ok {
			missingTables = append(missingTables, table)
		}
	}
	extraTables := make([]string, 0)
	for table := range readManifest {
		if _, ok := physical[table]; !ok {
			extraTables = append(extraTables, table)
		}
	}
	sort.Strings(missingTables)
	sort.Strings(extraTables)
	columnDiffs := make([]string, 0)
	missingCountSentinel := make([]string, 0)
	for table, physicalColumns := range physical {
		allowedColumns, ok := readManifest[table]
		if !ok {
			continue
		}
		if _, ok := allowedColumns[""]; !ok {
			missingCountSentinel = append(missingCountSentinel, table)
		}
		missing := make([]string, 0)
		extra := make([]string, 0)
		for column := range physicalColumns {
			if _, ok := allowedColumns[column]; !ok {
				missing = append(missing, column)
			}
		}
		for column := range allowedColumns {
			if column == "" {
				continue
			}
			if _, ok := physicalColumns[column]; !ok {
				extra = append(extra, column)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) != 0 || len(extra) != 0 {
			columnDiffs = append(columnDiffs, fmt.Sprintf("%s: unauthorized=%v missing=%v", table, missing, extra))
		}
	}
	sort.Strings(columnDiffs)
	sort.Strings(missingCountSentinel)
	if len(missingTables) != 0 || len(extraTables) != 0 || len(columnDiffs) != 0 || len(missingCountSentinel) != 0 {
		return fmt.Errorf("SQLite read manifest drift: missing_tables=%v extra_tables=%v column_diffs=%v missing_count_sentinel=%v", missingTables, extraTables, columnDiffs, missingCountSentinel)
	}
	return nil
}

func firstAudioValidateStartupReadManifest(db *gorm.DB) error {
	physical, err := firstAudioReadPhysicalColumnManifest(db)
	if err != nil {
		return err
	}
	if err := firstAudioCheckReadManifestColumns(firstAudioStartupReadManifest(), physical); err != nil {
		return err
	}
	return nil
}

func TestFirstAudioMatrixSchemaObjectManifest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "schema-inventory.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal("open migrated schema fixture:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("get migrated schema pool:", err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close migrated schema pool: %v", err)
		}
	}()
	if err := installFirstAudioStartupGuards(db); err != nil {
		t.Fatal("install reviewed startup guards:", err)
	}

	if err := firstAudioValidateSchemaManifest(db); err != nil {
		t.Fatal(err)
	}
	if err := firstAudioValidateStartupReadManifest(db); err != nil {
		t.Fatal("startup read manifest must match pinned physical schema:", err)
	}
}

func TestFirstAudioMatrixSchemaManifestRejectsUnexpectedObject(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "schema-drift.db"))
	if err != nil {
		t.Fatal("open migrated schema fixture:", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("get migrated schema pool:", err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close migrated schema pool: %v", err)
		}
	}()
	if err := installFirstAudioStartupGuards(db); err != nil {
		t.Fatal("install expected schema guards:", err)
	}
	if err := db.Exec(`CREATE TABLE unreviewed_matrix_object (id INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatal("create drift probe:", err)
	}
	if err := firstAudioValidateSchemaManifest(db); err == nil {
		t.Fatal("schema validator accepted an unreviewed SQLite object")
	} else if !strings.Contains(err.Error(), "SQLite schema drift") {
		t.Fatalf("schema mismatch failed for the wrong reason: %v", err)
	}
}

func TestFirstAudioMatrixReadManifestMatchesPhysicalColumns(t *testing.T) {
	readManifest := map[string]map[string]struct{}{
		"users":         {"": {}, "id": {}, "username": {}},
		"sqlite_master": {"": {}, "type": {}, "name": {}},
	}
	physical := map[string]map[string]struct{}{
		"users":         {"id": {}, "username": {}},
		"sqlite_master": {"type": {}, "name": {}},
	}
	if err := firstAudioCheckReadManifestColumns(readManifest, physical); err != nil {
		t.Fatalf("matching physical and authorized columns rejected: %v", err)
	}
}

func TestFirstAudioMatrixReadManifestRejectsColumnDrift(t *testing.T) {
	tests := []struct {
		name         string
		readManifest map[string]map[string]struct{}
		physical     map[string]map[string]struct{}
		want         string
	}{
		{
			name:         "missing authorized physical column",
			readManifest: map[string]map[string]struct{}{"users": {"": {}, "id": {}, "username": {}}},
			physical:     map[string]map[string]struct{}{"users": {"id": {}}},
			want:         "missing=[username]",
		},
		{
			name:         "physical column is not authorized",
			readManifest: map[string]map[string]struct{}{"users": {"": {}, "id": {}}},
			physical:     map[string]map[string]struct{}{"users": {"id": {}, "secret": {}}},
			want:         "unauthorized=[secret]",
		},
		{
			name:         "missing count sentinel",
			readManifest: map[string]map[string]struct{}{"users": {"id": {}}},
			physical:     map[string]map[string]struct{}{"users": {"id": {}}},
			want:         "missing_count_sentinel=[users]",
		},
		{
			name:         "unmodeled physical table",
			readManifest: map[string]map[string]struct{}{"users": {"": {}, "id": {}}},
			physical:     map[string]map[string]struct{}{"users": {"id": {}}, "new_table": {"id": {}}},
			want:         "missing_tables=[new_table]",
		},
		{
			name:         "read manifest names absent physical table",
			readManifest: map[string]map[string]struct{}{"users": {"": {}, "id": {}}, "missing_table": {"": {}, "id": {}}},
			physical:     map[string]map[string]struct{}{"users": {"id": {}}},
			want:         "extra_tables=[missing_table]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := firstAudioCheckReadManifestColumns(tt.readManifest, tt.physical)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected read-manifest drift containing %q, got %v", tt.want, err)
			}
		})
	}
}

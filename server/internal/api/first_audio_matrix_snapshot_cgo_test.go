//go:build cgo

package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type firstAudioSnapshotCell struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type firstAudioSnapshotRow struct {
	Values []firstAudioSnapshotCell `json:"values"`
}

type firstAudioSnapshotTable struct {
	Name    string                  `json:"name"`
	Columns []string                `json:"columns"`
	Rows    []firstAudioSnapshotRow `json:"rows"`
}

type firstAudioDatabaseSnapshot struct {
	SHA256       string                    `json:"sha256"`
	SchemaSHA256 string                    `json:"schema_sha256"`
	Tables       []firstAudioSnapshotTable `json:"tables"`
	Canonical    []byte                    `json:"-"`
}

type firstAudioSnapshotSpec struct {
	name    string
	columns []string
	orderBy []string
}

func firstAudioCaptureDatabaseSnapshot(db *gorm.DB) (firstAudioDatabaseSnapshot, error) {
	if err := firstAudioValidateSchemaManifest(db); err != nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("validate schema before snapshot: %w", err)
	}
	return firstAudioCaptureDatabaseSnapshotWithKnownSchema(db)
}

// firstAudioCaptureDatabaseSnapshotWithKnownSchema is used only after the
// immutable schema manifest has been verified and the reviewed lesson guard
// triggers have been added. The actual sqlite_master digest remains part of
// every snapshot even though the extra trigger objects are validated by their
// dedicated manifest.
func firstAudioCaptureDatabaseSnapshotWithKnownSchema(db *gorm.DB) (firstAudioDatabaseSnapshot, error) {
	cache := &sync.Map{}
	specs := make([]firstAudioSnapshotSpec, 0, 24)
	for _, model := range firstAudioStartupModels() {
		parsed, err := schema.Parse(model, cache, schema.NamingStrategy{})
		if err != nil {
			return firstAudioDatabaseSnapshot{}, fmt.Errorf("parse snapshot model: %w", err)
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
	// SQLite creates this internal table for AUTOINCREMENT sequences. Include
	// it in every boundary snapshot so ID allocator changes cannot go unnoticed.
	specs = append(specs, firstAudioSnapshotSpec{
		name:    "sqlite_sequence",
		columns: []string{"name", "seq"},
		orderBy: []string{"name", "seq"},
	})
	sort.Slice(specs, func(i, j int) bool { return specs[i].name < specs[j].name })
	actualSchema, _, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("read schema table set for snapshot: %w", err)
	}
	if err := firstAudioCheckSnapshotTableCoverage(specs, actualSchema.Objects); err != nil {
		return firstAudioDatabaseSnapshot{}, err
	}

	snapshot := firstAudioDatabaseSnapshot{SchemaSHA256: actualSchema.SHA256, Tables: make([]firstAudioSnapshotTable, 0, len(specs))}
	for _, spec := range specs {
		selected := make([]string, len(spec.columns))
		for i, column := range spec.columns {
			selected[i] = quoteFirstAudioIdentifier(column)
		}
		ordered := make([]string, len(spec.orderBy))
		for i, column := range spec.orderBy {
			ordered[i] = quoteFirstAudioIdentifier(column)
		}
		query := "SELECT " + strings.Join(selected, ",") + " FROM " + quoteFirstAudioIdentifier(spec.name) + " ORDER BY " + strings.Join(ordered, ",")
		rows, err := db.Raw(query).Rows()
		if err != nil {
			return firstAudioDatabaseSnapshot{}, fmt.Errorf("query snapshot table %s: %w", spec.name, err)
		}
		table := firstAudioSnapshotTable{
			Name:    spec.name,
			Columns: append([]string(nil), spec.columns...),
			Rows:    make([]firstAudioSnapshotRow, 0),
		}
		for rows.Next() {
			values := make([]any, len(spec.columns))
			destinations := make([]any, len(values))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				return firstAudioDatabaseSnapshot{}, fmt.Errorf("scan snapshot table %s: %w", spec.name, err)
			}
			row := firstAudioSnapshotRow{Values: make([]firstAudioSnapshotCell, len(values))}
			for i, value := range values {
				cell, err := normalizeFirstAudioSnapshotCell(value)
				if err != nil {
					_ = rows.Close()
					return firstAudioDatabaseSnapshot{}, fmt.Errorf("normalize %s.%s: %w", spec.name, spec.columns[i], err)
				}
				row.Values[i] = cell
			}
			table.Rows = append(table.Rows, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return firstAudioDatabaseSnapshot{}, fmt.Errorf("iterate snapshot table %s: %w", spec.name, err)
		}
		if err := rows.Close(); err != nil {
			return firstAudioDatabaseSnapshot{}, fmt.Errorf("close snapshot rows for %s: %w", spec.name, err)
		}
		snapshot.Tables = append(snapshot.Tables, table)
	}
	canonical, err := json.Marshal(struct {
		SchemaSHA256 string                    `json:"schema_sha256"`
		Tables       []firstAudioSnapshotTable `json:"tables"`
	}{SchemaSHA256: snapshot.SchemaSHA256, Tables: snapshot.Tables})
	if err != nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("encode schema and data snapshot digest input: %w", err)
	}
	digest := sha256.Sum256(canonical)
	snapshot.SHA256 = hex.EncodeToString(digest[:])
	snapshot.Canonical, err = json.MarshalIndent(struct {
		SHA256       string                    `json:"sha256"`
		SchemaSHA256 string                    `json:"schema_sha256"`
		Tables       []firstAudioSnapshotTable `json:"tables"`
	}{SHA256: snapshot.SHA256, SchemaSHA256: snapshot.SchemaSHA256, Tables: snapshot.Tables}, "", "  ")
	if err != nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("encode diagnostic database snapshot: %w", err)
	}
	return snapshot, nil
}

// firstAudioCaptureDatabaseSnapshotOnPinnedConn forces every GORM query onto
// the supplied sql.Conn. It is used after the whole application pool has been
// pinned so readback cannot reacquire from an exhausted *sql.DB pool.
func firstAudioCaptureDatabaseSnapshotOnPinnedConn(base *gorm.DB, conn *sql.Conn) (firstAudioDatabaseSnapshot, error) {
	if base == nil || base.Config == nil || base.Statement == nil || conn == nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("pinned snapshot requires base DB and sql.Conn")
	}
	baseConfig := base.Config
	baseStatement := base.Statement
	baseConfigPool := base.Config.ConnPool
	baseStatementPool := base.Statement.ConnPool
	local := base.Session(&gorm.Session{NewDB: true, Initialized: true})
	if local.Config == baseConfig || local.Statement == baseStatement {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("pinned snapshot requires isolated GORM config and statement")
	}
	local.Config.ConnPool = conn
	local.Statement.ConnPool = conn
	snapshot, err := firstAudioCaptureDatabaseSnapshotWithKnownSchema(local)
	if base.Config != baseConfig || base.Statement != baseStatement || base.Config.ConnPool != baseConfigPool || base.Statement.ConnPool != baseStatementPool {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("pinned snapshot mutated shared GORM pool configuration")
	}
	return snapshot, err
}

func firstAudioCheckSnapshotTableCoverage(specs []firstAudioSnapshotSpec, objects []firstAudioSQLiteObject) error {
	schemaTables := make(map[string]struct{})
	for _, object := range objects {
		if object.Type == "table" {
			schemaTables[object.Name] = struct{}{}
		}
	}
	snapshotTables := make(map[string]struct{}, len(specs))
	duplicates := make([]string, 0)
	for _, spec := range specs {
		if _, exists := snapshotTables[spec.name]; exists {
			duplicates = append(duplicates, spec.name)
		}
		snapshotTables[spec.name] = struct{}{}
	}
	missing := make([]string, 0)
	for table := range schemaTables {
		if _, ok := snapshotTables[table]; !ok {
			missing = append(missing, table)
		}
	}
	extra := make([]string, 0)
	for table := range snapshotTables {
		if _, ok := schemaTables[table]; !ok {
			extra = append(extra, table)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(duplicates)
	if len(missing) != 0 || len(extra) != 0 || len(duplicates) != 0 {
		return fmt.Errorf("snapshot table coverage differs from schema: missing=%v extra=%v duplicate_specs=%v", missing, extra, duplicates)
	}
	return nil
}

func TestFirstAudioMatrixSnapshotTableCoverageRejectsUnmodeledTable(t *testing.T) {
	specs := []firstAudioSnapshotSpec{{name: "users"}, {name: "sqlite_sequence"}}
	objects := []firstAudioSQLiteObject{
		{Type: "table", Name: "users"},
		{Type: "table", Name: "sqlite_sequence"},
		{Type: "table", Name: "new_startup_model"},
	}
	if err := firstAudioCheckSnapshotTableCoverage(specs, objects); err == nil {
		t.Fatal("snapshot coverage accepted a schema table with no row snapshot")
	}
}

func TestFirstAudioMatrixSnapshotTableCoverageRejectsDuplicateSpec(t *testing.T) {
	specs := []firstAudioSnapshotSpec{{name: "users"}, {name: "users"}}
	objects := []firstAudioSQLiteObject{{Type: "table", Name: "users"}}
	if err := firstAudioCheckSnapshotTableCoverage(specs, objects); err == nil {
		t.Fatal("snapshot coverage accepted duplicate table specs")
	}
}

func TestFirstAudioMatrixSnapshotNormalizesTimeAsUTC(t *testing.T) {
	value := time.Date(2026, time.October, 27, 12, 30, 0, 123000000, time.FixedZone("test", 8*60*60))
	cell, err := normalizeFirstAudioSnapshotCell(value)
	if err != nil {
		t.Fatalf("normalize time: %v", err)
	}
	if cell.Type != "text" || cell.Value != "2026-10-27T04:30:00.123Z" {
		t.Fatalf("unexpected normalized time: %#v", cell)
	}
}

func normalizeFirstAudioSnapshotCell(value any) (firstAudioSnapshotCell, error) {
	switch value := value.(type) {
	case nil:
		return firstAudioSnapshotCell{Type: "null"}, nil
	case int64:
		return firstAudioSnapshotCell{Type: "integer", Value: value}, nil
	case float64:
		return firstAudioSnapshotCell{Type: "real", Value: value}, nil
	case string:
		return firstAudioSnapshotCell{Type: "text", Value: value}, nil
	case []byte:
		return firstAudioSnapshotCell{Type: "blob", Value: base64.StdEncoding.EncodeToString(value)}, nil
	case time.Time:
		return firstAudioSnapshotCell{Type: "text", Value: value.UTC().Format(time.RFC3339Nano)}, nil
	default:
		return firstAudioSnapshotCell{}, fmt.Errorf("unsupported SQLite value type %T", value)
	}
}

func quoteFirstAudioIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

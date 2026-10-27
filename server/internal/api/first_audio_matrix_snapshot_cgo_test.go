//go:build cgo

package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

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
	SHA256    string                    `json:"sha256"`
	Tables    []firstAudioSnapshotTable `json:"tables"`
	Canonical []byte                    `json:"-"`
}

type firstAudioSnapshotSpec struct {
	name    string
	columns []string
	orderBy []string
}

func firstAudioCaptureDatabaseSnapshot(db *gorm.DB) (firstAudioDatabaseSnapshot, error) {
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

	snapshot := firstAudioDatabaseSnapshot{Tables: make([]firstAudioSnapshotTable, 0, len(specs))}
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
	canonical, err := json.Marshal(snapshot.Tables)
	if err != nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("encode canonical database snapshot: %w", err)
	}
	digest := sha256.Sum256(canonical)
	snapshot.SHA256 = hex.EncodeToString(digest[:])
	snapshot.Canonical, err = json.MarshalIndent(struct {
		SHA256 string                    `json:"sha256"`
		Tables []firstAudioSnapshotTable `json:"tables"`
	}{SHA256: snapshot.SHA256, Tables: snapshot.Tables}, "", "  ")
	if err != nil {
		return firstAudioDatabaseSnapshot{}, fmt.Errorf("encode diagnostic database snapshot: %w", err)
	}
	return snapshot, nil
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
	default:
		return firstAudioSnapshotCell{}, fmt.Errorf("unsupported SQLite value type %T", value)
	}
}

func quoteFirstAudioIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

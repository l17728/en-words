//go:build cgo

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"enwords/internal/store"
	"gorm.io/gorm"
)

type firstAudioSQLiteObject struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
}

type firstAudioSchemaManifest struct {
	FormatVersion int                     `json:"format_version"`
	SHA256        string                  `json:"sha256"`
	Objects       []firstAudioSQLiteObject `json:"objects"`
}

func firstAudioReadSchemaManifest(db *gorm.DB) (firstAudioSchemaManifest, []byte, error) {
	objects := make([]firstAudioSQLiteObject, 0)
	err := db.Raw(`SELECT type, name, tbl_name AS table, COALESCE(sql, '') AS sql FROM sqlite_master ORDER BY type, name, tbl_name`).Scan(&objects).Error
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

	actual, actualJSON, err := firstAudioReadSchemaManifest(db)
	if err != nil {
		t.Fatal("read SQLite schema inventory:", err)
	}
	manifestPath := filepath.Join("testdata", "first_audio_matrix_schema_manifest.json")
	baselineJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read checked-in schema inventory %s: %v\nactual manifest follows:\n%s", manifestPath, err, actualJSON)
	}
	var expected firstAudioSchemaManifest
	if err := json.Unmarshal(baselineJSON, &expected); err != nil {
		t.Fatalf("decode checked-in schema inventory: %v", err)
	}
	if expected.FormatVersion != 1 || expected.SHA256 == "" || len(expected.Objects) == 0 {
		t.Fatalf("checked-in schema inventory is incomplete; actual manifest follows:\n%s", actualJSON)
	}
	if !reflect.DeepEqual(expected, actual) {
		expectedJSON, _ := json.MarshalIndent(expected, "", "  ")
		t.Fatalf("SQLite schema drift: expected sha256=%s (%d objects), actual sha256=%s (%d objects)\nexpected:\n%s\nactual:\n%s", expected.SHA256, len(expected.Objects), actual.SHA256, len(actual.Objects), expectedJSON, actualJSON)
	}
}


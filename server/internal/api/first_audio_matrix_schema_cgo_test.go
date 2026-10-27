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
	if err := db.Exec(`CREATE TABLE unreviewed_matrix_object (id INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatal("create drift probe:", err)
	}
	if err := firstAudioValidateSchemaManifest(db); err == nil {
		t.Fatal("schema validator accepted an unreviewed SQLite object")
	} else if !strings.Contains(err.Error(), "SQLite schema drift") {
		t.Fatalf("schema mismatch failed for the wrong reason: %v", err)
	}
}

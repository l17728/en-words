//go:build cgo

package api

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"enwords/internal/store"
	sqlite3 "github.com/mattn/go-sqlite3"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type firstAudioUpsertObservation struct {
	mu       sync.Mutex
	events   []string
	mode     string
	key      string
	oldValue *string
	newValue string
}

func (o *firstAudioUpsertObservation) record(event, oldKey, oldValue, newKey, newValue string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
	if newKey != o.key || newValue != o.newValue {
		return false
	}
	switch event {
	case "INSERT":
		return oldKey == "" && oldValue == ""
	case "UPDATE":
		return o.mode == "update" && o.oldValue != nil && oldKey == o.key && oldValue == *o.oldValue
	default:
		return false
	}
}

func (o *firstAudioUpsertObservation) configure(mode, key string, oldValue *string, newValue string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode, o.key, o.newValue = mode, key, newValue
	if oldValue == nil {
		o.oldValue = nil
	} else {
		copy := *oldValue
		o.oldValue = &copy
	}
	o.events = nil
}

func (o *firstAudioUpsertObservation) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

var firstAudioUpsertDriverSequence atomic.Uint64

func openFirstAudioUpsertGuardDB(t *testing.T) (*gorm.DB, *firstAudioUpsertObservation) {
	t.Helper()
	observation := &firstAudioUpsertObservation{}
	driverName := fmt.Sprintf("first_audio_upsert_guard_%d", firstAudioUpsertDriverSequence.Add(1))
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		return conn.RegisterFunc("first_audio_matrix_guard", observation.record, false)
	}})
	path := filepath.Join(t.TempDir(), "upsert-guard.sqlite")
	db, err := gorm.Open(gormsqlite.New(gormsqlite.Config{DriverName: driverName, DSN: path}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open guarded test DB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	setup := []string{
		`CREATE TABLE runtime_records (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TRIGGER runtime_guard_insert BEFORE INSERT ON runtime_records BEGIN
			SELECT CASE WHEN first_audio_matrix_guard('INSERT', '', '', NEW.key, NEW.value)=0 THEN RAISE(ABORT, 'matrix insert denied') END;
		END`,
		`CREATE TRIGGER runtime_guard_update BEFORE UPDATE ON runtime_records BEGIN
			SELECT CASE WHEN first_audio_matrix_guard('UPDATE', OLD.key, OLD.value, NEW.key, NEW.value)=0 THEN RAISE(ABORT, 'matrix update denied') END;
		END`,
	}
	for _, statement := range setup {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("install test trigger/schema: %v", err)
		}
	}
	return db, observation
}

// runFirstAudioSnapshotUpsert models the production saveRuntimeContextLocked
// SQL while enforcing the transaction-local OLD check and callback ledger from
// docs/74 and docs/77.
func runFirstAudioSnapshotUpsert(db *gorm.DB, observation *firstAudioUpsertObservation, key string, oldValue *string, newValue string) (resultErr error) {
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("rollback incomplete callback sequence: %w", rollbackErr))
			}
		}
	}()

	var gotOld string
	err := tx.Raw("SELECT value FROM runtime_records WHERE key=?", key).Row().Scan(&gotOld)
	if oldValue == nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("initial insert requires absent OLD row, got value=%q err=%v", gotOld, err)
		}
	} else if err != nil || gotOld != *oldValue {
		return fmt.Errorf("update requires exact OLD row: got=%q want=%q err=%v", gotOld, *oldValue, err)
	}

	mode := "insert"
	if oldValue != nil {
		mode = "update"
	}
	observation.configure(mode, key, oldValue, newValue)
	row := store.RuntimeRecord{Key: key, Value: newValue}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&row).Error; err != nil {
		return err
	}
	wantEvents := []string{"INSERT"}
	if oldValue != nil {
		wantEvents = []string{"INSERT", "UPDATE"}
	}
	if gotEvents := observation.snapshot(); !reflect.DeepEqual(gotEvents, wantEvents) {
		return fmt.Errorf("unexpected row-guard callback sequence: got=%v want=%v", gotEvents, wantEvents)
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	committed = true
	return nil
}

func TestFirstAudioMatrixSnapshotUpsertGuardCallbackSequence(t *testing.T) {
	db, observation := openFirstAudioUpsertGuardDB(t)
	const key = "snapshot/101"
	if err := runFirstAudioSnapshotUpsert(db, observation, key, nil, "initial-state"); err != nil {
		t.Fatalf("initial upsert failed: %v", err)
	}
	if got, want := observation.snapshot(), []string{"INSERT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial insert callbacks = %v, want %v", got, want)
	}
	old := "initial-state"
	if err := runFirstAudioSnapshotUpsert(db, observation, key, &old, "first-tts-state"); err != nil {
		t.Fatalf("conflict update failed: %v", err)
	}
	if got, want := observation.snapshot(), []string{"INSERT", "UPDATE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("conflict callbacks = %v, want %v", got, want)
	}
	var final string
	if err := db.Raw("SELECT value FROM runtime_records WHERE key=?", key).Row().Scan(&final); err != nil || final != "first-tts-state" {
		t.Fatalf("final snapshot value=%q err=%v", final, err)
	}
}

func TestFirstAudioMatrixSnapshotUpsertRejectsMissingOldRowWithoutInsertFallback(t *testing.T) {
	db, observation := openFirstAudioUpsertGuardDB(t)
	old := "expected-old"
	err := runFirstAudioSnapshotUpsert(db, observation, "snapshot/missing", &old, "new-state")
	if err == nil {
		t.Fatal("update mode silently fell back to a fresh insert")
	}
	if got := observation.snapshot(); len(got) != 0 {
		t.Fatalf("missing-OLD rejection executed DML callbacks: %v", got)
	}
	var count int64
	if err := db.Table("runtime_records").Where("key=?", "snapshot/missing").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("missing-OLD rejection changed row set: count=%d err=%v", count, err)
	}
}

func TestFirstAudioMatrixSnapshotUpsertRollsBackWhenConflictUpdateGuardIsMissing(t *testing.T) {
	db, observation := openFirstAudioUpsertGuardDB(t)
	const key = "snapshot/101"
	if err := runFirstAudioSnapshotUpsert(db, observation, key, nil, "initial-state"); err != nil {
		t.Fatalf("initial upsert failed: %v", err)
	}
	old := "initial-state"
	if err := db.Exec("DROP TRIGGER runtime_guard_update").Error; err != nil {
		t.Fatalf("drop update trigger: %v", err)
	}
	if err := runFirstAudioSnapshotUpsert(db, observation, key, &old, "must-rollback"); err == nil {
		t.Fatal("missing UPDATE guard callback was accepted")
	}
	if got, want := observation.snapshot(), []string{"INSERT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing-update-trigger callbacks = %v, want observed incomplete sequence %v", got, want)
	}
	var final string
	if err := db.Raw("SELECT value FROM runtime_records WHERE key=?", key).Row().Scan(&final); err != nil || final != old {
		t.Fatalf("incomplete callback sequence did not roll back: value=%q err=%v", final, err)
	}
}

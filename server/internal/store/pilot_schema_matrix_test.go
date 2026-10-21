// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func pilotMatrixDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pilot.db")
	db, err := Open(path)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	return db, path
}
func pilotMatrixOwned(t *testing.T, db *gorm.DB) []pilotMasterRow {
	t.Helper()
	var all []pilotMasterRow
	require.NoError(t, db.Raw("SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name").Scan(&all).Error)
	var out []pilotMasterRow
	for _, row := range all {
		if pilotOwnedNamespace(row.Name) {
			out = append(out, row)
		}
	}
	return out
}

type pilotMatrixSnapshot struct {
	Objects []pilotMasterRow
	Runtime []RuntimeRecord
}

func pilotMatrixBefore(t *testing.T, db *gorm.DB) pilotMatrixSnapshot {
	t.Helper()
	var out pilotMatrixSnapshot
	require.NoError(t, db.Raw("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE substr(name,1,7)<>'sqlite_' ORDER BY type,name").Scan(&out.Objects).Error)
	require.NoError(t, db.Order("key").Find(&out.Runtime).Error)
	return out
}

func TestPilotSchemaDefaultOpenUnchangedAndCompleteMigrationDraft(t *testing.T) {
	db, _ := pilotMatrixDB(t)
	var tables int64
	require.NoError(t, db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND substr(name,1,7)<>'sqlite_'").Scan(&tables).Error)
	require.EqualValues(t, 22, tables)
	require.Empty(t, pilotMatrixOwned(t, db))
	mode, err := inspectPilotSchema(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, PilotLegacyUnmigrated, mode)
	require.NoError(t, migratePilotSchema(context.Background(), db))
	owned := pilotMatrixOwned(t, db)
	counts := map[string]int{}
	for _, row := range owned {
		counts[row.Type]++
	}
	require.Equal(t, map[string]int{"table": 9, "index": 4, "trigger": 3}, counts)
	wanted := []string{"classroom_mode_metadata", "classroom_scope_reservations", "classroom_mode_reservations", "classroom_pilot_states", "classroom_transition_receipts", "classroom_answer_claims", "classroom_active_commands", "classroom_effect_outbox", "classroom_completion_jobs"}
	var names []string
	for _, row := range owned {
		if row.Type == "table" {
			names = append(names, row.Name)
		}
	}
	require.ElementsMatch(t, wanted, names)
	mode, err = inspectPilotSchema(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, PilotSchemaReady, mode)
	before := pilotMatrixBefore(t, db)
	require.NoError(t, migratePilotSchema(context.Background(), db))
	require.Equal(t, before, pilotMatrixBefore(t, db))
}

func TestPilotSchemaConcurrentTwoPoolsPrivateMigrationDraft(t *testing.T) {
	first, path := pilotMatrixDB(t)
	second, err := Open(path)
	require.NoError(t, err)
	pool, err := second.DB()
	require.NoError(t, err)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*gorm.DB{first, second} {
		wg.Add(1)
		go func(db *gorm.DB) { defer wg.Done(); <-start; results <- migratePilotSchema(ctx, db) }(db)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	mode, err := inspectPilotSchema(ctx, first)
	require.NoError(t, err)
	require.Equal(t, PilotSchemaReady, mode)
	require.Len(t, pilotMatrixOwned(t, first), 16)
	var markers int64
	require.NoError(t, first.Model(&RuntimeRecord{}).Where("key=?", pilotV2MarkerKey).Count(&markers).Error)
	require.EqualValues(t, 1, markers)
}

func TestPilotSchemaRealDeferredCommitFailureLeavesOriginalDatabaseDraft(t *testing.T) {
	db, path := pilotMatrixDB(t)
	require.NoError(t, db.Create(&RuntimeRecord{Key: "unrelated/test", Value: `{"keep":true}`}).Error)
	before := pilotMatrixBefore(t, db)
	inserted := false
	// Inject an actual deferred foreign-key violation after marker INSERT. This
	// is not tx.AddError: every statement succeeds; database COMMIT must fail.
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("pilot_commit_fault", func(tx *gorm.DB) {
		if inserted || tx.Error != nil || !strings.HasPrefix(tx.Statement.SQL.String(), "INSERT INTO runtime_records(key,value)") {
			return
		}
		if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
			tx.AddError(errPilotV2SchemaInvalid)
			return
		}
		for _, q := range []string{"CREATE TABLE pilot_test_parent(id INTEGER PRIMARY KEY)", "CREATE TABLE pilot_test_child(id INTEGER REFERENCES pilot_test_parent(id) DEFERRABLE INITIALLY DEFERRED)", "INSERT INTO pilot_test_child VALUES(999)"} {
			if _, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context, q); err != nil {
				tx.AddError(err)
				return
			}
		}
		var count int
		if err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT count(*) FROM pilot_test_child").Scan(&count); err != nil {
			tx.AddError(err)
			return
		}
		inserted = count == 1
	}))
	err := migratePilotSchema(context.Background(), db)
	require.NoError(t, db.Callback().Raw().Remove("pilot_commit_fault"))
	require.True(t, inserted, "the violating INSERT must actually succeed before COMMIT")
	require.Error(t, err)
	require.Equal(t, before, pilotMatrixBefore(t, db))
	// Independent pool proves no commit; same-pool equality proves no polluted
	// connection retains uncommitted schema (previous atomicConnection regression).
	other, err := connectSQLite(path)
	require.NoError(t, err)
	otherPool, err := other.DB()
	require.NoError(t, err)
	defer otherPool.Close()
	require.Equal(t, before, pilotMatrixBefore(t, other))
	require.NoError(t, migratePilotSchema(context.Background(), db))
}

func TestPilotSchemaPermanentScopeTriggersRejectEveryReplacementDraft(t *testing.T) {
	db, _ := pilotMatrixDB(t)
	require.NoError(t, migratePilotSchema(context.Background(), db))
	require.NoError(t, db.Exec("PRAGMA recursive_triggers=OFF").Error)
	var enabled int
	require.NoError(t, db.Raw("PRAGMA recursive_triggers").Scan(&enabled).Error)
	require.Zero(t, enabled)
	// No Lesson FK references this row: REPLACE must be stopped by INSERT trigger,
	// not accidentally by a referencing row or recursive DELETE behavior.
	require.NoError(t, db.Exec("INSERT INTO classroom_scope_reservations VALUES(7,9,'legacy',2,1)").Error)
	for _, q := range []string{
		"UPDATE classroom_scope_reservations SET mode='pilot' WHERE student_id=7 AND book_id=9",
		"DELETE FROM classroom_scope_reservations WHERE student_id=7 AND book_id=9",
		"INSERT OR REPLACE INTO classroom_scope_reservations VALUES(7,9,'pilot',2,2)",
		"INSERT INTO classroom_scope_reservations VALUES(7,9,'pilot',2,2) ON CONFLICT(student_id,book_id) DO UPDATE SET mode='pilot'",
		"INSERT OR IGNORE INTO classroom_scope_reservations VALUES(7,9,'legacy',2,2)",
	} {
		err := db.Exec(q).Error
		require.Error(t, err)
		require.Contains(t, err.Error(), "scope_immutable")
	}
	var rows []pilotScopeRow
	require.NoError(t, db.Find(&rows).Error)
	require.Equal(t, []pilotScopeRow{{StudentID: 7, BookID: 9, Mode: "legacy", SchemaVersion: 2, ReservedAt: 1}}, rows)
}

func TestPilotSchemaCorruptionNeverFallsBackLegacyDraft(t *testing.T) {
	for _, kind := range []string{"missing_marker", "bad_marker", "missing_table", "metadata", "extra_owned", "extra_index", "missing_index", "missing_trigger", "fk"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := pilotMatrixDB(t)
			require.NoError(t, migratePilotSchema(context.Background(), db))
			switch kind {
			case "missing_marker":
				require.NoError(t, db.Where("key=?", pilotV2MarkerKey).Delete(&RuntimeRecord{}).Error)
			case "bad_marker":
				require.NoError(t, db.Model(&RuntimeRecord{}).Where("key=?", pilotV2MarkerKey).Update("value", `{"schema_version":2,"migration_id":"wrong"}`).Error)
			case "missing_table":
				require.NoError(t, db.Exec("DROP TABLE classroom_completion_jobs").Error)
			case "metadata":
				require.NoError(t, db.Exec("DELETE FROM classroom_mode_metadata").Error)
			case "extra_owned":
				require.NoError(t, db.Exec("CREATE TABLE classroom_pilot_unrecognized(id INTEGER)").Error)
			case "extra_index":
				require.NoError(t, db.Exec("CREATE INDEX rogue_index ON classroom_mode_metadata(schema_version)").Error)
			case "missing_index":
				var name string
				require.NoError(t, db.Raw("SELECT name FROM sqlite_master WHERE type='index' AND substr(name,1,14)='idx_classroom_' ORDER BY name LIMIT 1").Scan(&name).Error)
				require.NotEmpty(t, name)
				require.NoError(t, db.Exec("DROP INDEX "+pilotQuote(name)).Error)
			case "missing_trigger":
				require.NoError(t, db.Exec("DROP TRIGGER classroom_scope_no_reinsert").Error)
			case "fk":
				require.NoError(t, db.Exec("INSERT INTO classroom_scope_reservations VALUES(7,9,'legacy',2,1)").Error)
				require.NoError(t, db.Exec("PRAGMA foreign_keys=OFF").Error)
				require.NoError(t, db.Exec("INSERT INTO classroom_mode_reservations(lesson_id,student_id,book_id,mode,schema_version,reserved_at) VALUES(999,7,9,'legacy',2,1)").Error)
				require.NoError(t, db.Exec("PRAGMA foreign_keys=ON").Error)
			}
			_, err := inspectPilotSchema(context.Background(), db)
			require.Error(t, err)
			_, err = newPilotStore(context.Background(), db)
			require.Error(t, err)
			before := pilotMatrixBefore(t, db)
			require.Error(t, migratePilotSchema(context.Background(), db))
			require.Equal(t, before, pilotMatrixBefore(t, db))
		})
	}
}

func TestPilotSchemaStartupCompatibilityAndExplicitMigrationGateDraft(t *testing.T) {
	t.Run("legacy_orphan_does_not_break_startup_but_blocks_migration", func(t *testing.T) {
		db, _ := pilotMatrixDB(t)
		require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/999", Value: `{"LessonID":999,"StudentID":7,"BookID":9,"Phase":"lesson_end"}`}).Error)
		before := pilotMatrixBefore(t, db)
		mode, err := inspectPilotSchema(context.Background(), db)
		require.NoError(t, err)
		require.Equal(t, PilotLegacyUnmigrated, mode)
		_, err = newPilotStore(context.Background(), db)
		require.NoError(t, err)
		require.Error(t, migratePilotSchema(context.Background(), db))
		require.Equal(t, before, pilotMatrixBefore(t, db))
	})
	t.Run("ready_orphan_is_corruption", func(t *testing.T) {
		db, _ := pilotMatrixDB(t)
		require.NoError(t, migratePilotSchema(context.Background(), db))
		require.NoError(t, db.Create(&RuntimeRecord{Key: "settled/999", Value: `{}`}).Error)
		_, err := inspectPilotSchema(context.Background(), db)
		require.Error(t, err)
		_, err = newPilotStore(context.Background(), db)
		require.Error(t, err)
	})
	for _, kind := range []string{"partial", "g1_facts", "marker_without_schema"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := pilotMatrixDB(t)
			switch kind {
			case "partial":
				require.NoError(t, db.Exec("CREATE TABLE classroom_mode_metadata(id INTEGER)").Error)
			case "g1_facts":
				require.NoError(t, db.Exec("CREATE TABLE duplex_checkpoints(id INTEGER)").Error)
				require.NoError(t, db.Exec("INSERT INTO duplex_checkpoints VALUES(1)").Error)
			case "marker_without_schema":
				require.NoError(t, db.Create(&RuntimeRecord{Key: pilotV2MarkerKey, Value: `{"schema_version":2,"migration_id":"classroom-pilot-v2-initial"}`}).Error)
			}
			before := pilotMatrixBefore(t, db)
			_, err := inspectPilotSchema(context.Background(), db)
			require.Error(t, err)
			_, err = newPilotStore(context.Background(), db)
			require.Error(t, err)
			require.Error(t, migratePilotSchema(context.Background(), db))
			require.Equal(t, before, pilotMatrixBefore(t, db))
		})
	}
}

// Uses the real isolated creation fixture from pilot-identity_test, not manually
// inserting a marker/state. This locks the newly connected ready payload audit.
func TestPilotSchemaCreatedDataAuditOnNewFacadeDraft(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	receipt, err := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, err)
	var files []struct {
		Seq        int
		Name, File string
	}
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	var path string
	for _, f := range files {
		if f.Name == "main" {
			path = f.File
		}
	}
	require.NotEmpty(t, path)
	other, err := connectSQLite(path)
	require.NoError(t, err)
	pool, err := other.DB()
	require.NoError(t, err)
	defer pool.Close()
	_, err = newPilotStore(context.Background(), other)
	require.NoError(t, err)
	mode, err := inspectPilotSchema(context.Background(), other)
	require.NoError(t, err)
	require.Equal(t, PilotSchemaReady, mode)
	require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", receipt.LessonID).Update("room_facts_json", `{}`).Error)
	_, err = newPilotStore(context.Background(), other)
	require.Error(t, err)
	_, err = inspectPilotSchema(context.Background(), other)
	require.Error(t, err)
}

// Real SQLite object lookup is ASCII case-insensitive. Uppercase owned objects
// are corruption, never proof of an untouched legacy database.
func TestPilotSchemaUppercaseObjectsCannotHideFromInspectionDraft(t *testing.T) {
	cases := []struct {
		name       string
		statements []string
	}{
		{"owned_table", []string{"CREATE TABLE CLASSROOM_PILOT_STATES(id INTEGER)"}},
		{"owned_view", []string{"CREATE VIEW CLASSROOM_PILOT_STATES AS SELECT 1 AS id"}},
		{"owned_index", []string{"CREATE INDEX IDX_CLASSROOM_OUTBOX_UNKNOWN ON runtime_records(key)"}},
		{"g1_checkpoint", []string{"CREATE TABLE DUPLEX_CHECKPOINTS(id INTEGER)", "INSERT INTO DUPLEX_CHECKPOINTS VALUES(1)"}},
		{"g1_receipt", []string{"CREATE TABLE DuPlEx_ReCeIpTs(id INTEGER)", "INSERT INTO DuPlEx_ReCeIpTs VALUES(1)"}},
		{"g1_outbox", []string{"CREATE TABLE DUPLEX_OUTBOXES(id INTEGER)", "INSERT INTO DUPLEX_OUTBOXES VALUES(1)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := pilotMatrixDB(t)
			for _, statement := range tc.statements {
				require.NoError(t, db.Exec(statement).Error)
			}
			before := pilotMatrixBefore(t, db)
			_, err := inspectPilotSchema(context.Background(), db)
			require.Error(t, err)
			_, err = newPilotStore(context.Background(), db)
			require.Error(t, err)
			require.Error(t, migratePilotSchema(context.Background(), db))
			require.Equal(t, before, pilotMatrixBefore(t, db))
		})
	}
}

func TestPilotSchemaWarmUppercaseG1FactsRejectedWithoutCookieChangeDraft(t *testing.T) {
	db, _ := pilotMatrixDB(t)
	require.NoError(t, db.Exec("CREATE TABLE DUPLEX_CHECKPOINTS(id INTEGER)").Error)
	facade, err := newPilotStore(context.Background(), db)
	require.NoError(t, err)
	var before, after int64
	require.NoError(t, db.Raw("PRAGMA schema_version").Scan(&before).Error)
	require.NoError(t, db.Exec("INSERT INTO DUPLEX_CHECKPOINTS VALUES(1)").Error)
	require.NoError(t, db.Raw("PRAGMA schema_version").Scan(&after).Error)
	require.Equal(t, before, after)
	require.Error(t, atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE runtime_records SET value=value WHERE 1=0").Error; err != nil {
			return err
		}
		_, err := facade.verifier.inspectWriterTx(tx)
		return err
	}))
}

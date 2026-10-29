package api

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"enwords/internal/store"
	sqlite "modernc.org/sqlite"
)

func TestFirstAudioLessonPrimaryKeyCardinalityRealSchema(t *testing.T) {
	const traceID = "test-first-audio-lesson-primary-key-cardinality"
	db, err := store.Open(filepath.Join(t.TempDir(), "lesson-cardinality.sqlite"))
	if err != nil {
		t.Fatalf("trace_id=%s module=store event=open_failed err=%v", traceID, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("trace_id=%s module=store event=sql_db_failed err=%v", traceID, err)
	}
	defer sqlDB.Close()

	rows, err := sqlDB.Query("PRAGMA table_info(lessons)")
	if err != nil {
		t.Fatalf("trace_id=%s module=store event=schema_probe_failed err=%v", traceID, err)
	}
	type schemaColumn struct {
		name     string
		dataType string
		pk       int
	}
	var columns []schemaColumn
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatalf("trace_id=%s module=store event=schema_scan_failed err=%v", traceID, err)
		}
		columns = append(columns, schemaColumn{name: name, dataType: dataType, pk: primaryKey})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("trace_id=%s module=store event=schema_rows_failed err=%v", traceID, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("trace_id=%s module=store event=schema_close_failed err=%v", traceID, err)
	}
	primaryKeyCount := 0
	primaryKeyName, primaryKeyType := "", ""
	for _, column := range columns {
		if column.pk == 1 {
			primaryKeyCount++
			primaryKeyName, primaryKeyType = column.name, column.dataType
		} else if column.pk != 0 {
			t.Fatalf("trace_id=%s module=store event=schema_pk_ordinal_invalid pk_ordinal=%d", traceID, column.pk)
		}
	}
	if primaryKeyCount != 1 || primaryKeyName != "id" || !strings.EqualFold(primaryKeyType, "INTEGER") {
		t.Fatalf("trace_id=%s module=store event=schema_pk_mismatch pk_name=%q pk_type=%q pk_count=%d", traceID, primaryKeyName, primaryKeyType, primaryKeyCount)
	}
	t.Logf("trace_id=%s module=store event=schema_pk_verified pk_name=%s pk_type=%s pk_count=%d", traceID, primaryKeyName, primaryKeyType, primaryKeyCount)

	const lessonID int64 = 872351
	lesson := store.Lesson{ID: lessonID, StudentID: 872350, BookID: 872349, Status: "ACTIVE"}
	if err := db.Create(&lesson).Error; err != nil {
		t.Fatalf("trace_id=%s module=store event=seed_row_failed err=%v", traceID, err)
	}
	duplicate := store.Lesson{ID: lessonID, StudentID: 872348, BookID: 872347, Status: "ACTIVE"}
	duplicateErr := db.Create(&duplicate).Error
	var sqliteErr *sqlite.Error
	if !errors.As(duplicateErr, &sqliteErr) || sqliteErr.Code() != 1555 {
		code := 0
		if sqliteErr != nil {
			code = sqliteErr.Code()
		}
		t.Fatalf("trace_id=%s module=store event=duplicate_pk_rejection_invalid rejected=%t sqlite_code=%d", traceID, duplicateErr != nil, code)
	}
	var rowCount int64
	if err := db.Model(&store.Lesson{}).Where("id = ?", lessonID).Count(&rowCount).Error; err != nil {
		t.Fatalf("trace_id=%s module=store event=duplicate_pk_count_failed err=%v", traceID, err)
	}
	if rowCount != 1 {
		t.Fatalf("trace_id=%s module=store event=duplicate_pk_row_count_mismatch row_count=%d", traceID, rowCount)
	}
	t.Logf("trace_id=%s module=store event=duplicate_pk_rejected sqlite_code=%d row_count=%d", traceID, sqliteErr.Code(), rowCount)

	result := db.Exec("UPDATE main.lessons SET id=id WHERE id=?", lessonID)
	if result.Error != nil {
		t.Fatalf("trace_id=%s module=store event=fixed_update_failed statement=fixed_lesson_id_noop err=%v", traceID, result.Error)
	}
	affected := result.RowsAffected
	if affected != 1 {
		t.Fatalf("trace_id=%s module=store event=fixed_update_cardinality_mismatch statement=fixed_lesson_id_noop affected_rows=%d", traceID, affected)
	}
	t.Logf("trace_id=%s module=store event=fixed_update_cardinality_verified statement=fixed_lesson_id_noop affected_rows=%d", traceID, affected)
}

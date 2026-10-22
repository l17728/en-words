// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type pilotScopeRow struct {
	StudentID     int64  `gorm:"column:student_id"`
	BookID        int64  `gorm:"column:book_id"`
	Mode          string `gorm:"column:mode"`
	SchemaVersion int64  `gorm:"column:schema_version"`
	ReservedAt    int64  `gorm:"column:reserved_at"`
}

func (pilotScopeRow) TableName() string { return "classroom_scope_reservations" }

var (
	errPilotScopeProtected = errors.New("store: pilot scope protected")
	errPilotSchemaInvalid  = errors.New("store: pilot schema invalid")
)

// reserveLegacyScopeAfterSchemaTx is a small private SQL component only.
// The trusted caller must already authenticate the actor and perform the FULL
// current schema/marker check in THIS transaction. No bool allowPilot escape.
// The caller commits via atomicConnection; this function never commits, opens
// another connection, modifies cards or publishes success. A returned row is
// not an authorization receipt until the caller's transaction has committed.
func reserveLegacyScopeAfterSchemaTx(tx *gorm.DB, studentID, bookID, nowMS int64) (pilotScopeRow, error) {
	var zero pilotScopeRow
	if tx == nil || tx.Statement == nil || studentID <= 0 || bookID <= 0 || nowMS < 0 {
		return zero, fmt.Errorf("store: invalid scope arguments")
	}
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return zero, fmt.Errorf("store: scope operation requires current transaction")
	}
	// A deferred Begin is not a writer lock. Read scope only after this UPDATE.
	if err := tx.Exec("UPDATE classroom_mode_metadata SET schema_version=schema_version WHERE id=1").Error; err != nil {
		return zero, err
	}
	var metadata struct{ SchemaVersion int64 }
	if err := tx.Table("classroom_mode_metadata").Select("schema_version").Where("id=1").Take(&metadata).Error; err != nil {
		return zero, err
	}
	if metadata.SchemaVersion != 2 {
		return zero, errPilotSchemaInvalid
	}
	var row pilotScopeRow
	err := tx.Where("student_id=? AND book_id=?", studentID, bookID).Take(&row).Error
	if err == nil {
		if row.SchemaVersion != 2 || row.ReservedAt < 0 {
			return zero, errPilotSchemaInvalid
		}
		switch row.Mode {
		case "legacy":
			return row, nil // Read-only replay; never OR IGNORE/REPLACE/UPSERT.
		case "pilot":
			return zero, errPilotScopeProtected
		default:
			return zero, errPilotSchemaInvalid
		}
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return zero, err
	}
	row = pilotScopeRow{StudentID: studentID, BookID: bookID, Mode: "legacy", SchemaVersion: 2, ReservedAt: nowMS}
	if err := tx.Create(&row).Error; err != nil {
		return zero, err
	}
	return row, nil
}

// Ordinary SQLite CreateLesson compatibility sketch (NOT applied):
// rec := lessonFrom(l)
// err := tx.Create(rec).Error // ID==0 is left to INTEGER PRIMARY KEY allocation.
// if err == nil { l.ID = rec.ID } // Safe immediately for a detached tx-local candidate.
// An outer-owned transaction publishes the caller ID only after COMMIT; existing
// tx-bound owners may use the local ID before COMMIT but must discard candidates
// on failure. SQL rollback cannot undo a Go pointer or already published response.
// Nonzero Save must require existing owner/book and legacy scope, then UPDATE;
// it must not use GORM Save's INSERT fallback. Exact update fields remain those
// of current lessonFrom; no schema/owner changes or pilot bypass are permitted.

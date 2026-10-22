// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

const pilotLegacyBridgePrefix = "pilot/legacy_scope/"

type pilotLegacyBridge struct {
	SchemaVersion int64  `json:"schema_version"`
	StudentID     int64  `json:"student_id"`
	BookID        int64  `json:"book_id"`
	Mode          string `json:"mode"`
}

func pilotLegacyBridgeKey(studentID, bookID int64) (string, error) {
	if studentID <= 0 || bookID <= 0 {
		return "", errPilotSchemaInvalid
	}
	return pilotLegacyBridgePrefix + strconv.FormatInt(studentID, 10) + "/" + strconv.FormatInt(bookID, 10), nil
}

// Token-level decoding rejects duplicate fields as well as unknown/missing
// fields. json.Unmarshal alone accepts duplicates and cannot enforce this gate.
func decodePilotLegacyBridge(key, value string) (pilotLegacyBridge, error) {
	var zero pilotLegacyBridge
	if len(value) == 0 || len(value) > 512 || !utf8.ValidString(value) || !strings.HasPrefix(key, pilotLegacyBridgePrefix) {
		return zero, errPilotSchemaInvalid
	}
	parts := strings.Split(strings.TrimPrefix(key, pilotLegacyBridgePrefix), "/")
	if len(parts) != 2 {
		return zero, errPilotSchemaInvalid
	}
	sid, e1 := strconv.ParseInt(parts[0], 10, 64)
	bid, e2 := strconv.ParseInt(parts[1], 10, 64)
	canonical, e3 := pilotLegacyBridgeKey(sid, bid)
	if e1 != nil || e2 != nil || e3 != nil || canonical != key {
		return zero, errPilotSchemaInvalid
	}
	d := json.NewDecoder(bytes.NewBufferString(value))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return zero, errPilotSchemaInvalid
	}
	fields := make(map[string]json.RawMessage, 4)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return zero, errPilotSchemaInvalid
		}
		name, ok := token.(string)
		if !ok {
			return zero, errPilotSchemaInvalid
		}
		switch name {
		case "schema_version", "student_id", "book_id", "mode":
		default:
			return zero, errPilotSchemaInvalid
		}
		if _, duplicate := fields[name]; duplicate {
			return zero, errPilotSchemaInvalid
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return zero, errPilotSchemaInvalid
		}
		fields[name] = raw
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(fields) != 4 {
		return zero, errPilotSchemaInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return zero, errPilotSchemaInvalid
	}
	var row pilotLegacyBridge
	if json.Unmarshal(fields["schema_version"], &row.SchemaVersion) != nil ||
		json.Unmarshal(fields["student_id"], &row.StudentID) != nil ||
		json.Unmarshal(fields["book_id"], &row.BookID) != nil ||
		json.Unmarshal(fields["mode"], &row.Mode) != nil ||
		row.SchemaVersion != 1 || row.StudentID != sid || row.BookID != bid || row.Mode != "legacy" {
		return zero, errPilotSchemaInvalid
	}
	return row, nil
}

// Requires a pinned writer transaction AFTER complete schema inspection proved
// legacy_unmigrated. It must not be called from a pool or after v2 migration.
// Caller commits before publishing permission or performing old multi-step work.
func reserveLegacyBridgeUnmigratedTx(tx *gorm.DB, studentID, bookID int64) error {
	if tx == nil || tx.Statement == nil {
		return errPilotSchemaInvalid
	}
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return errPilotSchemaInvalid
	}
	key, err := pilotLegacyBridgeKey(studentID, bookID)
	if err != nil {
		return err
	}
	var existing RuntimeRecord
	err = tx.Where("key=?", key).Take(&existing).Error
	if err == nil {
		_, err = decodePilotLegacyBridge(existing.Key, existing.Value)
		return err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	value, err := json.Marshal(pilotLegacyBridge{SchemaVersion: 1, StudentID: studentID, BookID: bookID, Mode: "legacy"})
	if err != nil {
		return err
	}
	// Never repair malformed data, overwrite a record or ignore insert errors.
	return tx.Create(&RuntimeRecord{Key: key, Value: string(value)}).Error
}

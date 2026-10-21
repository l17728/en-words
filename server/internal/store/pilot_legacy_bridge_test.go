// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPilotLegacyBridgeStrictCodec(t *testing.T) {
	good := `{"schema_version":1,"student_id":7,"book_id":9,"mode":"legacy"}`
	key := "pilot/legacy_scope/7/9"
	row, err := decodePilotLegacyBridge(key, good)
	require.NoError(t, err)
	require.Equal(t, pilotLegacyBridge{SchemaVersion: 1, StudentID: 7, BookID: 9, Mode: "legacy"}, row)
	for _, tc := range []struct{ name, key, value string }{
		{"duplicate", key, strings.Replace(good, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)},
		{"escaped_duplicate", key, strings.Replace(good, `"schema_version":1`, `"schema_version":1,"\u0073chema_version":1`, 1)},
		{"case_alias", key, strings.Replace(good, "student_id", "Student_id", 1)},
		{"unknown", key, strings.Replace(good, `"mode"`, `"extra":false,"mode"`, 1)},
		{"missing", key, strings.Replace(good, `"schema_version":1,`, "", 1)},
		{"null", key, strings.Replace(good, `"student_id":7`, `"student_id":null`, 1)},
		{"float", key, strings.Replace(good, `"student_id":7`, `"student_id":7.0`, 1)},
		{"exponent", key, strings.Replace(good, `"student_id":7`, `"student_id":7e0`, 1)},
		{"string_number", key, strings.Replace(good, `"student_id":7`, `"student_id":"7"`, 1)},
		{"overflow", key, strings.Replace(good, `"student_id":7`, `"student_id":9223372036854775808`, 1)},
		{"wrong_version", key, strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1)},
		{"wrong_mode", key, strings.Replace(good, "legacy\"", "pilot\"", 1)},
		{"id_mismatch", key, strings.Replace(good, `"student_id":7`, `"student_id":8`, 1)},
		{"trailing_object", key, good + `{}`},
		{"trailing_scalar", key, good + ` true`},
		{"root_array", key, "[" + good + "]"},
		{"root_null", key, "null"},
		{"truncated", key, good[:len(good)-1]},
		{"too_large", key, good + strings.Repeat(" ", 513)},
		{"empty", key, ""},
		{"leading_zero_key", "pilot/legacy_scope/07/9", good},
		{"plus_key", "pilot/legacy_scope/+7/9", good},
		{"zero_key", "pilot/legacy_scope/0/9", good},
		{"negative_key", "pilot/legacy_scope/-7/9", good},
		{"overflow_key", "pilot/legacy_scope/9223372036854775808/9", good},
		{"suffix_key", key + "/more", good},
		{"prefix_key", "x/" + key, good},
		{"nul_key", key + "\x00", good},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodePilotLegacyBridge(tc.key, tc.value)
			require.ErrorIs(t, err, errPilotSchemaInvalid)
			require.Zero(t, got, "invalid persistent bridge must not publish a partial identity")
		})
	}
	const maximum int64 = 9223372036854775807
	maxKey, err := pilotLegacyBridgeKey(maximum, maximum)
	require.NoError(t, err)
	_, err = decodePilotLegacyBridge(maxKey, fmt.Sprintf(`{"schema_version":1,"student_id":%d,"book_id":%d,"mode":"legacy"}`, maximum, maximum))
	require.NoError(t, err)
	_, err = decodePilotLegacyBridge(key, good+strings.Repeat(" ", 512-len(good)))
	require.NoError(t, err, "exact byte bound remains accepted")
	for _, ids := range [][2]int64{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		badKey, err := pilotLegacyBridgeKey(ids[0], ids[1])
		require.Error(t, err)
		require.Empty(t, badKey)
	}
}

func TestPilotLegacyBridgeDurabilityIdempotencyAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := Open(path)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.Error(t, reserveLegacyBridgeUnmigratedTx(nil, 7, 9))
	require.Error(t, reserveLegacyBridgeUnmigratedTx(db, 7, 9), "pool handle is not a pinned writer")
	reserve := func() error {
		return atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
			if err := tx.Exec("UPDATE runtime_records SET key=key WHERE 1=0").Error; err != nil {
				return err
			}
			// Fixture has no pilot tables/marker. Caller schema gate is tested separately.
			return reserveLegacyBridgeUnmigratedTx(tx, 7, 9)
		})
	}
	require.NoError(t, reserve())
	var first RuntimeRecord
	require.NoError(t, db.Where("key=?", "pilot/legacy_scope/7/9").Take(&first).Error)
	require.NoError(t, reserve())
	var second RuntimeRecord
	require.NoError(t, db.Where("key=?", first.Key).Take(&second).Error)
	require.Equal(t, first, second, "replay must not rewrite even the timestamp")
	require.NoError(t, pool.Close())
	db, err = Open(path)
	require.NoError(t, err)
	reopened, err := db.DB()
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, reserve())
	require.NoError(t, db.Model(&RuntimeRecord{}).Where("key=?", first.Key).Update("value", `{"mode":"pilot"}`).Error)
	require.ErrorIs(t, reserve(), errPilotSchemaInvalid)
	require.NoError(t, db.Where("key=?", first.Key).Take(&second).Error)
	require.Equal(t, `{"mode":"pilot"}`, second.Value, "never repair corrupt authorization facts")
}

func TestPilotLegacyBridgeFailedCommitPublishesNoPermission(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "commit.db"))
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Exec("CREATE TABLE bridge_parent(id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE bridge_child(parent_id INTEGER REFERENCES bridge_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
	err = atomicConnection(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE runtime_records SET key=key WHERE 1=0").Error; err != nil {
			return err
		}
		if err := reserveLegacyBridgeUnmigratedTx(tx, 7, 9); err != nil {
			return err
		}
		return tx.Exec("INSERT INTO bridge_child(parent_id) VALUES (123)").Error
	})
	require.Error(t, err, "deferred FK fails at COMMIT, not at the bridge insert")
	var count int64
	require.NoError(t, db.Model(&RuntimeRecord{}).Where("key=?", "pilot/legacy_scope/7/9").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Table("bridge_child").Count(&count).Error)
	require.Zero(t, count)
}

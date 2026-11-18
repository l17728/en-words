// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"gorm.io/gorm"
)

// A verifier belongs to ONE open outer pool, not a filename/DSN/global cache.
// Its owner discards it with that pool. No marker/scope/account state is cached.
type pilotSchemaVerifier struct {
	db       *gorm.DB
	pool     *sql.DB
	mu       sync.RWMutex
	verified bool
	cookie   int64
	shape    PilotSchemaState
}

func newPilotSchemaVerifier(db *gorm.DB) (*pilotSchemaVerifier, error) {
	if db == nil || db.Statement == nil || db.Config == nil {
		return nil, errPilotV2SchemaInvalid
	}
	pool, ok := db.Statement.ConnPool.(*sql.DB)
	if !ok || db.Config.ConnPool != pool {
		return nil, errPilotV2SchemaInvalid
	}
	return &pilotSchemaVerifier{db: db, pool: pool}, nil
}

// AuditCommitted is the ONLY cache publisher. The read transaction has finished
// successfully before proof becomes visible. Never publish a proof from a DDL
// writer callback: rollback could reuse that cookie for a different schema.
// Explicit migration must COMMIT first, then call this stable audit separately.
func (v *pilotSchemaVerifier) AuditCommitted(ctx context.Context) error {
	if v == nil {
		return errPilotV2SchemaInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cookie int64
	var shape PilotSchemaState
	err := atomicConnection(ctx, v.db, func(tx *gorm.DB) error {
		var err error
		cookie, err = pilotSchemaCookieTx(tx)
		if err != nil {
			return err
		}
		shape, err = inspectPilotSchemaTx(tx)
		if err != nil {
			return err
		}
		// Preserve legacy startup compatibility. Full old-runtime attribution
		// is an explicit migration gate, not a new default startup requirement.
		if shape == PilotSchemaReady {
			if err = auditPilotDataTx(tx, shape); err != nil {
				return err
			}
		}
		last, err := pilotSchemaCookieTx(tx)
		if err != nil {
			return err
		}
		if last != cookie {
			return errPilotV2SchemaInvalid
		}
		return nil
	})
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.cookie, v.shape, v.verified = cookie, shape, true
	v.mu.Unlock()
	return nil
}

func pilotSchemaCookieTx(tx *gorm.DB) (int64, error) {
	var cookie int64
	if err := tx.Raw("PRAGMA schema_version").Scan(&cookie).Error; err != nil {
		return 0, err
	}
	if cookie < 0 {
		return 0, errPilotV2SchemaInvalid
	}
	return cookie, nil
}

// Caller MUST have acquired the common SQLite writer before invoking this.
// The current GORM v1.31.2 Begin changes Statement.ConnPool to *sql.Tx while
// Session copies Config, preserving Config.ConnPool as the originating pool.
// Prepared/nested/custom connection wrappers are not silently accepted.
func (v *pilotSchemaVerifier) inspectWriterTx(tx *gorm.DB) (PilotSchemaState, error) {
	return v.inspectCurrentTx(tx)
}

// Shared read-snapshot implementation. Callers that intend mutation acquire
// the writer first; readonly entry checks must not manufacture a write.
func (v *pilotSchemaVerifier) inspectCurrentTx(tx *gorm.DB) (PilotSchemaState, error) {
	if v == nil || tx == nil || tx.Statement == nil || tx.Config == nil || tx.Config.ConnPool != v.pool {
		return "", errPilotV2SchemaInvalid
	}
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return "", errPilotV2SchemaInvalid
	}
	cookie, err := pilotSchemaCookieTx(tx)
	if err != nil {
		return "", err
	}
	v.mu.RLock()
	verified, cachedCookie, shape := v.verified, v.cookie, v.shape
	v.mu.RUnlock()
	if !verified || cookie != cachedCookie {
		// DDL-only validation, transaction-local result. Do NOT publish globally.
		return inspectPilotSchemaTx(tx)
	}
	if err := checkPilotCurrentSchemaDataTx(tx, shape); err != nil {
		return "", err
	}
	return shape, nil
}

// Constant-size current-data validation; no all-bridge scan or foreign-key
// data scan. The calling scope helper reads THIS scope's row on EVERY Reserve,
// including after a savepoint rollback. Full scans belong to audit/migration.
func checkPilotCurrentSchemaDataTx(tx *gorm.DB, shape PilotSchemaState) error {
	var marker struct{ Value string }
	err := tx.Table("runtime_records").Select("value").Where("key=?", pilotV2MarkerKey).Take(&marker).Error
	if shape == PilotLegacyUnmigrated {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			if err != nil {
				return err
			}
			return errPilotV2SchemaInvalid
		}
		for _, table := range []string{"duplex_checkpoints", "duplex_receipts", "duplex_outboxes"} {
			var objects []struct{ Type string }
			if err := tx.Raw("SELECT type FROM sqlite_master WHERE name = ? COLLATE NOCASE", table).Scan(&objects).Error; err != nil {
				return err
			}
			if len(objects) == 0 {
				continue
			}
			if len(objects) != 1 || objects[0].Type != "table" {
				return errPilotV2SchemaInvalid
			}
			var present bool
			if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM " + pilotQuote(table) + " LIMIT 1)").Scan(&present).Error; err != nil {
				return err
			}
			if present {
				return errPilotV2SchemaInvalid
			}
		}
		return nil
	}
	if shape != PilotSchemaReady {
		return errPilotV2SchemaInvalid
	}
	if err != nil {
		return err
	}
	fields, err := pilotStrictObject([]byte(marker.Value), []string{"schema_version", "migration_id"})
	if err != nil {
		return err
	}
	var version int64
	var migrationID string
	if json.Unmarshal(fields["schema_version"], &version) != nil || version != 2 || json.Unmarshal(fields["migration_id"], &migrationID) != nil || migrationID != pilotV2MigrationID {
		return errPilotV2SchemaInvalid
	}
	var rows []struct{ ID, SchemaVersion int64 }
	if err := tx.Table("classroom_mode_metadata").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].ID != 1 || rows[0].SchemaVersion != 2 {
		return errPilotV2SchemaInvalid
	}
	return nil
}

// A batch-local guard can be shared only by trusted operations inside this
// exact sql.Tx. It must not escape its transaction owner or cross COMMIT.
// Teaching operations may not mutate marker/schema/metadata/scope themselves;
// explicit migration uses its separate path, never this reuse object.
type pilotLegacyTxGuard struct {
	tx    *sql.Tx
	shape PilotSchemaState
}

func newPilotLegacyTxGuard(tx *gorm.DB, verifier *pilotSchemaVerifier) (*pilotLegacyTxGuard, error) {
	if tx == nil || tx.Statement == nil {
		return nil, errPilotV2SchemaInvalid
	}
	raw, ok := tx.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return nil, errPilotV2SchemaInvalid
	}
	if err := tx.Exec("UPDATE runtime_records SET value=value WHERE 1=0").Error; err != nil {
		return nil, err
	}
	var shape PilotSchemaState
	var err error
	if verifier == nil {
		shape, err = inspectPilotSchemaTx(tx)
	} else {
		shape, err = verifier.inspectWriterTx(tx)
	}
	if err != nil {
		return nil, err
	}
	return &pilotLegacyTxGuard{tx: raw, shape: shape}, nil
}

func (g *pilotLegacyTxGuard) Reserve(tx *gorm.DB, studentID, bookID, nowMS int64) error {
	if g == nil || tx == nil || tx.Statement == nil || tx.Statement.ConnPool != g.tx || studentID <= 0 || bookID <= 0 || nowMS < 0 {
		return errPilotV2SchemaInvalid
	}
	if tx.Statement.Context == nil {
		return errPilotV2SchemaInvalid
	}
	if err := tx.Statement.Context.Err(); err != nil {
		return err
	}
	// No successful-scope cache: SAVEPOINT rollback can remove its row, and
	// pointer/context identity does not prove that sql.Tx remains live.
	// The indexed query below must execute even for a previously seen scope;
	// database/sql rejects committed/rolled-back transactions with ErrTxDone.
	var err error
	switch g.shape {
	case PilotLegacyUnmigrated:
		err = reserveLegacyBridgeUnmigratedTx(tx, studentID, bookID)
	case PilotSchemaReady:
		_, err = reserveLegacyScopeAfterSchemaTx(tx, studentID, bookID, nowMS)
	default:
		err = errPilotV2SchemaInvalid
	}
	return err
}

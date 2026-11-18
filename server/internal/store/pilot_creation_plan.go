// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"enwords/internal/config"
	"gorm.io/gorm"
)

var errPilotPlanStale = errors.New("store: pilot plan stale")

type pilotConfigRevision struct {
	Key      string          `json:"key"`
	Revision config.Revision `json:"revision"`
}

type pilotCreationPlanData struct {
	PlanVersion           int64                    `json:"plan_version"`
	CreationVersion       int64                    `json:"creation_version"`
	Nonce                 string                   `json:"creation_nonce"`
	StudentID             int64                    `json:"student_id"`
	BookID                int64                    `json:"book_id"`
	OpenAtMS              int64                    `json:"open_at_ms"`
	WordIDs               []int64                  `json:"word_ids"`
	Config                config.EffectiveSnapshot `json:"config"`
	ConfigSnapshotVersion int64                    `json:"config_snapshot_schema_version"`
	ConfigFingerprint     string                   `json:"config_fingerprint"`
	ConfigRevisions       []pilotConfigRevision    `json:"config_revisions"`
}

// Fields are private: no ordinary JSON request can supply checkpoint, results,
// arbitrary configuration or an existing Lesson ID through this type.
type PilotCreationPlan struct {
	data        pilotCreationPlanData
	fingerprint string
}

func pilotCanonicalHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// The pinned read transaction gives snapshot and every revision one consistent
// view. This does not authorize execution or reserve a scope.
func PreparePilotCreationPlan(ctx context.Context, db *gorm.DB, studentID, bookID int64, openAt time.Time, wordIDs []int64) (*PilotCreationPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || db.Statement == nil || studentID <= 0 || bookID <= 0 || openAt.IsZero() || openAt.UnixMilli() < 0 || len(wordIDs) == 0 || len(wordIDs) > 100 {
		return nil, errPilotSchemaInvalid
	}
	ids := append([]int64(nil), wordIDs...)
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errPilotSchemaInvalid
		}
		seen[id] = true
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	data := pilotCreationPlanData{PlanVersion: 1, CreationVersion: 1,
		Nonce: hex.EncodeToString(nonce[:]), StudentID: studentID, BookID: bookID,
		OpenAtMS: openAt.UnixMilli(), WordIDs: ids, ConfigSnapshotVersion: 1}
	err := atomicConnection(ctx, db, func(tx *gorm.DB) error {
		var err error
		data.Config, data.ConfigRevisions, data.ConfigFingerprint, err = readPilotConfigSnapshotTx(tx, studentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	fingerprint, err := pilotCanonicalHash(data)
	if err != nil {
		return nil, err
	}
	return &PilotCreationPlan{data: data, fingerprint: fingerprint}, nil
}

func readPilotConfigSnapshotTx(tx *gorm.DB, studentID int64) (config.EffectiveSnapshot, []pilotConfigRevision, string, error) {
	var zero config.EffectiveSnapshot
	if tx == nil || tx.Statement == nil || studentID <= 0 {
		return zero, nil, "", errPilotSchemaInvalid
	}
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return zero, nil, "", errPilotSchemaInvalid
	}
	cfg, err := config.NewStore(NewConfigPersister(tx), nil)
	if err != nil {
		return zero, nil, "", err
	}
	snapshot := cfg.Snapshot(studentID)
	keys := config.SortedKeys()
	revisions := make([]pilotConfigRevision, 0, len(keys))
	for _, key := range keys {
		revision, err := cfg.Revisions(studentID, key)
		if err != nil {
			return zero, nil, "", err
		}
		if revision.Global < 0 || revision.Student < 0 {
			return zero, nil, "", errPilotSchemaInvalid
		}
		revisions = append(revisions, pilotConfigRevision{Key: key, Revision: revision})
	}
	fingerprint, err := pilotCanonicalHash(snapshot)
	if err != nil {
		return zero, nil, "", err
	}
	return snapshot, revisions, fingerprint, nil
}

// First-creation-only validation. Nonce replay instead compares the original
// frozen plan fingerprint after reauthorizing its owner; current config may have
// legitimately changed since that creation and must not invalidate the receipt.
func verifyPilotPlanConfigTx(tx *gorm.DB, plan *PilotCreationPlan) error {
	if plan == nil || plan.fingerprint == "" {
		return errPilotSchemaInvalid
	}
	actualPlanHash, err := pilotCanonicalHash(plan.data)
	if err != nil {
		return err
	}
	if actualPlanHash != plan.fingerprint {
		return errPilotSchemaInvalid
	}
	_, revisions, fingerprint, err := readPilotConfigSnapshotTx(tx, plan.data.StudentID)
	if err != nil {
		return err
	}
	if fingerprint != plan.data.ConfigFingerprint || len(revisions) != len(plan.data.ConfigRevisions) {
		return errPilotPlanStale
	}
	for i := range revisions {
		if revisions[i] != plan.data.ConfigRevisions[i] {
			return errPilotPlanStale
		}
	}
	return nil
}

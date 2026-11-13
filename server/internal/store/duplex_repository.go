package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var ErrDuplexConflict = errors.New("duplex store: conflict")
var ErrDuplexOwner = errors.New("duplex store: owner mismatch")

type DuplexCheckpoint struct {
	LessonID  int64 `gorm:"primaryKey;autoIncrement:false"`
	StudentID int64
	Version   uint64
	State     string
}
type DuplexReceipt struct {
	DecisionID    string `gorm:"primaryKey"`
	LessonID      int64  `gorm:"uniqueIndex:duplex_input"`
	StudentID     int64
	Epoch         string `gorm:"uniqueIndex:duplex_input"`
	InputRevision uint64 `gorm:"uniqueIndex:duplex_input"`
	UtteranceID   string
	Fingerprint   string
	Version       uint64
	Status        string `gorm:"index"`
	TextHash      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
type DuplexOutbox struct {
	ID          uint64 `gorm:"primaryKey"`
	DecisionID  string `gorm:"uniqueIndex:duplex_effect"`
	EffectIndex int    `gorm:"uniqueIndex:duplex_effect"`
	LessonID    int64  `gorm:"index"`
	StudentID   int64
	Payload     string
}
type DuplexCandidate struct {
	DecisionID, Epoch, UtteranceID string
	InputRevision                  uint64
	InputFingerprint               string
	State                          json.RawMessage
	Effects                        []json.RawMessage
}
type DuplexRepo struct{ db *gorm.DB }

func NewDuplexRepo(db *gorm.DB) *DuplexRepo { return &DuplexRepo{db} }

// Migrate is explicitly opt-in; normal store.Migrate never creates these tables.
func (r *DuplexRepo) Migrate() error {
	return r.db.AutoMigrate(&DuplexCheckpoint{}, &DuplexReceipt{}, &DuplexOutbox{})
}

// PrepareReceipt records the single-use confirmation intent before classroom
// effects run. A duplicate returns the existing row so callers never create a
// second teaching decision for the same utterance.
func (r *DuplexRepo) PrepareReceipt(ctx context.Context, receipt DuplexReceipt) (DuplexReceipt, error) {
	if receipt.StudentID <= 0 || receipt.LessonID <= 0 || !validDuplexString(receipt.DecisionID) || !validDuplexString(receipt.Epoch) || !validDuplexString(receipt.UtteranceID) || receipt.InputRevision == 0 || receipt.Status != "committing" {
		return DuplexReceipt{}, ErrDuplexConflict
	}
	var existing DuplexReceipt
	err := r.db.WithContext(ctx).Where("lesson_id=? AND epoch=? AND input_revision=?", receipt.LessonID, receipt.Epoch, receipt.InputRevision).First(&existing).Error
	if err == nil {
		if existing.StudentID != receipt.StudentID || existing.UtteranceID != receipt.UtteranceID || existing.TextHash != receipt.TextHash {
			return DuplexReceipt{}, ErrDuplexConflict
		}
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return DuplexReceipt{}, err
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	receipt.UpdatedAt = receipt.CreatedAt
	if err = r.db.WithContext(ctx).Create(&receipt).Error; err != nil {
		var retry DuplexReceipt
		if e := r.db.WithContext(ctx).Where("lesson_id=? AND epoch=? AND input_revision=?", receipt.LessonID, receipt.Epoch, receipt.InputRevision).First(&retry).Error; e == nil && retry.StudentID == receipt.StudentID && retry.UtteranceID == receipt.UtteranceID && retry.TextHash == receipt.TextHash {
			return retry, nil
		}
		return DuplexReceipt{}, err
	}
	return receipt, nil
}

func (r *DuplexRepo) FinalizeReceipt(ctx context.Context, student, lesson int64, decisionID string) (DuplexReceipt, error) {
	var receipt DuplexReceipt
	if student <= 0 || lesson <= 0 || !validDuplexString(decisionID) {
		return receipt, ErrDuplexConflict
	}
	if err := r.db.WithContext(ctx).Where("decision_id=? AND student_id=? AND lesson_id=?", decisionID, student, lesson).First(&receipt).Error; err != nil {
		return receipt, err
	}
	if receipt.Status == "committed" {
		return receipt, nil
	}
	if receipt.Status != "committing" {
		return DuplexReceipt{}, ErrDuplexConflict
	}
	if err := r.db.WithContext(ctx).Model(&DuplexReceipt{}).Where("decision_id=? AND student_id=? AND lesson_id=? AND status=?", decisionID, student, lesson, "committing").Updates(map[string]any{"status": "committed", "updated_at": time.Now().UTC()}).Error; err != nil {
		return DuplexReceipt{}, err
	}
	receipt.Status = "committed"
	return receipt, nil
}
func (r *DuplexRepo) CreateCheckpoint(ctx context.Context, student, lesson int64, state json.RawMessage) error {
	if student <= 0 || lesson <= 0 || !json.Valid(state) || len(state) > 1<<20 {
		return ErrDuplexConflict
	}
	return r.db.WithContext(ctx).Create(&DuplexCheckpoint{LessonID: lesson, StudentID: student, State: string(state)}).Error
}
func (r *DuplexRepo) LoadCheckpoint(ctx context.Context, student, lesson int64) (DuplexCheckpoint, error) {
	var c DuplexCheckpoint
	err := r.db.WithContext(ctx).First(&c, "lesson_id=?", lesson).Error
	if err == nil && c.StudentID != student {
		return DuplexCheckpoint{}, ErrDuplexOwner
	}
	return c, err
}
func validDuplexString(s string) bool { return len(s) > 0 && len(s) <= 128 }

// Commit owns the outer transaction. The callback is a trusted integration seam,
// not model-provided code, and must use tx only (no nested tx, network or Room).
func (r *DuplexRepo) Commit(ctx context.Context, student, lesson int64, expected uint64, c DuplexCandidate, apply func(*gorm.DB) error) (DuplexReceipt, error) {
	var receipt DuplexReceipt
	if student <= 0 || lesson <= 0 || !validDuplexString(c.DecisionID) || !validDuplexString(c.Epoch) || !validDuplexString(c.UtteranceID) || c.InputRevision == 0 || len(c.InputFingerprint) != 64 || !json.Valid(c.State) || len(c.State) > 1<<20 || len(c.Effects) > 32 {
		return receipt, ErrDuplexConflict
	}
	for _, e := range c.Effects {
		if !json.Valid(e) || len(e) > 16<<10 {
			return receipt, ErrDuplexConflict
		}
	}
	if _, err := hex.DecodeString(c.InputFingerprint); err != nil {
		return receipt, ErrDuplexConflict
	}
	// Freeze callback-visible mutation risk: hash and persist a detached candidate.
	raw, _ := json.Marshal(c)
	var frozen DuplexCandidate
	_ = json.Unmarshal(raw, &frozen)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	err := atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		if e := tx.Exec("UPDATE duplex_checkpoints SET version=version WHERE 1=0").Error; e != nil {
			return e
		}
		var cp DuplexCheckpoint
		if e := tx.First(&cp, "lesson_id=?", lesson).Error; e != nil {
			return e
		}
		if cp.StudentID != student {
			return ErrDuplexOwner
		}
		e := tx.First(&receipt, "decision_id=?", frozen.DecisionID).Error
		if e == nil {
			if receipt.StudentID != student || receipt.LessonID != lesson || receipt.Fingerprint != fingerprint {
				return ErrDuplexConflict
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if cp.Version != expected || expected == ^uint64(0) {
			return ErrDuplexConflict
		}
		var count int64
		if e = tx.Model(&DuplexReceipt{}).Where("lesson_id=? AND epoch=? AND input_revision=?", lesson, frozen.Epoch, frozen.InputRevision).Count(&count).Error; e != nil {
			return e
		}
		if count != 0 {
			return ErrDuplexConflict
		}
		if apply != nil {
			if e = apply(tx); e != nil {
				return e
			}
		}
		update := tx.Model(&DuplexCheckpoint{}).Where("lesson_id=? AND student_id=? AND version=?", lesson, student, expected).Updates(map[string]any{"version": expected + 1, "state": string(frozen.State)})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDuplexConflict
		}
		receipt = DuplexReceipt{DecisionID: frozen.DecisionID, LessonID: lesson, StudentID: student, Epoch: frozen.Epoch, InputRevision: frozen.InputRevision, UtteranceID: frozen.UtteranceID, Fingerprint: fingerprint, Version: expected + 1}
		if e = tx.Create(&receipt).Error; e != nil {
			return e
		}
		for i, p := range frozen.Effects {
			if e = tx.Create(&DuplexOutbox{DecisionID: frozen.DecisionID, EffectIndex: i, LessonID: lesson, StudentID: student, Payload: string(p)}).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return DuplexReceipt{}, err
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	receipt.UpdatedAt = receipt.UpdatedAt.UTC()
	return receipt, nil
}
func (r *DuplexRepo) LookupReceipt(ctx context.Context, student, lesson int64, id string) (DuplexReceipt, error) {
	var result DuplexReceipt
	err := r.db.WithContext(ctx).First(&result, "decision_id=? AND student_id=? AND lesson_id=?", id, student, lesson).Error
	return result, err
}
func (r *DuplexRepo) ListOutbox(ctx context.Context, student, lesson int64, after uint64) ([]DuplexOutbox, error) {
	if student <= 0 || lesson <= 0 {
		return nil, fmt.Errorf("duplex store: invalid owner")
	}
	if _, err := r.LoadCheckpoint(ctx, student, lesson); err != nil {
		return nil, err
	}
	rows := []DuplexOutbox{}
	err := r.db.WithContext(ctx).Where("student_id=? AND lesson_id=? AND id>?", student, lesson, after).Order("id").Limit(64).Find(&rows).Error
	return rows, err
}

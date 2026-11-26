package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	ID            uint64 `gorm:"primaryKey"`
	DecisionID    string `gorm:"uniqueIndex:duplex_effect"`
	EffectIndex   int    `gorm:"uniqueIndex:duplex_effect"`
	LessonID      int64  `gorm:"index"`
	StudentID     int64
	Payload       string
	Status        string     `gorm:"index;not null;default:pending"`
	Attempts      int        `gorm:"not null;default:0"`
	LeaseToken    string     `gorm:"not null;default:''"`
	LeaseUntil    *time.Time `gorm:"index"`
	AckedAt       *time.Time
	AckLeaseToken string `gorm:"not null;default:''"`
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
type DuplexFeedbackDelivery struct {
	SubmissionID string `gorm:"primaryKey"`
	StudentID    int64  `gorm:"index"`
	LessonID     int64  `gorm:"index"`
	Text         string
	Status       string `gorm:"index;not null;default:pending"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
type DuplexCandidate struct {
	DecisionID, Epoch, UtteranceID string
	InputRevision                  uint64
	InputFingerprint, TextHash     string
	State                          json.RawMessage
	Effects                        []json.RawMessage
}
type DuplexRepo struct {
	db *gorm.DB
	// afterSnapshotCommit is a deterministic fault seam for the uncertain
	// commit boundary tests; production repositories leave it nil.
	afterSnapshotCommit func() error
}

func NewDuplexRepo(db *gorm.DB) *DuplexRepo { return &DuplexRepo{db: db} }

// Migrate is explicitly opt-in; normal store.Migrate never creates these tables.
func (r *DuplexRepo) Migrate() error {
	return r.db.AutoMigrate(&DuplexCheckpoint{}, &DuplexReceipt{}, &DuplexOutbox{}, &DuplexFeedbackDelivery{})
}

func (r *DuplexRepo) SaveFeedbackDelivery(ctx context.Context, student, lesson int64, id, text string) error {
	if student <= 0 || lesson <= 0 || !validDuplexString(id) || text == "" || len(text) > 8192 {
		return ErrDuplexConflict
	}
	row := DuplexFeedbackDelivery{SubmissionID: id, StudentID: student, LessonID: lesson, Text: text, Status: "pending"}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "submission_id"}}, DoNothing: true}).Create(&row).Error
}
func (r *DuplexRepo) PendingFeedbackDeliveries(ctx context.Context, student, lesson int64) ([]DuplexFeedbackDelivery, error) {
	rows := []DuplexFeedbackDelivery{}
	err := r.db.WithContext(ctx).Where("student_id=? AND lesson_id=? AND status=?", student, lesson, "pending").Order("created_at").Limit(32).Find(&rows).Error
	return rows, err
}
func (r *DuplexRepo) CompleteFeedbackDelivery(ctx context.Context, student int64, id string) error {
	return r.db.WithContext(ctx).Model(&DuplexFeedbackDelivery{}).Where("student_id=? AND submission_id=? AND status=?", student, id, "pending").Updates(map[string]any{"status": "completed", "updated_at": time.Now().UTC()}).Error
}
func (r *DuplexRepo) FailFeedbackDelivery(ctx context.Context, student int64, id string) error {
	return r.db.WithContext(ctx).Model(&DuplexFeedbackDelivery{}).Where("student_id=? AND submission_id=? AND status=?", student, id, "pending").Updates(map[string]any{"status": "failed", "updated_at": time.Now().UTC()}).Error
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
		if existing.Status == "rejected" {
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
		if e := r.db.WithContext(ctx).Where("lesson_id=? AND epoch=? AND input_revision=?", receipt.LessonID, receipt.Epoch, receipt.InputRevision).First(&retry).Error; e == nil && retry.StudentID == receipt.StudentID && retry.UtteranceID == receipt.UtteranceID && retry.TextHash == receipt.TextHash && retry.Status != "rejected" {
			return retry, nil
		}
		return DuplexReceipt{}, err
	}
	return receipt, nil
}

// RejectReceipt terminally closes a confirmation that reached Room validation
// but was rejected before the state/outbox transaction. It cannot reject a
// committed decision or reopen a rejected one.
func (r *DuplexRepo) RejectReceipt(ctx context.Context, student, lesson int64, decisionID string) error {
	if student <= 0 || lesson <= 0 || !validDuplexString(decisionID) {
		return ErrDuplexConflict
	}
	update := r.db.WithContext(ctx).Model(&DuplexReceipt{}).
		Where("decision_id=? AND student_id=? AND lesson_id=? AND status=?", decisionID, student, lesson, "committing").
		Updates(map[string]any{"status": "rejected", "updated_at": time.Now().UTC()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 1 {
		return nil
	}
	var row DuplexReceipt
	if err := r.db.WithContext(ctx).First(&row, "decision_id=? AND student_id=? AND lesson_id=?", decisionID, student, lesson).Error; err != nil {
		return err
	}
	if row.Status == "rejected" {
		return nil
	}
	return ErrDuplexConflict
}

// CommitSnapshot atomically publishes the durable classroom snapshot and marks
// the matching confirmation receipt committed, provided the persisted snapshot
// still equals the caller's pre-transition state. It is legacy-only and refuses
// to operate once a duplex checkpoint exists; new code must use Commit.
func (r *DuplexRepo) CommitSnapshot(ctx context.Context, student, lesson int64, decisionID string, expectedState, state any, feedback ...*DuplexFeedbackDelivery) error {
	if student <= 0 || lesson <= 0 || !validDuplexString(decisionID) || expectedState == nil || state == nil {
		return ErrDuplexConflict
	}
	expectedRaw, err := json.Marshal(expectedState)
	if err != nil || len(expectedRaw) > 1<<20 || !json.Valid(expectedRaw) {
		return ErrDuplexConflict
	}
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > 1<<20 || !json.Valid(raw) {
		return ErrDuplexConflict
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key := fmt.Sprintf("snapshot/%d", lesson)
		if err := tx.Exec("UPDATE runtime_records SET value=value WHERE key=? AND 1=0", key).Error; err != nil {
			return err
		}
		var checkpoints int64
		if err := tx.Model(&DuplexCheckpoint{}).Where("lesson_id=?", lesson).Count(&checkpoints).Error; err != nil {
			return err
		}
		if checkpoints != 0 {
			return ErrDuplexConflict
		}
		var current RuntimeRecord
		if err := tx.First(&current, "key=?", key).Error; err != nil {
			return err
		}
		if current.Value != string(expectedRaw) {
			return ErrDuplexConflict
		}
		var receipt DuplexReceipt
		if err := tx.Where("decision_id=? AND student_id=? AND lesson_id=?", decisionID, student, lesson).First(&receipt).Error; err != nil {
			return err
		}
		if receipt.Status != "committing" {
			return ErrDuplexConflict
		}
		update := tx.Model(&RuntimeRecord{}).Where("key=? AND value=?", key, current.Value).Update("value", string(raw))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDuplexConflict
		}
		receiptUpdate := tx.Model(&DuplexReceipt{}).Where("decision_id=? AND student_id=? AND lesson_id=? AND status=?", decisionID, student, lesson, "committing").Updates(map[string]any{"status": "committed", "updated_at": time.Now().UTC()})
		if receiptUpdate.Error != nil {
			return receiptUpdate.Error
		}
		if receiptUpdate.RowsAffected != 1 {
			return ErrDuplexConflict
		}
		if len(feedback) > 0 && feedback[0] != nil {
			f := *feedback[0]
			if f.StudentID != student || f.LessonID != lesson || !validDuplexString(f.SubmissionID) || f.Text == "" || len(f.Text) > 8192 {
				return ErrDuplexConflict
			}
			f.Status = "pending"
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "submission_id"}}, DoNothing: true}).Create(&f).Error; err != nil {
				return err
			}
			var existing DuplexFeedbackDelivery
			if err := tx.First(&existing, "submission_id=?", f.SubmissionID).Error; err != nil {
				return err
			}
			if existing.StudentID != student || existing.LessonID != lesson || existing.Text != f.Text {
				return ErrDuplexConflict
			}
		}
		return nil
	})
	if err == nil && r.afterSnapshotCommit != nil {
		return r.afterSnapshotCommit()
	}
	return err
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
	if err == nil && (c.Version > math.MaxInt64 || len(c.State) == 0 || len(c.State) > 1<<20 || !json.Valid([]byte(c.State))) {
		return DuplexCheckpoint{}, ErrDuplexConflict
	}
	return c, err
}

// LoadOrCreateCheckpoint binds the opt-in duplex CAS stream to the current
// validated classroom snapshot. Existing checkpoints are never overwritten.
func (r *DuplexRepo) LoadOrCreateCheckpoint(ctx context.Context, student, lesson int64, initial json.RawMessage) (DuplexCheckpoint, error) {
	if student <= 0 || lesson <= 0 || len(initial) == 0 || len(initial) > 1<<20 || !json.Valid(initial) {
		return DuplexCheckpoint{}, ErrDuplexConflict
	}
	err := atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		row := DuplexCheckpoint{LessonID: lesson, StudentID: student, State: string(append(json.RawMessage(nil), initial...))}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "lesson_id"}}, DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return DuplexCheckpoint{}, err
	}
	return r.LoadCheckpoint(ctx, student, lesson)
}

// LoadOrCreateCheckpointFromRuntimeSnapshot initializes the duplex version
// stream from the legacy durable snapshot while holding SQLite's writer lock.
// Unlike LoadOrCreateCheckpoint, the caller cannot pass a stale pre-read value.
func (r *DuplexRepo) LoadOrCreateCheckpointFromRuntimeSnapshot(ctx context.Context, student, lesson int64) (DuplexCheckpoint, error) {
	if student <= 0 || lesson <= 0 {
		return DuplexCheckpoint{}, ErrDuplexConflict
	}
	err := atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE duplex_checkpoints SET version=version WHERE 1=0").Error; err != nil {
			return err
		}
		var existing DuplexCheckpoint
		err := tx.First(&existing, "lesson_id=?", lesson).Error
		if err == nil {
			if existing.StudentID != student {
				return ErrDuplexOwner
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var snapshot RuntimeRecord
		if err := tx.First(&snapshot, "key=?", fmt.Sprintf("snapshot/%d", lesson)).Error; err != nil {
			return err
		}
		state := []byte(snapshot.Value)
		if len(state) == 0 || len(state) > 1<<20 || !json.Valid(state) {
			return ErrDuplexConflict
		}
		return tx.Create(&DuplexCheckpoint{LessonID: lesson, StudentID: student, Version: 0, State: string(state)}).Error
	})
	if err != nil {
		return DuplexCheckpoint{}, err
	}
	return r.LoadCheckpoint(ctx, student, lesson)
}

// LookupInputReceipt supports safe replay before mutating Room memory. It never
// creates a reservation; a missing row leaves the decision uncommitted.
func (r *DuplexRepo) LookupInputReceipt(ctx context.Context, student, lesson int64, epoch string, revision uint64) (DuplexReceipt, error) {
	if student <= 0 || lesson <= 0 || !validDuplexString(epoch) || revision == 0 {
		return DuplexReceipt{}, ErrDuplexConflict
	}
	var receipt DuplexReceipt
	err := r.db.WithContext(ctx).Where("student_id=? AND lesson_id=? AND epoch=? AND input_revision=?", student, lesson, epoch, revision).First(&receipt).Error
	return receipt, err
}
func validDuplexString(s string) bool { return len(s) > 0 && len(s) <= 128 }

// Commit owns the outer transaction. The callback is a trusted integration seam,
// not model-provided code, and must use tx only (no nested tx, network or Room).
func (r *DuplexRepo) Commit(ctx context.Context, student, lesson int64, expected uint64, c DuplexCandidate, apply func(*gorm.DB) error) (DuplexReceipt, error) {
	var receipt DuplexReceipt
	if student <= 0 || lesson <= 0 || !validDuplexString(c.DecisionID) || !validDuplexString(c.Epoch) || !validDuplexString(c.UtteranceID) || c.InputRevision == 0 || len(c.InputFingerprint) != 64 || (c.TextHash != "" && len(c.TextHash) != 64) || !json.Valid(c.State) || len(c.State) > 1<<20 || len(c.Effects) > 32 {
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
			if receipt.Status == "committed" {
				return nil
			}
			return ErrDuplexConflict
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if cp.Version != expected || expected >= math.MaxInt64 {
			return ErrDuplexConflict
		}
		// A lesson may have only one unacknowledged batch. Otherwise a later
		// checkpoint could overtake an effect that the browser has not applied.
		var pending int64
		if e = tx.Model(&DuplexOutbox{}).Where("lesson_id=? AND student_id=? AND status IN (?, ?, ?)", lesson, student, "pending", "claimed", "sent").Count(&pending).Error; e != nil {
			return e
		}
		if pending != 0 {
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
		receipt = DuplexReceipt{DecisionID: frozen.DecisionID, LessonID: lesson, StudentID: student, Epoch: frozen.Epoch, InputRevision: frozen.InputRevision, UtteranceID: frozen.UtteranceID, Fingerprint: fingerprint, Version: expected + 1, Status: "committed", TextHash: frozen.TextHash}
		if e = tx.Create(&receipt).Error; e != nil {
			return e
		}
		if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&RuntimeRecord{Key: fmt.Sprintf("snapshot/%d", lesson), Value: string(frozen.State)}).Error; e != nil {
			return e
		}
		for i, p := range frozen.Effects {
			if e = tx.Create(&DuplexOutbox{DecisionID: frozen.DecisionID, EffectIndex: i, LessonID: lesson, StudentID: student, Payload: string(p), Status: "pending"}).Error; e != nil {
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

// ClaimOutbox leases one committed effect for at-least-once delivery. An expired
// lease may be reclaimed after a process crash; the caller must retain the same
// decision/effect identity when replaying it.
func (r *DuplexRepo) ClaimOutbox(ctx context.Context, student, lesson int64, id uint64, token string, now time.Time, lease time.Duration) (DuplexOutbox, error) {
	if student <= 0 || lesson <= 0 || id == 0 || !validDuplexString(token) || now.IsZero() || lease < time.Second || lease > 5*time.Minute {
		return DuplexOutbox{}, ErrDuplexConflict
	}
	var claimed DuplexOutbox
	err := atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		until := now.UTC().Add(lease)
		update := tx.Model(&DuplexOutbox{}).
			Where("id=? AND student_id=? AND lesson_id=? AND (status=? OR (status IN (?, ?) AND lease_until <= ?))", id, student, lesson, "pending", "claimed", "sent", now.UTC()).
			Where("NOT EXISTS (SELECT 1 FROM duplex_outboxes earlier WHERE earlier.lesson_id=? AND earlier.student_id=? AND earlier.id<? AND earlier.status<>?)", lesson, student, id, "acked").
			Updates(map[string]any{"status": "claimed", "lease_token": token, "lease_until": until, "attempts": gorm.Expr("attempts + 1"), "last_error": "", "updated_at": now.UTC()})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDuplexConflict
		}
		return tx.First(&claimed, "id=? AND student_id=? AND lesson_id=? AND lease_token=?", id, student, lesson, token).Error
	})
	if err != nil {
		return DuplexOutbox{}, err
	}
	return claimed, nil
}

// MarkOutboxSent records that the effect was placed on the authenticated
// classroom connection while keeping a retry deadline until the browser ACKs.
func (r *DuplexRepo) MarkOutboxSent(ctx context.Context, student, lesson int64, id uint64, token string, now time.Time, retryAfter time.Duration) error {
	if student <= 0 || lesson <= 0 || id == 0 || !validDuplexString(token) || now.IsZero() || retryAfter < time.Second || retryAfter > 5*time.Minute {
		return ErrDuplexConflict
	}
	update := r.db.WithContext(ctx).Model(&DuplexOutbox{}).
		Where("id=? AND student_id=? AND lesson_id=? AND status=? AND lease_token=? AND lease_until > ?", id, student, lesson, "claimed", token, now.UTC()).
		Updates(map[string]any{"status": "sent", "lease_until": now.UTC().Add(retryAfter), "updated_at": now.UTC()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 1 {
		return nil
	}
	var row DuplexOutbox
	if err := r.db.WithContext(ctx).First(&row, "id=? AND student_id=? AND lesson_id=?", id, student, lesson).Error; err != nil {
		return err
	}
	if row.Status == "acked" && row.AckLeaseToken == token {
		return nil
	}
	return ErrDuplexConflict
}

// AckOutbox is idempotent and accepts an ACK that races MarkOutboxSent. The
// browser ACK means it applied the stable effect ID; audio completion remains
// governed by the separate tts_finished classroom protocol.
func (r *DuplexRepo) AckOutbox(ctx context.Context, student, lesson int64, decisionID string, effectIndex int, token string, now time.Time) error {
	if student <= 0 || lesson <= 0 || !validDuplexString(decisionID) || effectIndex < 0 || !validDuplexString(token) || now.IsZero() {
		return ErrDuplexConflict
	}
	update := r.db.WithContext(ctx).Model(&DuplexOutbox{}).
		Where("decision_id=? AND effect_index=? AND lesson_id=? AND student_id=? AND status IN (?, ?) AND lease_token=? AND lease_until > ?", decisionID, effectIndex, lesson, student, "claimed", "sent", token, now.UTC()).
		Where("NOT EXISTS (SELECT 1 FROM duplex_outboxes earlier WHERE earlier.lesson_id=? AND earlier.student_id=? AND earlier.id < (SELECT current.id FROM duplex_outboxes current WHERE current.decision_id=? AND current.effect_index=? AND current.lesson_id=? AND current.student_id=?) AND earlier.status<>?)", lesson, student, decisionID, effectIndex, lesson, student, "acked").
		Updates(map[string]any{"status": "acked", "ack_lease_token": token, "lease_token": "", "lease_until": nil, "acked_at": now.UTC(), "updated_at": now.UTC()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 1 {
		return nil
	}
	var row DuplexOutbox
	if err := r.db.WithContext(ctx).First(&row, "decision_id=? AND effect_index=? AND lesson_id=? AND student_id=?", decisionID, effectIndex, lesson, student).Error; err != nil {
		return err
	}
	if row.Status == "acked" && row.AckLeaseToken == token {
		return nil
	}
	return ErrDuplexConflict
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

func (r *DuplexRepo) ListRecoverableOutbox(ctx context.Context, student, lesson int64, now time.Time, limit int) ([]DuplexOutbox, error) {
	if student <= 0 || lesson <= 0 || now.IsZero() || limit < 1 || limit > 64 {
		return nil, ErrDuplexConflict
	}
	if _, err := r.LoadCheckpoint(ctx, student, lesson); err != nil {
		return nil, err
	}
	rows := []DuplexOutbox{}
	err := r.db.WithContext(ctx).Where("student_id=? AND lesson_id=? AND (status=? OR (status IN (?, ?) AND lease_until <= ?))", student, lesson, "pending", "claimed", "sent", now.UTC()).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

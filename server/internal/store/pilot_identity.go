// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"enwords/internal/wordbook"
	"gorm.io/gorm"
)

var errPilotCreationDenied = errors.New("store: pilot creation denied")
var errPilotCreationConflict = errors.New("store: pilot creation conflict")
var errPilotNotReady = errors.New("store: pilot not ready")

// Only the trusted tool adapter may bind this AFTER taskFromContext/scope checks.
// A task ID is an audit correlation value, not proof of authorization. This type
// cannot be populated by decoding ordinary tool JSON. A test-only harness may
// bind it directly; the product does not expose a pilot-create HTTP endpoint.
type PilotSystemPrincipal struct {
	studentID int64
	taskID    string
}

func BindPilotSystemPrincipal(studentID int64, taskID string) (PilotSystemPrincipal, error) {
	if studentID <= 0 || len(taskID) == 0 || len(taskID) > 128 || !utf8.ValidString(taskID) || strings.TrimSpace(taskID) != taskID || strings.IndexFunc(taskID, unicode.IsControl) >= 0 {
		return PilotSystemPrincipal{}, errPilotCreationDenied
	}
	return PilotSystemPrincipal{studentID: studentID, taskID: taskID}, nil
}

type PilotCreationReceipt struct {
	LessonID            int64  `json:"lesson_id"`
	StudentID           int64  `json:"student_id"`
	BookID              int64  `json:"book_id"`
	CreationNonce       string `json:"creation_nonce"`
	CreationFingerprint string `json:"creation_fingerprint"`
	SchemaVersion       int64  `json:"schema_version"`
	CheckpointVersion   int64  `json:"checkpoint_version"`
	Replayed            bool   `json:"replayed"`
}

type pilotModeReservation struct {
	LessonID            int64
	StudentID           int64
	BookID              int64
	Mode                string
	SchemaVersion       int64
	ReservedAt          int64
	FirstEnteredAt      *int64
	CreationNonce       *string
	CreationVersion     *int64
	CreationFingerprint *string
}

// r is the facade whose verifier belongs to this exact outer pool. Publication
// of Room, receipt messages, events and success logs belongs AFTER this returns.
func (r *PilotStore) CreateIsolatedPilotLesson(ctx context.Context, principal PilotSystemPrincipal, plan *PilotCreationPlan) (PilotCreationReceipt, error) {
	var zero PilotCreationReceipt
	if ctx == nil {
		ctx = context.Background()
	}
	event := PilotStoreEvent{Event: PilotLessonCreated, Outcome: PilotRejected, Reason: PilotUnauthorized, StudentID: principal.studentID}
	if plan != nil {
		event.BookID = plan.data.BookID
	}
	defer func() { r.observe(ctx, event) }()
	if r == nil || r.db == nil || r.verifier == nil || plan == nil || principal.studentID <= 0 || principal.taskID == "" || principal.studentID != plan.data.StudentID {
		return zero, errPilotCreationDenied
	}
	event.Reason = PilotCreationConflict
	// The plan is opaque to callers; rehash before using its frozen fields.
	if plan.data.PlanVersion != 1 || plan.data.CreationVersion != 1 || plan.data.ConfigSnapshotVersion != 1 || plan.fingerprint == "" {
		return zero, errPilotCreationConflict
	}
	fingerprint, err := pilotCanonicalHash(plan.data)
	if err != nil || fingerprint != plan.fingerprint {
		return zero, errPilotCreationConflict
	}
	var receipt PilotCreationReceipt
	event.Reason = PilotOperationFailed
	transactionStarted := time.Now()
	callbackSucceeded := false
	err = atomicConnection(ctx, r.db, func(tx *gorm.DB) error {
		writerStarted := time.Now()
		writerErr := tx.Exec("UPDATE runtime_records SET value=value WHERE 1=0").Error
		event.WriterWaitMS = time.Since(writerStarted).Milliseconds()
		if writerErr != nil {
			return writerErr
		}
		shape, err := r.verifier.inspectWriterTx(tx)
		if err != nil {
			return err
		}
		if shape != PilotSchemaReady {
			return errPilotNotReady
		}
		event.SchemaVersion = 2
		// Current account/owner authorization precedes nonce lookup on BOTH paths.
		var student User
		if err := tx.Select("id,role,status").First(&student, principal.studentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errPilotCreationDenied
			}
			return err
		}
		if student.Role != "student" || student.Status != "active" {
			return errPilotCreationDenied
		}
		var prior pilotModeReservation
		err = tx.Table("classroom_mode_reservations").Where("creation_nonce=?", plan.data.Nonce).Take(&prior).Error
		if err == nil {
			if prior.StudentID != principal.studentID || prior.BookID != plan.data.BookID || prior.Mode != "pilot" || prior.SchemaVersion != 2 || prior.CreationVersion == nil || *prior.CreationVersion != 1 || prior.CreationFingerprint == nil || *prior.CreationFingerprint != plan.fingerprint {
				return errPilotCreationConflict
			}
			// Current dictionary/config values are deliberately not compared on a
			// replay. Return the original creation version, not current checkpoint.
			if err := verifyPilotCreatedReplayTx(tx, plan, prior); err != nil {
				return err
			}
			receipt = pilotCreationReceiptFor(plan, prior.LessonID, true)
			callbackSucceeded = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var scopeCount int64
		if err := tx.Table("classroom_scope_reservations").Where("student_id=? AND book_id=?", principal.studentID, plan.data.BookID).Count(&scopeCount).Error; err != nil {
			return err
		}
		if scopeCount != 0 {
			return errPilotCreationConflict
		}
		for _, table := range []string{"memory_cards", "placement_drafts", "lessons"} {
			var present bool
			if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM "+pilotQuote(table)+" WHERE student_id=? AND book_id=? LIMIT 1)", principal.studentID, plan.data.BookID).Scan(&present).Error; err != nil {
				return err
			}
			if present {
				return errPilotCreationConflict
			}
		}
		bridgeKey, err := pilotLegacyBridgeKey(principal.studentID, plan.data.BookID)
		if err != nil {
			return err
		}
		var bridgeCount int64
		if err := tx.Model(&RuntimeRecord{}).Where("key=?", bridgeKey).Count(&bridgeCount).Error; err != nil {
			return err
		}
		if bridgeCount != 0 {
			return errPilotCreationConflict
		}
		// Full legacy runtime integrity is audited at migration/startup. All
		// supported subsequent legacy writers reserve a permanent scope first.
		var book WordBook
		if err := tx.Select("id,status").First(&book, plan.data.BookID).Error; err != nil {
			return err
		}
		if book.Status != string(wordbook.StatusPublished) {
			return errPilotCreationDenied
		}
		if err := verifyPilotPlanConfigTx(tx, plan); err != nil {
			return err
		}
		words, err := loadPilotFrozenWordsTx(tx, plan.data.BookID, plan.data.WordIDs)
		if err != nil {
			return err
		}
		wordsJSON, err := json.Marshal(words)
		if err != nil {
			return err
		}
		if len(wordsJSON) > 1<<20 {
			return errPilotCreationConflict
		}
		contentFingerprint, err := pilotCanonicalHash(words)
		if err != nil {
			return err
		}
		configJSON, err := json.Marshal(plan.data.Config)
		if err != nil {
			return err
		}
		revisionsJSON, err := json.Marshal(plan.data.ConfigRevisions)
		if err != nil {
			return err
		}
		planJSON, err := json.Marshal(plan.data)
		if err != nil {
			return err
		}
		idsJSON, err := json.Marshal(plan.data.WordIDs)
		if err != nil {
			return err
		}
		state, facts := newPilotCreatedBaseline()
		stateJSON, err := json.Marshal(state)
		if err != nil {
			return err
		}
		factsJSON, err := json.Marshal(facts)
		if err != nil {
			return err
		}
		if _, _, err := decodePilotCreatedBaseline(stateJSON, factsJSON); err != nil {
			return err
		}
		now := time.Now().UTC().UnixMilli()
		if err := tx.Exec("INSERT INTO classroom_scope_reservations(student_id,book_id,mode,schema_version,reserved_at) VALUES(?,?,'pilot',2,?)", principal.studentID, plan.data.BookID, now).Error; err != nil {
			return err
		}
		lesson := Lesson{StudentID: principal.studentID, BookID: plan.data.BookID, Status: "scheduled", OpenAt: time.UnixMilli(plan.data.OpenAtMS).UTC(), BatchWordIDs: string(idsJSON)}
		if err := tx.Create(&lesson).Error; err != nil {
			return err
		}
		version := int64(1)
		reservation := pilotModeReservation{LessonID: lesson.ID, StudentID: principal.studentID, BookID: plan.data.BookID, Mode: "pilot", SchemaVersion: 2, ReservedAt: now,
			CreationNonce: &plan.data.Nonce, CreationVersion: &version, CreationFingerprint: &plan.fingerprint}
		if err := tx.Table("classroom_mode_reservations").Create(&reservation).Error; err != nil {
			return err
		}
		if err := tx.Table("classroom_pilot_states").Create(map[string]any{
			"lesson_id": lesson.ID, "student_id": principal.studentID, "book_id": plan.data.BookID,
			"schema_version": 2, "mode": "pilot", "lifecycle": "created", "checkpoint_version": 1,
			"logical_input_revision": 0, "question_key": nil, "phase": "pending", "auth_generation": 0,
			"connection_epoch": nil, "retired": 0, "active_command_id": nil,
			"state_json": string(stateJSON), "room_facts_json": string(factsJSON), "frozen_words_json": string(wordsJSON),
			"frozen_config_json": string(configJSON), "config_snapshot_schema_version": 1,
			"config_revisions_json": string(revisionsJSON), "config_fingerprint": plan.data.ConfigFingerprint,
			"content_fingerprint": contentFingerprint, "creation_plan_json": string(planJSON), "created_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		receipt = pilotCreationReceiptFor(plan, lesson.ID, false)
		callbackSucceeded = true
		return nil
	})
	event.TransactionMS = time.Since(transactionStarted).Milliseconds()
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			event.Reason = PilotCancelled
		case errors.Is(err, errPilotCreationDenied):
			event.Reason = PilotUnauthorized
		case errors.Is(err, errPilotCreationConflict):
			event.Reason = PilotCreationConflict
		case errors.Is(err, errPilotPlanStale):
			event.Reason = PilotPlanStale
		case errors.Is(err, errPilotNotReady):
			event.Reason = PilotNotReady
		case errors.Is(err, errPilotSchemaInvalid), errors.Is(err, errPilotV2SchemaInvalid):
			event.Reason = PilotSchemaInvalid
		case callbackSucceeded:
			event.Reason = PilotCommitFailed
		}
		return zero, err
	}
	event.Outcome, event.Reason, event.LessonID, event.Replayed = PilotCommitted, PilotNoFailure, receipt.LessonID, receipt.Replayed
	return receipt, nil
}

type pilotCreatedCheckpointRow struct {
	LessonID, StudentID, BookID, SchemaVersion                                   int64
	Mode, Lifecycle, Phase                                                       string
	CheckpointVersion, LogicalInputRevision, AuthGeneration, Retired             int64
	QuestionKey, ConnectionEpoch, ActiveCommandID                                *string
	StateJSON, RoomFactsJSON, FrozenWordsJSON, FrozenConfigJSON                  string
	ConfigSnapshotSchemaVersion                                                  int64
	ConfigRevisionsJSON, ConfigFingerprint, ContentFingerprint, CreationPlanJSON string
	CreatedAt, UpdatedAt                                                         int64
}

// Strict first-slice replay loader. Later active lifecycle replay requires its
// own complete codec; unsupported states are rejected, never reconstructed from
// the live dictionary/config or downgraded to a legacy classroom.
func verifyPilotCreatedReplayTx(tx *gorm.DB, plan *PilotCreationPlan, prior pilotModeReservation) error {
	var scope pilotScopeRow
	if err := tx.Where("student_id=? AND book_id=?", prior.StudentID, prior.BookID).Take(&scope).Error; err != nil {
		return err
	}
	if scope.Mode != "pilot" || scope.SchemaVersion != 2 || scope.ReservedAt < 0 || scope.ReservedAt != prior.ReservedAt {
		return errPilotSchemaInvalid
	}
	var lesson Lesson
	if err := tx.First(&lesson, prior.LessonID).Error; err != nil {
		return err
	}
	if lesson.StudentID != prior.StudentID || lesson.BookID != prior.BookID || lesson.Status != "scheduled" || lesson.OpenAt.UnixMilli() != plan.data.OpenAtMS {
		return errPilotSchemaInvalid
	}
	idsJSON, err := json.Marshal(plan.data.WordIDs)
	if err != nil || !bytes.Equal(idsJSON, []byte(lesson.BatchWordIDs)) {
		return errPilotSchemaInvalid
	}
	var row pilotCreatedCheckpointRow
	if err := tx.Table("classroom_pilot_states").Where("lesson_id=?", prior.LessonID).Take(&row).Error; err != nil {
		return err
	}
	if row.LessonID != prior.LessonID || row.StudentID != prior.StudentID || row.BookID != prior.BookID ||
		row.SchemaVersion != 2 || row.Mode != "pilot" || row.Lifecycle != "created" || row.Phase != "pending" ||
		row.CheckpointVersion < 1 || row.LogicalInputRevision != 0 || row.QuestionKey != nil || row.ActiveCommandID != nil ||
		row.AuthGeneration < 0 || row.CreatedAt < 0 || row.UpdatedAt < row.CreatedAt || row.CreatedAt != prior.ReservedAt ||
		row.ConfigSnapshotSchemaVersion != 1 {
		return errPilotSchemaInvalid
	}
	if row.AuthGeneration == 0 {
		if row.Retired != 0 || row.ConnectionEpoch != nil {
			return errPilotSchemaInvalid
		}
	} else if row.Retired == 0 {
		if row.ConnectionEpoch == nil || len(*row.ConnectionEpoch) == 0 || len(*row.ConnectionEpoch) > 128 || !utf8.ValidString(*row.ConnectionEpoch) || strings.IndexFunc(*row.ConnectionEpoch, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return errPilotSchemaInvalid
		}
	} else if row.Retired != 1 || row.ConnectionEpoch != nil {
		return errPilotSchemaInvalid
	}
	if _, _, err := decodePilotCreatedBaseline([]byte(row.StateJSON), []byte(row.RoomFactsJSON)); err != nil {
		return err
	}
	planJSON, err := json.Marshal(plan.data)
	if err != nil || !bytes.Equal(planJSON, []byte(row.CreationPlanJSON)) {
		return errPilotSchemaInvalid
	}
	configJSON, err := json.Marshal(plan.data.Config)
	if err != nil || !bytes.Equal(configJSON, []byte(row.FrozenConfigJSON)) {
		return errPilotSchemaInvalid
	}
	revisionsJSON, err := json.Marshal(plan.data.ConfigRevisions)
	if err != nil || !bytes.Equal(revisionsJSON, []byte(row.ConfigRevisionsJSON)) {
		return errPilotSchemaInvalid
	}
	configHash, err := pilotCanonicalHash(plan.data.Config)
	if err != nil || configHash != row.ConfigFingerprint || configHash != plan.data.ConfigFingerprint {
		return errPilotSchemaInvalid
	}
	if len(row.FrozenWordsJSON) == 0 || len(row.FrozenWordsJSON) > 1<<20 || !utf8.ValidString(row.FrozenWordsJSON) {
		return errPilotSchemaInvalid
	}
	var words []wordbook.Entry
	if json.Unmarshal([]byte(row.FrozenWordsJSON), &words) != nil || len(words) != len(plan.data.WordIDs) {
		return errPilotSchemaInvalid
	}
	canonicalWords, err := json.Marshal(words)
	if err != nil || !bytes.Equal(canonicalWords, []byte(row.FrozenWordsJSON)) {
		return errPilotSchemaInvalid
	}
	for i, word := range words {
		if word.ID != plan.data.WordIDs[i] || word.BookID != plan.data.BookID || strings.TrimSpace(word.Word) == "" || len(word.Meanings) == 0 || len(word.Meanings) > 2 {
			return errPilotSchemaInvalid
		}
		for _, meaning := range word.Meanings {
			if strings.TrimSpace(meaning) == "" {
				return errPilotSchemaInvalid
			}
		}
	}
	contentHash, err := pilotCanonicalHash(words)
	if err != nil || contentHash != row.ContentFingerprint {
		return errPilotSchemaInvalid
	}
	return nil
}

func pilotCreationReceiptFor(plan *PilotCreationPlan, id int64, replayed bool) PilotCreationReceipt {
	return PilotCreationReceipt{LessonID: id, StudentID: plan.data.StudentID, BookID: plan.data.BookID,
		CreationNonce: plan.data.Nonce, CreationFingerprint: plan.fingerprint, SchemaVersion: 2, CheckpointVersion: 1, Replayed: replayed}
}

func loadPilotFrozenWordsTx(tx *gorm.DB, bookID int64, ids []int64) ([]wordbook.Entry, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, errPilotCreationConflict
	}
	var rows []WordEntry
	if err := tx.Where("book_id=? AND id IN ?", bookID, ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, errPilotCreationConflict
	}
	byID := make(map[int64]WordEntry, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	words := make([]wordbook.Entry, 0, len(ids))
	for _, id := range ids {
		row, ok := byID[id]
		if !ok || strings.TrimSpace(row.Word) == "" {
			return nil, errPilotCreationConflict
		}
		delete(byID, id)
		var meanings []string
		if json.Unmarshal([]byte(row.Meanings), &meanings) != nil || len(meanings) == 0 || len(meanings) > 2 {
			return nil, errPilotCreationConflict
		}
		for _, meaning := range meanings {
			if strings.TrimSpace(meaning) == "" {
				return nil, errPilotCreationConflict
			}
		}
		words = append(words, wordbook.Entry{ID: row.ID, BookID: row.BookID, Word: row.Word, NormKey: row.NormKey, Phonetic: row.Phonetic, Meanings: meanings, Unit: row.Unit, IsPhrase: row.IsPhrase, FreqRank: row.FreqRank})
	}
	return words, nil
}

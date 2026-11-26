package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func duplexRepo(t *testing.T) (*DuplexRepo, *gorm.DB) {
	t.Helper()
	db, _ := openTest(t)
	pool, e := db.DB()
	require.NoError(t, e)
	t.Cleanup(func() { pool.Close() })
	r := NewDuplexRepo(db)
	require.NoError(t, r.Migrate())
	require.NoError(t, r.CreateCheckpoint(context.Background(), 1, 2, json.RawMessage(`{"phase":"answer"}`)))
	require.NoError(t, db.Exec("CREATE TABLE duplex_effect_probe(value INTEGER)").Error)
	return r, db
}
func candidate() DuplexCandidate {
	return DuplexCandidate{DecisionID: "d1", Epoch: "e1", UtteranceID: "u1", InputRevision: 1, InputFingerprint: strings.Repeat("a", 64), State: json.RawMessage(`{"phase":"next"}`), Effects: []json.RawMessage{json.RawMessage(`{"type":"say"}`)}}
}
func TestDuplexCommitReplayPrecedesCASAndIsolation(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	calls := 0
	apply := func(tx *gorm.DB) error { calls++; return tx.Exec("INSERT INTO duplex_effect_probe VALUES(1)").Error }
	receipt, e := r.Commit(ctx, 1, 2, 0, candidate(), apply)
	require.NoError(t, e)
	require.EqualValues(t, 1, receipt.Version)
	again, e := r.Commit(ctx, 1, 2, 0, candidate(), apply)
	require.NoError(t, e)
	receipt.CreatedAt = time.Time{}
	receipt.UpdatedAt = time.Time{}
	again.CreatedAt = time.Time{}
	again.UpdatedAt = time.Time{}
	require.Equal(t, receipt, again)
	require.Equal(t, 1, calls)
	cp, e := r.LoadCheckpoint(ctx, 1, 2)
	require.NoError(t, e)
	require.EqualValues(t, 1, cp.Version)
	_, e = r.LoadCheckpoint(ctx, 9, 2)
	require.ErrorIs(t, e, ErrDuplexOwner)
	_, e = r.Commit(ctx, 9, 2, 0, candidate(), apply)
	require.ErrorIs(t, e, ErrDuplexOwner)
	changed := candidate()
	changed.State = json.RawMessage(`{"phase":"wrong"}`)
	_, e = r.Commit(ctx, 1, 2, 1, changed, apply)
	require.ErrorIs(t, e, ErrDuplexConflict)
	changed = candidate()
	changed.DecisionID = "d2"
	_, e = r.Commit(ctx, 1, 2, 0, changed, apply)
	require.Error(t, e)
	_, e = r.Commit(ctx, 1, 2, 1, changed, apply)
	require.Error(t, e)
	found, e := r.LookupReceipt(ctx, 1, 2, "d1")
	require.NoError(t, e)
	found.CreatedAt = time.Time{}
	found.UpdatedAt = time.Time{}
	require.Equal(t, receipt, found)
	_, e = r.LookupReceipt(ctx, 9, 2, "d1")
	require.Error(t, e)
	rows, e := r.ListOutbox(ctx, 1, 2, 0)
	require.NoError(t, e)
	require.Len(t, rows, 1)
	rows, e = r.ListOutbox(ctx, 1, 2, rows[0].ID)
	require.NoError(t, e)
	require.Empty(t, rows)
	_, e = r.ListOutbox(ctx, 9, 2, 0)
	require.Error(t, e)
	_, e = r.ListOutbox(ctx, 0, 2, 0)
	require.Error(t, e)
	var count int64
	require.NoError(t, db.Table("duplex_effect_probe").Count(&count).Error)
	require.EqualValues(t, 1, count)
}
func TestDuplexRollbackAllObjectsAtEveryFailureStage(t *testing.T) {
	for _, stage := range []string{"callback", "checkpoint", "receipt", "outbox", "commit", "cancel", "panic"} {
		t.Run(stage, func(t *testing.T) {
			r, db := duplexRepo(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "commit" {
				require.NoError(t, db.Exec("CREATE TABLE duplex_parent(id INTEGER PRIMARY KEY)").Error)
				require.NoError(t, db.Exec("CREATE TABLE duplex_child(id INTEGER REFERENCES duplex_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
				require.NoError(t, db.Exec("CREATE TRIGGER duplex_bad_commit AFTER INSERT ON duplex_receipts BEGIN INSERT INTO duplex_child VALUES(99); END").Error)
			}
			fail := func(tx *gorm.DB) {
				if (stage == "checkpoint" && tx.Statement.Table == "duplex_checkpoints") || (stage == "receipt" && tx.Statement.Table == "duplex_receipts") || (stage == "outbox" && tx.Statement.Table == "duplex_outboxes") {
					tx.AddError(errors.New("injected"))
				}
			}
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:duplex_create", fail))
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:duplex_update", fail))
			run := func() {
				got, e := r.Commit(ctx, 1, 2, 0, candidate(), func(tx *gorm.DB) error {
					if e := tx.Exec("INSERT INTO duplex_effect_probe VALUES(1)").Error; e != nil {
						return e
					}
					switch stage {
					case "callback":
						return errors.New("injected")
					case "cancel":
						cancel()
					case "panic":
						panic("injected")
					}
					return nil
				})
				require.Error(t, e)
				require.Empty(t, got.DecisionID)
			}
			if stage == "panic" {
				require.Panics(t, run)
			} else {
				run()
			}
			cp, e := r.LoadCheckpoint(context.Background(), 1, 2)
			require.NoError(t, e)
			require.Zero(t, cp.Version)
			for _, table := range []string{"duplex_effect_probe", "duplex_receipts", "duplex_outboxes"} {
				var count int64
				require.NoError(t, db.Table(table).Count(&count).Error)
				require.Zero(t, count)
			}
			var snapshot RuntimeRecord
			err := db.First(&snapshot, "key=?", "snapshot/2").Error
			require.ErrorIs(t, err, gorm.ErrRecordNotFound, "compatibility snapshot must roll back with the candidate")
		})
	}
}

func TestDuplexCommitAtomicallyMirrorsSnapshotAndBlocksOvertakingBatch(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	first := candidate()
	_, err := r.Commit(ctx, 1, 2, 0, first, nil)
	require.NoError(t, err)
	var snapshot RuntimeRecord
	require.NoError(t, db.First(&snapshot, "key=?", "snapshot/2").Error)
	require.JSONEq(t, string(first.State), snapshot.Value)

	next := candidate()
	next.DecisionID, next.UtteranceID, next.InputRevision = "d2", "u2", 2
	_, err = r.Commit(ctx, 1, 2, 1, next, nil)
	require.ErrorIs(t, err, ErrDuplexConflict, "a later checkpoint must not overtake unacknowledged effects")
	cp, err := r.LoadCheckpoint(ctx, 1, 2)
	require.NoError(t, err)
	require.EqualValues(t, 1, cp.Version)

	rows, err := r.ListOutbox(ctx, 1, 2, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	now := time.Now().UTC()
	claimed, err := r.ClaimOutbox(ctx, 1, 2, rows[0].ID, "lease", now, 10*time.Second)
	require.NoError(t, err)
	require.NoError(t, r.AckOutbox(ctx, 1, 2, claimed.DecisionID, claimed.EffectIndex, "lease", now.Add(time.Second)))
	_, err = r.Commit(ctx, 1, 2, 1, next, nil)
	require.NoError(t, err)
}

func TestDuplexCommitRejectsSQLiteIntegerVersionOverflow(t *testing.T) {
	r, db := duplexRepo(t)
	require.NoError(t, db.Model(&DuplexCheckpoint{}).Where("lesson_id=?", 2).Update("version", int64(math.MaxInt64)).Error)
	_, err := r.Commit(context.Background(), 1, 2, uint64(math.MaxInt64), candidate(), nil)
	require.ErrorIs(t, err, ErrDuplexConflict)
	cp, err := r.LoadCheckpoint(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Equal(t, uint64(math.MaxInt64), cp.Version)
}
func TestDuplexConcurrentDuplicateAndCandidateValidation(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	var files []struct{ File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	otherDB, e := Open(files[0].File)
	require.NoError(t, e)
	p, e := otherDB.DB()
	require.NoError(t, e)
	defer p.Close()
	other := NewDuplexRepo(otherDB)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, repo := range []*DuplexRepo{r, other} {
		wg.Add(1)
		go func(x *DuplexRepo) {
			defer wg.Done()
			_, err := x.Commit(ctx, 1, 2, 0, candidate(), func(tx *gorm.DB) error { return tx.Exec("INSERT INTO duplex_effect_probe VALUES(1)").Error })
			errs <- err
		}(repo)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var count int64
	require.NoError(t, db.Table("duplex_effect_probe").Count(&count).Error)
	require.EqualValues(t, 1, count)
	for _, mutate := range []func(*DuplexCandidate){func(c *DuplexCandidate) { c.DecisionID = "" }, func(c *DuplexCandidate) { c.InputFingerprint = "x" }, func(c *DuplexCandidate) { c.InputFingerprint = strings.Repeat("z", 64) }, func(c *DuplexCandidate) { c.State = json.RawMessage(`bad`) }, func(c *DuplexCandidate) { c.Effects = []json.RawMessage{json.RawMessage(`bad`)} }} {
		c := candidate()
		mutate(&c)
		_, e = r.Commit(ctx, 1, 2, 0, c, nil)
		require.Error(t, e)
	}
	require.Error(t, r.CreateCheckpoint(ctx, 0, 9, json.RawMessage(`{}`)))
	_, e = r.LoadCheckpoint(ctx, 1, 999)
	require.Error(t, e)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, e = r.Commit(cctx, 1, 2, 1, candidate(), nil)
	require.Error(t, e)
}

func TestDuplexConcurrentDifferentDecisionsHaveSingleCASWinner(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	var files []struct{ File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	otherDB, err := Open(files[0].File)
	require.NoError(t, err)
	p, err := otherDB.DB()
	require.NoError(t, err)
	defer p.Close()
	other := NewDuplexRepo(otherDB)
	left, right := candidate(), candidate()
	right.DecisionID, right.UtteranceID, right.InputRevision = "d2", "u2", 2
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, item := range []struct {
		repo *DuplexRepo
		c    DuplexCandidate
	}{{r, left}, {other, right}} {
		wg.Add(1)
		go func(repo *DuplexRepo, c DuplexCandidate) {
			defer wg.Done()
			<-start
			_, e := repo.Commit(ctx, 1, 2, 0, c, nil)
			errs <- e
		}(item.repo, item.c)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, failures := 0, 0
	var loser error
	for e := range errs {
		if e == nil {
			successes++
		} else {
			failures++
			loser = e
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, failures)
	require.ErrorIs(t, loser, ErrDuplexConflict, "a stale concurrent candidate reports a retryable CAS conflict")
	cp, err := r.LoadCheckpoint(ctx, 1, 2)
	require.NoError(t, err)
	require.EqualValues(t, 1, cp.Version)
	var receipts, effects int64
	require.NoError(t, db.Model(&DuplexReceipt{}).Count(&receipts).Error)
	require.NoError(t, db.Model(&DuplexOutbox{}).Count(&effects).Error)
	require.EqualValues(t, 1, receipts)
	require.EqualValues(t, 1, effects)
}

func TestDuplexLogicalRevisionCannotCommitTwoUtterances(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	_, err := r.Commit(ctx, 1, 2, 0, candidate(), nil)
	require.NoError(t, err)
	next := candidate()
	next.DecisionID = "d2"
	next.UtteranceID = "u2"
	_, err = r.Commit(ctx, 1, 2, 1, next, nil)
	require.Error(t, err)
}

func TestDuplexReceiptPrepareIsIdempotent(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	in := DuplexReceipt{DecisionID: "decision-1", LessonID: 2, StudentID: 1, Epoch: "epoch-1", InputRevision: 7, UtteranceID: "utterance-1", TextHash: strings.Repeat("a", 64), Status: "committing"}
	first, err := r.PrepareReceipt(ctx, in)
	require.NoError(t, err)
	require.Equal(t, "committing", first.Status)
	replay, err := r.PrepareReceipt(ctx, in)
	require.NoError(t, err)
	require.Equal(t, first.DecisionID, replay.DecisionID)
}

func TestDuplexRejectedReceiptIsTerminal(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	in := DuplexReceipt{DecisionID: "decision-rejected", LessonID: 2, StudentID: 1, Epoch: "epoch-rejected", InputRevision: 12, UtteranceID: "utterance-rejected", TextHash: strings.Repeat("c", 64), Status: "committing"}
	_, err := r.PrepareReceipt(ctx, in)
	require.NoError(t, err)
	require.NoError(t, r.RejectReceipt(ctx, 1, 2, in.DecisionID))
	require.NoError(t, r.RejectReceipt(ctx, 1, 2, in.DecisionID), "rejection is idempotent")
	rejected, err := r.LookupReceipt(ctx, 1, 2, in.DecisionID)
	require.NoError(t, err)
	require.Equal(t, "rejected", rejected.Status)
	_, err = r.PrepareReceipt(ctx, in)
	require.ErrorIs(t, err, ErrDuplexConflict, "a rejected decision cannot be reopened")
}

func TestDuplexCommitDoesNotTreatNonCommittedReceiptAsSuccess(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	c := candidate()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	in := DuplexReceipt{DecisionID: c.DecisionID, LessonID: 2, StudentID: 1, Epoch: c.Epoch, InputRevision: c.InputRevision, UtteranceID: c.UtteranceID, Fingerprint: hex.EncodeToString(sum[:]), Status: "committing"}
	_, err = r.PrepareReceipt(ctx, in)
	require.NoError(t, err)
	require.NoError(t, r.RejectReceipt(ctx, 1, 2, c.DecisionID))
	_, err = r.Commit(ctx, 1, 2, 0, c, nil)
	require.ErrorIs(t, err, ErrDuplexConflict)
	cp, err := r.LoadCheckpoint(ctx, 1, 2)
	require.NoError(t, err)
	require.Zero(t, cp.Version)
	var snapshot RuntimeRecord
	require.ErrorIs(t, db.First(&snapshot, "key=?", "snapshot/2").Error, gorm.ErrRecordNotFound)
	var effects int64
	require.NoError(t, db.Model(&DuplexOutbox{}).Count(&effects).Error)
	require.Zero(t, effects)
}

func TestDuplexCommitSnapshotAtomicallyPublishesStateAndReceipt(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	require.NoError(t, db.Where("lesson_id=?", 2).Delete(&DuplexCheckpoint{}).Error)
	in := DuplexReceipt{DecisionID: "decision-snapshot", LessonID: 2, StudentID: 1, Epoch: "epoch-snapshot", InputRevision: 8, UtteranceID: "utterance-snapshot", TextHash: strings.Repeat("b", 64), Status: "committing"}
	_, err := r.PrepareReceipt(ctx, in)
	require.NoError(t, err)
	require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/2", Value: `{"phase":"before"}`}).Error)
	delivery := &DuplexFeedbackDelivery{SubmissionID: "duplex_utterance-snapshot", StudentID: 1, LessonID: 2, Text: "请减少每天词量"}
	require.NoError(t, r.CommitSnapshot(ctx, 1, 2, in.DecisionID, map[string]any{"phase": "before"}, map[string]any{"phase": "wait_answer"}, delivery))
	var row RuntimeRecord
	require.NoError(t, db.First(&row, "key=?", "snapshot/2").Error)
	require.JSONEq(t, `{"phase":"wait_answer"}`, row.Value)
	got, err := r.LookupReceipt(ctx, 1, 2, in.DecisionID)
	require.NoError(t, err)
	require.Equal(t, "committed", got.Status)
	pending, err := r.PendingFeedbackDeliveries(ctx, 1, 2)
	require.NoError(t, err)
	require.Len(t, pending, 1, "feedback delivery intent commits with the room snapshot")
	require.Equal(t, delivery.SubmissionID, pending[0].SubmissionID)
	require.Equal(t, delivery.Text, pending[0].Text)
	require.ErrorIs(t, r.CommitSnapshot(ctx, 1, 2, in.DecisionID, map[string]any{"phase": "wait_answer"}, map[string]any{"phase": "evil"}), ErrDuplexConflict, "a committed receipt cannot overwrite a snapshot")
	require.NoError(t, db.First(&row, "key=?", "snapshot/2").Error)
	require.JSONEq(t, `{"phase":"wait_answer"}`, row.Value)
}

func TestDuplexCommitSnapshotCanReturnErrorAfterDurableCommit(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	require.NoError(t, db.Where("lesson_id=?", 2).Delete(&DuplexCheckpoint{}).Error)
	in := DuplexReceipt{DecisionID: "decision-uncertain", LessonID: 2, StudentID: 1, Epoch: "epoch-uncertain", InputRevision: 9, UtteranceID: "utterance-uncertain", Status: "committing"}
	_, err := r.PrepareReceipt(ctx, in)
	require.NoError(t, err)
	require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/2", Value: `{"phase":"before"}`}).Error)
	r.afterSnapshotCommit = func() error { return errors.New("simulated lost commit acknowledgement") }
	err = r.CommitSnapshot(ctx, 1, 2, in.DecisionID, map[string]any{"phase": "before"}, map[string]any{"phase": "after"})
	require.Error(t, err)
	var snapshot RuntimeRecord
	require.NoError(t, db.First(&snapshot, "key=?", "snapshot/2").Error)
	require.JSONEq(t, `{"phase":"after"}`, snapshot.Value)
	got, err := r.LookupReceipt(ctx, 1, 2, in.DecisionID)
	require.NoError(t, err)
	require.Equal(t, "committed", got.Status)
}

func TestDuplexLegacySnapshotCASRejectsStaleRoomWriter(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	require.NoError(t, db.Where("lesson_id=?", 2).Delete(&DuplexCheckpoint{}).Error)
	before := map[string]any{"phase": "before"}
	require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/2", Value: `{"phase":"before"}`}).Error)
	for _, decision := range []string{"first", "stale"} {
		_, err := r.PrepareReceipt(ctx, DuplexReceipt{DecisionID: decision, LessonID: 2, StudentID: 1, Epoch: "epoch-" + decision, InputRevision: 1, UtteranceID: "utterance-" + decision, Status: "committing"})
		require.NoError(t, err)
	}
	require.NoError(t, r.CommitSnapshot(ctx, 1, 2, "first", before, map[string]any{"phase": "winner"}))
	require.ErrorIs(t, r.CommitSnapshot(ctx, 1, 2, "stale", before, map[string]any{"phase": "loser"}), ErrDuplexConflict)
	var snapshot RuntimeRecord
	require.NoError(t, db.First(&snapshot, "key=?", "snapshot/2").Error)
	require.JSONEq(t, `{"phase":"winner"}`, snapshot.Value)
	loser, err := r.LookupReceipt(ctx, 1, 2, "stale")
	require.NoError(t, err)
	require.Equal(t, "committing", loser.Status, "stale writer cannot claim commit")
}

func TestDuplexLegacySnapshotWriterCannotBypassForeignCheckpoint(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	require.NoError(t, db.Model(&DuplexCheckpoint{}).Where("lesson_id=?", 2).Update("student_id", 9).Error)
	require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/2", Value: `{"phase":"before"}`}).Error)
	_, err := r.PrepareReceipt(ctx, DuplexReceipt{DecisionID: "foreign-owner", LessonID: 2, StudentID: 1, Epoch: "foreign-epoch", InputRevision: 1, UtteranceID: "foreign-utterance", Status: "committing"})
	require.NoError(t, err)
	require.ErrorIs(t, r.CommitSnapshot(ctx, 1, 2, "foreign-owner", map[string]any{"phase": "before"}, map[string]any{"phase": "attacker"}), ErrDuplexConflict)
	var snapshot RuntimeRecord
	require.NoError(t, db.First(&snapshot, "key=?", "snapshot/2").Error)
	require.JSONEq(t, `{"phase":"before"}`, snapshot.Value)
}

func TestDuplexCheckpointInitializesFromCurrentRuntimeSnapshot(t *testing.T) {
	r, db := duplexRepo(t)
	ctx := context.Background()
	require.NoError(t, db.Model(&DuplexCheckpoint{}).Where("lesson_id=?", 2).Delete(&DuplexCheckpoint{}).Error)
	require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/2", Value: `{"phase":"newest"}`}).Error)
	cp, err := r.LoadOrCreateCheckpointFromRuntimeSnapshot(ctx, 1, 2)
	require.NoError(t, err)
	require.JSONEq(t, `{"phase":"newest"}`, cp.State)
	require.EqualValues(t, 0, cp.Version)
	require.NoError(t, db.Model(&RuntimeRecord{}).Where("key=?", "snapshot/2").Update("value", `{"phase":"later"}`).Error)
	cp, err = r.LoadOrCreateCheckpointFromRuntimeSnapshot(ctx, 1, 2)
	require.NoError(t, err)
	require.JSONEq(t, `{"phase":"newest"}`, cp.State, "an existing checkpoint is authoritative")
	_, err = r.LoadOrCreateCheckpointFromRuntimeSnapshot(ctx, 9, 2)
	require.ErrorIs(t, err, ErrDuplexOwner)
}

func TestDuplexCheckpointConcurrentInitializationUsesOneSnapshot(t *testing.T) {
	r, db := duplexRepo(t)
	require.NoError(t, db.Where("lesson_id=?", 2).Delete(&DuplexCheckpoint{}).Error)
	require.NoError(t, db.Create(&RuntimeRecord{Key: "snapshot/2", Value: `{"phase":"frozen"}`}).Error)
	var files []struct{ File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	otherDB, err := Open(files[0].File)
	require.NoError(t, err)
	pool, err := otherDB.DB()
	require.NoError(t, err)
	defer pool.Close()
	other := NewDuplexRepo(otherDB)
	start := make(chan struct{})
	results := make(chan DuplexCheckpoint, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, repo := range []*DuplexRepo{r, other} {
		wg.Add(1)
		go func(repo *DuplexRepo) {
			defer wg.Done()
			<-start
			cp, e := repo.LoadOrCreateCheckpointFromRuntimeSnapshot(context.Background(), 1, 2)
			results <- cp
			errs <- e
		}(repo)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for cp := range results {
		require.EqualValues(t, 0, cp.Version)
		require.JSONEq(t, `{"phase":"frozen"}`, cp.State)
	}
	var count int64
	require.NoError(t, db.Model(&DuplexCheckpoint{}).Where("lesson_id=?", 2).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestDuplexOutboxLeaseAckAndCrashReplay(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	_, err := r.Commit(ctx, 1, 2, 0, candidate(), nil)
	require.NoError(t, err)
	rows, err := r.ListOutbox(ctx, 1, 2, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	row := rows[0]
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	claimed, err := r.ClaimOutbox(ctx, 1, 2, row.ID, "lease-a", now, time.Second)
	require.NoError(t, err)
	require.Equal(t, "claimed", claimed.Status)
	require.Equal(t, 1, claimed.Attempts)
	_, err = r.ClaimOutbox(ctx, 1, 2, row.ID, "lease-b", now, time.Second)
	require.ErrorIs(t, err, ErrDuplexConflict, "a live lease excludes another worker")

	// A crash after send but before ACK leaves a reclaimable row. The replay
	// retains the same primary key and decision/effect identity.
	require.NoError(t, r.MarkOutboxSent(ctx, 1, 2, row.ID, "lease-a", now, time.Second))
	replayed, err := r.ClaimOutbox(ctx, 1, 2, row.ID, "lease-b", now.Add(time.Second), time.Second)
	require.NoError(t, err)
	require.Equal(t, row.ID, replayed.ID)
	require.Equal(t, row.DecisionID, replayed.DecisionID)
	require.Equal(t, row.EffectIndex, replayed.EffectIndex)
	require.Equal(t, 2, replayed.Attempts)

	// Browser ACK may arrive before the server records its local Send result.
	require.NoError(t, r.AckOutbox(ctx, 1, 2, row.DecisionID, row.EffectIndex, "lease-b", now.Add(1500*time.Millisecond)))
	require.NoError(t, r.MarkOutboxSent(ctx, 1, 2, row.ID, "lease-b", now.Add(2*time.Second), time.Second))
	require.NoError(t, r.AckOutbox(ctx, 1, 2, row.DecisionID, row.EffectIndex, "lease-b", now.Add(3*time.Second)))
	var final DuplexOutbox
	require.NoError(t, r.db.First(&final, row.ID).Error)
	require.Equal(t, "acked", final.Status)
	require.Equal(t, 2, final.Attempts)
	require.NotNil(t, final.AckedAt)
	_, err = r.ClaimOutbox(ctx, 1, 2, row.ID, "lease-c", now.Add(10*time.Second), time.Second)
	require.ErrorIs(t, err, ErrDuplexConflict, "acked effects are terminal")
	require.NoError(t, r.AckOutbox(ctx, 1, 2, row.DecisionID, row.EffectIndex, "lease-b", now.Add(11*time.Second)), "identical ACK retry is idempotent")
	require.ErrorIs(t, r.AckOutbox(ctx, 1, 2, row.DecisionID, row.EffectIndex, "lease-c", now.Add(11*time.Second)), ErrDuplexConflict, "a stale/different worker cannot claim an ACK was accepted")
	require.ErrorIs(t, r.MarkOutboxSent(ctx, 1, 2, row.ID, "lease-c", now.Add(11*time.Second), time.Second), ErrDuplexConflict)
}

func TestDuplexOutboxRejectsWrongOwnerAndInvalidLease(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	_, err := r.Commit(ctx, 1, 2, 0, candidate(), nil)
	require.NoError(t, err)
	rows, err := r.ListOutbox(ctx, 1, 2, 0)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = r.ClaimOutbox(ctx, 9, 2, rows[0].ID, "lease-a", now, time.Second)
	require.ErrorIs(t, err, ErrDuplexConflict)
	_, err = r.ClaimOutbox(ctx, 1, 2, rows[0].ID, "lease-a", now, 0)
	require.ErrorIs(t, err, ErrDuplexConflict)
	require.Error(t, r.AckOutbox(ctx, 9, 2, rows[0].DecisionID, rows[0].EffectIndex, "lease-a", now))
}

func TestDuplexMigrateUpgradesLegacyOutboxAndPreservesRows(t *testing.T) {
	r, db := duplexRepo(t)
	require.NoError(t, db.Exec("DROP TABLE duplex_outboxes").Error)
	require.NoError(t, db.Exec("CREATE TABLE duplex_outboxes (id INTEGER PRIMARY KEY AUTOINCREMENT, decision_id TEXT NOT NULL, effect_index INTEGER NOT NULL, lesson_id INTEGER NOT NULL, student_id INTEGER NOT NULL, payload TEXT NOT NULL, created_at DATETIME, updated_at DATETIME)").Error)
	require.NoError(t, db.Exec("INSERT INTO duplex_outboxes (id, decision_id, effect_index, lesson_id, student_id, payload) VALUES (41, 'legacy-decision', 0, 2, 1, '{\"type\":\"legacy\"}')").Error)
	require.NoError(t, r.Migrate())
	var got DuplexOutbox
	require.NoError(t, db.First(&got, "id=?", 41).Error)
	require.Equal(t, "legacy-decision", got.DecisionID)
	require.Equal(t, "pending", got.Status, "legacy unacknowledged effects migrate to recoverable pending")
	require.Zero(t, got.Attempts)
	require.Empty(t, got.AckLeaseToken)
	require.NoError(t, r.Migrate(), "migration remains idempotent")
}

func TestDuplexOutboxFencesExpiredLeaseWorkers(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	_, err := r.Commit(ctx, 1, 2, 0, candidate(), nil)
	require.NoError(t, err)
	rows, err := r.ListOutbox(ctx, 1, 2, 0)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = r.ClaimOutbox(ctx, 1, 2, rows[0].ID, "old", now, time.Second)
	require.NoError(t, err)
	_, err = r.ClaimOutbox(ctx, 1, 2, rows[0].ID, "new", now.Add(time.Second), time.Second)
	require.NoError(t, err)
	require.ErrorIs(t, r.MarkOutboxSent(ctx, 1, 2, rows[0].ID, "old", now.Add(2*time.Second), time.Second), ErrDuplexConflict)
	require.ErrorIs(t, r.AckOutbox(ctx, 1, 2, rows[0].DecisionID, rows[0].EffectIndex, "old", now.Add(2*time.Second)), ErrDuplexConflict)
	require.NoError(t, r.AckOutbox(ctx, 1, 2, rows[0].DecisionID, rows[0].EffectIndex, "new", now.Add(1500*time.Millisecond)))
}

func TestDuplexOutboxDoesNotDeliverEffectsOutOfOrder(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	c := candidate()
	c.Effects = []json.RawMessage{json.RawMessage(`{"type":"first"}`), json.RawMessage(`{"type":"second"}`)}
	_, err := r.Commit(ctx, 1, 2, 0, c, nil)
	require.NoError(t, err)
	rows, err := r.ListOutbox(ctx, 1, 2, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	now := time.Now().UTC()
	_, err = r.ClaimOutbox(ctx, 1, 2, rows[1].ID, "second", now, time.Second)
	require.ErrorIs(t, err, ErrDuplexConflict)
	// Simulate a corrupted/old consumer that bypassed ClaimOutbox's ordering.
	// The ACK CAS itself must still refuse the later index.
	require.NoError(t, r.db.Model(&DuplexOutbox{}).Where("id=?", rows[1].ID).Updates(map[string]any{"status": "claimed", "lease_token": "second", "lease_until": now.Add(time.Minute)}).Error)
	require.ErrorIs(t, r.AckOutbox(ctx, 1, 2, rows[1].DecisionID, rows[1].EffectIndex, "second", now.Add(time.Second)), ErrDuplexConflict)
	require.NoError(t, r.db.Model(&DuplexOutbox{}).Where("id=?", rows[1].ID).Updates(map[string]any{"status": "pending", "lease_token": "", "lease_until": nil}).Error)
	first, err := r.ClaimOutbox(ctx, 1, 2, rows[0].ID, "first", now, 10*time.Second)
	require.NoError(t, err)
	require.NoError(t, r.AckOutbox(ctx, 1, 2, first.DecisionID, first.EffectIndex, "first", now.Add(time.Second)))
	_, err = r.ClaimOutbox(ctx, 1, 2, rows[1].ID, "second", now.Add(2*time.Second), time.Second)
	require.NoError(t, err)
}

func TestDuplexOutboxAckCannotOvertakeEarlierDecision(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	_, err := r.Commit(ctx, 1, 2, 0, candidate(), nil)
	require.NoError(t, err)
	now := time.Now().UTC()
	later := DuplexOutbox{DecisionID: "legacy-later", EffectIndex: 0, LessonID: 2, StudentID: 1, Payload: `{"type":"later"}`, Status: "claimed", LeaseToken: "later-lease", LeaseUntil: ptrTime(now.Add(time.Minute))}
	require.NoError(t, r.db.Create(&later).Error)
	require.ErrorIs(t, r.AckOutbox(ctx, 1, 2, later.DecisionID, later.EffectIndex, later.LeaseToken, now), ErrDuplexConflict)
}

func ptrTime(value time.Time) *time.Time { return &value }

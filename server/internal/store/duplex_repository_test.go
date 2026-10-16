package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
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
		})
	}
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

func TestDuplexLogicalRevisionCannotCommitTwoUtterances(t *testing.T) {
	r, _ := duplexRepo(t)
	ctx := context.Background()
	_, err := r.Commit(ctx, 1, 2, 0, candidate(), nil)
	require.NoError(t, err)
	next := candidate()
	next.DecisionID = "d2"
	next.UtteranceID = "u2"
	_, err = r.Commit(ctx, 1, 2, 1, next, nil)
	require.ErrorIs(t, err, ErrDuplexConflict)
}

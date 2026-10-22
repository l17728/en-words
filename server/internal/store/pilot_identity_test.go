// Experimental isolated persistence component; no application, HTTP or tool entry point.
package store

import (
	"context"
	"encoding/json"
	"enwords/internal/config"
	"enwords/internal/wordbook"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func pilotCreationFixtureDraft(t *testing.T) (*gorm.DB, *PilotStore, PilotSystemPrincipal, *PilotCreationPlan) {
	t.Helper()
	db, e := Open(filepath.Join(t.TempDir(), "creation.db"))
	require.NoError(t, e)
	pool, e := db.DB()
	require.NoError(t, e)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.Create(&User{ID: 7, Username: "pilot-a", Role: "student", Status: "active"}).Error)
	require.NoError(t, db.Create(&User{ID: 8, Username: "pilot-b", Role: "student", Status: "active"}).Error)
	require.NoError(t, db.Create(&WordBook{ID: 9, Name: "creation-fixture", Status: string(wordbook.StatusPublished)}).Error)
	require.NoError(t, db.Create(&WordEntry{ID: 91, BookID: 9, Word: "cat", NormKey: "cat", Meanings: `["猫"]`}).Error)
	require.NoError(t, db.Create(&WordEntry{ID: 92, BookID: 9, Word: "dog", NormKey: "dog", Meanings: `["狗"]`}).Error)
	require.NoError(t, migratePilotSchema(context.Background(), db))
	facade, e := newPilotStore(context.Background(), db)
	require.NoError(t, e)
	principal, e := BindPilotSystemPrincipal(7, "creation-test-task")
	require.NoError(t, e)
	plan, e := PreparePilotCreationPlan(context.Background(), db, 7, 9, time.Date(2028, 1, 1, 9, 0, 0, 0, time.UTC), []int64{92, 91})
	require.NoError(t, e)
	return db, facade, principal, plan
}

func TestPilotCreationTwoPoolsDraft(t *testing.T) {
	for _, scenario := range []string{"same_nonce", "different_nonce_same_scope", "different_scope"} {
		t.Run(scenario, func(t *testing.T) {
			db, first, principal, plan := pilotCreationFixtureDraft(t)
			var files []struct {
				Seq        int
				Name, File string
			}
			require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
			var path string
			for _, f := range files {
				if f.Name == "main" {
					path = f.File
				}
			}
			require.NotEmpty(t, path)
			otherDB, e := Open(path)
			require.NoError(t, e)
			otherPool, e := otherDB.DB()
			require.NoError(t, e)
			defer otherPool.Close()
			second, e := newPilotStore(context.Background(), otherDB)
			require.NoError(t, e)
			principals := []PilotSystemPrincipal{principal, principal}
			plans := []*PilotCreationPlan{plan, plan}
			if scenario != "same_nonce" {
				sid := int64(7)
				if scenario == "different_scope" {
					sid = 8
					principals[1], e = BindPilotSystemPrincipal(sid, "second-scope")
					require.NoError(t, e)
				}
				plans[1], e = PreparePilotCreationPlan(context.Background(), otherDB, sid, 9, time.UnixMilli(plan.data.OpenAtMS), []int64{92, 91})
				require.NoError(t, e)
			}
			facades := []*PilotStore{first, second}
			receipts := make([]PilotCreationReceipt, 2)
			errs := make([]error, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					receipts[i], errs[i] = facades[i].CreateIsolatedPilotLesson(context.Background(), principals[i], plans[i])
				}(i)
			}
			close(start)
			wg.Wait()
			successes := 0
			for i, e := range errs {
				if e == nil {
					successes++
				} else {
					require.Equal(t, PilotCreationReceipt{}, receipts[i])
				}
			}
			expected := int64(1)
			switch scenario {
			case "same_nonce":
				require.Equal(t, 2, successes)
				require.Equal(t, receipts[0].LessonID, receipts[1].LessonID)
				require.NotEqual(t, receipts[0].Replayed, receipts[1].Replayed)
			case "different_nonce_same_scope":
				require.Equal(t, 1, successes)
			case "different_scope":
				require.Equal(t, 2, successes)
				require.NotEqual(t, receipts[0].LessonID, receipts[1].LessonID)
				expected = 2
			}
			for _, table := range []string{"lessons", "classroom_scope_reservations", "classroom_mode_reservations", "classroom_pilot_states"} {
				var count int64
				require.NoError(t, otherDB.Table(table).Count(&count).Error)
				require.Equal(t, expected, count)
			}
		})
	}
}

func TestPilotCreationReplayRejectsFrozenIdentityDamageDraft(t *testing.T) {
	for _, field := range []string{"content_fingerprint", "config_fingerprint"} {
		t.Run(field, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			first, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", first.LessonID).Update(field, "0000000000000000000000000000000000000000000000000000000000000000").Error)
			receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.Error(t, e)
			require.Equal(t, PilotCreationReceipt{}, receipt)
		})
	}
	for _, damage := range []string{"reverse_words", "other_book", "other_word_id", "change_meaning"} {
		t.Run(damage, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			first, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			var raw string
			require.NoError(t, db.Table("classroom_pilot_states").Select("frozen_words_json").Where("lesson_id=?", first.LessonID).Scan(&raw).Error)
			var words []wordbook.Entry
			require.NoError(t, json.Unmarshal([]byte(raw), &words))
			switch damage {
			case "reverse_words":
				words[0], words[1] = words[1], words[0]
			case "other_book":
				words[0].BookID = 999
			case "other_word_id":
				words[0].ID = 999
			case "change_meaning":
				words[0].Meanings = []string{"changed"}
			}
			encoded, e := json.Marshal(words)
			require.NoError(t, e)
			require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", first.LessonID).Update("frozen_words_json", string(encoded)).Error)
			receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.Error(t, e)
			require.Equal(t, PilotCreationReceipt{}, receipt)
		})
	}
}

func TestPilotCreationReplayRejectsScopeDamageDraft(t *testing.T) {
	for _, damage := range []string{"missing", "legacy"} {
		t.Run(damage, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			_, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			// Deliberate corruption on ONE owned physical connection, restoring exact
			// DDL and FK setting before exercising loader. This is not a product path.
			pool, e := db.DB()
			require.NoError(t, e)
			conn, e := pool.Conn(context.Background())
			require.NoError(t, e)
			ctx := context.Background()
			trigger := "classroom_scope_no_delete"
			statement := "DELETE FROM classroom_scope_reservations WHERE student_id=7 AND book_id=9"
			if damage == "legacy" {
				trigger = "classroom_scope_no_update"
				statement = "UPDATE classroom_scope_reservations SET mode='legacy' WHERE student_id=7 AND book_id=9"
			}
			var ddl string
			require.NoError(t, conn.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", trigger).Scan(&ddl))
			_, e = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
			require.NoError(t, e)
			_, e = conn.ExecContext(ctx, "DROP TRIGGER "+trigger)
			require.NoError(t, e)
			_, e = conn.ExecContext(ctx, statement)
			require.NoError(t, e)
			_, e = conn.ExecContext(ctx, ddl)
			require.NoError(t, e)
			_, e = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			require.NoError(t, e)
			require.NoError(t, conn.Close())
			receipt, e := facade.CreateIsolatedPilotLesson(ctx, principal, plan)
			require.Error(t, e)
			require.Equal(t, PilotCreationReceipt{}, receipt)
		})
	}
}

func TestPilotCreationAndNonceReplayFreezeDraft(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	first, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, e)
	require.Positive(t, first.LessonID)
	require.False(t, first.Replayed)
	var row struct {
		StateJSON, RoomFactsJSON, FrozenWordsJSON               string
		CheckpointVersion, LogicalInputRevision, AuthGeneration int64
	}
	require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", first.LessonID).Take(&row).Error)
	_, _, e = decodePilotCreatedBaseline([]byte(row.StateJSON), []byte(row.RoomFactsJSON))
	require.NoError(t, e)
	require.EqualValues(t, 1, row.CheckpointVersion)
	require.Zero(t, row.LogicalInputRevision)
	require.Zero(t, row.AuthGeneration)
	var words []wordbook.Entry
	require.NoError(t, json.Unmarshal([]byte(row.FrozenWordsJSON), &words))
	require.Equal(t, []int64{92, 91}, []int64{words[0].ID, words[1].ID})
	cfg, e := config.NewStore(NewConfigPersister(db), nil)
	require.NoError(t, e)
	require.NoError(t, cfg.SetGlobal("test", "wordsPerLesson", 70, "replay uses original freeze"))
	require.NoError(t, db.Model(&WordEntry{}).Where("id=?", 91).Update("word", "changed-after-creation").Error)
	require.NoError(t, db.Model(&WordBook{}).Where("id=?", 9).Update("status", "draft").Error)
	replay, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, e)
	require.True(t, replay.Replayed)
	replay.Replayed = false
	require.Equal(t, first, replay)
	var after string
	require.NoError(t, db.Table("classroom_pilot_states").Select("frozen_words_json").Where("lesson_id=?", first.LessonID).Scan(&after).Error)
	require.Equal(t, row.FrozenWordsJSON, after)
	var count int64
	require.NoError(t, db.Model(&Lesson{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	// Authorization remains current even for a known valid nonce.
	require.NoError(t, db.Model(&User{}).Where("id=?", 7).Update("status", "disabled").Error)
	denied, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.Error(t, e)
	require.Equal(t, PilotCreationReceipt{}, denied)
}

func TestPilotCreationRejectsOwnerAndConfigABADraft(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	other, e := BindPilotSystemPrincipal(8, "other-task")
	require.NoError(t, e)
	receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), other, plan)
	require.Error(t, e)
	require.Equal(t, PilotCreationReceipt{}, receipt)
	cfg, e := config.NewStore(NewConfigPersister(db), nil)
	require.NoError(t, e)
	require.NoError(t, cfg.SetGlobal("test", "wordsPerLesson", 70, "ABA change"))
	require.NoError(t, cfg.SetGlobal("test", "wordsPerLesson", 60, "ABA restore"))
	receipt, e = facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.ErrorIs(t, e, errPilotPlanStale)
	require.Equal(t, PilotCreationReceipt{}, receipt)
	for _, table := range []string{"lessons", "classroom_scope_reservations", "classroom_mode_reservations", "classroom_pilot_states"} {
		var n int64
		require.NoError(t, db.Table(table).Count(&n).Error)
		require.Zero(t, n)
	}
}

func TestPilotCreationCommitFailurePublishesNoReceiptDraft(t *testing.T) {
	db, facade, principal, plan := pilotCreationFixtureDraft(t)
	var events []PilotStoreEvent
	facade.SetObserver(func(_ context.Context, event PilotStoreEvent) { events = append(events, event) })
	// Deferred FK fails at COMMIT, not during lesson INSERT.
	require.NoError(t, db.Exec("CREATE TABLE creation_fault_parent(id INTEGER PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE creation_fault_child(parent_id INTEGER REFERENCES creation_fault_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
	require.NoError(t, db.Exec("CREATE TRIGGER creation_commit_fault AFTER INSERT ON lessons BEGIN INSERT INTO creation_fault_child(parent_id) VALUES(12345); END").Error)
	receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.Error(t, e)
	require.Equal(t, PilotCreationReceipt{}, receipt)
	require.Len(t, events, 1)
	require.Equal(t, PilotRejected, events[0].Outcome)
	require.Equal(t, PilotCommitFailed, events[0].Reason)
	require.Zero(t, events[0].LessonID)
	for _, table := range []string{"lessons", "classroom_scope_reservations", "classroom_mode_reservations", "classroom_pilot_states", "creation_fault_child"} {
		var n int64
		require.NoError(t, db.Table(table).Count(&n).Error)
		require.Zero(t, n)
	}
	require.NoError(t, db.Exec("DROP TRIGGER creation_commit_fault").Error)
	receipt, e = facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
	require.NoError(t, e)
	require.Positive(t, receipt.LessonID)
	require.False(t, receipt.Replayed)
	require.Len(t, events, 2)
	require.Equal(t, PilotCommitted, events[1].Outcome)
	require.Equal(t, PilotNoFailure, events[1].Reason)
}

func TestPilotCreationObserverClassificationAndPrivacyDraft(t *testing.T) {
	for _, scenario := range []string{"created_replay", "cancelled", "query_failure", "unauthorized"} {
		t.Run(scenario, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			var events []PilotStoreEvent
			traceKey := struct{}{}
			ctx := context.WithValue(context.Background(), traceKey, "fixed-test-trace")
			facade.SetObserver(func(got context.Context, event PilotStoreEvent) {
				require.Equal(t, "fixed-test-trace", got.Value(traceKey))
				events = append(events, event)
			})
			expectedReason := PilotNoFailure
			expectedOutcome := PilotCommitted
			if scenario == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expectedReason = PilotCancelled
				expectedOutcome = PilotRejected
			}
			failure := errors.New("private injected database detail")
			if scenario == "query_failure" {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("pilot_owner_lookup_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(failure)
					}
				}))
				defer db.Callback().Query().Remove("pilot_owner_lookup_failure")
				expectedReason = PilotOperationFailed
				expectedOutcome = PilotRejected
			}
			if scenario == "unauthorized" {
				require.NoError(t, db.Model(&User{}).Where("id=?", 7).Update("status", "disabled").Error)
				expectedReason = PilotUnauthorized
				expectedOutcome = PilotRejected
			}
			receipt, e := facade.CreateIsolatedPilotLesson(ctx, principal, plan)
			if expectedOutcome == PilotCommitted {
				require.NoError(t, e)
			} else {
				require.Error(t, e)
				require.Equal(t, PilotCreationReceipt{}, receipt)
			}
			if scenario == "query_failure" {
				require.ErrorIs(t, e, failure, "storage errors must not become authorization failures")
			}
			require.Len(t, events, 1)
			require.Equal(t, expectedReason, events[0].Reason)
			require.Equal(t, expectedOutcome, events[0].Outcome)
			if scenario == "created_replay" {
				again, e := facade.CreateIsolatedPilotLesson(ctx, principal, plan)
				require.NoError(t, e)
				require.True(t, again.Replayed)
				require.Len(t, events, 2)
				require.True(t, events[1].Replayed)
				require.Equal(t, PilotCommitted, events[1].Outcome)
			}
			for _, event := range events {
				require.GreaterOrEqual(t, event.WriterWaitMS, int64(0))
				require.GreaterOrEqual(t, event.TransactionMS, int64(0))
			}
			raw, e := json.Marshal(events)
			require.NoError(t, e)
			for _, private := range []string{plan.data.Nonce, plan.fingerprint, "creation-test-task", "private injected database detail", "frozen_words_json"} {
				require.NotContains(t, string(raw), private)
			}
		})
	}
	var absent *PilotStore
	require.NotPanics(t, func() {
		receipt, e := absent.CreateIsolatedPilotLesson(nil, PilotSystemPrincipal{}, nil)
		require.Error(t, e)
		require.Equal(t, PilotCreationReceipt{}, receipt)
	})
}

func TestPilotCreationReplayRejectsCorruptCheckpointDraft(t *testing.T) {
	for _, field := range []string{"state_json", "room_facts_json", "frozen_words_json", "frozen_config_json", "config_revisions_json", "creation_plan_json"} {
		t.Run(field, func(t *testing.T) {
			db, facade, principal, plan := pilotCreationFixtureDraft(t)
			first, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.NoError(t, e)
			require.NoError(t, db.Table("classroom_pilot_states").Where("lesson_id=?", first.LessonID).Update(field, "{}").Error)
			receipt, e := facade.CreateIsolatedPilotLesson(context.Background(), principal, plan)
			require.Error(t, e, "must not issue success from owner-only row validation")
			require.Equal(t, PilotCreationReceipt{}, receipt)
		})
	}
}

// Still-required integrated cases, not mocked here: each legacy fact/bridge
// rejects; actual trusted task adapter role/scope authorization. All cases
// above are drafts only, not executed evidence. Owner principal binding alone
// is NOT evidence that the task authorization boundary has been implemented.

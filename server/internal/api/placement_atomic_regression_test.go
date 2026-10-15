package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlacementHTTPLogsCommitAndPartialFinishWithoutPrivateErrors(t *testing.T) {
	for _, mode := range []string{"success", "draft_failure", "finish_failure"} {
		t.Run(mode, func(t *testing.T) {
			a, db, bid, cards := placementAtomicFixture(t)
			_, err := a.Auth.CreateUser("placement-user", "fixture-password", "student", auth.RoleStudent)
			require.NoError(t, err)
			require.NoError(t, store.NewAuthStore(db).SaveUser(a.Auth, "placement-user"))
			token, uid := loginGet(t, a, "placement-user", "fixture-password")
			require.NoError(t, a.EnableBook(uid, bid))
			var output bytes.Buffer
			a.SetLogger(obs.New(&output))
			if mode == "draft_failure" {
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:private_draft_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "placement_drafts" {
						tx.AddError(errors.New("private-sql-password"))
					}
				}))
			}
			if mode == "finish_failure" {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:private_plan_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "lessons" {
						tx.AddError(errors.New("private-sql-password"))
					}
				}))
			}
			payload, err := json.Marshal(map[string]any{"knownWordIds": []int64{cards[0].WordID}, "finish": mode == "finish_failure"})
			require.NoError(t, err)
			req := httptest.NewRequest("POST", "/api/v1/student/books/"+itoa64(bid)+"/placement", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(obs.WithTrace(req.Context(), "placement-fixed-trace"))
			w := httptest.NewRecorder()
			a.Router().ServeHTTP(w, req)
			resp := doJSON(t, w)
			if mode == "success" {
				require.Zero(t, resp.Code)
			} else {
				require.NotZero(t, resp.Code)
			}
			require.NotContains(t, w.Body.String()+output.String(), "private-sql-password")
			saved, failed, planFailed := 0, 0, 0
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				switch event["event"] {
				case "placement_saved":
					saved++
					require.Equal(t, w.Header().Get("X-Trace-ID"), event["trace_id"])
					p := event
					require.EqualValues(t, 1, p["input_count"])
					require.EqualValues(t, 1, p["changed_count"])
					require.NotContains(t, p, "known_ids")
				case "placement_save_failed":
					failed++
				case "placement_plan_failed":
					planFailed++
				}
			}
			if mode == "draft_failure" {
				require.Zero(t, saved)
				require.Equal(t, 1, failed)
				require.Empty(t, a.Sched.PlacementDraft(uid, bid))
			} else {
				require.Equal(t, 1, saved)
				require.Equal(t, []int64{cards[0].WordID}, a.Sched.PlacementDraft(uid, bid))
			}
			if mode == "finish_failure" {
				require.Equal(t, 1, planFailed)
				require.Contains(t, resp.Msg, "已保存")
			}
		})
	}
}

func placementAtomicFixture(t *testing.T) (*App, *gorm.DB, int64, []store.MemoryCard) {
	t.Helper()
	a, db, bid := importAtomicApp(t)
	_, err := a.Books.ImportBatch(bid, importAtomicMany(105))
	require.NoError(t, err)
	require.NoError(t, a.Books.Publish(bid))
	require.NoError(t, a.EnableBook(701, bid))
	var cards []store.MemoryCard
	require.NoError(t, db.Where("student_id=? AND book_id=?", 701, bid).Order("word_id").Find(&cards).Error)
	return a, db, bid, cards
}

func TestPlacementAtomicProtectsProgressAndDraftOrder(t *testing.T) {
	a, db, bid, cards := placementAtomicFixture(t)
	at := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	for i, status := range []string{"learning", "graduated"} {
		cards[i].Status = status
		cards[i].LadderStep = 3
		cards[i].BoostFlag = true
		cards[i].BoostSequence = 9
		cards[i].LastReviewAt = &at
		require.NoError(t, db.Save(&cards[i]).Error)
	}
	known := []int64{cards[104].WordID, cards[2].WordID, cards[0].WordID, cards[104].WordID}
	require.NoError(t, a.Sched.MarkPlacementContext(nil, 701, bid, known))
	var actual []store.MemoryCard
	require.NoError(t, db.Where("student_id=? AND book_id=?", 701, bid).Order("word_id").Find(&actual).Error)
	for i := range cards {
		expected := cards[i]
		if i == 2 || i == 104 {
			expected.Status = "mastered"
		}
		require.Equal(t, expected, actual[i])
	}
	require.Equal(t, known, a.Sched.PlacementDraft(701, bid))
	known[0] = -1
	require.Equal(t, cards[104].WordID, a.Sched.PlacementDraft(701, bid)[0])
}

func TestPlacementCancellationAndCommitFailureLeavePriorState(t *testing.T) {
	for _, mode := range []string{"pre_canceled", "after_card_update", "commit"} {
		t.Run(mode, func(t *testing.T) {
			a, db, bid, before := placementAtomicFixture(t)
			require.NoError(t, a.Sched.MarkPlacement(701, bid, nil))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "pre_canceled" {
				cancel()
			}
			if mode == "after_card_update" {
				require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:placement_cancel", func(tx *gorm.DB) {
					if tx.Statement.Table == "memory_cards" {
						cancel()
					}
				}))
			}
			if mode == "commit" {
				require.NoError(t, db.Exec("CREATE TABLE ix15_parent(id INTEGER PRIMARY KEY)").Error)
				require.NoError(t, db.Exec("CREATE TABLE ix15_child(parent_id INTEGER REFERENCES ix15_parent(id) DEFERRABLE INITIALLY DEFERRED)").Error)
				require.NoError(t, db.Exec("CREATE TRIGGER ix15_commit_failure AFTER UPDATE ON memory_cards BEGIN INSERT INTO ix15_child VALUES(999); END").Error)
			}
			ids := make([]int64, len(before))
			for i := range before {
				ids[i] = before[i].WordID
			}
			require.Error(t, a.Sched.MarkPlacementContext(ctx, 701, bid, ids))
			var after []store.MemoryCard
			require.NoError(t, db.Where("student_id=? AND book_id=?", 701, bid).Order("word_id").Find(&after).Error)
			require.Equal(t, before, after)
			require.Empty(t, a.Sched.PlacementDraft(701, bid))
			if mode == "commit" {
				require.NoError(t, db.Exec("DROP TRIGGER ix15_commit_failure").Error)
			}
			require.NoError(t, db.Callback().Update().Remove("test:placement_cancel"))
			require.NoError(t, a.Sched.MarkPlacement(701, bid, ids))
			require.Equal(t, ids, a.Sched.PlacementDraft(701, bid))
		})
	}
}

func TestPlacementAcrossIndependentPoolsKeepsCardsAndDraftConsistent(t *testing.T) {
	a, db, bid, cards := placementAtomicFixture(t)
	var files []struct{ File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&files).Error)
	otherDB, err := store.Open(files[0].File)
	require.NoError(t, err)
	pool, err := otherDB.DB()
	require.NoError(t, err)
	defer pool.Close()
	other := store.NewSchedulerRepo(otherDB)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; errs <- a.Sched.MarkPlacement(701, bid, []int64{cards[0].WordID}) }()
	go func() {
		defer wg.Done()
		<-start
		_, e := other.MarkPlacementAtomic(context.Background(), 701, bid, []int64{cards[1].WordID})
		errs <- e
	}()
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	draft := a.Sched.PlacementDraft(701, bid)
	require.Len(t, draft, 1)
	var mastered []store.MemoryCard
	require.NoError(t, db.Where("student_id=? AND book_id=? AND status=?", 701, bid, "mastered").Find(&mastered).Error)
	require.Len(t, mastered, 1)
	require.Equal(t, draft[0], mastered[0].WordID)
}

func TestPlacementUnchangedCardsDoNotWriteEachRow(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	_, err := app.Books.ImportBatch(bid, importAtomicMany(100))
	require.NoError(t, err)
	require.NoError(t, app.Books.Publish(bid))
	require.NoError(t, app.EnableBook(701, bid))
	writes := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:placement_writes", func(tx *gorm.DB) {
		if tx.Statement.Table == "memory_cards" {
			writes++
		}
	}))
	require.NoError(t, app.Sched.MarkPlacement(701, bid, nil))
	require.Zero(t, writes, "unchanged learning cards must not cause N durable updates")
}

func TestPlacementDraftFailureRollsBackChangedCards(t *testing.T) {
	app, db, bid := importAtomicApp(t)
	_, err := app.Books.ImportBatch(bid, importAtomicInputs())
	require.NoError(t, err)
	require.NoError(t, app.Books.Publish(bid))
	require.NoError(t, app.EnableBook(701, bid))
	var entries []store.WordEntry
	require.NoError(t, db.Where("book_id=?", bid).Order("id").Find(&entries).Error)
	require.NoError(t, app.Sched.MarkPlacement(701, bid, []int64{entries[0].ID}))
	var before []store.MemoryCard
	require.NoError(t, db.Where("student_id=? AND book_id=?", 701, bid).Order("word_id").Find(&before).Error)
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == "placement_drafts" {
			tx.AddError(errors.New("injected draft write failure"))
		}
	}
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:placement_draft_create", fail))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:placement_draft_update", fail))
	require.Error(t, app.Sched.MarkPlacement(701, bid, []int64{entries[1].ID}))
	var after []store.MemoryCard
	require.NoError(t, db.Where("student_id=? AND book_id=?", 701, bid).Order("word_id").Find(&after).Error)
	require.Equal(t, before, after, "draft and changed card states must commit together")
	require.Equal(t, []int64{entries[0].ID}, app.Sched.PlacementDraft(701, bid))
}

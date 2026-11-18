package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/classroom"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func durableSettlementFixture(t *testing.T) (*App, int64, int64, int64, *classroom.Result) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "settlement.db"))
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	clock := &fakeClock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	a := NewApp(clock, db)
	require.Equal(t, 0, newAppBootstrap(t, a))
	_, err = a.Auth.CreateUser("settlement-student", "fixturepw", "Student", auth.RoleStudent)
	require.NoError(t, err)
	// Persistent fixtures must satisfy the same DB-backed identity contract as real accounts.
	require.NoError(t, store.NewAuthStore(db).SaveUser(a.Auth, "settlement-student"))
	_, sid := loginGet(t, a, "settlement-student", "fixturepw")
	admin := loginGet2(t, a)
	bid := seedBook(t, a, admin, "Settlement fixture", "g7", []string{"cat", "dog"})
	require.NoError(t, a.EnableBook(sid, bid))
	require.NoError(t, a.PlacementSubmit(sid, bid, nil, true))
	lessons, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, lessons, 1)
	res := &classroom.Result{TotalWords: 2, Correct: map[int64]bool{}, Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: 0}}
	for _, wid := range lessons[0].BatchWordIDs {
		res.Correct[wid] = false
	}
	require.Len(t, res.Correct, 2)
	return a, sid, bid, lessons[0].ID, res
}

func TestDurableSettlementDuplicateAndFailedWords(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	require.NoError(t, a.Cfg.SetStudent("system_agent", sid, "wordsPerLesson", 50, "fixture lower bound"))
	st := classroom.State{Finished: true}
	require.NoError(t, a.commitLesson(lid, sid, bid, res, st))
	require.NoError(t, a.commitLesson(lid, sid, bid, res, st))
	lessons, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, lessons, 2)
	finished, scheduled := 0, 0
	for _, lesson := range lessons {
		if lesson.Status == scheduler.LessonFinishedStr {
			finished++
		}
		if lesson.Status == scheduler.LessonScheduledStr {
			scheduled++
		}
	}
	require.Equal(t, 1, finished)
	require.Equal(t, 1, scheduled)
	require.Len(t, a.Notifications(sid), 1, "duplicate commit must not duplicate notification")
	require.Equal(t, 50, a.Cfg.Int(sid, "wordsPerLesson"))
	for wid := range res.Correct {
		card, err := a.Sched.GetCard(sid, bid, wid)
		require.NoError(t, err)
		require.Equal(t, scheduler.StatusLearning, card.Status, "failed new word is still a taught word")
		require.Zero(t, card.LadderStep)
		require.NotNil(t, card.LastReviewAt)
	}
	report, ok := a.Report(lid)
	require.True(t, ok)
	require.Equal(t, res.Correct, report.Correct)
}

func TestDurableSettlementRollbackAndRetry(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	require.NoError(t, a.Cfg.SetStudent("system_agent", sid, "wordsPerLesson", 60, "fixture rollback"))
	beforeCards := map[int64]scheduler.Card{}
	for wid := range res.Correct {
		c, err := a.Sched.GetCard(sid, bid, wid)
		require.NoError(t, err)
		beforeCards[wid] = *c
	}
	// Abort after card/lesson/config mutations, at report insert inside the same transaction.
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_fixture_report BEFORE INSERT ON runtime_records WHEN NEW.key LIKE 'report/%' BEGIN SELECT RAISE(ABORT, 'fixture report failure'); END`).Error)
	err := a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true})
	require.Error(t, err)
	lesson, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, scheduler.LessonScheduledStr, lesson.Status)
	for wid, want := range beforeCards {
		got, err := a.Sched.GetCard(sid, bid, wid)
		require.NoError(t, err)
		require.Equal(t, want, *got)
	}
	require.Equal(t, 60, a.Cfg.Int(sid, "wordsPerLesson"))
	require.Empty(t, a.Notifications(sid))
	_, exists := a.Report(lid)
	require.False(t, exists)
	lessons, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, lessons, 1)
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_fixture_report`).Error)
	require.NoError(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
	require.Len(t, a.Notifications(sid), 1)
	require.Equal(t, 50, a.Cfg.Int(sid, "wordsPerLesson"))
}

func TestDurableSettlementUncertainCommitIsNotReportedAsRollbackOrRetried(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	writes := 0
	a.atomicWriteObserved = func(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(store.AtomicConnectionTiming)) error {
		writes++
		if err := store.WithAtomicWrite(ctx, db, fn); err != nil {
			return err
		}
		observe(store.AtomicConnectionTiming{Outcome: "outcome_uncertain"})
		return errors.New("fixture: settlement commit receipt lost")
	}
	err := a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true})
	require.ErrorIs(t, err, errAtomicOutcomeUncertain)
	require.Equal(t, 1, writes)
	require.Zero(t, submissionCount(t, a, "audit_logs", "result LIKE 'rolled_back_%'"))
	lesson, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, scheduler.LessonFinishedStr, lesson.Status, "the fixture commits before losing its receipt")
	require.Len(t, a.Notifications(sid), 1)

	// A duplicate attempt must observe the durable commit and must not repeat side effects.
	a.atomicWriteObserved = nil
	require.NoError(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
	require.Len(t, a.Notifications(sid), 1)
	require.Equal(t, 1, writes)
}

func TestRoomDoesNotRetrySettlementAfterUncertainCommit(t *testing.T) {
	a, sid, _, lid, _ := durableSettlementFixture(t)
	r, _, err := a.Rooms.Enter(lid, sid)
	require.NoError(t, err)
	state := r.machine.Snapshot()
	state.Phase = classroom.PhaseLessonEnd
	state.Finished = true
	require.NoError(t, r.machine.Restore(state))
	writes := 0
	a.atomicWriteObserved = func(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(store.AtomicConnectionTiming)) error {
		writes++
		if err := store.WithAtomicWrite(ctx, db, fn); err != nil {
			return err
		}
		observe(store.AtomicConnectionTiming{Outcome: "outcome_uncertain"})
		return errors.New("fixture: settlement receipt lost")
	}
	r.mu.Lock()
	frames := r.maybeFinishLocked()
	require.True(t, r.settlementUncertain)
	require.Equal(t, 1, writes)
	require.Equal(t, "outcome_uncertain", frames[len(frames)-1]["outcome"])
	require.True(t, r.snapshotLocked()["settlement_uncertain"].(bool))
	require.Empty(t, r.maybeFinishLocked(), "must not re-run settlement after an unknown commit outcome")
	require.Equal(t, 1, writes)
	r.mu.Unlock()
}

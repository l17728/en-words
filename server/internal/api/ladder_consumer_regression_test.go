package api

import (
	"context"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/wordbook"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

// Append only after the current batch exists; enable is idempotent and creates
// the real remaining card without changing the lesson or fabricating progress.
func ladderRemainingNewWord(t *testing.T, a *App, sid, bid, lid int64) int64 {
	t.Helper()
	lesson, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	batch := append([]int64(nil), lesson.BatchWordIDs...)
	imported, err := a.Books.ImportBatch(bid, []wordbook.EntryInput{{Word: "elephant", Meanings: []string{"elephant meaning"}}})
	require.NoError(t, err)
	require.Equal(t, 1, imported.Inserted)
	entries, _, err := a.Books.ListEntries(bid, 1, 1000)
	require.NoError(t, err)
	var remaining int64
	for _, entry := range entries {
		if entry.Word == "elephant" {
			require.Equal(t, bid, entry.BookID)
			remaining = entry.ID
		}
	}
	require.NotZero(t, remaining)
	require.NotContains(t, batch, remaining)
	require.NoError(t, a.EnableBook(sid, bid))
	card, err := a.Sched.GetCard(sid, bid, remaining)
	require.NoError(t, err)
	require.Equal(t, scheduler.StatusNew, card.Status)
	require.False(t, card.BoostFlag)
	require.Nil(t, card.LastReviewAt)
	after, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, batch, after.BatchWordIDs)
	return remaining
}

func TestLadderReloadFailureReachesNewRoomAndNextLesson(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	a.TestMode = false
	remaining := ladderRemainingNewWord(t, a, sid, bid, lid)
	p := &ladderReloadFault{Persister: store.NewConfigPersister(a.DB)}
	cfg, err := config.NewStore(p, nil)
	require.NoError(t, err)
	a.Cfg = cfg
	p.armed = true
	result, err := a.ChangeConfig(context.Background(), config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{3, 4, 5}, Reason: "fresh consumers"})
	require.NoError(t, err)
	require.True(t, result.RefreshPending)
	require.Len(t, a.Cfg.Ladder(sid), 8)
	room, _, err := a.Rooms.EnterContext(context.Background(), lid, sid)
	require.NoError(t, err)
	require.Equal(t, []int{3, 4, 5}, room.Machine().Snapshot().Config.Ladder)
	require.NoError(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
	lessons, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, lessons, 2)
	for wid := range res.Correct {
		card, err := a.Sched.GetCard(sid, bid, wid)
		require.NoError(t, err)
		require.NotNil(t, card.LastReviewAt)
		require.NotNil(t, card.NextDueAt)
		require.Equal(t, 72*time.Hour, card.NextDueAt.Sub(*card.LastReviewAt))
	}
	for _, lesson := range lessons {
		if lesson.ID != lid {
			finished, err := a.Sched.Lesson(lid)
			require.NoError(t, err)
			require.NotNil(t, finished.FinishedAt)
			require.Contains(t, lesson.BatchWordIDs, remaining)
			require.Equal(t, 72*time.Hour, lesson.OpenAt.Sub(*finished.FinishedAt))
		}
	}
}

func TestLadderOldRoomRemainsFrozenWhileMemoryCompletionAndConfigSerialize(t *testing.T) {
	for _, order := range []string{"race", "config_first", "settlement_first"} {
		t.Run(order, func(t *testing.T) {
			a, _, lid, room := setupRoom(t)
			a.TestMode = false
			sid, bid := room.studentID, room.bookID
			frozen := append([]int(nil), room.Machine().Snapshot().Config.Ladder...)
			remaining := ladderRemainingNewWord(t, a, sid, bid, lid)
			lesson, err := a.Sched.Lesson(lid)
			require.NoError(t, err)
			correct := map[int64]bool{}
			for _, wid := range lesson.BatchWordIDs {
				correct[wid] = false
			}
			result := &classroom.Result{TotalWords: len(correct), Correct: correct, Report: classroom.LessonReport{TotalWords: len(correct), FirstTryRate: 0}}
			start := make(chan struct{})
			configDone := make(chan struct{})
			settledDone := make(chan struct{})
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				defer close(configDone)
				<-start
				if order == "settlement_first" {
					<-settledDone
				}
				_, e := a.ChangeConfig(context.Background(), config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{3, 4, 5}, Reason: "concurrent"})
				errs <- e
			}()
			go func() {
				defer wg.Done()
				defer close(settledDone)
				<-start
				if order == "config_first" {
					<-configDone
				}
				errs <- a.commitLesson(lid, sid, bid, result, classroom.State{Finished: true})
			}()
			close(start)
			wg.Wait()
			close(errs)
			for e := range errs {
				require.NoError(t, e)
			}
			require.Equal(t, []int{3, 4, 5}, a.Cfg.Ladder(sid))
			require.Equal(t, frozen, room.Machine().Snapshot().Config.Ladder)
			lessons, err := a.Sched.Lessons(sid, bid)
			require.NoError(t, err)
			require.Len(t, lessons, 2)
			finished, err := a.Sched.Lesson(lid)
			require.NoError(t, err)
			require.NotNil(t, finished.FinishedAt)
			var interval time.Duration
			for wid := range correct {
				card, err := a.Sched.GetCard(sid, bid, wid)
				require.NoError(t, err)
				require.NotNil(t, card.NextDueAt)
				require.NotNil(t, card.LastReviewAt)
				actual := card.NextDueAt.Sub(*card.LastReviewAt)
				if interval == 0 {
					interval = actual
				}
				require.Equal(t, interval, actual, "no mixed serial outcomes across cards")
			}
			switch order {
			case "config_first":
				require.Equal(t, 72*time.Hour, interval)
			case "settlement_first":
				require.Equal(t, 24*time.Hour, interval)
			default:
				require.Contains(t, []time.Duration{24 * time.Hour, 72 * time.Hour}, interval)
			}
			for _, next := range lessons {
				if next.ID != lid {
					require.Contains(t, next.BatchWordIDs, remaining)
					require.Equal(t, interval, next.OpenAt.Sub(*finished.FinishedAt), "card due and next lesson must share one serial outcome")
				}
			}
			require.Len(t, a.Notifications(sid), 1)
		})
	}
}

func TestLadderMixedBackendRefusesWithoutChangingNativeCards(t *testing.T) {
	a, _, _, room := setupRoom(t)
	durable, _, _ := feedbackTwoApps(t)
	cfg, err := config.NewStore(store.NewConfigPersister(durable.DB), nil)
	require.NoError(t, err)
	a.Cfg = cfg
	lesson, err := a.Sched.Lesson(room.lessonID)
	require.NoError(t, err)
	before, err := a.Sched.GetCard(room.studentID, room.bookID, lesson.BatchWordIDs[0])
	require.NoError(t, err)
	_, err = a.ChangeConfig(context.Background(), config.Mutation{Operation: "set", Actor: "admin", Scope: config.ScopeGlobal, Key: "ladder", Value: []int{1, 2, 3}, Reason: "mixed"})
	require.ErrorContains(t, err, "atomic_configuration_unavailable")
	after, err := a.Sched.GetCard(room.studentID, room.bookID, lesson.BatchWordIDs[0])
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, config.DefaultLadder, a.Cfg.Ladder(room.studentID))
}

func TestLadderCrossAppPlacementFinishUsesCurrentGlobalConfiguration(t *testing.T) {
	a, sid, bid, _, _ := durableSettlementFixture(t)
	b := NewApp(a.Clock, a.DB)
	require.NoError(t, a.Cfg.SetGlobal("admin", "ladder", []int{3, 4, 5}, "cross app"))
	require.Len(t, b.Cfg.Ladder(sid), 8)
	require.NoError(t, b.PlacementSubmitContext(context.Background(), sid, bid, nil, true))
	lessons, err := b.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	scheduled := 0
	for _, lesson := range lessons {
		if lesson.Status == "scheduled" {
			scheduled++
			require.Equal(t, b.Clock.Now().Add(72*time.Hour), lesson.OpenAt)
		}
	}
	require.Equal(t, 1, scheduled)
}

package api

import (
	"context"
	"enwords/internal/store"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOverdueAtomicRetryAndRestart(t *testing.T) {
	a, sid, bid, lid, _ := durableSettlementFixture(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now().Add(-25 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	before, err := a.Sched.GetCard(sid, bid, l.BatchWordIDs[0])
	require.NoError(t, err)
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_overdue_notice BEFORE INSERT ON feedback_notices WHEN NEW.kind='overdue' BEGIN SELECT RAISE(FAIL,'fixture'); END`).Error)
	require.Error(t, a.RunOverdue(context.Background()))
	rows, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "scheduled", rows[0].Status)
	var task store.OverdueTask
	require.NoError(t, a.DB.First(&task).Error)
	require.Equal(t, "prepared", task.Status)
	frozen := task.PlanJSON
	var audits int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Where("trace_id=?", task.TaskID).Count(&audits).Error)
	require.Positive(t, audits, "failed attempts are retained as rolled-back audits")
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_overdue_notice`).Error)
	restarted := NewApp(a.Clock, a.DB)
	require.NoError(t, restarted.RunOverdue(context.Background()))
	require.NoError(t, restarted.RunOverdue(context.Background()))
	rows, err = restarted.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, a.DB.Where("task_id=?", task.TaskID).First(&task).Error)
	require.Equal(t, "completed", task.Status)
	require.Equal(t, frozen, task.PlanJSON)
	after, err := restarted.Sched.GetCard(sid, bid, l.BatchWordIDs[0])
	require.NoError(t, err)
	require.Equal(t, before.LadderStep, after.LadderStep)
	require.Equal(t, before.LastReviewAt, after.LastReviewAt)
	notices, err := restarted.NotificationsContext(context.Background(), sid)
	require.NoError(t, err)
	count := 0
	for _, n := range notices {
		if n.Type == "overdue" {
			count++
		}
	}
	require.Equal(t, 1, count)
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Where("trace_id=?", task.TaskID).Count(&audits).Error)
	var successes int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Where("trace_id=? AND ok=?", task.TaskID, true).Count(&successes).Error)
	require.EqualValues(t, 2, successes)
}

func TestOverdueProtectsContinuationAndRetiresExpiredRoom(t *testing.T) {
	a, _, lid, r := setupRoom(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now().Add(-25 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	require.NoError(t, a.RunOverdue(context.Background()))
	require.False(t, r.retired)
	l.OpenAt = a.Clock.Now().Add(-49 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	require.NoError(t, a.RunOverdue(context.Background()))
	require.True(t, r.retired)
	rows, err := a.Sched.Lessons(r.studentID, r.bookID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	_, _, err = a.Rooms.Enter(lid, r.studentID)
	require.Error(t, err, "expired lesson must not reopen")
}

type overdueErrorClosingClient struct {
	fakeClient
	closed bool
}

func (c *overdueErrorClosingClient) Close() error { c.closed = true; return nil }

func TestOverdueRetirementClosesClientWithRealCloseSignature(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	client := &overdueErrorClosingClient{}
	room.Join(client)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now().Add(-49 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	require.NoError(t, a.RunOverdue(context.Background()))
	require.True(t, room.retired)
	require.True(t, client.closed, "production wsClient implements Close() error")
}

func TestExpiredLessonEnterReturnsTerminalForbidden(t *testing.T) {
	a, _, lid, _ := setupRoom(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now().Add(-49 * time.Hour)
	require.NoError(t, a.Sched.SaveLesson(l))
	require.NoError(t, a.RunOverdue(context.Background()))
	response := req2(t, a.Router(), "POST", "/api/v1/classroom/lessons/"+itoa64(lid)+"/enter", mustStuToken(t, a), map[string]any{})
	require.Equal(t, 403, response.Code, "expired room must terminate reconnect rather than invite retries")
}

func TestRetiredRoomJoinCannotReactivateDelivery(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	a.Rooms.Drop(lid)
	require.True(t, room.retired)
	require.False(t, room.deliveryReady)
	turn := room.turnVersion
	client := &overdueErrorClosingClient{}
	room.Join(client)
	require.True(t, client.closed)
	require.Empty(t, client.frames)
	require.False(t, room.deliveryReady)
	require.Empty(t, room.clients)
	require.Nil(t, room.currentCli)
	require.Equal(t, turn, room.turnVersion)
}

func TestEnterRechecksRetiredRoomAndPreservesStorageError(t *testing.T) {
	t.Run("captured_retired_room", func(t *testing.T) {
		a, _, lid, room := setupRoom(t)
		// Fault injection models Enter retaining a room pointer while retirement wins.
		room.mu.Lock()
		room.retired = true
		room.mu.Unlock()
		got, snapshot, err := a.Rooms.Enter(lid, room.studentID)
		require.ErrorIs(t, err, errLessonUnavailable)
		require.Nil(t, got)
		require.Nil(t, snapshot)
		require.False(t, room.deliveryReady)
	})
	t.Run("storage_error_is_not_terminal_lesson", func(t *testing.T) {
		a, sid, _, lid, _ := durableSettlementFixture(t)
		db, err := a.DB.DB()
		require.NoError(t, err)
		require.NoError(t, db.Close())
		got, snapshot, err := a.Rooms.Enter(lid, sid)
		require.Error(t, err)
		require.False(t, errors.Is(err, errLessonUnavailable))
		require.Nil(t, got)
		require.Nil(t, snapshot)
	})
}

func TestOverdueConsecutiveCareAndPauseAreSuggestions(t *testing.T) {
	a, sid, bid, lid, _ := durableSettlementFixture(t)
	for count := 1; count <= 3; count++ {
		l, err := a.Sched.Lesson(lid)
		require.NoError(t, err)
		l.OpenAt = a.Clock.Now().Add(-25 * time.Hour)
		require.NoError(t, a.Sched.SaveLesson(l))
		require.NoError(t, a.RunOverdue(context.Background()))
		var task store.OverdueTask
		require.NoError(t, a.DB.Where("lesson_id=?", lid).First(&task).Error)
		require.Equal(t, "completed", task.Status)
		var notice store.FeedbackNotice
		require.NoError(t, a.DB.Where("task_id=?", task.TaskID).First(&notice).Error)
		if count >= 2 {
			require.Contains(t, notice.Body, "时间紧")
		}
		if count >= 3 {
			require.Contains(t, notice.Body, "在你确认前仍保留")
		}
		next, err := a.Sched.Lesson(task.NewLessonID)
		require.NoError(t, err)
		require.Equal(t, "scheduled", next.Status)
		lid = next.ID
		a.Clock.(*fakeClock).t = a.Clock.Now().Add(48 * time.Hour)
	}
	rows, err := a.Sched.Lessons(sid, bid)
	require.NoError(t, err)
	require.Len(t, rows, 4)
}

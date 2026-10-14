package api

import (
	"context"
	"enwords/internal/auth"
	"enwords/internal/store"
	"fmt"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestFeedbackMonthlyPostponeQuotaConcurrentFinalSlot(t *testing.T) {
	a, sid, _, lid, _ := durableSettlementFixture(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now()
	require.NoError(t, a.Sched.SaveLesson(l))
	for i := 0; i < 2; i++ {
		r, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: fmt.Sprintf("prior%d", i), Text: fmt.Sprintf("课程改到10月%d日", 13+i)})
		require.NoError(t, err)
		require.Equal(t, "applied", r.Status)
	}
	b := NewApp(a.Clock, a.DB)
	var wg sync.WaitGroup
	statuses := make(chan string, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for i, app := range []*App{a, b} {
		wg.Add(1)
		go func(i int, app *App) {
			defer wg.Done()
			<-start
			r, err := app.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: fmt.Sprintf("race%d", i), Text: "课程改到10月15日"})
			if err != nil {
				errors <- err
				return
			}
			statuses <- r.Status
		}(i, app)
	}
	close(start)
	wg.Wait()
	close(statuses)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	applied := 0
	for status := range statuses {
		if status == "applied" {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	var count int64
	require.NoError(t, a.DB.Model(&store.FeedbackIntent{}).Where("kind=? AND status=?", "D_postpone", "applied").Count(&count).Error)
	require.EqualValues(t, 3, count)
}

func TestFeedbackClarificationForeignParentLeavesNoChild(t *testing.T) {
	a, sid, _, _, _ := durableSettlementFixture(t)
	_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "private-parent", Text: "我不太理解"})
	require.NoError(t, err)
	_, err = a.Auth.CreateUser("other-parent-user", "pw", "other", auth.RoleStudent)
	require.NoError(t, err)
	require.NoError(t, store.NewAuthStore(a.DB).SaveUser(a.Auth, "other-parent-user"))
	_, other := loginGet(t, a, "other-parent-user", "pw")
	_, err = a.SubmitFeedback(context.Background(), other, FeedbackSubmission{SubmissionID: "foreign-child", ParentSubmissionID: "private-parent", Text: "每天一次"})
	require.Error(t, err)
	var count int64
	require.NoError(t, a.DB.Model(&store.FeedbackTask{}).Where("student_id=? AND submission_id=?", other, "foreign-child").Count(&count).Error)
	require.Zero(t, count)
	parent, err := a.FeedbackSubmissionByID(context.Background(), sid, "private-parent")
	require.NoError(t, err)
	require.False(t, parent.ClarificationAnswered)
}

func TestFeedbackQuotaRollbackDoesNotConsumeAndSQLiteBoostSequencePersists(t *testing.T) {
	a, sid, bid, lid, _ := durableSettlementFixture(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now()
	require.NoError(t, a.Sched.SaveLesson(l))
	for i := 0; i < 2; i++ {
		r, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: fmt.Sprintf("used%d", i), Text: fmt.Sprintf("课程改到10月%d日", 13+i)})
		require.NoError(t, err)
		require.Equal(t, "applied", r.Status)
	}
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER quota_rollback BEFORE INSERT ON feedback_notices BEGIN SELECT RAISE(ABORT,'quota fixture rollback'); END`).Error)
	r, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "third-retry", Text: "课程改到10月15日"})
	if err != nil {
		require.Contains(t, err.Error(), "storage_unavailable")
	} else {
		require.NotEqual(t, "applied", r.Status)
	}
	var count int64
	require.NoError(t, a.DB.Model(&store.FeedbackIntent{}).Where("kind=? AND status=?", "D_postpone", "applied").Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, a.DB.Exec(`DROP TRIGGER quota_rollback`).Error)
	r, err = a.RetryFeedbackSubmission(context.Background(), sid, "third-retry")
	require.NoError(t, err)
	require.Equal(t, "applied", r.Status)
	wid := l.BatchWordIDs[0]
	_, err = a.Sched.BoostWordWithCapacity(sid, bid, wid, 20)
	require.NoError(t, err)
	card, err := a.Sched.GetCard(sid, bid, wid)
	require.NoError(t, err)
	require.True(t, card.BoostFlag)
	require.Positive(t, card.BoostSequence)
	b := NewApp(a.Clock, a.DB)
	persisted, err := b.Sched.GetCard(sid, bid, wid)
	require.NoError(t, err)
	require.Equal(t, card.BoostSequence, persisted.BoostSequence)
	require.True(t, persisted.BoostFlag)
}

func TestFeedbackMonthlyPostponeQuotaUsesCommittedReceipts(t *testing.T) {
	a, sid, _, lid, _ := durableSettlementFixture(t)
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	l.OpenAt = a.Clock.Now()
	require.NoError(t, a.Sched.SaveLesson(l))
	for i := 1; i <= 4; i++ {
		input := FeedbackSubmission{SubmissionID: fmt.Sprintf("quota_%d", i), Text: fmt.Sprintf("课程改到10月%d日", 12+i)}
		result, err := a.SubmitFeedback(context.Background(), sid, input)
		require.NoError(t, err)
		if i <= 3 {
			require.Equal(t, "applied", result.Status)
		} else {
			require.Equal(t, "need_confirm", result.Status)
			require.Contains(t, result.Reply, "已达3次")
		}
		replay, err := a.SubmitFeedback(context.Background(), sid, input)
		require.NoError(t, err)
		require.Equal(t, result.Results, replay.Results)
	}
	l, err = a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, 15, l.OpenAt.Day())
	var count int64
	require.NoError(t, a.DB.Model(&store.FeedbackIntent{}).Where("kind=? AND status=?", "D_postpone", "applied").Count(&count).Error)
	require.EqualValues(t, 3, count)
}

func TestFeedbackClarificationLinkAndSecondUnknownEscalation(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			a, _, _, room := setupRoom(t)
			sid := room.studentID
			if persistent {
				a, sid, _, _, _ = durableSettlementFixture(t)
			}
			parent, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "parent", Text: "我有点疑惑"})
			require.NoError(t, err)
			require.Equal(t, "clarify", parent.Status)
			in := FeedbackSubmission{SubmissionID: "child", ParentSubmissionID: "parent", Text: "还是不太明白"}
			child, err := a.SubmitFeedback(context.Background(), sid, in)
			require.NoError(t, err)
			require.Equal(t, "escalated", child.Status)
			require.Equal(t, parent.TaskID, child.ConversationID)
			repeat, err := a.SubmitFeedback(context.Background(), sid, in)
			require.NoError(t, err)
			require.Equal(t, child.TaskID, repeat.TaskID)
			parent, err = a.FeedbackSubmissionByID(context.Background(), sid, "parent")
			require.NoError(t, err)
			require.True(t, parent.ClarificationAnswered)
			_, err = a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "other_child", ParentSubmissionID: "parent", Text: "每天一次"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "clarification_already_answered")
			_, err = a.FeedbackSubmissionByID(context.Background(), sid, "other_child")
			require.Error(t, err, "rejected parent claim cannot leave a ghost child")
			todos, err := a.FeedbackAdminTodosContext(context.Background())
			require.NoError(t, err)
			require.Len(t, todos, 1)
		})
	}
}

func TestFeedbackClarificationClearReplyAndParentConflict(t *testing.T) {
	a, sid, _, _, _ := durableSettlementFixture(t)
	for _, id := range []string{"p1", "p2"} {
		result, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: id, Text: "我不太理解"})
		require.NoError(t, err)
		require.Equal(t, "clarify", result.Status)
	}
	in := FeedbackSubmission{SubmissionID: "clear", ParentSubmissionID: "p1", Text: "每天一次"}
	r, err := a.SubmitFeedback(context.Background(), sid, in)
	require.NoError(t, err)
	require.Equal(t, "applied", r.Status)
	in.ParentSubmissionID = "p2"
	_, err = a.SubmitFeedback(context.Background(), sid, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "submission_conflict")
	_, err = a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "missing", ParentSubmissionID: "absent", Text: "每天一次"})
	require.Error(t, err)
	var ghosts int64
	require.NoError(t, a.DB.Model(&store.FeedbackTask{}).Where("submission_id=?", "missing").Count(&ghosts).Error)
	require.Zero(t, ghosts)
}

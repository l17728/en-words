package api

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/feedback"
	"enwords/internal/obs"
	"enwords/internal/store"
	"fmt"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestFeedbackPostponeRevalidatesConflictExpiryAndStartedLesson(t *testing.T) {
	for _, kind := range []string{"conflict", "expired", "started"} {
		t.Run(kind, func(t *testing.T) {
			a, sid, bid, lid, _ := durableSettlementFixture(t)
			a.persistUser("settlement-student")
			lesson, err := a.Sched.Lesson(lid)
			require.NoError(t, err)
			target := lesson.OpenAt.AddDate(0, 0, 1)
			a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentPostpone, Confidence: 1, Date: &target}))
			// Freeze a valid plan while the first attempt fails late. Retry must recheck current resources.
			require.NoError(t, a.DB.Exec(`CREATE TRIGGER postpone_pause BEFORE INSERT ON durable_memories BEGIN SELECT RAISE(ABORT,'fixture postpone pause'); END`).Error)
			record, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "postpone-" + kind, Text: fmt.Sprintf("课程改到%d月%d日", target.Month(), target.Day())})
			require.NoError(t, err)
			require.Equal(t, "failed", record.Status)
			require.True(t, record.Retryable)
			require.NoError(t, a.DB.Exec(`DROP TRIGGER postpone_pause`).Error)
			expectedCode := "stale_target"
			switch kind {
			case "conflict":
				second := seedBook(t, a, loginGet2(t, a), "Calendar conflict fixture", "g8", []string{"owl"})
				require.NoError(t, a.EnableBook(sid, second))
				require.NoError(t, a.PlacementSubmit(sid, second, nil, true))
				lessons, err := a.Sched.Lessons(sid, second)
				require.NoError(t, err)
				require.Len(t, lessons, 1)
				lessons[0].OpenAt = target
				require.NoError(t, a.Sched.SaveLesson(lessons[0]))
				expectedCode = "schedule_conflict"
			case "expired":
				a.Clock.(*fakeClock).Advance(target.Sub(a.Clock.Now()) + time.Minute)
				expectedCode = "invalid_date"
			case "started":
				lesson.Status = "active"
				require.NoError(t, a.Sched.SaveLesson(lesson))
			}
			record, err = a.RetryFeedbackSubmission(context.Background(), sid, record.SubmissionID)
			require.NoError(t, err)
			require.Equal(t, "failed", record.Status)
			require.Len(t, record.Results, 1)
			require.Equal(t, expectedCode, record.Results[0].ResultCode)
			require.False(t, record.Retryable)
			current, err := a.Sched.Lesson(lid)
			require.NoError(t, err)
			require.Equal(t, lesson.OpenAt, current.OpenAt)
			require.Equal(t, bid, current.BookID)
			require.Zero(t, submissionCount(t, a, "durable_memories", "student_id=?", sid))
			require.Zero(t, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "scheduler.postpone_lesson", true))
		})
	}
}

func TestFeedbackBoostSecondBookFailureRollsBackFirstBook(t *testing.T) {
	a, sid, bid, _, _ := durableSettlementFixture(t)
	a.persistUser("settlement-student")
	second := seedBook(t, a, loginGet2(t, a), "Second cat fixture", "g8", []string{"cat"})
	require.NoError(t, a.EnableBook(sid, second))
	require.NoError(t, a.PlacementSubmit(sid, second, nil, true))
	a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentHardWord, Word: "cat", Confidence: 1}))
	require.NoError(t, a.DB.Exec(fmt.Sprintf(`CREATE TRIGGER fail_second_boost BEFORE UPDATE ON memory_cards WHEN NEW.book_id=%d BEGIN SELECT RAISE(ABORT,'fixture second resource'); END`, second)).Error)
	record, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "two-books", Text: "cat 总是忘"})
	require.NoError(t, err)
	require.Equal(t, "failed", record.Status)
	for _, id := range []int64{bid, second} {
		word, err := a.Books.EntryByKey(id, "cat")
		require.NoError(t, err)
		card, err := a.Sched.GetCard(sid, id, word.ID)
		require.NoError(t, err)
		require.False(t, card.BoostFlag)
	}
	require.Zero(t, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "scheduler.boost_word", true))
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_second_boost`).Error)
	record, err = a.RetryFeedbackSubmission(context.Background(), sid, "two-books")
	require.NoError(t, err)
	require.Equal(t, "applied", record.Status)
	for _, id := range []int64{bid, second} {
		word, err := a.Books.EntryByKey(id, "cat")
		require.NoError(t, err)
		card, err := a.Sched.GetCard(sid, id, word.ID)
		require.NoError(t, err)
		require.True(t, card.BoostFlag)
	}
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "scheduler.boost_word", true))
}

func TestFeedbackPartialComplaintCreatesRealAdminTodoAndSafeTrace(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentReduce, Confidence: 1}, feedback.Intent{Kind: feedback.IntentComplaint, Confidence: 1}))
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_partial_config BEFORE INSERT ON config_overrides WHEN NEW.key='wordsPerLesson' BEGIN SELECT RAISE(ABORT,'sensitive fixture SQL'); END`).Error)
	var sink bytes.Buffer
	a.SetLogger(obs.NewWithSink(&sink))
	ctx := obs.WithTrace(context.Background(), "feedback-matrix-trace")
	text := "private complaint fixture C++ content"
	r, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "partial-complaint", Text: text})
	require.NoError(t, err)
	require.Equal(t, "partial", r.Status)
	require.Len(t, r.Results, 2)
	require.Equal(t, "failed", r.Results[0].Status)
	require.Equal(t, "escalated", r.Results[1].Status)
	require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	todos, err := a.FeedbackAdminTodosContext(ctx)
	require.NoError(t, err)
	require.Len(t, todos, 1)
	require.Equal(t, text, todos[0].Text)
	require.Equal(t, r.TaskID, todos[0].TaskID)
	require.EqualValues(t, 1, submissionCount(t, a, "feedback_notices", "kind=?", "admin_feedback"))
	require.EqualValues(t, 1, submissionCount(t, a, "feedback_todos", "task_id=?", r.TaskID))
	found := 0
	for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
		var log map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &log))
		if log["module"] != "feedback" {
			continue
		}
		found++
		require.Equal(t, "feedback-matrix-trace", log["trace_id"])
		require.Equal(t, r.TaskID, log["task_id"])
		for _, key := range []string{"attempt_id", "student_id", "intent_index", "result_code", "transaction_outcome", "duration_ms"} {
			require.Contains(t, log, key)
		}
	}
	require.Greater(t, found, 0)
	require.NotContains(t, sink.String(), text)
	require.NotContains(t, sink.String(), "sensitive fixture SQL")
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_partial_config`).Error)
	r, err = a.RetryFeedbackSubmission(ctx, sid, r.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.EqualValues(t, 1, submissionCount(t, a, "feedback_notices", "kind=?", "admin_feedback"))
	require.EqualValues(t, 1, submissionCount(t, a, "feedback_todos", "task_id=?", r.TaskID))
}

func TestFeedbackLegacyMigrationTwiceNeverReexecutesOrNotifies(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	old := []FeedbackRecord{{ID: 41, StudentID: sid, Text: "old complaint", Status: "escalated", Reply: "historical reply", CreatedAt: a.Clock.Now()}, {ID: 42, StudentID: sid, Text: "old applied", Status: "applied", CreatedAt: a.Clock.Now()}}
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	require.NoError(t, a.DB.Create(&store.RuntimeRecord{Key: "feedbacks", Value: string(raw)}).Error)
	for i := 0; i < 2; i++ {
		restored := NewApp(a.Clock, a.DB)
		records, err := restored.FeedbacksContext(context.Background(), sid)
		require.NoError(t, err)
		require.Len(t, records, 2)
		for _, r := range records {
			require.Equal(t, "legacy", r.Idempotency)
			require.False(t, r.Retryable)
		}
		todos, err := restored.FeedbackAdminTodosContext(context.Background())
		require.NoError(t, err)
		require.Len(t, todos, 1)
		require.True(t, todos[0].Legacy)
		require.EqualValues(t, 2, submissionCount(t, a, "feedback_tasks", "student_id=?", sid))
		require.EqualValues(t, 1, submissionCount(t, a, "feedback_todos", "student_id=?", sid))
		require.Zero(t, submissionCount(t, a, "feedback_notices", "recipient=?", sid))
		require.Zero(t, submissionCount(t, a, "audit_logs", "tool LIKE ?", "feedback.%"))
		require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	}
}

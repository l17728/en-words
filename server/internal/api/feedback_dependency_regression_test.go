package api

import (
	"bytes"
	"context"
	"encoding/json"
	"enwords/internal/agent"
	"enwords/internal/feedback"
	"enwords/internal/obs"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFeedbackEscalationLogOnlyAfterCommit(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	var sink bytes.Buffer
	a.SetLogger(obs.NewWithSink(&sink))
	ctx := obs.WithTrace(context.Background(), "escalation-commit-trace")
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER reject_escalation_receipt BEFORE INSERT ON feedback_notices WHEN NEW.kind='admin_feedback' BEGIN SELECT RAISE(ABORT,'private fault'); END`).Error)
	r, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "escalation-log", Text: "投诉老师讲课太快私密正文"})
	require.NoError(t, err)
	require.Equal(t, "failed", r.Status)
	require.True(t, r.Retryable)
	require.NotContains(t, sink.String(), "feedback_admin_escalated")
	require.Zero(t, submissionCount(t, a, "feedback_todos", "task_id=?", r.TaskID))
	require.NoError(t, a.DB.Exec(`DROP TRIGGER reject_escalation_receipt`).Error)
	r, err = a.RetryFeedbackSubmission(ctx, sid, r.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, "escalated", r.Status)
	_, err = a.RetryFeedbackSubmission(ctx, sid, r.SubmissionID)
	require.NoError(t, err)
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["event"] != "feedback_admin_escalated" {
			continue
		}
		count++
		require.Equal(t, "escalation-commit-trace", entry["trace_id"])
		require.Equal(t, r.TaskID, entry["task_id"])
		require.Equal(t, "committed", entry["transaction_outcome"])
		require.Equal(t, "escalated", entry["result_code"])
		require.EqualValues(t, 0, entry["intent_index"])
	}
	require.Equal(t, 1, count)
	require.NotContains(t, sink.String(), "私密正文")
	require.NotContains(t, sink.String(), "private fault")
}

func TestFeedbackDependencyRetryabilityFollowsPredecessor(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		name := "temporary_storage_failure"
		if terminal {
			name = "terminal_stale_target"
		}
		t.Run(name, func(t *testing.T) {
			a, _, sid := feedbackTwoApps(t)
			ctx, cancel := context.WithCancel(context.Background())
			a.FB.SetLLM(submissionParser(func(context.Context, string, time.Time) ([]feedback.Intent, error) {
				cancel()
				return nil, context.Canceled
			}))
			_, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "dependency-state", Text: "分两步调整"})
			require.ErrorIs(t, err, context.Canceled)
			task, _, err := a.feedbackRead(context.Background(), sid, "dependency-state")
			require.NoError(t, err)
			// Explicit persisted-plan fixture: equivalent user intents are normally deduplicated.
			parsed := feedback.ParseResult{Intents: []feedback.Intent{{Kind: feedback.IntentReduce}, {Kind: feedback.IntentReduce}}}
			acts := []agent.SystemAction{
				{Kind: "adjust_words", IntentIndex: 0, Params: map[string]any{"target": 0, "dir": "A_reduce"}},
				{Kind: "adjust_words", IntentIndex: 1, Params: map[string]any{"target": 0, "dir": "A_reduce"}},
			}
			plan, err := compileFeedbackPlan(parsed, acts)
			require.NoError(t, err)
			require.NoError(t, a.withFeedbackWork(context.Background(), task.TaskID, func(w *feedbackWork) error { return w.freezeFeedbackPlan(plan) }))
			if terminal {
				require.NoError(t, a.Cfg.SetStudent("admin", sid, "wordsPerLesson", 80, "independent change"))
			} else {
				require.NoError(t, a.DB.Exec(`CREATE TRIGGER reject_dependency_config BEFORE INSERT ON config_overrides WHEN NEW.key='wordsPerLesson' BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END`).Error)
			}
			r, err := a.RetryFeedbackSubmission(context.Background(), sid, task.SubmissionID)
			require.NoError(t, err)
			require.Len(t, r.Results, 2)
			require.Equal(t, "failed", r.Status)
			require.Equal(t, "dependency_failed", r.Results[1].ResultCode)
			require.Equal(t, !terminal, r.Results[1].Retryable)
			require.Equal(t, !terminal, r.Retryable)
			_, rows, err := a.feedbackRead(context.Background(), sid, task.SubmissionID)
			require.NoError(t, err)
			if terminal {
				require.Equal(t, "stale_target", r.Results[0].ResultCode)
				require.NotNil(t, rows[1].CompletedAt)
				again, err := a.RetryFeedbackSubmission(context.Background(), sid, task.SubmissionID)
				require.NoError(t, err)
				require.Equal(t, r.Version, again.Version)
				require.Equal(t, 80, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
			} else {
				require.Equal(t, "storage_unavailable", r.Results[0].ResultCode)
				require.Nil(t, rows[1].CompletedAt)
				require.NoError(t, a.DB.Exec(`DROP TRIGGER reject_dependency_config`).Error)
				again, err := a.RetryFeedbackSubmission(context.Background(), sid, task.SubmissionID)
				require.NoError(t, err)
				require.Equal(t, "applied", again.Status)
				require.Equal(t, 50, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
			}
		})
	}
}

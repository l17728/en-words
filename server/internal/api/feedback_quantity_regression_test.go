package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/feedback"
	"github.com/stretchr/testify/require"
)

func TestFeedbackQuantityGuardPersistsClarificationWithoutEffects(t *testing.T) {
	for _, text := range []string{"每日安排0节课", "每课0个词", "每课50或60个词"} {
		t.Run(text, func(t *testing.T) {
			a, b, sid := feedbackTwoApps(t)
			bid := seedBook(t, a, loginGet2(t, a), "Quantity persistence fixture", "g7", []string{"cat"})
			require.NoError(t, a.EnableBook(sid, bid))
			require.NoError(t, a.PlacementSubmit(sid, bid, nil, true))
			lessonsBefore, err := a.Sched.Lessons(sid, bid)
			require.NoError(t, err)
			word, err := a.Books.EntryByKey(bid, "cat")
			require.NoError(t, err)
			cardBefore, err := a.Sched.GetCard(sid, bid, word.ID)
			require.NoError(t, err)
			other, err := a.Auth.CreateUser("other-quantity", "fixturepw", "Other", auth.RoleStudent)
			require.NoError(t, err)
			a.persistUser(other.Username)
			calls := 0
			a.FB.SetLLM(submissionParser(func(context.Context, string, time.Time) ([]feedback.Intent, error) {
				calls++
				return []feedback.Intent{{Kind: feedback.IntentIncrease, Confidence: 1}}, nil
			}))
			input := FeedbackSubmission{SubmissionID: "quantity-invalid", Text: text}
			record, err := a.SubmitFeedback(context.Background(), sid, input)
			require.NoError(t, err)
			require.Equal(t, "need_confirm", record.Status)
			require.False(t, record.Retryable)
			replay, err := b.SubmitFeedback(context.Background(), sid, input)
			require.NoError(t, err)
			require.Equal(t, record.TaskID, replay.TaskID)
			require.Equal(t, "need_confirm", replay.Status)
			require.Equal(t, 1, calls)
			lessonsAfter, err := b.Sched.Lessons(sid, bid)
			require.NoError(t, err)
			require.Equal(t, lessonsBefore, lessonsAfter)
			cardAfter, err := b.Sched.GetCard(sid, bid, word.ID)
			require.NoError(t, err)
			require.Equal(t, cardBefore, cardAfter)
			_, err = b.FeedbackSubmissionByID(context.Background(), other.ID, input.SubmissionID)
			require.Error(t, err)
			require.Equal(t, 70, submissionStoredConfig(t, b, sid).Int(sid, "wordsPerLesson"))
			require.Zero(t, submissionCount(t, b, "durable_memories", "student_id=?", sid))
			require.Zero(t, submissionCount(t, b, "audit_logs", "tool IN ? AND ok=?", []string{"config.adjust_words", "config.set_lessons_per_day", "scheduler.postpone_lesson", "scheduler.boost_word"}, true))
			require.Zero(t, submissionCount(t, b, "feedback_todos", "student_id=?", sid))
			// A new valid task must remain actionable after the clarification.
			a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentReduce, Confidence: 1, TargetWords: 50}))
			valid, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: fmt.Sprintf("valid-%d", sid), Text: "每课五十个词"})
			require.NoError(t, err)
			require.Equal(t, "applied", valid.Status)
			require.Equal(t, 50, submissionStoredConfig(t, b, sid).Int(sid, "wordsPerLesson"))
		})
	}
}

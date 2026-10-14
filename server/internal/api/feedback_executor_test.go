package api

import (
	"context"
	"encoding/json"
	"enwords/internal/agent"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/feedback"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/wordbook"
	"errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func TestFeedbackMissingFrozenResourcesAreTerminal(t *testing.T) {
	for _, err := range []error{gorm.ErrRecordNotFound, scheduler.ErrCardNotFound, scheduler.ErrLessonNotFound, wordbook.ErrBookNotFound, wordbook.ErrEntryNotFound} {
		require.Equal(t, "resource_unavailable", feedbackErrorCode(err))
		require.False(t, feedbackRetryable(err))
	}
	require.True(t, feedbackRetryable(errors.New("temporary storage failure")))
}

func TestFeedbackPreflightFailureHasActionableReply(t *testing.T) {
	for _, code := range []string{"plan_budget_exceeded", "invalid_plan", "unknown_action", "atomic_execution_unavailable"} {
		r := feedbackRecord(store.FeedbackTask{ErrorCode: code, Retryable: false}, nil)
		require.Equal(t, "failed", r.Status)
		require.False(t, r.Retryable)
		require.NotEmpty(t, r.Reply)
		require.NotContains(t, r.Reply, code)
		require.NotContains(t, r.Reply, "恢复未完成项")
		if code == "plan_budget_exceeded" {
			require.Equal(t, "一次反馈的调整事项太多，请拆成几次提交。", r.Reply)
		}
	}
	r := feedbackRecord(store.FeedbackTask{ErrorCode: "canceled", Retryable: true}, nil)
	require.True(t, r.Retryable)
	require.Contains(t, r.Reply, "已完成的调整会保留")
	require.Contains(t, r.Reply, "恢复未完成项")
	require.NotContains(t, feedbackFailureReply("unknown_terminal_failure", false), "恢复")
	require.Contains(t, feedbackFailureReply("storage_unavailable", true), "恢复")
}

func TestFeedbackMemoryConcurrentSettingsAndCompletionPreserveAllWrites(t *testing.T) {
	a, _, lid, room := setupRoom(t)
	sid, bid := room.studentID, room.bookID
	a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentFrequency, Confidence: 1, LessonsPerDay: 2}))
	lesson, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	correct := map[int64]bool{}
	for _, wid := range lesson.BatchWordIDs {
		correct[wid] = false
	}
	res := &classroom.Result{TotalWords: len(correct), Correct: correct, Report: classroom.LessonReport{TotalWords: len(correct), FirstTryRate: 0}}
	start := make(chan struct{})
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		r, e := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "memory-concurrent", Text: "每天两次"})
		if e == nil && r.Status != "applied" {
			e = errors.New(r.Status)
		}
		errs <- e
	}()
	go func() {
		defer wg.Done()
		<-start
		errs <- a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true})
	}()
	go func() {
		defer wg.Done()
		<-start
		errs <- a.Cfg.SetPlayback("admin", sid, false, config.PlaybackSettings{TtsEnRepeat: 2, TtsZhRepeat: 2, TtsIntervalMs: 300, TtsZhIntervalMs: 400, TtsSwitchIntervalMs: 500}, "fixture")
	}()
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.Equal(t, 2, a.Cfg.Int(sid, "lessonsPerDay"))
	require.Equal(t, 50, a.Cfg.Int(sid, "wordsPerLesson"))
	require.Equal(t, 2, a.Cfg.Playback(sid).TtsEnRepeat)
	got, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, "finished", got.Status)
	history, err := a.FeedbacksContext(context.Background(), sid)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Len(t, a.Notifications(sid), 2)
}

func TestFeedbackRefreshAndRefreshReceiptFailureStillReportsCommit(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	p := &refreshFailurePersister{Persister: store.NewConfigPersister(a.DB)}
	cfg, err := config.NewStore(p, nil)
	require.NoError(t, err)
	a.Cfg = cfg
	p.fail = true
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER reject_refresh_receipt BEFORE UPDATE ON feedback_intents WHEN NEW.result_json LIKE '%"refresh_pending":true%' BEGIN SELECT RAISE(ABORT,'fixture refresh receipt'); END`).Error)
	r, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "double-refresh-failure", Text: "太多了"})
	require.NoError(t, err)
	require.Equal(t, "applied_refresh_pending", r.Status)
	require.True(t, r.RefreshPending)
	require.True(t, r.Results[0].RefreshPending)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.Equal(t, 70, a.Cfg.Int(sid, "wordsPerLesson"))
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
	p.fail = false
	r, err = a.FeedbackSubmissionByID(context.Background(), sid, r.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, "applied", r.Status)
	require.False(t, r.RefreshPending)
	require.Equal(t, 60, a.Cfg.Int(sid, "wordsPerLesson"))
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
}

func TestFeedbackPlanRejectsUnknownMalformedAndOverBudget(t *testing.T) {
	parsed := feedback.ParseResult{Intents: []feedback.Intent{{Kind: feedback.IntentReduce, Confidence: 1}}}
	for _, act := range []agent.SystemAction{{Kind: "unregistered"}, {Kind: "adjust_words", Params: map[string]any{"target": "60"}}, {Kind: "adjust_words", Params: map[string]any{"target": 60, "unknown": true}}, {Kind: "memory_append", Params: map[string]any{"content": "without parent"}}, {Kind: "reply_clarify", IntentIndex: 2}} {
		_, err := compileFeedbackPlan(parsed, []agent.SystemAction{act})
		require.Error(t, err)
	}
	parsed.Intents = nil
	var acts []agent.SystemAction
	for i := 0; i < 9; i++ {
		parsed.Intents = append(parsed.Intents, feedback.Intent{Kind: feedback.IntentOther})
		acts = append(acts, agent.SystemAction{Kind: "reply_clarify", IntentIndex: i})
	}
	_, err := compileFeedbackPlan(parsed, acts)
	require.ErrorContains(t, err, "plan_budget_exceeded")
}

func TestFeedbackFrozenSameResourceDependencyUsesVirtualRevision(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	ctx, cancel := context.WithCancel(context.Background())
	a.FB.SetLLM(submissionParser(func(context.Context, string, time.Time) ([]feedback.Intent, error) {
		cancel()
		return nil, context.Canceled
	}))
	_, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "dependencies", Text: "分两步调整"})
	require.ErrorIs(t, err, context.Canceled)
	task, _, err := a.feedbackRead(context.Background(), sid, "dependencies")
	require.NoError(t, err)
	parsed := feedback.ParseResult{Intents: []feedback.Intent{{Kind: feedback.IntentReduce}, {Kind: feedback.IntentReduce}}}
	acts := []agent.SystemAction{{Kind: "adjust_words", IntentIndex: 0, Params: map[string]any{"target": 0, "dir": "A_reduce"}}, {Kind: "adjust_words", IntentIndex: 1, Params: map[string]any{"target": 0, "dir": "A_reduce"}}}
	plan, err := compileFeedbackPlan(parsed, acts)
	require.NoError(t, err)
	require.NoError(t, a.withFeedbackWork(context.Background(), task.TaskID, func(w *feedbackWork) error { return w.freezeFeedbackPlan(plan) }))
	r, err := a.RetryFeedbackSubmission(context.Background(), sid, "dependencies")
	require.NoError(t, err)
	require.Equal(t, "applied", r.Status)
	require.Len(t, r.Results, 2)
	require.Equal(t, 50, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.EqualValues(t, 2, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
}

func TestFeedbackCorruptFrozenPlanFailsWithoutBusinessWrites(t *testing.T) {
	for _, corruption := range []string{"negative_dependency", "forward_dependency", "oversized_dependency", "wrong_index", "unknown_schema"} {
		t.Run(corruption, func(t *testing.T) {
			a, _, sid := feedbackTwoApps(t)
			ctx, cancel := context.WithCancel(context.Background())
			a.FB.SetLLM(submissionParser(func(context.Context, string, time.Time) ([]feedback.Intent, error) {
				cancel()
				return nil, context.Canceled
			}))
			_, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "corrupt-plan", Text: "分两步调整"})
			require.ErrorIs(t, err, context.Canceled)
			task, _, err := a.feedbackRead(context.Background(), sid, "corrupt-plan")
			require.NoError(t, err)
			parsed := feedback.ParseResult{Intents: []feedback.Intent{{Kind: feedback.IntentReduce}, {Kind: feedback.IntentReduce}}}
			acts := []agent.SystemAction{{Kind: "adjust_words", IntentIndex: 0, Params: map[string]any{"target": 0, "dir": "A_reduce"}}, {Kind: "adjust_words", IntentIndex: 1, Params: map[string]any{"target": 0, "dir": "A_reduce"}}}
			plan, err := compileFeedbackPlan(parsed, acts)
			require.NoError(t, err)
			require.NoError(t, a.withFeedbackWork(context.Background(), task.TaskID, func(w *feedbackWork) error {
				if err := w.freezeFeedbackPlan(plan); err != nil {
					return err
				}
				f, err := decodeFrozen(w.intents[1])
				if err != nil {
					return err
				}
				switch corruption {
				case "negative_dependency":
					f.DependsOn = -2
				case "forward_dependency":
					f.DependsOn = 1
				case "oversized_dependency":
					f.DependsOn = 999
				case "wrong_index":
					f.Index = 0
				case "unknown_schema":
					w.task.SchemaVersion = 99
				}
				b, err := json.Marshal(f)
				w.intents[1].FrozenJSON = string(b)
				return err
			}))
			r, err := a.RetryFeedbackSubmission(context.Background(), sid, task.SubmissionID)
			require.NoError(t, err)
			require.Equal(t, "failed", r.Status)
			require.False(t, r.Retryable)
			for _, result := range r.Results {
				require.Equal(t, "invalid_plan", result.ResultCode)
			}
			require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
			require.Zero(t, submissionCount(t, a, "audit_logs", "tool=?", "config.set_student"))
			again, err := a.RetryFeedbackSubmission(context.Background(), sid, task.SubmissionID)
			require.NoError(t, err)
			require.Equal(t, r.Version, again.Version)
		})
	}
}

type cancelOnFeedbackReload struct {
	config.Persister
	cancel context.CancelFunc
	armed  bool
}

func (p *cancelOnFeedbackReload) LoadGlobals() (map[string]json.RawMessage, error) {
	if p.armed {
		p.cancel()
	}
	return p.Persister.LoadGlobals()
}
func TestFeedbackCancellationAfterCommitDoesNotReexecute(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &cancelOnFeedbackReload{Persister: store.NewConfigPersister(a.DB), cancel: cancel}
	cfg, err := config.NewStore(p, nil)
	require.NoError(t, err)
	a.Cfg = cfg
	p.armed = true
	r, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "cancel-after-commit", Text: "太多了"})
	require.NoError(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, "applied", r.Status)
	r, err = a.RetryFeedbackSubmission(context.Background(), sid, r.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, "applied", r.Status)
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
}

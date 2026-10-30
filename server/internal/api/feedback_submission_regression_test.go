package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/config"
	"enwords/internal/feedback"
	"enwords/internal/obs"
	"enwords/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFeedbackIntentUncertainCommitDoesNotMarkFailedOrContinue(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	a.FB.SetLLM(fixedSubmissionIntents(
		feedback.Intent{Kind: feedback.IntentReduce, TargetWords: 50, Confidence: 1},
		feedback.Intent{Kind: feedback.IntentFrequency, Confidence: 1, LessonsPerDay: 2},
	))
	writes := 0
	a.atomicWriteObserved = func(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(store.AtomicConnectionTiming)) error {
		writes++
		if writes != 3 { // task + frozen plan commit normally; lose the first intent receipt
			return store.WithAtomicWriteObserved(ctx, db, fn, observe)
		}
		if err := store.WithAtomicWrite(ctx, db, fn); err != nil {
			return err
		}
		observe(store.AtomicConnectionTiming{Outcome: "outcome_uncertain"})
		return errors.New("fixture: commit receipt lost")
	}

	_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "unknown-intent-commit", Text: "apply two changes", Channel: "page"})
	var submissionErr *FeedbackSubmissionError
	require.ErrorAs(t, err, &submissionErr)
	require.Equal(t, "outcome_uncertain", submissionErr.ErrorCode)
	require.False(t, submissionErr.Retryable)
	require.Equal(t, 3, writes, "must not run recovery or a later intent")
	_, intents, err := a.feedbackRead(context.Background(), sid, "unknown-intent-commit")
	require.NoError(t, err)
	require.Equal(t, 50, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"), "the committed effect remains visible")
	require.NotEqual(t, 2, submissionStoredConfig(t, a, sid).Int(sid, "lessonsPerDay"), "the next intent must not execute")
	require.Equal(t, "applied", intents[0].Status)
	require.Equal(t, "pending", intents[1].Status)
	require.Zero(t, submissionCount(t, a, "audit_logs", "result LIKE 'rolled_back_%'"))
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
}

func TestFeedbackPlanFreezeUncertainCommitDoesNotExecuteOrMarkIntentFailed(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentReduce, TargetWords: 50, Confidence: 1}))
	writes := 0
	a.atomicWriteObserved = func(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(store.AtomicConnectionTiming)) error {
		writes++
		if writes != 2 { // first write prepares the task; second freezes the plan
			return store.WithAtomicWriteObserved(ctx, db, fn, observe)
		}
		if err := store.WithAtomicWrite(ctx, db, fn); err != nil {
			return err
		}
		observe(store.AtomicConnectionTiming{Outcome: "outcome_uncertain"})
		return errors.New("fixture: frozen-plan receipt lost")
	}
	_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "unknown-plan-commit", Text: "reduce lesson size", Channel: "page"})
	var submissionErr *FeedbackSubmissionError
	require.ErrorAs(t, err, &submissionErr)
	require.Equal(t, "outcome_uncertain", submissionErr.ErrorCode)
	require.False(t, submissionErr.Retryable)
	require.Equal(t, 2, writes)
	task, intents, err := a.feedbackRead(context.Background(), sid, "unknown-plan-commit")
	require.NoError(t, err)
	require.NotEmpty(t, task.PlanJSON, "the plan may have committed despite losing its receipt")
	require.Equal(t, "pending", intents[0].Status, "no teaching intent is executed before the plan commit is confirmed")
	require.Zero(t, submissionCount(t, a, "audit_logs", "tool=?", "config.set_student"))
}

func TestFeedbackRecoveryUncertainCommitStopsWithoutRetry(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	a.FB.SetLLM(fixedSubmissionIntents(
		feedback.Intent{Kind: feedback.IntentReduce, TargetWords: 50, Confidence: 1},
		feedback.Intent{Kind: feedback.IntentFrequency, LessonsPerDay: 2, Confidence: 1},
	))
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER reject_feedback_effect BEFORE INSERT ON config_overrides WHEN NEW.key='wordsPerLesson' BEGIN SELECT RAISE(ABORT,'fixture effect failure'); END`).Error)
	writes := 0
	a.atomicWriteObserved = func(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error, observe func(store.AtomicConnectionTiming)) error {
		writes++
		if writes != 4 { // task, plan, failed intent, then recovery record
			return store.WithAtomicWriteObserved(ctx, db, fn, observe)
		}
		if err := store.WithAtomicWrite(ctx, db, fn); err != nil {
			return err
		}
		observe(store.AtomicConnectionTiming{Outcome: "outcome_uncertain"})
		return errors.New("fixture: recovery receipt lost")
	}
	_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "unknown-recovery-commit", Text: "reduce and schedule twice", Channel: "page"})
	var submissionErr *FeedbackSubmissionError
	require.ErrorAs(t, err, &submissionErr)
	require.Equal(t, "outcome_uncertain", submissionErr.ErrorCode)
	require.False(t, submissionErr.Retryable)
	require.Equal(t, 4, writes, "must not continue to the second intent after uncertain recovery")
	_, intents, err := a.feedbackRead(context.Background(), sid, "unknown-recovery-commit")
	require.NoError(t, err)
	require.Equal(t, "pending", intents[1].Status)
}

type submissionParser func(context.Context, string, time.Time) ([]feedback.Intent, error)

func (f submissionParser) ParseIntents(ctx context.Context, text string, at time.Time) ([]feedback.Intent, error) {
	return f(ctx, text, at)
}
func fixedSubmissionIntents(intents ...feedback.Intent) submissionParser {
	return func(ctx context.Context, _ string, _ time.Time) ([]feedback.Intent, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return append([]feedback.Intent(nil), intents...), nil
	}
}

// Separate SQL handles and Apps ensure uniqueness is not proved only by one process mutex.
func feedbackTwoApps(t *testing.T) (*App, *App, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feedback.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	handle, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = handle.Close() })
	clock := &fakeClock{t: time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)}
	a := NewApp(clock, db)
	require.Equal(t, 0, newAppBootstrap(t, a))
	u, err := a.Auth.CreateUser("feedback-student", "fixturepw", "Student", auth.RoleStudent)
	require.NoError(t, err)
	a.persistUser(u.Username)
	require.NoError(t, a.Cfg.SetStudent("admin", u.ID, "wordsPerLesson", 70, "initial fixture"))
	otherDB, err := store.Open(path)
	require.NoError(t, err)
	otherHandle, err := otherDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = otherHandle.Close() })
	b := NewApp(clock, otherDB)
	parser := fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentReduce, Confidence: 1})
	a.FB.SetLLM(parser)
	b.FB.SetLLM(parser)
	return a, b, u.ID
}

func submissionResultMap(t *testing.T, record *FeedbackRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}
func submissionStoredConfig(t *testing.T, a *App, sid int64) *config.Store {
	t.Helper()
	cfg, err := config.NewStore(store.NewConfigPersister(a.DB), nil)
	require.NoError(t, err)
	return cfg
}
func submissionCount(t *testing.T, a *App, table string, where string, args ...any) int64 {
	t.Helper()
	var n int64
	query := a.DB.Table(table)
	if where != "" {
		query = query.Where(where, args...)
	}
	require.NoError(t, query.Count(&n).Error)
	return n
}

func TestFeedbackSubmissionTwoAppsConcurrentReplayAndConflict(t *testing.T) {
	a, b, sid := feedbackTwoApps(t)
	input := FeedbackSubmission{SubmissionID: "same-submission", Text: "每次词太多了", Channel: "page"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		app := a
		if i%2 == 1 {
			app = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := app.SubmitFeedback(context.Background(), sid, input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	record, err := b.FeedbackSubmissionByID(context.Background(), sid, input.SubmissionID)
	require.NoError(t, err)
	require.Equal(t, "applied", record.Status)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.EqualValues(t, 1, submissionCount(t, a, "feedback_tasks", "student_id=? AND submission_id=?", sid, input.SubmissionID))
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
	again, err := a.SubmitFeedback(context.Background(), sid, input)
	require.NoError(t, err)
	require.Equal(t, record.ID, again.ID)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	input.Text = "每次多来一点"
	_, err = a.SubmitFeedback(context.Background(), sid, input)
	require.Error(t, err)
	var conflict *FeedbackSubmissionError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, 409, conflict.Code)
	require.Equal(t, "submission_conflict", conflict.ErrorCode)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
}

func TestFeedbackSubmissionFailedFrozenTargetRejectsABA(t *testing.T) {
	a, b, sid := feedbackTwoApps(t)
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_c_config BEFORE INSERT ON config_overrides WHEN NEW.key='wordsPerLesson' BEGIN SELECT RAISE(ABORT,'fixture mutation unavailable'); END`).Error)
	record, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "aba-task", Text: "每次词太多了", Channel: "page"})
	require.NoError(t, err)
	require.NotEqual(t, "applied", record.Status)
	require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_c_config`).Error)
	// Value returns to the frozen base; only a persistent revision detects this edit.
	require.NoError(t, b.Cfg.SetStudent("admin", sid, "wordsPerLesson", 80, "intervening edit"))
	require.NoError(t, b.Cfg.SetStudent("admin", sid, "wordsPerLesson", 70, "intervening edit back"))
	record, err = a.RetryFeedbackSubmission(context.Background(), sid, "aba-task")
	require.NoError(t, err)
	require.Equal(t, "failed", record.Status)
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	require.Contains(t, string(raw), "stale_target")
	require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.Zero(t, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
}

func TestFeedbackSubmissionPartialRollbackAndRetryFrozenDate(t *testing.T) {
	a, sid, bid, lid, _ := durableSettlementFixture(t)
	a.persistUser("settlement-student")
	lesson, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	date := lesson.OpenAt.AddDate(0, 0, 1)
	a.FB.SetLLM(fixedSubmissionIntents(feedback.Intent{Kind: feedback.IntentFrequency, Confidence: 1, LessonsPerDay: 2}, feedback.Intent{Kind: feedback.IntentPostpone, Confidence: 1, Date: &date}))
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_c_memory BEFORE INSERT ON durable_memories BEGIN SELECT RAISE(ABORT,'fixture memory failure'); END`).Error)
	record, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "partial-task", Text: fmt.Sprintf("每天两节课，课程改到%d月%d日", date.Month(), date.Day()), Channel: "page"})
	require.NoError(t, err)
	require.Equal(t, "partial", record.Status)
	require.Equal(t, 2, submissionStoredConfig(t, a, sid).Int(sid, "lessonsPerDay"))
	current, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, lesson.OpenAt, current.OpenAt)
	require.Zero(t, submissionCount(t, a, "durable_memories", "student_id=?", sid))
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_c_memory`).Error)
	a.FB.SetLLM(submissionParser(func(ctx context.Context, text string, at time.Time) ([]feedback.Intent, error) {
		t.Error("retry must not parse the frozen plan")
		return nil, nil
	}))
	record, err = a.RetryFeedbackSubmission(context.Background(), sid, "partial-task")
	require.NoError(t, err)
	require.Equal(t, "applied", record.Status)
	current, err = a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, date, current.OpenAt)
	require.EqualValues(t, 1, submissionCount(t, a, "durable_memories", "student_id=?", sid))
	require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
	require.Equal(t, bid, current.BookID)
}

func TestFeedbackSubmissionParserCancellationLeavesRecoverableTask(t *testing.T) {
	a, b, sid := feedbackTwoApps(t)
	started := make(chan struct{})
	a.FB.SetLLM(submissionParser(func(ctx context.Context, _ string, _ time.Time) ([]feedback.Intent, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{SubmissionID: "cancel-plan", Text: "每次词太多了", Channel: "page"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("parser did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not propagate")
	}
	require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.Zero(t, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
	record, err := b.RetryFeedbackSubmission(context.Background(), sid, "cancel-plan")
	require.NoError(t, err)
	require.Equal(t, "applied", record.Status)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
}

func TestFeedbackDisabledOldTokenCannotSubmitQueryOrRetryAcrossApps(t *testing.T) {
	a, b, sid := feedbackTwoApps(t)
	token, _ := loginGet(t, a, "feedback-student", "fixturepw")
	_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "disable-task", Text: "每次词太多了", Channel: "page"})
	require.NoError(t, err)
	require.NoError(t, a.Auth.Disable("feedback-student"))
	a.persistUser("feedback-student")
	for _, tc := range []struct{ method, path string }{{"POST", "/api/v1/feedbacks"}, {"GET", "/api/v1/feedbacks/submissions/disable-task"}, {"POST", "/api/v1/feedbacks/submissions/disable-task/retry"}, {"GET", "/api/v1/feedbacks/mine"}} {
		resp := req2(t, b.Router(), tc.method, tc.path, token, map[string]any{"submission_id": "after-disable", "text": "每次词太多了", "channel": "page"})
		require.Contains(t, []int{401, 403}, resp.Code)
	}
	require.EqualValues(t, 1, submissionCount(t, a, "feedback_tasks", "student_id=?", sid))
}

func TestFeedbackSubmissionCanonicalTextAndStudentIsolation(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	text := "  " + strings.Repeat("多词正文 保持大小写 Abandon！", 20) + "\r\n\t每次词太多了  "
	normalized := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	record, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "identity-task", Text: text, Channel: "voice"})
	require.NoError(t, err)
	require.Equal(t, normalized, record.Text)
	replay, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "identity-task", Text: normalized, Channel: "page"})
	require.NoError(t, err)
	require.Equal(t, record.ID, replay.ID)
	other, err := a.Auth.CreateUser("feedback-other", "fixturepw", "Other", auth.RoleStudent)
	require.NoError(t, err)
	a.persistUser(other.Username)
	_, err = a.FeedbackSubmissionByID(context.Background(), other.ID, "identity-task")
	require.Error(t, err)
	own, err := a.SubmitFeedback(context.Background(), other.ID, FeedbackSubmission{SubmissionID: "identity-task", Text: normalized, Channel: "page"})
	require.NoError(t, err)
	require.NotEqual(t, record.ID, own.ID)
	require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
	require.Equal(t, 50, submissionStoredConfig(t, a, other.ID).Int(other.ID, "wordsPerLesson"))
	require.EqualValues(t, 2, submissionCount(t, a, "feedback_tasks", "submission_id=?", "identity-task"))
}

func TestFeedbackSubmissionStrictInputRejectsBeforeTaskCreation(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	for _, id := range []string{"with/slash", "percent%id", "white space", strings.Repeat("a", 129), "换行\n"} {
		_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: id, Text: "每次词太多了", Channel: "page"})
		require.Error(t, err, id)
	}
	for _, text := range []string{" ", "private\x00body", strings.Repeat("字", 2001), string([]byte{0xff, 0xfe})} {
		_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "bad-input", Text: text, Channel: "page"})
		require.Error(t, err)
	}
	_, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "bad-channel", Text: "每次词太多了", Channel: "admin"})
	require.Error(t, err)
	require.Zero(t, submissionCount(t, a, "feedback_tasks", "student_id=?", sid))
	require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
}

func TestFeedbackHTTPRejectsMalformedBodyBeforeCreatingTask(t *testing.T) {
	a, _, sid := feedbackTwoApps(t)
	token, _ := loginGet(t, a, "feedback-student", "fixturepw")
	bodies := []string{
		`{"submission_id":"invalid","text":"ok","student_id":999}`,
		`{"submission_id":"invalid","text":"ok","task_id":"forged"}`,
		`{"submission_id":"invalid","text":"ok","channel":"other"}`,
		`{"submission_id":"invalid","text":null}`,
		`{"submission_id":"invalid","text":42}`,
		`{"submission_id":"invalid","text":"ok"} {}`,
		`{"submission_id":"invalid","text":"` + strings.Repeat("字", 2001) + `"}`,
		`{"submission_id":"invalid","text":"ok"}` + strings.Repeat(" ", 16385),
		"{\"submission_id\":\"invalid\",\"text\":\"\xff\"}",
	}
	for index, body := range bodies {
		t.Run(itoa(index), func(t *testing.T) {
			var sink bytes.Buffer
			a.SetLogger(obs.NewWithSink(&sink))
			req := httptest.NewRequest("POST", "/api/v1/feedbacks", bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			a.Router().ServeHTTP(out, req)
			require.Equal(t, 400, doJSONBody(t, out).Code)
			trace := out.Header().Get("X-Trace-ID")
			require.NotEmpty(t, trace)
			found := false
			for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				if entry["event"] != "feedback_request_rejected" {
					continue
				}
				found = true
				require.Equal(t, trace, entry["trace_id"])
				require.EqualValues(t, sid, entry["student_id"])
				require.EqualValues(t, 400, entry["code"])
				require.Equal(t, "/api/v1/feedbacks", entry["route"])
				for _, key := range []string{"text", "body", "input", "error", "authorization"} {
					require.NotContains(t, entry, key)
				}
			}
			require.True(t, found)
			require.NotContains(t, sink.String(), token)
			require.NotContains(t, sink.String(), body)
		})
	}
	require.Zero(t, submissionCount(t, a, "feedback_tasks", "student_id=?", sid))
	require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
}

func TestFeedbackIntentCertificateNoticeAndAuditFailuresAreAtomic(t *testing.T) {
	triggers := map[string]string{
		"certificate": `CREATE TRIGGER fail_c_stage BEFORE UPDATE ON feedback_intents WHEN NEW.status='applied' BEGIN SELECT RAISE(ABORT,'fixture certificate failure'); END`,
		"notice":      `CREATE TRIGGER fail_c_stage BEFORE INSERT ON feedback_notices BEGIN SELECT RAISE(ABORT,'fixture notice failure'); END`,
		"audit":       `CREATE TRIGGER fail_c_stage BEFORE INSERT ON audit_logs WHEN NEW.ok=1 BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`,
	}
	for _, stage := range []string{"certificate", "notice", "audit"} {
		t.Run(stage, func(t *testing.T) {
			a, b, sid := feedbackTwoApps(t)
			require.NoError(t, a.DB.Exec(triggers[stage]).Error)
			record, err := a.SubmitFeedback(context.Background(), sid, FeedbackSubmission{SubmissionID: "atomic-task", Text: "每次词太多了", Channel: "page"})
			if err == nil {
				require.NotNil(t, record)
				require.NotEqual(t, "applied", record.Status)
			} else {
				var failure *FeedbackSubmissionError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, 503, failure.Code)
			}
			require.Equal(t, 70, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
			require.Equal(t, 70, a.Cfg.Int(sid, "wordsPerLesson"))
			require.Zero(t, submissionCount(t, a, "feedback_intents", "status=?", "applied"))
			require.Zero(t, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
			require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_c_stage`).Error)
			record, err = b.RetryFeedbackSubmission(context.Background(), sid, "atomic-task")
			require.NoError(t, err)
			require.Equal(t, "applied", record.Status)
			require.Equal(t, 60, submissionStoredConfig(t, a, sid).Int(sid, "wordsPerLesson"))
			require.EqualValues(t, 1, submissionCount(t, a, "feedback_intents", "status=?", "applied"))
			require.EqualValues(t, 1, submissionCount(t, a, "audit_logs", "tool=? AND ok=?", "config.set_student", true))
		})
	}
}

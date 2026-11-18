package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"enwords/internal/agent"
	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/memory"
	"enwords/internal/scheduler"
	"enwords/internal/store"
	"enwords/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestLessonPlanCompilesAllCurrentDecisionBranches(t *testing.T) {
	a := NewApp(nil, nil)
	for _, rate := range []float64{0, .7, .95} {
		for _, ms := range []int{1000, 16000} {
			acts := a.System.Decide(agent.SystemInput{Trigger: "lesson_finished", CorrectRate: rate, AvgAnswerMs: ms})
			plan, err := compileLessonPlan(acts)
			require.NoError(t, err)
			require.Len(t, plan.Steps, len(acts))
			require.LessOrEqual(t, plan.Calls, 8)
			require.Equal(t, "report_push", plan.Steps[len(plan.Steps)-1].Kind)
			if ms > 15000 {
				require.Equal(t, .4, plan.Policy.MaxNewRatio)
			}
		}
	}
}

func TestLessonPlanRejectsUnknownDuplicateMalformedAndOverBudget(t *testing.T) {
	base := []agent.SystemAction{{Kind: "plan_next"}, {Kind: "report_push"}}
	cases := [][]agent.SystemAction{
		{{Kind: "unknown"}},
		append(append([]agent.SystemAction{}, base...), agent.SystemAction{Kind: "plan_next"}),
		append(append([]agent.SystemAction{}, base...), agent.SystemAction{Kind: "adjust_words_down", Params: map[string]any{"delta": "-10"}}),
		append(append([]agent.SystemAction{}, base...), agent.SystemAction{Kind: "adjust_new_ratio", Params: map[string]any{"ratio": math.NaN()}}),
		append(append([]agent.SystemAction{}, base...), agent.SystemAction{Kind: "adjust_new_ratio", Params: map[string]any{"ratio": 2}}),
		{{Kind: "plan_next", Params: map[string]any{"unexpected": true}}, {Kind: "report_push"}},
	}
	over := append([]agent.SystemAction{}, base...)
	for i := 0; i < 7; i++ {
		over = append(over, agent.SystemAction{Kind: "memory_append", Params: map[string]any{"kind": "performance", "content": "fixture", "importance": 3}})
	}
	cases = append(cases, over)
	for _, actions := range cases {
		_, err := compileLessonPlan(actions)
		require.Error(t, err)
	}
}

func TestLessonAgentDurableSlowCompletionAndReplayAfterRemoval(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	res.Report.AvgAnswerMs = 16001
	st := classroom.State{Finished: true}
	require.NoError(t, a.commitLesson(lid, sid, bid, res, st))
	mem, err := a.Mem.Recall(memory.OwnerSystem, sid, bid, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
	require.Equal(t, lid, mem[0].SourceLessonID)
	var result struct {
		Actions []lessonStep
		Next    lessonScheduleResult
	}
	require.NoError(t, a.readRuntimeError("agent_result/"+itoa64(lid), &result))
	require.Len(t, result.Actions, 6)
	require.Equal(t, .4, result.Next.RequestedRatio)
	require.Equal(t, "planned", result.Next.Status)
	require.Contains(t, a.Notifications(sid)[0].Body, "每课学习量已设为50词")
	var audits int64
	require.NoError(t, a.DB.Model(&store.AuditLog{}).Where("trace_id=?", "lesson-"+itoa64(lid)).Count(&audits).Error)
	require.Equal(t, int64(6), audits)
	// Completed history no longer depends on an active enrollment.
	require.NoError(t, a.Sched.RemoveBook(sid, bid))
	reopened := NewApp(a.Clock, a.DB)
	require.NoError(t, reopened.commitLesson(lid, sid, bid, res, st))
	require.Len(t, reopened.Notifications(sid), 1)
	require.Error(t, reopened.commitLesson(lid, sid+1, bid, res, st))
	changed := *res
	changed.Report.AvgAnswerMs++
	require.Error(t, reopened.commitLesson(lid, sid, bid, &changed, st), "durable replay must preserve original facts")
	mem, err = reopened.Mem.Recall(memory.OwnerSystem, sid, bid, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
}

func TestLessonAgentDefaultRegistryAdvertisesButRejectsUnboundWrites(t *testing.T) {
	a := NewApp(nil, nil)
	seen := map[string]bool{}
	for _, def := range a.Tools.ToolsFor(tools.RoleSystemAgent) {
		seen[def.Name] = true
	}
	for _, name := range []string{"scheduler.plan_next_lesson", "report.compose_reply", "report.publish"} {
		require.True(t, seen[name], name)
		_, err := a.Tools.Call(context.Background(), tools.Caller{Role: tools.RoleSystemAgent}, name, []byte(`{}`))
		require.Error(t, err)
		require.NotErrorIs(t, err, tools.ErrUnknownTool)
	}
}

func TestLessonAgentExecutesRateAndPaceMatrix(t *testing.T) {
	for _, rate := range []float64{0, .7, .95} {
		for _, ms := range []int{1000, 16000} {
			a, _, lid, res := memoryLessonPlanFixture(t)
			res.Report.FirstTryRate, res.Report.AvgAnswerMs = rate, ms
			require.NoError(t, a.commitLesson(lid, 7, 1, res, classroom.State{Finished: true}))
			task := a.completionTasks[lid]
			require.Len(t, task.Done, len(task.Plan.Steps))
			mem, err := a.Mem.Recall(memory.OwnerSystem, 7, 1, "performance", "", 10)
			require.NoError(t, err)
			if rate >= .6 && rate < .85 {
				require.Empty(t, mem)
			} else {
				require.Len(t, mem, 1)
			}
			if rate < .6 {
				require.Equal(t, 50, a.Cfg.Int(7, "wordsPerLesson"))
			} else {
				require.Equal(t, 60, a.Cfg.Int(7, "wordsPerLesson"))
			}
			if ms > 15000 {
				require.Equal(t, .4, task.Next.RequestedRatio)
			}
			require.Len(t, a.Notifications(7), 1)
		}
	}
}

func TestLessonAgentNoWordsIsExplicitSuccessfulOutcome(t *testing.T) {
	a, repo, lid, res := memoryLessonPlanFixture(t)
	for _, wid := range []int64{1, 2} {
		card, err := repo.GetCard(7, 1, wid)
		require.NoError(t, err)
		card.Status = scheduler.StatusLearning
		card.LadderStep = len(a.Cfg.Ladder(7)) - 1
		require.NoError(t, repo.SaveCard(card))
		res.Correct[wid] = true
	}
	res.Report.FirstTryRate = .95
	require.NoError(t, a.commitLesson(lid, 7, 1, res, classroom.State{Finished: true}))
	require.Equal(t, "no_words", a.completionTasks[lid].Next.Status)
	require.Nil(t, a.completionTasks[lid].Next.Lesson)
	require.Len(t, a.Notifications(7), 1)
}

func TestLessonAgentCommittedRefreshFailureDoesNotRepeatEffects(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	p := &refreshFailurePersister{Persister: store.NewConfigPersister(a.DB)}
	cfg, err := config.NewStore(p, nil)
	require.NoError(t, err)
	a.Cfg = cfg
	p.fail = true
	st := classroom.State{Finished: true}
	require.NoError(t, a.commitLesson(lid, sid, bid, res, st), "committed lesson must not be reported as unsaved")
	require.NoError(t, a.commitLesson(lid, sid, bid, res, st))
	require.Equal(t, 60, a.Cfg.Int(sid, "wordsPerLesson"), "cache is still stale until refresh recovers")
	stored, err := config.NewStore(store.NewConfigPersister(a.DB), nil)
	require.NoError(t, err)
	require.Equal(t, 50, stored.Int(sid, "wordsPerLesson"))
	mem, err := a.Mem.Recall(memory.OwnerSystem, sid, bid, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
	require.Len(t, a.Notifications(sid), 1)
	p.fail = false
	require.NoError(t, a.commitLesson(lid, sid, bid, res, st))
	require.Equal(t, 50, a.Cfg.Int(sid, "wordsPerLesson"))
}

func TestLessonAgentBoundToolsRejectDifferentResourceAndArguments(t *testing.T) {
	a, _, lid, res := memoryLessonPlanFixture(t)
	ctx, err := a.newAgentTaskContext(context.Background(), 7, 1, lid, "lesson_finished", tools.RoleSystemAgent)
	require.NoError(t, err)
	p := &lessonCompletion{LessonID: lid, StudentID: 7, BookID: 2, Result: res}
	reg := tools.New(&tools.MemAuditor{})
	require.NoError(t, a.registerLessonAgentTools(reg, agentServices{scheduler: a.Sched, publisher: &lessonPublisher{task: p}}))
	_, err = reg.Call(ctx, tools.Caller{Role: tools.RoleSystemAgent}, "scheduler.plan_next_lesson", []byte(`{}`))
	require.Error(t, err)
	p.BookID = 1
	_, err = reg.Call(ctx, tools.Caller{Role: tools.RoleSystemAgent}, "scheduler.plan_next_lesson", []byte(`{"student_id":99}`))
	require.Error(t, err)
	_, err = reg.Call(ctx, tools.Caller{Role: tools.RoleTeacherAgent}, "report.publish", []byte(`{}`))
	require.ErrorIs(t, err, tools.ErrScopeDenied)
	_, err = reg.Call(ctx, tools.Caller{Role: tools.RoleSystemAgent}, "report.publish", []byte(`{}`))
	require.Error(t, err, "missing bound writer must not publish")
	require.Empty(t, a.Notifications(7))
}

func TestLessonAgentAuditFailureRollsBackAllEffects(t *testing.T) {
	a, sid, bid, lid, res := durableSettlementFixture(t)
	require.NoError(t, a.DB.Exec(`CREATE TRIGGER fail_lesson_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END`).Error)
	require.Error(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, scheduler.LessonScheduledStr, l.Status)
	require.Equal(t, 60, a.Cfg.Int(sid, "wordsPerLesson"))
	mem, err := a.Mem.Recall(memory.OwnerSystem, sid, bid, "performance", "", 10)
	require.NoError(t, err)
	require.Empty(t, mem)
	require.Empty(t, a.Notifications(sid))
	_, ok := a.Report(lid)
	require.False(t, ok)
	require.NoError(t, a.DB.Exec(`DROP TRIGGER fail_lesson_audit`).Error)
	require.NoError(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
	mem, err = a.Mem.Recall(memory.OwnerSystem, sid, bid, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
}

type lessonPlanFailRepo struct {
	scheduler.Repo
	failCreate      bool
	failReservation bool
}

func (r *lessonPlanFailRepo) CreateLesson(l *scheduler.Lesson) error {
	if r.failCreate {
		return errors.New("fixture plan failure")
	}
	return r.Repo.CreateLesson(l)
}
func (r *lessonPlanFailRepo) SaveCard(c *scheduler.Card) error {
	if r.failReservation && c.LastReviewAt != nil && c.NextDueAt != nil && !c.NextDueAt.Equal(c.LastReviewAt.Add(24*time.Hour)) {
		return errors.New("fixture reservation failure")
	}
	return r.Repo.SaveCard(c)
}

func memoryLessonPlanFixture(t *testing.T) (*App, *lessonPlanFailRepo, int64, *classroom.Result) {
	t.Helper()
	a := NewApp(&fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}, nil)
	repo := &lessonPlanFailRepo{Repo: scheduler.NewMemRepo()}
	for _, id := range []int64{1, 2} {
		require.NoError(t, repo.CreateCard(&scheduler.Card{StudentID: 7, BookID: 1, WordID: id, Status: scheduler.StatusNew}))
	}
	l := &scheduler.Lesson{StudentID: 7, BookID: 1, Status: scheduler.LessonScheduledStr, BatchWordIDs: []int64{1, 2}, OpenAt: a.Clock.Now()}
	require.NoError(t, repo.CreateLesson(l))
	a.Sched = scheduler.NewService(repo, a.Cfg, a.Clock)
	res := &classroom.Result{TotalWords: 2, Correct: map[int64]bool{1: false, 2: false}, Report: classroom.LessonReport{TotalWords: 2, FirstTryRate: 0}}
	return a, repo, l.ID, res
}

func TestLessonAgentMemoryRetryKeepsCompletedActionReceipts(t *testing.T) {
	a, repo, lid, res := memoryLessonPlanFixture(t)
	repo.failCreate = true
	st := classroom.State{Finished: true}
	require.Error(t, a.commitLesson(lid, 7, 1, res, st))
	mem, err := a.Mem.Recall(memory.OwnerSystem, 7, 1, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
	require.Equal(t, 50, a.Cfg.Int(7, "wordsPerLesson"))
	require.Empty(t, a.Notifications(7))
	for i := 0; i < 6; i++ {
		require.Error(t, a.commitLesson(lid, 7, 1, res, st), "retry gets a new bounded budget, not a new plan")
	}
	repo.failCreate = false
	require.NoError(t, a.commitLesson(lid, 7, 1, res, st))
	require.NoError(t, a.commitLesson(lid, 7, 1, res, st))
	mem, err = a.Mem.Recall(memory.OwnerSystem, 7, 1, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
	require.Len(t, a.Notifications(7), 1)
	lessons, err := a.Sched.Lessons(7, 1)
	require.NoError(t, err)
	require.Len(t, lessons, 2)
	res.Report.FirstTryRate = .9
	require.Error(t, a.commitLesson(lid, 7, 1, res, st), "retry must not change frozen facts")
}

func TestLessonAgentDurableWriteFailuresRollbackWholeTask(t *testing.T) {
	for _, failure := range []struct{ name, table, condition string }{
		{"config", "config_overrides", ""},
		{"memory", "durable_memories", ""},
		{"next_lesson", "lessons", ""},
		{"snapshot", "runtime_records", " WHEN NEW.key LIKE 'snapshot/%'"},
		{"notification", "runtime_records", " WHEN NEW.key = 'notifications'"},
		{"actions", "runtime_records", " WHEN NEW.key LIKE 'agent_result/%'"},
		{"marker", "runtime_records", " WHEN NEW.key LIKE 'settled/%'"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			a, sid, bid, lid, res := durableSettlementFixture(t)
			query := fmt.Sprintf("CREATE TRIGGER fail_completion BEFORE INSERT ON %s%s BEGIN SELECT RAISE(ABORT, 'fixture completion failure'); END", failure.table, failure.condition)
			require.NoError(t, a.DB.Exec(query).Error)
			require.Error(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
			l, err := a.Sched.Lesson(lid)
			require.NoError(t, err)
			require.Equal(t, scheduler.LessonScheduledStr, l.Status)
			mem, err := a.Mem.Recall(memory.OwnerSystem, sid, bid, "performance", "", 10)
			require.NoError(t, err)
			require.Empty(t, mem)
			require.Equal(t, 60, a.Cfg.Int(sid, "wordsPerLesson"))
			require.Empty(t, a.Notifications(sid))
			_, ok := a.Report(lid)
			require.False(t, ok)
			lessons, err := a.Sched.Lessons(sid, bid)
			require.NoError(t, err)
			require.Len(t, lessons, 1)
			require.NoError(t, a.DB.Exec("DROP TRIGGER fail_completion").Error)
			require.NoError(t, a.commitLesson(lid, sid, bid, res, classroom.State{Finished: true}))
			require.Len(t, a.Notifications(sid), 1)
		})
	}
}

func TestLessonAgentRejectsForeignWordsAndNaNFactsBeforeWrites(t *testing.T) {
	a, _, lid, res := memoryLessonPlanFixture(t)
	res.Correct[999] = true
	require.Error(t, a.commitLesson(lid, 7, 1, res, classroom.State{}))
	delete(res.Correct, 999)
	res.Report.FirstTryRate = math.NaN()
	require.Error(t, a.commitLesson(lid, 7, 1, res, classroom.State{}))
	l, err := a.Sched.Lesson(lid)
	require.NoError(t, err)
	require.Equal(t, scheduler.LessonScheduledStr, l.Status)
	require.Empty(t, a.Notifications(7))
}

func TestLessonAgentMemoryRepairsReservationAfterLessonCreated(t *testing.T) {
	a, repo, lid, res := memoryLessonPlanFixture(t)
	a.TestMode = true
	repo.failReservation = true
	st := classroom.State{Finished: true}
	require.Error(t, a.commitLesson(lid, 7, 1, res, st))
	lessons, err := a.Sched.Lessons(7, 1)
	require.NoError(t, err)
	require.Len(t, lessons, 2)
	require.Empty(t, a.Notifications(7))
	repo.failReservation = false
	require.NoError(t, a.commitLesson(lid, 7, 1, res, st))
	lessons, err = a.Sched.Lessons(7, 1)
	require.NoError(t, err)
	require.Len(t, lessons, 2)
	for _, l := range lessons {
		if l.Status == scheduler.LessonScheduledStr {
			for _, wid := range l.BatchWordIDs {
				c, e := a.Sched.GetCard(7, 1, wid)
				require.NoError(t, e)
				require.Equal(t, l.OpenAt, *c.NextDueAt)
			}
		}
	}
	mem, err := a.Mem.Recall(memory.OwnerSystem, 7, 1, "performance", "", 10)
	require.NoError(t, err)
	require.Len(t, mem, 1)
	require.Len(t, a.Notifications(7), 1)
}

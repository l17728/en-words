package agent

import (
	"strings"
	"testing"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/feedback"
	"enwords/internal/memory"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }

// ---- TeacherAgent 决策表 ----

func TestTeacherDecide(t *testing.T) {
	ta := NewTeacherAgent(nil)
	in := TeacherInput{LessonID: 12, StudentName: "小明",
		Word: classroom.Word{ID: 1, Word: "apple", Meanings: []string{"n. 苹果"}}}

	// 开场
	u := ta.Decide(in.withEvent("lesson_started"))
	require.NotNil(t, u)
	assert.Equal(t, "open", u.Category)

	// 答错 → correct 槽带释义
	u = ta.Decide(in.withEvent("judged_wrong"))
	require.NotNil(t, u)
	assert.Equal(t, "correct", u.Category)
	assert.Equal(t, "n. 苹果", u.Vars["meaning"])

	// 超时 → nudge
	u = ta.Decide(in.withEvent("repeat_timeout"))
	require.NotNil(t, u)
	assert.Equal(t, "nudge", u.Category)

	// 下课 → closing
	u = ta.Decide(in.withEvent("lesson_end"))
	require.NotNil(t, u)
	assert.Equal(t, "closing", u.Category)

	// 鼓励密度不足 → 无话术
	u = ta.Decide(TeacherInput{LessonID: 12, Event: "judged_correct", EncourageN: 2})
	assert.Nil(t, u)
	// 密度到 → encourage
	u = ta.Decide(TeacherInput{LessonID: 12, Event: "judged_correct", EncourageN: 8})
	require.NotNil(t, u)
	assert.Equal(t, "encourage", u.Category)

	// 未知事件 → nil
	assert.Nil(t, ta.Decide(in.withEvent("unknown")))
}

func TestTeacherRenderDeterministic(t *testing.T) {
	ta := NewTeacherAgent(nil)
	in := TeacherInput{LessonID: 12, StudentName: "小明",
		Word: classroom.Word{Word: "apple", Meanings: []string{"n. 苹果"}},
		Event: "judged_wrong"}
	u := ta.Decide(in)
	t1 := ta.Render(in, u)
	t2 := ta.Render(in, u)
	assert.Equal(t, t1, t2, "seed=lessonId 渲染确定性")
	assert.False(t, strings.Contains(t1, "{"), "占位符已替换")
	assert.Contains(t, t1, "apple")
}

// ---- SystemAgent 结课决策（S-01..S-04 + S-15）----

func newSA() *SystemAgent {
	return NewSystemAgent(nil, nil, feedback.NewParser(fixedNow))
}

func TestSystemLessonFinishedHigh(t *testing.T) { // S-01
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "lesson_finished", CorrectRate: 0.9, AvgAnswerMs: 5000})
	require.NotEmpty(t, acts)
	assert.Equal(t, "plan_next", acts[0].Kind)
	// 高正确率无降量
	for _, a := range acts {
		assert.NotEqual(t, "adjust_words_down", a.Kind)
	}
	assert.Equal(t, "report_push", acts[len(acts)-1].Kind, "S-15 课报推送收尾")
}

func TestSystemLessonFinishedLow(t *testing.T) { // S-03
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "lesson_finished", CorrectRate: 0.5, AvgAnswerMs: 8000})
	found := map[string]bool{}
	for _, a := range acts {
		found[a.Kind] = true
	}
	assert.True(t, found["adjust_words_down"], "低正确率降量")
	assert.True(t, found["memory_append"], "写任务记忆(正确率低)")
	assert.True(t, found["reply_comfort"], "回复安慰")
}

func TestSystemLessonFinishedSlow(t *testing.T) { // S-04
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "lesson_finished", CorrectRate: 0.75, AvgAnswerMs: 18000})
	found := map[string]bool{}
	for _, a := range acts {
		found[a.Kind] = true
	}
	assert.True(t, found["adjust_new_ratio"], "慢学生降新词占比 0.4")
}

// ---- SystemAgent 反馈决策（S-05..S-12）----

func TestSystemFeedbackReduce(t *testing.T) { // S-05 A 类
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "每次词太多了，累"})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "adjust_words")
}

func TestSystemFeedbackPostponeWithMemory(t *testing.T) { // S-06 + D-16 记忆
	mem := memory.NewService(memory.NewMemRepo())
	sa := NewSystemAgent(nil, mem, feedback.NewParser(fixedNow))
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "这周三有事，先缓缓", StudentID: 7})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "postpone")
	assert.Contains(t, kinds, "memory_append", "请假写入任务记忆")
	assert.Contains(t, kinds, "reply_applied")
	// 记忆落库（装配层执行 memory_append 动作；请假属任务记忆需 bookId）
	for _, a := range acts {
		if a.Kind == "memory_append" {
			_, err := mem.Append(memory.OwnerSystem, memory.ScopeTaskMemory, 7, 3,
				"promise", "学生请假", 4, 0)
			require.NoError(t, err)
		}
	}
	ms, _ := mem.List(memory.OwnerSystem, 7, 3, "")
	require.Len(t, ms, 1)
}

func TestSystemFeedbackMultiIntent(t *testing.T) { // S-12
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "太多了，而且这周三有事"})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "adjust_words")
	assert.Contains(t, kinds, "postpone", "多意图逐项处理")
}

func TestSystemFeedbackNeedConfirm(t *testing.T) { // G-4 超界
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "词量降到30个"})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "reply_needconfirm")
	assert.NotContains(t, kinds, "adjust_words", "超界不直接应用")
}

func TestSystemFeedbackEscalate(t *testing.T) { // S-08 F 类
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "发音读错了"})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "escalate")
}

// ---- 逾期决策（S-10/S-11）----

func TestSystemOverdue(t *testing.T) {
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "cron_overdue", OverdueLessons: 1})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "postpone")
	assert.Contains(t, kinds, "notify_replan")

	acts = sa.Decide(SystemInput{Trigger: "cron_overdue", OverdueLessons: 3})
	kinds = kindsOf(acts)
	assert.Contains(t, kinds, "suggest_pause", "连续3次逾期建议暂停(S-11)")
}

// ---- 回复渲染 ----

func TestSystemRenderReply(t *testing.T) {
	sa := newSA()
	act := SystemAction{ReplyCat: "feedback_reply",
		ReplyVars: map[string]any{"date": "周三", "newDate": "周四", "n": 50}}
	text := sa.RenderReply(7, act)
	assert.False(t, strings.Contains(text, "{"))
	assert.NotEmpty(t, text)
	// 无回复槽
	assert.Empty(t, sa.RenderReply(7, SystemAction{Kind: "postpone"}))
}

// ---- 决策表剩余分支 ----

func TestTeacherDecideBranches(t *testing.T) {
	ta := NewTeacherAgent(nil)
	// word_done：lessonID 可被 3 整除 → transition；否则无话术
	u := ta.Decide(TeacherInput{LessonID: 9, Event: "word_done"})
	require.NotNil(t, u)
	assert.Equal(t, "transition", u.Category)
	u = ta.Decide(TeacherInput{LessonID: 10, Event: "word_done"})
	assert.Nil(t, u)

	// start_quiz(final 唯一测试, D-21) / answer_timeout / state_restored
	for _, ev := range []string{"start_quiz", "answer_timeout", "state_restored"} {
		u := ta.Decide(TeacherInput{LessonID: 1, Event: ev})
		require.NotNil(t, u, "%s 应有话术槽", ev)
	}
}

func TestTeacherRenderFallback(t *testing.T) { // 渲染失败 → Fallback 原文
	ta := NewTeacherAgent(nil)
	text := ta.Render(TeacherInput{LessonID: 1}, &Utterance{Category: "no_such_cat", Fallback: "保底话术"})
	assert.Equal(t, "保底话术", text)
	// nil utterance
	assert.Empty(t, ta.Render(TeacherInput{LessonID: 1}, nil))
}

func TestSystemLessonLowAndSlow(t *testing.T) { // 低正确率+慢 组合
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "lesson_finished", CorrectRate: 0.5, AvgAnswerMs: 20000})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "adjust_words_down")
	assert.Contains(t, kinds, "adjust_new_ratio", "组合触发两个调整")
}

func TestSystemUnknownTrigger(t *testing.T) {
	sa := newSA()
	assert.Nil(t, sa.Decide(SystemInput{Trigger: "whatever"}))
}

func TestSystemFeedbackClarify(t *testing.T) { // G 类追问
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "今天天气好"})
	assert.Contains(t, kindsOf(acts), "reply_clarify")
}

func TestSystemFeedbackBoost(t *testing.T) { // E 类难词
	sa := newSA()
	acts := sa.Decide(SystemInput{Trigger: "feedback_submitted", FeedbackText: "abandon 我老忘"})
	kinds := kindsOf(acts)
	assert.Contains(t, kinds, "boost")
	assert.Contains(t, kinds, "reply_applied")
}

// ---- helpers ----

func (in TeacherInput) withEvent(ev string) TeacherInput {
	in.Event = ev
	return in
}

func kindsOf(acts []SystemAction) []string {
	var out []string
	for _, a := range acts {
		out = append(out, a.Kind)
	}
	return out
}

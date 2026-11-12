package agent

import (
	"strings"
	"testing"
	"time"

	"enwords/internal/classroom"

	"github.com/stretchr/testify/require"
)

// The teacher may select a tool for a validated classroom fact, but must not
// silently drop or reorder effects that would change what the student sees.
func TestTeacherPlanEffectsPreservesOrderAndRejectsUnknown(t *testing.T) {
	a := NewTeacherAgent(nil)
	actions := []classroom.Action{
		{Type: classroom.ActShowWord},
		{Type: classroom.ActPlayTTS},
		{Type: classroom.ActStartRepeat},
		{Type: classroom.ActNudgeRepeat},
		{Type: classroom.ActStartQuiz},
		{Type: classroom.ActShowQuizItem},
		{Type: classroom.ActNudgeAnswer},
		{Type: classroom.ActJudgeRequest},
		{Type: classroom.ActStartReview},
		{Type: classroom.ActShowCheck},
		{Type: classroom.ActShowCross},
		{Type: classroom.ActSayHint},
		{Type: classroom.ActTTSSkipped},
		{Type: classroom.ActJudgeFallback},
		{Type: classroom.ActLessonPaused},
		{Type: classroom.ActLessonSummary},
	}
	wantTools := []string{
		"classroom.show_word", "classroom.play_tts",
		"classroom.start_repeat", "classroom.start_repeat",
		"classroom.start_quiz", "classroom.start_quiz", "classroom.start_quiz",
		"classroom.judge_answer", "classroom.start_review",
		"classroom.show_feedback_ui", "classroom.show_feedback_ui",
		"classroom.show_feedback_ui", "classroom.show_feedback_ui",
		"classroom.show_feedback_ui", "classroom.show_feedback_ui",
		"classroom.show_feedback_ui",
	}
	effects, err := a.PlanEffects(actions)
	require.NoError(t, err)
	require.Len(t, effects, len(actions))
	for i, effect := range effects {
		require.Equal(t, i, effect.Index)
		require.Equal(t, wantTools[i], effect.Tool)
	}

	// A partially planned list must never be executed after an unknown action.
	actions = append(actions, classroom.Action{Type: "unregistered_effect"})
	effects, err = a.PlanEffects(actions)
	require.Error(t, err)
	require.Nil(t, effects)
}

func TestOverduePlanRespectsStudentCalendarAndEscalation(t *testing.T) {
	a := NewSystemAgent(nil, nil, nil)
	local := time.FixedZone("student", 8*60*60)
	original := time.Date(2026, 10, 10, 19, 30, 0, 0, local)
	// This is already the next day in the student's timezone.
	detected := time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		consecutive int
		care, pause bool
	}{
		{1, false, false},
		{2, true, false},
		{3, true, true},
	} {
		plan := a.PlanOverdue(original, detected, tc.consecutive)
		require.Equal(t, time.Date(2026, 10, 13, 19, 30, 0, 0, local), plan.Target)
		require.Equal(t, tc.consecutive, plan.Consecutive)
		require.Equal(t, tc.care, plan.Care)
		require.Equal(t, tc.pause, plan.SuggestPause)
		notice := a.OverdueNotice(plan)
		require.Contains(t, notice, "2026-10-13 19:30")
		require.Equal(t, tc.care, strings.Contains(notice, "时间紧"))
		require.Equal(t, tc.pause, strings.Contains(notice, "暂停计划"))
	}
}

func TestFeedbackLimitsAtBoundary(t *testing.T) {
	require.Equal(t, 3, FeedbackPostponeLimit())
	require.True(t, FeedbackPostponeAllowed(2))
	require.False(t, FeedbackPostponeAllowed(3))
	require.Equal(t, 20, FeedbackBoostCapacity())
}

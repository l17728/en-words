package agent

import (
	"enwords/internal/scripts"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTeacherUsesExplicitEncouragementThreshold(t *testing.T) {
	a := NewTeacherAgent(nil)
	in := TeacherInput{Event: "judged_correct", EncourageN: 2, EncourageEveryN: 3}
	require.Nil(t, a.Decide(in))
	in.EncourageN = 3
	require.Equal(t, "encourage", a.Decide(in).Category)
	in.EncourageEveryN = 0
	require.Nil(t, a.Decide(in), "missing configuration is not an implicit hard-coded 8")
}

func TestTeacherFallbackAndFactBoundedLifecycle(t *testing.T) {
	a := NewTeacherAgent(scripts.NewBank())
	in := TeacherInput{Event: "lesson_started", LessonID: 1, TotalWords: 5}
	u := a.Decide(in)
	require.NotEmpty(t, u.Fallback)
	require.Equal(t, u.Fallback, a.Render(in, u))
	in.Event = "lesson_end"
	text := a.Render(in, a.Decide(in))
	require.NotContains(t, text, "都过关")
	require.NotContains(t, text, "比上次")
	require.LessOrEqual(t, len([]rune(text)), 60)
}

package agent

import (
	"enwords/internal/classroom"
	"fmt"
)

// TeacherEffect chooses a capability for an already validated state-machine
// fact. It cannot choose another student, word, phase or playback setting.
type TeacherEffect struct {
	Tool  string
	Index int
}

func (a *TeacherAgent) PlanEffects(actions []classroom.Action) ([]TeacherEffect, error) {
	out := make([]TeacherEffect, 0, len(actions))
	for i, action := range actions {
		name := ""
		switch action.Type {
		case classroom.ActShowWord:
			name = "classroom.show_word"
		case classroom.ActPlayTTS:
			name = "classroom.play_tts"
		case classroom.ActStartRepeat, classroom.ActNudgeRepeat:
			name = "classroom.start_repeat"
		case classroom.ActStartQuiz, classroom.ActShowQuizItem, classroom.ActNudgeAnswer:
			name = "classroom.start_quiz"
		case classroom.ActJudgeRequest:
			name = "classroom.judge_answer"
		case classroom.ActStartReview:
			name = "classroom.start_review"
		case classroom.ActShowCheck, classroom.ActShowCross, classroom.ActSayHint, classroom.ActTTSSkipped, classroom.ActJudgeFallback, classroom.ActLessonPaused, classroom.ActLessonSummary:
			name = "classroom.show_feedback_ui"
		default:
			return nil, fmt.Errorf("unsupported classroom effect")
		}
		out = append(out, TeacherEffect{Tool: name, Index: i})
	}
	return out, nil
}

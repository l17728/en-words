package classroom

import "fmt"

func (m *Machine) validateRuntimePosition(s State) error {
	bad := func() error { return fmt.Errorf("classroom: invalid runtime snapshot") }
	switch s.Phase {
	case PhaseShowWord, PhaseTTSSeg, PhaseRepeating, PhaseWaitAnswer, PhaseJudging, PhaseReteachShow, PhaseLessonEnd:
	default:
		return bad()
	}
	if s.WordIdx < 0 || s.WordIdx > len(m.words) || s.QuizIdx < 0 || s.QuizIdx > len(s.QuizQueue) || s.ReviewIdx < 0 || s.ReviewIdx > len(s.ReviewList) {
		return bad()
	}
	needsQuiz := s.Phase == PhaseWaitAnswer || s.Phase == PhaseJudging || s.Phase == PhaseReteachShow || (s.Phase == PhaseRepeating && s.InReteach)
	if needsQuiz && (s.QuizIdx >= len(s.QuizQueue) || m.wordByID[s.QuizQueue[s.QuizIdx].WordID].ID == 0) {
		return bad()
	}
	if s.InReview && s.Phase == PhaseTTSSeg {
		if s.ReviewIdx >= len(s.ReviewList) || m.wordByID[s.ReviewList[s.ReviewIdx]].ID == 0 {
			return bad()
		}
	} else if !s.InReteach && (s.Phase == PhaseShowWord || s.Phase == PhaseTTSSeg || s.Phase == PhaseRepeating) && s.WordIdx >= len(m.words) {
		return bad()
	}
	return nil
}

// RecoverDelivery reissues only the current unacknowledged business action.
// It never advances teaching, grades an answer or restarts a lesson.
func (m *Machine) RecoverDelivery(judgmentInFlight bool) {
	st := m.state
	if st.Phase == PhaseJudging && !judgmentInFlight {
		m.RecoverJudgment()
	}
	if st.Paused || st.Finished {
		return
	}
	switch st.Phase {
	case PhaseTTSSeg, PhaseReteachShow:
		m.acts = append(m.acts, m.currentPlaybackAction())
	case PhaseRepeating:
		w := m.CurrentWord()
		if st.InReteach {
			w = m.wordByID[m.currentQuizItem().WordID]
		}
		m.acts = append(m.acts, Action{Type: ActStartRepeat, WordID: w.ID})
	case PhaseWaitAnswer:
		m.acts = append(m.acts, Action{Type: ActShowQuizItem, WordID: m.currentQuizItem().WordID})
	}
}

// RecoverJudgment also restores REST-only rooms before their first snapshot.
func (m *Machine) RecoverJudgment() {
	st := m.state
	if st.Phase != PhaseJudging {
		return
	}
	st.Phase = PhaseWaitAnswer
	st.PendingText = ""
	if len(st.AnswerMs) > 0 {
		st.AnswerMs = st.AnswerMs[:len(st.AnswerMs)-1]
	}
}

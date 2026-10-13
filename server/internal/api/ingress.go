package api

import (
	"enwords/internal/classroom"
	"enwords/internal/obs"
	"fmt"
	"strings"
)

type TurnContext struct {
	Epoch string `json:"epoch"`
	Turn  string `json:"turn"`
}
type ClientEnvelope struct {
	Context TurnContext `json:"context"`
	EventID string      `json:"event_id"`
}

func (r *Room) contextLocked() TurnContext {
	if r.epoch == "" {
		r.epoch = obs.NewTraceID()
	}
	s := r.machine.Snapshot()
	signature := fmt.Sprintf("%s/%d/%d/%d/%d/%d/%d/%t/%t/%d/%t/%d/%d", s.Phase, s.WordIdx, s.TtsSegIdx, s.QuizIdx, s.QuizSeq, s.AnswerStage, s.RepeatCount, s.InReteach, r.machine.Paused(), s.ReteachSeg, s.InReview, s.ReviewIdx, s.ReviewSeg)
	if signature != r.lastTurnSignature {
		r.turnVersion++
		r.lastTurnSignature = signature
		r.phaseSince = r.app.Clock.Now()
	}
	return TurnContext{r.epoch, fmt.Sprintf("%d:%s", r.turnVersion, signature)}
}

func (r *Room) acceptLocked(e ClientEnvelope) bool {
	if strings.TrimSpace(e.EventID) == "" || len(e.EventID) > 128 || e.Context != r.contextLocked() {
		return false
	}
	if r.seenEvents == nil {
		r.seenEvents = map[string]bool{}
	}
	if r.seenEvents[e.EventID] {
		return false
	}
	r.seenEvents[e.EventID] = true
	r.eventOrder = append(r.eventOrder, e.EventID)
	if len(r.eventOrder) > 2048 {
		delete(r.seenEvents, r.eventOrder[0])
		r.eventOrder = r.eventOrder[1:]
	}
	return true
}
func (r *Room) rejectedLocked() []Msg {
	r.app.Log.Info("classroom", "event_rejected", map[string]any{"lesson_id": r.lessonID, "reason": "missing_stale_or_duplicate"})
	return []Msg{{"type": "event_rejected", "reason": "missing_stale_or_duplicate", "context": r.contextLocked()}, r.snapshotLocked()}
}
func (r *Room) HandleClientEvent(e ClientEnvelope, ev classroom.Event, source ...Client) []Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (len(source) > 0 && r.currentCli != source[0]) || !r.acceptLocked(e) {
		return r.rejectedLocked()
	}
	out := r.handleEventLocked(ev)
	done := Msg{"type": "event_processed", "event_id": e.EventID}
	r.emitLocked(done)
	return append(out, done)
}
func (r *Room) HandleClientVoice(e ClientEnvelope, text string, source ...Client) (string, []Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (len(source) > 0 && r.currentCli != source[0]) || !r.acceptLocked(e) {
		return "", r.rejectedLocked()
	}
	command, out := r.handleVoiceLocked(text)
	done := Msg{"type": "event_processed", "event_id": e.EventID}
	r.emitLocked(done)
	return command, append(out, done)
}

package api

import (
	"context"
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
	Context      TurnContext `json:"context"`
	EventID      string      `json:"event_id"`
	SubmissionID string      `json:"submission_id,omitempty"`
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
func (r *Room) rejectedLocked(e ClientEnvelope) []Msg {
	r.app.Log.InfoCtx(r.traceContext(), "classroom", "event_rejected", map[string]any{"lesson_id": r.lessonID, "reason": "missing_stale_or_duplicate"})
	return []Msg{{"type": "event_rejected", "reason": "missing_stale_or_duplicate", "context": r.contextLocked(), "event_id": e.EventID, "submission_id": e.SubmissionID}, r.snapshotLocked()}
}
func (r *Room) HandleClientEvent(e ClientEnvelope, ev classroom.Event, source ...Client) []Msg {
	return r.HandleClientEventContext(context.Background(), e, ev, source...)
}
func (r *Room) HandleClientEventContext(ctx context.Context, e ClientEnvelope, ev classroom.Event, source ...Client) []Msg {
	lease, ownedLease, leaseErr := r.operationLease(ctx)
	if leaseErr != nil {
		return []Msg{{"type": "event_rejected", "reason": "generation_retired", "event_id": e.EventID}}
	}
	if ownedLease {
		defer lease.Release()
		ctx = withGenerationLease(lease.Context(), lease)
	}
	effect, err := r.generation.Permit()
	if err != nil {
		return []Msg{{"type": "event_rejected", "reason": "generation_retired", "event_id": e.EventID}}
	}
	defer effect.Release()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired || ctx.Err() != nil || (len(source) > 0 && !r.activeLocked(source[0])) || !r.acceptLocked(e) {
		return r.rejectedLocked(e)
	}
	r.inputContext = ctx
	defer func() { r.inputContext = nil }()
	if ev.Type == classroom.EvAnswerSubmitted && r.machine.Phase() == classroom.PhaseWaitAnswer &&
		r.voiceParser.MatchesPhrase(ev.Text, "跳过", "跳过这个词", "过") {
		item := r.machine.CurrentItem().QuizItemID
		ev = classroom.Event{Type: classroom.EvQuizSkipped, Item: &item}
		r.app.Log.InfoCtx(r.traceContext(), "classroom", "quiz_skipped", map[string]any{
			"lesson_id": r.lessonID, "word_id": item.WordID, "quiz_item": item,
			"outcome": "wrong_advance", "reason": "explicit_skip",
		})
	}
	r.app.Log.InfoCtx(r.traceContext(), "classroom", "event_received", map[string]any{"lesson_id": r.lessonID, "event_id": e.EventID, "request_trace_id": obs.TraceID(ctx), "event_type": ev.Type})
	out := r.handleEventLocked(ev)
	if r.retired {
		return nil
	}
	done := Msg{"type": "event_processed", "event_id": e.EventID}
	r.emitLocked(done)
	return append(out, done)
}
func (r *Room) HandleClientVoice(e ClientEnvelope, text string, source ...Client) (string, []Msg) {
	cmd, _, frames := r.HandleClientVoiceSubmission(e, text, source...)
	return cmd, frames
}

// Return the body belonging to this event while still holding the Room lock.
// HTTP/WS must never consume a shared queue after another event could replace it.
func (r *Room) HandleClientVoiceSubmission(e ClientEnvelope, text string, source ...Client) (string, string, []Msg) {
	return r.HandleClientVoiceSubmissionContext(context.Background(), e, text, source...)
}
func (r *Room) HandleClientVoiceSubmissionContext(ctx context.Context, e ClientEnvelope, text string, source ...Client) (string, string, []Msg) {
	lease, ownedLease, leaseErr := r.operationLease(ctx)
	if leaseErr != nil {
		return "", "", []Msg{{"type": "event_rejected", "reason": "generation_retired", "event_id": e.EventID, "submission_id": e.SubmissionID}}
	}
	if ownedLease {
		defer lease.Release()
		ctx = withGenerationLease(lease.Context(), lease)
	}
	effect, err := r.generation.Permit()
	if err != nil {
		return "", "", []Msg{{"type": "event_rejected", "reason": "generation_retired", "event_id": e.EventID, "submission_id": e.SubmissionID}}
	}
	defer effect.Release()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired || ctx.Err() != nil || (len(source) > 0 && !r.activeLocked(source[0])) || !r.acceptLocked(e) {
		return "", "", r.rejectedLocked(e)
	}
	r.inputContext = ctx
	defer func() { r.inputContext = nil }()
	r.app.Log.InfoCtx(r.traceContext(), "classroom", "event_received", map[string]any{"lesson_id": r.lessonID, "event_id": e.EventID, "request_trace_id": obs.TraceID(ctx), "event_type": "voice_text", "text_length": len([]rune(text))})
	var command, payload string
	var out []Msg
	if cmd, body, ok := r.voiceParser.MatchFeedback(text); ok {
		r.activateDeliveryLocked()
		command, payload = cmd, body
	} else {
		command, out = r.handleVoiceInputLocked(text, sanitizeText(text))
	}
	if r.retired {
		return "", "", nil
	}
	done := Msg{"type": "event_processed", "event_id": e.EventID, "command": command, "submission_id": e.SubmissionID}
	if command == "feedback" {
		done["normalized_text"] = strings.TrimSpace(strings.ReplaceAll(payload, "\r\n", "\n"))
	}
	r.emitLocked(done)
	return command, payload, append(out, done)
}

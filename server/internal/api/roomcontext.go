package api

import (
	"context"
	"fmt"
	"time"

	"enwords/internal/classroom"
	"enwords/internal/judge"
	"enwords/internal/obs"
)

type roomJudgment struct {
	turn   TurnContext
	cancel context.CancelFunc
}

// Stable across REST, WebSocket, recovery and the settlement tool transaction.
func (r *Room) traceContext() context.Context {
	return obs.WithTrace(context.Background(), fmt.Sprintf("lesson-%d", r.lessonID))
}

// Caller holds r.mu; the network runs outside it. Return always reacquires it.
func (r *Room) evaluateLocked(word classroom.Word, text string) (judge.Judgment, bool) {
	if r.pendingJudge != nil || r.machine.Paused() || r.retired {
		return "", false
	}
	base := r.inputContext
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(obs.WithTrace(base, fmt.Sprintf("lesson-%d", r.lessonID)), 8*time.Second)
	pending := &roomJudgment{turn: r.contextLocked(), cancel: cancel}
	r.pendingJudge = pending
	r.app.saveLessonStateContext(r.traceContext(), r.lessonID, r.machine.Snapshot())
	r.app.Log.InfoCtx(ctx, "judge", "judge_started", map[string]any{"lesson_id": r.lessonID, "word_id": word.ID, "answer_length": len([]rune(text)), "turn": pending.turn.Turn})
	start := time.Now()
	r.mu.Unlock()
	result := r.app.Judge.JudgeContext(ctx, word.Word, append([]string(nil), word.Meanings...), text)
	r.mu.Lock()
	valid := r.pendingJudge == pending && !r.retired && !r.machine.Paused() && pending.turn == r.contextLocked() && ctx.Err() == nil
	if r.pendingJudge == pending {
		r.pendingJudge = nil
	}
	cancel()
	event := "judge_completed"
	if !valid {
		event = "judge_stale"
	}
	r.app.Log.InfoCtx(ctx, "judge", event, map[string]any{"lesson_id": r.lessonID, "word_id": word.ID, "turn": pending.turn.Turn, "duration_ms": time.Since(start).Milliseconds()})
	return result, valid
}

func (r *Room) cancelJudgmentLocked() {
	if r.pendingJudge != nil {
		r.pendingJudge.cancel()
		r.pendingJudge = nil
	}
}

func (r *Room) activeLocked(c Client) bool {
	if r.currentCli != c || r.retired {
		return false
	}
	if state, ok := c.(interface{ IsClosed() bool }); ok && state.IsClosed() {
		return false
	}
	return true
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"enwords/internal/agent"
	"enwords/internal/obs"
	"enwords/internal/tools"
)

func (a *App) registerTeacherTools() {
	if err := a.registerTeacherTool(a.Tools); err != nil {
		panic(err)
	}
}

func (a *App) registerTeacherTool(reg *tools.Registry) error {
	return reg.Register(tools.Tool{Name: "classroom.say", Domain: "classroom", Risk: tools.RiskLow,
		Scope:       []tools.Role{tools.RoleTeacherAgent},
		InputSchema: mustSchema(`{"type":"object","properties":{"text":{"type":"string"},"category":{"type":"string"}},"required":["text","category"]}`),
		Handler: func(ctx context.Context, caller tools.Caller, args tools.Args) (any, error) {
			task, err := taskFromContext(ctx, caller)
			if err != nil {
				return nil, err
			}
			if task.app != a || task.lessonID <= 0 || task.trigger != "classroom_event" {
				return nil, fmt.Errorf("trusted classroom task required")
			}
			var in struct {
				Text     string `json:"text"`
				Category string `json:"category"`
			}
			dec := json.NewDecoder(bytes.NewReader(args))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				return nil, err
			}
			var extra any
			if err := dec.Decode(&extra); err != io.EOF {
				return nil, fmt.Errorf("single argument object required")
			}
			if strings.TrimSpace(in.Text) == "" || len([]rune(in.Text)) > 60 {
				return nil, fmt.Errorf("speech text must contain 1-60 characters")
			}
			switch in.Category {
			case "open", "transition", "encourage", "correct", "nudge", "closing":
			default:
				return nil, fmt.Errorf("unknown teacher category")
			}
			// Pure result: no Room mutex, no state transition or extra audio side effect.
			return Msg{"type": "ui_action", "action": map[string]any{"type": "say", "text": in.Text, "category": in.Category}}, nil
		}})
}

func (r *Room) teacherInputLocked(event string, wordID int64) agent.TeacherInput {
	st := r.machine.Snapshot()
	w := r.words[wordID]
	if w.ID == 0 {
		w = r.machine.CurrentWord()
	}
	threshold := 0
	if st.Config != nil {
		threshold = st.Config.EncourageEveryN
	}
	return agent.TeacherInput{LessonID: r.lessonID, StudentName: r.studentName(), Event: event, Phase: st.Phase, Word: w,
		EncourageN: r.encourageCounter, EncourageEveryN: threshold, StreakBest: st.StreakBest, TotalWords: len(r.words), ReteachCount: st.ReteachN[wordID]}
}

func (r *Room) teacherUtteranceLocked(ctx context.Context, in agent.TeacherInput) []Msg {
	taskID := ""
	if task, err := taskFromContext(ctx, tools.Caller{Role: tools.RoleTeacherAgent}); err == nil {
		taskID = task.taskID
	}
	u := r.app.Teacher.Decide(in)
	r.app.Log.InfoCtx(ctx, "agent", "agent_decision", map[string]any{"task_id": taskID, "student_id": r.studentID, "lesson_id": r.lessonID, "trigger": in.Event, "has_utterance": u != nil})
	if u == nil {
		return nil
	}
	text := r.app.Teacher.Render(in, u)
	result, err := r.app.callAgentTool(ctx, "classroom.say", map[string]any{"text": text, "category": u.Category})
	if err != nil {
		r.app.Log.ErrorCtx(ctx, "agent", "agent_action_failed", map[string]any{"lesson_id": r.lessonID, "tool": "classroom.say", "error": err.Error()})
		return nil
	}
	m, ok := result.(Msg)
	if !ok {
		r.app.Log.ErrorCtx(ctx, "agent", "agent_action_failed", map[string]any{"lesson_id": r.lessonID, "tool": "classroom.say", "error": "invalid_result"})
		return nil
	}
	r.teacherText = text
	m["seq"] = r.nextSeqLocked()
	r.emitLocked(m)
	if u.Category == "encourage" {
		r.encourageCounter = 0
	}
	r.app.Log.InfoCtx(ctx, "classroom", "teacher_say", map[string]any{"task_id": taskID, "student_id": r.studentID, "lesson_id": r.lessonID, "category": u.Category, "text_length": len([]rune(text)), "fallback": text == u.Fallback})
	return []Msg{m}
}

func (r *Room) teacherTaskLocked() (context.Context, error) {
	// Room turn is the causal trace; each group receives one shared call budget.
	ctx := obs.WithTrace(context.Background(), r.contextLocked().Epoch)
	return r.app.newAgentTaskContext(ctx, r.studentID, r.bookID, r.lessonID, "classroom_event", tools.RoleTeacherAgent)
}

func (r *Room) teacherLifecycleLocked(event string) []Msg {
	ctx, err := r.teacherTaskLocked()
	if err != nil {
		r.app.Log.Error("agent", "agent_action_failed", map[string]any{"lesson_id": r.lessonID, "error": err.Error()})
		return nil
	}
	return r.teacherUtteranceLocked(ctx, r.teacherInputLocked(event, 0))
}

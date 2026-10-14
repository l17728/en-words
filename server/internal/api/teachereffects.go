package api

import (
	"context"
	"encoding/json"
	"enwords/internal/classroom"
	"enwords/internal/tools"
	"fmt"
)

type teacherEffectsKey struct{}
type teacherEffects struct {
	task    *agentTask
	actions []classroom.Action
	state   []byte
}

// withTeacherEffects preserves the existing event budget; it never creates a
// new task or budget. Call while holding the Room lock with a detached Snapshot.
func withTeacherEffects(ctx context.Context, actions []classroom.Action, state classroom.State) (context.Context, error) {
	t, err := taskFromContext(ctx, tools.Caller{Role: tools.RoleTeacherAgent})
	if err != nil {
		return nil, err
	}
	copyActions := append([]classroom.Action(nil), actions...)
	for i := range copyActions {
		copyActions[i].Meanings = append([]string(nil), copyActions[i].Meanings...)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, teacherEffectsKey{}, &teacherEffects{task: t, actions: copyActions, state: raw}), nil
}

func (a *App) registerTeacherEffects(reg *tools.Registry) error {
	for _, name := range []string{"classroom.show_word", "classroom.play_tts", "classroom.start_repeat", "classroom.start_quiz", "classroom.judge_answer", "classroom.start_review", "classroom.show_feedback_ui", "classroom.get_state"} {
		name := name
		if err := reg.Register(tools.Tool{Name: name, Domain: "classroom", Risk: tools.RiskLow, Scope: []tools.Role{tools.RoleTeacherAgent}, InputSchema: mustSchema(`{"type":"object","properties":{"index":{"type":"integer"}},"required":["index"],"additionalProperties":false}`), Handler: func(ctx context.Context, c tools.Caller, args tools.Args) (any, error) {
			t, err := taskFromContext(ctx, c)
			if err != nil {
				return nil, err
			}
			f, ok := ctx.Value(teacherEffectsKey{}).(*teacherEffects)
			if !ok || f.task != t || t.app != a || t.trigger != "classroom_event" {
				return nil, fmt.Errorf("frozen classroom facts required")
			}
			var in struct {
				Index *int `json:"index"`
			}
			if err := strictToolArgs(args, &in); err != nil {
				return nil, err
			}
			if in.Index == nil {
				return nil, fmt.Errorf("effect index required")
			}
			if name == "classroom.get_state" {
				if *in.Index != -1 {
					return nil, fmt.Errorf("state index must be -1")
				}
				var state classroom.State
				if err := json.Unmarshal(f.state, &state); err != nil {
					return nil, err
				}
				return state, nil
			}
			if *in.Index < 0 || *in.Index >= len(f.actions) {
				return nil, fmt.Errorf("effect index out of range")
			}
			plans, err := a.Teacher.PlanEffects(f.actions)
			if err != nil {
				return nil, err
			}
			if plans[*in.Index].Tool != name {
				return nil, fmt.Errorf("effect capability mismatch")
			}
			out := f.actions[*in.Index]
			out.Meanings = append([]string(nil), out.Meanings...)
			return out, nil
		}}); err != nil {
			return err
		}
	}
	return nil
}

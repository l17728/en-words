package config

import (
	"context"
	"fmt"
)

type Mutation struct {
	Operation string `json:"operation"`
	Actor     string `json:"-"`
	Scope     Scope  `json:"scope"`
	StudentID int64  `json:"studentId"`
	Key       string `json:"key"`
	Value     any    `json:"value"`
	Reason    string `json:"reason"`
}
type MutationResult struct {
	ChangedCards     int64 `json:"changed_cards"`
	AffectedStudents int64 `json:"affected_students"`
	Noop             bool  `json:"noop"`
	Committed        bool  `json:"committed"`
	RefreshPending   bool  `json:"refresh_pending"`
}
type MutationHandler func(context.Context, Mutation) (MutationResult, error)

func SensitiveKey(key string) bool { return key == "ladder" || key == "goalMode" }

// Bind only during assembly, before the Store becomes visible to other goroutines.
func (s *Store) BindAtomicMutationHandler(h MutationHandler) error {
	if h == nil || s.mutationHandler != nil {
		return fmt.Errorf("configuration coordinator already bound or absent")
	}
	s.mutationHandler = h
	s.paired = true
	return nil
}
func (s *Store) RequireAtomicMutationHandler() { s.paired = true }
func ValidateMutation(m Mutation) error {
	if m.Scope != ScopeGlobal && m.Scope != ScopeStudent {
		return fmt.Errorf("invalid configuration scope")
	}
	if (m.Scope == ScopeGlobal && m.StudentID != 0) || (m.Scope == ScopeStudent && m.StudentID <= 0) {
		return fmt.Errorf("invalid configuration target")
	}
	if m.Operation == "delete_override" {
		if m.Scope != ScopeStudent {
			return fmt.Errorf("override requires student")
		}
		if _, ok := specs[m.Key]; !ok {
			return fmt.Errorf("unknown configuration key")
		}
		return nil
	}
	if m.Operation != "set" || isBlank(m.Reason) {
		return fmt.Errorf("configuration operation and reason required")
	}
	_, err := canon(m.Key, m.Value)
	return err
}
func (s *Store) ApplyContext(ctx context.Context, m Mutation) (MutationResult, error) {
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if err := ValidateMutation(m); err != nil {
		return MutationResult{}, err
	}
	if s.mutationHandler != nil {
		return s.mutationHandler(ctx, m)
	}
	if s.paired && SensitiveKey(m.Key) {
		return MutationResult{}, fmt.Errorf("atomic_configuration_unavailable")
	}
	noop := m.Operation == "delete_override" && !s.HasOverride(m.StudentID, m.Key)
	var err error
	if m.Operation == "delete_override" {
		err = s.DeleteOverride(m.Actor, m.StudentID, m.Key)
	} else {
		err = s.set(m.Actor, m.Scope, m.StudentID, m.Key, m.Value, m.Reason)
	}
	return MutationResult{Noop: noop}, err
}
func (s *Store) HasOverride(sid int64, key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.overrides[sid][key]
	return ok
}

// CopyValues is a private-work candidate, with no persistence, audit, or handlers.
func (s *Store) CopyValues() *Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, _ := NewStore(nil, nil)
	for k, v := range s.globals {
		n.globals[k] = cloneValue(v)
	}
	for id, vs := range s.overrides {
		n.overrides[id] = map[string]any{}
		for k, v := range vs {
			n.overrides[id][k] = cloneValue(v)
		}
	}
	return n
}

type EffectiveField struct {
	Effective              any      `json:"effective"`
	Source                 string   `json:"source"`
	HasOverride            bool     `json:"has_override"`
	Override               any      `json:"override,omitempty"`
	Fallback               any      `json:"fallback,omitempty"`
	FallbackSource         string   `json:"fallback_source,omitempty"`
	DerivedFrom            string   `json:"derived_from,omitempty"`
	GoalMode               string   `json:"goal_mode,omitempty"`
	GoalModeSource         string   `json:"goal_mode_source,omitempty"`
	FallbackGoalMode       string   `json:"fallback_goal_mode,omitempty"`
	FallbackGoalModeSource string   `json:"fallback_goal_mode_source,omitempty"`
	Revision               Revision `json:"revision"`
}
type EffectiveConfig struct {
	StudentID int64          `json:"student_id"`
	GoalMode  EffectiveField `json:"goalMode"`
	Ladder    EffectiveField `json:"ladder"`
}

func (s *Store) valueSource(sid int64, key string) string {
	if _, ok := s.overrides[sid][key]; sid > 0 && ok {
		return "student"
	}
	if _, ok := s.globals[key]; ok {
		return "global"
	}
	return "default"
}

// Call on a frozen work Store: the DTO and both dependency revisions share its transaction.
func (s *Store) EffectiveView(sid int64) (EffectiveConfig, error) {
	out := EffectiveConfig{StudentID: sid}
	for _, key := range []string{"goalMode", "ladder"} {
		s.mu.RLock()
		field := EffectiveField{Effective: cloneValue(s.effective(sid, key)), Source: s.valueSource(sid, key)}
		if sid > 0 {
			v, ok := s.overrides[sid][key]
			field.HasOverride = ok
			if ok {
				field.Override = cloneValue(v)
			}
		}
		if key == "ladder" && field.Source == "default" {
			field.DerivedFrom = "goalMode"
			field.GoalMode = s.effectiveGoalMode(sid)
			field.GoalModeSource = s.valueSource(sid, "goalMode")
		}
		s.mu.RUnlock()
		if field.HasOverride {
			candidate := s.CopyValues()
			delete(candidate.overrides[sid], key)
			field.Fallback = cloneValue(candidate.effective(sid, key))
			field.FallbackSource = candidate.valueSource(sid, key)
			if key == "ladder" && field.FallbackSource == "default" {
				field.FallbackGoalMode = candidate.effectiveGoalMode(sid)
				field.FallbackGoalModeSource = candidate.valueSource(sid, "goalMode")
			}
		}
		rev, err := s.Revisions(sid, key)
		if err != nil {
			return out, err
		}
		field.Revision = rev
		if key == "ladder" {
			out.Ladder = field
		} else {
			out.GoalMode = field
		}
	}
	return out, nil
}

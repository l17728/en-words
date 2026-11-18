package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"enwords/internal/agent"
	"enwords/internal/classroom"
	"enwords/internal/scheduler"
)

type lessonStep struct {
	Index               int
	Kind                string
	Delta               int
	MemoryKind, Content string
	Importance          int
}

type lessonAgentPlan struct {
	Steps  []lessonStep
	Policy scheduler.PlanPolicy
	Calls  int
}

// Compile before any mutation. Every accepted input action remains in Steps,
// including the policy action whose effect is part of the scheduling tool.
func compileLessonPlan(actions []agent.SystemAction) (lessonAgentPlan, error) {
	p := lessonAgentPlan{}
	counts := map[string]int{}
	for i, action := range actions {
		s := lessonStep{Index: i, Kind: action.Kind}
		counts[action.Kind]++
		raw, err := json.Marshal(action.Params)
		if err != nil {
			return p, fmt.Errorf("action %d: invalid parameters", i)
		}
		if string(raw) == "null" {
			raw = []byte("{}")
		}
		switch action.Kind {
		case "plan_next", "report_push", "reply_comfort":
			var empty struct{}
			if err := strictToolArgs(raw, &empty); err != nil {
				return p, err
			}
			p.Calls++
		case "adjust_words_down":
			var in struct {
				Delta int `json:"delta"`
			}
			if err := strictToolArgs(raw, &in); err != nil || in.Delta != -10 {
				return p, fmt.Errorf("invalid words adjustment")
			}
			s.Delta = in.Delta
			p.Calls += 2 // Read current value and write the frozen absolute target.
		case "adjust_new_ratio":
			var in struct {
				Ratio float64 `json:"ratio"`
			}
			if err := strictToolArgs(raw, &in); err != nil || in.Ratio <= 0 || in.Ratio > 1 || math.IsNaN(in.Ratio) || math.IsInf(in.Ratio, 0) {
				return p, fmt.Errorf("invalid planning ratio")
			}
			p.Policy.MaxNewRatio = in.Ratio
		case "memory_append":
			var in struct {
				Kind       string `json:"kind"`
				Content    string `json:"content"`
				Importance int    `json:"importance"`
			}
			if err := strictToolArgs(raw, &in); err != nil || in.Kind != "performance" || in.Content == "" || in.Importance < 1 || in.Importance > 5 {
				return p, fmt.Errorf("invalid lesson memory")
			}
			s.MemoryKind, s.Content, s.Importance = in.Kind, in.Content, in.Importance
			p.Calls++
		default:
			return p, fmt.Errorf("unsupported_action: %s", action.Kind)
		}
		if counts[action.Kind] > 1 && action.Kind != "memory_append" {
			return p, fmt.Errorf("duplicate lesson action: %s", action.Kind)
		}
		p.Steps = append(p.Steps, s)
	}
	if counts["plan_next"] != 1 || counts["report_push"] != 1 {
		return p, fmt.Errorf("lesson plan requires scheduling and publication")
	}
	if p.Calls > 8 {
		return p, fmt.Errorf("lesson tool budget exceeded before execution")
	}
	order := map[string]int{"adjust_words_down": 0, "memory_append": 1, "adjust_new_ratio": 2, "plan_next": 3, "reply_comfort": 4, "report_push": 5}
	sort.SliceStable(p.Steps, func(i, j int) bool { return order[p.Steps[i].Kind] < order[p.Steps[j].Kind] })
	return p, nil
}

type lessonCompletion struct {
	LessonID, StudentID, BookID int64
	Fingerprint                 string
	Result                      *classroom.Result
	State                       classroom.State
	Plan                        lessonAgentPlan
	Done                        map[int]any
	Target                      *int
	Settled                     bool
	At                          time.Time
	Replies                     []string
	Next                        *lessonScheduleResult
}

func freezeLessonInput(res *classroom.Result, st classroom.State) (*classroom.Result, classroom.State, string, error) {
	if res == nil {
		return nil, st, "", fmt.Errorf("lesson result required")
	}
	raw, err := json.Marshal(struct {
		Result *classroom.Result
		State  classroom.State
	}{res, st})
	if err != nil {
		return nil, st, "", err
	}
	var frozen struct {
		Result *classroom.Result
		State  classroom.State
	}
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, st, "", err
	}
	hash := sha256.Sum256(raw)
	return frozen.Result, frozen.State, hex.EncodeToString(hash[:]), nil
}

func validateLessonResult(lesson *scheduler.Lesson, sid, bid int64, res *classroom.Result) error {
	if lesson.StudentID != sid || lesson.BookID != bid {
		return fmt.Errorf("settlement owner mismatch")
	}
	words := map[int64]bool{}
	for _, wid := range lesson.BatchWordIDs {
		words[wid] = true
	}
	for wid := range res.Correct {
		if !words[wid] {
			return fmt.Errorf("result contains foreign word")
		}
	}
	if res.Report.FirstTryRate < 0 || res.Report.FirstTryRate > 1 || math.IsNaN(res.Report.FirstTryRate) || res.Report.AvgAnswerMs < 0 {
		return fmt.Errorf("invalid lesson facts")
	}
	return nil
}

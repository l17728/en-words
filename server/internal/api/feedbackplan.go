package api

import (
	"encoding/json"
	"enwords/internal/agent"
	"enwords/internal/config"
	"enwords/internal/feedback"
	"enwords/internal/store"
	"enwords/internal/wordbook"
	"errors"
	"sort"
	"strings"
	"time"
)

type feedbackWordTarget struct {
	BookID int64 `json:"book_id"`
	WordID int64 `json:"word_id"`
}
type feedbackFrozenIntent struct {
	Index            int                  `json:"index"`
	Kind             string               `json:"kind"`
	Mode             string               `json:"mode"`
	Key              string               `json:"key,omitempty"`
	Target           int                  `json:"target,omitempty"`
	Before           int                  `json:"before,omitempty"`
	Revision         config.Revision      `json:"revision"`
	DependsOn        int                  `json:"depends_on"`
	LessonID         int64                `json:"lesson_id,omitempty"`
	BookID           int64                `json:"book_id,omitempty"`
	OriginalAt       time.Time            `json:"original_at"`
	TargetAt         time.Time            `json:"target_at"`
	Words            []feedbackWordTarget `json:"words,omitempty"`
	Word             string               `json:"word,omitempty"`
	Failure          string               `json:"failure,omitempty"`
	Calls            []string             `json:"calls"`
	MemoryKind       string               `json:"memory_kind,omitempty"`
	MemoryContent    string               `json:"memory_content,omitempty"`
	MemoryImportance int                  `json:"memory_importance,omitempty"`
}

func (w *feedbackWork) books() *wordbook.Service {
	if w.db != nil {
		return wordbook.NewService(store.NewWordbookRepo(w.db))
	}
	return w.app.Books
}
func compileFeedbackPlan(parsed feedback.ParseResult, acts []agent.SystemAction) ([]feedbackFrozenIntent, error) {
	out := make([]feedbackFrozenIntent, len(parsed.Intents))
	seen := make([]bool, len(out))
	replies := make([]bool, len(out))
	memories := make([]bool, len(out))
	for i, it := range parsed.Intents {
		out[i] = feedbackFrozenIntent{Index: i, Kind: string(it.Kind), DependsOn: -1}
	}
	for _, act := range acts {
		if act.IntentIndex < 0 || act.IntentIndex >= len(out) {
			return nil, feedbackErr(400, "invalid_plan", false)
		}
		f := &out[act.IntentIndex]
		switch act.Kind {
		case "adjust_words", "adjust_frequency":
			if act.Kind == "adjust_frequency" && f.Kind != string(feedback.IntentFrequency) {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if act.Kind == "adjust_words" && f.Kind != string(feedback.IntentReduce) && f.Kind != string(feedback.IntentIncrease) {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if v, ok := act.Params["dir"]; ok {
				if s, ok := v.(string); !ok || s != f.Kind {
					return nil, feedbackErr(400, "invalid_plan", false)
				}
			}
			allowed := map[string]bool{"target": true}
			if act.Kind == "adjust_words" {
				allowed["dir"] = true
			}
			for key := range act.Params {
				if !allowed[key] {
					return nil, feedbackErr(400, "invalid_plan", false)
				}
			}
			if seen[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			seen[f.Index] = true
			f.Mode = "config"
			f.Key = "wordsPerLesson"
			if act.Kind == "adjust_frequency" {
				f.Key = "lessonsPerDay"
			}
			n, ok := act.Params["target"].(int)
			if !ok {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			f.Target = n
			f.Calls = []string{"config.set_student", "feedback.reply"}
		case "postpone":
			if f.Kind != string(feedback.IntentPostpone) {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if len(act.Params) != 1 {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if seen[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			seen[f.Index] = true
			f.Mode = "postpone"
			date, ok := act.Params["date"].(string)
			if !ok {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			t, err := time.Parse("2006-01-02", date)
			if err != nil {
				f.Failure = "invalid_date"
			}
			f.TargetAt = t
			f.Calls = []string{"scheduler.postpone_lesson", "memory.append", "feedback.reply"}
		case "boost":
			if f.Kind != string(feedback.IntentHardWord) {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if len(act.Params) != 1 {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if seen[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			seen[f.Index] = true
			f.Mode = "boost"
			f.Word = str(act.Params["word"])
			if strings.TrimSpace(f.Word) == "" {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			f.Calls = []string{"scheduler.boost_word", "feedback.reply"}
		case "escalate":
			clarification := len(act.Params) == 1 && act.Params["clarification"] == true
			if f.Kind != string(feedback.IntentComplaint) && !clarification {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if len(act.Params) != 0 && !clarification {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if seen[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			seen[f.Index] = true
			f.Mode = "escalate"
			f.Calls = []string{"feedback.escalate", "feedback.reply"}
		case "reply_needconfirm", "reply_clarify":
			if len(act.Params) != 0 {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if seen[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			seen[f.Index] = true
			f.Mode = "clarify"
			if act.Kind == "reply_needconfirm" {
				f.Mode = "need_confirm"
			}
			f.Calls = []string{"feedback.reply"}
		case "memory_append":
			if f.Mode != "postpone" || memories[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			f.MemoryKind = str(act.Params["kind"])
			f.MemoryContent = str(act.Params["content"])
			n, ok := act.Params["importance"].(int)
			if !ok || n < 1 || n > 5 || strings.TrimSpace(f.MemoryKind) == "" || strings.TrimSpace(f.MemoryContent) == "" || len(act.Params) != 3 {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			f.MemoryImportance = n
			memories[f.Index] = true
		case "reply_applied", "reply_escalated", "reply_comfort":
			if len(act.Params) != 0 {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			if !seen[f.Index] || replies[f.Index] {
				return nil, feedbackErr(400, "invalid_plan", false)
			}
			replies[f.Index] = true
		default:
			return nil, feedbackErr(400, "unknown_action", false)
		}
	}
	calls := 0
	for i, f := range out {
		if !seen[i] {
			return nil, feedbackErr(400, "invalid_plan", false)
		}
		if f.Mode == "postpone" && !memories[i] {
			return nil, feedbackErr(400, "invalid_plan", false)
		}
		calls += len(f.Calls)
	}
	if len(out) == 0 || calls > 8 {
		return nil, feedbackErr(400, "plan_budget_exceeded", false)
	}
	return out, nil
}
func (w *feedbackWork) freezeFeedbackPlan(plan []feedbackFrozenIntent) error {
	if err := w.claimClarificationParent(); err != nil {
		return err
	}
	last := map[string]int{}
	virtual := map[string]int{}
	revs := map[string]config.Revision{}
	for i := range plan {
		f := &plan[i]
		switch f.Mode {
		case "config":
			if prev, ok := last[f.Key]; ok {
				f.DependsOn = prev
				f.Before = virtual[f.Key]
				f.Revision = revs[f.Key]
			} else {
				f.Before = w.cfg.Int(w.task.StudentID, f.Key)
				var err error
				f.Revision, err = w.cfg.Revisions(w.task.StudentID, f.Key)
				if err != nil {
					return err
				}
			}
			if f.Key == "wordsPerLesson" {
				var err error
				f.Target, err = agent.ResolveFeedbackWords(f.Kind, f.Before, f.Target)
				if err != nil {
					return feedbackErr(400, "invalid_plan", false)
				}
			}
			if (f.Key == "wordsPerLesson" && (f.Target < 50 || f.Target > 100)) || (f.Key == "lessonsPerDay" && (f.Target < 1 || f.Target > 2)) {
				return feedbackErr(400, "invalid_plan", false)
			}
			last[f.Key] = i
			virtual[f.Key] = f.Target
			next := f.Revision
			next.Student++
			revs[f.Key] = next
		case "postpone":
			if f.Failure != "" {
				continue
			}
			lessons, err := w.sched.StudentLessons(w.task.StudentID)
			if err != nil {
				return err
			}
			sort.Slice(lessons, func(i, j int) bool {
				if lessons[i].OpenAt.Equal(lessons[j].OpenAt) {
					return lessons[i].ID < lessons[j].ID
				}
				return lessons[i].OpenAt.Before(lessons[j].OpenAt)
			})
			for _, l := range lessons {
				if l.Status == "scheduled" {
					f.LessonID, f.BookID, f.OriginalAt = l.ID, l.BookID, l.OpenAt
					break
				}
			}
			if f.LessonID == 0 {
				f.Failure = "lesson_not_found"
				continue
			}
			loc := f.OriginalAt.Location()
			f.TargetAt = time.Date(f.TargetAt.Year(), f.TargetAt.Month(), f.TargetAt.Day(), f.OriginalAt.Hour(), f.OriginalAt.Minute(), f.OriginalAt.Second(), 0, loc)
			if !f.TargetAt.After(f.OriginalAt) || !f.TargetAt.After(w.task.CreatedAt) || f.TargetAt.After(w.task.CreatedAt.In(loc).AddDate(0, 0, 7)) {
				f.Failure = "invalid_date"
			}
		case "boost":
			ids, err := w.sched.StudentBooks(w.task.StudentID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				b, err := w.books().GetBook(id)
				if err != nil {
					if errors.Is(err, wordbook.ErrBookNotFound) {
						f.Failure = "book_unavailable"
						continue
					}
					return err
				}
				if b.Status != wordbook.StatusPublished {
					continue
				}
				entry, err := w.books().EntryByKey(id, normWord(f.Word))
				if err != nil {
					if errors.Is(err, wordbook.ErrEntryNotFound) {
						continue
					}
					return err
				}
				f.Words = append(f.Words, feedbackWordTarget{id, entry.ID})
			}
			if len(f.Words) == 0 {
				f.Failure = "word_not_enrolled"
			}
		}
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	w.task.PlanJSON = string(raw)
	w.task.ErrorCode = ""
	w.task.Retryable = false
	w.intents = nil
	for _, f := range plan {
		b, _ := json.Marshal(f)
		result := FeedbackIntentResult{Index: f.Index, Kind: f.Kind, Status: "pending", ResultCode: "pending", Retryable: true}
		rb, _ := json.Marshal(result)
		w.intents = append(w.intents, store.FeedbackIntent{TaskID: w.task.TaskID, IntentIndex: f.Index, Kind: f.Kind, FrozenJSON: string(b), ResultJSON: string(rb), Status: "pending"})
	}
	return nil
}
func decodeFrozen(it store.FeedbackIntent) (feedbackFrozenIntent, error) {
	var f feedbackFrozenIntent
	err := json.Unmarshal([]byte(it.FrozenJSON), &f)
	if err != nil || len(f.Calls) == 0 || f.Index != it.IntentIndex || f.Index < 0 || f.DependsOn < -1 || f.DependsOn >= f.Index {
		err = feedbackErr(400, "invalid_plan", false)
	}
	return f, err
}

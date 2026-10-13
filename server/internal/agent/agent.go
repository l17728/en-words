// Package agent 实现双 Agent Mock 运行时（04 §7 / SDD §4）：
// TeacherAgent = 课堂话术层（快路径伴生；按决策表 T-01..T-20 选槽，
//
//	从话术库 seed=lessonId 确定性渲染；预取超时降级模板原文）。
//
// SystemAgent = 事务决策表 S-01..S-15（结课评估/反馈处理/记忆读写）。
// Mock=确定性脚本；接口与真实 LLM 版（智谱 GLM）一致，切换零改码。
package agent

import (
	"strings"

	"enwords/internal/classroom"
	"enwords/internal/config"
	"enwords/internal/feedback"
	"enwords/internal/memory"
	"enwords/internal/scripts"
)

// ---- TeacherAgent（话术层 Mock）----

// TeacherInput 课堂事件上下文（SDD §4.2：有界 token）。
type TeacherInput struct {
	LessonID        int64
	StudentName     string
	Event           string // classroom Ev* / phase_timeout / state_restored
	Phase           string
	Word            classroom.Word
	CorrectLen      int // 本课已答对数（streak 话术变量）
	StreakBest      int
	EncourageN      int // 自上次鼓励完成的词数
	EncourageEveryN int // 课堂冻结配置，不从运行时配置重读
	TotalWords      int
	ReteachCount    int
}

// Utterance 一句话术。
type Utterance struct {
	Category string // scripts 分类
	Text     string // 渲染成品（空=由调用方异步渲染）
	Fallback string // 降级模板原文（预取超时时直接用）
	Vars     map[string]any
}

// TeacherAgent Mock 决策表（04 §2 T-01..T-20 的话术槽映射）。
type TeacherAgent struct {
	bank *scripts.Bank
}

func NewTeacherAgent(bank *scripts.Bank) *TeacherAgent {
	if bank == nil {
		bank = scripts.NewBank()
		bank.SeedAll()
	}
	return &TeacherAgent{bank: bank}
}

// Decide 事件 → 话术槽（确定性；无 LLM）。返回 nil 表示该事件无话术。
func (a *TeacherAgent) Decide(in TeacherInput) (u *Utterance) {
	defer func() {
		if u != nil && u.Fallback == "" {
			u.Fallback = map[string]string{"open": "我是叮当老师，我们开始今天的学习。", "transition": "接下来继续学习，一步一步来。", "encourage": "认真作答做得很好，继续加油。", "correct": "没关系，我们再看一遍这个词。", "nudge": "慢慢来，准备好了再试一次。", "closing": "今天的课堂完成了，辛苦啦！课报已经准备好了。"}[u.Category]
		}
	}()
	base := map[string]any{"name": in.StudentName, "word": in.Word.Word, "n": in.CorrectLen}
	switch in.Event {
	case "lesson_started":
		base["n"] = in.TotalWords
		return &Utterance{Category: "open", Vars: base}
	case "word_done":
		// T-04：30% 语气由 seed 决定——Mock 用 lessonId 奇偶模拟确定性交替
		if in.LessonID%3 == 0 {
			return &Utterance{Category: "transition", Vars: base}
		}
		return nil
	case "start_quiz": // final 唯一测试开始（D-21：教学中零测试，教完才测）
		return &Utterance{Category: "transition", Vars: base}
	case "judged_correct":
		// 鼓励密度：EncourageEveryN 由调用方传入计数（快路径在每次判对后调）
		if in.EncourageEveryN > 0 && in.EncourageN >= in.EncourageEveryN {
			return &Utterance{Category: "encourage", Vars: merge(base, map[string]any{"n": in.StreakBest})}
		}
		return nil
	case "judged_wrong":
		return &Utterance{Category: "correct",
			Vars: merge(base, map[string]any{"meaning": firstMeaning(in.Word), "n": in.ReteachCount})}
	case "repeat_timeout":
		return &Utterance{Category: "nudge", Vars: base}
	case "answer_timeout":
		return &Utterance{Category: "nudge", Vars: base}
	case "lesson_end":
		// No comparison with an unknown previous lesson, or unsupported claim that
		// all words were mastered. This is emitted only after settlement commits.
		return &Utterance{Category: "closing", Text: "今天的课堂完成了，辛苦啦！课报已经准备好了。", Vars: base}
	case "state_restored":
		return &Utterance{Category: "nudge", Vars: base}
	}
	return nil
}

// Render 渲染话术（seed=lessonId 确定性；渲染失败降级 Fallback 原文）。
func (a *TeacherAgent) Render(in TeacherInput, u *Utterance) string {
	if u == nil {
		return ""
	}
	if u.Text != "" {
		return u.Text
	}
	rng := scripts.NewSeededRNG(in.LessonID)
	// These openers assume previous results or student difficulty we do not know.
	text := a.bank.Render(rng, u.Category, u.Vars, map[string]bool{"OP-2": true, "OP-4": true, "OP-5": true, "OP-6": true})
	if text == "" || len([]rune(text)) > 60 || strings.ContainsAny(text, "{}") {
		return u.Fallback
	}
	return text
}

// ---- SystemAgent（事务 Mock）----

// SystemInput 触发上下文。
type SystemInput struct {
	Trigger   string // lesson_finished | feedback_submitted | cron_overdue | cron_daily
	StudentID int64
	BookID    int64
	// lesson_finished
	CorrectRate    float64
	AvgAnswerMs    int
	OverdueLessons int
	// feedback
	FeedbackText   string
	ParsedFeedback *feedback.ParseResult         // Parse once; execution and audit use identical candidates.
	Now            func() (year, month, day int) // 简化日期注入（测试可控）
}

// SystemAction 系统动作（工具调用序列的抽象；由装配层映射到具体工具）。
type SystemAction struct {
	Kind string // plan_normal | plan_stall | adjust_words_down | suggest_up_needconfirm |
	//         postpone | boost | escalate | reply_* | memory_append | memory_recall
	Params    map[string]any
	ReplyCat  string // 非空=需渲染回复话术（scripts 分类）
	ReplyVars map[string]any
}

// SystemAgent Mock 决策表（04 §4 S-01..S-15）。
type SystemAgent struct {
	Bank           *scripts.Bank
	Memory         *memory.Service
	feedbackParser *feedback.Parser
}

func NewSystemAgent(bank *scripts.Bank, mem *memory.Service, fp *feedback.Parser) *SystemAgent {
	if bank == nil {
		bank = scripts.NewBank()
		bank.SeedAll()
	}
	return &SystemAgent{Bank: bank, Memory: mem, feedbackParser: fp}
}

// Decide 事务决策。
func (a *SystemAgent) Decide(in SystemInput) []SystemAction {
	switch in.Trigger {
	case "lesson_finished":
		return a.decideLessonFinished(in)
	case "feedback_submitted":
		return a.decideFeedback(in)
	case "cron_overdue":
		if in.OverdueLessons >= 3 {
			return []SystemAction{{Kind: "suggest_pause", ReplyCat: "feedback_reply",
				ReplyVars: map[string]any{"name": ""}}}
		}
		return []SystemAction{{Kind: "postpone"}, {Kind: "notify_replan",
			ReplyCat: "system_notify", ReplyVars: map[string]any{}}}
	}
	return nil
}

// decideLessonFinished S-01..S-04 + S-13~S-15（课报审视）。
// 正确率三档互斥；慢学生调整为独立维度（低且慢双触发）。
func (a *SystemAgent) decideLessonFinished(in SystemInput) []SystemAction {
	acts := []SystemAction{{Kind: "plan_next"}}
	switch {
	case in.CorrectRate >= 0.85:
		acts = append(acts, SystemAction{Kind: "memory_append", Params: map[string]any{
			"kind": "performance", "content": "正确率高", "importance": 3}})
	case in.CorrectRate < 0.6: // S-03：降档 + 回复安慰建议
		acts = append(acts,
			SystemAction{Kind: "adjust_words_down", Params: map[string]any{"delta": -10}},
			SystemAction{Kind: "memory_append", Params: map[string]any{
				"kind": "performance", "content": "正确率低，已降量", "importance": 4}},
			SystemAction{Kind: "reply_comfort", ReplyCat: "report"})
	}
	if in.AvgAnswerMs > 15000 { // S-04 慢学生（独立维度）
		acts = append(acts, SystemAction{Kind: "adjust_new_ratio", Params: map[string]any{"ratio": 0.4}})
	}
	// S-15 课报生成推送
	acts = append(acts, SystemAction{Kind: "report_push", ReplyCat: "report"})
	return acts
}

// decideFeedback S-05..S-12。
func (a *SystemAgent) decideFeedback(in SystemInput) []SystemAction {
	if a.feedbackParser == nil {
		return []SystemAction{{Kind: "escalate"}}
	}
	var res feedback.ParseResult
	if in.ParsedFeedback != nil {
		res = *in.ParsedFeedback
	} else {
		res = a.feedbackParser.Parse(in.FeedbackText)
	}
	plans := a.feedbackParser.Plan(res)
	var acts []SystemAction
	for _, p := range plans {
		switch p.Action {
		case "apply_config":
			if p.Intent.Kind == feedback.IntentFrequency {
				acts = append(acts, SystemAction{Kind: "adjust_frequency", Params: map[string]any{"target": p.Intent.LessonsPerDay}})
				continue
			}
			acts = append(acts, SystemAction{Kind: "adjust_words",
				Params: map[string]any{"target": p.Intent.TargetWords, "dir": string(p.Intent.Kind)}})
		case "need_confirm":
			acts = append(acts, SystemAction{Kind: "reply_needconfirm", ReplyCat: "feedback_reply",
				ReplyVars: map[string]any{"n": p.Intent.TargetWords}})
		case "postpone":
			d := ""
			if p.Intent.Date != nil {
				d = p.Intent.Date.Format("2006-01-02")
			}
			acts = append(acts,
				SystemAction{Kind: "postpone", Params: map[string]any{"date": d}},
				SystemAction{Kind: "memory_append", Params: map[string]any{
					"kind": "promise", "content": "学生请假 " + d, "importance": 4}},
				SystemAction{Kind: "reply_applied", ReplyCat: "feedback_reply",
					ReplyVars: map[string]any{"date": d, "newDate": d + "(改期)"}})
		case "boost":
			acts = append(acts,
				SystemAction{Kind: "boost", Params: map[string]any{"word": p.Intent.Word}},
				SystemAction{Kind: "reply_applied", ReplyCat: "feedback_reply",
					ReplyVars: map[string]any{"word": p.Intent.Word}})
		case "escalate":
			acts = append(acts, SystemAction{Kind: "escalate"}, SystemAction{Kind: "reply_escalated", ReplyCat: "feedback_reply"})
		case "clarify":
			acts = append(acts, SystemAction{Kind: "reply_clarify", ReplyCat: "feedback_reply"})
		}
	}
	return acts
}

// RenderReply 渲染回复话术（seed=StudentID 确定性）。
func (a *SystemAgent) RenderReply(studentID int64, act SystemAction) string {
	if act.ReplyCat == "" {
		return ""
	}
	rng := scripts.NewSeededRNG(studentID)
	return a.Bank.Render(rng, act.ReplyCat, act.ReplyVars, nil)
}

// ---- helpers ----

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func firstMeaning(w classroom.Word) string {
	if len(w.Meanings) > 0 {
		return strings.TrimSpace(w.Meanings[0])
	}
	return ""
}

// Ensure interface compliance / config 引用（避免未使用导入）。
var _ = config.DefaultLadder

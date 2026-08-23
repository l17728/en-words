// 反馈闭环流程（SDD §7 / FR-G / S-05..S-12）：
// 提交→解析→逐项应用（config/postpone/boost/memory）→回复通知→记录留痕。
package api

import (
	"regexp"
	"strings"
	"time"

	"enwords/internal/agent"
	"enwords/internal/memory"
	"enwords/internal/scheduler"
	"enwords/internal/scripts"
	"enwords/internal/wordbook"
)

// normWord 词文本归一化（与 wordbook.normKey 同口径）。
var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func normWord(w string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(strings.TrimSpace(w)), "")
}

// FeedbackRecord 反馈记录（G-5 状态跟踪）。
type FeedbackRecord struct {
	ID        int64     `json:"id"`
	StudentID int64     `json:"student_id"`
	Text      string    `json:"text"`
	Status    string    `json:"status"` // applied | need_confirm | escalated | clarify
	Intents   []string  `json:"intents"`
	Reply     string    `json:"reply"`
	CreatedAt time.Time `json:"created_at"`
}

// HandleFeedback 处理一条学生反馈（文字/语音同管线，G-2）。
func (a *App) HandleFeedback(studentID int64, text string) *FeedbackRecord {
	a.mu.Lock()
	a.nextFeedbackID++
	rec := &FeedbackRecord{ID: a.nextFeedbackID, StudentID: studentID,
		Text: text, Status: "processing", CreatedAt: a.Clock.Now()}
	a.mu.Unlock()

	acts := a.System.Decide(sysFeedbackInput(studentID, text))
	replyParts := []string{}
	status := "applied"
	for _, act := range acts {
		switch act.Kind {
		case "adjust_words":
			target, _ := act.Params["target"].(int)
			if target == 0 {
				// 无数字反馈（"太多了/多来点"）→ 相对调整 ±10（S-05）
				dir, _ := act.Params["dir"].(string)
				cur := a.Cfg.Int(studentID, "wordsPerLesson")
				if strings.Contains(dir, "increase") {
					target = cur + 10
				} else {
					target = cur - 10
				}
			}
			if target < 40 {
				target = 40
			}
			if target > 100 {
				target = 100
			}
			_ = a.Cfg.SetStudent("system_agent", studentID, "wordsPerLesson",
				target, "学生反馈自动调整(G-3)")
			// FR-1 精确模板：用实际生效值渲染（决策表出槽位，装配层注入终值）
			replyParts = append(replyParts, a.Bank.RenderID(
				scripts.NewSeededRNG(studentID), "FR-1",
				map[string]any{"n": target}))
		case "postpone":
			if a.applyPostpone(studentID, str(act.Params["date"])) {
				replyParts = append(replyParts, a.System.RenderReply(studentID, act))
			}
		case "boost":
			a.applyBoost(studentID, str(act.Params["word"]))
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		case "memory_append":
			a.applyMemoryAppend(studentID, act.Params)
		case "reply_needconfirm":
			status = "need_confirm"
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		case "escalate":
			status = "escalated"
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		case "reply_clarify":
			status = "clarify"
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		case "reply_comfort", "reply_applied", "reply_escalated":
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		}
	}
	if len(replyParts) == 0 {
		replyParts = append(replyParts, "收到！")
	}
	rec.Status = status
	rec.Reply = strings.Join(replyParts, " ")
	// 意图清单
	res := a.FB.Parse(text)
	for _, it := range res.Intents {
		rec.Intents = append(rec.Intents, string(it.Kind))
	}
	a.mu.Lock()
	a.feedbackRecs = append(a.feedbackRecs, *rec)
	a.mu.Unlock()
	a.Notify(studentID, "反馈处理结果", rec.Reply)
	return rec
}

// Feedbacks 学生反馈历史（G-5）。
func (a *App) Feedbacks(studentID int64) []FeedbackRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []FeedbackRecord
	for _, f := range a.feedbackRecs {
		if f.StudentID == studentID {
			out = append(out, f)
		}
	}
	return out
}

func sysFeedbackInput(sid int64, text string) agent.SystemInput {
	return agent.SystemInput{Trigger: "feedback_submitted", StudentID: sid, FeedbackText: text}
}

// applyPostpone 反馈 D：定位该生最近一节 scheduled 课 → 推迟到目标日同时段。
func (a *App) applyPostpone(studentID int64, dateStr string) bool {
	if dateStr == "" {
		return false
	}
	t, err := time.ParseInLocation("2006-01-02", dateStr, time.UTC)
	if err != nil {
		return false
	}
	lessons, _ := a.Sched.StudentLessons(studentID)
	var target *scheduler.Lesson
	for _, l := range lessons {
		if l.Status == scheduler.LessonScheduledStr {
			if target == nil || l.OpenAt.Before(target.OpenAt) {
				target = l
			}
		}
	}
	if target == nil {
		return false
	}
	// 目标日保留原时段
	newAt := time.Date(t.Year(), t.Month(), t.Day(),
		target.OpenAt.Hour(), target.OpenAt.Minute(), 0, 0, target.OpenAt.Location())
	if err := a.Sched.PostponeLesson(target.ID, newAt); err != nil {
		return false
	}
	a.Notify(studentID, "课程已改期", dateStr+" 见（你的请假申请）")
	return true
}

// applyBoost 反馈 E：词文本 → 跨库定位词条 → BoostFlag。
func (a *App) applyBoost(studentID int64, word string) {
	if strings.TrimSpace(word) == "" {
		return
	}
	books, _ := a.Books.ListBooks()
	for _, b := range books {
		if b.Status != wordbook.StatusPublished {
			continue
		}
		if e, err := a.Books.EntryByKey(b.ID, normWord(word)); err == nil {
			_ = a.Sched.BoostWord(studentID, b.ID, e.ID)
		}
	}
}

// applyMemoryAppend D-16：系统 Agent 把反馈决策写入任务/档案记忆。
func (a *App) applyMemoryAppend(studentID int64, params map[string]any) {
	kind := str(params["kind"])
	content := str(params["content"])
	if kind == "" || content == "" {
		return
	}
	imp := 3
	if v, ok := params["importance"].(int); ok {
		imp = v
	}
	// promise/performance 类挂学生档案（book 无关），教学笔记挂任务记忆
	_, _ = a.Mem.Append(memory.OwnerSystem, memory.ScopeStudentProfile,
		studentID, 0, kind, content, imp, 0)
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

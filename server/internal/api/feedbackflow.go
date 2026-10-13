// 反馈闭环流程（SDD §7 / FR-G / S-05..S-12）：
// 提交→解析→逐项应用（config/postpone/boost/memory）→回复通知→记录留痕。
package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"enwords/internal/agent"
	"enwords/internal/memory"
	"enwords/internal/scheduler"
	"enwords/internal/scripts"
	"enwords/internal/tools"
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
	return a.HandleFeedbackContext(context.Background(), studentID, text)
}

func (a *App) HandleFeedbackContext(parent context.Context, studentID int64, text string) *FeedbackRecord {
	a.mu.Lock()
	a.nextFeedbackID++
	rec := &FeedbackRecord{ID: a.nextFeedbackID, StudentID: studentID,
		Text: text, Status: "processing", CreatedAt: a.Clock.Now()}
	a.mu.Unlock()
	ctx, taskErr := a.newAgentTaskContext(parent, studentID, 0, 0, "feedback_submitted", tools.RoleSystemAgent)
	if taskErr != nil {
		rec.Status, rec.Reply = "failed", "反馈身份无效，本次未应用。"
		return rec
	}
	setConfig := func(key string, value any, reason string) error {
		_, err := a.callAgentTool(ctx, "config.set_student", map[string]any{"key": key, "value": value, "reason": reason})
		return err
	}
	a.Log.InfoCtx(ctx, "feedback", "feedback_submitted", map[string]any{
		"student_id": studentID, "text_length": len([]rune(text))})

	parsed := a.FB.Parse(text)
	in := sysFeedbackInput(studentID, text)
	in.ParsedFeedback = &parsed
	acts := a.System.Decide(in)
	replyParts := []string{}
	status := "applied"
	failed := false
	refreshPending := false
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
			if target < 50 {
				target = 50
			}
			if target > 100 {
				target = 100
			}
			if err := setConfig("wordsPerLesson", target, "学生反馈自动调整(G-3)"); err != nil {
				var committed *committedToolError
				if errors.As(err, &committed) {
					refreshPending = true
				} else {
					failed = true
					replyParts = append(replyParts, "学习量未能保存，请稍后重试。")
					continue
				}
			}
			// FR-1 精确模板：用实际生效值渲染（决策表出槽位，装配层注入终值）
			replyParts = append(replyParts, a.Bank.RenderID(
				scripts.NewSeededRNG(studentID), "FR-1",
				map[string]any{"n": target}))
		case "adjust_frequency":
			target, _ := act.Params["target"].(int)
			if err := setConfig("lessonsPerDay", target, "学生反馈调整频率"); err != nil {
				var committed *committedToolError
				if errors.As(err, &committed) {
					refreshPending = true
					replyParts = append(replyParts, fmt.Sprintf("每天 %d 节课的设置已保存。", target))
				} else {
					failed = true
					replyParts = append(replyParts, "学习频率未能保存，请稍后重试。")
				}
			} else {
				replyParts = append(replyParts, fmt.Sprintf("后续每天安排 %d 节课。", target))
			}
		case "postpone":
			if a.applyPostpone(studentID, str(act.Params["date"])) {
				replyParts = append(replyParts, a.System.RenderReply(studentID, act))
			} else {
				failed = true
				replyParts = append(replyParts, "未找到可改期的课程或日期不可用，本次未改期。")
			}
		case "boost":
			if err := a.applyBoost(studentID, str(act.Params["word"])); err != nil {
				failed = true
				replyParts = append(replyParts, "未能加强该词，请确认已选择包含该词的词库。")
				continue
			}
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		case "memory_append":
			if failed {
				continue
			}
			if _, err := a.callAgentTool(ctx, "memory.append", map[string]any{"scope": string(memory.ScopeStudentProfile), "kind": act.Params["kind"], "content": act.Params["content"], "importance": act.Params["importance"]}); err != nil {
				failed = true
				replyParts = append(replyParts, "学习备注未能保存。")
				continue
			}
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
			if failed {
				continue
			}
			replyParts = append(replyParts, a.System.RenderReply(studentID, act))
		}
	}
	if len(replyParts) == 0 {
		replyParts = append(replyParts, "收到！")
	}
	if failed {
		status = "failed"
	}
	if refreshPending {
		replyParts = append(replyParts, "设置已保存，但运行配置刷新失败；请勿重复提交调整，待服务恢复后检查设置。")
		if !failed {
			status = "applied_refresh_pending"
		}
	}
	rec.Status = status
	rec.Reply = strings.Join(replyParts, " ")
	a.Log.InfoCtx(ctx, "feedback", "feedback_handled", map[string]any{
		"student_id": studentID, "id": rec.ID, "status": rec.Status,
		"reply_length": len([]rune(rec.Reply))})
	// 意图清单
	res := parsed
	for _, it := range res.Intents {
		rec.Intents = append(rec.Intents, string(it.Kind))
	}
	a.mu.Lock()
	a.feedbackRecs = append(a.feedbackRecs, *rec)
	if !a.saveRuntimeLocked("feedbacks", a.feedbackRecs) {
		rec.Status = "failed"
		rec.Reply = "反馈记录未能保存，请稍后检查学习设置。"
		a.feedbackRecs[len(a.feedbackRecs)-1] = *rec
	}
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
func (a *App) applyBoost(studentID int64, word string) error {
	if strings.TrimSpace(word) == "" {
		return fmt.Errorf("empty word")
	}
	books := a.MyBooks(studentID)
	matched := false
	for _, b := range books {
		if b.Status != wordbook.StatusPublished {
			continue
		}
		if e, err := a.Books.EntryByKey(b.ID, normWord(word)); err == nil {
			if err := a.Sched.BoostWord(studentID, b.ID, e.ID); err != nil {
				return err
			}
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("word not enrolled")
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

package agent

import (
	"enwords/internal/feedback"
	"enwords/internal/scripts"
	"fmt"
)

// ResolveFeedbackWords is the S-05 teaching policy. The platform freezes its
// absolute result; retries never invoke this policy against a new current value.
func ResolveFeedbackWords(kind string, current, target int) (int, error) {
	if kind != string(feedback.IntentReduce) && kind != string(feedback.IntentIncrease) {
		return 0, fmt.Errorf("unsupported words intent")
	}
	if target != 0 {
		if target < 50 || target > 100 {
			return 0, fmt.Errorf("words target needs confirmation")
		}
		return target, nil
	}
	target = current - 10
	if kind == string(feedback.IntentIncrease) {
		target = current + 10
	}
	if target < 50 {
		target = 50
	}
	if target > 100 {
		target = 100
	}
	return target, nil
}

type FeedbackReplyFacts struct {
	Mode, Key, Date string
	Target, Count   int
}

// RenderFeedbackFacts accepts facts from committed-tool planning, not a model's
// claimed success. Intent transaction rollback also discards this reply.
func RenderFeedbackFacts(bank *scripts.Bank, sid int64, f FeedbackReplyFacts) string {
	switch f.Mode {
	case "config":
		if f.Key == "wordsPerLesson" {
			return bank.RenderID(scripts.NewSeededRNG(sid), "FR-1", map[string]any{"n": f.Target})
		}
		return fmt.Sprintf("每天 %d 节课的设置已保存。", f.Target)
	case "postpone":
		return "课程已改期到 " + f.Date + "。"
	case "boost":
		return fmt.Sprintf("已标记加强，共 %d 个词库词条。", f.Count)
	case "escalate":
		return "反馈已转交管理员待办。"
	case "need_confirm":
		if f.Key == "postpone_monthly_limit" {
			return fmt.Sprintf("本月主动改期已达%d次，本次未改期；请联系管理员协调学习安排。", FeedbackPostponeLimit())
		}
		return "请确认具体调整目标后重新提交，本次未修改学习安排。"
	case "clarify":
		return "请补充希望调整的学习量、日期或具体单词。"
	}
	return ""
}

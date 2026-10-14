package agent

import "time"

// OverduePlan owns the teaching decision; executors validate frozen facts only.
type OverduePlan struct {
	Target       time.Time `json:"target"`
	Consecutive  int       `json:"consecutive"`
	Care         bool      `json:"care"`
	SuggestPause bool      `json:"suggest_pause"`
}

func (a *SystemAgent) PlanOverdue(original, detected time.Time, consecutive int) OverduePlan {
	local := detected.In(original.Location())
	target := time.Date(local.Year(), local.Month(), local.Day()+1, original.Hour(), original.Minute(), original.Second(), 0, original.Location())
	plan := OverduePlan{Target: target, Consecutive: consecutive}
	for _, act := range a.Decide(SystemInput{Trigger: "cron_overdue", OverdueLessons: consecutive}) {
		if act.Kind == "reply_care" {
			plan.Care = true
		}
		if act.Kind == "suggest_pause" {
			plan.SuggestPause = true
		}
	}
	return plan
}

func (a *SystemAgent) OverdueNotice(p OverduePlan) string {
	text := "已为你重新安排课程：" + p.Target.Format("2006-01-02 15:04") + "。"
	if p.Care {
		text += "最近是不是时间紧？可以反馈希望调整的学习节奏。"
	}
	if p.SuggestPause {
		text += "建议考虑暂停计划；在你确认前仍保留课程安排。"
	}
	return text
}

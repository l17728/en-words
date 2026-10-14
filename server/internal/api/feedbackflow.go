package api

import (
	"context"
	"enwords/internal/agent"
	"regexp"
	"strings"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func normWord(w string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(strings.TrimSpace(w)), "")
}
func str(v any) string { s, _ := v.(string); return s }
func sysFeedbackInput(sid int64, text string) agent.SystemInput {
	return agent.SystemInput{Trigger: "feedback_submitted", StudentID: sid, FeedbackText: text}
}
func (a *App) HandleFeedback(sid int64, text string) *FeedbackRecord {
	return a.HandleFeedbackContext(context.Background(), sid, text)
}
func (a *App) HandleFeedbackContext(ctx context.Context, sid int64, text string) *FeedbackRecord {
	rec, err := a.SubmitFeedback(ctx, sid, FeedbackSubmission{Text: text})
	if err != nil {
		return &FeedbackRecord{StudentID: sid, Text: text, Status: "failed", Reply: "反馈未能处理，请查询后重试。", ErrorCode: feedbackErrorCode(err), Results: []FeedbackIntentResult{}, Intents: []string{}}
	}
	return rec
}
func (a *App) Feedbacks(sid int64) []FeedbackRecord {
	out, _ := a.FeedbacksContext(context.Background(), sid)
	return out
}

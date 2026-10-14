package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"enwords/internal/judge"
	"enwords/internal/obs"
	"github.com/stretchr/testify/require"
)

type decisionTraceChat struct{ body string }

func (c decisionTraceChat) ChatJSON(_ context.Context, _, _ string, out any) error {
	return json.Unmarshal([]byte(c.body), out)
}

func TestJudgeDecisionUsesCurrentAppLoggerAndPreservesLessonTrace(t *testing.T) {
	a, _, _, lessonID, _ := durableSettlementFixture(t)
	trace := fmt.Sprintf("lesson-%d", lessonID)
	ctx := obs.WithTrace(context.Background(), trace)
	var first, second bytes.Buffer
	a.SetLogger(obs.NewWithSink(&first))
	a.Judge.LLM = judge.NewZhipu(decisionTraceChat{body: `{"judgment":"private-model-body"}`})
	require.Equal(t, judge.JudgmentWrong, a.Judge.JudgeContext(ctx, "cat", []string{"猫"}, "private-student-body"))
	before := first.String()
	a.SetLogger(obs.NewWithSink(&second))
	a.Judge.LLM = judge.NewZhipu(decisionTraceChat{body: `{"judgment":"wrong"}`})
	require.Equal(t, judge.JudgmentWrong, a.Judge.JudgeContext(ctx, "cat", []string{"猫"}, "private-student-body"))
	require.Equal(t, before, first.String(), "the hook must not retain the previous logger")
	for _, tc := range []struct{ logs, source, reason string }{
		{first.String(), "fallback", "invalid_judgment"},
		{second.String(), "l2", "model_verdict"},
	} {
		requireRuntimeTrace(t, tc.logs, "judge_decision", trace)
		var event map[string]any
		require.NoError(t, json.Unmarshal(bytes.TrimSpace([]byte(tc.logs)), &event))
		require.Equal(t, "judge", event["module"])
		require.Equal(t, tc.source, event["source"])
		require.Equal(t, tc.reason, event["reason"])
		require.Equal(t, "wrong", event["result"])
		for _, private := range []string{"private-student-body", "private-model-body", "猫", "cat"} {
			require.NotContains(t, tc.logs, private)
		}
	}
}

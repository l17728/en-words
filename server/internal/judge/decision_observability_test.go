package judge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"enwords/internal/obs"
	"github.com/stretchr/testify/require"
)

type decisionChatStub struct {
	body   string
	err    error
	calls  int
	cancel context.CancelFunc
}

func (s *decisionChatStub) ChatJSON(_ context.Context, _, _ string, out any) error {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	if s.err != nil {
		return s.err
	}
	return json.Unmarshal([]byte(s.body), out)
}

func TestLLMAdapterSeparatesInvalidJudgmentFromModelWrong(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"judgment":null}`, `{"judgment":""}`, `{"judgment":"private-response-marker"}`} {
		adapter := NewZhipu(&decisionChatStub{body: body})
		got, err := adapter.Judge("cat", []string{"猫"}, "这是一只猫。")
		require.Error(t, err, "a malformed result must not masquerade as a successful model verdict")
		require.ErrorIs(t, err, ErrInvalidJudgment)
		require.Empty(t, got)
		require.NotContains(t, err.Error(), "private-response-marker")
	}
	for _, verdict := range []string{"correct", "near", "wrong"} {
		adapter := NewZhipu(&decisionChatStub{body: `{"judgment":"` + verdict + `"}`})
		got, err := adapter.Judge("cat", []string{"猫"}, "这是一只猫。")
		require.NoError(t, err)
		require.Equal(t, verdict, got)
	}
}

func TestDecisionObservationKeepsScoringAndSeparatesSources(t *testing.T) {
	for _, tc := range []struct {
		name, answer, model, source, reason string
		err                                 error
		result                              Judgment
		noProvider, canceled                bool
		calls                               int
	}{
		{name: "l1", answer: "猫", model: "wrong", result: JudgmentCorrect, source: "l1", reason: "match"},
		{name: "empty", answer: " ", model: "correct", result: JudgmentWrong, source: "l1", reason: "empty_answer"},
		{name: "unavailable", answer: "private-student-body", noProvider: true, result: JudgmentWrong, source: "fallback", reason: "provider_unavailable"},
		{name: "correct", answer: "private-student-body", model: "correct", result: JudgmentCorrect, source: "l2", reason: "model_verdict", calls: 1},
		{name: "near", answer: "private-student-body", model: "near", result: JudgmentNear, source: "l2", reason: "model_verdict", calls: 1},
		{name: "wrong", answer: "private-student-body", model: "wrong", result: JudgmentWrong, source: "l2", reason: "model_verdict", calls: 1},
		{name: "invalid_success", answer: "private-student-body", model: "private-model-body", result: JudgmentWrong, source: "fallback", reason: "invalid_judgment", calls: 1},
		{name: "invalid_error", answer: "private-student-body", err: ErrInvalidJudgment, result: JudgmentWrong, source: "fallback", reason: "invalid_judgment", calls: 1},
		{name: "provider_error", answer: "private-student-body", err: errors.New("private-provider-secret"), result: JudgmentWrong, source: "fallback", reason: "provider_error", calls: 1},
		{name: "pre_canceled", answer: "猫", canceled: true, result: JudgmentWrong, source: "fallback", reason: "context_canceled"},
		{name: "provider_canceled", answer: "private-student-body", err: context.Canceled, result: JudgmentWrong, source: "fallback", reason: "context_canceled", calls: 1},
		{name: "provider_deadline", answer: "private-student-body", err: context.DeadlineExceeded, result: JudgmentWrong, source: "fallback", reason: "context_canceled", calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := New(DefaultSynonyms())
			model := &fakeLLM{ret: tc.model, err: tc.err}
			if !tc.noProvider {
				j.LLM = model
			}
			ctx, cancel := context.WithCancel(obs.WithTrace(context.Background(), "lesson-observation"))
			defer cancel()
			if tc.canceled {
				cancel()
			}
			var decisions []Decision
			j.OnDecision = func(got context.Context, d Decision) {
				require.Equal(t, "lesson-observation", obs.TraceID(got))
				decisions = append(decisions, d)
			}
			require.Equal(t, tc.result, j.JudgeContext(ctx, "cat", []string{"猫"}, tc.answer))
			require.Equal(t, tc.calls, model.called)
			require.Equal(t, []Decision{{Source: tc.source, Reason: tc.reason, Result: tc.result}}, decisions)
			payload, err := json.Marshal(decisions)
			require.NoError(t, err)
			for _, private := range []string{"private-student-body", "private-model-body", "private-provider-secret"} {
				require.NotContains(t, string(payload), private)
			}
		})
	}
}

func TestContextAdapterDecisionPreservesPostCallCancellationContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := &decisionChatStub{body: `{"judgment":"correct"}`, cancel: cancel}
	j := New(DefaultSynonyms())
	j.LLM = NewZhipu(model)
	var decisions []Decision
	j.OnDecision = func(_ context.Context, d Decision) { decisions = append(decisions, d) }
	// The classroom owns stale-turn rejection; observation does not change this return contract.
	require.Equal(t, JudgmentCorrect, j.JudgeContext(ctx, "cat", []string{"猫"}, "这是一只猫。"))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, 1, model.calls)
	require.Equal(t, []Decision{{Source: "l2", Reason: "model_verdict", Result: JudgmentCorrect}}, decisions)
}

func TestAdapterInvalidResponseIsObservedAsFallback(t *testing.T) {
	j := New(DefaultSynonyms())
	j.LLM = NewZhipu(&decisionChatStub{body: `{}`})
	var decisions []Decision
	j.OnDecision = func(_ context.Context, d Decision) { decisions = append(decisions, d) }
	require.Equal(t, JudgmentWrong, j.JudgeContext(context.Background(), "cat", []string{"猫"}, "这是一只猫。"))
	require.Equal(t, []Decision{{Source: "fallback", Reason: "invalid_judgment", Result: JudgmentWrong}}, decisions)
}

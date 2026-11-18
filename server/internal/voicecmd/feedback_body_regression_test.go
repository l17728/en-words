package voicecmd

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestFeedbackPrefixNormalizationPreservesEntireBody(t *testing.T) {
	p := New(DefaultTable())
	bodies := []string{strings.Repeat("请保留完整中文正文。", 30), "C++ / C# 和 e-mail，3.5秒；cat  dog 我老忘啊", "cat 我老忘，dog 我也老忘呢", "Line One\n\tLine  Two！呀"}
	for _, prefix := range []string{"我想反馈", " 我 想 反 馈 ", "嗯， 我 要 反 馈 ", "反馈 "} {
		for _, body := range bodies {
			cmd, got, ok := p.MatchFeedback(prefix + body)
			require.True(t, ok)
			require.Equal(t, CmdFeedback, cmd)
			require.Equal(t, body, got)
		}
	}
	_, _, ok := p.MatchFeedback("我 想 反 馈   ")
	require.False(t, ok)
}

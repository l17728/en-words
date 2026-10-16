package voicecmd

import (
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestControlsRejectNegationQuestionsAndFuzzySuffixes(t *testing.T) {
	p := New(nil)
	for _, text := range []string{"不跳过", "不要跳过", "不会吗", "跳过？", "不是不会", "跳过这个词然后退出", "下一下", "嗯嗯下一个啊", "跳过!!", "跳过.！", "\"跳过\""} {
		t.Run(text, func(t *testing.T) { cmd, ok := p.Match(text); assert.False(t, ok); assert.Empty(t, cmd) })
	}
}

func TestControlExactNormalizationAndFeedbackIsolation(t *testing.T) {
	p := New(nil)
	for _, text := range []string{"跳过", "跳过这个词", "不会", " 跳过。 ", "下一个！", "继续!"} {
		_, ok := p.Match(text)
		assert.True(t, ok, text)
	}
	for _, text := range []string{"跳 过", "跳过！？", "跳过…", "跳过 .", "跳过\"", "跳过,", "跳过。！", "跳过 ! !"} {
		_, ok := p.Match(text)
		if text == "跳过 ." {
			assert.True(t, ok)
		} else {
			assert.False(t, ok, text)
		}
	}
	custom := New(Table{CmdNext: {"NEXT WORD", "café"}, CmdFeedback: {"feedback"}})
	cmd, ok := custom.Match(" next\tword. ")
	assert.True(t, ok)
	assert.Equal(t, CmdNext, cmd)
	_, ok = custom.Match("cafe\u0301")
	assert.True(t, ok)
	_, ok = custom.Match("feedback")
	assert.False(t, ok)
	_, ok = custom.Match(string([]byte{255}))
	assert.False(t, ok)
	_, ok = p.Match("!")
	assert.False(t, ok)
	_, ok = p.Match("跳过" + strings.Repeat(" ", 2048))
	assert.False(t, ok, "oversize raw must be rejected before trimming")
	cmd, body, ok := p.MatchFeedback("嗯 我想反馈 每天1.5节课，不要跳过！")
	assert.True(t, ok)
	assert.Equal(t, CmdFeedback, cmd)
	assert.Equal(t, "每天1.5节课，不要跳过！", body)
}

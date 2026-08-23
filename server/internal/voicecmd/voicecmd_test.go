package voicecmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---- FR-L1/L4（D-14 全程语音操作）：确定性指令词表 ----

func TestClassroomCommands(t *testing.T) {
	p := New(DefaultTable())
	for _, c := range []struct {
		text string
		want string
	}{
		{"下一个", CmdNext},
		{"下一下", CmdNext},       // 编辑距离容错
		{"继续", CmdNext},        // 同义词
		{"跳过这个词", CmdSkip},
		{"跳过", CmdSkip},
		{"我读完了", CmdRepeatDone},
		{"读完了", CmdRepeatDone},
		{"重听一遍", CmdReplay},
		{"暂停一下", CmdPause},
		{"暂停", CmdPause},
		{"退出课堂", CmdExit},
	} {
		got, ok := p.Match(c.text)
		assert.True(t, ok, "%q 应命中", c.text)
		assert.Equal(t, c.want, got, "%q", c.text)
	}
}

func TestFeedbackCapture(t *testing.T) { // FR-L2 语音反馈：前缀触发捕获
	p := New(DefaultTable())
	cmd, payload, ok := p.MatchFeedback("我想反馈每天词太多了")
	assert.True(t, ok)
	assert.Equal(t, CmdFeedback, cmd)
	assert.Equal(t, "每天词太多了", payload)

	cmd, payload, ok = p.MatchFeedback("反馈 周三有事")
	assert.True(t, ok)
	assert.Equal(t, CmdFeedback, cmd)
	assert.Equal(t, "周三有事", payload)

	// 非反馈文本不触发
	_, _, ok = p.MatchFeedback("苹果")
	assert.False(t, ok)
}

func TestNavigationCommands(t *testing.T) { // FR-L3 页面导航
	p := New(DefaultTable())
	for _, c := range []struct {
		text string
		want string
	}{
		{"首页", CmdNavHome},
		{"回首页", CmdNavHome},
		{"课程表", CmdNavSchedule},
		{"看统计", CmdNavStats},
		{"开始学习", CmdNavStart},
	} {
		got, ok := p.Match(c.text)
		assert.True(t, ok, "%q 应命中", c.text)
		assert.Equal(t, c.want, got, "%q", c.text)
	}
}

func TestNormalization(t *testing.T) { // 全半角/标点/空白容错
	p := New(DefaultTable())
	got, ok := p.Match("  下一个！ ")
	assert.True(t, ok)
	assert.Equal(t, CmdNext, got)
	// 大小写不敏感（英文触发词场景预留）
	_, ok = p.Match("嗯嗯下一个啊")
	assert.True(t, ok, "容忍语气词前后缀")
}

func TestNoMatch(t *testing.T) {
	p := New(DefaultTable())
	_, ok := p.Match("香蕉苹果大鸭梨")
	assert.False(t, ok)
	_, ok = p.Match("")
	assert.False(t, ok)
}

func TestQuizModeRestriction(t *testing.T) { // 02 §12.4: 测试作答阶段仅"跳过"生效
	p := New(DefaultTable())
	assert.True(t, p.AllowedInQuiz(CmdSkip))
	assert.False(t, p.AllowedInQuiz(CmdNext))
	assert.True(t, p.AllowedInQuiz(CmdFeedback), "反馈在答题期也允许")
}

package feedback

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedNow() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) } // 周日

func newP() *Parser { return NewParser(fixedNow) }

// ---- FB-01..08（03 §2.4 Mock 规则表）----

func TestIntentA(t *testing.T) { // FB-01 减量
	p := newP()
	out := p.Parse("每次词太多了，累")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentReduce, i.Kind)
	assert.Greater(t, i.Confidence, 0.6)
}

func TestIntentB(t *testing.T) { // FB-02 加量
	out := newP().Parse("太简单了，多来点")
	require.Len(t, out.Intents, 1)
	assert.Equal(t, IntentIncrease, out.Intents[0].Kind)
}

func TestIntentC(t *testing.T) { // FB-03 频率
	out := newP().Parse("我想一天学两次")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentFrequency, i.Kind)
	assert.Equal(t, 2, i.LessonsPerDay)
}

func TestIntentD(t *testing.T) { // FB-04 推迟 + 日期解析
	out := newP().Parse("这周三有事，先缓缓")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentPostpone, i.Kind)
	require.NotNil(t, i.Date)
	// 2026-08-23 是周日 → 本周三 = 2026-08-26
	assert.Equal(t, "2026-08-26", i.Date.Format("2006-01-02"))
}

func TestIntentE(t *testing.T) { // FB-05 难词
	out := newP().Parse("abandon 我老忘")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentHardWord, i.Kind)
	assert.Equal(t, "abandon", i.Word)
}

func TestIntentF(t *testing.T) { // FB-06 投诉
	out := newP().Parse("发音读错了")
	require.Len(t, out.Intents, 1)
	assert.Equal(t, IntentComplaint, out.Intents[0].Kind)
}

func TestIntentG(t *testing.T) { // FB-07 无法分类 → 低置信
	out := newP().Parse("今天天气好")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentOther, i.Kind)
	assert.LessOrEqual(t, i.Confidence, 0.6, "低置信触发追问")
}

func TestExplicitCount(t *testing.T) { // FB-08 数量意图 + 超界确认标记
	out := newP().Parse("词量降到30个")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentReduce, i.Kind)
	assert.Equal(t, 30, i.TargetWords)
	assert.True(t, i.NeedsConfirm, "低于下界 50 → need_confirm")
	// 界内
	out = newP().Parse("词量改到80个")
	assert.False(t, out.Intents[0].NeedsConfirm)
}

// ---- 多意图（S-12 / FR-G2 / D-13）----

func TestMultiIntent(t *testing.T) {
	out := newP().Parse("每天太多了，而且这周三有事")
	require.Len(t, out.Intents, 2, "拆分为 [A, D]")
	kinds := []IntentKind{out.Intents[0].Kind, out.Intents[1].Kind}
	assert.Contains(t, kinds, IntentReduce)
	assert.Contains(t, kinds, IntentPostpone)
}

func TestThreeIntents(t *testing.T) {
	out := newP().Parse("太多了，周三有事，abandon老忘")
	assert.Len(t, out.Intents, 3, "[A, D, E]")
}

// ---- 明天/后天 相对日期 ----

func TestRelativeDates(t *testing.T) {
	p := newP()
	out := p.Parse("明天有事")
	require.Len(t, out.Intents, 1)
	require.NotNil(t, out.Intents[0].Date)
	assert.Equal(t, "2026-08-24", out.Intents[0].Date.Format("2006-01-02"))

	out = p.Parse("后天请假")
	assert.Equal(t, "2026-08-25", out.Intents[0].Date.Format("2006-01-02"))
}

// ---- 处置决策（02 §7.1 分派）----

func TestDispatch(t *testing.T) {
	p := newP()
	// A 边界内 → apply_config
	d := p.Plan(p.Parse("太多了"))
	assert.Equal(t, ActionApplyConfig, d[0].Action)
	// 超界 → need_confirm
	d = p.Plan(p.Parse("降到30个"))
	assert.Equal(t, ActionNeedConfirm, d[0].Action)
	// D → postpone（≤7 天）
	d = p.Plan(p.Parse("周三有事"))
	assert.Equal(t, ActionPostpone, d[0].Action)
	// 推迟 >7 天 → need_confirm
	d = p.Plan(p.Parse("下个月15号都有事"))
	assert.Equal(t, ActionNeedConfirm, d[0].Action)
	// E → boost
	d = p.Plan(p.Parse("abandon老忘"))
	assert.Equal(t, ActionBoost, d[0].Action)
	// F → escalate
	d = p.Plan(p.Parse("发音不对"))
	assert.Equal(t, ActionEscalate, d[0].Action)
	// G → clarify
	d = p.Plan(p.Parse("今天天气好"))
	assert.Equal(t, ActionClarify, d[0].Action)
}

func TestPlanMulti(t *testing.T) { // 多意图逐项决策（S-12）
	d := newP().Plan(newP().Parse("太多了，而且周三有事"))
	require.Len(t, d, 2)
	actions := []ActionType{d[0].Action, d[1].Action}
	assert.Contains(t, actions, ActionApplyConfig)
	assert.Contains(t, actions, ActionPostpone)
}

// ---- 边界补充（覆盖率）----

func TestEmptyAndFallback(t *testing.T) {
	p := newP()
	out := p.Parse("")
	require.Len(t, out.Intents, 1)
	assert.Equal(t, IntentOther, out.Intents[0].Kind)

	// 无日期请假 → 明日保守
	out = p.Parse("明天有事") // 有"明天"
	require.Len(t, out.Intents, 1)
	assert.Equal(t, "2026-08-24", out.Intents[0].Date.Format("2006-01-02"))
}

func TestFrequencyOverflowNeedsConfirm(t *testing.T) { // 一天三次 → 超界确认
	out := newP().Parse("一天学三次")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentFrequency, i.Kind)
	assert.Equal(t, 3, i.LessonsPerDay)
	assert.True(t, i.NeedsConfirm, "超过 lessonsPerDayMax=2 → need_confirm")
	d := newP().Plan(out)
	assert.Equal(t, ActionNeedConfirm, d[0].Action)
}

func TestExplicitAndRelativeTargetsNeedConfirmation(t *testing.T) {
	out := newP().Parse("词量降到30个，太多了") // 两段同为 A
	require.Len(t, out.Intents, 2, "explicit and relative targets are distinct intents")
	i := out.Intents[0]
	assert.Equal(t, 30, i.TargetWords, "显式数字保留")
	assert.True(t, i.NeedsConfirm, "超界标记保留")
	for _, action := range newP().Plan(out) {
		assert.Equal(t, ActionNeedConfirm, action.Action, "neither conflicting intent may write configuration")
	}
}

func TestExplicitIncreaseInRange(t *testing.T) { // 界内加量
	out := newP().Parse("词量改到80个")
	require.Len(t, out.Intents, 1)
	i := out.Intents[0]
	assert.Equal(t, IntentIncrease, i.Kind)
	assert.Equal(t, 80, i.TargetWords)
	assert.False(t, i.NeedsConfirm)
}

func TestDahoutian(t *testing.T) { // 大后天
	out := newP().Parse("大后天没空")
	require.Len(t, out.Intents, 1)
	assert.Equal(t, "2026-08-26", out.Intents[0].Date.Format("2006-01-02"))
}

func TestNextYearDate(t *testing.T) { // 已过日期 → 明年
	out := newP().Parse("1月5号有事")
	require.Len(t, out.Intents, 1)
	assert.Equal(t, "2027-01-05", out.Intents[0].Date.Format("2006-01-02"))
}

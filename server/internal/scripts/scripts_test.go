package scripts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBank() *Bank {
	b := NewBank()
	b.SeedAll() // 内置 04 §5 全量模板
	return b
}

// ---- 模板齐全性（04 §5 每类 ≥6 条）----

func TestBankCategoriesComplete(t *testing.T) {
	b := newBank()
	for _, cat := range []string{"open", "transition", "encourage", "correct", "closing", "nudge", "feedback_reply", "report", "system_notify"} {
		tpls := b.List(cat)
		assert.GreaterOrEqual(t, len(tpls), 6, "分类 %s 至少 6 条模板", cat)
	}
}

// ---- weight 加权抽取 + 同课降权（04 §6.1）----

func TestPickWeightedAndDedup(t *testing.T) {
	b := newBank()
	rng := NewSeededRNG(1001)
	picked := map[string]int{}
	for i := 0; i < 30; i++ {
		tpl, ok := b.Pick("nudge", rng, map[string]bool{})
		require.True(t, ok)
		picked[tpl.ID]++
	}
	assert.Greater(t, len(picked), 1, "加权随机应命中多个模板")

	// 已用降权：排除后不再出现
	used := map[string]bool{}
	first, _ := b.Pick("nudge", rng, used)
	used[first.ID] = true
	for i := 0; i < 20; i++ {
		tpl, ok := b.Pick("nudge", rng, used)
		require.True(t, ok)
		assert.NotEqual(t, first.ID, tpl.ID, "已用模板被降权排除")
	}
}

// ---- 变量替换 + 安全校验（04 §6 步骤 2/4）----

func TestRenderVariablesAndSafety(t *testing.T) {
	b := newBank()
	rng := NewSeededRNG(42)
	text := b.Render(rng, "open", map[string]any{"name": "小明", "n": 60}, map[string]bool{})
	require.NotEmpty(t, text)
	// OP-1 含 {name} 与 {n}——渲染结果不应有残留占位符
	assert.False(t, strings.Contains(text, "{"), "占位符必须全部替换: %q", text)
}

func TestRenderFallsBackOnMissingVars(t *testing.T) {
	b := newBank()
	rng := NewSeededRNG(7)
	// 缺变量 → 渲染失败回退原文模板（安全）
	text := b.Render(rng, "nudge", map[string]any{}, map[string]bool{})
	assert.NotEmpty(t, text)
}

// ---- 口语化管线（04 §6 步骤 3）----

func TestColloquialization(t *testing.T) {
	b := newBank()
	// 长句拆短 + 语气词注入至少可被触发（不同 seed 扫描）
	variants := map[string]int{}
	for seed := int64(0); seed < 60; seed++ {
		rng := NewSeededRNG(seed)
		text := b.Render(rng, "closing", map[string]any{"n": 60, "m": 3, "name": "小明", "p": 85, "word": "apple", "date": "周三", "newDate": "周四", "cum": 100, "grad": 20, "r": 2, "t": 8, "done": 3, "plan": 5, "streak": "", "weak": "apple", "wrongwords": "apple,bee", "trend": "升了", "delta_p": "+5%"}, map[string]bool{})
		variants[text]++
		assert.LessOrEqual(t, len([]rune(text)), 80, "渲染结果含口语化仍需 ≤ 安全长度")
		assert.False(t, strings.Contains(text, "{"))
	}
	// 不同 seed 应产生差异（口语化生效的证据）
	assert.Greater(t, len(variants), 3, "60 个 seed 至少 4 种输出变体")
}

// ---- seed 确定性（03 §6：同 seed 重放一致）----

func TestSeededDeterministic(t *testing.T) {
	b := newBank()
	ctx := map[string]any{"name": "小明", "n": 60}
	t1 := b.Render(NewSeededRNG(123), "open", ctx, map[string]bool{})
	t2 := b.Render(NewSeededRNG(123), "open", ctx, map[string]bool{})
	assert.Equal(t, t1, t2, "同 seed 渲染必须一致(E2E 断言稳定)")
}

// ---- FR-11 多意图合并模板存在 ----

func TestMultiIntentMergeTemplate(t *testing.T) {
	b := newBank()
	found := false
	for _, tpl := range b.List("feedback_reply") {
		if strings.Contains(tpl.Template, "{n}") && strings.Contains(tpl.Template, "{newDate}") {
			found = true
		}
	}
	assert.True(t, found, "FR-11 多意图合并模板(词量+改期)必须存在")
}

// ---- RB 课报系列（D-13）----

func TestReportTemplates(t *testing.T) {
	b := newBank()
	assert.GreaterOrEqual(t, len(b.List("report")), 8, "RB-1..8 课报模板")
}

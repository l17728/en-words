package scripts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- RenderID 精确模板渲染 ----

func TestRenderID(t *testing.T) {
	b := newBank()
	rng := NewSeededRNG(7)
	out := b.RenderID(rng, "FR-1", map[string]any{"n": 50})
	require.NotEmpty(t, out)
	assert.Contains(t, out, "50", "变量注入")
	assert.False(t, strings.Contains(out, "{"))
	// 不存在的 ID
	assert.Empty(t, b.RenderID(rng, "NOPE-1", nil))
}

// ---- PickMatching：占位符子集过滤 + 回退全量 ----

func TestPickMatchingFilters(t *testing.T) {
	b := newBank()
	rng := NewSeededRNG(3)
	// vars 只有 n → 仅无占位符/只需 n 的模板可入选（结果必然不含其他占位符）
	for i := 0; i < 30; i++ {
		tpl, ok := b.PickMatching("closing", rng, map[string]any{"n": 5}, nil)
		require.True(t, ok)
		placeholders := placeholderRe.FindAllStringSubmatch(tpl.Template, -1)
		for _, m := range placeholders {
			assert.Equal(t, "n", m[1], "入选模板占位符必须 ⊆ vars 键集")
		}
	}
	// 无任何匹配（report 全模板需要缺失变量）→ 回退全量池仍可抽中
	tpl, ok := b.PickMatching("report", rng, map[string]any{}, nil)
	require.True(t, ok)
	assert.NotEmpty(t, tpl.ID)
}

// ---- Render 缺变量安全回退（含占位符的原文模板）----

func TestRenderMissingVarsFallsBackRaw(t *testing.T) {
	b := newBank()
	rng := NewSeededRNG(11)
	out := b.Render(rng, "report", map[string]any{}, nil)
	require.NotEmpty(t, out)
	// 回退=某模板原文（以【开头），口语化未破坏段标
	assert.Contains(t, out, "【", "回退为带段落标的原文模板")
}

// ---- colloquialize 分支（白盒：seed 扫描触发 0~2 条规则）----

func TestColloquializeBranches(t *testing.T) {
	// 尾词替换：句号结尾 20% 概率
	hitTail := false
	for seed := int64(0); seed < 300 && !hitTail; seed++ {
		out := colloquialize(NewSeededRNG(seed), "今天搞定了。", nil)
		if strings.HasSuffix(out, "哈。") || strings.HasSuffix(out, "啦。") || strings.HasSuffix(out, "呗。") {
			hitTail = true
		}
	}
	assert.True(t, hitTail, "句号尾词分支应被触发")

	// 称呼变体：name 出现时 25% → "小明同学"
	hitName := false
	for seed := int64(0); seed < 300 && !hitName; seed++ {
		out := colloquialize(NewSeededRNG(seed), "小明，上课啦！", map[string]any{"name": "小明"})
		if strings.Contains(out, "小明同学") {
			hitName = true
		}
	}
	assert.True(t, hitName, "称呼变体分支应被触发")

	// 长句拆短：>25 字含逗号 40%
	hitSplit := false
	long := "这是一条特别长的句子用来测试拆短逻辑，后面还有内容继续凑字数以达到阈值标准。"
	for seed := int64(0); seed < 300 && !hitSplit; seed++ {
		out := colloquialize(NewSeededRNG(seed), long, nil)
		if strings.Contains(out, "\n") {
			hitSplit = true
		}
	}
	assert.True(t, hitSplit, "长句拆短分支应被触发")

	// 语气词注入 30%
	hitFill := false
	for seed := int64(0); seed < 300 && !hitFill; seed++ {
		out := colloquialize(NewSeededRNG(seed), "好的", nil)
		if strings.HasPrefix(out, "来，") || strings.HasPrefix(out, "好嘞，") ||
			strings.HasPrefix(out, "嗯——") || strings.HasPrefix(out, "哎，") {
			hitFill = true
		}
	}
	assert.True(t, hitFill, "语气词注入分支应被触发")
}

// ---- Add 默认权重 ----

func TestAddDefaultWeight(t *testing.T) {
	b := NewBank()
	b.Add(Template{ID: "X-1", Category: "x", Template: "t"}) // Weight 0 → 默认 1
	tpl, ok := b.Pick("x", NewSeededRNG(1), nil)
	require.True(t, ok)
	assert.Equal(t, "X-1", tpl.ID)
	// 空分类 Pick 失败
	_, ok = b.Pick("none", NewSeededRNG(1), nil)
	assert.False(t, ok)
	// 全部已用 → 忽略降权重抽
	_, ok = b.Pick("x", NewSeededRNG(1), map[string]bool{"X-1": true})
	assert.True(t, ok)
}

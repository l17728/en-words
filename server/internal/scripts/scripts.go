// Package scripts 实现话术库与口语化管线（04 §5/§6）：
// 分类模板（≥6 条/类）、weight 加权抽取（同课降权）、变量替换、
// 口语化微调（语气词/称呼变体/长句拆短）、seed 确定性（E2E 断言稳定）、
// 安全校验（占位符残留/超长回退）。
package scripts

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
)

// Template 话术模板。
type Template struct {
	ID       string
	Category string
	Template string
	Tone     string
	Weight   int
}

// Bank 话术库。
type Bank struct {
	cats map[string][]Template
}

func NewBank() *Bank {
	return &Bank{cats: map[string][]Template{}}
}

// Add 添加模板。
func (b *Bank) Add(ts ...Template) {
	for _, t := range ts {
		if t.Weight <= 0 {
			t.Weight = 1
		}
		b.cats[t.Category] = append(b.cats[t.Category], t)
	}
}

// List 分类模板列表。
func (b *Bank) List(cat string) []Template {
	out := append([]Template(nil), b.cats[cat]...)
	return out
}

// ---- RNG（seed 确定性：04 §7.1 seed=lessonId 语义）----

type RNG struct{ r *rand.Rand }

// NewSeededRNG 确定性随机源（同 seed 同序列）。
func NewSeededRNG(seed int64) *RNG { return &RNG{r: rand.New(rand.NewSource(seed))} }

func (g *RNG) Intn(n int) int { return g.r.Intn(n) }
func (g *RNG) Float64() float64 { return g.r.Float64() }

// Pick 加权抽取（used 内的模板降权至 0=排除；04 §6 步骤 1）。
func (b *Bank) Pick(cat string, rng *RNG, used map[string]bool) (Template, bool) {
	tpls := b.cats[cat]
	if len(tpls) == 0 {
		return Template{}, false
	}
	type cand struct {
		t Template
		w int
	}
	var pool []cand
	total := 0
	for _, t := range tpls {
		w := t.Weight
		if used[t.ID] {
			w = 0
		}
		if w > 0 {
			pool = append(pool, cand{t, w})
			total += w
		}
	}
	if total == 0 { // 全部已用 → 忽略降权重抽
		for _, t := range tpls {
			pool = append(pool, cand{t, t.Weight})
			total += t.Weight
		}
	}
	pick := rng.Intn(total)
	for _, c := range pool {
		pick -= c.w
		if pick < 0 {
			return c.t, true
		}
	}
	return pool[0].t, true
}

// ---- 渲染管线（04 §6）----

var placeholderRe = regexp.MustCompile(`\{(\w+)\}`)

// Render 完整管线：抽取（仅占位符可被 vars 覆盖的模板）→ 变量替换 →
// 口语化（0~2 条，seed 决定）→ 安全校验 → 回退。
func (b *Bank) Render(rng *RNG, cat string, vars map[string]any, used map[string]bool) string {
	tpl, ok := b.PickMatching(cat, rng, vars, used)
	if !ok {
		return ""
	}
	text := replaceVars(tpl.Template, vars)
	text = colloquialize(rng, text, vars)
	if !safeText(text) {
		return tpl.Template
	}
	return text
}

// RenderID 按模板 ID 精确渲染（反馈闭环等绑定具体话术槽的场景）。
func (b *Bank) RenderID(rng *RNG, id string, vars map[string]any) string {
	for _, tpls := range b.cats {
		for _, t := range tpls {
			if t.ID == id {
				text := replaceVars(t.Template, vars)
				text = colloquialize(rng, text, vars)
				if !safeText(text) {
					return t.Template
				}
				return text
			}
		}
	}
	return ""
}

// PickMatching 加权抽取，但仅从「占位符 ⊆ vars 键集」的模板中抽
// （消灭"抽到缺变量模板→回退带占位符原文"这一类问题；无匹配则回退全量）。
func (b *Bank) PickMatching(cat string, rng *RNG, vars map[string]any, used map[string]bool) (Template, bool) {
	tpls := b.cats[cat]
	if len(tpls) == 0 {
		return Template{}, false
	}
	var match []Template
	for _, t := range tpls {
		if placeholdersSubset(t.Template, vars) {
			match = append(match, t)
		}
	}
	if len(match) == 0 {
		match = tpls // 无完美匹配回退全量（安全校验兜底）
	}
	// 在 match 上做加权抽取（复用 Pick 的权重逻辑，局部实现避免 map 复制）
	type cand struct {
		t Template
		w int
	}
	var pool []cand
	total := 0
	for _, t := range match {
		w := t.Weight
		if used[t.ID] {
			w = 0
		}
		if w > 0 {
			pool = append(pool, cand{t, w})
			total += w
		}
	}
	if total == 0 {
		for _, t := range match {
			pool = append(pool, cand{t, t.Weight})
			total += t.Weight
		}
	}
	pick := rng.Intn(total)
	for _, c := range pool {
		pick -= c.w
		if pick < 0 {
			return c.t, true
		}
	}
	return pool[0].t, true
}

// placeholdersSubset 模板全部占位符均存在于 vars。
func placeholdersSubset(tpl string, vars map[string]any) bool {
	for _, m := range placeholderRe.FindAllStringSubmatch(tpl, -1) {
		key := m[1]
		if _, ok := vars[key]; !ok {
			return false
		}
	}
	return true
}

// replaceVars 变量替换（04 §6 步骤 2）。
func replaceVars(tpl string, vars map[string]any) string {
	return placeholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := strings.Trim(m, "{}")
		if v, ok := vars[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return m // 缺变量保留占位符（安全校验会拦截回退）
	})
}

// safeText 安全校验：≤80 字（含口语化增字）、无残留占位符（04 §6 步骤 4）。
func safeText(s string) bool {
	if len([]rune(s)) > 80 {
		return false
	}
	return !strings.Contains(s, "{")
}

// colloquialize 口语化微调（04 §6 步骤 3：固定管线，seed 驱动）。
func colloquialize(rng *RNG, text string, vars map[string]any) string {
	// a. 语气词注入（30%）
	if rng.Float64() < 0.3 {
		fills := []string{"来，", "好嘞，", "嗯——", "哎，"}
		text = fills[rng.Intn(len(fills))] + text
	}
	// d. 长句拆短（>25 字且含逗号，40%）
	if rng.Float64() < 0.4 && len([]rune(text)) > 25 && strings.Contains(text, "，") {
		idx := strings.Index(text, "，")
		if idx > 4 && idx < len(text)-4 {
			text = text[:idx+1] + "\n" + text[idx+1:]
		}
	}
	// c. 语气尾词（20%，模板以句号结尾时）
	if strings.HasSuffix(text, "。") && rng.Float64() < 0.2 {
		tails := []string{"哈", "啦", "呗"}
		text = strings.TrimSuffix(text, "。") + tails[rng.Intn(len(tails))] + "。"
	}
	// b. 称呼变体（25%）：{name} 已替换为具体昵称时前后缀"同学"
	if name, ok := vars["name"].(string); ok && name != "" && rng.Float64() < 0.25 {
		if strings.Contains(text, name) {
			text = strings.Replace(text, name, name+"同学", 1)
		}
	}
	return text
}

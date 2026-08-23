package judge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---- JU-01..08（03 文档 §2.3）：第 1 级确定性判分 ----

func newJ() *Judge {
	return New(DefaultSynonyms())
}

func TestExactChinese(t *testing.T) { // JU-01
	j := newJ()
	got := j.Level1("apple", []string{"n. 苹果"}, "苹果")
	assert.Equal(t, JudgmentCorrect, got)
}

func TestEnglishNormalized(t *testing.T) { // JU-02
	j := newJ()
	got := j.Level1("apple", []string{"n. 苹果"}, ".apple APPle ")
	assert.Equal(t, JudgmentCorrect, got, "归一化(去标点/冠词/大小写)")
	got = j.Level1("an apple", []string{"n. 苹果"}, "apple")
	assert.Equal(t, JudgmentCorrect, got, "去冠词")
}

func TestSynonymNear(t *testing.T) { // JU-03
	j := newJ()
	got := j.Level1("abandon", []string{"v. 放弃"}, "抛弃")
	assert.Equal(t, JudgmentNear, got, "同义表命中 → near")
}

func TestUnrelated(t *testing.T) { // JU-04
	j := newJ()
	got := j.Level1("abandon", []string{"v. 放弃"}, "香蕉")
	assert.Equal(t, JudgmentWrong, got)
}

func TestStageAdaptedMeaning(t *testing.T) { // JU-05/06 学段适配
	j := newJ()
	assert.Equal(t, JudgmentNear, j.Level1("address", []string{"n. 地址"}, "住址"), "同义命中=near(判对)")
	// 释义只有"地址"，答"演说"（非该学段义）→ 不判对（Stage-Adapted, FR-B5）
	assert.Equal(t, JudgmentWrong, j.Level1("address", []string{"n. 地址"}, "演说"))
}

func TestEmptyAndNoise(t *testing.T) { // JU-07
	j := newJ()
	for _, txt := range []string{"", "   ", "呃", "嗯…", "不知道", "我想不起来"} {
		assert.Equal(t, JudgmentWrong, j.Level1("apple", []string{"n. 苹果"}, txt), "输入 %q", txt)
	}
}

func TestDontKnowPhrases(t *testing.T) {
	j := newJ()
	assert.Equal(t, JudgmentWrong, j.Level1("apple", []string{"n. 苹果"}, "不记得了"))
	assert.Equal(t, JudgmentWrong, j.Level1("apple", []string{"n. 苹果"}, "忘了"))
}

// ---- 释义拆解（pos 前缀剥离/义项切分）----

func TestMeaningParsing(t *testing.T) {
	j := newJ()
	// "n. 地址" → 候选 {地址}
	assert.Equal(t, JudgmentCorrect, j.Level1("address", []string{"n. 地址"}, "地址"))
	// 多义项 "v.&n. 放弃；抛弃" → 候选 {放弃,抛弃}
	assert.Equal(t, JudgmentCorrect, j.Level1("abandon", []string{"v.&n. 放弃；抛弃"}, "放弃"))
	assert.Equal(t, JudgmentNear, j.Level1("abandon", []string{"v.&n. 放弃；抛弃"}, "抛弃2"), "编辑距离容错≤1")
}

// ---- Level2：LLM 兜底仅未命中时调用 ----

type fakeLLM struct{ called int; ret string; err error }

func (f *fakeLLM) Judge(word string, meanings []string, text string) (string, error) {
	f.called++
	return f.ret, f.err
}

func TestTwoLevelFlow(t *testing.T) { // 05 §2.1 两级判分
	llm := &fakeLLM{ret: "correct"}
	j := New(DefaultSynonyms())
	j.LLM = llm

	// L1 命中 → 不调 LLM
	assert.Equal(t, JudgmentCorrect, j.Judge("apple", []string{"n. 苹果"}, "苹果"))
	assert.Equal(t, 0, llm.called)

	// L1 未命中 → L2
	assert.Equal(t, JudgmentCorrect, j.Judge("apple", []string{"n. 苹果"}, "一种水果"), "L2 判对")
	assert.Equal(t, 1, llm.called)

	// L1 未命中且空文本 → 直接 wrong 不调 LLM
	assert.Equal(t, JudgmentWrong, j.Judge("apple", []string{"n. 苹果"}, ""))
	assert.Equal(t, 1, llm.called)
}

func TestLLMErrorFallback(t *testing.T) { // LLM 失败回退 L1 结果
	llm := &fakeLLM{ret: "", err: errBoom}
	j := New(DefaultSynonyms())
	j.LLM = llm
	assert.Equal(t, JudgmentWrong, j.Judge("apple", []string{"n. 苹果"}, "某种东西"))
}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

var errBoom = boomErr{}

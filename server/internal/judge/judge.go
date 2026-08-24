// Package judge 实现两级判分（05 §2.1，D-9/D-12）：
// L1 确定性关键词归一化 + 同义表（零成本可单测）；
// L2 LLM 兜底（智谱 GLM Provider 注入），仅 L1 未命中且文本非空时调用；失败回退 L1。
package judge

import (
	"context"
	"strings"
	"unicode"
)

// Judgment 判分结果（与 classroom.Judgment 对齐，避免包循环独立定义）。
type Judgment string

const (
	JudgmentCorrect Judgment = "correct"
	JudgmentNear    Judgment = "near"
	JudgmentWrong   Judgment = "wrong"
)

// LLMJudge L2 兜底接口（真实=智谱 GLM；mock=脚本；nil=禁用）。
type LLMJudge interface {
	Judge(word string, meanings []string, text string) (string, error)
}

// Judge 判分器。
type Judge struct {
	syn map[string][]string // 规范词 → 同义词组
	LLM LLMJudge
}

// ZhipuLLM 真实智谱判分适配器（L2；D-12）。
type ZhipuLLM struct {
	Client ZhipuChat // 接口化避免包依赖：由 llmzhipu.Client 满足
}

// ZhipuChat 智谱最小接口（llmzhipu.Client 满足）。
type ZhipuChat interface {
	ChatJSON(ctx context.Context, system, user string, out any) error
}

// NewZhipu 构造真实 L2。
func NewZhipu(c ZhipuChat) *ZhipuLLM { return &ZhipuLLM{Client: c} }

// Judge L2 判分（智谱 GLM 真实调用）。
func (z *ZhipuLLM) Judge(word string, meanings []string, text string) (string, error) {
	var out struct {
		Judgment string `json:"judgment"`
	}
	sys := "你是单词学习判分器。给定单词、标准中文释义与学生口头回答，判断掌握情况。只输出 JSON：{\"judgment\":\"correct|near|wrong\"}。规则：意思相同=correct；同义/近义=near；无关/不知道/空=wrong。"
	user := "单词: " + word + "；标准释义: " + strings.Join(meanings, "；") +
		"；学生回答: \"" + text + "\"。请判分。"
	if err := z.Client.ChatJSON(context.Background(), sys, user, &out); err != nil {
		return "", err
	}
	switch out.Judgment {
	case "correct", "near", "wrong":
		return out.Judgment, nil
	}
	return "wrong", nil
}

// New 构造；syn 同义表（key/value 均为规范词）。
func New(syn map[string][]string) *Judge {
	return &Judge{syn: syn}
}

// DefaultSynonyms 默认同义表（03 §6 Mock 规格；testdata/synonyms.csv 可扩展）。
func DefaultSynonyms() map[string][]string {
	return map[string][]string{
		"放弃": {"抛弃", "舍弃", "摒弃"},
		"地址": {"住址", "地址"},
		"苹果": {"apple"},
		"能力": {"本领", "才干"},
		"美丽的": {"漂亮的", "好看的"},
		"重要": {"要紧", "关键"},
		"帮助": {"帮忙", "协助"},
		"聪明": {"机灵", "智慧"},
		"困难": {"艰难", "难"},
	}
}

// Level1 第 1 级确定性判分。
func (j *Judge) Level1(word string, meanings []string, text string) Judgment {
	ans := normalizeZh(text)
	if ans == "" || isGiveUp(ans) {
		return JudgmentWrong
	}
	// 英文作答：整体或任一 token 等价（去标点分词，token 天然剥离冠词）
	if enMatch(text, word) {
		return JudgmentCorrect
	}
	for _, m := range meanings {
		for _, cand := range candidates(m) {
			if cand == "" {
				continue
			}
			if ans == cand {
				return JudgmentCorrect
			}
			// 同义表（双向）
			for _, s := range j.syn[cand] {
				if s == ans {
					return JudgmentNear // 同义命中=near（IsCorrect 判对，JU-03/JU-05）
				}
			}
			// 编辑距离 ≤1 容错（双方均 ≥2 字防短词误判）
			if len([]rune(cand)) >= 2 && len([]rune(ans)) >= 2 && editDistanceLE1(ans, cand) {
				return JudgmentNear
			}
			if enMatch(text, cand) {
				return JudgmentCorrect
			}
		}
	}
	return JudgmentWrong
}

// enMatch 英文匹配：任一 token 等价（去冠词后完整 token 比对；R4-backend#5 简化单路径）。
func enMatch(text, target string) bool {
	nt := normEn(target)
	if nt == "" {
		return false
	}
	for _, tok := range enTokens(text) {
		tok = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(tok, "the "), "an "), "a "))
		if tok == nt {
			return true
		}
	}
	return false
}

// enTokens 按非字母切分的英文 token（小写）。
func enTokens(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Judge 两级判分入口（课堂调用）。
func (j *Judge) Judge(word string, meanings []string, text string) Judgment {
	l1 := j.Level1(word, meanings, text)
	if l1 != JudgmentWrong {
		return l1 // L1 命中（correct/near）零成本返回
	}
	if strings.TrimSpace(text) == "" || j.LLM == nil {
		return JudgmentWrong
	}
	got, err := j.LLM.Judge(word, meanings, text)
	if err != nil {
		return JudgmentWrong // LLM 失败回退 L1 结果（05 §2.1）
	}
	switch Judgment(got) {
	case JudgmentCorrect, JudgmentNear, JudgmentWrong:
		return Judgment(got)
	default:
		return JudgmentWrong
	}
}

// ---- 文本工具 ----

// normalizeZh 中文答案归一化：去空白/标点/常见语气词。
func normalizeZh(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	out := b.String()
	// 前后语气词剥离
	for _, p := range []string{"嗯", "啊", "哦", "呃", "呀", "唉", "诶"} {
		out = strings.TrimPrefix(out, p)
		out = strings.TrimSuffix(out, p)
	}
	return out
}

// eqEn 英文等价：小写、去冠词/标点/空白。
func eqEn(a, b string) bool {
	na := normEn(a)
	nb := normEn(b)
	return na != "" && na == nb
}

func normEn(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, art := range []string{"an ", "a ", "the "} {
		s = strings.TrimPrefix(s, art)
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isGiveUp 放弃类回答判错（AI-09：不知道/忘了…）。
func isGiveUp(s string) bool {
	giveups := []string{"不知道", "不认识", "不记得", "想不起来", "忘了", "不清楚", "没学过"}
	for _, g := range giveups {
		if strings.Contains(s, g) {
			return true
		}
	}
	return false
}

// candidates 释义拆解为候选词列表：去词性前缀、按 ；;/分义项、剥括号注释。
func candidates(meaning string) []string {
	m := strings.TrimSpace(meaning)
	// 剥词性前缀（n./v./adj./adv./vt./vi./prep./conj./pron./num./art./interj./modal v./aux v./phrase 及组合）
	for _, p := range []string{"modal v.", "aux v.", "vt. & vi.", "vt.& vi.", "vt.&vi.", "v.&n.", "v. & n.",
		"interj.", "pron.", "prep.", "conj.", "num.", "art.", "adj.", "adv.", "vt.", "vi.", "n.", "v.", "int.", "phrase.", "名词短语.", "动词短语.", "短语."} {
		if strings.HasPrefix(m, p) {
			m = strings.TrimSpace(strings.TrimPrefix(m, p))
			break
		}
	}
	parts := strings.FieldsFunc(m, func(r rune) bool {
		return r == '；' || r == ';' || r == '，' || r == ',' || r == '。'
	})
	var out []string
	for _, p := range parts {
		p = stripParen(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stripParen 剥括号注释：（表示…）/(...)。
func stripParen(s string) string {
	s = strings.TrimSpace(s)
	for _, open := range []string{"（", "("} {
		if i := strings.Index(s, open); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	return s
}

// editDistanceLE1 编辑距离 ≤1（仅同长或差 1 的快速路径）。
func editDistanceLE1(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if abs(len(ra)-len(rb)) > 1 {
		return false
	}
	if len(ra) == len(rb) {
		diff := 0
		for i := range ra {
			if ra[i] != rb[i] {
				diff++
				if diff > 1 {
					return false
				}
			}
		}
		return diff <= 1
	}
	// 长度差 1：单字符插入/删除
	long, short := ra, rb
	if len(short) > len(long) {
		long, short = short, long
	}
	i, j, skipped := 0, 0, false
	for i < len(long) && j < len(short) {
		if long[i] == short[j] {
			i++
			j++
			continue
		}
		if skipped {
			return false
		}
		skipped = true
		i++
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

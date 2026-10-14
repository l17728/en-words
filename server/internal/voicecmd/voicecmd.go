// Package voicecmd 实现全程语音操作的确定性指令解析（D-14 / 01 FR-L / 02 §12.1）：
// 词表匹配 + 归一化 + 编辑距离容错 + 反馈前缀捕获；不经 LLM，误识别率低、可单测。
package voicecmd

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 指令集（01 FR-L1/L3）。
// 注：repeat_done 口令已随 2026-08-25"老师听学生读完"定案移除——
// 跟读推进只认语音判读（HandleVoiceText 听读匹配当前词），不认口令。
const (
	CmdNext        = "next"     // 下一个/继续
	CmdSkip        = "skip"     // 跳过（测试作答期唯一生效的教学指令）
	CmdReplay      = "replay"   // 重听一遍
	CmdPause       = "pause"    // 暂停
	CmdExit        = "exit"     // 退出课堂
	CmdFeedback    = "feedback" // 我想反馈…（前缀捕获）
	CmdNavHome     = "nav_home"
	CmdNavSchedule = "nav_schedule"
	CmdNavStats    = "nav_stats"
	CmdNavStart    = "nav_start"
)

// Table 指令词表：指令 → 触发词组。
type Table map[string][]string

// DefaultTable 默认词表（config voiceCommands 可覆盖，L5）。
func DefaultTable() Table {
	return Table{
		CmdNext:        {"下一个", "继续", "下一题", "下一词"},
		CmdSkip:        {"跳过", "跳过这个词", "不会", "过"},
		CmdReplay:      {"重听", "再读一遍", "重听一遍"},
		CmdPause:       {"暂停", "暂停一下", "歇会"},
		CmdExit:        {"退出", "退出课堂", "下课"},
		CmdNavHome:     {"首页", "回首页", "主页"},
		CmdNavSchedule: {"课程表", "我的课表"},
		CmdNavStats:    {"统计", "看统计", "学习统计"},
		CmdNavStart:    {"开始学习", "开始上课", "上课"},
	}
}

// feedbackPrefixes 反馈前缀触发词（FR-L2）。
var feedbackPrefixes = []string{"我想反馈", "我要反馈", "反馈", "我反馈", "提个意见", "建议"}

// Parser 指令解析器。
type Parser struct {
	table Table
	// cmds 有序指令键（构造时固化，消灭 map 迭代序非确定性：
	// 同输入必须同执行路径——覆盖率稳定 + 行为可复现）
	cmds []string
}

func New(t Table) *Parser {
	if t == nil {
		t = DefaultTable()
	}
	cmds := make([]string, 0, len(t))
	for cmd := range t {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds) // 字典序稳定
	return &Parser{table: t, cmds: cmds}
}

// normalize 归一化：去空白/标点（中英）、容忍首尾语气词。
func normalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '！' || r == '。' || r == '，' || r == '？' ||
			r == '!' || r == '.' || r == ',' || r == '?' {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	// 首尾语气词剥离（循环两层：嗯嗯/啊啊）
	for pass := 0; pass < 2; pass++ {
		for _, p := range []string{"嗯", "啊", "哦", "呃", "呀", "诶"} {
			out = strings.TrimPrefix(out, p)
			out = strings.TrimSuffix(out, p)
		}
	}
	return out
}

// Match 教学指令匹配：精确触发词 → 触发词前缀（长词优先）→ 编辑距离 ≤1 容错。
// 遍历按 cmds 固定序（确定性）。返回 (指令, 是否命中)。
func (p *Parser) Match(text string) (string, bool) {
	n := normalize(text)
	if n == "" {
		return "", false
	}
	// 1) 精确/前缀（长触发词优先，避免"跳过这个词"被"跳过"截胡歧义）
	bestCmd, bestLen := "", 0
	for _, cmd := range p.cmds {
		if cmd == CmdFeedback {
			continue
		}
		for _, w := range p.table[cmd] {
			wn := normalize(w)
			if wn == "" {
				continue
			}
			if n == wn {
				return cmd, true
			}
			if strings.HasPrefix(n, wn) && len(wn) > bestLen {
				bestCmd, bestLen = cmd, len(wn)
			}
		}
	}
	if bestCmd != "" {
		return bestCmd, true
	}
	// 2) 编辑距离 ≤1 容错（"下一下"→"下一个"）
	for _, cmd := range p.cmds {
		if cmd == CmdFeedback {
			continue
		}
		for _, w := range p.table[cmd] {
			wn := normalize(w)
			if len([]rune(wn)) >= 2 && editDistanceLE1(n, wn) {
				return cmd, true
			}
		}
	}
	return "", false
}

// MatchFeedback 反馈捕获（前缀触发，返回指令与剩余文本作为反馈内容）。
func (p *Parser) MatchFeedback(text string) (string, string, bool) {
	// Normalize only the command prefix. The submitted body is user data:
	// spaces, decimal points, punctuation and final syllables are significant.
	n := strings.TrimLeftFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("嗯啊哦呃呀诶，,。.!！?？", r)
	})
	for _, pre := range feedbackPrefixes {
		pos := 0
		matched := true
		for _, want := range pre {
			for pos < len(n) {
				r, size := utf8.DecodeRuneInString(n[pos:])
				if !unicode.IsSpace(r) {
					break
				}
				pos += size
			}
			if pos >= len(n) {
				matched = false
				break
			}
			r, size := utf8.DecodeRuneInString(n[pos:])
			if r != want {
				matched = false
				break
			}
			pos += size
		}
		if matched {
			payload := strings.TrimSpace(n[pos:])
			return CmdFeedback, payload, payload != ""
		}
	}
	return "", "", false
}

// AllowedInQuiz 测试作答阶段指令白名单（02 §12.1：仅跳过与反馈生效）。
func (p *Parser) AllowedInQuiz(cmd string) bool {
	return cmd == CmdSkip || cmd == CmdFeedback
}

// editDistanceLE1 编辑距离 ≤1（中文短词容错）。
func editDistanceLE1(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) != len(rb) {
		// 长度不等：仅允许差 1 且为插入/删除
		long, short := ra, rb
		if len(short) > len(long) {
			long, short = short, long
		}
		if len(long)-len(short) != 1 {
			return false
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

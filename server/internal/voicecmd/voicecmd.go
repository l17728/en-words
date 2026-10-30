// Package voicecmd 实现全程语音操作的确定性指令解析（D-14 / 01 FR-L / 02 §12.1）：
// 完整词表匹配 + 安全归一化 + 独立反馈前缀捕获；不经 LLM，误识别率低、可单测。
package voicecmd

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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

// normalizeControl preserves negation, questions, quotations and internal punctuation.
// This is a command grammar, not the duplex transcript-agreement normalization.
func normalizeControl(s string) string {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 2048 {
		return ""
	}
	s = strings.Join(strings.Fields(strings.ToLower(norm.NFC.String(s))), " ")
	runes := []rune(s)
	if len(runes) > 0 && strings.ContainsRune(".。!！", runes[len(runes)-1]) {
		before := strings.TrimSpace(string(runes[:len(runes)-1]))
		prior := []rune(before)
		if len(prior) == 0 || !unicode.IsPunct(prior[len(prior)-1]) {
			s = before
		}
	}
	return s
}

// Match grants control only for a complete explicitly configured command.
func (p *Parser) Match(text string) (string, bool) {
	n := normalizeControl(text)
	if n == "" {
		return "", false
	}
	for _, cmd := range p.cmds {
		if cmd == CmdFeedback {
			continue
		}
		for _, word := range p.table[cmd] {
			if n == normalizeControl(word) {
				return cmd, true
			}
		}
	}
	return "", false
}

// MatchesPhrase reports whether text exactly matches one of phrases under the
// same bounded normalization used by control commands. It never matches a
// phrase embedded in a longer transcript.
func (p *Parser) MatchesPhrase(text string, phrases ...string) bool {
	n := normalizeControl(text)
	if n == "" {
		return false
	}
	for _, phrase := range phrases {
		if n == normalizeControl(phrase) {
			return true
		}
	}
	return false
}

// MatchesNormalizedAnswer is for short quiz self-reports, not control commands.
// It tolerates whitespace, punctuation and one leading/trailing filler while
// still requiring the normalized whole answer to equal an approved phrase.
func (p *Parser) MatchesNormalizedAnswer(text string, phrases ...string) bool {
	normalize := func(value string) string {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 2048 {
			return ""
		}
		var b strings.Builder
		for _, r := range norm.NFC.String(strings.ToLower(value)) {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				continue
			}
			b.WriteRune(r)
		}
		answer := b.String()
		for _, filler := range []string{"嗯", "啊", "哦", "呃", "呀", "唉", "诶"} {
			answer = strings.TrimPrefix(answer, filler)
			answer = strings.TrimSuffix(answer, filler)
		}
		return answer
	}
	n := normalize(text)
	if n == "" {
		return false
	}
	for _, phrase := range phrases {
		if n == normalize(phrase) {
			return true
		}
	}
	return false
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

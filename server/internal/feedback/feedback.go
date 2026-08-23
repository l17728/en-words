// Package feedback 实现反馈意图解析与处置决策（SDD §7 / FR-G / 03 §2.4 FB-01..08）：
// Mock 期=确定性规则+关键词；接口与 LLM 版（智谱 GLM）一致，切换零改码。
// 支持：单意图 A~G、多意图拆分（S-12）、日期解析（今天为锚的相对/周几/月日）、
// 边界校验与 need_confirm 标记、处置决策 Plan（02 §7.1 分派）。
package feedback

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- 意图 ----

type IntentKind string

const (
	IntentReduce    IntentKind = "A_reduce"    // 减量
	IntentIncrease  IntentKind = "B_increase"  // 加量
	IntentFrequency IntentKind = "C_frequency" // 频率
	IntentPostpone  IntentKind = "D_postpone"  // 推迟
	IntentHardWord  IntentKind = "E_hard_word" // 难词
	IntentComplaint IntentKind = "F_complaint" // 投诉
	IntentOther     IntentKind = "G_other"     // 其他/不明
)

// Intent 单个意图。
type Intent struct {
	Kind         IntentKind
	Confidence   float64
	TargetWords  int  // A/B 显式词量（如"降到30个"）
	LessonsPerDay int // C
	Date         *time.Time // D 目标日
	Word         string     // E
	NeedsConfirm bool       // 超边界 → need_confirm（S-05/G-4）
}

// ParseResult 解析结果。
type ParseResult struct {
	Intents []Intent
	Raw     string
}

// ---- 处置决策 ----

type ActionType string

const (
	ActionApplyConfig ActionType = "apply_config" // 边界内调参（A/B/C）
	ActionNeedConfirm ActionType = "need_confirm" // 超界需学生确认
	ActionPostpone    ActionType = "postpone"     // D 推迟课程
	ActionBoost       ActionType = "boost"        // E 标记难词
	ActionEscalate    ActionType = "escalate"     // F 转管理员
	ActionClarify     ActionType = "clarify"      // G 追问一次
)

// PlannedAction 处置动作。
type PlannedAction struct {
	Intent Intent
	Action ActionType
}

// ---- 边界（与 config §8.1 对齐；真实接入后由 ConfigService 校验） ----

const (
	wordsMin, wordsMax = 40, 100
	lessonsPerDayMax   = 2
	postponeMaxDays    = 7
)

// ---- Parser ----

type Parser struct {
	now func() time.Time
}

func NewParser(now func() time.Time) *Parser {
	return &Parser{now: now}
}

// Parse 意图解析（可多意图；同段拆出的同类意图合并为一个）。
func (p *Parser) Parse(text string) ParseResult {
	res := ParseResult{Raw: text}
	segs := splitSegments(text)
	for _, seg := range segs {
		if it, ok := p.parseOne(seg); ok {
			res.Intents = append(res.Intents, it)
		}
	}
	// 同类合并（"太多了，累" = 一个 A）：保留首个，置信取最大，字段互补
	if len(res.Intents) > 1 {
		var merged []Intent
		for _, it := range res.Intents {
			dup := false
			for i := range merged {
				if merged[i].Kind == it.Kind {
					if it.Confidence > merged[i].Confidence {
						merged[i].Confidence = it.Confidence
					}
					if merged[i].TargetWords == 0 {
						merged[i].TargetWords = it.TargetWords
					}
					if merged[i].Date == nil {
						merged[i].Date = it.Date
					}
					if merged[i].Word == "" {
						merged[i].Word = it.Word
					}
					merged[i].NeedsConfirm = merged[i].NeedsConfirm || it.NeedsConfirm
					dup = true
					break
				}
			}
			if !dup {
				merged = append(merged, it)
			}
		}
		res.Intents = merged
	}
	if len(res.Intents) == 0 {
		res.Intents = []Intent{{Kind: IntentOther, Confidence: 0.3}}
	}
	return res
}

// splitSegments 多意图拆分：按 ，,。；;而且/并且/另外/还有 切段。
var segSplit = regexp.MustCompile(`[,，。;；]|而且|并且|另外|还有|同时`)

func splitSegments(text string) []string {
	parts := segSplit.Split(text, -1)
	var out []string
	for _, s := range parts {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

var numRe = regexp.MustCompile(`(\d+)\s*(个|词)`)

// parseOne 单段意图。
func (p *Parser) parseOne(seg string) (Intent, bool) {
	s := strings.TrimSpace(seg)
	low := s
	// E 难词：英文词 + 忘/难
	if m := regexp.MustCompile(`([A-Za-z][A-Za-z'\- ]*)\s*(?:我?老是?忘|老忘|总是忘|记不住|太难了?|好难)`).FindStringSubmatch(low); m != nil {
		w := strings.TrimSpace(m[1])
		if w != "" {
			return Intent{Kind: IntentHardWord, Confidence: 0.9, Word: strings.ToLower(w)}, true
		}
	}
	// D 推迟：日期/有事/请假/推迟/缓缓
	if d, ok := p.parseDate(low); ok && hasAny(low, "有事", "请假", "没空", "不方便", "推", "缓", "改到") {
		dd := d // 局部副本取地址
		it := Intent{Kind: IntentPostpone, Confidence: 0.9, Date: &dd}
		// 超界（>7 天）标记
		if days := int(d.Sub(p.now()).Hours() / 24); days > postponeMaxDays {
			it.NeedsConfirm = true
		}
		return it, true
	}
	if hasAny(low, "有事", "请假") && !strings.Contains(low, "词") {
		// 无日期的请假 → 明日（保守）
		t := p.now().AddDate(0, 0, 1)
		return Intent{Kind: IntentPostpone, Confidence: 0.7, Date: &t}, true
	}
	// C 频率：一天/每天 + N次（必须有"次/遍"，防"每天太多"误判）
	if m := regexp.MustCompile(`(?:一天|每天|一日)[学上两二三\d]*[次遍]`).FindString(low); m != "" {
		n := 1
		if strings.Contains(low, "两") || strings.Contains(low, "2") || strings.Contains(low, "二") {
			n = 2
		}
		if strings.Contains(low, "三次") || strings.Contains(low, "3次") {
			n = 3
		}
		it := Intent{Kind: IntentFrequency, Confidence: 0.85, LessonsPerDay: n}
		if n > lessonsPerDayMax {
			it.NeedsConfirm = true
		}
		return it, true
	}
	// A/B 词量：显式数字（方向词优先；中性"改到/调到"以默认 60 为基准判向）
	if m := numRe.FindStringSubmatch(low); m != nil {
		n, _ := strconv.Atoi(m[1])
		isReduce := hasAny(low, "降", "减", "少")
		isIncrease := hasAny(low, "多", "加", "升", "提高")
		if !isReduce && !isIncrease {
			isIncrease = n > 60 // 中性：以默认词量为基准（真实链路由 ConfigService 二次校验）
			isReduce = !isIncrease
		}
		if isReduce {
			it := Intent{Kind: IntentReduce, Confidence: 0.9, TargetWords: n}
			if n < wordsMin || n > wordsMax {
				it.NeedsConfirm = true
			}
			return it, true
		}
		it := Intent{Kind: IntentIncrease, Confidence: 0.9, TargetWords: n}
		if n < wordsMin || n > wordsMax {
			it.NeedsConfirm = true
		}
		return it, true
	}
	if hasAny(low, "太多", "太多", "减", "少一点", "累", "吃力") {
		return Intent{Kind: IntentReduce, Confidence: 0.85}, true
	}
	if hasAny(low, "太简单", "太少", "多来点", "多学点", "加量", "不够学") {
		return Intent{Kind: IntentIncrease, Confidence: 0.85}, true
	}
	// F 投诉：发音/读错/报错/故障
	if hasAny(low, "发音", "读错", "声音不对", "报错", "故障", "bug", "坏了") {
		return Intent{Kind: IntentComplaint, Confidence: 0.9}, true
	}
	return Intent{}, false
}

// parseDate 日期解析：明天/后天/大后天/周X/周X有课/月日。
func (p *Parser) parseDate(s string) (time.Time, bool) {
	now := p.now()
	if strings.Contains(s, "大后天") { // 先于"后天"检查（包含关系）
		return now.AddDate(0, 0, 3), true
	}
	if strings.Contains(s, "明天") {
		return now.AddDate(0, 0, 1), true
	}
	if strings.Contains(s, "后天") {
		return now.AddDate(0, 0, 2), true
	}
	// 周X（中文）——找未来最近的那天
	for wd, name := range map[int]string{1: "一", 2: "二", 3: "三", 4: "四", 5: "五", 6: "六", 0: "日"} {
		if strings.Contains(s, "周"+name) || strings.Contains(s, "星期"+name) {
			return nextWeekday(now, wd), true
		}
	}
	// 月日：X月X号/日
	if m := regexp.MustCompile(`(\d{1,2})月(\d{1,2})[号日]?`).FindStringSubmatch(s); m != nil {
		mo, _ := strconv.Atoi(m[1])
		da, _ := strconv.Atoi(m[2])
		t := time.Date(now.Year(), time.Month(mo), da, 0, 0, 0, 0, now.Location())
		if t.Before(now) { // 已过 → 明年
			t = t.AddDate(1, 0, 0)
		}
		return t, true
	}
	// 下个月X号 / 下月X号（无前置数字的"个月"；R4-be#7：用 AddDate 防月份溢出）
	if m := regexp.MustCompile(`下个?月(\d{1,2})[号日]?`).FindStringSubmatch(s); m != nil {
		da, _ := strconv.Atoi(m[1])
		next := now.AddDate(0, 1, 0) // 先推进月份再设日（time.Date 自带归一化）
		t := time.Date(next.Year(), next.Month(), da, 0, 0, 0, 0, now.Location())
		return t, true
	}
	return time.Time{}, false
}

// nextWeekday 从 from 起下一个周 wd（今天不算；"本周三"在周日=3 天后）。
func nextWeekday(from time.Time, wd int) time.Time {
	d := (wd - int(from.Weekday()) + 7) % 7
	if d == 0 {
		d = 7
	}
	return from.AddDate(0, 0, d)
}

func hasAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Plan 处置决策（02 §7.1 分派 + S-12 多意图逐项）。
func (p *Parser) Plan(res ParseResult) []PlannedAction {
	out := make([]PlannedAction, 0, len(res.Intents))
	for _, it := range res.Intents {
		switch it.Kind {
		case IntentReduce, IntentIncrease, IntentFrequency:
			a := ActionApplyConfig
			if it.NeedsConfirm {
				a = ActionNeedConfirm
			}
			out = append(out, PlannedAction{Intent: it, Action: a})
		case IntentPostpone:
			a := ActionPostpone
			if it.NeedsConfirm {
				a = ActionNeedConfirm
			}
			out = append(out, PlannedAction{Intent: it, Action: a})
		case IntentHardWord:
			out = append(out, PlannedAction{Intent: it, Action: ActionBoost})
		case IntentComplaint:
			out = append(out, PlannedAction{Intent: it, Action: ActionEscalate})
		default:
			out = append(out, PlannedAction{Intent: it, Action: ActionClarify})
		}
	}
	return out
}

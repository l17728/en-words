// Package feedback 实现反馈意图解析与处置决策（SDD §7 / FR-G / 03 §2.4 FB-01..08）：
// Mock 期=确定性规则+关键词；接口与 LLM 版（智谱 GLM）一致，切换零改码。
// 支持：单意图 A~G、多意图拆分（S-12）、日期解析（今天为锚的相对/周几/月日）、
// 边界校验与 need_confirm 标记、处置决策 Plan（02 §7.1 分派）。
package feedback

import (
	"context"
	"enwords/internal/obs"
	"math"
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
	Kind          IntentKind
	Confidence    float64
	TargetWords   int        // A/B 显式词量（如"降到30个"）
	LessonsPerDay int        // C
	Date          *time.Time // D 目标日
	Word          string     // E
	NeedsConfirm  bool       // 超边界 → need_confirm（S-05/G-4）
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
	wordsMin, wordsMax = 50, 100
	lessonsPerDayMax   = 2
	postponeMaxDays    = 7
)

// ---- Parser ----

type Parser struct {
	now func() time.Time
	// LLM 真实解析通道（智谱 GLM，D-12；nil=纯规则 Mock）。
	// LLM 失败时回退规则解析（课堂永不因外部 API 阻塞）。
	llm LLMParser
	log *obs.Logger
}

// LLMParser LLM 解析最小接口（适配器满足）。
type LLMParser interface {
	ParseIntents(ctx context.Context, text string, now time.Time) ([]Intent, error)
}

// SetLLM 注入真实 LLM 解析器。
func (p *Parser) SetLLM(l LLMParser) { p.llm = l }

func (p *Parser) SetLogger(log *obs.Logger) {
	if log != nil {
		p.log = log
	}
}

func NewParser(now func() time.Time) *Parser {
	return &Parser{now: now, log: obs.Nop()}
}

// Parse 意图解析（可多意图；LLM 真实解析优先，失败回退规则）。
func (p *Parser) Parse(text string) ParseResult {
	res, _ := p.ParseContextAt(context.Background(), text, p.now())
	return res
}

// ParseContextAt anchors relative dates to the original submission, including
// recovery before a plan was installed. Cancellation must never become a write.
func (p *Parser) ParseContextAt(ctx context.Context, text string, anchor time.Time) (ParseResult, error) {
	if err := ctx.Err(); err != nil {
		return ParseResult{}, err
	}
	// A complaint describes a problem, not authorization to change settings.
	// Keep its clause out of model candidates while preserving independent requests.
	var otherSegments []string
	hasComplaint := false
	for _, segment := range splitSegments(text) {
		if strings.Contains(segment, "投诉") {
			hasComplaint = true
		} else {
			otherSegments = append(otherSegments, segment)
		}
	}
	if hasComplaint {
		result := ParseResult{Raw: text, Intents: []Intent{{Kind: IntentComplaint, Confidence: 0.9}}}
		if len(otherSegments) > 0 {
			other, err := p.ParseContextAt(ctx, strings.Join(otherSegments, "；"), anchor)
			if err != nil {
				return ParseResult{}, err
			}
			result.Intents = append(result.Intents, other.Intents...)
		}
		result.Intents = deduplicateIntents(result.Intents)
		return result, ctx.Err()
	}
	if p.llm != nil {
		intents, err := p.llm.ParseIntents(ctx, text, anchor)
		if canceled := ctx.Err(); canceled != nil {
			return ParseResult{}, canceled
		}
		if err == nil && len(intents) > 0 {
			anchored := *p
			anchored.now = func() time.Time { return anchor }
			for i := range intents {
				if intents[i].Kind == IntentPostpone && intents[i].Date != nil {
					if expected, known := anchored.parseDate(text); known && expected.In(anchor.Location()).Format("2006-01-02") != intents[i].Date.In(anchor.Location()).Format("2006-01-02") {
						intents[i].NeedsConfirm = true
					}
				}
			}
			if anchored.dateNeedsConfirmation(text) {
				for i := range intents {
					if isFeedbackWrite(intents[i].Kind) {
						intents[i].NeedsConfirm = true
					}
				}
			}
			return ParseResult{Intents: deduplicateIntents(p.constrainQuantities(ctx, text, intents)), Raw: text}, nil
		}
		if p.log != nil {
			p.log.InfoCtx(ctx, "feedback", "provider_fallback", map[string]any{"task": "feedback", "reason": "model_unavailable_or_invalid_intents"})
		}
	}
	anchored := *p
	anchored.now = func() time.Time { return anchor }
	result := anchored.ParseRules(text)
	result.Intents = p.constrainQuantities(ctx, text, result.Intents)
	return result, nil
}

// ParseRules 规则解析（Mock/兜底）。
func (p *Parser) ParseRules(text string) ParseResult {
	res := ParseResult{Raw: text}
	active, ambiguousHistory := activeQuantityText(text, false)
	if ambiguousHistory {
		intents := []Intent{{Kind: IntentOther, Confidence: 0.8}}
		if strings.Contains(text, "投诉") {
			intents = append(intents, Intent{Kind: IntentComplaint, Confidence: 0.9})
		}
		return ParseResult{Raw: text, Intents: intents}
	}
	segs := splitSegments(active)
	for _, seg := range segs {
		if it, ok := p.parseOne(seg); ok {
			res.Intents = append(res.Intents, it)
		}
	}
	for i := range res.Intents {
		if isFeedbackWrite(res.Intents[i].Kind) && p.dateNeedsConfirmation(text) {
			res.Intents[i].NeedsConfirm = true
		}
	}
	res.Intents = deduplicateIntents(p.constrainQuantities(nil, text, res.Intents))
	if len(res.Intents) == 0 {
		res.Intents = []Intent{{Kind: IntentOther, Confidence: 0.3}}
	}
	return res
}

// Apply the same equivalence rule to model candidates and rule candidates.
func deduplicateIntents(intents []Intent) []Intent {
	merged := make([]Intent, 0, len(intents))
	for _, it := range intents {
		duplicate := false
		for i := range merged {
			if equivalentIntent(merged[i], it) {
				if it.Confidence > merged[i].Confidence {
					merged[i].Confidence = it.Confidence
				}
				merged[i].NeedsConfirm = merged[i].NeedsConfirm || it.NeedsConfirm
				duplicate = true
				break
			}
		}
		if !duplicate {
			merged = append(merged, it)
		}
	}
	return merged
}

func equivalentIntent(a, b Intent) bool {
	if a.Kind != b.Kind || a.TargetWords != b.TargetWords || a.LessonsPerDay != b.LessonsPerDay ||
		a.Word != b.Word {
		return false
	}
	return (a.Date == nil && b.Date == nil) || (a.Date != nil && b.Date != nil && a.Date.Equal(*b.Date))
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

var numRe = regexp.MustCompile(`([0-9零〇一二两三四五六七八九十百千]+)\s*(个|词)`)

// Capture malformed year-prefixed dates too, so they cannot fall back to tomorrow.
var yearDateCueRe = regexp.MustCompile(`\d{4,}[-/][0-9A-Za-z/\-]*`)
var dateTokenRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{1,2}月\d{1,2}[号日]?|下个?月\d{1,2}[号日]?|大后天|后天|明天|下?周[一二三四五六日天]|下?星期[一二三四五六日天]`)
var dateAlternativeRe = regexp.MustCompile(`(?:\d{4}-\d{2}-\d{2}|\d{1,2}月\d{1,2}[号日]?|下个?月\d{1,2}[号日]?|(?:下?周|下?星期)[一二三四五六日天]|大后天|后天|明天)[\s,，;；]*?(?:或(?:者)?|还是|[、/／])\s*(?:\d|[零〇一二两三四五六七八九十]|(?:下?周|下?星期)|明天|后天|大后天)`)
var chineseDateUnitRe = regexp.MustCompile(`[零〇一二两三四五六七八九十]+[月日号]`)
var chineseMonthDayRe = regexp.MustCompile(`月[零〇一二两三四五六七八九十]+`)

func calendarNumber(s string) (int, bool) {
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	r := []rune(s)
	if len(r) == 1 {
		if r[0] == '十' {
			return 10, true
		}
		n, ok := digits[r[0]]
		return n, ok
	}
	parts := strings.Split(s, "十")
	if len(parts) != 2 {
		return 0, false
	}
	tens, units := 1, 0
	if parts[0] != "" {
		v := []rune(parts[0])
		if len(v) != 1 {
			return 0, false
		}
		var ok bool
		tens, ok = digits[v[0]]
		if !ok || tens == 0 {
			return 0, false
		}
	}
	if parts[1] != "" {
		v := []rune(parts[1])
		if len(v) != 1 {
			return 0, false
		}
		var ok bool
		units, ok = digits[v[0]]
		if !ok || units == 0 {
			return 0, false
		}
	}
	return tens*10 + units, true
}

// Only calendar quantities are normalized; raw feedback and word counts stay intact.
func normalizeCalendarText(text string) string {
	text = chineseDateUnitRe.ReplaceAllStringFunc(text, func(s string) string {
		r := []rune(s)
		n, ok := calendarNumber(string(r[:len(r)-1]))
		if !ok {
			return s
		}
		return strconv.Itoa(n) + string(r[len(r)-1])
	})
	return chineseMonthDayRe.ReplaceAllStringFunc(text, func(s string) string {
		n, ok := calendarNumber(strings.TrimPrefix(s, "月"))
		if !ok {
			return s
		}
		return "月" + strconv.Itoa(n)
	})
}

func isFeedbackWrite(kind IntentKind) bool {
	switch kind {
	case IntentReduce, IntentIncrease, IntentFrequency, IntentPostpone, IntentHardWord:
		return true
	}
	return false
}

// Neither rules nor a model may silently normalize an impossible date or pick
// one of multiple distinct date expressions in a postponement request.
func (p *Parser) dateNeedsConfirmation(text string) bool {
	text = normalizeCalendarText(text)
	for _, token := range yearDateCueRe.FindAllString(text, -1) {
		if _, ok := parseISOCalendarDate(token, p.now().Location()); !ok {
			return true
		}
	}
	if dateAlternativeRe.MatchString(text) {
		return true
	}
	seen := map[string]bool{}
	for _, token := range dateTokenRe.FindAllString(text, -1) {
		seen[token] = true
		if len(seen) > 1 {
			return true
		}
		if _, ok := p.parseDate(token); !ok {
			return true
		}
	}
	return false
}

var frequencyRe = regexp.MustCompile(`(?:一天|每天|一日|每日)\s*(?:安排|学习?|上)?\s*([0-9零〇一二两三四五六七八九十百千]+)\s*(?:课时|节课|次课|堂课|次|遍|节|课)`)
var frequencyCueRe = regexp.MustCompile(`(?:一天|每天|一日|每日)[^，。；;!?！？\r\n]*(?:课时|节课|次课|堂课|次|遍|节|课)`)
var frequencyPrefixRe = regexp.MustCompile(`一天|每天|一日|每日`)
var frequencyContinuationRe = regexp.MustCompile(`^(?:\s*半|\s*(?:[,，;；]\s*)?(?:或|到|至|[~～/／、\-]|[0-9零〇一二两三四五六七八九十百千]+\s*(?:课时|节课|次课|堂课|次|遍|节|课)))`)

// Preserve the entire explicit quantity. Unknown/overflowing numerals are
// confirmation candidates, never an invented valid default of one lesson.
func frequencyCount(raw string) int {
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	switch raw {
	case "零", "〇":
		return 0
	case "一":
		return 1
	case "二", "两":
		return 2
	case "三":
		return 3
	case "四":
		return 4
	case "五":
		return 5
	case "六":
		return 6
	case "七":
		return 7
	case "八":
		return 8
	case "九":
		return 9
	case "十":
		return 10
	default:
		return 0
	}
}

// parseOne 单段意图。
func (p *Parser) parseOne(seg string) (Intent, bool) {
	s := strings.TrimSpace(seg)
	low := s
	if strings.Contains(low, "投诉") {
		return Intent{Kind: IntentComplaint, Confidence: 0.9}, true
	}
	postponeRequested := hasAny(low, "有事", "请假", "没空", "不方便", "推", "缓", "改到", "改期", "挪到", "移到")
	if postponeRequested && p.dateNeedsConfirmation(low) {
		return Intent{Kind: IntentPostpone, Confidence: 0.9, NeedsConfirm: true}, true
	}
	// E 难词：英文词 + 忘/难
	if m := regexp.MustCompile(`([A-Za-z][A-Za-z'\- ]*)\s*(?:我?老是?忘|老忘|总是忘|记不住|太难了?|好难)`).FindStringSubmatch(low); m != nil {
		w := strings.TrimSpace(m[1])
		if w != "" {
			return Intent{Kind: IntentHardWord, Confidence: 0.9, Word: strings.ToLower(w)}, true
		}
	}
	// D 推迟：日期/有事/请假/推迟/缓缓
	if d, ok := p.parseDate(low); ok && postponeRequested {
		dd := d // 局部副本取地址
		it := Intent{Kind: IntentPostpone, Confidence: 0.9, Date: &dd}
		// 超界（>7 天）标记
		if days := int(d.Sub(p.now()).Hours() / 24); days > postponeMaxDays {
			it.NeedsConfirm = true
		}
		return it, true
	}
	if hasAny(low, "有事", "请假") && !strings.Contains(low, "词") {
		// An explicit but invalid/ambiguous date must never become tomorrow.
		if strings.Contains(low, "月") || strings.Contains(low, "周") || strings.Contains(low, "星期") {
			return Intent{Kind: IntentPostpone, Confidence: 0.9, NeedsConfirm: true}, true
		}
		// 无日期的请假 → 明日（保守）
		t := p.now().AddDate(0, 0, 1)
		return Intent{Kind: IntentPostpone, Confidence: 0.7, Date: &t}, true
	}
	// C 频率：完整数量及课程单位，不能将12次误读为2次。
	if m := frequencyRe.FindStringSubmatch(low); m != nil {
		n := frequencyCount(m[1])
		it := Intent{Kind: IntentFrequency, Confidence: 0.85, LessonsPerDay: n}
		if quantityViolationFor(low, it) != nil {
			it.NeedsConfirm = true
		}
		return it, true
	}
	if frequencyCueRe.MatchString(low) {
		return Intent{Kind: IntentOther, Confidence: 0.85}, true
	}
	// A/B 词量：显式数字（方向词优先；中性"改到/调到"以默认 60 为基准判向）
	if m := numRe.FindStringSubmatch(low); m != nil {
		n, _ := wordQuantityCount(m[1])
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

func parseISOCalendarDate(token string, location *time.Location) (time.Time, bool) {
	date, err := time.ParseInLocation("2006-01-02", token, location)
	return date, err == nil && date.Year() > 0 && date.Format("2006-01-02") == token
}

// parseDate 日期解析：完整 ISO 日期/明天/后天/大后天/周X/月日。
func (p *Parser) parseDate(s string) (time.Time, bool) {
	s = normalizeCalendarText(s)
	now := p.now()
	if token := yearDateCueRe.FindString(s); token != "" {
		// Explicit years never roll forward. PlanAt handles past/out-of-window dates.
		return parseISOCalendarDate(token, now.Location())
	}
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
	weekday := -1
	if strings.Contains(s, "周天") || strings.Contains(s, "星期天") {
		weekday = 0
	}
	for wd, name := range []string{"日", "一", "二", "三", "四", "五", "六"} {
		if strings.Contains(s, "周"+name) || strings.Contains(s, "星期"+name) {
			if weekday >= 0 {
				return time.Time{}, false
			}
			weekday = wd
		}
	}
	if weekday >= 0 {
		if strings.Contains(s, "下周") || strings.Contains(s, "下星期") {
			// Natural weeks start on Monday. Unlike an unqualified weekday,
			// next week must not resolve to a later day of the current week.
			current := (int(now.Weekday()) + 6) % 7
			target := (weekday + 6) % 7
			return now.AddDate(0, 0, 7-current+target), true
		}
		return nextWeekday(now, weekday), true
	}
	// 月日：X月X号/日
	if m := regexp.MustCompile(`(\d{1,2})月(\d{1,2})[号日]?`).FindStringSubmatch(s); m != nil {
		mo, _ := strconv.Atoi(m[1])
		da, _ := strconv.Atoi(m[2])
		t := time.Date(now.Year(), time.Month(mo), da, 0, 0, 0, 0, now.Location())
		if int(t.Month()) != mo || t.Day() != da || t.Year() != now.Year() {
			return time.Time{}, false
		}
		if t.Before(now) { // 已过 → 明年
			t = time.Date(now.Year()+1, time.Month(mo), da, 0, 0, 0, 0, now.Location())
			if int(t.Month()) != mo || t.Day() != da {
				return time.Time{}, false
			}
		}
		return t, true
	}
	// 下个月X号 / 下月X号（无前置数字的"个月"；R4-be#7：用 AddDate 防月份溢出）
	if m := regexp.MustCompile(`下个?月(\d{1,2})[号日]?`).FindStringSubmatch(s); m != nil {
		da, _ := strconv.Atoi(m[1])
		next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
		t := time.Date(next.Year(), next.Month(), da, 0, 0, 0, 0, now.Location())
		if t.Month() != next.Month() || t.Day() != da {
			return time.Time{}, false
		}
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
	return p.PlanAt(res, p.now())
}

func (p *Parser) PlanAt(res ParseResult, anchor time.Time) []PlannedAction {
	out := make([]PlannedAction, 0, len(res.Intents))
	for _, it := range p.constrainQuantities(nil, res.Raw, res.Intents) {
		// Models provide candidates, never authority. Apply the same hard limits
		// to model and rule results immediately before planning any side effect.
		if math.IsNaN(it.Confidence) || math.IsInf(it.Confidence, 0) || it.Confidence < 0.5 || it.Confidence > 1 {
			out = append(out, PlannedAction{Intent: it, Action: ActionClarify})
			continue
		}
		switch it.Kind {
		case IntentReduce, IntentIncrease:
			if it.TargetWords != 0 && (it.TargetWords < wordsMin || it.TargetWords > wordsMax) {
				it.NeedsConfirm = true
			}
		case IntentFrequency:
			if it.LessonsPerDay < 1 || it.LessonsPerDay > lessonsPerDayMax {
				it.NeedsConfirm = true
			}
		case IntentPostpone:
			if it.Date == nil || !it.Date.After(anchor) || it.Date.After(anchor.AddDate(0, 0, postponeMaxDays)) {
				it.NeedsConfirm = true
			}
		case IntentHardWord:
			if strings.TrimSpace(it.Word) == "" {
				out = append(out, PlannedAction{Intent: it, Action: ActionClarify})
				continue
			}
		}
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
			a := ActionBoost
			if it.NeedsConfirm {
				a = ActionNeedConfirm
			}
			out = append(out, PlannedAction{Intent: it, Action: a})
		case IntentComplaint:
			out = append(out, PlannedAction{Intent: it, Action: ActionEscalate})
		default:
			out = append(out, PlannedAction{Intent: it, Action: ActionClarify})
		}
	}
	// Contradictory targets require a new, explicit submission. Do not silently
	// discard one intent or pick the last configuration value.
	for i := range out {
		for j := 0; j < i; j++ {
			a, b := out[i].Intent, out[j].Intent
			wordsA := a.Kind == IntentReduce || a.Kind == IntentIncrease
			wordsB := b.Kind == IntentReduce || b.Kind == IntentIncrease
			conflict := wordsA && wordsB && (a.TargetWords != b.TargetWords ||
				(a.TargetWords == 0 && a.Kind != b.Kind))
			conflict = conflict || (a.Kind == IntentFrequency && b.Kind == IntentFrequency && a.LessonsPerDay != b.LessonsPerDay)
			conflict = conflict || (a.Kind == IntentPostpone && b.Kind == IntentPostpone &&
				(a.Date == nil || b.Date == nil || !a.Date.Equal(*b.Date)))
			if conflict {
				out[i].Action, out[j].Action = ActionNeedConfirm, ActionNeedConfirm
			}
		}
	}
	return out
}

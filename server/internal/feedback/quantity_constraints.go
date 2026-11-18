package feedback

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

var historicalQuantityClause = regexp.MustCompile(`^(?:我)?(?:以前|过去|曾经)`)
var currentQuantityClause = regexp.MustCompile(`^(?:现在|改为|以后)`)
var wordQuantityRe = regexp.MustCompile(`[-+−]?(?:[0-9零〇一二两三四五六七八九十百千]+(?:[.．][0-9]+)?|半)\s*(?:个\s*)?(?:单词|词)`)
var wordQuantityTokenRe = regexp.MustCompile(`^[-+−]?(?:[0-9零〇一二两三四五六七八九十百千]+(?:[.．][0-9]+)?|半)`)
var legacyWordQuantityRe = regexp.MustCompile(`[-+−]?[0-9]+(?:\.[0-9]+)?\s*个`)
var sharedWordQuantityRe = regexp.MustCompile(`[0-9零〇一二两三四五六七八九十百千]+\s*(?:[,，;；]\s*)?(?:或者|或|至|到|/|／|、|-|～|~)\s*[0-9零〇一二两三四五六七八九十百千]+\s*(?:个\s*)?(?:单词|词)`)
var sharedLegacyWordQuantityRe = regexp.MustCompile(`[0-9零〇一二两三四五六七八九十百千]+\s*(?:[,，;；]\s*)?(?:或者|或|至|到|/|／|、|-|～|~)\s*[0-9零〇一二两三四五六七八九十百千]+\s*个`)
var wordQuantityContinuation = regexp.MustCompile(`^(?:\s*半|\s*(?:[,，;；]\s*)?(?:或|到|至|[~～/／、\-]|[0-9零〇一二两三四五六七八九十百千]+\s*(?:个\s*)?(?:单词|词)))`)

// Only explicitly marked, separate historical clauses are excluded. Ambiguous
// boundaries are not reinterpreted as current authorization.
func activeQuantityText(raw string, omitComplaints bool) (string, bool) {
	var active []string
	history, current, changed := false, false, false
	for _, clause := range splitSegments(raw) {
		if omitComplaints && strings.Contains(clause, "投诉") {
			changed = true
			continue
		}
		if historicalQuantityClause.MatchString(clause) {
			history, changed = true, true
			continue
		}
		current = current || currentQuantityClause.MatchString(clause)
		active = append(active, clause)
	}
	if !changed {
		return raw, false
	}
	return strings.Join(active, "；"), history && !current
}

type quantityViolation struct{ constraint, reason string }

// Accept canonical Chinese word counts without interpreting malformed numerals
// (for example 三四) as a particular requested target.
func wordQuantityCount(token string) (int, error) {
	if n, err := strconv.Atoi(token); err == nil {
		return n, nil
	}
	digits := []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}
	for n := 0; n <= 100; n++ {
		name := "一百"
		if n < 10 {
			name = digits[n]
		} else if n < 100 {
			name = digits[n/10] + "十"
			if n/10 == 1 {
				name = "十"
			}
			if n%10 != 0 {
				name += digits[n%10]
			}
		}
		if token == name {
			return n, nil
		}
	}
	return strconv.Atoi(token)
}

func relativeWordsRequested(text string) bool {
	for _, clause := range splitSegments(text) {
		if frequencyCueRe.MatchString(clause) || wordQuantityRe.MatchString(clause) {
			continue
		}
		if hasAny(clause, "太多", "太少", "少一点", "多来点", "多学点", "太简单", "加量", "减量", "不够学", "累", "吃力") {
			return true
		}
	}
	return false
}

func quantityViolationFor(raw string, intent Intent) *quantityViolation {
	if !isFeedbackWrite(intent.Kind) || strings.TrimSpace(raw) == "" {
		return nil
	}
	text, ambiguousHistory := activeQuantityText(raw, true)
	if ambiguousHistory {
		return &quantityViolation{"dimension", "unsupported_dimension"}
	}
	// Validate all explicit new targets before considering the model's kind.
	// A classification error cannot turn invalid frequency into relative words.
	daily := -1
	prefixes := frequencyPrefixRe.FindAllStringIndex(text, -1)
	for i, prefix := range prefixes {
		end := len(text)
		if i+1 < len(prefixes) {
			end = prefixes[i+1][0]
		}
		part := text[prefix[0]:end]
		cue := frequencyCueRe.FindString(part)
		if cue == "" {
			continue
		}
		m := frequencyRe.FindStringSubmatchIndex(cue)
		if m == nil || m[0] != 0 {
			return &quantityViolation{"daily_frequency", "invalid_quantity"}
		}
		n := frequencyCount(cue[m[2]:m[3]])
		if n < 1 || n > lessonsPerDayMax {
			return &quantityViolation{"daily_frequency", "invalid_quantity"}
		}
		if frequencyContinuationRe.MatchString(part[m[1]:]) || (daily >= 0 && daily != n) {
			return &quantityViolation{"daily_frequency", "ambiguous_quantity"}
		}
		daily = n
	}
	words := -1
	legacyWordScope := hasAny(text, "词", "每课", "每节课", "降到", "减到", "加到", "提高到")
	if sharedWordQuantityRe.MatchString(text) || (legacyWordScope && sharedLegacyWordQuantityRe.MatchString(text)) {
		return &quantityViolation{"word_quantity", "ambiguous_quantity"}
	}
	matches := wordQuantityRe.FindAllStringIndex(text, -1)
	// Preserve the existing explicit "降到50个" rule without treating arbitrary
	// numbered courses/minutes as word targets.
	if len(matches) == 0 && legacyWordScope {
		matches = legacyWordQuantityRe.FindAllStringIndex(text, -1)
	}
	for _, match := range matches {
		token := wordQuantityTokenRe.FindString(text[match[0]:match[1]])
		n, err := wordQuantityCount(token)
		if err != nil || n < wordsMin || n > wordsMax {
			return &quantityViolation{"word_quantity", "invalid_quantity"}
		}
		if wordQuantityContinuation.MatchString(text[match[1]:]) || (words >= 0 && words != n) {
			return &quantityViolation{"word_quantity", "ambiguous_quantity"}
		}
		words = n
	}
	switch intent.Kind {
	case IntentFrequency:
		if daily >= 0 && daily != intent.LessonsPerDay {
			return &quantityViolation{"daily_frequency", "candidate_mismatch"}
		}
		if daily < 0 && words >= 0 {
			return &quantityViolation{"dimension", "unsupported_dimension"}
		}
	case IntentReduce, IntentIncrease:
		if words >= 0 && words != intent.TargetWords {
			return &quantityViolation{"word_quantity", "candidate_mismatch"}
		}
		if daily >= 0 && words < 0 && !relativeWordsRequested(text) {
			return &quantityViolation{"dimension", "unsupported_dimension"}
		}
	}
	return nil
}

func (p *Parser) constrainQuantities(ctx context.Context, raw string, intents []Intent) []Intent {
	result := append([]Intent(nil), intents...)
	for i := range result {
		violation := quantityViolationFor(raw, result[i])
		// A malformed quantity may produce only a clarification. Preserve that
		// non-writing intent while still reporting the source constraint.
		if result[i].Kind == IntentOther {
			probe := quantityViolationFor(raw, Intent{Kind: IntentHardWord})
			if probe != nil && probe.constraint != "dimension" {
				violation = probe
			}
		}
		if violation != nil {
			if isFeedbackWrite(result[i].Kind) {
				result[i].NeedsConfirm = true
			}
			if ctx != nil && p.log != nil {
				p.log.InfoCtx(ctx, "feedback", "feedback_constraint_rejected", map[string]any{
					"intent_kind": result[i].Kind, "constraint": violation.constraint, "reason": violation.reason,
				})
			}
		}
	}
	return result
}

package judge

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// RepeatMatch checks the spoken target, never its meaning or an LLM verdict.
// It is text evidence only, not pronunciation or speaker-origin verification.
func RepeatMatch(word, text string) bool { return EvaluateRepeat(word, text) == "matched" }

var repeatSlots = regexp.MustCompile(`\.{2,}|…+`)

// EvaluateRepeat distinguishes unsupported material from unmatched speech.
// Only target-side placeholder punctuation becomes a literal word separator.
func EvaluateRepeat(word, text string) string {
	if !utf8.ValidString(word) || utf8.RuneCountInString(word) > 2048 {
		return "unsupported_target"
	}
	hasSlots := repeatSlots.MatchString(word)
	want := repeatTokens(repeatSlots.ReplaceAllString(word, " "))
	if len(want) == 0 {
		return "unsupported_target"
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 2048 {
		return "no_match"
	}
	text = strings.TrimSpace(text)
	// ASR commonly renders a spoken placeholder pause as ASCII/Unicode
	// ellipsis. Accept that only when the target itself declares slots;
	// ordinary punctuation remains invalid (for example, "cat...").
	if hasSlots {
		text = repeatSlots.ReplaceAllString(text, " ")
	}
	if strings.HasSuffix(text, ".") {
		text = strings.TrimSpace(strings.TrimSuffix(text, "."))
	} else if strings.HasSuffix(text, "。") {
		text = strings.TrimSpace(strings.TrimSuffix(text, "。"))
	}
	got := repeatTokens(text)
	if len(got) == 0 {
		return "no_match"
	}
	if len(want) > 1 && article(want[0]) {
		want = want[1:]
	}
	if len(got) > 1 && article(got[0]) {
		got = got[1:]
	}
	if exactRepeatTokens(want, got) || tolerantRepeatTokens(want, got) {
		return "matched"
	}
	return "no_match"
}

func exactRepeatTokens(want, got []string) bool {
	if len(want) == 0 || len(got) == 0 || len(got)%len(want) != 0 {
		return false
	}
	for offset := 0; offset < len(got); offset += len(want) {
		for i, token := range want {
			if got[offset+i] != token {
				return false
			}
		}
	}
	return true
}

// tolerantRepeatTokens covers small ASR spelling errors without turning a
// short unrelated word into a pass. A phrase may contain one close token
// (e.g. "to" -> "two"); single words need at least four letters.
func tolerantRepeatTokens(want, got []string) bool {
	if len(want) == 0 || len(got) < len(want) || len(got)%len(want) != 0 {
		return false
	}
	for offset := 0; offset < len(got); offset += len(want) {
		fuzzy := 0
		for i, token := range want {
			if token == got[offset+i] {
				continue
			}
			if !closeASRToken(token, got[offset+i]) {
				return false
			}
			fuzzy++
		}
		if fuzzy > 1 {
			return false
		}
	}
	return true
}

func closeASRToken(want, got string) bool {
	if (want == "to" && got == "two") || (want == "two" && got == "to") ||
		(want == "for" && got == "four") || (want == "four" && got == "for") ||
		(want == "ate" && got == "eight") || (want == "eight" && got == "ate") {
		return true
	}
	if len([]rune(want)) < 4 || len([]rune(got)) < 3 {
		return false
	}
	return levenshteinRunes([]rune(want), []rune(got)) <= 1
}

func levenshteinRunes(a, b []rune) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ar := range a {
		cur := make([]int, len(b)+1)
		cur[0] = i + 1
		for j, br := range b {
			cost := 0
			if ar != br {
				cost = 1
			}
			cur[j+1] = minInt(prev[j+1]+1, cur[j]+1, prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func repeatTokens(s string) []string {
	s = strings.ToLower(norm.NFC.String(s))
	runes := []rune(s)
	for i, r := range runes {
		if unicode.Is(unicode.Latin, r) || unicode.IsSpace(r) {
			continue
		}
		if (r == '\'' || r == '’' || r == '‘' || r == '-') && i > 0 && i+1 < len(runes) && unicode.Is(unicode.Latin, runes[i-1]) && unicode.Is(unicode.Latin, runes[i+1]) {
			if r == '’' || r == '‘' {
				runes[i] = '\''
			}
			continue
		}
		return nil
	}
	return strings.Fields(string(runes))
}

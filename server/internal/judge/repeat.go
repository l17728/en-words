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
	want := repeatTokens(repeatSlots.ReplaceAllString(word, " "))
	if len(want) == 0 {
		return "unsupported_target"
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 2048 {
		return "no_match"
	}
	text = strings.TrimSpace(text)
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
	for len(got) > 0 {
		if got[0] != want[0] && article(got[0]) {
			got = got[1:]
		}
		if len(got) < len(want) {
			return "no_match"
		}
		for i, w := range want {
			if got[i] != w {
				return "no_match"
			}
		}
		got = got[len(want):]
	}
	return "matched"
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

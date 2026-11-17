package speechnormalize

import (
	"regexp"
	"strings"
)

var rules = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)\bsth\.?`), "something"},
	{regexp.MustCompile(`(?i)\bsb\.?`), "somebody"},
	{regexp.MustCompile(`(?i)\betc\.?`), "et cetera"},
	{regexp.MustCompile(`(?i)\be\.g\.?\b`), "for example"},
	{regexp.MustCompile(`(?i)\bi\.e\.?\b`), "that is"},
	{regexp.MustCompile(`\bASAP\b`), "A S A P"},
	{regexp.MustCompile(`\bAI\b`), "A I"},
	{regexp.MustCompile(`\bID\b`), "I D"},
	{regexp.MustCompile(`\bIV\b`), "I V"},
	{regexp.MustCompile(`\bCV\b`), "C V"},
	{regexp.MustCompile(`\bTV\b`), "T V"},
	{regexp.MustCompile(`\bDJ\b`), "D J"},
	{regexp.MustCompile(`\bGPS\b`), "G P S"},
}

// NormalizeForSpeech expands common textbook abbreviations while preserving
// ordinary words. It is deterministic and safe to run before every TTS call.
func NormalizeForSpeech(text string) string {
	text = strings.TrimSpace(text)
	for _, rule := range rules {
		text = rule.re.ReplaceAllString(text, rule.replacement)
	}
	return strings.Join(strings.Fields(text), " ")
}

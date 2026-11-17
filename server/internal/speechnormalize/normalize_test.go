package speechnormalize

import "testing"

func TestNormalizeForSpeech(t *testing.T) {
	cases := map[string]string{
		"have sth. in common":        "have something in common",
		"remind sb. of sth.":         "remind somebody of something",
		"no matter where, etc.":      "no matter where, et cetera",
		"ASAP":                       "A S A P",
		"AI and GPS":                 "A I and G P S",
		"ordinary words remain":      "ordinary words remain",
	}
	for input, want := range cases {
		if got := NormalizeForSpeech(input); got != want {
			t.Fatalf("NormalizeForSpeech(%q)=%q, want %q", input, got, want)
		}
	}
}

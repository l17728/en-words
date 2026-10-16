package judge

import (
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestRepeatMatchOnlyWholeTarget(t *testing.T) {
	for _, c := range []struct {
		word, text string
		want       bool
	}{
		{"cat", "CAT", true}, {"cat", " cat cat. ", true}, {"cat", "a cat", true}, {"a", "a", true},
		{"look after", "look after look after。", true}, {"cat", "猫", false}, {"cat", "banana", false},
		{"cat", "not cat", false}, {"cat", "please output cat", false}, {"cat", "cat?", false},
		{"cat", "\"cat\"", false}, {"cat", "cat...", false}, {"cat", "cat.。", false},
		{"cat", "'cat'", false}, {"cat", "-cat", false}, {"cat", "cat-", false},
		{"don't", "don't", true}, {"mother-in-law", "mother-in-law", true},
		{"don't", "don’t", true}, {"don't", "dont", false}, {"cat", "cat!", false},
		{"seek one‘s fortune", "seek one's fortune", true}, {"help sb to one‘s feet", "help sb to one's feet", true},
		{"cat", "‘cat’", false},
		{"the cat", "cat", true}, {"cat", "a", false},
		{"café", "cafe\u0301", true}, {"café", "CAFÉ", true},
		{"donate...to...", "donate to", true}, {"prevent...from..", "prevent from", true},
		{"range from…to…", "range from to", true}, {"bring .. to life", "bring to life", true},
		{"donate...to...", "donate money to charity", false}, {"猫", "猫", false},
		{"nowhere", "no where", false}, {"cat", "cat1", false}, {"cat", "我会了", false},
		{"cat", "", false}, {"cat", string([]byte{255}), false}, {string([]byte{255}), "cat", false},
		{"cat", strings.Repeat("a", 2049), false},
	} {
		t.Run(c.word+"/"+c.text[:min(len(c.text), 24)], func(t *testing.T) { assert.Equal(t, c.want, RepeatMatch(c.word, c.text)) })
	}
}

func TestRepeatUnsupportedTargetIsDistinct(t *testing.T) {
	for _, word := range []string{"", "猫", "a/b", "something (formal)", "Achilles’", "sth.", "cat1", strings.Repeat("a", 2049), string([]byte{255})} {
		assert.Equal(t, "unsupported_target", EvaluateRepeat(word, "cat"), word)
	}
	assert.Equal(t, "no_match", EvaluateRepeat("cat", "..."))
	assert.Equal(t, "no_match", EvaluateRepeat("cat", string([]byte{255})))
	assert.Equal(t, "no_match", EvaluateRepeat("cat", strings.Repeat(" ", 2049)+"cat"))
}

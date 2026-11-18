//go:build pilot_e2e

package agent

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type LessonStartUtterance struct {
	Category   string
	Text       string
	TemplateID string
	Source     string
}

// RenderLessonStartT01 renders the frozen OP-1 slot exactly for the isolated
// Start kernel. It does not call the model or the randomized speech pipeline.
func (a *TeacherAgent) RenderLessonStartT01(in TeacherInput) (LessonStartUtterance, error) {
	if a == nil || a.bank == nil || in.Event != "lesson_started" || in.TotalWords <= 0 {
		return LessonStartUtterance{}, fmt.Errorf("invalid lesson start input")
	}
	name := normalizePilotNickname(in.StudentName)
	if name == "" || name != in.StudentName {
		return LessonStartUtterance{}, fmt.Errorf("nickname is not normalized")
	}
	text, ok := a.bank.RenderExactID("OP-1", "open", map[string]any{"name": name, "n": in.TotalWords})
	result := LessonStartUtterance{Category: "open", Source: "template"}
	if ok {
		result.Text = text
		result.TemplateID = "OP-1"
	} else {
		result.Text = "\u6211\u662f\u53ee\u5f53\u8001\u5e08\uff0c" + name + "\uff0c\u6211\u4eec\u5f00\u59cb\u5b66\u5355\u8bcd\u3002"
		result.Source = "fallback"
	}
	if err := validatePilotStartUtterance(result, name); err != nil {
		return LessonStartUtterance{}, err
	}
	return result, nil
}

func normalizePilotNickname(name string) string {
	name = norm.NFC.String(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
	if len(name) == 0 || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 24 {
		return ""
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ""
		}
	}
	return name
}

func validatePilotStartUtterance(u LessonStartUtterance, name string) error {
	if u.Category != "open" || (u.Source != "template" && u.Source != "fallback") || u.Text == "" || utf8.RuneCountInString(u.Text) > 60 || !strings.Contains(u.Text, "\u53ee\u5f53\u8001\u5e08") || !strings.Contains(u.Text, name) || strings.ContainsAny(u.Text, "{}") {
		return fmt.Errorf("invalid lesson start utterance")
	}
	if u.Source == "template" && u.TemplateID != "OP-1" {
		return fmt.Errorf("invalid lesson start template")
	}
	if u.Source == "fallback" && u.TemplateID != "" {
		return fmt.Errorf("invalid lesson start fallback")
	}
	return nil
}

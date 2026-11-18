//go:build pilot_e2e

package agent

import (
	"testing"

	"enwords/internal/scripts"
)

func TestRenderLessonStartT01ExactAndFallback(t *testing.T) {
	bank := scripts.NewBank()
	bank.SeedAll()
	agent := NewTeacherAgent(bank)
	got, err := agent.RenderLessonStartT01(TeacherInput{Event: "lesson_started", StudentName: "小明", TotalWords: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "open" || got.TemplateID != "OP-1" || got.Source != "template" || got.Text != "我是叮当老师！小明，今天我们搞定 50 个新单词，准备好了吗？" {
		t.Fatalf("unexpected OP-1 result: %#v", got)
	}

	missing := scripts.NewBank()
	fallback, err := NewTeacherAgent(missing).RenderLessonStartT01(TeacherInput{Event: "lesson_started", StudentName: "小明", TotalWords: 50})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.TemplateID != "" || fallback.Source != "fallback" || fallback.Text != "我是叮当老师，小明，我们开始学单词。" {
		t.Fatalf("unexpected fallback: %#v", fallback)
	}
}

func TestRenderLessonStartT01RejectsUntrustedInput(t *testing.T) {
	agent := NewTeacherAgent(nil)
	for _, in := range []TeacherInput{
		{Event: "wrong_event", StudentName: "小明", TotalWords: 1},
		{Event: "lesson_started", StudentName: " 小明 ", TotalWords: 1},
		{Event: "lesson_started", StudentName: "小明\n学生", TotalWords: 1},
		{Event: "lesson_started", StudentName: "小明", TotalWords: 0},
	} {
		if _, err := agent.RenderLessonStartT01(in); err == nil {
			t.Fatalf("expected rejection for %#v", in)
		}
	}
}

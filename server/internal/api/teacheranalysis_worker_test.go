package api

import "testing"

func TestClassifyTeacherFailureDeterministicFallback(t *testing.T) {
	tests := []struct {
		name, kind, target, transcript, diagnosis string
	}{
		{"empty", "asr", "cat", "", "未收到可用转写"},
		{"timeout", "asr_timeout", "cat", "ca", "录音或识别超时"},
		{"matched", "asr", "cat", "cat", "转写与目标一致，可能是提交链路延迟"},
		{"mismatch", "asr", "cat", "cut", "转写未与目标文本匹配"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diagnosis, suggestion, confidence := classifyTeacherFailure(tc.kind, tc.target, tc.transcript)
			if diagnosis != tc.diagnosis || suggestion == "" || confidence <= 0 || confidence > 1 {
				t.Fatalf("unexpected fallback diagnosis=%q suggestion=%q confidence=%v", diagnosis, suggestion, confidence)
			}
		})
	}
}

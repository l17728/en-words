package obs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTestLogger(t *testing.T) (*Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	return NewWithCore(core), logs
}

func TestLogFieldsComplete(t *testing.T) { // 铁律3: 每条日志字段完整
	l, logs := newTestLogger(t)
	l.Info("classroom", "phase_changed", map[string]any{
		"lesson_id": 12, "from": "TTS_SEG", "to": "REPEATING", "word": "apple",
	}, Int("duration_ms", 3))
	entries := logs.All()
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, "classroom", e.ContextMap()["module"])
	assert.Equal(t, "phase_changed", e.ContextMap()["event"])
	assert.Equal(t, int64(12), e.ContextMap()["lesson_id"])
	assert.Equal(t, "TTS_SEG", e.ContextMap()["from"])
	assert.Equal(t, "REPEATING", e.ContextMap()["to"])
	assert.Equal(t, "apple", e.ContextMap()["word"])
	assert.Equal(t, int64(3), e.ContextMap()["duration_ms"])
	assert.NotEmpty(t, e.ContextMap()["trace_id"], "必须有 trace_id")
	assert.NotZero(t, e.Time)
}

func TestTracePropagation(t *testing.T) { // trace_id 贯穿 ctx
	l, logs := newTestLogger(t)
	ctx := WithTrace(context.Background(), "tr-abc123")
	got := TraceID(ctx)
	assert.Equal(t, "tr-abc123", got)

	l.InfoCtx(ctx, "scheduler", "plan_next_lesson", map[string]any{"student_id": 7})
	e := logs.All()[0]
	assert.Equal(t, "tr-abc123", e.ContextMap()["trace_id"])

	// 无 trace 的 ctx → 自动生成新 trace_id 并写回 ctx
	ctx2 := context.Background()
	l.InfoCtx(ctx2, "api", "request_in", nil)
	e2 := logs.All()[1]
	assert.NotEmpty(t, e2.ContextMap()["trace_id"])
	assert.NotEqual(t, "tr-abc123", e2.ContextMap()["trace_id"])
}

func TestNewTraceID(t *testing.T) {
	ids := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewTraceID()
		assert.Regexp(t, `^tr-[0-9a-f]{12,}$`, id)
		assert.False(t, ids[id], "trace id 必须唯一")
		ids[id] = true
	}
}

func TestChildTrace(t *testing.T) { // 子调用继承同一 trace
	l, logs := newTestLogger(t)
	ctx := WithTrace(context.Background(), "tr-parent")
	child := WithChild(ctx, "agent")
	assert.Equal(t, "tr-parent", TraceID(child), "子 ctx 继承 trace")

	l.InfoCtx(child, "agent", "tool_call", map[string]any{"tool": "classroom.say"})
	e := logs.All()[0]
	assert.Equal(t, "tr-parent", e.ContextMap()["trace_id"])
}

func TestLevelsAndPayloadRedaction(t *testing.T) { // 错误日志含堆栈摘要; payload 脱敏
	l, logs := newTestLogger(t)
	l.Error("speech", "asr_failed", map[string]any{
		"error": "timeout", "api_key": "sk-ws-SECRET",
	})
	e := logs.All()[0]
	assert.Equal(t, "error", e.Level.String())
	assert.Equal(t, "timeout", e.ContextMap()["error"])
	assert.Equal(t, "[REDACTED]", e.ContextMap()["api_key"], "密钥类字段必须脱敏(铁律)")
}

func TestJSONOutputShape(t *testing.T) { // 生产 JSON 输出字段形状(SDD §14.1)
	buf := &jsonBuffer{}
	l := NewWithSink(buf)
	l.Info("classroom", "quiz_judged", map[string]any{"lesson_id": 3, "judgment": "correct"})
	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.lines[0], &m))
	for _, k := range []string{"ts", "level", "trace_id", "module", "event", "lesson_id", "judgment"} {
		assert.Contains(t, m, k, "缺字段 %s", k)
	}
	assert.Equal(t, "info", m["level"])
}

func TestSlowPayloadSummary(t *testing.T) { // payload 摘要: 超长截断
	l, logs := newTestLogger(t)
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	l.Info("classroom", "big_event", map[string]any{"blob": string(long)})
	e := logs.All()[0]
	blob, _ := e.ContextMap()["blob"].(string)
	assert.LessOrEqual(t, len(blob), 2100, "超长 payload 必须截断为摘要")
}

func TestModuleLoggerAndLevels(t *testing.T) { // With 子日志器 + Warn/Debug 全方法
	l, logs := newTestLogger(t)
	m := l.With("scheduler")
	m.Info("planned", map[string]any{"lesson_id": 9})
	m.Warn("overdue", map[string]any{"hours": 25})
	m.Error("plan_failed", map[string]any{"err": "x"})
	m.InfoCtx(context.Background(), "ctx_event", nil)
	m.ErrorCtx(context.Background(), "ctx_err", nil)
	m.Warn("w", nil)
	all := logs.All()
	assert.GreaterOrEqual(t, len(all), 5, "Info/Warn/Error/InfoCtx/ErrorCtx 应各一条")
	for _, e := range all {
		assert.Equal(t, "scheduler", e.ContextMap()["module"], "ModuleLogger 固定 module")
	}
}

func TestWithChildNoTrace(t *testing.T) { // WithChild 无 trace 时补新 trace
	l, logs := newTestLogger(t)
	child := WithChild(context.Background(), "agent")
	assert.NotEmpty(t, TraceID(child))
	l.InfoCtx(child, "agent", "tool_call", nil)
	assert.NotEmpty(t, logs.All()[0].ContextMap()["trace_id"])
}

func TestNopLogger(t *testing.T) { // 装配占位不 panic
	Nop().Info("any", "any", nil)
	Nop().With("m").Error("e", nil)
}

type jsonBuffer struct {
	lines [][]byte
}

func (b *jsonBuffer) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	b.lines = append(b.lines, cp)
	return len(p), nil
}

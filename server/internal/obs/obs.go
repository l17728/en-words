// Package obs 实现结构化可观测日志（SDD §14 / 质量铁律 3）：
// 每条日志携带 trace_id/module/event + payload 字段，密钥脱敏，超长截断。
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field 强类型字段（避免 map 装箱歧义时使用）。
type Field struct {
	Key string
	Val any
}

// Int 构造整型字段。
func Int(key string, val int) Field { return Field{Key: key, Val: val} }

// String 构造字符串字段。
func String(key, val string) Field { return Field{Key: key, Val: val} }

type ctxKey int

const traceKey ctxKey = iota

// NewTraceID 生成全局唯一 trace id（tr- + 16 hex）。
func NewTraceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// 退化为时间戳，保证非空且基本唯一
		return fmt.Sprintf("tr-%x", time.Now().UnixNano())
	}
	return "tr-" + hex.EncodeToString(b)
}

// WithTrace 将 trace_id 写入 ctx（幂等：已有则覆盖——显式指定优先）。
func WithTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey, id)
}

// WithChild 派生子 ctx：继承同一 trace_id（跨 goroutine/工具调用同链路）。
func WithChild(ctx context.Context, _ string) context.Context {
	if TraceID(ctx) != "" {
		return ctx
	}
	return WithTrace(ctx, NewTraceID())
}

// TraceID 读取 ctx 的 trace_id；无则返回空串。
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceKey).(string); ok {
		return v
	}
	return ""
}

// Logger 结构化日志器。并发安全（zap 保证）。
type Logger struct {
	z *zap.Logger
}

// New 生产构造：JSON 编码输出到 io.Writer（stdout / 文件）。
func New(w io.Writer) *Logger {
	enc := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg", // 不用 message，event 即语义
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	})
	core := zapcore.NewCore(enc, zapcore.AddSync(w), zapcore.InfoLevel)
	return &Logger{z: zap.New(core)}
}

// NewWithCore 测试构造（observer core）。
func NewWithCore(core zapcore.Core) *Logger {
	return &Logger{z: zap.New(core)}
}

// NewWithSink 测试构造：JSON → 内存/缓冲（验证输出形状）。
func NewWithSink(w io.Writer) *Logger {
	return New(w)
}

// Nop 空日志器（装配期占位）。
func Nop() *Logger { return &Logger{z: zap.NewNop()} }

// ---- 脱敏与截断 ----

var sensitiveKeys = map[string]bool{
	"api_key": true, "apikey": true, "token": true, "authorization": true,
	"password": true, "secret": true, "key": true,
}

const redacted = "[REDACTED]"
const maxStrLen = 2000

func sanitize(k string, v any) any {
	lk := strings.ToLower(k)
	if sensitiveKeys[lk] {
		return redacted
	}
	if s, ok := v.(string); ok && len(s) > maxStrLen {
		return s[:maxStrLen] + fmt.Sprintf("...[truncated %d]", len(s)-maxStrLen)
	}
	return v
}

func fields(module, event string, payload map[string]any, extra []Field, trace string) []zap.Field {
	fs := make([]zap.Field, 0, 5+len(payload)+len(extra))
	fs = append(fs, zap.String("module", module), zap.String("event", event))
	if trace != "" {
		fs = append(fs, zap.String("trace_id", trace))
	}
	for k, v := range payload {
		fs = append(fs, zap.Any(k, sanitize(k, v)))
	}
	for _, f := range extra {
		fs = append(fs, zap.Any(f.Key, sanitize(f.Key, f.Val)))
	}
	return fs
}

// ---- 日志方法 ----

// Info 无 ctx 版本：自动生成新 trace_id（独立事件）。
func (l *Logger) Info(module, event string, payload map[string]any, extra ...Field) {
	l.z.Info(event, fields(module, event, payload, extra, NewTraceID())...)
}

// Error 错误日志（生产配置下 zap 自带 stackdump，此处保证字段形状）。
func (l *Logger) Error(module, event string, payload map[string]any, extra ...Field) {
	l.z.Error(event, fields(module, event, payload, extra, NewTraceID())...)
}

// Warn 警告日志。
func (l *Logger) Warn(module, event string, payload map[string]any, extra ...Field) {
	l.z.Warn(event, fields(module, event, payload, extra, NewTraceID())...)
}

// Debug 调试日志。
func (l *Logger) Debug(module, event string, payload map[string]any, extra ...Field) {
	l.z.Debug(event, fields(module, event, payload, extra, NewTraceID())...)
}

// InfoCtx 携带 ctx：使用 ctx 内 trace_id；无则生成并写回 ctx 语义（日志侧兜底）。
func (l *Logger) InfoCtx(ctx context.Context, module, event string, payload map[string]any, extra ...Field) {
	tr := TraceID(ctx)
	if tr == "" {
		tr = NewTraceID()
	}
	l.z.Info(event, fields(module, event, payload, extra, tr)...)
}

// ErrorCtx 携带 ctx 的错误日志。
func (l *Logger) ErrorCtx(ctx context.Context, module, event string, payload map[string]any, extra ...Field) {
	tr := TraceID(ctx)
	if tr == "" {
		tr = NewTraceID()
	}
	l.z.Error(event, fields(module, event, payload, extra, tr)...)
}

func (l *Logger) WarnCtx(ctx context.Context, module, event string, payload map[string]any, extra ...Field) {
	tr := TraceID(ctx)
	if tr == "" {
		tr = NewTraceID()
	}
	l.z.Warn(event, fields(module, event, payload, extra, tr)...)
}

// With 返回带固定模块名的子日志器（糖）。
func (l *Logger) With(module string) *ModuleLogger {
	return &ModuleLogger{l: l, module: module}
}

// ModuleLogger 模块固定子日志器。
type ModuleLogger struct {
	l      *Logger
	module string
}

func (m *ModuleLogger) Info(event string, payload map[string]any, extra ...Field) {
	m.l.Info(m.module, event, payload, extra...)
}

func (m *ModuleLogger) InfoCtx(ctx context.Context, event string, payload map[string]any, extra ...Field) {
	m.l.InfoCtx(ctx, m.module, event, payload, extra...)
}

func (m *ModuleLogger) Error(event string, payload map[string]any, extra ...Field) {
	m.l.Error(m.module, event, payload, extra...)
}

func (m *ModuleLogger) ErrorCtx(ctx context.Context, event string, payload map[string]any, extra ...Field) {
	m.l.ErrorCtx(ctx, m.module, event, payload, extra...)
}

func (m *ModuleLogger) WarnCtx(ctx context.Context, event string, payload map[string]any, extra ...Field) {
	m.l.WarnCtx(ctx, m.module, event, payload, extra...)
}

func (m *ModuleLogger) Warn(event string, payload map[string]any, extra ...Field) {
	m.l.Warn(m.module, event, payload, extra...)
}

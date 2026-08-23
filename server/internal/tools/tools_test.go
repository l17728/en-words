package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 测试基建 ----

func newRegistry(t *testing.T) (*Registry, *MemAuditor) {
	t.Helper()
	a := &MemAuditor{}
	r := New(a)
	// 注册样例工具：low/mid/high 三档
	require.NoError(t, r.Register(Tool{
		Name: "classroom.say", Domain: "classroom", Risk: RiskLow,
		Scope: []Role{RoleTeacherAgent},
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Handler: func(ctx context.Context, caller Caller, args json.RawMessage) (any, error) {
			var in struct{ Text string }
			_ = json.Unmarshal(args, &in)
			return map[string]any{"said": in.Text}, nil
		},
	}))
	require.NoError(t, r.Register(Tool{
		Name: "scheduler.postpone_lesson", Domain: "scheduler", Risk: RiskMid,
		Scope: []Role{RoleSystemAgent},
		InputSchema: json.RawMessage(`{"type":"object","properties":{"lesson_id":{"type":"integer"}},"required":["lesson_id"]}`),
		Handler: func(ctx context.Context, caller Caller, args json.RawMessage) (any, error) {
			return map[string]any{"postponed": true}, nil
		},
	}))
	require.NoError(t, r.Register(Tool{
		Name: "config.set", Domain: "config", Risk: RiskHigh,
		Scope: []Role{RoleSystemAgent, RoleAdmin},
		InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"},"value":{},"reason":{"type":"string"}},"required":["key","value","reason"]}`),
		Handler: func(ctx context.Context, caller Caller, args json.RawMessage) (any, error) {
			return map[string]any{"set": true}, nil
		},
	}))
	return r, a
}

var teacher = Caller{Role: RoleTeacherAgent, ID: "teacher-agent"}
var systemA = Caller{Role: RoleSystemAgent, ID: "system-agent"}
var admin = Caller{Role: RoleAdmin, ID: "admin-1"}

// ---- TR-01: Schema 校验失败 ----

func TestSchemaValidation(t *testing.T) {
	r, a := newRegistry(t)
	// 缺必填 text
	_, err := r.Call(context.Background(), teacher, "classroom.say", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text")
	// 类型错
	_, err = r.Call(context.Background(), teacher, "classroom.say", json.RawMessage(`{"text":123}`))
	require.Error(t, err)
	// 失败也审计
	require.Len(t, a.Entries, 2)
	assert.Equal(t, "classroom.say", a.Entries[0].Tool)
	assert.False(t, a.Entries[0].OK)
}

// ---- TR-02: 越权拒绝 ----

func TestScopeEnforcement(t *testing.T) {
	r, a := newRegistry(t)
	// teacher 调 system 工具 → 拒绝
	_, err := r.Call(context.Background(), teacher, "config.set",
		json.RawMessage(`{"key":"wordsPerLesson","value":80,"reason":"x"}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrScopeDenied)
	// 未知工具
	_, err = r.Call(context.Background(), teacher, "nope.tool", json.RawMessage(`{}`))
	require.Error(t, err)
	// 越权审计含 risk 标记
	require.Len(t, a.Entries, 2)
	assert.Equal(t, "denied", a.Entries[0].Result)
	assert.Equal(t, "unknown_tool", a.Entries[1].Result)
}

// ---- TR-03: 高风险 reason 强制（在 Schema required 之上双保险）----

func TestHighRiskReasonRequired(t *testing.T) {
	r, _ := newRegistry(t)
	// Schema 已含 required reason → Schema 层拦截
	_, err := r.Call(context.Background(), systemA, "config.set",
		json.RawMessage(`{"key":"wordsPerLesson","value":80}`))
	require.Error(t, err)
	// 合法调用成功
	res, err := r.Call(context.Background(), systemA, "config.set",
		json.RawMessage(`{"key":"wordsPerLesson","value":80,"reason":"反馈调整"}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"set": true}, res)
}

// ---- TR-04: Guard 调用上限 ----

func TestGuardCallLimit(t *testing.T) {
	r, a := newRegistry(t)
	ctx := WithEventBudget(context.Background(), 3) // 单事件上限 3
	for i := 0; i < 3; i++ {
		_, err := r.Call(ctx, teacher, "classroom.say", json.RawMessage(`{"text":"hi"}`))
		require.NoError(t, err)
	}
	_, err := r.Call(ctx, teacher, "classroom.say", json.RawMessage(`{"text":"hi"}`))
	require.ErrorIs(t, err, ErrBudgetExceeded, "第4次超上限被 Guard 中断")
	// 无预算 ctx 不限制
	_, err = r.Call(context.Background(), teacher, "classroom.say", json.RawMessage(`{"text":"hi"}`))
	require.NoError(t, err)
	_ = a
}

// ---- TR-05: 成功链路审计完整 ----

func TestAuditTrailComplete(t *testing.T) {
	r, a := newRegistry(t)
	_, err := r.Call(context.Background(), admin, "config.set",
		json.RawMessage(`{"key":"wordsPerLesson","value":90,"reason":"review"}`))
	require.NoError(t, err)
	require.Len(t, a.Entries, 1)
	e := a.Entries[0]
	assert.Equal(t, "admin-1", e.ActorID)
	assert.Equal(t, RoleAdmin, e.ActorRole)
	assert.Equal(t, "config.set", e.Tool)
	assert.True(t, e.OK)
	assert.Equal(t, RiskHigh, e.Risk)
	assert.NotZero(t, e.DurationMs)
	assert.NotEmpty(t, e.Args)
	_ = r
}

// ---- 注册校验 ----

func TestRegisterValidation(t *testing.T) {
	r, _ := newRegistry(t)
	assert.Error(t, r.Register(Tool{Name: "", Domain: "x", Risk: RiskLow, Scope: []Role{RoleAdmin}}), "空名拒绝")
	assert.Error(t, r.Register(Tool{Name: "a.b", Domain: "x", Risk: "weird", Scope: []Role{RoleAdmin}}), "非法风险级拒绝")
	assert.Error(t, r.Register(Tool{Name: "classroom.say", Domain: "classroom", Risk: RiskLow, Scope: []Role{RoleAdmin}}), "重复名拒绝")
	assert.Error(t, r.Register(Tool{Name: "c.d", Domain: "x", Risk: RiskLow, Scope: nil}), "空作用域拒绝")
}

// ---- ToolsFor：Agent 可见清单（LLM function 定义）----

func TestToolsFor(t *testing.T) {
	r, _ := newRegistry(t)
	sysTools := r.ToolsFor(RoleSystemAgent)
	names := map[string]bool{}
	for _, td := range sysTools {
		names[td.Name] = true
	}
	assert.True(t, names["config.set"])
	assert.True(t, names["scheduler.postpone_lesson"])
	assert.False(t, names["classroom.say"], "teacher 工具不可见")

	admTools := r.ToolsFor(RoleAdmin)
	found := false
	for _, td := range admTools {
		if td.Name == "config.set" {
			found = true
		}
	}
	assert.True(t, found, "admin 可见 config.set")
}

// ---- Handler 错误传播 ----

func TestHandlerError(t *testing.T) {
	a := &MemAuditor{}
	r := New(a)
	require.NoError(t, r.Register(Tool{
		Name: "x.fail", Domain: "x", Risk: RiskLow, Scope: []Role{RoleAdmin},
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, caller Caller, args json.RawMessage) (any, error) {
			return nil, errors.New("boom")
		},
	}))
	_, err := r.Call(context.Background(), admin, "x.fail", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	require.Len(t, a.Entries, 1)
	assert.False(t, a.Entries[0].OK)
	assert.Equal(t, "boom", a.Entries[0].Result)
}

// ---- reason 空白串拦截（双保险第二层）----

func TestHighRiskBlankReason(t *testing.T) {
	a := &MemAuditor{}
	r := New(a)
	require.NoError(t, r.Register(Tool{
		Name: "cfg.set2", Domain: "config", Risk: RiskHigh,
		Scope: []Role{RoleAdmin},
		// Schema 不强制 reason（模拟注册疏漏）→ 管线层兜底
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string"}},"required":[]}`),
		Handler: func(ctx context.Context, caller Caller, args json.RawMessage) (any, error) {
			return "ok", nil
		},
	}))
	_, err := r.Call(context.Background(), admin, "cfg.set2", json.RawMessage(`{"reason":"  "}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reason", "高风险工具空白 reason 被管线兜底拦截")
	fmt.Print()
}

// ---- Schema 校验器类型分支 ----

func TestSchemaTypeBranches(t *testing.T) {
	r, _ := newRegistry(t)
	// 整数类型错
	_, err := r.Call(context.Background(), systemA, "scheduler.postpone_lesson",
		json.RawMessage(`{"lesson_id":"str"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lesson_id")
	// 非对象 args
	_, err = r.Call(context.Background(), teacher, "classroom.say", json.RawMessage(`[1,2]`))
	require.Error(t, err)
	// 非法 JSON
	_, err = r.Call(context.Background(), teacher, "classroom.say", json.RawMessage(`{bad`))
	require.Error(t, err)
	// 坏 Schema 注册 → Call 时报 schema 错
	a2 := &MemAuditor{}
	r2 := New(a2)
	require.NoError(t, r2.Register(Tool{
		Name: "x.badschema", Domain: "x", Risk: RiskLow, Scope: []Role{RoleAdmin},
		InputSchema: json.RawMessage(`{not-json`),
		Handler:     func(ctx context.Context, c Caller, a json.RawMessage) (any, error) { return nil, nil },
	}))
	_, err = r2.Call(context.Background(), admin, "x.badschema", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema")
	// 根类型非 object 的 schema（非法注册形态，校验器拒绝）
	require.NoError(t, r2.Register(Tool{
		Name: "x.arrschema", Domain: "x", Risk: RiskLow, Scope: []Role{RoleAdmin},
		InputSchema: json.RawMessage(`{"type":"array"}`),
		Handler:     func(ctx context.Context, c Caller, a json.RawMessage) (any, error) { return nil, nil },
	}))
	_, err = r2.Call(context.Background(), admin, "x.arrschema", json.RawMessage(`{}`))
	require.Error(t, err, "非 object 根 schema 拒绝")
}

// ---- 复杂类型字段（number/boolean/array/object/未知类型放行）----

func TestSchemaComplexTypes(t *testing.T) {
	a := &MemAuditor{}
	r := New(a)
	require.NoError(t, r.Register(Tool{
		Name: "x.complex", Domain: "x", Risk: RiskLow, Scope: []Role{RoleAdmin},
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"n":{"type":"number"},"b":{"type":"boolean"},
			"arr":{"type":"array"},"obj":{"type":"object"},
			"weird":{"type":"future-type"},
			"any":{}},"required":[]}`),
		Handler: func(ctx context.Context, c Caller, a json.RawMessage) (any, error) { return "ok", nil },
	}))
	// 全部类型正确
	_, err := r.Call(context.Background(), admin, "x.complex", json.RawMessage(
		`{"n":3.5,"b":false,"arr":[1,2],"obj":{"k":1},"weird":123,"any":"whatever"}`))
	require.NoError(t, err)
	// number 类型错
	_, err = r.Call(context.Background(), admin, "x.complex", json.RawMessage(`{"n":"3.5"}`))
	require.Error(t, err)
	// boolean 类型错
	_, err = r.Call(context.Background(), admin, "x.complex", json.RawMessage(`{"b":"yes"}`))
	require.Error(t, err)
	// array 类型错
	_, err = r.Call(context.Background(), admin, "x.complex", json.RawMessage(`{"arr":{}}`))
	require.Error(t, err)
	// object 类型错
	_, err = r.Call(context.Background(), admin, "x.complex", json.RawMessage(`{"obj":[]}`))
	require.Error(t, err)
}

// ---- 空 Schema = 任意值放行 ----

func TestNoSchemaAllowsAnything(t *testing.T) {
	a := &MemAuditor{}
	r := New(a)
	require.NoError(t, r.Register(Tool{
		Name: "x.free", Domain: "x", Risk: RiskLow, Scope: []Role{RoleAdmin},
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			return string(args), nil
		},
	}))
	res, err := r.Call(context.Background(), admin, "x.free", json.RawMessage(`{"anything":[1,true,"x"]}`))
	require.NoError(t, err)
	assert.NotEmpty(t, res)
}

// ---- 预算耗尽不执行不审计成功 ----

func TestBudgetGuardOrder(t *testing.T) {
	r, a := newRegistry(t)
	ctx := WithEventBudget(context.Background(), 1)
	_, err := r.Call(ctx, teacher, "classroom.say", json.RawMessage(`{"text":"a"}`))
	require.NoError(t, err)
	// 预算耗尽后即使未知工具也先被预算拦截
	_, err = r.Call(ctx, teacher, "who.knows", json.RawMessage(`{}`))
	assert.ErrorIs(t, err, ErrBudgetExceeded)
	require.Len(t, a.Entries, 1, "预算拦截不产生审计条目")
}

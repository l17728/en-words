// Package tools 实现开放能力层 Tool Registry（SDD §3 / FR-I1~I7 / 03 §2.6 TR-01..05）：
// 统一 JSON Schema 校验 → 作用域检查 → 高风险 reason 双保险 → 事件预算 Guard →
// Handler 执行 → 审计落库。Agent（teacher/system）与 admin 共用同一管线。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- 角色 / 风险级 ----

type Role string

const (
	RoleStudent      Role = "student"
	RoleTeacherAgent Role = "teacher_agent"
	RoleSystemAgent  Role = "system_agent"
	RoleAdmin        Role = "admin"
)

type Risk string

const (
	RiskLow  Risk = "low"
	RiskMid  Risk = "mid"
	RiskHigh Risk = "high"
)

var validRisks = map[Risk]bool{RiskLow: true, RiskMid: true, RiskHigh: true}

// ---- 哨兵错误 ----

var (
	ErrScopeDenied    = fmt.Errorf("tools: scope denied")
	ErrUnknownTool    = fmt.Errorf("tools: unknown tool")
	ErrBudgetExceeded = fmt.Errorf("tools: event budget exceeded")
)

// ---- 调用者 / 工具 ----

type Caller struct {
	Role Role
	ID   string
}

type ToolHandler func(ctx context.Context, caller Caller, args json.RawMessage) (any, error)

type Tool struct {
	Name        string
	Domain      string
	Description string
	Scope       []Role
	Risk        Risk
	InputSchema json.RawMessage
	Handler     ToolHandler
}

// ToolDef 对外可见定义（ToolsFor / LLM function 列表）。
type ToolDef struct {
	Name        string
	Domain      string
	Description string
	Risk        Risk
	InputSchema json.RawMessage
}

// ---- 审计 ----

type AuditEntry struct {
	TS         time.Time
	ActorID    string
	ActorRole  Role
	Tool       string
	Risk       Risk
	OK         bool
	Result     string // ok | denied | unknown_tool | <error>
	Args       string
	DurationMs int64
}

type Auditor interface {
	ToolCalled(e AuditEntry)
}

// MemAuditor 内存审计（测试与早期装配；生产替换为 DB 实现）。
type MemAuditor struct {
	mu      sync.Mutex
	Entries []AuditEntry
}

func (a *MemAuditor) ToolCalled(e AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Entries = append(a.Entries, e)
}

// ---- 事件预算 Guard（FR-I7：单事件工具调用上限）----

type budgetKey struct{}

// WithEventBudget 注入单事件调用预算；超出后 Call 返回 ErrBudgetExceeded。
func WithEventBudget(ctx context.Context, n int) context.Context {
	v := n
	return context.WithValue(ctx, budgetKey{}, &v)
}

// ---- Registry ----

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	audit Auditor
}

func New(a Auditor) *Registry {
	return &Registry{tools: map[string]Tool{}, audit: a}
}

// Register 注册工具（TR 系列前置校验：名称/风险/作用域/重复）。
func (r *Registry) Register(t Tool) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("tools: tool name required")
	}
	if !validRisks[t.Risk] {
		return fmt.Errorf("tools: invalid risk %q", t.Risk)
	}
	if len(t.Scope) == 0 {
		return fmt.Errorf("tools: scope required")
	}
	if t.Handler == nil {
		return fmt.Errorf("tools: handler required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("tools: duplicate tool %q", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// ToolsFor 返回该角色可见的工具定义（给 LLM 的 function 清单）。
func (r *Registry) ToolsFor(role Role) []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolDef
	for _, t := range r.tools {
		for _, sc := range t.Scope {
			if sc == role {
				out = append(out, ToolDef{Name: t.Name, Domain: t.Domain,
					Description: t.Description, Risk: t.Risk, InputSchema: t.InputSchema})
				break
			}
		}
	}
	return out
}

// Call 统一管线：预算 → 查找 → 作用域 → Schema → 高风险 reason → 执行 → 审计（SDD §3.3）。
func (r *Registry) Call(ctx context.Context, caller Caller, name string, args json.RawMessage) (any, error) {
	// ① 事件预算 Guard（FR-I7）
	if b, ok := ctx.Value(budgetKey{}).(*int); ok {
		if *b <= 0 {
			return nil, ErrBudgetExceeded
		}
		*b--
	}
	start := time.Now()
	entry := AuditEntry{TS: start, ActorID: caller.ID, ActorRole: caller.Role, Tool: name, Args: string(args)}
	done := func(ok bool, result string) (any, error) {
		entry.OK = ok
		entry.Result = result
		entry.DurationMs = elapsedMs(start)
		if r.audit != nil {
			r.audit.ToolCalled(entry)
		}
		return nil, nil
	}

	// ② 工具查找
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		done(false, "unknown_tool")
		return nil, ErrUnknownTool
	}
	entry.Risk = t.Risk

	// ③ 作用域
	if !scopeAllows(t.Scope, caller.Role) {
		done(false, "denied")
		return nil, fmt.Errorf("%w: %s cannot call %s", ErrScopeDenied, caller.Role, name)
	}

	// ④ JSON Schema 校验（子集：type/properties/required）
	if err := validateSchema(t.InputSchema, args); err != nil {
		done(false, err.Error())
		return nil, fmt.Errorf("tools: %s: %w", name, err)
	}

	// ⑤ 高风险 reason 双保险（Schema 之外的管线级兜底，TR-03；R4-sec#2 类型严格校验）
	if t.Risk == RiskHigh {
		var payload struct {
			Reason json.RawMessage `json:"reason"`
		}
		_ = json.Unmarshal(args, &payload)
		// 必须是 JSON 字符串且非空白（防 {"reason":123}/{"reason":null} 绕过）
		if len(payload.Reason) == 0 || payload.Reason[0] != '"' {
			err := fmt.Errorf("high-risk tool %q requires string reason", name)
			done(false, err.Error())
			return nil, err
		}
		var reason string
		_ = json.Unmarshal(payload.Reason, &reason)
		if strings.TrimSpace(reason) == "" {
			err := fmt.Errorf("high-risk tool %q requires non-blank reason", name)
			done(false, err.Error())
			return nil, err
		}
	}

	// ⑥ 执行
	res, err := t.Handler(ctx, caller, args)
	if err != nil {
		done(false, err.Error())
		return nil, err
	}
	// ⑦ 成功审计
	entry.OK = true
	entry.Result = "ok"
	entry.DurationMs = elapsedMs(start)
	if r.audit != nil {
		r.audit.ToolCalled(entry)
	}
	return res, nil
}

func scopeAllows(scope []Role, role Role) bool {
	for _, sc := range scope {
		if sc == role {
			return true
		}
	}
	return false
}

func elapsedMs(start time.Time) int64 {
	ms := time.Since(start).Milliseconds()
	if ms < 1 {
		ms = 1 // 观测下限：避免亚毫秒记 0（NF-6 日志定位需要非零时长）
	}
	return ms
}

// ---- 最小 JSON Schema 校验器（子集：object/string/integer/number/boolean/array）----
// 设计取舍（SDD §3.3 校验库选型）：内置零依赖子集覆盖全部注册工具；
// 空 Schema / 无 type 属性 = 任意值；未知关键字忽略。

type schemaNode struct {
	Type       string                 `json:"type"`
	Properties map[string]schemaNode  `json:"properties"`
	Required   []string               `json:"required"`
}

func validateSchema(schema, args json.RawMessage) error {
	if len(schema) == 0 || string(schema) == "{}" {
		return nil // 无约束
	}
	var root schemaNode
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("bad tool schema: %v", err)
	}
	if root.Type == "" {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(args, &payload); err != nil {
		return fmt.Errorf("args must be JSON object: %v", err)
	}
	return validateNode(root, payload)
}

func validateNode(n schemaNode, obj map[string]json.RawMessage) error {
	if n.Type != "object" {
		return fmt.Errorf("root schema must be object, got %q", n.Type)
	}
	for _, req := range n.Required {
		if _, ok := obj[req]; !ok {
			return fmt.Errorf("missing required field %q", req)
		}
	}
	for name, val := range obj {
		sub, has := n.Properties[name]
		if !has || sub.Type == "" {
			continue // 未定义/无类型 = 任意
		}
		if err := validateType(sub.Type, val); err != nil {
			return fmt.Errorf("field %q: %v", name, err)
		}
	}
	return nil
}

func validateType(typ string, raw json.RawMessage) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	switch typ {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("expected string")
		}
	case "integer":
		num, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("expected integer")
		}
		if !isInt(num) {
			return fmt.Errorf("expected integer")
		}
	case "number":
		if _, ok := v.(json.Number); !ok {
			return fmt.Errorf("expected number")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("expected object")
		}
	case "array":
		if _, ok := v.([]any); !ok {
			return fmt.Errorf("expected array")
		}
	default:
		// 未知类型关键字：放行（前向兼容）
	}
	return nil
}

func isInt(n json.Number) bool {
	s := n.String()
	// R4-sec#5：拒绝科学计数法/小数（json.Number 保留原文）
	if strings.ContainsAny(s, ".eE") {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64) // 范围校验（防溢出）
	return err == nil
}

// ---- 装配层简写别名 ----

// Ctx 即 context.Context（Handler 签名简写）。
type Ctx = context.Context

// Args 即 json.RawMessage（Handler 入参简写）。
type Args = json.RawMessage

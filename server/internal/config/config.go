// Package config 实现配置中心（SDD §8）：全部学习参数可配置、带边界校验、
// 两层合并（全局默认 → 学生覆盖）、审计钩子、ladder 变更回调（R2-AR-02）。
package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// Scope 配置层级。
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeStudent Scope = "student"
)

// AuditEntry 配置变更审计记录（SDD §8.3：审计归属 ConfigService.Set 内部）。
type AuditEntry struct {
	Actor     string // admin / system_agent / ...
	Scope     Scope
	StudentID int64 // ScopeStudent 时有效
	Key       string
	Old       any
	New       any // DeleteOverride 时为 nil
	Reason    string
}

// Persister 持久化接口（由 store 层提供 GORM 实现；单测用内存替身）。
type Persister interface {
	LoadGlobals() (map[string]json.RawMessage, error)
	LoadOverrides() (map[int64]map[string]json.RawMessage, error)
	SaveGlobal(key string, val any) error
	SaveOverride(studentID int64, key string, val any) error
	DeleteOverride(studentID int64, key string) error
}

// Auditor 审计出口。
type Auditor interface {
	ConfigChanged(entry AuditEntry)
}

// EffectiveSnapshot 课堂/调度消费的不可变生效配置（课堂冻结语义：课中持快照不重读）。
type EffectiveSnapshot struct {
	WordsPerLesson    int
	GroupSize         int
	TtsEnRepeat       int
	TtsIntervalMs     int
	RepeatTimes       int
	QuizOldWords      int
	TimeoutShowWordS  int
	TimeoutRepeatS    int
	TimeoutAnswerS    int
	SessionTimeoutMin int
	LessonsPerDay     int
	OverdueHours      int
	GraduateThreshold int
	EncourageEveryN   int
	GoalMode          string
	Ladder            []int
}

// 阶梯默认（SDD §6.2）
var (
	DefaultLadder = []int{1, 1, 2, 2, 4, 7, 14, 30}
	SprintLadder  = []int{1, 1, 1, 1, 2, 2, 3, 3}
)

const (
	ladderElemMin = 1
	ladderElemMax = 60
	ladderLenMin  = 3
	ladderLenMax  = 10
)

type keySpec struct {
	typ     string   // "int" | "string" | "ladder"
	min     int      // int 边界
	max     int      // int 边界
	def     any      // 默认值（int/string 用；ladder 用 DefaultLadder）
	enum    []string // string 枚举
	hasEnum bool
}

var specs = map[string]keySpec{
	"wordsPerLesson":    {typ: "int", min: 50, max: 100, def: 60},
	"groupSize":         {typ: "int", min: 5, max: 5, def: 5},
	"ttsEnRepeat":       {typ: "int", min: 1, max: 5, def: 3},
	"ttsIntervalMs":     {typ: "int", min: 500, max: 4000, def: 1500},
	"repeatTimes":       {typ: "int", min: 1, max: 5, def: 1},
	"quizOldWords":      {typ: "int", min: 0, max: 10, def: 3},
	"timeoutShowWordS":  {typ: "int", min: 3, max: 30, def: 8},
	"timeoutRepeatS":    {typ: "int", min: 5, max: 60, def: 10},
	"timeoutAnswerS":    {typ: "int", min: 10, max: 120, def: 20},
	"sessionTimeoutMin": {typ: "int", min: 30, max: 120, def: 60},
	"lessonsPerDay":     {typ: "int", min: 1, max: 2, def: 1},
	"overdueHours":      {typ: "int", min: 6, max: 72, def: 24},
	"graduateThreshold": {typ: "int", min: 2, max: 4, def: 2},
	"encourageEveryN":   {typ: "int", min: 3, max: 20, def: 8},
	"goalMode":          {typ: "string", def: "longterm", enum: []string{"longterm", "exam_sprint"}, hasEnum: true},
	"ladder":            {typ: "ladder"},
}

// Store 配置中心。读走内存原子快照（RWMutex），写在校验/回调/持久化全部成功后才换内存。
type Store struct {
	mu        sync.RWMutex
	persist   Persister
	audit     Auditor
	globals   map[string]any            // 显式设置过的全局值
	overrides map[int64]map[string]any  // 学生覆盖
	// OnLadderChanged 在 ladder 变更持久化前调用（如缩短截断 memory_cards.ladder_step），
	// 返回错误则整个 Set 中止（R2-AR-02 事务语义）。
	OnLadderChanged func(newLadder []int) error
}

func NewStore(p Persister, a Auditor) (*Store, error) {
	s := &Store{persist: p, audit: a,
		globals:   map[string]any{},
		overrides: map[int64]map[string]any{},
	}
	if p == nil {
		return s, nil
	}
	g, err := p.LoadGlobals()
	if err != nil {
		return nil, fmt.Errorf("load globals: %w", err)
	}
	for k, raw := range g {
		v, ok := decodeValidate(k, raw)
		if ok {
			s.globals[k] = v
		} // 越界/损坏数据 → 忽略，回落默认（TestLoadRejectsCorruptPersisted）
	}
	o, err := p.LoadOverrides()
	if err != nil {
		return nil, fmt.Errorf("load overrides: %w", err)
	}
	for sid, m := range o {
		for k, raw := range m {
			if v, ok := decodeValidate(k, raw); ok {
				if s.overrides[sid] == nil {
					s.overrides[sid] = map[string]any{}
				}
				s.overrides[sid][k] = v
			}
		}
	}
	return s, nil
}

// decodeValidate 将持久化 JSON 解码并校验；非法返回 false。
func decodeValidate(key string, raw json.RawMessage) (any, bool) {
	if _, ok := specs[key]; !ok {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	cv, err := canon(key, v)
	if err != nil {
		return nil, false
	}
	return cv, true
}

// canon 将任意来源（int/float64/json.Number/[]any）规范为键的规范类型并校验边界。
func canon(key string, v any) (any, error) {
	spec, ok := specs[key]
	if !ok {
		return nil, fmt.Errorf("未知配置键 %q", key)
	}
	switch spec.typ {
	case "int":
		n, err := toInt(v)
		if err != nil {
			return nil, fmt.Errorf("%s 需要整数: %w", key, err)
		}
		if n < spec.min || n > spec.max {
			return nil, fmt.Errorf("%s=%d 越界，允许 [%d, %d]", key, n, spec.min, spec.max)
		}
		return n, nil
	case "string":
		sv, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s 需要字符串", key)
		}
		if spec.hasEnum && !contains(spec.enum, sv) {
			return nil, fmt.Errorf("%s=%q 不合法，允许 %v", key, sv, spec.enum)
		}
		return sv, nil
	case "ladder":
		lv, err := toIntSlice(v)
		if err != nil {
			return nil, fmt.Errorf("%s 需要整数数组: %w", key, err)
		}
		if len(lv) < ladderLenMin || len(lv) > ladderLenMax {
			return nil, fmt.Errorf("%s 长度 %d 越界，允许 [%d, %d]", key, len(lv), ladderLenMin, ladderLenMax)
		}
		for i, n := range lv {
			if n < ladderElemMin || n > ladderElemMax {
				return nil, fmt.Errorf("%s[%d]=%d 越界，允许 [%d, %d]", key, i, n, ladderElemMin, ladderElemMax)
			}
			if i > 0 && lv[i] < lv[i-1] {
				return nil, fmt.Errorf("%s 必须非降序（等间隔阶梯语义）: %v", key, lv)
			}
		}
		out := make([]int, len(lv))
		copy(out, lv)
		return out, nil
	}
	return nil, fmt.Errorf("键 %q 类型未定义", key)
}

func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		if x != float64(int(x)) {
			return 0, fmt.Errorf("非整数 %v", x)
		}
		return int(x), nil
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, fmt.Errorf("非整数 %s: %w", x, err)
		}
		return int(i), nil
	case string:
		return 0, fmt.Errorf("字符串 %q 不是整数", x)
	default:
		return 0, fmt.Errorf("类型 %T 不支持", v)
	}
}

func toIntSlice(v any) ([]int, error) {
	switch x := v.(type) {
	case []int:
		return x, nil
	case []any:
		out := make([]int, 0, len(x))
		for _, e := range x {
			n, err := toInt(e)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	case []float64:
		out := make([]int, 0, len(x))
		for _, e := range x {
			n, err := toInt(e)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("类型 %T 不支持", v)
	}
}

func contains(ss []string, t string) bool {
	for _, s := range ss {
		if s == t {
			return true
		}
	}
	return false
}

// effective 返回生效值（需持读锁）。
func (s *Store) effective(studentID int64, key string) any {
	if m := s.overrides[studentID]; m != nil {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if v, ok := s.globals[key]; ok {
		return v
	}
	spec := specs[key]
	if spec.typ == "ladder" {
		if s.effectiveGoalMode(studentID) == "exam_sprint" {
			return append([]int(nil), SprintLadder...)
		}
		return append([]int(nil), DefaultLadder...)
	}
	return spec.def
}

func (s *Store) effectiveGoalMode(studentID int64) string {
	if v, ok := s.effective(studentID, "goalMode").(string); ok {
		return v
	}
	return "longterm"
}

// ---- 读接口 ----

func (s *Store) Int(studentID int64, key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, _ := s.effective(studentID, key).(int)
	return n
}

func (s *Store) String(studentID int64, key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	str, _ := s.effective(studentID, key).(string)
	return str
}

// Ladder 返回该生生效阶梯（返回副本，调用方改不动内部状态）。
func (s *Store) Ladder(studentID int64) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if lv, ok := s.effective(studentID, "ladder").([]int); ok {
		return append([]int(nil), lv...)
	}
	return append([]int(nil), DefaultLadder...)
}

// Snapshot 返回该生的生效配置快照（课堂课中冻结用，SDD §8.3）。
func (s *Store) Snapshot(studentID int64) EffectiveSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return EffectiveSnapshot{
		WordsPerLesson:    s.effInt(studentID, "wordsPerLesson"),
		GroupSize:         s.effInt(studentID, "groupSize"),
		TtsEnRepeat:       s.effInt(studentID, "ttsEnRepeat"),
		TtsIntervalMs:     s.effInt(studentID, "ttsIntervalMs"),
		RepeatTimes:       s.effInt(studentID, "repeatTimes"),
		QuizOldWords:      s.effInt(studentID, "quizOldWords"),
		TimeoutShowWordS:  s.effInt(studentID, "timeoutShowWordS"),
		TimeoutRepeatS:    s.effInt(studentID, "timeoutRepeatS"),
		TimeoutAnswerS:    s.effInt(studentID, "timeoutAnswerS"),
		SessionTimeoutMin: s.effInt(studentID, "sessionTimeoutMin"),
		LessonsPerDay:     s.effInt(studentID, "lessonsPerDay"),
		OverdueHours:      s.effInt(studentID, "overdueHours"),
		GraduateThreshold: s.effInt(studentID, "graduateThreshold"),
		EncourageEveryN:   s.effInt(studentID, "encourageEveryN"),
		GoalMode:          s.effectiveGoalMode(studentID),
		Ladder:            s.LadderLocked(studentID),
	}
}

// LadderLocked 供已持读锁的内部调用。
func (s *Store) LadderLocked(studentID int64) []int {
	if lv, ok := s.effective(studentID, "ladder").([]int); ok {
		return append([]int(nil), lv...)
	}
	return append([]int(nil), DefaultLadder...)
}

func (s *Store) effInt(studentID int64, key string) int {
	n, _ := s.effective(studentID, key).(int)
	return n
}

// ---- 写接口 ----

// SetGlobal 设置全局默认。管线：校验 → ladder 回调 → 持久化 → 换内存 → 审计（SDD §8.3）。
func (s *Store) SetGlobal(actor, key string, val any, reason string) error {
	return s.set(actor, ScopeGlobal, 0, key, val, reason)
}

// SetStudent 设置学生覆盖。
func (s *Store) SetStudent(actor string, studentID int64, key string, val any, reason string) error {
	return s.set(actor, ScopeStudent, studentID, key, val, reason)
}

func (s *Store) set(actor string, scope Scope, studentID int64, key string, val any, reason string) error {
	if reason == "" || isBlank(reason) {
		return fmt.Errorf("配置变更必须携带 reason（SDD §8.3/CF-03）")
	}
	cv, err := canon(key, val)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.effective(studentID, key)
	// ladder 回调（截断 memory_cards 等），失败中止（R2-AR-02）
	if key == "ladder" && s.OnLadderChanged != nil {
		if err := s.OnLadderChanged(cv.([]int)); err != nil {
			return fmt.Errorf("ladder 变更被拒绝: %w", err)
		}
	}
	if scope == ScopeGlobal {
		if s.persist != nil {
			if err := s.persist.SaveGlobal(key, cv); err != nil {
				return fmt.Errorf("持久化失败，变更未生效: %w", err)
			}
		}
		s.globals[key] = cv
	} else {
		if s.persist != nil {
			if err := s.persist.SaveOverride(studentID, key, cv); err != nil {
				return fmt.Errorf("持久化失败，变更未生效: %w", err)
			}
		}
		if s.overrides[studentID] == nil {
			s.overrides[studentID] = map[string]any{}
		}
		s.overrides[studentID][key] = cv
	}
	if s.audit != nil {
		s.audit.ConfigChanged(AuditEntry{Actor: actor, Scope: scope, StudentID: studentID,
			Key: key, Old: old, New: cv, Reason: reason})
	}
	return nil
}

// DeleteOverride 删除学生覆盖，回落全局（BE-08）。
func (s *Store) DeleteOverride(actor string, studentID int64, key string) error {
	if _, ok := specs[key]; !ok {
		return fmt.Errorf("未知配置键 %q", key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.overrides[studentID][key]; !exists {
		return nil // 幂等
	}
	old := s.effective(studentID, key)
	if s.persist != nil {
		if err := s.persist.DeleteOverride(studentID, key); err != nil {
			return fmt.Errorf("持久化失败: %w", err)
		}
	}
	delete(s.overrides[studentID], key)
	if s.audit != nil {
		s.audit.ConfigChanged(AuditEntry{Actor: actor, Scope: ScopeStudent, StudentID: studentID,
			Key: key, Old: old, New: nil, Reason: "删除覆盖，回落全局"})
	}
	return nil
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// SortedKeys 返回全部配置键（管理界面/校验用）。
func SortedKeys() []string {
	out := make([]string, 0, len(specs))
	for k := range specs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// overrideKey 供持久化实现的组合键约定（memPersister 使用）。
func overrideKey(studentID int64, key string) string {
	return strconv.FormatInt(studentID, 10) + "|" + key
}

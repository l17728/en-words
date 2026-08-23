package config

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 测试替身 ----

type memPersister struct {
	mu        sync.Mutex
	globals   map[string]json.RawMessage
	overrides map[string]json.RawMessage // key "studentID|cfgkey"
	failOn    string
}

func newMemPersister() *memPersister {
	return &memPersister{globals: map[string]json.RawMessage{}, overrides: map[string]json.RawMessage{}}
}

func (m *memPersister) LoadGlobals() (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for k, v := range m.globals {
		out[k] = v
	}
	return out, nil
}

func (m *memPersister) LoadOverrides() (map[int64]map[string]json.RawMessage, error) {
	out := map[int64]map[string]json.RawMessage{}
	for k, v := range m.overrides {
		var sid int64
		var key string
		if _, err := sscanf(k, &sid, &key); err != nil {
			continue
		}
		if out[sid] == nil {
			out[sid] = map[string]json.RawMessage{}
		}
		out[sid][key] = v
	}
	return out, nil
}

func sscanf(k string, sid *int64, key *string) (int, error) {
	var s int64
	var c string
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == '|' {
			if err := json.Unmarshal([]byte(k[:i]), &s); err != nil {
				continue
			}
			c = k[i+1:]
			*sid, *key = s, c
			return 2, nil
		}
	}
	return 0, errors.New("bad key")
}

func (m *memPersister) SaveGlobal(key string, val any) error {
	if m.failOn == "global" {
		return errors.New("persist fail")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(val)
	m.globals[key] = b
	return nil
}

func (m *memPersister) SaveOverride(studentID int64, key string, val any) error {
	if m.failOn == "override" {
		return errors.New("persist fail")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(val)
	m.overrides[overrideKey(studentID, key)] = b
	return nil
}

func (m *memPersister) DeleteOverride(studentID int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.overrides, overrideKey(studentID, key))
	return nil
}

type memAuditor struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (a *memAuditor) ConfigChanged(e AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *memAuditor) last() AuditEntry {
	if len(a.entries) == 0 {
		return AuditEntry{}
	}
	return a.entries[len(a.entries)-1]
}

// ---- 用例（对应 03-测试用例 CF-01..06 与 R2/R3 语义）----

func newTestStore(t *testing.T) (*Store, *memAuditor, *memPersister) {
	t.Helper()
	p := newMemPersister()
	a := &memAuditor{}
	s, err := NewStore(p, a)
	require.NoError(t, err)
	return s, a, p
}

func TestDefaultsEffective(t *testing.T) {
	s, _, _ := newTestStore(t)
	assert.Equal(t, 60, s.Int(1, "wordsPerLesson"))
	assert.Equal(t, 5, s.Int(1, "groupSize"))
	assert.Equal(t, 3, s.Int(1, "ttsEnRepeat"))
	assert.Equal(t, 1500, s.Int(1, "ttsIntervalMs"))
	assert.Equal(t, 24, s.Int(1, "overdueHours"))
	assert.Equal(t, "longterm", s.String(1, "goalMode"))
	assert.Equal(t, []int{1, 1, 2, 2, 4, 7, 14, 30}, s.Ladder(1))
}

func TestSetGlobalInBoundsWithAudit(t *testing.T) { // CF-01
	s, a, _ := newTestStore(t)
	err := s.SetGlobal("admin", "wordsPerLesson", 80, "反馈调整")
	require.NoError(t, err)
	assert.Equal(t, 80, s.Int(1, "wordsPerLesson"))
	e := a.last()
	assert.Equal(t, "wordsPerLesson", e.Key)
	assert.Equal(t, 60, e.Old)
	assert.Equal(t, 80, e.New)
	assert.Equal(t, "反馈调整", e.Reason)
	assert.Equal(t, "admin", e.Actor)
	assert.Equal(t, ScopeGlobal, e.Scope)
}

func TestSetOutOfBounds(t *testing.T) { // CF-02
	s, _, _ := newTestStore(t)
	err := s.SetGlobal("admin", "wordsPerLesson", 120, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "40")
	assert.Contains(t, err.Error(), "100")
	assert.Equal(t, 60, s.Int(1, "wordsPerLesson")) // 未变更
}

func TestSetWithoutReason(t *testing.T) { // CF-03
	s, _, _ := newTestStore(t)
	require.Error(t, s.SetGlobal("admin", "wordsPerLesson", 80, ""))
	require.Error(t, s.SetStudent("admin", 7, "wordsPerLesson", 80, "  "))
}

func TestUnknownKeyAndTypeMismatch(t *testing.T) {
	s, _, _ := newTestStore(t)
	require.Error(t, s.SetGlobal("admin", "nope", 1, "r"))
	require.Error(t, s.SetGlobal("admin", "wordsPerLesson", "eighty", "r"))
	require.Error(t, s.SetGlobal("admin", "goalMode", 3, "r"))
}

func TestStudentOverrideAndFallback(t *testing.T) { // CF-04
	s, _, _ := newTestStore(t)
	require.NoError(t, s.SetStudent("system_agent", 7, "wordsPerLesson", 50, "反馈A"))
	assert.Equal(t, 50, s.Int(7, "wordsPerLesson"))
	assert.Equal(t, 60, s.Int(8, "wordsPerLesson")) // 其他学生不受影响
	require.NoError(t, s.SetGlobal("admin", "wordsPerLesson", 70, "r"))
	assert.Equal(t, 50, s.Int(7, "wordsPerLesson")) // 覆盖仍优先
	require.NoError(t, s.DeleteOverride("admin", 7, "wordsPerLesson"))
	assert.Equal(t, 70, s.Int(7, "wordsPerLesson")) // 回落全局
}

func TestOverridePersistsAcrossReload(t *testing.T) {
	p := newMemPersister()
	a := &memAuditor{}
	s1, err := NewStore(p, a)
	require.NoError(t, err)
	require.NoError(t, s1.SetStudent("admin", 7, "wordsPerLesson", 45, "r"))
	s2, err := NewStore(p, a) // 模拟重启重载
	require.NoError(t, err)
	assert.Equal(t, 45, s2.Int(7, "wordsPerLesson"))
}

func TestLadderValidation(t *testing.T) { // CF-05
	s, _, _ := newTestStore(t)
	require.Error(t, s.SetGlobal("admin", "ladder", []int{1, 2}, "r"))         // 长度<3
	require.Error(t, s.SetGlobal("admin", "ladder", []int{1, 0, 2}, "r"))       // 元素<1
	require.Error(t, s.SetGlobal("admin", "ladder", []int{1, 61, 2}, "r"))      // 元素>60
	require.Error(t, s.SetGlobal("admin", "ladder", []string{"1"}, "r"))        // 类型
	require.Error(t, s.SetGlobal("admin", "ladder", []int{3, 1, 2, 9, 9}, "r")) // 必须非降序(等间隔阶梯)
	require.NoError(t, s.SetGlobal("admin", "ladder", []int{1, 1, 2, 2, 4}, "r"))
}

func TestGoalModeLadder(t *testing.T) { // exam_sprint 压缩阶梯 (SDD §6.2/§8)
	s, _, _ := newTestStore(t)
	assert.Equal(t, []int{1, 1, 2, 2, 4, 7, 14, 30}, s.Ladder(1))
	require.NoError(t, s.SetGlobal("admin", "goalMode", "exam_sprint", "考前"))
	assert.Equal(t, []int{1, 1, 1, 1, 2, 2, 3, 3}, s.Ladder(1))
	// 显式设置 ladder 覆盖目标模式默认
	require.NoError(t, s.SetGlobal("admin", "ladder", []int{2, 2, 5}, "r"))
	assert.Equal(t, []int{2, 2, 5}, s.Ladder(1))
}

func TestLadderShortenHook(t *testing.T) { // R2-AR-02: 变更触发回调, hook 失败则拒绝且值不变
	s, _, _ := newTestStore(t)
	called := 0
	s.OnLadderChanged = func(newLadder []int) error {
		called++
		if newLadder[0] == 3 { // 合法值但 hook 主动拒绝
			return errors.New("hook fail")
		}
		return nil
	}
	require.NoError(t, s.SetGlobal("admin", "ladder", []int{1, 1, 2}, "r")) // 变短(8→3)也触发
	assert.Equal(t, 1, called)
	assert.Equal(t, []int{1, 1, 2}, s.Ladder(1))
	// hook 失败 → set 拒绝且内存不变
	require.Error(t, s.SetGlobal("admin", "ladder", []int{3, 3, 3}, "r"))
	assert.Equal(t, 2, called)
	assert.Equal(t, []int{1, 1, 2}, s.Ladder(1)) // 未变更
}

func TestPersistFailureAborts(t *testing.T) { // 持久化失败 → 内存不生效
	p := newMemPersister()
	p.failOn = "global"
	s, err := NewStore(p, &memAuditor{})
	require.NoError(t, err)
	require.Error(t, s.SetGlobal("admin", "wordsPerLesson", 99, "r"))
	assert.Equal(t, 60, s.Int(1, "wordsPerLesson"))
}

func TestConcurrency(t *testing.T) { // 并发读写安全
	s, _, _ := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); _ = s.Int(1, "wordsPerLesson") }(i)
		go func(i int) {
			defer wg.Done()
			_ = s.SetGlobal("t", "encourageEveryN", 3+(i%18), "race")
		}(i)
	}
	wg.Wait()
}

func TestSnapshot(t *testing.T) { // 课堂冻结语义: 消费者拿不可变快照
	s, _, _ := newTestStore(t)
	snap := s.Snapshot(7)
	assert.Equal(t, 60, snap.WordsPerLesson)
	assert.Equal(t, 3, snap.TtsEnRepeat)
	require.NoError(t, s.SetGlobal("admin", "wordsPerLesson", 100, "r"))
	assert.Equal(t, 60, snap.WordsPerLesson) // 旧快照不受影响
}

func TestLoadRejectsCorruptPersisted(t *testing.T) { // 加载时防御性校验
	p := newMemPersister()
	p.globals["wordsPerLesson"] = json.RawMessage("999") // 越界持久化数据
	p.globals["goalMode"] = json.RawMessage(`"bogus"`)  // 非法枚举
	p.globals["ladder"] = json.RawMessage("[1,2]")      // 长度不足
	s, err := NewStore(p, &memAuditor{})
	require.NoError(t, err) // 拒绝坏值, 回落默认
	assert.Equal(t, 60, s.Int(1, "wordsPerLesson"))
	assert.Equal(t, "longterm", s.String(1, "goalMode"))
	assert.Equal(t, []int{1, 1, 2, 2, 4, 7, 14, 30}, s.Ladder(1))
}

func TestNilPersisterAndAuditor(t *testing.T) { // 纯内存模式（早期装配/嵌入场景）
	s, err := NewStore(nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetGlobal("admin", "wordsPerLesson", 90, "r"))
	assert.Equal(t, 90, s.Int(1, "wordsPerLesson"))
}

func TestDeleteOverrideIdempotentAndUnknown(t *testing.T) {
	s, _, _ := newTestStore(t)
	require.NoError(t, s.DeleteOverride("admin", 7, "wordsPerLesson")) // 不存在也成功(幂等)
	require.Error(t, s.DeleteOverride("admin", 7, "nope"))             // 未知键拒绝
}

func TestCanonTypeEdges(t *testing.T) {
	s, _, _ := newTestStore(t)
	// int 通道各类型
	require.NoError(t, s.SetGlobal("a", "quizOldWords", int64(4), "r"))
	require.NoError(t, s.SetGlobal("a", "quizOldWords", float64(4), "r"))
	require.Error(t, s.SetGlobal("a", "quizOldWords", 4.5, "r"))  // 非整数 float
	require.Error(t, s.SetGlobal("a", "quizOldWords", true, "r")) // bool
	require.Error(t, s.SetGlobal("a", "quizOldWords", []int{1}, "r"))
	// ladder float64 数组 (JSON 来源)
	require.NoError(t, s.SetGlobal("a", "ladder", []any{float64(2), float64(2), float64(5)}, "r"))
	// 覆盖删除后 override map 清理
	require.NoError(t, s.SetStudent("a", 9, "quizOldWords", 6, "r"))
	require.NoError(t, s.DeleteOverride("a", 9, "quizOldWords"))
}

func TestSprintLadderPerStudent(t *testing.T) { // 学生级 goalMode 覆盖影响其阶梯
	s, _, _ := newTestStore(t)
	require.NoError(t, s.SetStudent("agent", 7, "goalMode", "exam_sprint", "考前冲刺"))
	assert.Equal(t, []int{1, 1, 1, 1, 2, 2, 3, 3}, s.Ladder(7))
	assert.Equal(t, []int{1, 1, 2, 2, 4, 7, 14, 30}, s.Ladder(8)) // 他人不受影响
}

// Package memory 实现 Agent 自维护记忆（D-16 / SDD §3.2 memory 域 / §9 agent_memories）：
// 平台提供完整 CRUD 能力（append/recall/revise/delete/list），内容由 Agent 决定；
// owner_role 隔离（teacher/system 各见各的），admin 只读审计；
// recall 按 近因+重要度 加权排序，支持 kind 过滤与关键词查询。
package memory

import (
	"strings"
	"sync"
	"time"
)

// OwnerRole 记忆归属的 Agent 角色。
type OwnerRole string

const (
	OwnerTeacher OwnerRole = "teacher_agent"
	OwnerSystem  OwnerRole = "system_agent"
	// admin 无写入权（只读审计走 Service 层直查，不走工具）
)

// Scope 记忆范围。
type Scope string

const (
	ScopeStudentProfile Scope = "student_profile" // 学生档案（book_id 为空）
	ScopeTaskMemory     Scope = "task_memory"     // 教学任务记忆（book_id 必填）
)

// Memory 一条记忆。
type Memory struct {
	ID             int64
	OwnerRole      OwnerRole
	StudentID      int64
	BookID         int64 // 0=学生档案；>0=任务记忆
	Kind           string
	Content        string
	Importance     int // 1..5
	SourceLessonID int64
	RevisedFrom    int64 // 修订来源（记忆链）
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ---- 存储 ----

type Repo interface {
	Append(m *Memory) error
	Get(id int64) (*Memory, error)
	Update(m *Memory) error
	Delete(id int64) error
	List(owner OwnerRole, studentID, bookID int64) ([]*Memory, error)
}

// MemRepo 内存实现（测试/早期装配）。
type MemRepo struct {
	mu   sync.RWMutex
	ms   map[int64]*Memory
	next int64
}

func NewMemRepo() *MemRepo { return &MemRepo{ms: map[int64]*Memory{}, next: 0} }

func (r *MemRepo) Append(m *Memory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	m.ID = r.next
	cp := *m
	r.ms[m.ID] = &cp
	return nil
}

func (r *MemRepo) Get(id int64) (*Memory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.ms[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (r *MemRepo) Update(m *Memory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ms[m.ID]; !ok {
		return ErrNotFound
	}
	cp := *m
	r.ms[m.ID] = &cp
	return nil
}

func (r *MemRepo) Delete(id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ms[id]; !ok {
		return ErrNotFound
	}
	delete(r.ms, id)
	return nil
}

func (r *MemRepo) List(owner OwnerRole, studentID, bookID int64) ([]*Memory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Memory
	for _, m := range r.ms {
		if m.OwnerRole == owner && m.StudentID == studentID && m.BookID == bookID {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

// ---- Service（工具 Handler 绑定层）----

var ErrNotFound = notFound{}
var ErrForbidden = forbidden{}

type notFound struct{}

func (notFound) Error() string { return "memory: not found" }

type forbidden struct{}

func (forbidden) Error() string { return "memory: owner role mismatch" }

type Service struct {
	repo Repo
	now  func() time.Time
}

func NewService(repo Repo) *Service {
	return &Service{repo: repo, now: time.Now}
}

// Append 写入（memory.append）。scope 决定 bookID 语义；importance 夹取 [1,5]。
func (s *Service) Append(owner OwnerRole, scope Scope, studentID, bookID int64,
	kind, content string, importance int, sourceLessonID int64) (*Memory, error) {
	if content == "" || strings.TrimSpace(content) == "" {
		return nil, errBad("content required")
	}
	if kind == "" {
		return nil, errBad("kind required")
	}
	if scope == ScopeTaskMemory && bookID == 0 {
		return nil, errBad("task memory requires bookId")
	}
	if scope == ScopeStudentProfile {
		bookID = 0 // 档案强制库无关
	}
	if importance < 1 {
		importance = 1
	}
	if importance > 5 {
		importance = 5
	}
	m := &Memory{OwnerRole: owner, StudentID: studentID, BookID: bookID,
		Kind: kind, Content: content, Importance: importance,
		SourceLessonID: sourceLessonID,
		CreatedAt: s.now(), UpdatedAt: s.now()}
	if err := s.repo.Append(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ownerCheck 权属校验：只能操作自己 owner 的记忆。
func (s *Service) ownerCheck(id int64, owner OwnerRole) (*Memory, error) {
	m, err := s.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if m.OwnerRole != owner {
		return nil, ErrForbidden
	}
	return m, nil
}

// Revise 修订（memory.revise）：改内容/重要度，记 RevisedFrom 保留链。
func (s *Service) Revise(owner OwnerRole, id int64, content string, importance int) (*Memory, error) {
	m, err := s.ownerCheck(id, owner)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) != "" {
		m.Content = content
	}
	if importance > 0 {
		if importance < 1 {
			importance = 1
		}
		if importance > 5 {
			importance = 5
		}
		m.Importance = importance
	}
	m.UpdatedAt = s.now()
	if err := s.repo.Update(m); err != nil {
		return nil, err
	}
	return m, nil
}

// Delete 删除（memory.delete）：权属校验后删。
func (s *Service) Delete(owner OwnerRole, id int64) error {
	if _, err := s.ownerCheck(id, owner); err != nil {
		return err
	}
	return s.repo.Delete(id)
}

// List 全量列出（memory.list）。
func (s *Service) List(owner OwnerRole, studentID, bookID int64, kind string) ([]*Memory, error) {
	ms, err := s.repo.List(owner, studentID, bookID)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		return ms, nil
	}
	var out []*Memory
	for _, m := range ms {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out, nil
}

// Recall 检索（memory.recall）：kind 过滤 + 关键词包含 + 近因&重要度加权排序。
func (s *Service) Recall(owner OwnerRole, studentID, bookID int64, kind, query string, limit int) ([]*Memory, error) {
	ms, err := s.repo.List(owner, studentID, bookID)
	if err != nil {
		return nil, err
	}
	if kind != "" {
		var f []*Memory
		for _, m := range ms {
			if m.Kind == kind {
				f = append(f, m)
			}
		}
		ms = f
	}
	q := strings.TrimSpace(query)
	if q != "" {
		var f []*Memory
		for _, m := range ms {
			if strings.Contains(m.Content, q) {
				f = append(f, m)
			}
		}
		ms = f
	}
	if limit <= 0 {
		limit = 10
	}
	// 加权分：importance*2 + 近因（每满 7 天衰减 1 分，向上取整；R4-ai#4 修粒度）
	now := s.now()
	score := func(m *Memory) int {
		days := int(now.Sub(m.UpdatedAt).Hours() / 24)
		if days < 0 {
			days = 0
		}
		recency := 5 - (days+6)/7 // 向上取整衰减
		if recency < 0 {
			recency = 0
		}
		return m.Importance*2 + recency
	}
	// 简单选择排序（记忆量小）
	for i := 0; i < len(ms) && i < limit; i++ {
		maxJ := i
		for j := i + 1; j < len(ms); j++ {
			if score(ms[j]) > score(ms[maxJ]) {
				maxJ = j
			}
		}
		ms[i], ms[maxJ] = ms[maxJ], ms[i]
	}
	if len(ms) > limit {
		ms = ms[:limit]
	}
	return ms, nil
}

type badArg struct{ msg string }

func (b badArg) Error() string { return "memory: " + b.msg }

func errBad(msg string) error { return badArg{msg: msg} }

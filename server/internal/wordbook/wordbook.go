// Package wordbook 实现词库管理（SDD §9 / FR-B1~B5）：
// 词库与词条 CRUD、归一化去重、批量导入、发布门（空库不可发布）。
package wordbook

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ---- 状态与错误 ----

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

var (
	ErrBookNotFound   = fmt.Errorf("wordbook: book not found")
	ErrEntryNotFound  = fmt.Errorf("wordbook: entry not found")
	ErrDuplicateWord  = fmt.Errorf("wordbook: duplicate word in book")
	ErrBookEmpty      = fmt.Errorf("wordbook: empty book cannot publish")
	ErrInvalidStatus  = fmt.Errorf("wordbook: invalid status transition")
	ErrInvalidInput   = fmt.Errorf("wordbook: invalid input")
)

var validGrades = map[string]bool{
	"g7": true, "g8": true, "g9": true, "g10": true, "g11": true, "g12": true, "gaokao": true,
}

// ---- 领域模型 ----

type Book struct {
	ID          int64
	Name        string
	Grade       string
	Description string
	Source      string
	Status      Status
	WordCount   int64
}

type Entry struct {
	ID        int64
	BookID    int64
	Word      string
	NormKey   string   // 归一化 key（小写去非字母数字），库内唯一
	Phonetic  string   // 可空（高中/高考库待补全）
	Meanings  []string // ≤2 条
	Unit      string
	IsPhrase  bool
	FreqRank  int
}

// EntryInput 写入入参（ImportBatch/AddEntry 共用）。
type EntryInput struct {
	Word     string
	Phonetic string
	Meanings []string
	Unit     string
	IsPhrase bool
}

// ImportResult 批量导入结果（FR-B4：预览去重）。
type ImportResult struct {
	Inserted int
	Skipped  []string // 被跳过的原始词（重复/空/无释义）
}

// normKey 归一化：小写 + 仅保留字母数字（与 data/tools/verify_seed.py 口径一致）。
var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func normKey(w string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(strings.TrimSpace(w)), "")
}

// ---- 存储接口 + 内存实现 ----

type Repo interface {
	CreateBook(b *Book) error
	GetBook(id int64) (*Book, error)
	UpdateBook(b *Book) error
	DeleteBook(id int64) error
	ListBooks() ([]*Book, error)

	CreateEntry(e *Entry) error
	GetEntry(id int64) (*Entry, error)
	UpdateEntry(e *Entry) error
	DeleteEntry(id int64) error
	CountEntries(bookID int64) (int64, error)
	EntryByKey(bookID int64, key string) (*Entry, error)
	ListEntries(bookID int64, page, size int) ([]*Entry, int64, error)
}

type MemRepo struct {
	mu      sync.RWMutex
	books   map[int64]*Book
	entries map[int64]*Entry
	nextB   int64
	nextE   int64
}

func NewMemRepo() *MemRepo {
	return &MemRepo{books: map[int64]*Book{}, entries: map[int64]*Entry{}}
}

func cloneBook(b *Book) *Book { c := *b; return &c }

func cloneEntry(e *Entry) *Entry {
	c := *e
	c.Meanings = append([]string(nil), e.Meanings...)
	return &c
}

func (m *MemRepo) CreateBook(b *Book) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextB++
	b.ID = m.nextB
	m.books[b.ID] = cloneBook(b)
	return nil
}

func (m *MemRepo) GetBook(id int64) (*Book, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.books[id]
	if !ok {
		return nil, ErrBookNotFound
	}
	return cloneBook(b), nil
}

func (m *MemRepo) UpdateBook(b *Book) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.books[b.ID]; !ok {
		return ErrBookNotFound
	}
	m.books[b.ID] = cloneBook(b)
	return nil
}

func (m *MemRepo) DeleteBook(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.books[id]; !ok {
		return ErrBookNotFound
	}
	delete(m.books, id)
	for k, e := range m.entries {
		if e.BookID == id {
			delete(m.entries, k)
		}
	}
	return nil
}

func (m *MemRepo) ListBooks() ([]*Book, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Book, 0, len(m.books))
	for _, b := range m.books {
		out = append(out, cloneBook(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemRepo) CreateEntry(e *Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextE++
	e.ID = m.nextE
	m.entries[e.ID] = cloneEntry(e)
	return nil
}

func (m *MemRepo) GetEntry(id int64) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, ErrEntryNotFound
	}
	return cloneEntry(e), nil
}

func (m *MemRepo) UpdateEntry(e *Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[e.ID]; !ok {
		return ErrEntryNotFound
	}
	m.entries[e.ID] = cloneEntry(e)
	return nil
}

func (m *MemRepo) DeleteEntry(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
	return nil
}

func (m *MemRepo) CountEntries(bookID int64) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var n int64
	for _, e := range m.entries {
		if e.BookID == bookID {
			n++
		}
	}
	return n, nil
}

func (m *MemRepo) EntryByKey(bookID int64, key string) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if e.BookID == bookID && e.NormKey == key {
			return cloneEntry(e), nil
		}
	}
	return nil, ErrEntryNotFound
}

func (m *MemRepo) ListEntries(bookID int64, page, size int) ([]*Entry, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []*Entry
	for _, e := range m.entries {
		if e.BookID == bookID {
			all = append(all, cloneEntry(e))
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	total := int64(len(all))
	start := (page - 1) * size
	if start >= len(all) {
		return []*Entry{}, total, nil
	}
	end := start + size
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

// ---- Service ----

type Service struct {
	repo Repo
}

func NewService(repo Repo) *Service { return &Service{repo: repo} }

func (s *Service) CreateBook(name, grade, description string) (*Book, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	if !validGrades[grade] {
		return nil, fmt.Errorf("%w: bad grade %q", ErrInvalidInput, grade)
	}
	b := &Book{Name: name, Grade: grade, Description: description, Status: StatusDraft}
	if err := s.repo.CreateBook(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) GetBook(id int64) (*Book, error) { return s.repo.GetBook(id) }

func (s *Service) UpdateBook(b *Book) error {
	if strings.TrimSpace(b.Name) == "" || !validGrades[b.Grade] {
		return fmt.Errorf("%w: bad name/grade", ErrInvalidInput)
	}
	return s.repo.UpdateBook(b)
}

func (s *Service) DeleteBook(id int64) error { return s.repo.DeleteBook(id) }

func (s *Service) ListBooks() ([]*Book, error) { return s.repo.ListBooks() }

// validateEntry 校验并规范化词条输入。
func validateEntry(in EntryInput) (*Entry, error) {
	w := strings.TrimSpace(in.Word)
	if w == "" {
		return nil, fmt.Errorf("%w: empty word", ErrInvalidInput)
	}
	ms := make([]string, 0, 2)
	for _, m := range in.Meanings {
		if t := strings.TrimSpace(m); t != "" {
			ms = append(ms, t)
		}
		if len(ms) == 2 {
			break // ≤2 条（B2/释义学段适配）
		}
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("%w: meanings required", ErrInvalidInput)
	}
	return &Entry{
		Word:     w,
		NormKey:  normKey(w),
		Phonetic: strings.TrimSpace(in.Phonetic),
		Meanings: ms,
		Unit:     in.Unit,
		IsPhrase: in.IsPhrase,
	}, nil
}

func (s *Service) AddEntry(bookID int64, in EntryInput) (*Entry, error) {
	if _, err := s.repo.GetBook(bookID); err != nil {
		return nil, err
	}
	e, err := validateEntry(in)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.EntryByKey(bookID, e.NormKey); err == nil {
		return nil, ErrDuplicateWord
	}
	e.BookID = bookID
	if err := s.repo.CreateEntry(e); err != nil {
		return nil, err
	}
	s.refreshCount(bookID)
	return e, nil
}

func (s *Service) UpdateEntry(e *Entry) error {
	ne, err := validateEntry(EntryInput{Word: e.Word, Phonetic: e.Phonetic, Meanings: e.Meanings, Unit: e.Unit, IsPhrase: e.IsPhrase})
	if err != nil {
		return err
	}
	e.NormKey = ne.NormKey
	e.Meanings = ne.Meanings
	return s.repo.UpdateEntry(e)
}

func (s *Service) GetEntry(id int64) (*Entry, error) { return s.repo.GetEntry(id) }

func (s *Service) DeleteEntry(id int64) error { return s.repo.DeleteEntry(id) }

func (s *Service) ListEntries(bookID int64, page, size int) ([]*Entry, int64, error) {
	return s.repo.ListEntries(bookID, page, size)
}

// EntriesByIDs 按 ID 集合取词条（课堂批次组装用；保持入参顺序）。
func (s *Service) EntriesByIDs(bookID int64, ids []int64) ([]*Entry, error) {
	if _, err := s.repo.GetBook(bookID); err != nil {
		return nil, err
	}
	all, _, err := s.repo.ListEntries(bookID, 1, 1<<30)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Entry, len(all))
	for _, e := range all {
		byID[e.ID] = e
	}
	out := make([]*Entry, 0, len(ids))
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Service) CountEntries(bookID int64) (int64, error) { return s.repo.CountEntries(bookID) }

// EntryByKey 按归一化 key 查词条（反馈 E 难词定位用）。
func (s *Service) EntryByKey(bookID int64, key string) (*Entry, error) {
	return s.repo.EntryByKey(bookID, key)
}

// ImportBatch 批量导入：逐条校验，重复/非法跳过并记录（不中断整批，B4）。
func (s *Service) ImportBatch(bookID int64, ins []EntryInput) (*ImportResult, error) {
	if _, err := s.repo.GetBook(bookID); err != nil {
		return nil, err
	}
	res := &ImportResult{}
	for _, in := range ins {
		e, err := validateEntry(in)
		if err != nil {
			res.Skipped = append(res.Skipped, in.Word)
			continue
		}
		if _, err := s.repo.EntryByKey(bookID, e.NormKey); err == nil {
			res.Skipped = append(res.Skipped, in.Word)
			continue
		}
		e.BookID = bookID
		if err := s.repo.CreateEntry(e); err != nil {
			res.Skipped = append(res.Skipped, in.Word)
			continue
		}
		res.Inserted++
	}
	s.refreshCount(bookID)
	return res, nil
}

func (s *Service) refreshCount(bookID int64) {
	n, _ := s.repo.CountEntries(bookID)
	if b, err := s.repo.GetBook(bookID); err == nil && b.WordCount != n {
		b.WordCount = n
		_ = s.repo.UpdateBook(b)
	}
}

// Publish 发布：空库拒绝（学生可用性保障）。
func (s *Service) Publish(bookID int64) error {
	b, err := s.repo.GetBook(bookID)
	if err != nil {
		return err
	}
	if b.Status == StatusPublished {
		return ErrInvalidStatus
	}
	if b.Status != StatusDraft {
		return ErrInvalidStatus
	}
	n, err := s.repo.CountEntries(bookID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrBookEmpty
	}
	b.Status = StatusPublished
	return s.repo.UpdateBook(b)
}

// Archive 下架：published → archived。
func (s *Service) Archive(bookID int64) error {
	b, err := s.repo.GetBook(bookID)
	if err != nil {
		return err
	}
	if b.Status != StatusPublished {
		return ErrInvalidStatus
	}
	b.Status = StatusArchived
	return s.repo.UpdateBook(b)
}

// ListPublished 学生端词库广场：仅已发布。
func (s *Service) ListPublished() ([]*Book, error) {
	all, err := s.repo.ListBooks()
	if err != nil {
		return nil, err
	}
	out := make([]*Book, 0, len(all))
	for _, b := range all {
		if b.Status == StatusPublished {
			out = append(out, b)
		}
	}
	return out, nil
}

// ---- 种子文档（data/seed/wordbook-*.json）----

type SeedEntry struct {
	Word     string   `json:"word"`
	Phonetic string   `json:"phonetic"`
	Meanings []string `json:"meanings"`
	Unit     string   `json:"unit"`
	IsPhrase bool     `json:"is_phrase"`
}

type SeedDoc struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Grade   string      `json:"grade"`
	Source  string      `json:"source"`
	Entries []SeedEntry `json:"entries"`
}

func (d *SeedDoc) ToInputs() []EntryInput {
	out := make([]EntryInput, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, EntryInput{
			Word: e.Word, Phonetic: e.Phonetic, Meanings: e.Meanings, Unit: e.Unit, IsPhrase: e.IsPhrase,
		})
	}
	return out
}

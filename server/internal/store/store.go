// Package store SQLite 持久化层（SDD §9 落地——消灭"重启丢数据"）。
// 纯 Go 驱动 glebarez/sqlite（无 cgo，本机无 gcc 也可用）+ GORM。
// 表结构 = SDD §9 数据模型；Repo 实现满足各域 Repo 接口（内存版可无缝替换）。
package store

import (
	"encoding/json"
	"errors"
	"time"

	"enwords/internal/wordbook"

	"gorm.io/gorm"
)

// ---- 实体（GORM 模型；列名 snake_case 自动映射） ----

type User struct {
	ID        int64  `gorm:"primaryKey"`
	Username  string `gorm:"uniqueIndex"`
	Nickname  string
	Role      string
	Status    string
	Salt      string
	Hash      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type WordBook struct {
	ID          int64  `gorm:"primaryKey"`
	Name        string `gorm:"uniqueIndex"`
	Grade       string
	Description string
	Source      string
	Status      string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type WordEntry struct {
	ID       int64  `gorm:"primaryKey"`
	BookID   int64  `gorm:"index:idx_book_word,unique;index:idx_book_norm,unique"` // 双复合索引成员
	Word     string `gorm:"index:idx_book_word,unique"`
	NormKey  string `gorm:"index:idx_book_norm,unique"` // 复合 (book_id,norm_key)：跨库同词不冲突
	Phonetic string
	Meanings string // JSON 数组
	Unit     string
	IsPhrase bool
	FreqRank int
}

type MemoryCard struct {
	ID                 int64  `gorm:"primaryKey"`
	StudentID          int64  `gorm:"uniqueIndex:idx_card_stu_word"`
	BookID             int64  `gorm:"uniqueIndex:idx_card_stu_word"`
	WordID             int64  `gorm:"uniqueIndex:idx_card_stu_word"`
	Status             string `gorm:"index:idx_card_due,priority:2"`
	LadderStep         int
	ConsecutiveCorrect int
	WrongCount         int
	BoostFlag          bool
	BoostSequence      int64
	FirstLearnedAt     *time.Time
	LastReviewAt       *time.Time
	NextDueAt          *time.Time `gorm:"index:idx_card_due,priority:1"`
}

type Lesson struct {
	ID            int64 `gorm:"primaryKey"`
	StudentID     int64 `gorm:"index"`
	BookID        int64 `gorm:"index"`
	Seq           int
	Status        string `gorm:"index"`
	OpenAt        time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	BatchWordIDs  string // JSON 数组
	Phase         string
	GroupIdx      int // 废弃（D-21 组架构移除），保留兼容旧库
	WordIdx       int
	RepeatCount   int
	TtsSegIdx     int
	QuizState     string
	CumulativeIDs string // 废弃（D-21 综测池移除），保留兼容旧库
	Result        string // JSON 课报
	StateUpdAt    *time.Time
}

type ClassroomEvent struct {
	ID       int64 `gorm:"primaryKey"`
	LessonID int64 `gorm:"index"`
	TS       time.Time
	Event    string
	Payload  string // JSON
	TraceID  string
}

type WsTicket struct {
	ID        int64  `gorm:"primaryKey"`
	Ticket    string `gorm:"uniqueIndex"`
	LessonID  int64
	StudentID int64
	ExpiresAt time.Time
	CreatedAt time.Time
}

type ConfigGlobal struct {
	Key       string `gorm:"primaryKey"`
	Value     string // JSON
	UpdatedBy string
	Reason    string
	UpdatedAt time.Time
}

type ConfigOverride struct {
	ID        int64  `gorm:"primaryKey"`
	StudentID int64  `gorm:"uniqueIndex:idx_ovr"`
	Key       string `gorm:"uniqueIndex:idx_ovr"`
	Value     string
	UpdatedBy string
	Reason    string
	UpdatedAt time.Time
}

type Feedback struct {
	ID        int64 `gorm:"primaryKey"`
	StudentID int64 `gorm:"index"`
	Channel   string
	Text      string
	Status    string `gorm:"index"`
	Intents   string
	Reply     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Notification struct {
	ID        int64 `gorm:"primaryKey"`
	UserID    int64 `gorm:"index"`
	Type      string
	Title     string
	Body      string
	RefID     int64
	Read      bool
	CreatedAt time.Time
}

type AgentMemory struct {
	ID             int64  `gorm:"primaryKey"`
	OwnerRole      string `gorm:"index:idx_mem"`
	StudentID      int64  `gorm:"index:idx_mem"`
	BookID         int64  `gorm:"index:idx_mem"`
	Kind           string
	Content        string
	Importance     int
	SourceLessonID int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AuditLog struct {
	ID         int64     `gorm:"primaryKey"`
	TS         time.Time `gorm:"index"`
	ActorType  string
	ActorID    string
	ActorRole  string
	Tool       string
	Risk       string
	OK         bool
	Result     string
	Args       string
	DurationMs int64
	TraceID    string
}

type PlacementDraft struct {
	StudentID int64  `gorm:"primaryKey;autoIncrement:false"`
	BookID    int64  `gorm:"primaryKey;autoIncrement:false"`
	KnownIDs  string // JSON
	UpdatedAt time.Time
}

// Open 打开/初始化 SQLite（WAL+busy_timeout，SDD §9 配置）。

// MeaningsJSON 辅助：[]string ↔ JSON。
func ToJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func FromJSON(s string, v any) {
	if s == "" {
		return
	}
	_ = json.Unmarshal([]byte(s), v)
}

// ---- wordbook.Repo 实现 ----

type WordbookRepo struct{ db *gorm.DB }

func NewWordbookRepo(db *gorm.DB) *WordbookRepo { return &WordbookRepo{db: db} }

func (r *WordbookRepo) CreateBook(b *wordbook.Book) error {
	return r.db.Create(&WordBook{ID: b.ID, Name: b.Name, Grade: b.Grade,
		Description: b.Description, Source: b.Source, Status: string(b.Status)}).Error
}

func (r *WordbookRepo) GetBook(id int64) (*wordbook.Book, error) {
	var w WordBook
	if err := r.db.First(&w, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, wordbook.ErrBookNotFound
		}
		return nil, err
	}
	return &wordbook.Book{ID: w.ID, Name: w.Name, Grade: w.Grade,
		Description: w.Description, Source: w.Source,
		Status: wordbook.Status(w.Status), WordCount: r.countWords(w.ID)}, nil
}

func (r *WordbookRepo) UpdateBook(b *wordbook.Book) error {
	return r.db.Model(&WordBook{}).Where("id = ?", b.ID).Updates(map[string]any{
		"name": b.Name, "grade": b.Grade, "description": b.Description,
		"source": b.Source, "status": string(b.Status)}).Error
}

func (r *WordbookRepo) DeleteBook(id int64) error {
	r.db.Where("book_id = ?", id).Delete(&WordEntry{})
	return r.db.Delete(&WordBook{}, id).Error
}

func (r *WordbookRepo) ListBooks() ([]*wordbook.Book, error) {
	var ws []WordBook
	if err := r.db.Order("id").Find(&ws).Error; err != nil {
		return nil, err
	}
	out := make([]*wordbook.Book, 0, len(ws))
	for _, w := range ws {
		out = append(out, &wordbook.Book{ID: w.ID, Name: w.Name, Grade: w.Grade,
			Description: w.Description, Source: w.Source,
			Status: wordbook.Status(w.Status), WordCount: r.countWords(w.ID)})
	}
	return out, nil
}

func (r *WordbookRepo) countWords(bookID int64) int64 {
	var n int64
	r.db.Model(&WordEntry{}).Where("book_id = ?", bookID).Count(&n)
	return n
}

func (r *WordbookRepo) CreateEntry(e *wordbook.Entry) error {
	return r.db.Create(&WordEntry{ID: e.ID, BookID: e.BookID, Word: e.Word,
		NormKey: e.NormKey, Phonetic: e.Phonetic, Meanings: ToJSON(e.Meanings),
		Unit: e.Unit, IsPhrase: e.IsPhrase, FreqRank: e.FreqRank}).Error
}

func (r *WordbookRepo) GetEntry(id int64) (*wordbook.Entry, error) {
	var w WordEntry
	if err := r.db.First(&w, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, wordbook.ErrEntryNotFound
		}
		return nil, err
	}
	return entryFrom(&w), nil
}

func entryFrom(w *WordEntry) *wordbook.Entry {
	e := &wordbook.Entry{ID: w.ID, BookID: w.BookID, Word: w.Word,
		Phonetic: w.Phonetic, Unit: w.Unit, IsPhrase: w.IsPhrase, FreqRank: w.FreqRank}
	FromJSON(w.Meanings, &e.Meanings)
	return e
}

func (r *WordbookRepo) UpdateEntry(e *wordbook.Entry) error {
	return r.db.Model(&WordEntry{}).Where("id = ?", e.ID).Updates(map[string]any{
		"word": e.Word, "norm_key": e.NormKey, "phonetic": e.Phonetic,
		"meanings": ToJSON(e.Meanings), "unit": e.Unit, "is_phrase": e.IsPhrase}).Error
}

func (r *WordbookRepo) DeleteEntry(id int64) error {
	return r.db.Delete(&WordEntry{}, id).Error
}

func (r *WordbookRepo) CountEntries(bookID int64) (int64, error) {
	var n int64
	err := r.db.Model(&WordEntry{}).Where("book_id = ?", bookID).Count(&n).Error
	return n, err
}

func (r *WordbookRepo) EntryByKey(bookID int64, key string) (*wordbook.Entry, error) {
	var w WordEntry
	if err := r.db.Where("book_id = ? AND norm_key = ?", bookID, key).First(&w).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, wordbook.ErrEntryNotFound
		}
		return nil, err
	}
	return entryFrom(&w), nil
}

func (r *WordbookRepo) ListEntries(bookID int64, page, size int) ([]*wordbook.Entry, int64, error) {
	var total int64
	r.db.Model(&WordEntry{}).Where("book_id = ?", bookID).Count(&total)
	var ws []WordEntry
	if err := r.db.Where("book_id = ?", bookID).Order("id").
		Offset((page - 1) * size).Limit(size).Find(&ws).Error; err != nil {
		return nil, 0, err
	}
	out := make([]*wordbook.Entry, 0, len(ws))
	for i := range ws {
		out = append(out, entryFrom(&ws[i]))
	}
	return out, total, nil
}

// EntriesByIDs bounds returned rows to the classroom batch and scopes every query.
func (r *WordbookRepo) EntriesByIDs(bookID int64, ids []int64) ([]*wordbook.Entry, error) {
	out := make([]*wordbook.Entry, 0, len(ids))
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	for start := 0; start < len(unique); start += 500 {
		end := start + 500
		if end > len(unique) {
			end = len(unique)
		}
		var rows []WordEntry
		if err := r.db.Where("book_id = ? AND id IN ?", bookID, unique[start:end]).Find(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			out = append(out, entryFrom(&rows[i]))
		}
	}
	return out, nil
}

// SelfInc 自增 ID 序列（内存版由 repo 维护；SQLite 用 MAX(id)+1 简化）。
func (r *WordbookRepo) NextBookID() int64 {
	var m int64
	r.db.Model(&WordBook{}).Select("COALESCE(MAX(id),0)").Scan(&m)
	return m + 1
}

func (r *WordbookRepo) NextEntryID() int64 {
	var m int64
	r.db.Model(&WordEntry{}).Select("COALESCE(MAX(id),0)").Scan(&m)
	return m + 1
}

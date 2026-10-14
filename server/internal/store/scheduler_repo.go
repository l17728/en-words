// scheduler 域 SQLite Repo 实现（记忆卡/课程/摸底草稿——SDD §9 对应表）。
package store

import (
	"encoding/json"
	"time"

	"enwords/internal/scheduler"

	"gorm.io/gorm"
)

// ---- 转换 ----

func cardTo(m *MemoryCard) *scheduler.Card {
	c := &scheduler.Card{StudentID: m.StudentID, BookID: m.BookID, WordID: m.WordID,
		Status: scheduler.Status(m.Status), LadderStep: m.LadderStep,
		BoostFlag: m.BoostFlag, BoostSequence: m.BoostSequence, LastReviewAt: m.LastReviewAt, NextDueAt: m.NextDueAt}
	return c
}

func cardFrom(c *scheduler.Card) *MemoryCard {
	return &MemoryCard{StudentID: c.StudentID, BookID: c.BookID, WordID: c.WordID,
		Status: string(c.Status), LadderStep: c.LadderStep,
		BoostFlag: c.BoostFlag, BoostSequence: c.BoostSequence, LastReviewAt: c.LastReviewAt, NextDueAt: c.NextDueAt}
}

func lessonTo(l *Lesson) *scheduler.Lesson {
	out := &scheduler.Lesson{ID: l.ID, StudentID: l.StudentID, BookID: l.BookID,
		Status: l.Status, OpenAt: l.OpenAt}
	out.FinishedAt = l.FinishedAt
	_ = json.Unmarshal([]byte(l.BatchWordIDs), &out.BatchWordIDs)
	return out
}

func lessonFrom(l *scheduler.Lesson) *Lesson {
	b, _ := json.Marshal(l.BatchWordIDs)
	return &Lesson{ID: l.ID, StudentID: l.StudentID, BookID: l.BookID,
		Status: l.Status, OpenAt: l.OpenAt,
		FinishedAt:   l.FinishedAt,
		BatchWordIDs: string(b)}
}

// ---- SchedulerRepo ----

type SchedulerRepo struct{ db *gorm.DB }

func NewSchedulerRepo(db *gorm.DB) *SchedulerRepo { return &SchedulerRepo{db: db} }

func (r *SchedulerRepo) CreateCard(c *scheduler.Card) error {
	return r.db.Create(cardFrom(c)).Error
}

func (r *SchedulerRepo) GetCard(studentID, bookID, wordID int64) (*scheduler.Card, error) {
	var m MemoryCard
	if err := r.db.Where("student_id=? AND book_id=? AND word_id=?", studentID, bookID, wordID).
		First(&m).Error; err != nil {
		return nil, err
	}
	return cardTo(&m), nil
}

func (r *SchedulerRepo) SaveCard(c *scheduler.Card) error {
	m := cardFrom(c)
	// 复合主键语义：按三元组 upsert（ID 可能未填）
	res := r.db.Where("student_id=? AND book_id=? AND word_id=?",
		m.StudentID, m.BookID, m.WordID).First(&MemoryCard{})
	if res.Error != nil {
		if res.Error != gorm.ErrRecordNotFound {
			return res.Error
		}
		return r.db.Create(m).Error
	}
	return r.db.Model(&MemoryCard{}).Where("student_id=? AND book_id=? AND word_id=?",
		m.StudentID, m.BookID, m.WordID).Updates(map[string]any{
		"status": m.Status, "ladder_step": m.LadderStep,
		"boost_flag": m.BoostFlag, "boost_sequence": m.BoostSequence, "last_review_at": m.LastReviewAt,
		"next_due_at": m.NextDueAt}).Error
}

func (r *SchedulerRepo) ListCards(studentID, bookID int64) ([]*scheduler.Card, error) {
	var ms []MemoryCard
	if err := r.db.Where("student_id=? AND book_id=?", studentID, bookID).
		Order("word_id").Find(&ms).Error; err != nil {
		return nil, err
	}
	out := make([]*scheduler.Card, 0, len(ms))
	for i := range ms {
		out = append(out, cardTo(&ms[i]))
	}
	return out, nil
}

func (r *SchedulerRepo) Boost(studentID, bookID, wordID int64) error {
	return r.db.Model(&MemoryCard{}).
		Where("student_id=? AND book_id=? AND word_id=?", studentID, bookID, wordID).
		Update("boost_flag", true).Error
}

func (r *SchedulerRepo) CreateLesson(l *scheduler.Lesson) error {
	rec := lessonFrom(l)
	if rec.ID == 0 { // SQLite 自增
		rec.ID = r.NextLessonID()
	}
	if err := r.db.Create(rec).Error; err != nil {
		return err
	}
	l.ID = rec.ID // 回写（MemRepo 语义对齐——openNowIfTestMode 后续 SaveLesson 依赖）
	return nil
}

func (r *SchedulerRepo) GetLesson(id int64) (*scheduler.Lesson, error) {
	var l Lesson
	if err := r.db.First(&l, id).Error; err != nil {
		return nil, err
	}
	return lessonTo(&l), nil
}

func (r *SchedulerRepo) SaveLesson(l *scheduler.Lesson) error {
	rec := lessonFrom(l)
	if rec.ID == 0 { // 防 GORM Save 零值主键插入新行（双课 BUG 真凶）
		rec.ID = r.NextLessonID()
		l.ID = rec.ID
	}
	return r.db.Save(rec).Error
}

func (r *SchedulerRepo) ListLessons(studentID, bookID int64) ([]*scheduler.Lesson, error) {
	var ls []Lesson
	if err := r.db.Where("student_id=? AND book_id=?", studentID, bookID).
		Order("id").Find(&ls).Error; err != nil {
		return nil, err
	}
	out := make([]*scheduler.Lesson, 0, len(ls))
	for i := range ls {
		out = append(out, lessonTo(&ls[i]))
	}
	return out, nil
}

// StudentBooks 某生已启用词库 ID 列表（enablement=记忆卡存在，升序去重）。
func (r *SchedulerRepo) StudentBooks(studentID int64) ([]int64, error) {
	var ids []int64
	if err := r.db.Model(&MemoryCard{}).
		Where("student_id=?", studentID).
		Distinct().Order("book_id").Pluck("book_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteStudentBook 移除某生某词库的全部记忆卡和课程（取消选库）。
func (r *SchedulerRepo) DeleteStudentBook(studentID, bookID int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("student_id=? AND book_id=?", studentID, bookID).Delete(&MemoryCard{}).Error; err != nil {
			return err
		}
		return tx.Where("student_id=? AND book_id=?", studentID, bookID).Delete(&Lesson{}).Error
	})
}

// ListLessonsAll 跨学生全量（api CronOverdue 巡检用；替代内存版 repoLessonsAll）。
func (r *SchedulerRepo) ListLessonsAll() ([]*scheduler.Lesson, error) {
	var ls []Lesson
	if err := r.db.Order("id").Find(&ls).Error; err != nil {
		return nil, err
	}
	out := make([]*scheduler.Lesson, 0, len(ls))
	for i := range ls {
		out = append(out, lessonTo(&ls[i]))
	}
	return out, nil
}

// ---- 摸底草稿 ----

func (r *SchedulerRepo) SavePlacementDraft(studentID, bookID int64, knownIDs []int64) error {
	b, _ := json.Marshal(knownIDs)
	return r.db.Save(&PlacementDraft{StudentID: studentID, BookID: bookID,
		KnownIDs: string(b), UpdatedAt: time.Now()}).Error
}

func (r *SchedulerRepo) LoadPlacementDraft(studentID, bookID int64) ([]int64, error) {
	var p PlacementDraft
	if err := r.db.First(&p, "student_id=? AND book_id=?", studentID, bookID).Error; err != nil {
		return nil, nil // 无草稿
	}
	var ids []int64
	_ = json.Unmarshal([]byte(p.KnownIDs), &ids)
	return ids, nil
}

// NextLessonID 课程自增 ID。
func (r *SchedulerRepo) NextLessonID() int64 {
	var m int64
	r.db.Model(&Lesson{}).Select("COALESCE(MAX(id),0)").Scan(&m)
	return m + 1
}

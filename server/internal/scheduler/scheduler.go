// Package scheduler 实现等间隔复习调度器（SDD §6 / FR-F）：
// 间隔阶梯、LadderStep 统一语义（R2/R3）、批次构造（due 全保留+boost+new≤0.6W）、
// 逾期重排（省力再学）、长期毕业与维护、主线 step 中位数。
package scheduler

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ---- 状态与错误 ----

type Status string

const (
	StatusNew       Status = "new"
	StatusLearning  Status = "learning"
	StatusMastered  Status = "mastered" // 摸底已会（不参与教学，仅低频维护）
	StatusGraduated Status = "graduated"
)

type LessonStatus string

const (
	LessonScheduled Status = "scheduled"
	LessonExpired   Status = "expired"
)

const (
	LessonScheduledStr = "scheduled"
	LessonExpiredStr   = "expired"
	LessonFinishedStr  = "finished"
)

var (
	ErrCardNotFound   = errors.New("scheduler: card not found")
	ErrLessonNotFound = errors.New("scheduler: lesson not found")
	ErrNoWords        = errors.New("scheduler: no words to schedule")
	ErrAlreadyPlanned = errors.New("scheduler: next lesson already scheduled")
)

// ---- 领域模型 ----

// Card 记忆卡（SDD §6.1，LadderStep=已成功完成的复习次数=下次间隔索引）。
type Card struct {
	StudentID, BookID, WordID int64
	Status                    Status
	LadderStep                int
	BoostFlag                 bool
	BoostSequence             int64
	LastReviewAt              *time.Time
	NextDueAt                 *time.Time
}

// Lesson 一节课。
type Lesson struct {
	ID           int64
	StudentID    int64
	BookID       int64
	Status       string // scheduled | expired (课堂态另存)
	OpenAt       time.Time
	FinishedAt   *time.Time
	BatchWordIDs []int64
}

// ---- 存储接口 + 内存实现 ----

type Repo interface {
	CreateCard(c *Card) error
	GetCard(studentID, bookID, wordID int64) (*Card, error)
	SaveCard(c *Card) error
	ListCards(studentID, bookID int64) ([]*Card, error)
	Boost(studentID, bookID, wordID int64) error
	DeleteStudentBook(studentID, bookID int64) error

	CreateLesson(l *Lesson) error
	GetLesson(id int64) (*Lesson, error)
	SaveLesson(l *Lesson) error
	ListLessons(studentID, bookID int64) ([]*Lesson, error)
	// StudentBooks 某生已启用词库 ID 列表（enablement=记忆卡存在，升序去重）。
	StudentBooks(studentID int64) ([]int64, error)
}

type MemRepo struct {
	mu         sync.Mutex
	cards      map[[3]int64]*Card
	lessons    []*Lesson
	nextLesson int64
	drafts     map[placementKey][]int64
}

func NewMemRepo() *MemRepo {
	return &MemRepo{cards: map[[3]int64]*Card{}, drafts: map[placementKey][]int64{}}
}

func cardKey(s, b, w int64) [3]int64 { return [3]int64{s, b, w} }

func (m *MemRepo) CreateCard(c *Card) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.cards[cardKey(c.StudentID, c.BookID, c.WordID)] = &cp
	return nil
}

func (m *MemRepo) GetCard(s, b, w int64) (*Card, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cards[cardKey(s, b, w)]
	if !ok {
		return nil, ErrCardNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *MemRepo) SaveCard(c *Card) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.cards[cardKey(c.StudentID, c.BookID, c.WordID)] = &cp
	return nil
}

func (m *MemRepo) ListCards(s, b int64) ([]*Card, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Card
	for _, c := range m.cards {
		if c.StudentID == s && c.BookID == b {
			cp := *c
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WordID < out[j].WordID })
	return out, nil
}

func (m *MemRepo) Boost(s, b, w int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cards[cardKey(s, b, w)]
	if !ok {
		return ErrCardNotFound
	}
	c.BoostFlag = true
	return nil
}

func (m *MemRepo) CreateLesson(l *Lesson) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextLesson++
	l.ID = m.nextLesson
	cp := *l
	cp.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
	m.lessons = append(m.lessons, &cp)
	return nil
}

func (m *MemRepo) GetLesson(id int64) (*Lesson, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.lessons {
		if l.ID == id {
			cp := *l
			cp.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
			return &cp, nil
		}
	}
	return nil, ErrLessonNotFound
}

func (m *MemRepo) SaveLesson(l *Lesson) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.lessons {
		if e.ID == l.ID {
			cp := *l
			cp.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
			m.lessons[i] = &cp
			return nil
		}
	}
	return ErrLessonNotFound
}

func (m *MemRepo) ListLessons(s, b int64) ([]*Lesson, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Lesson
	for _, l := range m.lessons {
		if l.StudentID == s && l.BookID == b {
			cp := *l
			cp.BatchWordIDs = append([]int64(nil), l.BatchWordIDs...)
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemRepo) StudentBooks(s int64) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[int64]bool{}
	var out []int64
	for _, c := range m.cards {
		if c.StudentID == s && !seen[c.BookID] {
			seen[c.BookID] = true
			out = append(out, c.BookID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// DeleteStudentBook 移除某生某词库的全部记忆卡和课程（取消选库）。
func (m *MemRepo) DeleteStudentBook(studentID, bookID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.cards {
		if k[0] == studentID && k[1] == bookID {
			delete(m.cards, k)
		}
	}
	var kept []*Lesson
	for _, l := range m.lessons {
		if l.StudentID == studentID && l.BookID == bookID {
			continue // 删除该生该库的课程
		}
		kept = append(kept, l)
	}
	m.lessons = kept
	return nil
}

// ---- 时钟 ----

type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// ---- Service ----

type LadderSource interface {
	Ladder(studentID int64) []int
	Int(studentID int64, key string) int
}

type Service struct {
	settlementMu       sync.Mutex
	pendingSettlements map[int64]*settlementPlan
	metaMu             sync.RWMutex
	repo               Repo
	cfg                LadderSource
	clock              Clock
	// finishedAt 记录每课实际完成时间（内存版；GORM 版存 lessons.finished_at）
	finishedAt map[int64]time.Time
	// Legacy read fallback for custom repositories; built-in repos own atomic drafts.
	placementDrafts map[placementKey][]int64
	// OnPlanned 排课成功钩子（§14 排课日志：api 装配层注入；可为 nil）
	OnPlanned        func(studentID, bookID int64, l *Lesson)
	OnPlacementSaved func(context.Context, int64, int64, int, int)
}

func NewService(repo Repo, cfg LadderSource, clock Clock) *Service {
	return &Service{repo: repo, cfg: cfg, clock: clock, finishedAt: map[int64]time.Time{},
		placementDrafts: map[placementKey][]int64{}}
}

// interval 安全取阶梯值（防配置缩短越界）。
func interval(ladder []int, step int) int {
	if len(ladder) == 0 {
		return 1
	}
	if step < 0 {
		step = 0
	}
	if step > len(ladder)-1 {
		step = len(ladder) - 1
	}
	return ladder[step]
}

// ---- Service 访问器（api 装配层用）----

// Lesson 按 ID 查课。
func (s *Service) Lesson(id int64) (*Lesson, error) { return s.repo.GetLesson(id) }

// Lessons 某生某库全部课程。
func (s *Service) Lessons(studentID, bookID int64) ([]*Lesson, error) {
	return s.repo.ListLessons(studentID, bookID)
}

// SaveLesson 更新课程行（api 层改状态用）。
func (s *Service) SaveLesson(l *Lesson) error { return s.repo.SaveLesson(l) }

// StudentBooks 某生已启用词库 ID（Dashboard"我的词库"）。
func (s *Service) StudentBooks(studentID int64) ([]int64, error) {
	return s.repo.StudentBooks(studentID)
}

// RemoveBook 移除某生某词库（取消选库：删记忆卡+课程）。
func (s *Service) RemoveBook(studentID, bookID int64) error {
	return s.repo.DeleteStudentBook(studentID, bookID)
}

// StudentLessons 某生全部课程（跨词库；反馈 D 定位下一课用）。
func (s *Service) StudentLessons(studentID int64) ([]*Lesson, error) {
	var out []*Lesson
	for _, l := range s.repoLessonsAll() {
		if l.StudentID == studentID {
			cp := *l
			out = append(out, &cp)
		}
	}
	return out, nil
}

// SeedCard 建卡（选库时为词库全部词条建 new 卡）。
func (s *Service) SeedCard(studentID, bookID, wordID int64) error {
	return s.repo.CreateCard(&Card{StudentID: studentID, BookID: bookID, WordID: wordID, Status: StatusNew})
}

// GetCard 查记忆卡（api 装配层/测试用）。
func (s *Service) GetCard(studentID, bookID, wordID int64) (*Card, error) {
	return s.repo.GetCard(studentID, bookID, wordID)
}

// BoostWord 反馈 E：标 BoostFlag。
func (s *Service) BoostWord(studentID, bookID, wordID int64) error {
	return s.repo.Boost(studentID, bookID, wordID)
}

// ResetForReplacement 重新摸底后的计划重置（用户定案 2026-08-23 BUG-5）：
// 作废全部未开始的 scheduled 课（含重排生成的），并把对应批次的学习卡回退 new
// （NextDueAt/Boost 清空）——重新摸底结果以全新状态重建计划。
// R5-P0 守卫：mastered 卡跳过（摸底成果不被批次重置抹掉）。
// 已开始/已完成的课与其卡片不动（历史保留）。
func (s *Service) ResetForReplacement(studentID, bookID int64) error {
	lessons, err := s.repo.ListLessons(studentID, bookID)
	if err != nil {
		return err
	}
	for _, l := range lessons {
		if l.Status != LessonScheduledStr {
			continue
		}
		l.Status = "voided_replacement"
		if err := s.repo.SaveLesson(l); err != nil {
			return err
		}
		// 批次卡回退 new（mastered 跳过——R5-P0）
		for _, wid := range l.BatchWordIDs {
			if c, err := s.repo.GetCard(studentID, bookID, wid); err == nil {
				if c.Status == StatusMastered || c.Status == StatusGraduated {
					continue
				}
				c.Status = StatusNew
				c.NextDueAt = nil
				c.BoostFlag = false
				if err := s.repo.SaveCard(c); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// MarkPlacement 摸底结算：勾选词 mastered，**取消勾选的回退 new**（覆盖语义——
// 用户定案：重新摸底以最新勾选为准，点错的可取消）。
// R5-P0 守卫：Learning/Graduated 卡不可被摸底覆盖（教学成果保护）。
// 草稿与实际状态变化同事务提交；未变化卡不写，存储失败不得部分发布。
func (s *Service) MarkPlacement(studentID, bookID int64, knownWordIDs []int64) error {
	return s.MarkPlacementContext(context.Background(), studentID, bookID, knownWordIDs)
}

// PlacementDraft 读取摸底草稿（已勾选词 ID；无草稿返回 nil）。
func (s *Service) PlacementDraft(studentID, bookID int64) []int64 {
	if dr, ok := s.repo.(draftRepo); ok {
		if ids, err := dr.LoadPlacementDraft(studentID, bookID); err == nil {
			return ids
		}
		return nil
	}
	return s.placementDrafts[placementKey{studentID, bookID}]
}

type placementKey struct{ studentID, bookID int64 }

// 草稿读取兼容接口：SQLite 与 MemRepo 均实现；保存走原子能力接口。
type draftRepo interface {
	SavePlacementDraft(studentID, bookID int64, knownIDs []int64) error
	LoadPlacementDraft(studentID, bookID int64) ([]int64, error)
}

// mainlineStep 主线 step = 未毕业词(learning) LadderStep 的中位数（R2-04）。
func (s *Service) mainlineStep(cards []*Card) int {
	var steps []int
	for _, c := range cards {
		if c.Status == StatusLearning {
			steps = append(steps, c.LadderStep)
		}
	}
	if len(steps) == 0 {
		return 0
	}
	sort.Ints(steps)
	return steps[len(steps)/2]
}

// PlanNext 排下一课（SDD §6.3）。OnPlanned 钩子用于 §14 排课日志（api 装配层注入）。
func (s *Service) PlanNext(studentID, bookID int64) (*Lesson, error) {
	return s.PlanNextWithPolicy(studentID, bookID, PlanPolicy{})
}

// PlanNextWithPolicy applies an optional policy to this batch only. Persistence
// transactions remain the caller's responsibility, as with PlanNext.
func (s *Service) PlanNextWithPolicy(studentID, bookID int64, policy PlanPolicy) (*Lesson, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	l, err := s.planNext(studentID, bookID, policy)
	if err == nil && s.OnPlanned != nil {
		s.OnPlanned(studentID, bookID, l)
	}
	return l, err
}

func (s *Service) planNext(studentID, bookID int64, policy PlanPolicy) (*Lesson, error) {
	now := s.clock.Now()
	W := s.cfg.Int(studentID, "wordsPerLesson")
	if W < 50 || W > 100 {
		return nil, ErrInvalidLessonSize
	}
	ladder := s.cfg.Ladder(studentID)

	cards, err := s.repo.ListCards(studentID, bookID)
	if err != nil {
		return nil, err
	}

	// 已有 scheduled 未过期课 → 不重复排（只排下一课 F2）
	// R5：voided_replacement 是作废课不拦截；其余非 scheduled 状态（finished/expired/voided）同样放行
	lessons, err := s.repo.ListLessons(studentID, bookID)
	if err != nil {
		return nil, err
	}
	for _, l := range lessons {
		if l.Status == LessonScheduledStr {
			return nil, ErrAlreadyPlanned
		}
	}

	// 1. due 全保留（learning 且 NextDueAt ≤ now+24h，按到期升序）
	var due []*Card
	for _, c := range cards {
		if c.Status == StatusLearning && c.NextDueAt != nil && !c.NextDueAt.After(now.Add(24*time.Hour)) {
			due = append(due, c)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].NextDueAt.Before(*due[j].NextDueAt) })

	// 2. boost（未在 due；new/learning 均可——新词也可被反馈标"难记"，排除已毕业/摸底已会）
	var boosted []*Card
	for _, c := range cards {
		if c.BoostFlag && (c.Status == StatusLearning || c.Status == StatusNew) && !containsCard(due, c) {
			boosted = append(boosted, c)
		}
	}

	// 3. new（按词库序；已在 boost 的跳过防重复）
	var fresh []*Card
	for _, c := range cards {
		if c.Status == StatusNew && !containsCard(boosted, c) {
			fresh = append(fresh, c)
		}
	}

	// 4. 批次构造（顺序与保留性语义，SC-04 + SDD §6.3 统一）：
	//    顺序：boosted 头部 → due（按到期序）→ new 补足；
	//    保留性：due 全保留（不足 W 时）；new ≤ max(0.6W, 剩余额度)（首课可全为新词）
	batch := make([]int64, 0, W)
	if policy.MaxNewRatio > 0 {
		batch = policy.batch(W, due, boosted, fresh, cards, lessons)
	} else {
		for _, c := range boosted {
			if len(batch) >= W {
				break
			}
			batch = append(batch, c.WordID)
		}
		for _, c := range due {
			if len(batch) >= W {
				break
			}
			batch = append(batch, c.WordID)
		}
		newLimit := W * 6 / 10
		if rem := W - len(batch); rem > newLimit {
			newLimit = rem
		}
		newCount := 0
		for _, c := range fresh {
			if len(batch) >= W || newCount >= newLimit {
				break
			}
			batch = append(batch, c.WordID)
			newCount++
		}
	}
	if len(batch) == 0 {
		return nil, ErrNoWords
	}

	// 5. lessonTime = 最近 finished_at + ladder[主线中位数]；首课 = now + ladder[0] 天数语义(1d)
	var base time.Time
	if ft, ok := s.lastFinishedAt(studentID, bookID); ok {
		base = ft
	} else {
		base = now
		stepDays := time.Duration(interval(ladder, 0)) * 24 * time.Hour
		openAt := base.Add(stepDays)
		l := &Lesson{StudentID: studentID, BookID: bookID, Status: LessonScheduledStr,
			OpenAt: openAt, BatchWordIDs: batch}
		if err := s.repo.CreateLesson(l); err != nil {
			return nil, err
		}
		if err := s.reserveCards(studentID, bookID, batch, l.OpenAt); err != nil {
			return nil, err
		}
		return l, nil
	}
	step := s.mainlineStep(cards)
	openAt := base.Add(time.Duration(interval(ladder, step)) * 24 * time.Hour)
	l := &Lesson{StudentID: studentID, BookID: bookID, Status: LessonScheduledStr,
		OpenAt: openAt, BatchWordIDs: batch}
	if err := s.repo.CreateLesson(l); err != nil {
		return nil, err
	}
	if err := s.reserveCards(studentID, bookID, batch, l.OpenAt); err != nil {
		return nil, err
	}
	return l, nil
}

// ReserveCards 约课到课：NextDueAt=lessonTime（api 装配层 testmode 重拨时复用）。
func (s *Service) ReserveCards(studentID, bookID int64, batch []int64, at time.Time) error {
	return s.reserveCards(studentID, bookID, batch, at)
}

func (s *Service) reserveCards(studentID, bookID int64, batch []int64, at time.Time) error {
	for _, w := range batch {
		c, err := s.repo.GetCard(studentID, bookID, w)
		if err != nil {
			return err
		}
		t := at
		c.NextDueAt = &t
		if err := s.repo.SaveCard(c); err != nil {
			return err
		}
	}
	return nil
}

// lastFinishedAt 最近一次完成课时间。
func (s *Service) lastFinishedAt(studentID, bookID int64) (time.Time, bool) {
	var latest time.Time
	found := false
	lessons, _ := s.repo.ListLessons(studentID, bookID)
	for _, l := range lessons {
		if l.FinishedAt != nil && (!found || l.FinishedAt.After(latest)) {
			latest = *l.FinishedAt
			found = true
		}
		if ft, ok := s.finishedTime(l.ID); ok && (!found || ft.After(latest)) {
			latest = ft
			found = true
		}
	}
	return latest, found
}

// SettleLessonFinished 结课结算（SDD §6.2 step 推进规则）：
// 答对≥1 → 视为本次复习成功 step+1（新学课 step 0→0 除外——首课不 +1）；
// 全错 → step 不变但 LastReviewAt 更新；新词 new → learning。
func (s *Service) SettleLessonFinished(studentID, bookID int64, correct map[int64]bool, finishedAt time.Time) error {
	cards, err := s.repo.ListCards(studentID, bookID)
	if err != nil {
		return err
	}
	lessons, _ := s.repo.ListLessons(studentID, bookID)
	var currentLessonID int64 = -1
	for _, l := range lessons {
		if _, done := s.finishedTime(l.ID); !done && l.Status == LessonScheduledStr {
			currentLessonID = l.ID
		}
	}
	if currentLessonID != -1 {
		s.rememberFinished(currentLessonID, finishedAt)
		// 结课：lesson 置 finished（否则后续排课被"已排课"挡住）
		if l, err := s.repo.GetLesson(currentLessonID); err == nil {
			l.Status = LessonFinishedStr
			_ = s.repo.SaveLesson(l)
		}
	}
	for _, c := range cards {
		wasCorrect, tested := correct[c.WordID]
		if !tested {
			continue // 未出现在本课的词不动
		}
		wasNew := c.Status == StatusNew
		if wasNew {
			c.Status = StatusLearning
		}
		t := finishedAt
		c.LastReviewAt = &t
		c.BoostFlag = false // boost 消费
		ladder := s.cfg.Ladder(studentID)
		if wasCorrect {
			if wasNew {
				// 新学不算复习（R3）：step 保持 0
				c.LadderStep = 0
				d := finishedAt.Add(time.Duration(interval(ladder, 0)) * 24 * time.Hour)
				c.NextDueAt = &d
			} else {
				c.LadderStep++
				if c.LadderStep >= len(ladder) {
					// 长期毕业：走完阶梯且本次（到期测试）答对
					c.Status = StatusGraduated
					c.NextDueAt = nil
				} else {
					d := finishedAt.Add(time.Duration(interval(ladder, c.LadderStep)) * 24 * time.Hour)
					c.NextDueAt = &d
				}
			}
		} else {
			// 全错：step 不变（省力再学），间隔从本课重起
			d := finishedAt.Add(time.Duration(interval(ladder, c.LadderStep)) * 24 * time.Hour)
			c.NextDueAt = &d
		}
		if err := s.repo.SaveCard(c); err != nil {
			return err
		}
	}
	return nil
}

// HandleOverdue is retained only for legacy repository tests.
// Deprecated: production callers must use App.RunOverdue's Agent task transaction.
func (s *Service) HandleOverdue() ([]int64, error) {
	now := s.clock.Now()
	// R6-be：逾期阈值按学生级配置（学生覆盖优先于全局；overdueFor 内联缓存）
	studentOverdue := map[int64]time.Duration{}
	var replanned []int64
	lessons, err := s.repo.ListLessons(0, 0)
	if err != nil {
		return nil, err
	}
	// 全量扫描（cron 视角）——R6-be：每生阈值实时取（缓存去重）
	type sb struct{ s, b int64 }
	seen := map[sb]bool{}
	var pairs []sb
	overdueFor := func(studentID int64) time.Duration {
		if d, ok := studentOverdue[studentID]; ok {
			return d
		}
		d := time.Duration(s.cfg.Int(studentID, "overdueHours")) * time.Hour
		studentOverdue[studentID] = d
		return d
	}
	for _, l := range s.repoLessonsAll() {
		if l.Status == LessonScheduledStr && now.After(l.OpenAt.Add(overdueFor(l.StudentID))) {
			p := sb{l.StudentID, l.BookID}
			if !seen[p] {
				seen[p] = true
				pairs = append(pairs, p)
			}
		}
	}
	_ = lessons
	for _, p := range pairs {
		// 该生该库当前 scheduled 且过期的课 → expired
		oh := overdueFor(p.s)
		ls, _ := s.repo.ListLessons(p.s, p.b)
		var latest *Lesson
		for _, l := range ls {
			if l.Status == LessonScheduledStr && now.After(l.OpenAt.Add(oh)) {
				l.Status = LessonExpiredStr
				_ = s.repo.SaveLesson(l)
				latest = l
			}
		}
		if latest == nil {
			continue
		}
		// 重排：次日同时段；同批次词 step 不变、LastReviewAt 不动（未上课不结算）
		nl := &Lesson{StudentID: p.s, BookID: p.b, Status: LessonScheduledStr,
			OpenAt: latest.OpenAt.Add(24 * time.Hour), BatchWordIDs: latest.BatchWordIDs}
		if err := s.repo.CreateLesson(nl); err != nil {
			return nil, err
		}
		s.reserveCards(p.s, p.b, nl.BatchWordIDs, nl.OpenAt)
		replanned = append(replanned, nl.ID)
	}
	return replanned, nil
}

// Repo 扩展接口（可选）：全量课程列举——SQLite 版实现（内存版走下方分支）。
type allLessoner interface {
	ListLessonsAll() ([]*Lesson, error)
}

// repoLessonsAll 列出全部课（跨学生）：优先 allLessoner（SQLite）；内存版遍历。
func (s *Service) repoLessonsAll() []*Lesson {
	if al, ok := s.repo.(allLessoner); ok {
		if ls, err := al.ListLessonsAll(); err == nil {
			return ls
		}
		return nil
	}
	if all, ok := s.repo.(*MemRepo); ok {
		all.mu.Lock()
		defer all.mu.Unlock()
		var out []*Lesson
		for _, l := range all.lessons {
			cp := *l
			out = append(out, &cp)
		}
		return out
	}
	return nil
}

// MaintenanceCheck 维护期抽查（毕业词每 30 天）：答错重置 step=min(5,len-1) 回 learning。
func (s *Service) MaintenanceCheck(studentID, bookID, wordID int64, correct bool) error {
	c, err := s.repo.GetCard(studentID, bookID, wordID)
	if err != nil {
		return err
	}
	if !correct {
		c.Status = StatusLearning
		ladder := s.cfg.Ladder(studentID)
		step := 5
		if step > len(ladder)-1 {
			step = len(ladder) - 1
		}
		c.LadderStep = step
		t := s.clock.Now()
		c.LastReviewAt = &t
		d := t.Add(time.Duration(interval(ladder, step)) * 24 * time.Hour)
		c.NextDueAt = &d
	} else {
		t := s.clock.Now()
		c.LastReviewAt = &t
	}
	return s.repo.SaveCard(c)
}

// TruncateLadderSteps ladder 配置缩短时截断全部 step（R2-AR-02；由 config.OnLadderChanged 触发）。
func (s *Service) TruncateLadderSteps(newLen int) error {
	// 内存版遍历全部卡（GORM 版为 UPDATE ... WHERE）
	if all, ok := s.repo.(*MemRepo); ok {
		all.mu.Lock()
		defer all.mu.Unlock()
		for _, c := range all.cards {
			if c.LadderStep > newLen-1 {
				c.LadderStep = newLen - 1
			}
		}
	}
	return nil
}

// PostponeLesson 推迟课程（反馈意图 D：≤7 天，目标日语次日同时段语义）。
func (s *Service) PostponeLesson(lessonID int64, to time.Time) error {
	l, err := s.repo.GetLesson(lessonID)
	if err != nil {
		return err
	}
	if l.Status != LessonScheduledStr {
		return errors.New("scheduler: only scheduled lesson can postpone")
	}
	l.OpenAt = to
	if err := s.repo.SaveLesson(l); err != nil {
		return err
	}
	return s.reserveCards(l.StudentID, l.BookID, l.BatchWordIDs, to)
}

func containsCard(xs []*Card, t *Card) bool {
	for _, x := range xs {
		if x.WordID == t.WordID {
			return true
		}
	}
	return false
}

// SaveCardDirect 内存仓库直写（TruncateLadderSteps 用）。
func (m *MemRepo) SaveCardDirect(k [3]int64, c *Card) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.cards[k] = &cp
	return nil
}

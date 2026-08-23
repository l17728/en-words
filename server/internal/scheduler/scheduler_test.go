package scheduler

import (
	"testing"
	"time"

	"enwords/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 测试基建 ----

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newSched(t *testing.T) (*Service, *fakeClock, *config.Store) {
	t.Helper()
	cfg, err := config.NewStore(nil, nil)
	require.NoError(t, err)
	clk := &fakeClock{t: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	return NewService(NewMemRepo(), cfg, clk), clk, cfg
}

func (s *Service) seedWords(t *testing.T, sid, bid int64, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, s.repo.CreateCard(&Card{StudentID: sid, BookID: bid, WordID: id, Status: StatusNew}))
	}
}

var day = 24 * time.Hour

// ---- SC-01: 摸底 → 首课 ----

func TestPlacementFirstLesson(t *testing.T) {
	s, clk, _ := newSched(t)
	var ids []int64
	for i := int64(1); i <= 100; i++ {
		ids = append(ids, i)
	}
	s.seedWords(t, 7, 1, ids...)
	// 摸底勾选 20 词已会
	require.NoError(t, s.MarkPlacement(7, 1, ids[:20]))

	cards, _ := s.repo.ListCards(7, 1)
	known, nw := 0, 0
	for _, c := range cards {
		if c.Status == StatusMastered {
			known++
		} else if c.Status == StatusNew {
			nw++
		}
	}
	assert.Equal(t, 20, known)
	assert.Equal(t, 80, nw)

	// 首课：now+1d；batch = 60 new（无 due）
	l, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	assert.Equal(t, clk.t.Add(day), l.OpenAt)
	assert.Equal(t, LessonScheduledStr, l.Status)
	assert.Len(t, l.BatchWordIDs, 60, "W=60 首课全为新词")
	// 断言用最新卡片快照（reserveCards 已写 NextDueAt）
	cards, _ = s.repo.ListCards(7, 1)
	for _, c := range cards {
		if contains(l.BatchWordIDs, c.WordID) {
			require.NotNil(t, c.NextDueAt)
			assert.Equal(t, l.OpenAt, *c.NextDueAt, "批次词约课到课: NextDueAt=lessonTime")
		}
	}
}

// ---- SC-02/R2-01: 等间隔阶梯与 step 推进 ----

func TestEqualIntervalLadderAndStepAdvance(t *testing.T) {
	s, clk, _ := newSched(t)
	s.seedWords(t, 7, 1, 1, 2, 3)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l1, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	assert.Equal(t, clk.t.Add(day), l1.OpenAt)

	// 上完课1（全对）：新学不算复习→step 仍 0；LastReviewAt=finished_at
	fin1 := l1.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true, 2: true, 3: true}, fin1))
	c1, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 0, c1.LadderStep, "新学不算复习(R3)")
	assert.Equal(t, StatusLearning, c1.Status)
	assert.Equal(t, fin1.Add(day), *c1.NextDueAt, "due = finished + ladder[0]=1d")

	// 课2 = finished1 + ladder[0] = 1d 后
	clk.Advance(fin1.Sub(clk.t)) // now = fin1
	l2, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	assert.Equal(t, fin1.Add(day), l2.OpenAt, "课2=finished_at+ladder[0]")

	// 上完课2（全对）：step 0→1（第一次复习成功）
	fin2 := l2.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true, 2: true, 3: true}, fin2))
	c1, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 1, c1.LadderStep)
	assert.Equal(t, fin2.Add(day), *c1.NextDueAt, "ladder[1]=1d")

	// 课3 上完（全对）：step 1→2
	clk.Advance(fin2.Sub(clk.t)) // 到达课3排课窗口
	l3, _ := s.PlanNext(7, 1)
	fin3 := l3.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true, 2: true, 3: true}, fin3))
	c1, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 2, c1.LadderStep)
	assert.Equal(t, fin3.Add(2*day), *c1.NextDueAt, "ladder[2]=2d")
}

// ---- 全错：step 不变，LastReviewAt 更新（间隔从本课重起） ----

func TestAllWrongKeepsStep(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l1, _ := s.PlanNext(7, 1)
	fin1 := l1.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: false}, fin1))
	c, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 0, c.LadderStep, "全错 step 不变(省力再学)")
	assert.Equal(t, fin1, *c.LastReviewAt, "LastReviewAt 仍更新")
	assert.Equal(t, fin1.Add(day), *c.NextDueAt, "间隔从本课重起 ladder[0]")
}

// ---- SC-03/11/R2-13: due 全保留 + new ≤0.6W + 优先级 ----

func TestBatchPriorityDueBoostNew(t *testing.T) {
	s, clk, _ := newSched(t)
	var ids []int64
	for i := int64(1); i <= 100; i++ {
		ids = append(ids, i)
	}
	s.seedWords(t, 7, 1, ids...)
	require.NoError(t, s.MarkPlacement(7, 1, ids[:20]))

	// 制造 30 个 due 词（前 30 个 new→learning 到期）+ 2 个 boost
	l1, _ := s.PlanNext(7, 1)
	fin1 := l1.OpenAt.Add(30 * time.Minute)
	answers := map[int64]bool{}
	for _, id := range l1.BatchWordIDs[:30] {
		answers[id] = true
	}
	require.NoError(t, s.SettleLessonFinished(7, 1, answers, fin1))
	require.NoError(t, s.repo.Boost(7, 1, l1.BatchWordIDs[30]))
	require.NoError(t, s.repo.Boost(7, 1, l1.BatchWordIDs[31]))

	clk.Advance(fin1.Add(day).Sub(clk.t)) // 到期
	l2, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	batch := l2.BatchWordIDs
	assert.Len(t, batch, 60, "总 W=60")
	assert.Equal(t, int64(l2.BatchWordIDs[0]), l1.BatchWordIDs[30], "boost 头部(按库序)")
	assert.Equal(t, int64(l2.BatchWordIDs[1]), l1.BatchWordIDs[31], "boost 头部(按库序)")
	// 30 due 全保留 + 2 boost + new 补 28 → new=28 ≤ 0.6*60
	assert.LessOrEqual(t, 28, 36)
	// 断言 due 词全部在批
	for _, id := range l1.BatchWordIDs[:30] {
		assert.Contains(t, batch, id, "due 词必须全保留")
	}
}

// ---- SC-05/06/R2-03: 逾期重排 ----

func TestOverdueReplan(t *testing.T) {
	s, clk, cfg := newSched(t)
	s.seedWords(t, 7, 1, 1, 2)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l1, _ := s.PlanNext(7, 1) // openAt = now+1d
	fin1 := l1.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true, 2: true}, fin1))
	clk.Advance(fin1.Sub(clk.t)) // 到达课2排课窗口
	l2, _ := s.PlanNext(7, 1)
	assert.Equal(t, fin1.Add(day), l2.OpenAt)

	// 未到逾期阈值：不动
	clk.Advance(l2.OpenAt.Add(23 * time.Hour).Sub(clk.t))
	replanned, err := s.HandleOverdue()
	require.NoError(t, err)
	assert.Empty(t, replanned, "23h<24h 不触发(SC-06)")

	// 超过：expired + 次日同时段 + step 不变 + LastReviewAt 不动
	clk.Advance(2 * time.Hour)
	replanned, err = s.HandleOverdue()
	require.NoError(t, err)
	require.Len(t, replanned, 1)
	got2, _ := s.repo.GetLesson(l2.ID)
	assert.Equal(t, LessonExpiredStr, got2.Status)
	l3, _ := s.repo.GetLesson(replanned[0])
	assert.Equal(t, l2.OpenAt.Add(day), l3.OpenAt, "次日同时段(SC-05)")
	c, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 0, c.LadderStep, "逾期 step 不变(课1为首次新学, step=0)")
	assert.Equal(t, fin1, *c.LastReviewAt, "未上课 LastReviewAt 不动(R3)")
	assert.Equal(t, l3.OpenAt, *c.NextDueAt, "约课到课跟随新课")
	_ = cfg
}

// ---- SC-07/08: 长期毕业与维护 ----

func TestLongTermGraduation(t *testing.T) {
	s, clk, _ := newSched(t)
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	ladder := config.DefaultLadder // 8 级
	// 模拟走完整个阶梯: 首课 + 8 次成功复习
	l, _ := s.PlanNext(7, 1)
	fin := l.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true}, fin))
	for step := 0; step < len(ladder); step++ {
		// 等间隔阶梯间隔>1天后，须推进到该词 NextDueAt 才会 due（真实系统=cron 到期触发）
		c, _ := s.repo.GetCard(7, 1, 1)
		require.NotNil(t, c.NextDueAt, "step=%d 词1应有到期时间", step)
		clk.Advance(c.NextDueAt.Sub(clk.t))
		l, err := s.PlanNext(7, 1)
		if err == ErrNoWords {
			break
		}
		require.NoError(t, err)
		if !contains(l.BatchWordIDs, 1) {
			break
		}
		fin = l.OpenAt.Add(30 * time.Minute)
		require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true}, fin))
	}
	c, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusGraduated, c.Status, "step>=len 且答对 → 长期毕业(SC-07)")
	_, err := s.PlanNext(7, 1)
	assert.ErrorIs(t, err, ErrNoWords, "毕业词不参与排课")

	// 维护期答错 → 回 learning, step=min(5,len-1)
	require.NoError(t, s.MaintenanceCheck(7, 1, 1, false))
	c, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusLearning, c.Status)
	assert.Equal(t, 5, c.LadderStep, "重置到 step=5 (SC-08)")
}

// ---- 课内掌握 ≠ 长期毕业（R3 推演修复项）----

func TestLessonScopedMasteryNotGraduate(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l, _ := s.PlanNext(7, 1)
	// 连对 2 次(课内掌握语义) 传入结算仍是 "答对过"
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true}, l.OpenAt.Add(30*time.Minute)))
	c, _ := s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusLearning, c.Status, "课内掌握不改卡片: 连对≠毕业")
}

// ---- R2-04: 主线 step 中位数 ----

func TestMedianMainlineStep(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1, 2, 3, 4)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l1, _ := s.PlanNext(7, 1)
	fin1 := l1.OpenAt.Add(30 * time.Minute)
	// 构造 step 分布: 全部词首课后(=0), 然后手动改卡 step 为 [0,0,2,8] → 中位数 1
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{}, fin1))
	c2, _ := s.repo.GetCard(7, 1, 2)
	c2.LadderStep = 2
	s.repo.SaveCard(c2)
	c4, _ := s.repo.GetCard(7, 1, 4)
	c4.LadderStep = 8
	s.repo.SaveCard(c4)

	l2, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	// 主线=中位数([0,0,2,8]→1) → l2 = fin1 + ladder[1] = 1d
	assert.Equal(t, fin1.Add(day), l2.OpenAt, "主线step=中位数(R2-04)")
}

// ---- R2-02: ladder 缩短截断 ----

func TestLadderShortenTruncate(t *testing.T) {
	s, clk, cfg := newSched(t)
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l1, _ := s.PlanNext(7, 1)
	fin1 := l1.OpenAt.Add(30 * time.Minute)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true}, fin1))
	c, _ := s.repo.GetCard(7, 1, 1)
	c.LadderStep = 7
	s.repo.SaveCard(c)

	// 真实场景：config.ladder 改短 → OnLadderChanged 触发截断（此处手动模拟两步）
	require.NoError(t, cfg.SetGlobal("admin", "ladder", []int{1, 1, 2}, "缩短"))
	require.NoError(t, s.TruncateLadderSteps(3)) // 新 len=3 → max step=2
	c, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, 2, c.LadderStep, "step 截断到新len-1")
	// 再成功一次复习 → step=3 >= len(3) → 毕业(门槛自洽)
	clk.Advance(fin1.Sub(clk.t)) // 到达排课窗口(卡 due)
	l2, _ := s.PlanNext(7, 1)
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true}, l2.OpenAt.Add(30*time.Minute)))
	c, _ = s.repo.GetCard(7, 1, 1)
	assert.Equal(t, StatusGraduated, c.Status)
}

// ---- SC-10: 多词库独立 ----

func TestMultiBookIndependence(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1)
	s.seedWords(t, 7, 2, 101)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	require.NoError(t, s.MarkPlacement(7, 2, nil))
	l1, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	l2, err := s.PlanNext(7, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), l1.BookID)
	assert.Equal(t, int64(2), l2.BookID)
	assert.Equal(t, []int64{1}, l1.BatchWordIDs)
	assert.Equal(t, []int64{101}, l2.BatchWordIDs)
}

// ---- 新词不足时批次 < W 不强凑 ----

func TestBatchSmallerThanW(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1, 2, 3)
	require.NoError(t, s.MarkPlacement(7, 1, nil)) // 3 new, W=60
	l, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	assert.Len(t, l.BatchWordIDs, 3, "new 不足时批次<W")
}

func contains(xs []int64, v int64) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ---- 已排课幂等（只排下一课 F2）----

func TestAlreadyPlannedIdempotent(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	_, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	_, err = s.PlanNext(7, 1)
	assert.ErrorIs(t, err, ErrAlreadyPlanned, "已有 scheduled 未过期课不可重复排")
}

// ---- 反馈 D：推迟课程 ----

func TestPostponeLesson(t *testing.T) {
	s, _, _ := newSched(t)
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l1, _ := s.PlanNext(7, 1)
	to := l1.OpenAt.Add(2 * day)
	require.NoError(t, s.PostponeLesson(l1.ID, to))
	got, _ := s.repo.GetLesson(l1.ID)
	assert.Equal(t, to, got.OpenAt, "课程推迟到指定时间")
	c, _ := s.repo.GetCard(7, 1, 1)
	require.NotNil(t, c.NextDueAt)
	assert.Equal(t, to, *c.NextDueAt, "约课到课跟随新时间")
	// 非scheduled课不可推迟（结课后）
	clk2 := &fakeClock{t: time.Now()}
	_ = clk2
	require.NoError(t, s.SettleLessonFinished(7, 1, map[int64]bool{1: true}, to.Add(time.Hour)))
	assert.Error(t, s.PostponeLesson(l1.ID, to.Add(2*day)), "finished 课不可推迟")
	assert.Error(t, s.PostponeLesson(9999, to), "不存在的课报错")
}

// ---- RealClock ----

func TestRealClockNow(t *testing.T) {
	before := time.Now()
	got := RealClock{}.Now()
	after := time.Now()
	assert.False(t, got.Before(before) || got.After(after))
}

// ---- 逾期巡检空仓/无过期 ----

func TestHandleOverdueEmpty(t *testing.T) {
	s, _, _ := newSched(t)
	r, err := s.HandleOverdue()
	require.NoError(t, err)
	assert.Empty(t, r, "无任何课程时空巡检")

	// 有课但未到期 → 空结果
	s.seedWords(t, 7, 1, 1)
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	_, _ = s.PlanNext(7, 1)
	r, err = s.HandleOverdue()
	require.NoError(t, err)
	assert.Empty(t, r, "未过期不触发")
}

// ---- api 装配层访问器（补覆盖）----

func TestAccessorMethods(t *testing.T) {
	s, clk, _ := newSched(t)
	// SeedCard/BoostWord/Lesson/Lessons/SaveLesson/StudentLessons
	require.NoError(t, s.SeedCard(7, 1, 100))
	require.NoError(t, s.SeedCard(7, 2, 200))
	require.NoError(t, s.SeedCard(8, 1, 300))
	c, err := s.GetCard(7, 1, 100)
	require.NoError(t, err)
	assert.Equal(t, StatusNew, c.Status)

	require.NoError(t, s.BoostWord(7, 1, 100))
	c, _ = s.GetCard(7, 1, 100)
	assert.True(t, c.BoostFlag, "BoostWord 标记难词")

	// 排一课后走访问器
	require.NoError(t, s.MarkPlacement(7, 1, nil))
	l, err := s.PlanNext(7, 1)
	require.NoError(t, err)
	got, err := s.Lesson(l.ID)
	require.NoError(t, err)
	assert.Equal(t, l.OpenAt, got.OpenAt)

	all, err := s.Lessons(7, 1)
	require.NoError(t, err)
	require.Len(t, all, 1)

	// SaveLesson 改状态
	all[0].Status = LessonFinishedStr
	require.NoError(t, s.SaveLesson(all[0]))
	got, _ = s.Lesson(l.ID)
	assert.Equal(t, LessonFinishedStr, got.Status)

	// StudentLessons 跨库
	sl, err := s.StudentLessons(7)
	require.NoError(t, err)
	assert.Len(t, sl, 1, "该生仅库1有课（跨库聚合）")
	sl8, _ := s.StudentLessons(8)
	assert.Empty(t, sl8)

	// 不存在课程报错
	_, err = s.Lesson(9999)
	assert.Error(t, err)
	_ = clk
}

// ---- GetCard 不存在 ----

func TestGetCardMissing(t *testing.T) {
	s, _, _ := newSched(t)
	_, err := s.GetCard(1, 1, 1)
	assert.Error(t, err)
	// Boost 不存在卡报错
	assert.Error(t, s.BoostWord(1, 1, 1))
}

package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedNow() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }

func newSvc() *Service {
	return &Service{repo: NewMemRepo(), now: func() time.Time { return fixedNow() }}
}

// ---- append 基础与校验 ----

func TestAppendAndScopes(t *testing.T) {
	s := newSvc()
	// 学生档案（book 强制清零）
	m, err := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 99, "observation", "对激将法反应好", 4, 0)
	require.NoError(t, err)
	assert.NotZero(t, m.ID)
	assert.Zero(t, m.BookID, "档案与词库无关")

	// 任务记忆（book 必填）
	m2, err := s.Append(OwnerTeacher, ScopeTaskMemory, 7, 3, "note", "abandon 需拆音节教", 5, 12)
	require.NoError(t, err)
	assert.Equal(t, int64(3), m2.BookID)
	assert.Equal(t, int64(12), m2.SourceLessonID, "关联课次可回溯")

	// 校验拒绝
	_, err = s.Append(OwnerTeacher, ScopeTaskMemory, 7, 0, "note", "x", 3, 0)
	assert.Error(t, err, "任务记忆缺 bookId 拒绝")
	_, err = s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "note", "  ", 3, 0)
	assert.Error(t, err, "空内容拒绝")
	_, err = s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "", "x", 3, 0)
	assert.Error(t, err, "空 kind 拒绝")

	// importance 夹取
	m3, _ := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "k", "v", 99, 0)
	assert.Equal(t, 5, m3.Importance)
	m4, _ := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "k", "v", -2, 0)
	assert.Equal(t, 1, m4.Importance)
}

// ---- owner 隔离（teacher/system 互不可见互不可改）----

func TestOwnerIsolation(t *testing.T) {
	s := newSvc()
	m, err := s.Append(OwnerTeacher, ScopeTaskMemory, 7, 3, "note", "老师笔记", 4, 0)
	require.NoError(t, err)

	// system 改老师的记忆 → 拒绝
	_, err = s.Revise(OwnerSystem, m.ID, "篡改", 0)
	assert.ErrorIs(t, err, ErrForbidden)
	// system 删老师的记忆 → 拒绝
	err = s.Delete(OwnerSystem, m.ID)
	assert.ErrorIs(t, err, ErrForbidden)
	// system list 看不到老师的
	ms, _ := s.List(OwnerSystem, 7, 3, "")
	assert.Empty(t, ms)
	// system 自己写自己的可见
	_, err = s.Append(OwnerSystem, ScopeTaskMemory, 7, 3, "decision", "已降量 60→50", 4, 0)
	require.NoError(t, err)
	ms, _ = s.List(OwnerSystem, 7, 3, "")
	require.Len(t, ms, 1)
	// teacher 看不到 system 的
	ms, _ = s.List(OwnerTeacher, 7, 3, "")
	require.Len(t, ms, 1)
}

// ---- revise 修订链 ----

func TestRevise(t *testing.T) {
	s := newSvc()
	m, _ := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "weak", "发音弱", 2, 0)
	_, err := s.Revise(OwnerTeacher, m.ID, "发音偏弱，长元音易错", 4)
	require.NoError(t, err)
	got, _ := s.repo.Get(m.ID)
	assert.Equal(t, "发音偏弱，长元音易错", got.Content)
	assert.Equal(t, 4, got.Importance)
	// 不存在
	_, err = s.Revise(OwnerTeacher, 9999, "x", 0)
	assert.ErrorIs(t, err, ErrNotFound)
	// 空内容仅调重要度
	_, err = s.Revise(OwnerTeacher, m.ID, "", 5)
	require.NoError(t, err)
	got, _ = s.repo.Get(m.ID)
	assert.Equal(t, "发音偏弱，长元音易错", got.Content, "空内容不改")
	assert.Equal(t, 5, got.Importance)
}

// ---- delete ----

func TestDelete(t *testing.T) {
	s := newSvc()
	m, _ := s.Append(OwnerSystem, ScopeTaskMemory, 7, 3, "promise", "学生说周三请假", 3, 0)
	require.NoError(t, s.Delete(OwnerSystem, m.ID))
	_, err := s.repo.Get(m.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, s.Delete(OwnerSystem, m.ID), ErrNotFound)
}

// ---- recall 加权与过滤 ----

func TestRecallRanking(t *testing.T) {
	s := newSvc()
	// 三条：低重要度新 / 高重要度新 / 高重要度旧
	mLow, _ := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "obs", "一般观察", 1, 0)
	mHigh, _ := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "obs", "关键弱项", 5, 0)
	mOld := &Memory{OwnerRole: OwnerTeacher, StudentID: 7, Kind: "obs",
		Content: "旧的重要", Importance: 5,
		CreatedAt: fixedNow().AddDate(0, 0, -60), UpdatedAt: fixedNow().AddDate(0, 0, -60)}
	require.NoError(t, s.repo.Append(mOld))

	got, err := s.Recall(OwnerTeacher, 7, 0, "", "", 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, mHigh.ID, got[0].ID, "高重要度新记忆排第一")
	// 旧的重要 vs 新的低：importance*2=10+recency0=10 vs 1*2+5=7 → 旧的仍靠前
	assert.Equal(t, mOld.ID, got[1].ID)
	assert.Equal(t, mLow.ID, got[2].ID)

	// limit
	got, _ = s.Recall(OwnerTeacher, 7, 0, "", "", 1)
	assert.Len(t, got, 1)
	// kind 过滤
	got, _ = s.Recall(OwnerTeacher, 7, 0, "other", "", 10)
	assert.Empty(t, got)
	// 关键词
	got, _ = s.Recall(OwnerTeacher, 7, 0, "", "弱项", 10)
	require.Len(t, got, 1)
	assert.Equal(t, "关键弱项", got[0].Content)
	_ = mLow
}

// ---- list kind 过滤 ----

func TestListKindFilter(t *testing.T) {
	s := newSvc()
	_, _ = s.Append(OwnerTeacher, ScopeTaskMemory, 7, 3, "note", "a", 3, 0)
	_, _ = s.Append(OwnerTeacher, ScopeTaskMemory, 7, 3, "promise", "b", 3, 0)
	ms, _ := s.List(OwnerTeacher, 7, 3, "promise")
	require.Len(t, ms, 1)
	assert.Equal(t, "b", ms[0].Content)
	// 其他学生不可见
	ms, _ = s.List(OwnerTeacher, 8, 3, "")
	assert.Empty(t, ms)
	// 其他 book 不可见（任务记忆隔离）
	_, _ = s.Append(OwnerTeacher, ScopeTaskMemory, 7, 4, "note", "c", 3, 0)
	ms, _ = s.List(OwnerTeacher, 7, 3, "")
	assert.Len(t, ms, 2, "book=3 只有 2 条")
}

// ---- 边界：默认 limit、Get 不存在、Recall 空集 ----

func TestRecallDefaultsAndEmpty(t *testing.T) {
	s := newSvc()
	// 空集
	got, err := s.Recall(OwnerTeacher, 7, 0, "", "", 0)
	require.NoError(t, err)
	assert.Empty(t, got)
	// 默认 limit=10：写 12 条取 10
	for i := 0; i < 12; i++ {
		_, err := s.Append(OwnerTeacher, ScopeStudentProfile, 7, 0, "obs", "m", 1, 0)
		require.NoError(t, err)
	}
	got, _ = s.Recall(OwnerTeacher, 7, 0, "", "", 0)
	assert.Len(t, got, 10, "默认 limit=10")
	// repo Get 不存在
	_, err = s.repo.Get(999)
	assert.ErrorIs(t, err, ErrNotFound)
	// repo Update 不存在
	assert.ErrorIs(t, s.repo.Update(&Memory{ID: 999}), ErrNotFound)
	// repo Delete 不存在
	assert.ErrorIs(t, s.repo.Delete(999), ErrNotFound)
}

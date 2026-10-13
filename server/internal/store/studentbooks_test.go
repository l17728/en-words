package store

// BUG 锁定（2026-08-25 Dashboard"我的词库"点击不生效）：
// enablement=记忆卡存在——StudentBooks 必须去重、升序、空学生返回空切片（非 null）。
import (
	"testing"

	"enwords/internal/scheduler"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchedulerRepoStudentBooks(t *testing.T) {
	db, _ := openTest(t)
	t.Cleanup(func() { d, _ := db.DB(); _ = d.Close() })
	r := NewSchedulerRepo(db)

	seed := func(s, b int64, ws ...int64) {
		t.Helper()
		for _, w := range ws {
			require.NoError(t, r.CreateCard(&scheduler.Card{
				StudentID: s, BookID: b, WordID: w, Status: scheduler.StatusNew}))
		}
	}
	seed(1, 30, 1, 2) // 学生1：库30 两词
	seed(1, 10, 3)    // 学生1：库10 一词（乱序验证升序）
	seed(2, 30, 4)    // 学生2：库30

	ids, err := r.StudentBooks(1)
	require.NoError(t, err)
	assert.Equal(t, []int64{10, 30}, ids, "去重+升序")

	ids2, err := r.StudentBooks(2)
	require.NoError(t, err)
	assert.Equal(t, []int64{30}, ids2)

	ids3, err := r.StudentBooks(99)
	require.NoError(t, err)
	assert.Empty(t, ids3, "无卡学生返回空切片（非 null）")
}

package store

// R6 双课 BUG 锁定：CreateLesson 必须回写 ID——否则 openNowIfTestMode 的
// SaveLesson(ID=0) 被 GORM Save 当 INSERT 插入第二行（同一 finish 双课）。
import (
	"testing"

	"enwords/internal/scheduler"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateLessonWritebackID(t *testing.T) {
	db1, _ := openTest(t)
	t.Cleanup(func() { d, _ := db1.DB(); _ = d.Close() })
	repo := NewSchedulerRepo(db1)

	// 创建（ID 零值进）→ 必须回写
	l1 := &scheduler.Lesson{StudentID: 7, BookID: 1, Status: "scheduled", BatchWordIDs: []int64{1, 2, 3}}
	require.NoError(t, repo.CreateLesson(l1))
	assert.NotZero(t, l1.ID, "CreateLesson 必须回写 ID（双课 BUG 锁定）")

	// Save 用同一对象（模拟 openNowIfTestMode）→ 更新而非插入
	l1.Status = "finished"
	require.NoError(t, repo.SaveLesson(l1))

	var n int64
	db1.Model(&Lesson{}).Where("student_id=? AND book_id=?", 7, 1).Count(&n)
	assert.EqualValues(t, 1, n, "Save 不得插入新行（仍只有 1 课）")

	got, err := repo.GetLesson(l1.ID)
	require.NoError(t, err)
	assert.Equal(t, "finished", got.Status, "状态已更新")
}

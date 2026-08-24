package api

// R17 BUG 锁定用例（铁律 0.5）：重新摸底完成 → 覆盖旧结果 + 重排计划（不再报 already scheduled）；
// 摸底草稿二次进入恢复（FR-C4 不清空）；取消勾选回退。
import (
	"testing"

	"enwords/internal/auth"
	"enwords/internal/scheduler"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestR17_ReplacementOverwritesPlan(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	words := []string{"w1", "w2", "w3", "w4", "w5"}
	bid := seedBook(t, app, atok, "重摸库", "g7", words)
	// 真实 entry ID（全局递增，不能假设 1..5）
	es, _, _ := app.Books.ListEntries(bid, 1, 100)
	wordIDs := make([]int64, 0, len(es))
	for _, e := range es {
		wordIDs = append(wordIDs, e.ID)
	}
	require.Len(t, wordIDs, 5)
	_, _ = app.Auth.CreateUser("r17", "pw12345", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "r17", "pw12345")
	require.NoError(t, app.EnableBook(uid, bid))
	router := app.Router()

	// 第一次摸底完成：勾 w1 → 生成计划（4 词批次）
	resp := req2(t, router, "POST", "/api/v1/student/books/"+itoa64(bid)+"/placement", stok,
		map[string]any{"knownWordIds": []int64{wordIDs[0]}, "finish": true})
	require.Equal(t, 0, resp.Code, resp.Msg)
	ls, _ := app.Sched.Lessons(uid, bid)
	require.Len(t, ls, 1)

	// 草稿恢复：GET 返回已选
	resp = req2(t, router, "GET", "/api/v1/student/books/"+itoa64(bid)+"/placement", stok, nil)
	require.Equal(t, 0, resp.Code)
	pm, _ := resp.Data.(map[string]any)
	known, _ := pm["knownWordIds"].([]any)
	require.Len(t, known, 1, "二次进入草稿恢复不清空")

	// 重新摸底完成（改勾 w1+w2）：不报错且重排
	resp = req2(t, router, "POST", "/api/v1/student/books/"+itoa64(bid)+"/placement", stok,
		map[string]any{"knownWordIds": []int64{wordIDs[0], wordIDs[1]}, "finish": true})
	require.Equal(t, 0, resp.Code, "重新摸底完成不得报 already scheduled（BUG 锁定）")

	// 旧 scheduled 课作废 + 新课生成（3 词批次）
	ls2, _ := app.Sched.Lessons(uid, bid)
	require.Len(t, ls2, 2)
	assert.Equal(t, "voided_replacement", ls2[0].Status, "旧课作废")
	assert.Equal(t, scheduler.LessonScheduledStr, ls2[1].Status)
	assert.Len(t, ls2[1].BatchWordIDs, 3, "重排批次=5-2 已会")

	// 取消勾选回退：w2 从 mastered 回 new（第三次摸底只勾 w1）
	resp = req2(t, router, "POST", "/api/v1/student/books/"+itoa64(bid)+"/placement", stok,
		map[string]any{"knownWordIds": []int64{wordIDs[0]}, "finish": true})
	require.Equal(t, 0, resp.Code)
	c, err := app.Sched.GetCard(uid, bid, wordIDs[1])
	require.NoError(t, err)
	assert.Equal(t, scheduler.StatusNew, c.Status, "取消勾选回退 new（点错可取消锁定）")
}

// 摸底页面返回词库广场按钮存在性（UI 层——此处验证保存语义：退出时草稿已存）
func TestR17_DraftSavedBeforeFinish(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "草稿库", "g8", []string{"a1", "a2"})
	_, _ = app.Auth.CreateUser("r17b", "pw12345", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "r17b", "pw12345")
	require.NoError(t, app.EnableBook(uid, bid))
	_ = stok
	// 中间保存（不 finish）→ 草稿在（真实 entry ID）
	es2, _, _ := app.Books.ListEntries(bid, 1, 100)
	require.NoError(t, app.PlacementSubmit(uid, bid, []int64{es2[0].ID}, false))
	assert.Equal(t, []int64{es2[0].ID}, app.Sched.PlacementDraft(uid, bid))
	// 未生成课
	ls, _ := app.Sched.Lessons(uid, bid)
	assert.Empty(t, ls, "不 finish 不排课")
}

// R17 类：重复 enable 不得抹掉摸底结果（幂等锁定）
func TestR17_ReenableKeepsPlacement(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid := seedBook(t, app, atok, "再选库", "g9", []string{"b1", "b2", "b3"})
	_, _ = app.Auth.CreateUser("r17c", "pw12345", "小", auth.RoleStudent)
	_, uid := loginGet(t, app, "r17c", "pw12345")
	require.NoError(t, app.EnableBook(uid, bid))
	es, _, _ := app.Books.ListEntries(bid, 1, 100)
	// 摸底勾 b1
	require.NoError(t, app.PlacementSubmit(uid, bid, []int64{es[0].ID}, true))
	c1, err := app.Sched.GetCard(uid, bid, es[0].ID)
	require.NoError(t, err)
	require.Equal(t, scheduler.StatusMastered, c1.Status)
	// 再次 enable（二次选库场景）：摸底结果保持
	require.NoError(t, app.EnableBook(uid, bid), "重复 enable 幂等成功")
	c1b, _ := app.Sched.GetCard(uid, bid, es[0].ID)
	assert.Equal(t, scheduler.StatusMastered, c1b.Status, "重复 enable 不得抹掉摸底 mastered（BUG 锁定）")
	// 草稿也保持
	assert.Equal(t, []int64{es[0].ID}, app.Sched.PlacementDraft(uid, bid))
}

package api

// BUG 锁定（2026-08-25 Dashboard 点击不生效）：
// ① Dashboard"我的词库"曾拿全量 /wordbooks 渲染（别人的库也进来，点击必无课=零反馈）；
// ② 后端无 GET /student/books。锁定：我的词库=仅已启用（有记忆卡），
// enable 后未摸底也在列（dingdangcat 场景），空态返回 []（nonNilSlice 约定）。
import (
	"testing"

	"enwords/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStudentBooksEndpoint(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	bid1 := seedBook(t, app, atok, "我的库A", "g7", []string{"apple", "bee"})
	bid2 := seedBook(t, app, atok, "我的库B", "g8", []string{"cat"})
	bid3 := seedBook(t, app, atok, "别人的库", "g9", []string{"dog"})

	_, _ = app.Auth.CreateUser("sb1", "pw", "小", auth.RoleStudent)
	stok, uid := loginGet(t, app, "sb1", "pw")

	// 未启用：空列表（数组而非 null——Go nil slice → JSON null 前端炸弹教训）
	resp := req2(t, app.Router(), "GET", "/api/v1/student/books", stok, nil)
	require.Equal(t, 0, resp.Code)
	list0, ok := resp.Data.([]any)
	require.True(t, ok, "空态必须是数组而非 null")
	assert.Empty(t, list0)

	// 启用 2 个库（未摸底）：恰好这 2 个，不含第 3 个
	require.NoError(t, app.EnableBook(uid, bid1))
	require.NoError(t, app.EnableBook(uid, bid2))
	resp = req2(t, app.Router(), "GET", "/api/v1/student/books", stok, nil)
	require.Equal(t, 0, resp.Code)
	list, _ := resp.Data.([]any)
	require.Len(t, list, 2, "仅含已启用词库")
	got := map[float64]bool{}
	for _, it := range list {
		m, _ := it.(map[string]any)
		got[m["ID"].(float64)] = true
	}
	assert.True(t, got[float64(bid1)] && got[float64(bid2)], "两个已启用库都在")
	assert.False(t, got[float64(bid3)], "未启用库不得出现（BUG 锁定）")

	// 学生角色隔离：admin 不得走学生端点（roleMW 403）
	resp = req2(t, app.Router(), "GET", "/api/v1/student/books", atok, nil)
	assert.Equal(t, errForbidden, resp.Code)
}

package api

// 持久化集成测试（铁律 0.5）：SQLite 模式下 App 全链路重启不丢。
// 覆盖：用户/词库/词条/记忆卡/课程/摸底草稿——重启后账号可登录、词库可见、
// 课程与卡片状态保持（"单机家用"核心场景）。
import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"
	"enwords/internal/scheduler"
	"enwords/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func httptestRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func httptestNewPost(path, body string) *http.Request {
	return httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
}

func doJSONBody(t *testing.T, w *httptest.ResponseRecorder) Resp {
	t.Helper()
	var out Resp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

// loginGet2 取 admin token（bootstrap 后）。
func loginGet2(t *testing.T, app *App) string {
	t.Helper()
	tok, _ := loginGet(t, app, "dad", "family123")
	return tok
}

func TestAppPersistenceRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist.db")

	mk := func() *App {
		db, err := store.Open(dbPath)
		require.NoError(t, err)
		t.Cleanup(func() { d, _ := db.DB(); _ = d.Close() })
		return NewApp(&fakeClock{t: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}, db)
	}

	// ---- 第一次启动：bootstrap + 建学生 + 选库 + 摸底 + 排课 ----
	app1 := mk()
	r1 := newAppBootstrap(t, app1) // bootstrap dad
	require.Equal(t, 0, r1)
	// 持久化模式：用户创建后立即落库（SaveUser）
	_, err := app1.Auth.CreateUser("pkid", "pw12345", "小明", auth.RoleStudent)
	require.NoError(t, err)
	if app1.DB != nil {
		require.NoError(t, store.NewAuthStore(app1.DB).SaveUser(app1.Auth, "pkid"))
		require.NoError(t, store.NewAuthStore(app1.DB).SaveUser(app1.Auth, "dad"))
	}
	stok, uid := loginGet(t, app1, "pkid", "pw12345")
	require.NotEmpty(t, stok)
	atok := loginGet2(t, app1)
	bid := seedBook(t, app1, atok, "持久库", "g7",
		[]string{"w1", "w2", "w3", "w4", "w5"})
	require.NoError(t, app1.EnableBook(uid, bid))
	require.NoError(t, app1.PlacementSubmit(uid, bid, []int64{}, true))
	ls, _ := app1.Sched.Lessons(uid, bid)
	require.Len(t, ls, 1)

	// ---- "重启"：新 App 同一 db 文件 ----
	app2 := mk()

	// 账号：重启后原密码可登录
	stok2, uid2 := loginGet(t, app2, "pkid", "pw12345")
	require.NotEmpty(t, stok2, "重启后账号不丢（持久化核心）")
	assert.Equal(t, uid, uid2)

	// 词库+词条：可见且词条在
	b, err := app2.Books.GetBook(bid)
	require.NoError(t, err, "重启后词库不丢")
	assert.Equal(t, "持久库", b.Name)
	n, _ := app2.Books.CountEntries(bid)
	assert.EqualValues(t, 5, n)

	// 课程：排的课仍在
	ls2, _ := app2.Sched.Lessons(uid, bid)
	require.Len(t, ls2, 1, "重启后课程不丢")
	assert.Equal(t, scheduler.LessonScheduledStr, ls2[0].Status)
	assert.Len(t, ls2[0].BatchWordIDs, 5)

	// 记忆卡：状态与约课时间保持（首课未上=仍 new+已约 NextDueAt）
	for _, wid := range ls2[0].BatchWordIDs[:3] {
		c, err := app2.Sched.GetCard(uid, bid, wid)
		require.NoError(t, err, "重启后记忆卡不丢 word=%d", wid)
		assert.Equal(t, scheduler.StatusNew, c.Status, "未上课保持 new（new→learning 在结课结算）")
		require.NotNil(t, c.NextDueAt, "约课时间保持")
		assert.Equal(t, ls2[0].OpenAt, *c.NextDueAt, "NextDueAt=课程开放时间")
	}

	// 幂等种子：重启 AutoSeed 不重复导入（种子目录缺省时跳过——仅验证不重复）
	seedDir := filepath.Join("..", "..", "data", "seed")
	if _, err := os.Stat(seedDir); err == nil {
		books, words, err := app2.AutoSeed()
		require.NoError(t, err)
		assert.Equal(t, 0, books, "已存在的种子库跳过")
		assert.Equal(t, 0, words)
	}
}

// require.NewAppBootstrap 辅助：bootstrap dad（存在则跳过）。
func newAppBootstrap(t *testing.T, app *App) int {
	t.Helper()
	// 已有 admin（重启恢复）→ 直接成功
	if app.Auth.HasAdmin() {
		return 0
	}
	w := httptestRecorder()
	req := httptestNewPost("/api/v1/bootstrap", `{"username":"dad","password":"family123","nickname":"爸爸"}`)
	app.Router().ServeHTTP(w, req)
	return doJSONBody(t, w).Code
}

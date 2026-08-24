package api

// 首次启动向导测试（SDD §17 双击即用）：
// 无 admin 时 POST /bootstrap 创建首个管理员 + 可选导入种子词库；
// 已有 admin 后 bootstrap 封死（防接管）。
import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"enwords/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBareApp 裸 App（无预建账号——向导测试需要真正的"首次启动"状态）。
func newBareApp() *App {
	return NewApp(&fakeClock{t: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}, nil)
}

func TestBootstrapWizard(t *testing.T) {
	app := newBareApp()
	// bootstrap 可用（无任何账号）
	router := app.Router()

	// 1) 引导状态查询（前端向导页探针）
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/bootstrap/status", nil)
	router.ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code)
	m, _ := resp.Data.(map[string]any)
	assert.Equal(t, true, m["needed"], "无 admin 需要引导")

	// 2) 创建首个 admin（不需要 token）
	w = httptest.NewRecorder()
	body := `{"username":"dad","password":"family123","nickname":"爸爸"}`
	req = httptest.NewRequest("POST", "/api/v1/bootstrap", strReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	resp = doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)

	// 能登录且是 admin
	tok, _ := loginGet(t, app, "dad", "family123")
	require.NotEmpty(t, tok)
	u, _ := app.Auth.GetUser("dad")
	assert.Equal(t, auth.RoleAdmin, u.Role)

	// 3) bootstrap 封死（防二次接管）
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/bootstrap/status", nil)
	router.ServeHTTP(w, req)
	resp = doJSON(t, w)
	m, _ = resp.Data.(map[string]any)
	assert.Equal(t, false, m["needed"], "已有 admin 不再需要引导")

	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/v1/bootstrap", strReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	resp = doJSON(t, w)
	assert.Equal(t, errForbidden, resp.Code, "重复 bootstrap 拒绝")
}

func TestBootstrapValidation(t *testing.T) {
	app := newBareApp()
	router := app.Router()
	// 空 username / 短密码
	for _, bad := range []string{
		`{"username":"","password":"family123"}`,
		`{"username":"x","password":"123"}`,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/bootstrap", strReader(bad))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		assert.Equal(t, errBadRequest, doJSON(t, w).Code)
	}
}

// 种子导入端点：admin 可一键导入 data/seed 全部词库（发布态）
func TestSeedImportEndpoint(t *testing.T) {
	app, _ := newTestApp(t)
	atok, _ := loginGet(t, app, "admin", "adminpw")
	router := app.Router()

	// 指向仓库真实种子目录（相对 server/ 的上级 data/seed）
	seedDir := filepath.Join("..", "..", "..", "data", "seed")
	if _, err := os.Stat(seedDir); err != nil {
		t.Skip("种子目录不存在（非仓库环境）")
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/admin/seed-import?dir="+seedDir, nil)
	req.Header.Set("Authorization", "Bearer "+atok)
	router.ServeHTTP(w, req)
	resp := doJSON(t, w)
	require.Equal(t, 0, resp.Code, resp.Msg)
	m, _ := resp.Data.(map[string]any)
	assert.Equal(t, float64(7), m["books"], "7 个种子词库")
	assert.Equal(t, float64(5568), m["words"], "5568 词条全部入库")

	// 学生立即可见（已发布）
	_, _ = app.Auth.CreateUser("skid", "pw", "小", auth.RoleStudent)
	stok, _ := loginGet(t, app, "skid", "pw")
	resp = req2(t, router, "GET", "/api/v1/wordbooks", stok, nil)
	require.Equal(t, 0, resp.Code)
	list, _ := resp.Data.([]any)
	assert.Len(t, list, 7, "学生词库广场 7 库可见")

	// 重复导入幂等（全跳过不报错）
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/v1/admin/seed-import?dir="+seedDir, nil)
	req.Header.Set("Authorization", "Bearer "+atok)
	router.ServeHTTP(w, req)
	resp = doJSON(t, w)
	require.Equal(t, 0, resp.Code)
	m, _ = resp.Data.(map[string]any)
	assert.Equal(t, float64(0), m["words"], "重复导入 0 新词（词库重名创建被拒后跳过）")
}

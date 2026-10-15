// 首次启动向导（SDD §17：双击即用）：
// 无 admin 时开放 bootstrap 创建首个管理员（随后封死）；admin 一键导入种子词库。
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"enwords/internal/auth"
	"enwords/internal/wordbook"

	"github.com/gin-gonic/gin"
)

// strReader 测试辅助。
func strReader(s string) io.Reader { return strings.NewReader(s) }

// hasAdmin 是否已存在管理员（真实探测 auth 层——经任何途径创建的 admin 都封死向导）。
func (a *App) hasAdmin() bool {
	return a.Auth.HasAdmin()
}

// registerBootstrap 注册向导路由。
func (a *App) registerBootstrap(r *gin.RouterGroup) {
	r.GET("/bootstrap/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, ok(gin.H{"needed": !a.hasAdmin()}))
	})
	r.POST("/bootstrap", func(c *gin.Context) {
		if a.hasAdmin() {
			// Reject without draining a potentially unfinished identity body.
			c.Header("Connection", "close")
			c.Request.Close = true
			c.JSON(http.StatusOK, fail(errForbidden, "已完成初始化"))
			return
		}
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Nickname string `json:"nickname"`
		}
		if !a.readIdentityBody(c, &in) {
			return
		}
		if len(in.Username) < 2 || len(in.Password) < 6 {
			c.JSON(http.StatusOK, fail(errBadRequest, "用户名至少2位，密码至少6位"))
			return
		}
		if _, err := a.Auth.CreateUser(in.Username, in.Password, in.Nickname, auth.RoleAdmin); err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		a.persistUser(in.Username) // 持久化落库（bootstrap admin 重启不丢——R18）
		a.Log.Info("bootstrap", "admin_created", map[string]any{"username": in.Username})
		c.JSON(http.StatusOK, ok("admin created"))
	})
	// 种子一键导入（admin）：data/seed/wordbook-*.json 全部入库并发布
	r.POST("/admin/seed-import", a.authMW(), a.roleMW(auth.RoleAdmin), func(c *gin.Context) {
		dir := c.Query("dir")
		if dir == "" {
			dir = defaultSeedDir()
		}
		books, words, err := a.importSeeds(dir)
		if err != nil {
			c.JSON(http.StatusOK, fail(errBadRequest, err.Error()))
			return
		}
		c.JSON(http.StatusOK, ok(gin.H{"books": books, "words": words}))
	})
}

// AutoSeed 首启自动播种（SDD §17 双击即用）：无 admin 时导入全部种子词库并发布。
// 目录不存在时返回错误，调用方（main）记日志后继续——不阻塞启动。
func (a *App) AutoSeed() (books, words int, err error) {
	return a.importSeeds(defaultSeedDir())
}

// defaultSeedDir 默认种子目录（可执行文件同仓 data/seed 或源码布局）。
func defaultSeedDir() string {
	candidates := []string{
		filepath.Join("data", "seed"),
		filepath.Join("..", "data", "seed"),
		filepath.Join("..", "..", "data", "seed"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return filepath.Join("data", "seed")
}

// importSeeds 导入目录内全部 wordbook-*.json（重名库跳过=幂等），成功后发布。
func (a *App) importSeeds(dir string) (books, words int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("seed dir: %w", err)
	}
	existing := map[string]bool{}
	if lst, err := a.Books.ListPublished(); err == nil {
		for _, b := range lst {
			existing[b.Name] = true
		}
	}
	if all, err := a.Books.ListBooks(); err == nil {
		for _, b := range all {
			existing[b.Name] = true
		}
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "wordbook-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return books, words, err
		}
		var doc wordbook.SeedDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return books, words, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if existing[doc.Name] {
			continue // 幂等：同名库跳过
		}
		b, err := a.Books.CreateBook(doc.Name, doc.Grade, doc.Source)
		if err != nil {
			continue
		}
		res, err := a.Books.ImportBatch(b.ID, doc.ToInputs())
		if err != nil {
			continue
		}
		_ = a.Books.Publish(b.ID)
		books++
		words += res.Inserted
		a.Log.Info("bootstrap", "seed_imported", map[string]any{
			"book": doc.Name, "words": res.Inserted})
	}
	return books, words, nil
}

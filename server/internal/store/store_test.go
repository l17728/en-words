package store

// SQLite 持久化冒烟（真实文件、重启不丢——核心卖点验证）。
import (
	"path/filepath"
	"testing"

	"enwords/internal/wordbook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openTest(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(p)
	require.NoError(t, err)
	return db, p
}

func TestSQLitePersistence(t *testing.T) {
	s1, path := openTest(t)
	t.Cleanup(func() { // Windows 文件锁：测试尾必须关连接，否则 TempDir 清理失败
		if db, _ := s1.DB(); db != nil {
			_ = db.Close()
		}
	})

	// 写：建库+导词+发布
	repo1 := NewWordbookRepo(s1)
	b := &wordbook.Book{ID: repo1.NextBookID(), Name: "初一库", Grade: "g7", Status: wordbook.StatusDraft}
	require.NoError(t, repo1.CreateBook(b))
	e := &wordbook.Entry{ID: repo1.NextEntryID(), BookID: b.ID, Word: "Apple",
		NormKey: "apple", Meanings: []string{"n. 苹果", "n. 苹果树"}}
	require.NoError(t, repo1.CreateEntry(e))
	n, err := repo1.CountEntries(b.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// 关闭（模拟进程退出）
	sqlDB, _ := s1.DB()
	_ = sqlDB.Close()

	// 重开：数据仍在（重启不丢——核心断言）
	db2, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if d, _ := db2.DB(); d != nil {
			_ = d.Close()
		}
	})
	repo2 := NewWordbookRepo(db2)
	got, err := repo2.GetBook(b.ID)
	require.NoError(t, err, "重启后词库仍在")
	assert.Equal(t, "初一库", got.Name)
	assert.EqualValues(t, 1, got.WordCount)

	// normKey 去重索引：同库同词唯一约束
	_, err = repo2.EntryByKey(b.ID, "apple")
	require.NoError(t, err, "重启后词条仍在")
	dup := &wordbook.Entry{ID: repo2.NextEntryID(), BookID: b.ID, Word: "APPLE2", NormKey: "apple"}
	assert.Error(t, repo2.CreateEntry(dup), "norm_key 唯一约束生效")
}

// WAL 模式与单连接配置验证
func TestSQLitePragmas(t *testing.T) {
	s, _ := openTest(t)
	t.Cleanup(func() {
		if d, _ := s.DB(); d != nil {
			_ = d.Close()
		}
	})
	var mode string
	s.Raw("PRAGMA journal_mode").Scan(&mode)
	assert.Equal(t, "wal", mode, "WAL 模式")
	sqlDB, _ := s.DB()
	assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections, "单写者连接")
}

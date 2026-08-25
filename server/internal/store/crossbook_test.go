package store

// R6 BUG 锁定：跨库同词导入冲突（idx_book_norm 单列唯一真凶）。
import (
	"testing"

	"enwords/internal/wordbook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrossBookSameWordImport(t *testing.T) {
	db1, path := openTest(t)
	t.Cleanup(func() { d, _ := db1.DB(); _ = d.Close() })
	repo1 := NewWordbookRepo(db1)

	// 库 A：apple
	bA := &wordbook.Book{ID: repo1.NextBookID(), Name: "库A", Grade: "g7", Status: wordbook.StatusDraft}
	require.NoError(t, repo1.CreateBook(bA))
	eA := &wordbook.Entry{ID: repo1.NextEntryID(), BookID: bA.ID, Word: "Apple",
		NormKey: "apple", Meanings: []string{"n. 苹果"}}
	require.NoError(t, repo1.CreateEntry(eA))

	// 库 B：同词 apple——必须成功（跨库同词合法）
	bB := &wordbook.Book{ID: repo1.NextBookID(), Name: "库B", Grade: "g8", Status: wordbook.StatusDraft}
	require.NoError(t, repo1.CreateBook(bB))
	eB := &wordbook.Entry{ID: repo1.NextEntryID(), BookID: bB.ID, Word: "apple",
		NormKey: "apple", Meanings: []string{"n. 苹果"}}
	require.NoError(t, repo1.CreateEntry(eB), "跨库同词必须可导入（BUG 锁定）")

	// 同库同词仍拒绝（归一化重复）
	eDup := &wordbook.Entry{ID: repo1.NextEntryID(), BookID: bB.ID, Word: "APPLE2",
		NormKey: "apple"}
	assert.Error(t, repo1.CreateEntry(eDup), "同库重复仍拒绝")

	// 重启后复合索引仍在（迁移幂等重建）
	db2, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { d, _ := db2.DB(); _ = d.Close() })
	var idxSQL string
	db2.Raw("SELECT sql FROM sqlite_master WHERE name='idx_book_norm'").Scan(&idxSQL)
	assert.Contains(t, idxSQL, "book_id", "复合索引含 book_id 维度")
	assert.Contains(t, idxSQL, "norm_key", "复合索引含 norm_key 维度")
}

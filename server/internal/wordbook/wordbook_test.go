package wordbook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newService(t *testing.T) *Service {
	t.Helper()
	return NewService(NewMemRepo())
}

// ---------- 词库 CRUD（FR-B1）----------

func TestBookCRUD(t *testing.T) {
	s := newService(t)
	b, err := s.CreateBook("初一词库", "g7", "人教版七年级")
	require.NoError(t, err)
	assert.Equal(t, StatusDraft, b.Status)
	assert.NotZero(t, b.ID)

	got, err := s.GetBook(b.ID)
	require.NoError(t, err)
	assert.Equal(t, "初一词库", got.Name)

	got.Description = "updated"
	require.NoError(t, s.UpdateBook(got))
	got2, _ := s.GetBook(b.ID)
	assert.Equal(t, "updated", got2.Description)

	books, err := s.ListBooks()
	require.NoError(t, err)
	assert.Len(t, books, 1)

	require.NoError(t, s.DeleteBook(b.ID))
	_, err = s.GetBook(b.ID)
	assert.ErrorIs(t, err, ErrBookNotFound)
}

func TestBookValidation(t *testing.T) {
	s := newService(t)
	_, err := s.CreateBook("", "g7", "d")
	require.Error(t, err)
	_, err = s.CreateBook("x", "xx", "d")
	require.Error(t, err, "非法 grade 必须拒绝")
}

// ---------- 词条 CRUD（FR-B2）----------

func TestEntryCRUDAndValidation(t *testing.T) {
	s := newService(t)
	b, _ := s.CreateBook("test", "g7", "")

	e, err := s.AddEntry(b.ID, EntryInput{Word: "Apple", Phonetic: "/ˈæpl/", Meanings: []string{"n. 苹果"}})
	require.NoError(t, err)
	assert.Equal(t, "apple", e.NormKey, "NormKey 必须归一化")

	// 词重复(归一化后)拒绝（UNIQUE(book_id, word) 语义, B4）
	_, err = s.AddEntry(b.ID, EntryInput{Word: "APPLE", Meanings: []string{"n. 苹果"}})
	assert.ErrorIs(t, err, ErrDuplicateWord)

	// 释义缺失拒绝
	_, err = s.AddEntry(b.ID, EntryInput{Word: "egg", Meanings: []string{}})
	require.Error(t, err)
	// 释义超 2 条截断为 2
	e3, err := s.AddEntry(b.ID, EntryInput{Word: "egg", Meanings: []string{"n. 蛋", "n. 鸡蛋", "n. 卵"}})
	require.NoError(t, err)
	assert.Len(t, e3.Meanings, 2)

	// 更新
	e3.Meanings = []string{"n. 鸡蛋"}
	require.NoError(t, s.UpdateEntry(e3))
	got, _ := s.GetEntry(e3.ID)
	assert.Equal(t, []string{"n. 鸡蛋"}, got.Meanings)

	// 删除后可重新添加
	require.NoError(t, s.DeleteEntry(e3.ID))
	_, err = s.AddEntry(b.ID, EntryInput{Word: "egg", Meanings: []string{"n. 蛋"}})
	require.NoError(t, err)

	// 分页列表
	page, total, err := s.ListEntries(b.ID, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, page, 2)
}

// ---------- 批量导入与去重（FR-B3/B4）----------

func TestImportBatchDedup(t *testing.T) {
	s := newService(t)
	b, _ := s.CreateBook("test", "g8", "")
	res, err := s.ImportBatch(b.ID, []EntryInput{
		{Word: "apple", Meanings: []string{"n. 苹果"}},
		{Word: "banana", Meanings: []string{"n. 香蕉"}},
		{Word: "Apple", Meanings: []string{"n. 苹果(重复)"}}, // 归一化重复 → skip
		{Word: "", Meanings: []string{"x"}},                 // 空词 → skip
		{Word: "cherry", Meanings: nil},                     // 无释义 → skip
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Inserted)
	assert.Equal(t, []string{"Apple", "", "cherry"}, res.Skipped)

	cnt, _ := s.CountEntries(b.ID)
	assert.Equal(t, int64(2), cnt)
}

// ---------- 发布门（FR-B1/B5）----------

func TestPublishGate(t *testing.T) {
	s := newService(t)
	b, _ := s.CreateBook("test", "g9", "")

	// 空词库不可发布
	require.ErrorIs(t, s.Publish(b.ID), ErrBookEmpty)

	_, _ = s.AddEntry(b.ID, EntryInput{Word: "apple", Meanings: []string{"n. 苹果"}})
	require.NoError(t, s.Publish(b.ID))
	got, _ := s.GetBook(b.ID)
	assert.Equal(t, StatusPublished, got.Status)

	// 已发布不可再发布(幂等错误) / 可下架
	require.ErrorIs(t, s.Publish(b.ID), ErrInvalidStatus)
	require.NoError(t, s.Archive(b.ID))
	got, _ = s.GetBook(b.ID)
	assert.Equal(t, StatusArchived, got.Status)

	// 学生只看到 published
	b2, _ := s.CreateBook("t2", "g9", "")
	_, _ = s.AddEntry(b2.ID, EntryInput{Word: "bee", Meanings: []string{"n. 蜜蜂"}})
	require.NoError(t, s.Publish(b2.ID))
	pub, err := s.ListPublished()
	require.NoError(t, err)
	assert.Equal(t, int64(b2.ID), pub[0].ID, "draft 不对学生可见; archived 不可见")
	assert.Len(t, pub, 1)

	// 已发布词库改词 → ok（课堂快照不受影响的语义由 classroom 层负责）
	_, err = s.AddEntry(b2.ID, EntryInput{Word: "cat", Meanings: []string{"n. 猫"}})
	require.NoError(t, err)
}

// ---------- 真实种子导入（5568 词条验收）----------

func TestImportSeedRealFiles(t *testing.T) {
	seedDir := filepath.Join("..", "..", "..", "data", "seed")
	expect := map[string]int64{
		"wordbook-g7.json": 691, "wordbook-g8.json": 698, "wordbook-g9.json": 531,
		"wordbook-g10.json": 986, "wordbook-g11.json": 667, "wordbook-g12.json": 649,
		"wordbook-gaokao.json": 1346,
	}
	s := newService(t)
	var total int64
	for fn, want := range expect {
		raw, err := os.ReadFile(filepath.Join(seedDir, fn))
		require.NoError(t, err, "种子文件 %s 必须存在", fn)
		var doc SeedDoc
		require.NoError(t, json.Unmarshal(raw, &doc), fn)
		b, err := s.CreateBook(doc.Name, doc.Grade, doc.Source)
		require.NoError(t, err, fn)
		res, err := s.ImportBatch(b.ID, doc.ToInputs())
		require.NoError(t, err, fn)
		assert.Equal(t, want, int64(res.Inserted), "%s 导入数不符(重复:%v)", fn, res.Skipped)
		total += int64(res.Inserted)
		// 发布门：全部种子 0 缺释义 → 可发布
		require.NoError(t, s.Publish(b.ID), "%s 应可发布(0缺释义)", fn)
	}
	assert.Equal(t, int64(5568), total, "七库总词条必须等于 5568")

	// 跨库同名词允许（各库独立）; 库内无重复已由 Inserted==want 验证
	pub, _ := s.ListPublished()
	assert.Len(t, pub, 7)
}

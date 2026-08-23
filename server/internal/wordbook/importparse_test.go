package wordbook

// ImportParse/ExportCSV 测试（B3 Mock 解析 / TX-02 CSV 导出）。
import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportParse(t *testing.T) {
	s := newService(t)
	b, _ := s.CreateBook("t", "g7", "")

	entries, warnings := s.ImportParse(b.ID, "apple|n.|苹果\nbee n. 蜜蜂\n这一行没有英文词\n\ncherry|n.|樱桃")
	require.Len(t, entries, 3)
	assert.Equal(t, "apple", entries[0].Word)
	assert.Equal(t, []string{"n. 苹果"}, entries[0].Meanings)
	assert.Equal(t, "bee", entries[1].Word)
	assert.Equal(t, "cherry", entries[2].Word)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "没有英文词", "无法解析行进 warnings")

	// 解析结果可直接入库
	res, err := s.ImportBatch(b.ID, entries)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Inserted)
}

func TestExportCSV(t *testing.T) {
	s := newService(t)
	b, _ := s.CreateBook("t", "g8", "")
	_, _ = s.AddEntry(b.ID, EntryInput{Word: "apple", Phonetic: "/ˈæpl/", Meanings: []string{"n. 苹果", "n. 苹果树"}})
	_, _ = s.AddEntry(b.ID, EntryInput{Word: "has,comma", Meanings: []string{"n. 含逗号测试"}})

	csv, err := s.ExportCSV(b.ID)
	require.NoError(t, err)
	content := string(csv)
	assert.True(t, strings.HasPrefix(content, "\xEF\xBB\xBF"), "UTF-8 BOM（Excel 兼容）")
	assert.Contains(t, content, "apple,/ˈæpl/,n. 苹果；n. 苹果树", "多义项合并导出")
	assert.Contains(t, content, "\"has,comma\"", "含逗号字段加引号")

	// 坏库报错
	_, err = s.ExportCSV(999)
	assert.Error(t, err)
}
